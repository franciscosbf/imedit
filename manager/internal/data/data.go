package data

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"manager/ent"
	"manager/internal/conf"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-redis/redis/v8"
	"github.com/google/wire"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	amqp "github.com/rabbitmq/amqp091-go"

	_ "github.com/go-sql-driver/mysql"
)

// ProviderSet is data providers.
var ProviderSet = wire.NewSet(NewData, NewUserRepo)

func getSupportedDbDrivers() []string {
	return []string{
		"mysql",
	}
}

// Data .
type Data struct {
	edb *ent.Client
	rdb *redis.Client
	mdb *minio.Client
	rmq *amqp.Connection
}

// NewData .
func NewData(c *conf.Data) (*Data, func(), error) {
	driver := c.Database.Driver
	supportedDbDrivers := getSupportedDbDrivers()
	if !slices.Contains(supportedDbDrivers, driver) {
		return nil, nil, fmt.Errorf(
			"unsupported database driver %s, available drivers are: %v",
			driver, strings.Join(supportedDbDrivers, ","))
	}

	db, err := ent.Open(driver, c.Database.Source)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open %s database connection: %v", driver, err)
	}

	redisOpts := redis.Options{
		Addr:         c.Redis.Endpoint,
		Password:     c.Redis.Password,
		DialTimeout:  c.Redis.DialTimeout.AsDuration(),
		WriteTimeout: c.Redis.WriteTimeout.AsDuration(),
		ReadTimeout:  c.Redis.ReadTimeout.AsDuration(),
	}
	rdb := redis.NewClient(&redisOpts)
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		return nil, nil, fmt.Errorf("failed to ping Redis server: %v", err)
	}

	minioOpts := minio.Options{
		Creds: credentials.NewStaticV4(c.Minio.AccessKey, c.Minio.SecretKey, ""),
	}
	mdb, err := minio.New(c.Minio.Endpoint, &minioOpts)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open MinIO database connection: %v", err)
	}
	if _, err := mdb.GetCreds(); err != nil {
		return nil, nil, fmt.Errorf(
			"failed test connection to MinIO database by requesting credentials: %v", err)
	}

	rmq, err := amqp.Dial(c.Rabbitmq.Source)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to open RabbitMQ connection: %v", err)
	}

	data := &Data{db, rdb, mdb, rmq}

	cleanup := func() {
		log.Info("message", "closing the data resources")

		if err := data.edb.Close(); err != nil {
			log.Error(err)
		}

		if err := data.rdb.Close(); err != nil {
			log.Error(err)
		}

		if err := data.rmq.Close(); err != nil {
			log.Error(err)
		}
	}

	return data, cleanup, nil
}
