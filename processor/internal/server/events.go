package server

import (
	iv1 "processor/api/image/v1"
	"processor/internal/conf"
	"processor/internal/events"
	"processor/internal/service"

	"github.com/go-kratos/kratos/v2/log"
)

func NewConsumerServer(c *conf.Server, image *service.ImageService, logger log.Logger) (*events.Server, error) {
	es, err := events.NewServer(c)
	if err != nil {
		return nil, err
	}

	if err := iv1.RegisterImageEventsServer(c, es, image, logger); err != nil {
		return nil, err
	}

	return es, nil
}
