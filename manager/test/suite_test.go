package test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
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

	"github.com/go-kratos/kratos/v2"
	"github.com/go-redis/redis/v8"
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
	app      *kratos.App
	db       *ent.Client
	rdb      *redis.Client
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

func (s *IntegrationSuite) SetupSuite() {
	var (
		crdb    *testcontainers.DockerContainer
		crdbErr error
		cdb     *testcontainers.DockerContainer
		cdbErr  error
		jwtAuth auth.JwtAuthenticator
		pwdGen  auth.PasswordGenerator
	)

	go func() {
		if crdbErr == nil || cdbErr == nil {
			return
		}

		if crdb != nil {
			testcontainers.CleanupContainer(s.T(), crdb)
		}
		if cdb != nil {
			testcontainers.CleanupContainer(s.T(), cdb)
		}
	}()

	crdb, crdbErr = testcontainers.Run(
		context.Background(), "redis:8.6.1",
		testcontainers.WithCmd("redis-server", "--requirepass", "password"),
		testcontainers.WithExposedPorts("6379/tcp"),
		testcontainers.WithWaitStrategy(
			wait.ForListeningPort("6379/tcp"),
			wait.ForLog("Ready to accept connections"),
		),
	)
	assert.NoError(s.T(), crdbErr, "failed to launch Redis container")
	crdbEndpoint, err := crdb.Endpoint(context.Background(), "")
	assert.NoError(s.T(), err, "failed to retrieve Redis container endpoint")

	cdb, cdbErr = testcontainers.Run(
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
	assert.NoError(s.T(), cdbErr, "failed to launch database container")
	cdbEndpoint, err := cdb.Endpoint(context.Background(), "")
	assert.NoError(s.T(), err, "failed to retrieve database container endpoint")

	config := conf.Bootstrap{
		Server: &conf.Server{Http: &conf.Server_HTTP{
			Addr:    "0.0.0.0:8080",
			Timeout: durationpb.New(time.Second),
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
				Addr:         crdbEndpoint,
				Password:     "password",
				DialTimeout:  durationpb.New(4 * time.Second),
				ReadTimeout:  durationpb.New(2 * time.Second),
				WriteTimeout: durationpb.New(2 * time.Second),
			},
		},
	}

	jwtAuth, err = auth.NewJwtAuthenticator(config.Auth)
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

	db, err := ent.Open(config.Data.Database.Driver, config.Data.Database.Source)
	assert.NoError(s.T(), err, "failed to open database connection")

	assert.NoError(s.T(), db.Schema.Create(context.Background()),
		"failed to create schema")

	rdb := redis.NewClient(&redis.Options{
		Addr:     config.Data.Redis.Addr,
		Password: config.Data.Redis.Password,
	})

	endpoint := "localhost:8080"
	client, err := khttp.NewClient(
		context.Background(),
		khttp.WithEndpoint(endpoint))
	assert.NoError(s.T(), err, "failed to create http client")

	go func() { _ = app.Run() }()

	s.crdb = crdb
	s.cdb = cdb
	s.db = db
	s.rdb = rdb
	s.app = app
	s.jwtAuth = jwtAuth
	s.pwdGen = pwdGen
	s.endpoint = endpoint
	s.client = client
}

func (s *IntegrationSuite) TeardownSuite() {
	assert.NoError(s.T(), s.app.Stop(), "failed to stop app")

	assert.NoError(s.T(), s.rdb.Close(), "failed to close Redis client")

	assert.NoError(s.T(), s.db.Close(), "failed to close database client")

	testcontainers.CleanupContainer(s.T(), s.crdb)
	testcontainers.CleanupContainer(s.T(), s.cdb)
}

func (s *IntegrationSuite) AfterTest(_, _ string) {
	_, err := s.db.User.Delete().Exec(context.Background())
	assert.NoError(s.T(), err, "failed to delete users")
}

func TestIntegrationSuite(t *testing.T) {
	suite.Run(t, new(IntegrationSuite))
}
