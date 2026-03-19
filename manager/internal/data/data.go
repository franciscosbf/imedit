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
	db  *ent.Client
	rdb *redis.Client
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
		return nil, nil, fmt.Errorf("failed to open database connection: %v", err)
	}

	redisOpts := redis.Options{
		Addr:         c.Redis.Addr,
		Password:     c.Redis.Password,
		DialTimeout:  c.Redis.DialTimeout.AsDuration(),
		WriteTimeout: c.Redis.WriteTimeout.AsDuration(),
		ReadTimeout:  c.Redis.ReadTimeout.AsDuration(),
	}
	rdb := redis.NewClient(&redisOpts)
	if err := rdb.Ping(context.Background()).Err(); err != nil {
		return nil, nil, fmt.Errorf("failed to ping Redis server: %v", err)
	}

	data := &Data{db, rdb}

	cleanup := func() {
		log.Info("message", "closing the data resources")

		if err := data.db.Close(); err != nil {
			log.Error(err)
		}

		if err := data.rdb.Close(); err != nil {
			log.Error(err)
		}
	}

	return data, cleanup, nil
}
