package test

import (
	"context"
	"path/filepath"

	"github.com/cloudresty/go-rabbitmq"
	"github.com/docker/go-connections/nat"
	cminio "github.com/franciscosbf/imedit/common/pkg/minio"
	"github.com/go-redis/redis/v8"
	"github.com/minio/minio-go/v7"
	"github.com/minio/minio-go/v7/pkg/credentials"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/suite"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

type BaseIntegrationSuite struct {
	suite.Suite

	crdb *testcontainers.DockerContainer
	cmio *testcontainers.DockerContainer
	crmq *testcontainers.DockerContainer

	CrdbEndpoint string
	CmioEndpoint string
	CrmqEndpoint string

	Rdb *redis.Client
	Mdb *minio.Client
	Rmq *rabbitmq.Client
}

func (s *BaseIntegrationSuite) RunReddisContainer() {
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
	s.CrdbEndpoint, err = s.crdb.Endpoint(context.Background(), "")
	assert.NoError(s.T(), err, "failed to retrieve Redis container endpoint")
}

func (s *BaseIntegrationSuite) RunMinIOContainer() {
	var (
		license string
		err     error
	)

	license, err = filepath.Abs("../../common/pkg/test/minio/minio.license")
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
	assert.NoError(s.T(), err, "failed to launch MinIO database container")
	s.CmioEndpoint, err = s.cmio.Endpoint(context.Background(), "")
	assert.NoError(s.T(), err, "failed to retrieve MinIO database container endpoint")
}

func (s *BaseIntegrationSuite) RunRabbitMQContainer() {
	var (
		plugins string
		err     error
	)

	plugins, err = filepath.Abs("../../common/pkg/test/rabbitmq/enabled_plugins")
	assert.NoError(s.T(), err, "failed to obtain absolute path for ./rabbitmq/enabled_plugins")
	s.crmq, err = testcontainers.Run(
		context.Background(), "rabbitmq:4.2.5-management",
		testcontainers.WithEnv(map[string]string{
			"RABBITMQ_DEFAULT_USER": "user",
			"RABBITMQ_DEFAULT_PASS": "password",
		}),
		testcontainers.WithFiles(testcontainers.ContainerFile{
			HostFilePath:      plugins,
			ContainerFilePath: "/etc/rabbitmq/enabled_plugins",
			FileMode:          0o777,
		}),
		testcontainers.WithExposedPorts("5672/tcp"),
		testcontainers.WithWaitStrategy(
			wait.ForListeningPort("5672/tcp"),
			wait.ForLog("Time to start RabbitMQ"),
		),
	)
	assert.NoError(s.T(), err, "failed to launch RabbitMQ container")
	s.CrmqEndpoint, err = s.crmq.PortEndpoint(context.Background(), nat.Port("5672"), "")
	assert.NoError(s.T(), err, "failed to retrieve RabbitMQ container endpoint")
}

func (s *BaseIntegrationSuite) TeardownContainers() {
	if s.crdb != nil {
		testcontainers.CleanupContainer(s.T(), s.crdb)
	}
	if s.cmio != nil {
		testcontainers.CleanupContainer(s.T(), s.cmio)
	}
	if s.crmq != nil {
		testcontainers.CleanupContainer(s.T(), s.crmq)
	}
}

func (s *BaseIntegrationSuite) SetupRedisConnection(endpoint, password string) {
	s.Rdb = redis.NewClient(&redis.Options{
		Addr:     endpoint,
		Password: password,
	})
}

func (s *BaseIntegrationSuite) SetupMinIOConnection(endpoint, accessKey, secretKey string) {
	var err error

	s.Mdb, err = minio.New(endpoint, &minio.Options{
		Creds: credentials.NewStaticV4(accessKey, secretKey, ""),
	})
	assert.NoError(s.T(), err, "failed to open MinIO database connection")
}

func (s *BaseIntegrationSuite) SetupRabbitMQConnection(endpoint, username, password string) {
	var err error

	s.Rmq, err = rabbitmq.NewClient(
		rabbitmq.WithHosts(endpoint),
		rabbitmq.WithCredentials(username, password),
	)
	assert.NoError(s.T(), err, "failed to open RabbitMQ connection")
}

func (s *BaseIntegrationSuite) SetupMinIODatabase() {
	assert.NoError(s.T(),
		s.Mdb.MakeBucket(context.Background(), cminio.ImagesBucket, minio.MakeBucketOptions{}),
		"failed to create bucket %s in MinIO database")
}

func (s *BaseIntegrationSuite) TeardownConnections() {
	assert.NoError(s.T(), s.Rdb.Close(), "failed to close Redis client")
	assert.NoError(s.T(), s.Rmq.Close(), "failed to close RabbitMQ client")
}

func (s *BaseIntegrationSuite) FlushDatabases() {
	imageIds := []string{}
	for objInfo := range s.Mdb.ListObjects(context.Background(), cminio.ImagesBucket, minio.ListObjectsOptions{}) {
		assert.NoError(s.T(), objInfo.Err, "failed to remove object from MinIO bucket %s", cminio.ImagesBucket)
		imageIds = append(imageIds, objInfo.Key)
	}
	for _, imageId := range imageIds {
		assert.NoError(s.T(),
			s.Mdb.RemoveObject(context.Background(), cminio.ImagesBucket, imageId, minio.RemoveObjectOptions{}),
			"failed to remove object %s from bucket")
	}

	assert.NoError(s.T(), s.Rdb.FlushAll(context.Background()).Err(), "failed to flush Redis entries")
}
