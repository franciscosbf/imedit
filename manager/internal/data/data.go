package data

import (
	"context"
	"fmt"
	"slices"
	"strings"
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

	_ "github.com/go-sql-driver/mysql"
)

// ProviderSet is data providers.
var ProviderSet = wire.NewSet(NewData, NewUserRepo, NewImageRepo)

func getSupportedDbDrivers() []string {
	return []string{
		"mysql",
	}
}

const defaultCacheEviction = time.Minute * 30

type cache struct {
	eviction time.Duration
}

// Data .
type Data struct {
	edb *ent.Client
	rdb *redis.Client
	mdb *minio.Client
	rmq *rmqpool.ConnectionPool

	ch cache

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
			"failed to declare exchange %s with kind %s: %v", name, kind, err,
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
	data := &Data{config: config}
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
		Addr:         config.Redis.Endpoint,
		Password:     config.Redis.Password,
		DialTimeout:  config.Redis.DialTimeout.AsDuration(),
		WriteTimeout: config.Redis.WriteTimeout.AsDuration(),
		ReadTimeout:  config.Redis.ReadTimeout.AsDuration(),
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
