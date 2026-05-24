package test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/jpeg"
	"io"
	"os"
	"strconv"
	"time"

	"github.com/anthonynsimon/bild/imgio"
	"github.com/anthonynsimon/bild/transform"
	"github.com/cloudresty/go-rabbitmq"
	cdata "github.com/franciscosbf/imedit/common/pkg/data"
	cminio "github.com/franciscosbf/imedit/common/pkg/minio"
	cmsgp "github.com/franciscosbf/imedit/common/pkg/msgp"
	crabbitmq "github.com/franciscosbf/imedit/common/pkg/rabbitmq"
	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"
	"github.com/stretchr/testify/assert"
	"github.com/tinylib/msgp/msgp"
	"github.com/vitali-fedulov/images4"
)

func (s *IntegrationSuite) TestTransformImage() {
	path := "./image/cam.jpeg"
	content, err := os.ReadFile(path)
	assert.NoError(s.T(), err, "failed to load image %s", path)

	var rotate uint32 = 20
	img, _, err := image.Decode(bytes.NewBuffer(content))
	assert.NoError(s.T(), err, "failed to decode image %s", path)
	rotatedImg := transform.Rotate(img, float64(rotate), nil)
	encode := imgio.JPEGEncoder(jpeg.DefaultQuality)
	rotatedContent := bytes.Buffer{}
	assert.NoError(s.T(),
		encode(&rotatedContent, rotatedImg), "failed to endecode rotated image %s", path)

	username := "username"
	imgId := uuid.NewString()

	imgConf, _, err := image.DecodeConfig(bytes.NewBuffer(content))
	assert.NoError(s.T(), err, "failed to decode header of image %s", path)

	ibuf := bytes.NewBuffer(content)
	currUsrMeta := map[string]string{
		"Image-Id": imgId,
		"Name":     "cam.jpeg",
		"Encoding": "jpeg",
		"Size":     strconv.Itoa(ibuf.Len()),
		"Width":    strconv.Itoa(imgConf.Width),
		"Height":   strconv.Itoa(imgConf.Height),
	}

	objId := cdata.NewObjectId(username, imgId).String()
	_, err = s.Mdb.PutObject(
		context.Background(),
		cminio.ImagesBucket,
		objId,
		ibuf, int64(ibuf.Len()),
		minio.PutObjectOptions{
			ContentType:  "application/octet-stream",
			UserMetadata: currUsrMeta,
		},
	)
	assert.NoError(s.T(), err, "failed to store image %s", path)

	cimgId := cdata.NewCachedImageId(username, imgId).String()
	assert.NoError(s.T(),
		s.Rdb.Set(context.Background(), cimgId, 0, 0).Err(),
		"failed to insert fake cached image")

	cmetaId := cdata.NewCachedMetadataId(username, imgId).String()
	assert.NoError(s.T(),
		s.Rdb.Set(context.Background(), cmetaId, 0, 0).Err(),
		"failed to insert fake cached image metadata")

	admin := s.Rmq.Admin()

	q, err := admin.DeclareQueue(context.Background(), "", rabbitmq.WithAutoDelete(), rabbitmq.WithClassicQueue())
	assert.NoError(s.T(), err, "failed to declare queue")

	assert.NoError(s.T(),
		admin.BindQueue(
			context.Background(), q.Name, crabbitmq.EventsExchange, crabbitmq.EventsRoutingKey(username),
		),
		"failed to bind queue to exchange %s", crabbitmq.EventsExchange)

	consumer, err := s.Rmq.NewConsumer(
		rabbitmq.WithExclusiveConsumer(), rabbitmq.WithAutoAck(),
	)
	assert.NoError(s.T(), err, "failed to create queue consumer")

	defer func() { _ = consumer.Close() }()

	type event struct {
		delivery *rabbitmq.Delivery
		err      error
	}
	eventCh := make(chan event, 2)
	go func() {
		if err := consumer.Consume(context.Background(), q.Name,
			func(ctx context.Context, delivery *rabbitmq.Delivery) error {
				eventCh <- event{delivery: delivery}
				return nil
			}); err != nil {
			eventCh <- event{err: err}
		}
	}()

	publisher, err := s.Rmq.NewPublisher()
	assert.NoError(s.T(), err, "failed to create publisher")

	ctransformations := cmsgp.Transformations{
		TransformationId: uuid.NewString(),
		ImageId:          imgId,
		Username:         username,
		Rotate:           &rotate,
	}

	tbuf := bytes.Buffer{}
	assert.NoError(s.T(), msgp.Encode(&tbuf, &ctransformations), "failed to encode transformations")

	routingKey := crabbitmq.TransformationsRoutingKey(username, imgId)
	message := rabbitmq.Message{
		ContentType: "application/octet-stream",
		Body:        tbuf.Bytes(),
	}
	assert.NoError(s.T(),
		publisher.Publish(
			context.Background(), crabbitmq.TransformationsExchange, routingKey, &message,
		),
		"failed to publish event")

	ctx, cancelCtx := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancelCtx()

	select {
	case <-ctx.Done():
		assert.NoError(s.T(), errors.New("event wasn't received by queue consumer"))
	case event := <-eventCh:
		assert.NoError(s.T(), event.err)

		assert.Equal(s.T(), "application/octet-stream", event.delivery.ContentType)
		var epack cmsgp.EventPack
		buf := bytes.NewBuffer(event.delivery.Body)
		assert.NoError(s.T(), msgp.Decode(buf, &epack), "failed to decode event pack")
		assert.Equal(s.T(), epack.Type(), cmsgp.TransformedImage)
		e := epack.Event.(*cmsgp.TransformedImageEvent)
		assert.Equal(s.T(), ctransformations.TransformationId, e.TransformationId)
		assert.Equal(s.T(), ctransformations.ImageId, e.ImageId)

		obj, err := s.Mdb.GetObject(
			context.Background(),
			cminio.ImagesBucket,
			objId,
			minio.GetObjectOptions{},
		)
		assert.NoError(s.T(), err, "failed to retrieve object from bucket")

		objInfo, err := obj.Stat()
		assert.NoError(s.T(), err, "failed to retrieve object info")
		newUsrMeta := objInfo.UserMetadata
		assert.Equal(s.T(), currUsrMeta["Image-Id"], newUsrMeta["Image-Id"])
		assert.Equal(s.T(), currUsrMeta["Name"], newUsrMeta["Name"])
		assert.Equal(s.T(), currUsrMeta["Encoding"], newUsrMeta["Encoding"])
		size := strconv.Itoa(rotatedContent.Len())
		assert.Equal(s.T(), size, newUsrMeta["Size"])
		assert.Equal(s.T(), currUsrMeta["Width"], newUsrMeta["Width"])
		assert.Equal(s.T(), currUsrMeta["Height"], newUsrMeta["Height"])

		storedContent, err := io.ReadAll(obj)
		assert.NoError(s.T(), err, "failed to read object content")
		defer func() { _ = obj.Close() }()

		rotatedStoredImg, _, err := image.Decode(bytes.NewBuffer(storedContent))
		assert.NoError(s.T(), err, "failed decode stored image")

		assert.True(s.T(),
			images4.Similar(images4.Icon(rotatedImg), images4.Icon(rotatedStoredImg)),
			"image pixels aren't identical")

		assert.Equal(s.T(), s.Rdb.Get(context.Background(), cimgId).Err(), redis.Nil)
		assert.Equal(s.T(), s.Rdb.Get(context.Background(), cmetaId).Err(), redis.Nil)
	}
}
