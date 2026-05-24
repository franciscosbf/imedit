package events

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"sync"
	"sync/atomic"
	"syscall"

	"processor/internal/conf"

	"github.com/cloudresty/go-rabbitmq"
	rmqpool "github.com/cloudresty/go-rabbitmq/pool"
	"golang.org/x/sync/errgroup"
)

type ConsumeEventsOptions struct {
	Queue   string
	Handler rabbitmq.MessageHandler

	consumerOptions []rabbitmq.ConsumerOption
	consumeOptions  []rabbitmq.ConsumeOption
}

func (o *ConsumeEventsOptions) WithConsumerOptions(opts ...rabbitmq.ConsumerOption) {
	o.consumerOptions = opts
}

func (o *ConsumeEventsOptions) WithConsumeOptions(opts ...rabbitmq.ConsumeOption) {
	o.consumeOptions = opts
}

type SenderOptions struct {
	BufferSize int

	publisherOptions []rabbitmq.PublisherOption
}

type Publishing struct {
	Exchange, RoutingKey string
	Message              *rabbitmq.Message
}

type PublishingRequester struct {
	closed        atomic.Bool
	publishingsCh chan Publishing
}

func (pr *PublishingRequester) close() {
	pr.closed.Store(true)
}

func (pr *PublishingRequester) isClosed() bool {
	return pr.closed.Load()
}

func (pr *PublishingRequester) Send(publishing Publishing) {
	if pr.isClosed() {
		return
	}

	pr.publishingsCh <- publishing
}

type consumerConfigs struct {
	opts *ConsumeEventsOptions
}

type senderConfigs struct {
	requester *PublishingRequester
	stopCh    chan struct{}
	opts      *SenderOptions
}

func (o *SenderOptions) WithPublisherOptions(opts ...rabbitmq.PublisherOption) {
	o.publisherOptions = opts
}

type QueueBindingOptions struct {
	Name, Exchange, RoutingKey string

	queueOptions   []rabbitmq.QueueOption
	bindingOptions []rabbitmq.BindingOption
}

func (o *QueueBindingOptions) WithQueueOptions(opts ...rabbitmq.QueueOption) {
	o.queueOptions = opts
}

func (o *QueueBindingOptions) WithBindingOptions(opts ...rabbitmq.BindingOption) {
	o.bindingOptions = opts
}

type Server struct {
	rmq       *rmqpool.ConnectionPool
	cconfs    []consumerConfigs
	sconfs    []senderConfigs
	cmu       sync.Mutex
	ctx       context.Context
	cancelCtx func()
	stopWait  chan error
}

func (s *Server) getClient() (*rabbitmq.Client, error) {
	rmqc, err := s.rmq.Get()
	if err != nil {
		return nil, fmt.Errorf("failed to obtain RabbitMQ client for consumer: %v", err)
	}

	return rmqc, nil
}

func (s *Server) consumeEvents(ctx context.Context, confs consumerConfigs) error {
	rmqc, err := s.getClient()
	if err != nil {
		return err
	}

	opts := confs.opts

	consumer, err := rmqc.NewConsumer(opts.consumerOptions...)
	if err != nil {
		return fmt.Errorf("failed to initialize RabbitMQ consumer: %v", err)
	}

	if err := consumer.Consume(
		ctx, opts.Queue, opts.Handler, opts.consumeOptions...,
	); err != nil && err != context.Canceled {
		return fmt.Errorf("RabbitMQ consumer failed: %v", err)
	}

	return nil
}

func (s *Server) sendEvents(
	ctx context.Context,
	sconfs *senderConfigs,
) error {
	rmqc, err := s.getClient()
	if err != nil {
		return err
	}

	publisher, err := rmqc.NewPublisher(sconfs.opts.publisherOptions...)
	if err != nil {
		return fmt.Errorf("failed to initialize RabbitMQ publisher: %v", err)
	}
	defer func() { _ = publisher.Close() }()

	var (
		publishing Publishing
		ok         bool
	)
	requester := sconfs.requester
	for {
		select {
		case <-sconfs.stopCh:
			requester.close()
			if publishing, ok = <-requester.publishingsCh; !ok {
				return nil
			}
		case publishing = <-requester.publishingsCh:
		}

		if err := publisher.Publish(
			ctx, publishing.Exchange, publishing.RoutingKey, publishing.Message,
		); err != nil {
			return fmt.Errorf("failed to publish RabbitMQ message: %v", err)
		}
	}
}

func (s *Server) close() error {
	return s.rmq.Close()
}

func (s *Server) DeclareExchange(
	ctx context.Context,
	name, kind string,
	opts ...rabbitmq.ExchangeOption,
) error {
	rmqc, err := s.getClient()
	if err != nil {
		return err
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

func (s *Server) DeclareAndBindQueue(ctx context.Context, opts *QueueBindingOptions) (*rabbitmq.Queue, error) {
	rmqc, err := s.getClient()
	if err != nil {
		return nil, err
	}

	admin := rmqc.Admin()

	q, err := admin.DeclareQueue(ctx, opts.Name, opts.queueOptions...)
	if err != nil {
		return nil, fmt.Errorf("failed to declare queue %s: %v", opts.Name, err)
	}

	if err := admin.BindQueue(
		ctx, q.Name, opts.Exchange, opts.RoutingKey, opts.bindingOptions...,
	); err != nil {
		return nil, fmt.Errorf(
			"failed to bind queue %s to exchange %s: %v",
			q.Name, opts.Exchange, err,
		)
	}

	return q, nil
}

func (s *Server) RegisterConsumer(opts *ConsumeEventsOptions) {
	cconfs := consumerConfigs{
		opts: opts,
	}

	s.cconfs = append(s.cconfs, cconfs)
}

func (s *Server) RegisterSender(opts *SenderOptions) *PublishingRequester {
	requester := &PublishingRequester{
		publishingsCh: make(chan Publishing, opts.BufferSize),
	}
	sconfs := senderConfigs{
		requester: requester,
		stopCh:    make(chan struct{}),
		opts:      opts,
	}

	s.sconfs = append(s.sconfs, sconfs)

	return requester
}

func (s *Server) Run() error {
	s.cmu.Lock()
	sctx := s.ctx
	s.cmu.Unlock()
	if sctx != nil {
		return errors.New("server is already running")
	}

	if len(s.cconfs) == 0 && len(s.sconfs) == 0 {
		return errors.New("there is no registered RabbitMQ consumer or sender")
	}

	s.ctx, s.cancelCtx = context.WithCancel(context.Background())
	eg, ctx := errgroup.WithContext(s.ctx)

	for _, cconfs := range s.cconfs {
		confs := cconfs
		eg.Go(func() error {
			return s.consumeEvents(ctx, confs)
		})
	}

	var swg sync.WaitGroup
	for _, sconfs := range s.sconfs {
		swg.Add(1)

		confs := sconfs
		eg.Go(func() error {
			defer swg.Done()

			return s.sendEvents(ctx, &confs)
		})
	}

	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGHUP, syscall.SIGINT, syscall.SIGTERM, syscall.SIGQUIT)
	eg.Go(func() error {
		select {
		case <-ctx.Done():
		case <-sigs:
		}

		for _, sconfs := range s.sconfs {
			close(sconfs.stopCh)
		}
		swg.Wait()

		return s.close()
	})

	if err := eg.Wait(); err != context.Canceled {
		s.stopWait <- err

		return err
	}

	s.stopWait <- nil

	return nil
}

func (s *Server) cancelContext() bool {
	s.cmu.Lock()
	defer s.cmu.Unlock()

	if s.ctx == nil {
		return false
	}

	ok := true
	select {
	case _, ok = <-s.ctx.Done():
	default:
	}

	if ok {
		s.cancelCtx()
	}

	return true
}

func (s *Server) Stop() error {
	if !s.cancelContext() {
		return nil
	}

	return <-s.stopWait
}

func NewServer(c *conf.Server) (*Server, error) {
	rmq, err := rmqpool.New(int(c.Events.Connections), rmqpool.WithClientOptions(
		rabbitmq.WithHosts(c.Events.Endpoint),
		rabbitmq.WithCredentials(
			c.Events.Username,
			c.Events.Password,
		),
	))
	if err != nil {
		return nil, fmt.Errorf("failed to open RabbitMQ connections pool: %v", err)
	}

	return &Server{
		rmq:      rmq,
		stopWait: make(chan error, 1),
	}, nil
}
