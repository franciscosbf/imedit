package test

import (
	"os"
	"testing"
	"time"

	"processor/internal/biz"
	"processor/internal/conf"
	"processor/internal/data"
	"processor/internal/events"
	"processor/internal/server"
	"processor/internal/service"

	ctest "github.com/franciscosbf/imedit/common/pkg/test"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
	"google.golang.org/protobuf/types/known/durationpb"
)

type IntegrationSuite struct {
	ctest.BaseIntegrationSuite

	config *conf.Bootstrap
	app    *events.Server
}

func (s *IntegrationSuite) setupAppConfig() {
	s.config = &conf.Bootstrap{
		Server: &conf.Server{
			Events: &conf.Server_Events{
				Endpoint:    s.CrmqEndpoint,
				Connections: 2,
				Username:    "user",
				Password:    "password",
			},
			Publisher: &conf.Server_Publisher{},
			Consumer:  &conf.Server_Consumer{},
		},
		Data: &conf.Data{
			Redis: &conf.Data_Redis{
				Endpoint:     s.CrdbEndpoint,
				Password:     "password",
				DialTimeout:  durationpb.New(4 * time.Second),
				ReadTimeout:  durationpb.New(2 * time.Second),
				WriteTimeout: durationpb.New(2 * time.Second),
			},
			Minio: &conf.Data_MinIO{
				Endpoint:  s.CmioEndpoint,
				AccessKey: "user",
				SecretKey: "password",
			},
		},
	}
}

func (s *IntegrationSuite) setupRedisConnection() {
	s.SetupRedisConnection(s.config.Data.Redis.Endpoint, s.config.Data.Redis.Password)
}

func (s *IntegrationSuite) setupMinIOConnection() {
	s.SetupMinIOConnection(
		s.config.Data.Minio.Endpoint,
		s.config.Data.Minio.AccessKey,
		s.config.Data.Minio.SecretKey,
	)
}

func (s *IntegrationSuite) setupRabbitMQConnection() {
	s.SetupRabbitMQConnection(
		s.config.Server.Events.Endpoint,
		s.config.Server.Events.Username,
		s.config.Server.Events.Password,
	)
}

func (s *IntegrationSuite) setupAppAndRun() {
	var err error

	logger := log.NewStdLogger(os.Stdout)
	ddata, _, err := data.NewData(s.config.Data)
	assert.NoError(s.T(), err, "failed to create data")
	irepo := data.NewImageRepo(ddata, logger)
	iuc := biz.NewImageUsecase(irepo, logger)
	image := service.NewImageService(iuc, logger)
	server, err := server.NewConsumerServer(s.config.Server, image, logger)
	assert.NoError(s.T(), err, "failed to create consumer server")
	s.app = server

	go func() { _ = s.app.Run() }()
}

func (s *IntegrationSuite) teardownApp() {
	assert.NoError(s.T(), s.app.Stop(), "failed to stop app")
}

func (s *IntegrationSuite) SetupSuite() {
	s.RunReddisContainer()
	s.RunMinIOContainer()
	s.RunRabbitMQContainer()

	s.setupAppConfig()

	s.setupRedisConnection()
	s.setupMinIOConnection()
	s.setupRabbitMQConnection()

	s.SetupMinIODatabase()

	s.setupAppAndRun()
}

func (s *IntegrationSuite) TeardownSuite() {
	s.teardownApp()

	s.TeardownConnections()

	s.TeardownContainers()
}

func (s *IntegrationSuite) AfterTest(_, _ string) {
	s.FlushDatabases()
}

func TestIntegrationSuite(t *testing.T) {
	suite.Run(t, new(IntegrationSuite))
}
