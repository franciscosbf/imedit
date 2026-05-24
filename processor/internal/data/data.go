package data

import (
	"context"
	"fmt"

	"processor/internal/conf"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-redis/redis/v8"
	"github.com/google/wire"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
)

// ProviderSet is data providers.
var ProviderSet = wire.NewSet(NewData, NewImageRepo)

// Data .
type Data struct {
	rdb *redis.Client
	mdb *minio.Client

	config *conf.Data
}

// NewData .
func NewData(config *conf.Data) (*Data, func(), error) {
	data := &Data{config: config}
	var err error

	close := func() {
		if data.rdb != nil {
			if err := data.rdb.Close(); err != nil {
				log.Error(err)
			}
		}
	}

	defer func() {
		if err != nil {
			close()
		}
	}()

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

	cleanup := func() {
		log.Info("message", "closing the data resources")

		close()
	}

	return data, cleanup, nil
}
