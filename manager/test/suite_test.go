package test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	iapi "manager/api/image/v1"
	uapi "manager/api/user/v1"
	"manager/ent"
	"manager/internal/auth"
	"manager/internal/biz"
	"manager/internal/conf"
	"manager/internal/data"
	"manager/internal/server"
	"manager/internal/service"

	"github.com/coder/websocket"
	ctest "github.com/franciscosbf/imedit/common/pkg/test"
	"github.com/go-kratos/kratos/v2"
	"github.com/go-kratos/kratos/v2/log"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
	"google.golang.org/protobuf/types/known/durationpb"
)

type testUser struct {
	username string
	password string
}

type IntegrationSuite struct {
	ctest.BaseIntegrationSuite

	cdb *testcontainers.DockerContainer

	cdbEndpoint string

	config      *conf.Bootstrap
	app         *kratos.App
	edb         *ent.Client
	jwtAuth     auth.JwtAuthenticator
	pwdGen      auth.PasswordGenerator
	appEndpoint string
	client      *khttp.Client
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
	request := uapi.RegisterUserRequest{
		Username: tu.username,
		Password: tu.password,
	}
	response := uapi.RegisterUserReply{}

	assert.NoError(
		s.T(),
		s.sendJsonRequest("POST", "/v1/user/register", &request, &response),
	)
}

func (s *IntegrationSuite) loginUser(tu *testUser) string {
	request := uapi.LoginUserRequest{
		Username: tu.username,
		Password: tu.password,
	}
	response := uapi.LoginUserReply{}

	assert.NoError(
		s.T(),
		s.sendJsonRequest("POST", "/v1/user/login", &request, &response),
	)

	return "Bearer " + response.Token
}

func (s *IntegrationSuite) registerAndLoginUser() (testUser, string) {
	tu := testUser{
		username: "username",
		password: "password",
	}

	s.registerUser(&tu)
	return tu, s.loginUser(&tu)
}

func (s *IntegrationSuite) encodeJsonBody(v any) io.Reader {
	buf := &bytes.Buffer{}

	content, err := json.Marshal(v)
	assert.NoError(s.T(), err, "failed to encode body")

	_, _ = buf.Write(content)

	return buf
}

func (s *IntegrationSuite) decodeJsonBody(body io.ReadCloser, v any) {
	content, err := io.ReadAll(body)
	defer func() { _ = body.Close() }()
	assert.NoError(s.T(), err, "failed to read body")

	assert.NoError(s.T(), json.Unmarshal(content, v), "failed to decode body")
}

func (s *IntegrationSuite) uploadImage(path, bearerToken string) (string, []byte) {
	content, err := os.ReadFile(path)
	assert.NoError(s.T(), err, "failed to load image %s", path)

	buf := bytes.Buffer{}

	mw := multipart.NewWriter(&buf)

	header := http.Header{}
	header.Set("Content-Type", mw.FormDataContentType())

	mHeaders := make(textproto.MIMEHeader)
	imgName := filepath.Base(path)
	mHeaders.Set("Content-Disposition", multipart.FileContentDisposition("image", imgName))
	imgType := strings.Split(imgName, ".")[1]
	mHeaders.Set("Content-Type", "image/"+imgType)

	mpw, err := mw.CreatePart(mHeaders)
	assert.NoError(s.T(), err, "failed to create multipart section")

	_, err = mpw.Write(content)
	assert.NoError(s.T(), err, "failed to write file into multipart section")

	assert.NoError(s.T(), mw.Close())

	header.Add("Authorization", bearerToken)

	resp, err := s.sendRawRequest("POST", "/v1/image/upload", nil, header, &buf)
	assert.NoError(s.T(), err)

	metadata := iapi.ImageMeta{}
	s.decodeJsonBody(resp.Body, &metadata)

	return metadata.ImageId, content
}

func (s *IntegrationSuite) validateMidiaType(header http.Header) (_params map[string]string) {
	mediaType, params, err := mime.ParseMediaType(header.Get("Content-Type"))
	assert.NoError(s.T(), err, "failed to parse media type from Content-Type")
	assert.Equal(s.T(), "multipart/form-data", mediaType)
	assert.Contains(s.T(), params, "boundary", "missing boundary in Content-Type")

	return params
}

func (s *IntegrationSuite) validateExpectedMimePart(mr *multipart.Reader, path string, imageContent []byte) {
	part, err := mr.NextPart()
	assert.NoError(s.T(), err, "expecting image part")
	assert.Equal(s.T(), "image", part.FormName())
	imgName := filepath.Base(path)
	assert.Equal(s.T(), imgName, part.FileName())
	imgType := strings.Split(imgName, ".")[1]
	assert.Equal(s.T(), "image/"+imgType, part.Header.Get("Content-Type"))

	storedContent, err := io.ReadAll(part)
	assert.NoError(s.T(), err, "failed to read image content")
	defer func() { _ = part.Close() }()
	assert.ElementsMatch(s.T(), imageContent, storedContent)
}

func (s *IntegrationSuite) openWsConnection(ctx context.Context, path string, header http.Header) (*websocket.Conn, error) {
	conn, _, err := websocket.Dial(
		ctx,
		fmt.Sprintf("ws://%s/%s", s.appEndpoint, path),
		&websocket.DialOptions{HTTPHeader: header},
	)

	return conn, err
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

func (s *IntegrationSuite) setupAppConfig() {
	s.config = &conf.Bootstrap{
		Server: &conf.Server{Http: &conf.Server_HTTP{
			Endpoint:       "0.0.0.0:0",
			RequestTimeout: durationpb.New(16 * time.Second),
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
			Rabbitmq: &conf.Data_RabbitMQ{
				Endpoint:    s.CrmqEndpoint,
				Connections: 5,
				Username:    "user",
				Password:    "password",
			},
			Cache: &conf.Data_Cache{},
			Event: &conf.Data_Event{},
		},
	}
}

func (s *IntegrationSuite) setupMySQLConnection() {
	var err error

	s.edb, err = ent.Open(s.config.Data.Database.Driver, s.config.Data.Database.Source)
	assert.NoError(s.T(), err, "failed to open MySQl database connection")
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
		s.config.Data.Rabbitmq.Endpoint,
		s.config.Data.Rabbitmq.Username,
		s.config.Data.Rabbitmq.Password,
	)
}

func (s *IntegrationSuite) setupMySQLDatabase() {
	assert.NoError(s.T(), s.edb.Schema.Create(context.Background()),
		"failed to create schema for %v database")
}

func (s *IntegrationSuite) setupAppAndRun() {
	var err error

	s.jwtAuth, err = auth.NewJwtAuthenticator(s.config.Auth)
	assert.NoError(s.T(), err, "failed to create JWT authenticator")
	s.pwdGen = auth.NewPasswordGenerator()
	logger := log.NewStdLogger(os.Stdout)
	ddata, _, err := data.NewData(s.config.Data)
	assert.NoError(s.T(), err, "failed to create data")
	urepo := data.NewUserRepo(ddata, logger)
	uuc := biz.NewUserUsecase(s.jwtAuth, s.pwdGen, urepo)
	user := service.NewUserService(uuc, logger)
	irepo := data.NewImageRepo(ddata, logger)
	iuc := biz.NewImageUsecase(s.jwtAuth, irepo, logger)
	image := service.NewImageService(iuc, logger)
	server := server.NewHTTPServer(s.config.Server, s.jwtAuth, user, image, logger)
	s.app = kratos.New(kratos.Server(server))

	u, err := server.Endpoint()
	assert.NoError(s.T(), err, "failed to retrieve http server endpoint")
	s.appEndpoint = u.Host

	go func() { _ = s.app.Run() }()
}

func (s *IntegrationSuite) setupAppConnection() {
	var err error

	s.client, err = khttp.NewClient(
		context.Background(),
		khttp.WithEndpoint(s.appEndpoint),
	)
	assert.NoError(s.T(), err, "failed to create http client")
}

func (s *IntegrationSuite) teardownApp() {
	assert.NoError(s.T(), s.app.Stop(), "failed to stop app")
}

func (s *IntegrationSuite) teardownConnections() {
	s.TeardownConnections()

	assert.NoError(s.T(), s.edb.Close(), "failed to close MySQL database client")
	assert.NoError(s.T(), s.client.Close(), "failed to close app client")
}

func (s *IntegrationSuite) teardownContainers() {
	s.TeardownContainers()
	if s.cdb != nil {
		testcontainers.CleanupContainer(s.T(), s.cdb)
	}
}

func (s *IntegrationSuite) SetupSuite() {
	go func() {
		if !s.T().Failed() {
			return
		}

		s.teardownContainers()
	}()

	s.RunReddisContainer()
	s.runMySQLContainer()
	s.RunMinIOContainer()
	s.RunRabbitMQContainer()

	s.setupAppConfig()

	s.setupMySQLConnection()
	s.setupRedisConnection()
	s.setupMinIOConnection()
	s.setupRabbitMQConnection()

	s.setupMySQLDatabase()
	s.SetupMinIODatabase()

	s.setupAppAndRun()
	s.setupAppConnection()
}

func (s *IntegrationSuite) TeardownSuite() {
	s.teardownApp()

	s.teardownConnections()

	s.teardownContainers()
}

func (s *IntegrationSuite) AfterTest(_, _ string) {
	s.FlushDatabases()

	_, err := s.edb.User.Delete().Exec(context.Background())
	assert.NoError(s.T(), err, "failed to delete users from MySQL database")

	_, err = s.edb.Image.Delete().Exec(context.Background())
	assert.NoError(s.T(), err, "failed to delete images from MySQL database")
}

func TestIntegrationSuite(t *testing.T) {
	suite.Run(t, new(IntegrationSuite))
}
