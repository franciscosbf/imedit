package test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	pb "manager/api/user/v1"
	"manager/ent"
	"manager/internal/auth"
	"manager/internal/biz"
	"manager/internal/conf"
	"manager/internal/data"
	"manager/internal/server"
	"manager/internal/service"

	"github.com/docker/go-connections/nat"
	"github.com/go-kratos/kratos/v2"
	"github.com/go-redis/redis/v8"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"google.golang.org/protobuf/types/known/durationpb"

	"github.com/go-kratos/kratos/v2/log"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
)

type testUser struct {
	username string
	password string
}

type IntegrationSuite struct {
	suite.Suite
	cdb          *testcontainers.DockerContainer
	crdb         *testcontainers.DockerContainer
	cmio         *testcontainers.DockerContainer
	crmq         *testcontainers.DockerContainer
	crdbEndpoint string
	cdbEndpoint  string
	cmioEndpoint string
	crmqEndpoint string
	config       *conf.Bootstrap
	app          *kratos.App
	edb          *ent.Client
	rdb          *redis.Client
	mdb          *minio.Client
	rmq          *amqp.Connection
	jwtAuth      auth.JwtAuthenticator
	pwdGen       auth.PasswordGenerator
	appEndpoint  string
	client       *khttp.Client
}

func (s *IntegrationSuite) sendJsonRequest(method, path string, args, reply any, opts ...khttp.CallOption) error {
	return s.client.Invoke(context.Background(), method, path, args, reply, opts...)
}

func (s *IntegrationSuite) sendRawRequest(
	method, path string,
	query url.Values,
	header http.Header,
	body io.Reader,
) (*http.Response, error) {
	u := url.URL{
		Scheme: "http",
		Host:   s.appEndpoint,
		Path:   path,
	}
	q := u.Query()
	for k, vs := range query {
		for _, v := range vs {
			q.Add(k, v)
		}
	}
	u.RawQuery = q.Encode()
	req, err := http.NewRequest(method, u.String(), body)
	assert.NoError(s.T(), err, "failed to build request")

	for k, vs := range header {
		for _, v := range vs {
			req.Header.Add(k, v)
		}
	}

	return s.client.Do(req)
}

func (s *IntegrationSuite) registerUser(tu *testUser) {
	request := pb.RegisterUserRequest{
		Username: tu.username,
		Password: tu.password,
	}
	response := pb.RegisterUserReply{}

	assert.NoError(
		s.T(),
		s.sendJsonRequest("POST", "/v1/user/register", &request, &response),
	)
}

func (s *IntegrationSuite) loginUser(tu *testUser) string {
	request := pb.LoginUserRequest{
		Username: tu.username,
		Password: tu.password,
	}
	response := pb.LoginUserReply{}

	assert.NoError(
		s.T(),
		s.sendJsonRequest("POST", "/v1/user/login", &request, &response),
	)

	return "Bearer " + response.Token
}

func (s *IntegrationSuite) runReddisContainer() {
	var err error

	s.crdb, err = testcontainers.Run(
		context.Background(), "redis:8.6.1",
		testcontainers.WithCmd("redis-server", "--requirepass", "password"),
		testcontainers.WithExposedPorts("6379/tcp"),
		testcontainers.WithWaitStrategy(
			wait.ForListeningPort("6379/tcp"),
			wait.ForLog("Ready to accept connections"),
		),
	)
	assert.NoError(s.T(), err, "failed to launch Redis container")
	s.crdbEndpoint, err = s.crdb.Endpoint(context.Background(), "")
	assert.NoError(s.T(), err, "failed to retrieve Redis container endpoint")
}

func (s *IntegrationSuite) runMySQLContainer() {
	var err error

	s.cdb, err = testcontainers.Run(
		context.Background(), "mysql:8.4.8",
		testcontainers.WithEnv(map[string]string{
			"MYSQL_ROOT_PASSWORD": "password",
			"MYSQL_DATABASE":      "test",
		}),
		testcontainers.WithExposedPorts("3306/tcp"),
		testcontainers.WithWaitStrategy(
			wait.ForListeningPort("3306/tcp"),
			wait.ForLog("mysqld: ready for connections"),
		),
	)
	assert.NoError(s.T(), err, "failed to launch MySQL database container")
	s.cdbEndpoint, err = s.cdb.Endpoint(context.Background(), "")
	assert.NoError(s.T(), err, "failed to retrieve MySQL container endpoint")
}

func (s *IntegrationSuite) runMinIOContainer() {
	var (
		license string
		err     error
	)

	license, err = filepath.Abs("./minio/minio.license/")
	assert.NoError(s.T(), err, "failed to obtain absolute path for ./minio/minio.license")
	s.cmio, err = testcontainers.Run(
		context.Background(), "quay.io/minio/aistor/minio:RELEASE.2026-03-26T21-24-40Z",
		testcontainers.WithCmd("minio", "server", "/mnt/data", "--license", "/minio.license"),
		testcontainers.WithEnv(map[string]string{
			"MINIO_ROOT_USER":     "user",
			"MINIO_ROOT_PASSWORD": "password",
		}),
		testcontainers.WithFiles(testcontainers.ContainerFile{
			HostFilePath:      license,
			ContainerFilePath: "/minio.license",
		}),
		testcontainers.WithExposedPorts("9000/tcp"),
		testcontainers.WithWaitStrategy(
			wait.ForListeningPort("9000/tcp"),
			wait.ForLog("MinIO AIStor Server"),
		),
	)
	assert.NoError(s.T(), err, "failed to launch MinIO AIStor database container")
	s.cmioEndpoint, err = s.cmio.Endpoint(context.Background(), "")
	assert.NoError(s.T(), err, "failed to retrieve MinIO AIStor database container endpoint")
}

func (s *IntegrationSuite) runRabbitMQContainer() {
	var err error

	s.crmq, err = testcontainers.Run(
		context.Background(), "rabbitmq:4.2.5-management",
		testcontainers.WithEnv(map[string]string{
			"RABBITMQ_DEFAULT_USER": "user",
			"RABBITMQ_DEFAULT_PASS": "password",
		}),
		testcontainers.WithExposedPorts("5672/tcp"),
		testcontainers.WithWaitStrategy(
			wait.ForListeningPort("5672/tcp"),
			wait.ForLog("Time to start RabbitMQ"),
		),
	)
	assert.NoError(s.T(), err, "failed to launch RabbitMQ container")
	s.crmqEndpoint, err = s.crmq.PortEndpoint(context.Background(), nat.Port("5672"), "")
	assert.NoError(s.T(), err, "failed to retrieve RabbitMQ container endpoint")
}

func (s *IntegrationSuite) teardownContainers() {
	if s.crdb != nil {
		testcontainers.CleanupContainer(s.T(), s.crdb)
	}
	if s.cdb != nil {
		testcontainers.CleanupContainer(s.T(), s.cdb)
	}
	if s.cmio != nil {
		testcontainers.CleanupContainer(s.T(), s.cmio)
	}
	if s.crmq != nil {
		testcontainers.CleanupContainer(s.T(), s.crmq)
	}
}

func (s *IntegrationSuite) setupAppConfig() {
	s.config = &conf.Bootstrap{
		Server: &conf.Server{Http: &conf.Server_HTTP{
			Endpoint:       "0.0.0.0:0",
			RequestTimeout: durationpb.New(5 * time.Second),
		}},
		Auth: &conf.Auth{
			Algorithm:      "ES256",
			PublicKeyFile:  "./cert/ec256-public.pem",
			PrivateKeyFile: "./cert/ec256-private.pem",
			Issuer:         "tester",
			ExpirationTime: durationpb.New(60 * time.Second),
		},
		Data: &conf.Data{
			Database: &conf.Data_Database{
				Driver: "mysql",
				Source: fmt.Sprintf("root:password@tcp(%s)/test", s.cdbEndpoint),
			},
			Redis: &conf.Data_Redis{
				Endpoint:     s.crdbEndpoint,
				Password:     "password",
				DialTimeout:  durationpb.New(4 * time.Second),
				ReadTimeout:  durationpb.New(2 * time.Second),
				WriteTimeout: durationpb.New(2 * time.Second),
			},
			Minio: &conf.Data_MinIO{
				Endpoint:  s.cmioEndpoint,
				AccessKey: "user",
				SecretKey: "password",
			},
			Rabbitmq: &conf.Data_RabbitMQ{
				Source: fmt.Sprintf("amqp://user:password@%s/", s.crmqEndpoint),
			},
		},
	}
}

func (s *IntegrationSuite) setupAppAndRun() {
	var err error

	s.jwtAuth, err = auth.NewJwtAuthenticator(s.config.Auth)
	assert.NoError(s.T(), err, "failed to create JWT authenticator")
	s.pwdGen = auth.NewPasswordGenerator()
	logger := log.NewStdLogger(os.Stdout)
	ddata, _, err := data.NewData(s.config.Data)
	assert.NoError(s.T(), err, "failed to create data")
	repo := data.NewUserRepo(ddata, logger)
	uc := biz.NewUserUsecase(s.jwtAuth, s.pwdGen, repo)
	user := service.NewUserService(uc, logger)
	image := service.NewImageService(logger)
	server := server.NewHTTPServer(s.config.Server, s.jwtAuth, user, image, logger)
	s.app = kratos.New(kratos.Server(server))

	u, err := server.Endpoint()
	assert.NoError(s.T(), err, "failed to retrieve http server endpoint")
	s.appEndpoint = u.Host

	go func() { _ = s.app.Run() }()
}

func (s *IntegrationSuite) teardownApp() {
	assert.NoError(s.T(), s.app.Stop(), "failed to stop app")
}

func (s *IntegrationSuite) setupMySQLConnection() {
	var err error

	s.edb, err = ent.Open(s.config.Data.Database.Driver, s.config.Data.Database.Source)
	assert.NoError(s.T(), err, "failed to open MySQl database connection")

	assert.NoError(s.T(), s.edb.Schema.Create(context.Background()),
		"failed to create schema for %v database")
}

func (s *IntegrationSuite) setupRedisConnection() {
	s.rdb = redis.NewClient(&redis.Options{
		Addr:     s.config.Data.Redis.Endpoint,
		Password: s.config.Data.Redis.Password,
	})
}

func (s *IntegrationSuite) setupMinIOConnection() {
	var err error

	s.mdb, err = minio.New(s.config.Data.Minio.Endpoint, &minio.Options{
		Creds: credentials.NewStaticV4(s.config.Data.Minio.AccessKey, s.config.Data.Minio.SecretKey, ""),
	})
	assert.NoError(s.T(), err, "failed to open MinIO AIStor database connection")
}

func (s *IntegrationSuite) setupRabbitMQConnection() {
	var err error

	s.rmq, err = amqp.Dial(s.config.Data.Rabbitmq.Source)
	assert.NoError(s.T(), err, "failed to open RabbitMQ connection")
}

func (s *IntegrationSuite) setupAppConnection() {
	var err error

	s.client, err = khttp.NewClient(
		context.Background(),
		khttp.WithEndpoint(s.appEndpoint))
	assert.NoError(s.T(), err, "failed to create http client")
}

func (s *IntegrationSuite) teardownConnections() {
	assert.NoError(s.T(), s.rdb.Close(), "failed to close Redis client")
	assert.NoError(s.T(), s.edb.Close(), "failed to close database client")
	assert.NoError(s.T(), s.rmq.Close(), "failed to close RabbitMQ client")
	assert.NoError(s.T(), s.client.Close(), "failed to close app client")
}

func (s *IntegrationSuite) SetupSuite() {
	go func() {
		if !s.T().Failed() {
			return
		}

		s.teardownContainers()
	}()

	s.runReddisContainer()
	s.runMySQLContainer()
	s.runMinIOContainer()
	s.runRabbitMQContainer()

	s.setupAppConfig()
	s.setupAppAndRun()

	s.setupMySQLConnection()
	s.setupRedisConnection()
	s.setupMinIOConnection()
	s.setupRabbitMQConnection()
	s.setupAppConnection()
}

func (s *IntegrationSuite) TeardownSuite() {
	s.teardownApp()

	s.teardownConnections()

	s.teardownContainers()
}

func (s *IntegrationSuite) AfterTest(_, _ string) {
	_, err := s.edb.User.Delete().Exec(context.Background())
	assert.NoError(s.T(), err, "failed to delete users")

	// TODO: delete everything about images
}

func TestIntegrationSuite(t *testing.T) {
	suite.Run(t, new(IntegrationSuite))
}
