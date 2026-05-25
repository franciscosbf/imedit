package data

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"manager/ent"
	"manager/internal/conf"

	"github.com/cloudresty/go-rabbitmq"
	rmqpool "github.com/cloudresty/go-rabbitmq/pool"
	crabbitmq "github.com/franciscosbf/imedit/common/pkg/rabbitmq"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-redis/redis/v8"
	"github.com/google/wire"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"golang.org/x/sync/errgroup"

	_ "github.com/go-sql-driver/mysql"
)

// ProviderSet is data providers.
var ProviderSet = wire.NewSet(NewData, NewUserRepo, NewImageRepo)

func getSupportedDbDrivers() []string {
	return []string{
		"mysql",
	}
}

const (
	defaultCacheEviction       = time.Minute * 30
	defaultPubConcurrency      = 1
	defaultPublisherBufferSize = 50
)

type cache struct {
	eviction time.Duration
}

type publishing struct {
	exchange, routingKey string
	message              *rabbitmq.Message
	assurance            bool
	deliveryOpts         rabbitmq.DeliveryOptions
}

type publisherPool struct {
	mu            sync.Mutex
	curr          int
	publishingChs []chan<- *publishing
	stopCh        chan struct{}
	stopped       atomic.Bool
	wg            errgroup.Group
}

func (pp *publisherPool) getPublishingCh() chan<- *publishing {
	pp.mu.Lock()
	defer pp.mu.Unlock()

	publishingCh := pp.publishingChs[pp.curr]
	pp.curr = (pp.curr + 1) % len(pp.publishingChs)

	return publishingCh
}

func (pp *publisherPool) request(p *publishing) {
	if pp.stopped.Load() {
		return
	}

	publishingCh := pp.getPublishingCh()
	publishingCh <- p
}

func (pp *publisherPool) stop() bool {
	if !pp.stopped.CompareAndSwap(false, true) {
		return false
	}

	close(pp.stopCh)

	return true
}

func (pp *publisherPool) stopAndWait() error {
	if !pp.stop() {
		return nil
	}

	return pp.wg.Wait()
}

func newPublisherPool(rmq *rmqpool.ConnectionPool, config *conf.Data) (*publisherPool, error) {
	var pubConcurrency int
	if concurrency := config.Events.Publisher.Concurrency; concurrency > 0 {
		pubConcurrency = int(concurrency)
	} else {
		pubConcurrency = defaultPubConcurrency
	}

	var pubBufSz int
	if bufferSz := config.Events.Publisher.BufferSize; bufferSz > 0 {
		pubBufSz = int(bufferSz)
	} else {
		pubBufSz = defaultPublisherBufferSize
	}

	pp := &publisherPool{
		stopCh: make(chan struct{}),
	}

	type pubConf struct {
		publishingCh chan *publishing
		worker       func() error
	}
	pubConfs := []pubConf{}
	for concurrency := pubConcurrency; concurrency > 0; concurrency-- {

		client, err := rmq.Get()
		if err != nil {
			return nil, err
		}

		publisher, err := client.NewPublisher(rabbitmq.WithDeliveryAssurance())
		if err != nil {
			return nil, err
		}

		publishingCh := make(chan *publishing, pubBufSz)
		worker := func() error {
			defer func() { _ = publisher.Close() }()

			var (
				ctx context.Context
				p   *publishing
				ok  bool
				err error
			)

			for {
				select {
				case p = <-publishingCh:
				case <-pp.stopCh:
					if p, ok = <-publishingCh; !ok {
						return nil
					}
				}

				ctx = context.Background()

				if p.assurance {
					err = publisher.PublishWithDeliveryAssurance(
						ctx, p.exchange, p.routingKey, p.message, p.deliveryOpts,
					)
				} else {
					err = publisher.Publish(
						ctx, p.exchange, p.routingKey, p.message,
					)
				}

				if err != nil {
					pp.stop()

					return fmt.Errorf("failed to publish event: %v", err)
				}
			}
		}

		pubConfs = append(pubConfs, pubConf{publishingCh, worker})
	}

	for _, pubConf := range pubConfs {
		pp.wg.Go(pubConf.worker)

		pp.publishingChs = append(pp.publishingChs, pubConf.publishingCh)
	}

	return pp, nil
}

// Data .
type Data struct {
	edb *ent.Client
	rdb *redis.Client
	mdb *minio.Client
	rmq *rmqpool.ConnectionPool

	ch      cache
	pubPool *publisherPool

	config *conf.Data
}

func (d *Data) declareExchange(name, kind string, opts ...rabbitmq.ExchangeOption) error {
	rmqc, err := d.rmq.Get()
	if err != nil {
		return fmt.Errorf("failed to obtain RabbitMQ client for exchange declaration: %v", err)
	}

	admin := rmqc.Admin()
	if err := admin.DeclareExchange(
		context.Background(),
		name, rabbitmq.ExchangeType(kind), opts...,
	); err != nil {
		return fmt.Errorf(
			"failed to declare RabbitMQ exchange %s with kind %s: %v", name, kind, err,
		)
	}

	return nil
}

func (d *Data) withEntTx(ctx context.Context, fn func(tx *ent.Tx) error) error {
	tx, err := d.edb.Tx(ctx)
	if err != nil {
		return err
	}

	if err := fn(tx); err != nil {
		if rerr := tx.Rollback(); rerr != nil {
			err = fmt.Errorf("%w: rolling back transaction: %v", err, rerr)
		}
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("committing transaction: %w", err)
	}

	return nil
}

// NewData .
func NewData(config *conf.Data) (*Data, func(), error) {
	data := &Data{
		config: config,
	}
	var err error

	close := func() {
		if data.edb != nil {
			if err := data.edb.Close(); err != nil {
				log.Error(err)
			}
		}

		if data.rdb != nil {
			if err := data.rdb.Close(); err != nil {
				log.Error(err)
			}
		}

		if data.pubPool != nil {
			if err := data.pubPool.stopAndWait(); err != nil {
				log.Error(err)
			}
		}

		if data.rmq != nil {
			if err := data.rmq.Close(); err != nil {
				log.Error(err)
			}
		}
	}

	defer func() {
		if err != nil {
			close()
		}
	}()

	driver := config.Database.Driver
	supportedDbDrivers := getSupportedDbDrivers()
	if !slices.Contains(supportedDbDrivers, driver) {
		return nil, nil, fmt.Errorf(
			"unsupported database driver %s, available drivers are: %v",
			driver, strings.Join(supportedDbDrivers, ","),
		)
	}

	data.edb, err = ent.Open(driver, config.Database.Source)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open %s database connection: %v", driver, err)
	}

	redisOpts := redis.Options{
		Addr:     config.Redis.Endpoint,
		Password: config.Redis.Password,
	}
	if dialTimeout := config.Redis.DialTimeout; dialTimeout != nil {
		redisOpts.DialTimeout = dialTimeout.AsDuration()
	}
	if writeTimeout := config.Redis.WriteTimeout; writeTimeout != nil {
		redisOpts.WriteTimeout = writeTimeout.AsDuration()
	}
	if readTimeout := config.Redis.ReadTimeout; readTimeout != nil {
		redisOpts.ReadTimeout = readTimeout.AsDuration()
	}
	data.rdb = redis.NewClient(&redisOpts)
	if err = data.rdb.Ping(context.Background()).Err(); err != nil {
		return nil, nil, fmt.Errorf("failed to ping Redis server: %v", err)
	}

	minioOpts := minio.Options{
		Creds: credentials.NewStaticV4(config.Minio.AccessKey, config.Minio.SecretKey, ""),
	}
	data.mdb, err = minio.New(config.Minio.Endpoint, &minioOpts)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open MinIO database connection: %v", err)
	}
	if _, err := data.mdb.GetCreds(); err != nil {
		return nil, nil, fmt.Errorf(
			"failed test connection to MinIO database by requesting credentials: %v", err,
		)
	}

	data.rmq, err = rmqpool.New(int(config.Rabbitmq.Connections), rmqpool.WithClientOptions(
		rabbitmq.WithHosts(config.Rabbitmq.Endpoint),
		rabbitmq.WithCredentials(
			config.Rabbitmq.Username,
			config.Rabbitmq.Password,
		),
	))
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open RabbitMQ connections pool: %v", err)
	}

	if err = data.declareExchange(
		crabbitmq.EventsExchange, crabbitmq.EventsKind,
		rabbitmq.WithExchangeDurable(),
	); err != nil {
		return nil, nil, err
	}
	if err = data.declareExchange(
		crabbitmq.TransformationsExchange, crabbitmq.TransformationsKind,
		rabbitmq.WithExchangeDurable(),
	); err != nil {
		return nil, nil, err
	}

	data.pubPool, err = newPublisherPool(data.rmq, config)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to initialize RabbitmQ publishers pool: %v", err)
	}

	var cacheEviction time.Duration
	if evictionTime := config.Cache.EvictionTime; evictionTime != nil {
		cacheEviction = evictionTime.AsDuration()
	} else {
		cacheEviction = defaultCacheEviction
	}
	data.ch = cache{
		eviction: cacheEviction,
	}

	cleanup := func() {
		log.Info("message", "closing the data resources")

		close()
	}

	return data, cleanup, nil
}
