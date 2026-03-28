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
	cdb      *testcontainers.DockerContainer
	crdb     *testcontainers.DockerContainer
	cmio     *testcontainers.DockerContainer
	crmq     *testcontainers.DockerContainer
	app      *kratos.App
	edb      *ent.Client
	rdb      *redis.Client
	mdb      *minio.Client
	rmq      *amqp.Connection
	jwtAuth  auth.JwtAuthenticator
	pwdGen   auth.PasswordGenerator
	endpoint string
	client   *khttp.Client
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
		Host:   s.endpoint,
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

func (s *IntegrationSuite) runReddisContainer() (container *testcontainers.DockerContainer, endpoint string) {
	var err error

	container, err = testcontainers.Run(
		context.Background(), "redis:8.6.1",
		testcontainers.WithCmd("redis-server", "--requirepass", "password"),
		testcontainers.WithExposedPorts("6379/tcp"),
		testcontainers.WithWaitStrategy(
			wait.ForListeningPort("6379/tcp"),
			wait.ForLog("Ready to accept connections"),
		),
	)
	assert.NoError(s.T(), err, "failed to launch Redis container")
	endpoint, err = container.Endpoint(context.Background(), "")
	assert.NoError(s.T(), err, "failed to retrieve Redis container endpoint")

	return
}

func (s *IntegrationSuite) runMySQLContainer() (container *testcontainers.DockerContainer, endpoint string) {
	var err error

	container, err = testcontainers.Run(
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
	endpoint, err = container.Endpoint(context.Background(), "")
	assert.NoError(s.T(), err, "failed to retrieve MySQL container endpoint")

	return
}

func (s *IntegrationSuite) runMinIOContainer() (container *testcontainers.DockerContainer, endpoint string) {
	var (
		license string
		err     error
	)

	license, err = filepath.Abs("./minio/minio.license/")
	assert.NoError(s.T(), err, "failed to obtain absolute path for ./minio/minio.license")
	container, err = testcontainers.Run(
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
	endpoint, err = container.Endpoint(context.Background(), "")
	assert.NoError(s.T(), err, "failed to retrieve MinIO AIStor database container endpoint")

	return
}

func (s *IntegrationSuite) runRabbitMQContainer() (container *testcontainers.DockerContainer, endpoint string) {
	var err error

	container, err = testcontainers.Run(
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
	endpoint, err = container.PortEndpoint(context.Background(), nat.Port("5672"), "")
	assert.NoError(s.T(), err, "failed to retrieve RabbitMQ container endpoint")

	return
}

func (s *IntegrationSuite) SetupSuite() {
	var (
		crdb         *testcontainers.DockerContainer
		crdbEndpoint string
		cdb          *testcontainers.DockerContainer
		cdbEndpoint  string
		cmio         *testcontainers.DockerContainer
		cmioEndpoint string
		crmq         *testcontainers.DockerContainer
		crmqEndpoint string
		jwtAuth      auth.JwtAuthenticator
		pwdGen       auth.PasswordGenerator
	)

	go func() {
		if !s.T().Failed() {
			return
		}

		if crdb != nil {
			testcontainers.CleanupContainer(s.T(), crdb)
		}
		if cdb != nil {
			testcontainers.CleanupContainer(s.T(), cdb)
		}
		if cmio != nil {
			testcontainers.CleanupContainer(s.T(), cmio)
		}
		if crmq != nil {
			testcontainers.CleanupContainer(s.T(), crmq)
		}
	}()

	crdb, crdbEndpoint = s.runReddisContainer()
	cdb, cdbEndpoint = s.runMySQLContainer()
	cmio, cmioEndpoint = s.runMinIOContainer()
	crmq, crmqEndpoint = s.runRabbitMQContainer()

	config := conf.Bootstrap{
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
				Source: fmt.Sprintf("root:password@tcp(%s)/test", cdbEndpoint),
			},
			Redis: &conf.Data_Redis{
				Endpoint:     crdbEndpoint,
				Password:     "password",
				DialTimeout:  durationpb.New(4 * time.Second),
				ReadTimeout:  durationpb.New(2 * time.Second),
				WriteTimeout: durationpb.New(2 * time.Second),
			},
			Minio: &conf.Data_MinIO{
				Endpoint:  cmioEndpoint,
				AccessKey: "user",
				SecretKey: "password",
			},
			Rabbitmq: &conf.Data_RabbitMQ{
				Source: fmt.Sprintf("amqp://user:password@%s/", crmqEndpoint),
			},
		},
	}

	jwtAuth, err := auth.NewJwtAuthenticator(config.Auth)
	assert.NoError(s.T(), err, "failed to create JWT authenticator")
	pwdGen = auth.NewPasswordGenerator()
	logger := log.NewStdLogger(os.Stdout)
	ddata, _, err := data.NewData(config.Data)
	assert.NoError(s.T(), err, "failed to create data")
	repo := data.NewUserRepo(ddata, logger)
	uc := biz.NewUserUsecase(jwtAuth, pwdGen, repo)
	user := service.NewUserService(uc, logger)
	image := service.NewImageService(logger)
	server := server.NewHTTPServer(config.Server, jwtAuth, user, image, logger)
	app := kratos.New(kratos.Server(server))

	edb, err := ent.Open(config.Data.Database.Driver, config.Data.Database.Source)
	assert.NoError(s.T(), err, "failed to open MySQl database connection")

	assert.NoError(s.T(), edb.Schema.Create(context.Background()),
		"failed to create schema for %v database")

	rdb := redis.NewClient(&redis.Options{
		Addr:     config.Data.Redis.Endpoint,
		Password: config.Data.Redis.Password,
	})

	mdb, err := minio.New(config.Data.Minio.Endpoint, &minio.Options{
		Creds: credentials.NewStaticV4(config.Data.Minio.AccessKey, config.Data.Minio.SecretKey, ""),
	})
	assert.NoError(s.T(), err, "failed to open MinIO AIStor database connection")
	_, err = mdb.GetCreds()
	assert.NoError(s.T(), err, "failed test connection by requesting credentials for MinIO AIStor database")

	rmq, err := amqp.Dial(config.Data.Rabbitmq.Source)
	assert.NoError(s.T(), err, "failed to open RabbitMQ connection")

	u, err := server.Endpoint()
	assert.NoError(s.T(), err, "failed to retrieve http server endpoint")
	endpoint := u.Host
	client, err := khttp.NewClient(
		context.Background(),
		khttp.WithEndpoint(endpoint))
	assert.NoError(s.T(), err, "failed to create http client")

	go func() { _ = app.Run() }()

	s.crdb = crdb
	s.cdb = cdb
	s.cmio = cmio
	s.edb = edb
	s.rdb = rdb
	s.mdb = mdb
	s.rmq = rmq
	s.app = app
	s.jwtAuth = jwtAuth
	s.pwdGen = pwdGen
	s.endpoint = endpoint
	s.client = client
}

func (s *IntegrationSuite) TeardownSuite() {
	assert.NoError(s.T(), s.app.Stop(), "failed to stop app")

	assert.NoError(s.T(), s.rdb.Close(), "failed to close Redis client")

	assert.NoError(s.T(), s.edb.Close(), "failed to close database client")

	assert.NoError(s.T(), s.rmq.Close(), "failed to close RabbitMQ client")

	testcontainers.CleanupContainer(s.T(), s.crdb)
	testcontainers.CleanupContainer(s.T(), s.cdb)
	testcontainers.CleanupContainer(s.T(), s.cmio)
	testcontainers.CleanupContainer(s.T(), s.crmq)
}

func (s *IntegrationSuite) AfterTest(_, _ string) {
	_, err := s.edb.User.Delete().Exec(context.Background())
	assert.NoError(s.T(), err, "failed to delete users")

	// TODO: delete everything about images
}

func TestIntegrationSuite(t *testing.T) {
	suite.Run(t, new(IntegrationSuite))
}
