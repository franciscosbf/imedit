package test

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"os"

	"github.com/stretchr/testify/assert"
)

func (s *IntegrationSuite) TestUploadImage() {
	image, err := os.ReadFile("./image/nature.jpg")
	assert.NoError(s.T(), err, "failed to load image")

	buf := bytes.Buffer{}

	mw := multipart.NewWriter(&buf)

	header := http.Header{}
	header.Set("Content-Type", mw.FormDataContentType())

	mHeaders := make(textproto.MIMEHeader)
	mHeaders.Set("Content-Disposition", multipart.FileContentDisposition("image", "nature.jpg"))
	mHeaders.Set("Content-Type", "image/jpeg")

	mpw, err := mw.CreatePart(mHeaders)
	assert.NoError(s.T(), err, "failed to create multipart section")

	_, err = mpw.Write(image)
	assert.NoError(s.T(), err, "failed to write file into multipart section")

	assert.NoError(s.T(), mw.Close())

	tu := &testUser{
		username: "username",
		password: "password",
	}

	s.registerUser(tu)
	bearerToken := s.loginUser(tu)
	header.Add("Authorization", bearerToken)

	resp, err := s.sendRawRequest("POST", "/v1/image/upload", nil, header, &buf)
	assert.NoError(s.T(), err)

	_ = resp
	// TODO: implement
}

func (s *IntegrationSuite) TestGetSingleImage() {
	// TODO: implement
}

func (s *IntegrationSuite) TestGetPaginatedImage() {
	// TODO: implement
}

func (s *IntegrationSuite) TestGetImageMeta() {
	// TODO: implement
}

func (s *IntegrationSuite) TestTransformImage() {
	// TODO: implement
}

func (s *IntegrationSuite) TestImageNotification() {
	// TODO: implement
}
