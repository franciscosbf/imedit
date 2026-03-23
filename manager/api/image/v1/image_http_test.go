package v1

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
	"testing"
	"time"

	"manager/internal/conf"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/suite"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/go-kratos/kratos/v2/middleware/recovery"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
)

var (
	imgUpload = &ImageUpload{
		Image: ImageContent{
			Name:    "img.png",
			Type:    "png",
			Content: []byte("abcd"),
		},
	}
	img = &Image{
		ImageId: "abc",
	}
	imgTransformations = &ImageTransformations{
		ImageId: "abc",
		Transformations: TransformImage{
			Resize: &ResizeImage{
				Width:  12,
				Height: 45,
			},
		},
	}

	imgMeta = &ImageMeta{
		ImageId: "abc",
		Name:    "img.png",
		Type:    "png",
		Size:    4,
		Width:   50,
		Height:  40,
	}
	imgContent = &ImageContent{
		Name:    "img.png",
		Type:    "png",
		Content: []byte("abcd"),
	}
	schedImgTransformation = &ScheduledImageTransformation{
		TransformationId: "def",
	}
	transformedImgEvent = &TransformedImageEvent{
		ImageId:          "abc",
		TransformationId: "def",
	}
)

type MocketNotifier struct {
	mock.Mock
	called bool
}

func (m *MocketNotifier) Notify(ctx context.Context) (Event, error) {
	if m.called {
		<-ctx.Done()

		return nil, nil
	}

	m.called = true

	m.Called(nil)

	return transformedImgEvent, nil
}

type MockedHttpServer struct {
	mock.Mock
	mNotifier *MocketNotifier
}

func (m *MockedHttpServer) UploadImage(ctx context.Context, req *ImageUpload) (*ImageMeta, error) {
	m.Called(nil, req)

	return imgMeta, nil
}

func (m *MockedHttpServer) GetSingleImage(ctx context.Context, req *Image) (*ImageContent, error) {
	m.Called(nil, req)

	return imgContent, nil
}

func (m *MockedHttpServer) GetPaginatedImage(ctx context.Context, req *Pagination) (ImageStream, error) {
	m.Called(nil, req)

	return nil, nil // TODO: implement
}

func (m *MockedHttpServer) GetImageMeta(ctx context.Context, req *Image) (*ImageMeta, error) {
	m.Called(nil, req)

	return imgMeta, nil
}

func (m *MockedHttpServer) TransformImage(ctx context.Context, req *ImageTransformations) (*ScheduledImageTransformation, error) {
	m.Called(nil, req)

	return schedImgTransformation, nil
}

func (m *MockedHttpServer) ImageNotification(ctx context.Context) (ImageNotifier, error) {
	m.Called(nil)

	return m.mNotifier, nil
}

type ImageHttpTestSuite struct {
	suite.Suite
	endpoint string
	srv      *khttp.Server
	mHttpSrv *MockedHttpServer
	client   *khttp.Client
}

func (s *ImageHttpTestSuite) BeforeTest(_, _ string) {
	config := conf.Server{Http: &conf.Server_HTTP{}}
	opts := []khttp.ServerOption{
		khttp.Address("0.0.0.0:0"),
		khttp.Middleware(
			recovery.Recovery(),
			ImageValidate(),
		),
	}
	srv := khttp.NewServer(opts...)
	mHttpSrv := new(MockedHttpServer)
	mHttpSrv.mNotifier = new(MocketNotifier)
	logger := log.NewStdLogger(os.Stdout)
	RegisterImageHTTPServer(&config, srv, mHttpSrv, logger)

	u, err := srv.Endpoint()
	assert.NoError(s.T(), err, "failed to retrieve http server endpoint")
	endpoint := u.Host
	client, err := khttp.NewClient(
		context.Background(),
		khttp.WithEndpoint(endpoint))
	assert.NoError(s.T(), err, "failed to create http client")

	go func() { _ = srv.Start(context.Background()) }()

	s.endpoint = endpoint
	s.srv = srv
	s.mHttpSrv = mHttpSrv
	s.client = client
}

func (s *ImageHttpTestSuite) AfterTest(_, _ string) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()

	assert.NoError(s.T(), s.srv.Stop(ctx), "failed to stop server")
}

func (s *ImageHttpTestSuite) sendRawRequest(
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

func (s *ImageHttpTestSuite) openWsConnection(ctx context.Context, path string) (*websocket.Conn, error) {
	conn, _, err := websocket.Dial(ctx, fmt.Sprintf("ws://%s/%s", s.endpoint, path), nil)

	return conn, err
}

func (s *ImageHttpTestSuite) encodeJsonBody(v any) io.Reader {
	buf := &bytes.Buffer{}

	content, err := json.Marshal(v)
	assert.NoError(s.T(), err, "failed to encode body")

	_, _ = buf.Write(content)

	return buf
}

func (s *ImageHttpTestSuite) decodeJsonBody(body io.ReadCloser, v any) {
	content, err := io.ReadAll(body)
	defer func() { _ = body.Close() }()
	assert.NoError(s.T(), err, "failed to read body")

	assert.NoError(s.T(), json.Unmarshal(content, v), "failed to decode body")
}

func (s *ImageHttpTestSuite) waitAndAssertMock(call *mock.Call) {
	call.WaitUntil(time.After(4 * time.Second))

	s.mHttpSrv.AssertExpectations(s.T())
}

func (s *ImageHttpTestSuite) TestUploadImage() {
	call := s.mHttpSrv.On("UploadImage", nil, imgUpload).Return(imgMeta, nil).Once()

	buf := bytes.Buffer{}

	mw := multipart.NewWriter(&buf)

	header := http.Header{}
	header.Set("Content-Type", mw.FormDataContentType())

	mHeaders := make(textproto.MIMEHeader)
	mHeaders.Set("Content-Disposition", multipart.FileContentDisposition("image", imgUpload.Image.Name))
	mHeaders.Set("Content-Type", imgUpload.Image.Type)

	mpw, err := mw.CreatePart(mHeaders)
	assert.NoError(s.T(), err, "failed to create multipart section")

	_, err = mpw.Write(imgUpload.Image.Content)
	assert.NoError(s.T(), err, "failed to write file into multipart section")

	assert.NoError(s.T(), mw.Close())

	resp, err := s.sendRawRequest("POST", "/v1/image/upload", nil, header, &buf)
	assert.NoError(s.T(), err, "failed to request image upload")
	assert.Equal(s.T(), http.StatusOK, resp.StatusCode)
	assert.Equal(s.T(), "application/json", resp.Header.Get("Content-Type"))

	gotImgMeta := &ImageMeta{}
	s.decodeJsonBody(resp.Body, gotImgMeta)
	assert.Equal(s.T(), imgMeta, gotImgMeta)

	s.waitAndAssertMock(call)
}

func (s *ImageHttpTestSuite) TestGetSingleImage() {
	call := s.mHttpSrv.On("GetSingleImage", nil, img).Return(imgContent, nil).Once()

	header := http.Header{}
	header.Add("Accept", "multipart/form-data")

	resp, err := s.sendRawRequest("GET", "/v1/image/single/abc", nil, header, nil)
	assert.NoError(s.T(), err, "failed to request get image")
	assert.Equal(s.T(), resp.StatusCode, http.StatusOK)

	mediaType, params, err := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	assert.NoError(s.T(), err, "failed to parse media type from Content-Type")
	assert.Equal(s.T(), "multipart/form-data", mediaType)
	assert.Contains(s.T(), params, "boundary", "missing boundary in Content-Type")

	mr := multipart.NewReader(resp.Body, params["boundary"])
	part, err := mr.NextPart()
	assert.NoError(s.T(), err, "expeting image part")
	assert.Equal(s.T(), "image", part.FormName())
	assert.Equal(s.T(), "img.png", part.FileName())
	assert.Equal(s.T(), "image/png", part.Header.Get("Content-Type"))
	gotContent, err := io.ReadAll(part)
	assert.NoError(s.T(), err, "failed to read file content")
	assert.Equal(s.T(), imgContent.Content, gotContent)
	_, err = mr.NextPart()
	assert.Error(s.T(), err, "expected to be no more parts")

	s.waitAndAssertMock(call)
}

func (s *ImageHttpTestSuite) TestGetPaginatedImage() {
	// TODO: implement
}

func (s *ImageHttpTestSuite) TestGetImageMeta() {
	call := s.mHttpSrv.On("GetImageMeta", nil, img).Return(imgMeta, nil).Once()

	resp, err := s.sendRawRequest("GET", "/v1/image/meta/abc", nil, nil, nil)
	assert.NoError(s.T(), err, "failed to request get image")
	assert.Equal(s.T(), http.StatusOK, resp.StatusCode)
	assert.Equal(s.T(), "application/json", resp.Header.Get("Content-Type"))

	gotImgMeta := &ImageMeta{}
	s.decodeJsonBody(resp.Body, gotImgMeta)
	assert.Equal(s.T(), imgMeta, gotImgMeta)

	s.waitAndAssertMock(call)
}

func (s *ImageHttpTestSuite) TestTransformImage() {
	call := s.mHttpSrv.On("TransformImage", nil, imgTransformations).Return(schedImgTransformation, nil).Once()

	body := s.encodeJsonBody(imgTransformations)

	header := http.Header{}
	header.Add("Content-Type", "application/json")

	resp, err := s.sendRawRequest("PUT", "/v1/image/transform", nil, header, body)
	assert.NoError(s.T(), err, "failed to request get image")
	assert.Equal(s.T(), http.StatusOK, resp.StatusCode)
	assert.Equal(s.T(), "application/json", resp.Header.Get("Content-Type"))

	gotSchedImgTransformation := &ScheduledImageTransformation{}
	s.decodeJsonBody(resp.Body, gotSchedImgTransformation)
	assert.Equal(s.T(), schedImgTransformation, gotSchedImgTransformation)

	s.waitAndAssertMock(call)
}

func (s *ImageHttpTestSuite) TestImageNotification() {
	callEndpoint := s.mHttpSrv.On("ImageNotification", nil).Return(s.mHttpSrv.mNotifier, nil).Once()
	callNotifier := s.mHttpSrv.mNotifier.On("Notify", nil).Return(transformedImgEvent, nil).Once()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := s.openWsConnection(ctx, "/v1/image/ws")
	assert.NoError(s.T(), err, "failed to open WebSocket connection")
	defer func() { _ = conn.CloseNow() }()

	n := struct {
		Etype string                `json:"type"`
		Event TransformedImageEvent `json:"event"`
	}{}
	assert.NoError(s.T(), wsjson.Read(ctx, conn, &n), "failed to read notification")
	assert.Equal(s.T(), TransformedImage.String(), n.Etype)
	assert.Equal(s.T(), transformedImgEvent, &n.Event)

	s.waitAndAssertMock(callEndpoint)
	s.waitAndAssertMock(callNotifier)
}

func TestImageHttpTestSuite(t *testing.T) {
	suite.Run(t, new(ImageHttpTestSuite))
}
