package v1

import (
	"bytes"
	"context"
	"fmt"

	"processor/internal/conf"
	"processor/internal/events"

	"github.com/cloudresty/go-rabbitmq"
	cmsgp "github.com/franciscosbf/imedit/common/pkg/msgp"
	crabbitmq "github.com/franciscosbf/imedit/common/pkg/rabbitmq"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/tinylib/msgp/msgp"
)

const (
	defaultPublisherBufferSize = 50
	defaultConsumerPrefetch    = 1
	defaultConcurrency         = 1
)

type ImageEventsServer interface {
	TransformImage(context.Context, *ImageTransformations) error
}

func RegisterImageEventsServer(c *conf.Server, es *events.Server, srv ImageEventsServer, logger log.Logger) error {
	log := log.NewHelper(logger)
	ctx := context.Background()

	if err := es.DeclareExchange(
		ctx, crabbitmq.TransformationsExchange, crabbitmq.TransformationsKind,
	); err != nil {
		return err
	}
	if err := es.DeclareExchange(
		ctx, crabbitmq.EventsExchange, crabbitmq.EventsKind,
	); err != nil {
		return err
	}

	var pubBufSz int
	if bufferSz := c.Publisher.BufferSize; bufferSz > 0 {
		pubBufSz = int(bufferSz)
	} else {
		pubBufSz = defaultPublisherBufferSize
	}

	var conPrefetch int
	if prefetch := c.Consumer.Prefetch; prefetch > 0 {
		conPrefetch = int(prefetch)
	} else {
		conPrefetch = defaultConsumerPrefetch
	}

	var concurrency int
	if c.Concurrency > 0 {
		concurrency = int(c.Concurrency)
	} else {
		concurrency = defaultConcurrency
	}

	for ; concurrency > 0; concurrency-- {
		qopts := events.QueueBindingOptions{
			Name:       "",
			Exchange:   crabbitmq.TransformationsExchange,
			RoutingKey: "1",
		}
		qopts.WithQueueOptions(rabbitmq.WithAutoDelete(), rabbitmq.WithClassicQueue())
		q, err := es.DeclareAndBindQueue(ctx, &qopts)
		if err != nil {
			return err
		}

		sopts := events.SenderOptions{BufferSize: pubBufSz}
		requester := es.RegisterSender(&sopts)

		copts := events.ConsumeEventsOptions{
			Queue:   q.Name,
			Handler: imageTransformationsHandler(srv, requester, log),
		}
		copts.WithConsumerOptions(
			rabbitmq.WithExclusiveConsumer(),
			rabbitmq.WithPrefetchCount(conPrefetch),
		)
		es.RegisterConsumer(&copts)
	}

	return nil
}

func imageTransformationsHandler(
	srv ImageEventsServer,
	requester *events.PublishingRequester,
	log *log.Helper,
) rabbitmq.MessageHandler {
	return func(ctx context.Context, delivery *rabbitmq.Delivery) error {
		defer func() {
			if err := delivery.Ack(); err != nil {
				log.Warnf("Failed to acknowledge transformation: %v", err)
			}
		}()

		var transformations cmsgp.Transformations
		tbuf := bytes.NewBuffer(delivery.Body)
		if err := msgp.Decode(tbuf, &transformations); err != nil {
			log.Warnf("Failed to decode transformation request: %v", err)

			return nil
		}

		req := ImageTransformations{
			Username: transformations.Username,
			ImageId:  transformations.ImageId,
		}
		if crop := transformations.Crop; crop != nil {
			req.Transformations.Crop = &CropImage{
				Width:  crop.Width,
				Height: crop.Height,
				X:      crop.X,
				Y:      crop.Y,
			}
		}
		if resize := transformations.Resize; resize != nil {
			req.Transformations.Resize = &ResizeImage{
				Width:  resize.Width,
				Height: resize.Height,
			}
		}
		if filter := transformations.Filter; filter != nil {
			req.Transformations.Filter = &FilterImage{
				Grayscale: filter.Grayscale,
				Sepia:     filter.Sepia,
			}
		}
		if rotate := transformations.Rotate; rotate != nil {
			req.Transformations.Rotate = rotate
		}
		if format := transformations.Format; format != nil {
			req.Transformations.Format = format
		}

		var epack cmsgp.EventPack
		if err := srv.TransformImage(ctx, &req); err != nil {
			epack.Event = &cmsgp.FailedImageTranformationEvent{
				TransformationId: transformations.TransformationId,
				ImageId:          transformations.ImageId,
				Reason: fmt.Sprintf(
					"failed to transform image %v with transformation id %s: %v",
					transformations.ImageId, transformations.TransformationId, err,
				),
			}
		} else {
			epack.Event = &cmsgp.TransformedImageEvent{
				TransformationId: transformations.TransformationId,
				ImageId:          transformations.ImageId,
			}
		}

		ebuf := bytes.Buffer{}
		if err := msgp.Encode(&ebuf, &epack); err != nil {
			log.Warnf("Image transformation was processed with success but event encoding failed: %v", err)

			return nil
		}

		publishing := events.Publishing{
			Exchange:   crabbitmq.EventsExchange,
			RoutingKey: crabbitmq.EventsRoutingKey(transformations.Username),
			Message: &rabbitmq.Message{
				ContentType: "application/octet-stream",
				Body:        ebuf.Bytes(),
			},
		}
		requester.Send(publishing)

		return nil
	}
}
