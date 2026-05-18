package test

import (
	"bytes"
	"context"
	"errors"
	"image"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"os"
	"strconv"
	"time"

	api "manager/api/image/v1"
	"manager/ent/user"

	"github.com/cloudresty/go-rabbitmq"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	cminio "github.com/franciscosbf/imedit/common/pkg/minio"
	cmsgp "github.com/franciscosbf/imedit/common/pkg/msgp"
	crabbitmq "github.com/franciscosbf/imedit/common/pkg/rabbitmq"
	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"
	"github.com/stretchr/testify/assert"
	"github.com/tinylib/msgp/msgp"
)

func (s *IntegrationSuite) TestUploadImage() {
	content, err := os.ReadFile("./image/nature.jpeg")
	assert.NoError(s.T(), err, "failed to load image")
	imgConf, _, err := image.DecodeConfig(bytes.NewBuffer(content))
	assert.NoError(s.T(), err, "failed to decode meta info about image ./image/nature.jpeg")

	buf := bytes.Buffer{}

	mw := multipart.NewWriter(&buf)

	header := http.Header{}
	header.Set("Content-Type", mw.FormDataContentType())

	mHeaders := make(textproto.MIMEHeader)
	mHeaders.Set("Content-Disposition", multipart.FileContentDisposition("image", "nature.jpeg"))
	mHeaders.Set("Content-Type", "image/jpeg")

	mpw, err := mw.CreatePart(mHeaders)
	assert.NoError(s.T(), err, "failed to create multipart section")

	_, err = mpw.Write(content)
	assert.NoError(s.T(), err, "failed to write file into multipart section")

	assert.NoError(s.T(), mw.Close())

	tu, bearerToken := s.registerAndLoginUser()
	header.Add("Authorization", bearerToken)

	resp, err := s.sendRawRequest("POST", "/v1/image/upload", nil, header, &buf)
	assert.NoError(s.T(), err, "failed to request image upload")

	defer func() { _ = resp.Body.Close() }()

	metadata := api.ImageMeta{}
	s.decodeJsonBody(resp.Body, &metadata)
	assert.NoError(s.T(), uuid.Validate(metadata.ImageId))
	assert.Equal(s.T(), "nature.jpeg", metadata.Name)
	assert.Equal(s.T(), "jpeg", metadata.Encoding)
	assert.EqualValues(s.T(), len(content), metadata.Size)
	assert.EqualValues(s.T(), imgConf.Width, metadata.Width)
	assert.EqualValues(s.T(), imgConf.Height, metadata.Height)
	assert.Equal(s.T(), "0001-01-01 00:00:00 +0000 UTC", metadata.LastModified)
	assert.NoError(s.T(), err)

	obj, err := s.mdb.GetObject(
		context.Background(), cminio.ImagesBucket, tu.username+"."+metadata.ImageId, minio.GetObjectOptions{},
	)
	assert.NoError(s.T(), err, "failed to retrieve object from bucket")

	objInfo, err := obj.Stat()
	assert.NoError(s.T(), err, "failed to retrieve object info")
	usrMeta := objInfo.UserMetadata
	assert.Equal(s.T(), metadata.ImageId, usrMeta["Image-Id"])
	assert.Equal(s.T(), metadata.Name, usrMeta["Name"])
	assert.Equal(s.T(), metadata.Encoding, usrMeta["Encoding"])
	size, err := strconv.ParseUint(usrMeta["Size"], 10, 32)
	assert.NoError(s.T(), err)
	assert.EqualValues(s.T(), metadata.Size, size)
	width, err := strconv.ParseUint(usrMeta["Width"], 10, 32)
	assert.NoError(s.T(), err)
	assert.EqualValues(s.T(), metadata.Width, width)
	height, err := strconv.ParseUint(usrMeta["Height"], 10, 32)
	assert.NoError(s.T(), err)
	assert.EqualValues(s.T(), metadata.Height, height)

	storedContent, err := io.ReadAll(obj)
	assert.NoError(s.T(), err, "failed to read object content")
	defer func() { _ = obj.Close() }()
	assert.ElementsMatch(s.T(), content, storedContent)

	u, err := s.edb.User.Query().Where(user.Username(tu.username)).Only(context.Background())
	assert.NoError(s.T(), err, "failed to retrieve user")
	i, err := u.QueryImages().Only(context.Background())
	assert.NoError(s.T(), err, "failed to retrieve user image id")
	assert.Equal(s.T(), metadata.ImageId, i.ImageID)
}

func (s *IntegrationSuite) TestGetSingleImage() {
	_, bearerToken := s.registerAndLoginUser()
	path := "./image/nature.jpeg"
	imageId, content := s.uploadImage(path, bearerToken)

	header := http.Header{}
	header.Add("Accept", "multipart/form-data")
	header.Add("Authorization", bearerToken)

	resp, err := s.sendRawRequest("GET", "/v1/image/single/"+imageId, nil, header, nil)
	assert.NoError(s.T(), err, "failed to request image")
	assert.Equal(s.T(), resp.StatusCode, http.StatusOK)

	defer func() { _ = resp.Body.Close() }()

	params := s.validateMidiaType(resp.Header)

	mr := multipart.NewReader(resp.Body, params["boundary"])
	s.validateExpectedMimePart(mr, path, content)

	_, err = mr.NextPart()
	assert.Equal(s.T(), io.EOF, err, "expected one part only")
}

func (s *IntegrationSuite) TestGetPaginatedImage() {
	_, bearerToken := s.registerAndLoginUser()
	pathNature := "./image/nature.jpeg"
	_, contentNature := s.uploadImage(pathNature, bearerToken)
	pathTrain := "./image/train.jpeg"
	_, contentTrain := s.uploadImage(pathTrain, bearerToken)

	query := url.Values{}
	query.Add("page", strconv.Itoa(1))
	query.Add("limit", strconv.Itoa(2))

	header := http.Header{}
	header.Add("Accept", "multipart/form-data")
	header.Add("Authorization", bearerToken)

	resp, err := s.sendRawRequest("GET", "/v1/image/paginated", query, header, nil)
	assert.NoError(s.T(), err, "failed to request paginated image")
	assert.Equal(s.T(), resp.StatusCode, http.StatusOK)

	defer func() { _ = resp.Body.Close() }()

	params := s.validateMidiaType(resp.Header)

	mr := multipart.NewReader(resp.Body, params["boundary"])
	s.validateExpectedMimePart(mr, pathNature, contentNature)
	s.validateExpectedMimePart(mr, pathTrain, contentTrain)

	_, err = mr.NextPart()
	assert.Equal(s.T(), io.EOF, err, "expected two parts only")
}

func (s *IntegrationSuite) TestGetImageMeta() {
	tu, bearerToken := s.registerAndLoginUser()
	imageId, content := s.uploadImage("./image/nature.jpeg", bearerToken)
	imgConf, _, err := image.DecodeConfig(bytes.NewBuffer(content))
	assert.NoError(s.T(), err, "failed to decode meta info about image ./image/nature.jpeg")

	header := http.Header{}
	header.Add("Authorization", bearerToken)

	resp, err := s.sendRawRequest("GET", "/v1/image/meta/"+imageId, nil, header, nil)
	assert.NoError(s.T(), err, "failed to request image metadata")
	assert.Equal(s.T(), resp.StatusCode, http.StatusOK)

	defer func() { _ = resp.Body.Close() }()

	objInfo, err := s.mdb.StatObject(
		context.Background(), cminio.ImagesBucket, tu.username+"."+imageId, minio.GetObjectOptions{},
	)
	assert.NoError(s.T(), err, "failed to retrieve object from bucket")

	metadata := api.ImageMeta{}
	s.decodeJsonBody(resp.Body, &metadata)
	assert.NoError(s.T(), uuid.Validate(metadata.ImageId))
	assert.Equal(s.T(), imageId, metadata.ImageId)
	assert.Equal(s.T(), "nature.jpeg", metadata.Name)
	assert.Equal(s.T(), "jpeg", metadata.Encoding)
	assert.EqualValues(s.T(), len(content), metadata.Size)
	assert.EqualValues(s.T(), imgConf.Width, metadata.Width)
	assert.EqualValues(s.T(), imgConf.Height, metadata.Height)
	assert.Equal(s.T(), objInfo.LastModified.UTC().String(), metadata.LastModified)
}

func (s *IntegrationSuite) TestTransformImage() {
	tu, bearerToken := s.registerAndLoginUser()
	imageId, _ := s.uploadImage("./image/nature.jpeg", bearerToken)

	admin := s.rmq.Admin()

	q, err := admin.DeclareQueue(context.Background(), "", rabbitmq.WithAutoDelete(), rabbitmq.WithClassicQueue())
	assert.NoError(s.T(), err, "failed to declare queue")

	assert.NoError(s.T(),
		admin.BindQueue(
			context.Background(), q.Name, crabbitmq.TransformationsExchange, "1",
		),
		"failed to bind queue to exchange %s", crabbitmq.TransformationsExchange)

	consumer, err := s.rmq.NewConsumer(
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

	imgTransformations := &api.ImageTransformations{
		ImageId: imageId,
		Transformations: api.TransformImage{
			Resize: &api.ResizeImage{
				Width:  12,
				Height: 45,
			},
		},
	}
	body := s.encodeJsonBody(imgTransformations)

	header := http.Header{}
	header.Add("Content-Type", "application/json")
	header.Add("Authorization", bearerToken)

	resp, err := s.sendRawRequest("PUT", "/v1/image/transform", nil, header, body)
	assert.NoError(s.T(), err, "failed to request get image")
	assert.Equal(s.T(), http.StatusOK, resp.StatusCode)
	assert.Equal(s.T(), "application/json", resp.Header.Get("Content-Type"))

	defer func() { _ = resp.Body.Close() }()

	schedImgTransformation := &api.ScheduledImageTransformation{}
	s.decodeJsonBody(resp.Body, schedImgTransformation)
	assert.NoError(s.T(), uuid.Validate(schedImgTransformation.TransformationId))

	ctx, cancelCtx := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancelCtx()

	select {
	case <-ctx.Done():
		assert.Error(s.T(), errors.New("event wasn't received by queue consumer"))
	case event := <-eventCh:
		assert.NoError(s.T(), event.err)

		assert.Equal(s.T(), "application/octet-stream", event.delivery.ContentType)
		var transformation cmsgp.Transformations
		buf := bytes.NewBuffer(event.delivery.Body)
		assert.NoError(s.T(), msgp.Decode(buf, &transformation), "failed to decode event")
		assert.Equal(s.T(), schedImgTransformation.TransformationId, transformation.TransformationId)
		assert.Equal(s.T(), tu.username, transformation.Username)
		assert.Equal(s.T(), imageId, transformation.ImageId)
		assert.NotNil(s.T(), transformation.Resize)
		assert.Equal(s.T(), imgTransformations.Transformations.Resize.Width, transformation.Resize.Width)
		assert.Equal(s.T(), imgTransformations.Transformations.Resize.Height, transformation.Resize.Height)
		assert.Nil(s.T(), transformation.Crop)
		assert.Nil(s.T(), transformation.Rotate)
		assert.Nil(s.T(), transformation.Format)
	}
}

func (s *IntegrationSuite) TestImageNotification() {
	tu, bearerToken := s.registerAndLoginUser()

	publisher, err := s.rmq.NewPublisher(
		rabbitmq.WithMandatory(),
		rabbitmq.WithConfirmation(4*time.Second),
	)
	assert.NoError(s.T(), err, "failed to create publisher")

	header := http.Header{}
	header.Add("Authorization", bearerToken)
	header.Add("Sec-WebSocket-Version", "boda")

	conn, err := s.openWsConnection(context.Background(), "/v1/image/ws", header)
	assert.NoError(s.T(), err, "failed to open WebSocket connection")

	// queue may not be ready yet
	time.Sleep(4 * time.Second)

	event := cmsgp.TransformedImageEvent{
		ImageId:          uuid.NewString(),
		TransformationId: uuid.NewString(),
	}
	epack := cmsgp.EventPack{
		Event: &event,
	}
	encodedEvent := bytes.Buffer{}
	assert.NoError(s.T(),
		msgp.Encode(&encodedEvent, &epack),
		"failed to encode event")

	routingKey := crabbitmq.EventsRoutingKey(tu.username)
	message := rabbitmq.Message{
		ContentType: "application/octet-stream",
		Body:        encodedEvent.Bytes(),
	}
	assert.NoError(s.T(),
		publisher.Publish(
			context.Background(), crabbitmq.EventsExchange, routingKey, &message,
		),
		"failed to publish event")

	ctx, cancelCtx := context.WithTimeout(context.Background(), 16*time.Second)
	defer cancelCtx()
	notification := struct {
		Etype string                    `json:"type"`
		Event api.TransformedImageEvent `json:"event"`
	}{}
	assert.NoError(s.T(), wsjson.Read(ctx, conn, &notification), "failed to read notification")
	assert.Equal(s.T(), api.TransformedImage.String(), notification.Etype)
	assert.Equal(s.T(), event.ImageId, notification.Event.ImageId)
	assert.Equal(s.T(), event.TransformationId, notification.Event.TransformationId)

	assert.NoError(s.T(), conn.Close(websocket.StatusNormalClosure, ""), "failed to issue close handshake")
}
