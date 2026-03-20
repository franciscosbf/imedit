package v1

import (
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"strings"

	"manager/internal/conf"

	khttp "github.com/go-kratos/kratos/v2/transport/http"
)

const (
	defaultMultiPartMaxMemory uint32 = 32 << 20 // 32 MB
)

type ImageHTTPServer interface {
	UploadImage(context.Context, *ImageUpload) (*ImageMeta, error)
	GetSingleImage(context.Context, *Image) (*ImageContent, error)
	GetPaginatedImage(context.Context, *Pagination) (ImageStream, error)
	GetImageMeta(context.Context, *Image) (*ImageMeta, error)
	TransformImage(context.Context, *ImageTransformations) (*ScheduledImageTransformation, error)
	ImageNotification(context.Context) (ImageNotifier, error)
}

func RegisterImageHTTPServer(c *conf.Server, s *khttp.Server, srv ImageHTTPServer) {
	r := s.Route("/")
	r.POST("/v1/image/upload", uploadImageHandler(c, srv))
	r.GET("/v1/image/single/{image_id}", getSingleImageHandler(srv))
	r.GET("/v1/image/paginated", getPaginatedImageHandler(srv))
	r.GET("/v1/image/meta/{image_id}", getImageMetaHandler(srv))
	r.PUT("/v1/image/transform", transformImageHandler(srv))

	s.HandleFunc("/v1/image/ws", imageNotificationHandler(srv))
}

func uploadImageHandler(c *conf.Server, srv ImageHTTPServer) func(ctx khttp.Context) error {
	multiPartMaxMemory := c.Http.MaxImageSize

	if multiPartMaxMemory == 0 {
		multiPartMaxMemory = defaultMultiPartMaxMemory
	}

	return func(ctx khttp.Context) error {
		req := ctx.Request()

		if err := req.ParseMultipartForm(int64(multiPartMaxMemory)); err != nil {
			return err
		}

		file, fHandler, err := req.FormFile("image")
		if err != nil {
			return err
		}
		defer func() {
			_ = file.Close()
		}()

		imgName := fHandler.Filename
		imgType := fHandler.Header.Get("Content-Type")
		imgContent, err := io.ReadAll(file)
		if err != nil {
			return err
		}

		imgType = strings.TrimLeft(imgType, "image/")

		in := ImageUpload{
			Image: ImageContent{imgName, imgType, imgContent},
		}

		mHandler := ctx.Middleware(func(ctx context.Context, req any) (any, error) {
			return srv.UploadImage(ctx, req.(*ImageUpload))
		})
		out, err := mHandler(ctx, &in)
		if err != nil {
			return err
		}

		reply := out.(*ImageMeta)
		return ctx.Result(http.StatusOK, reply)
	}
}

func getSingleImageHandler(srv ImageHTTPServer) func(ctx khttp.Context) error {
	return func(ctx khttp.Context) error {
		acceptValue := ctx.Header().Get("Accept")
		if mediaType, _, err := mime.ParseMediaType(acceptValue); err != nil {
			return ErrorNotAcceptableMediaType("invalid Accept value: %v", err)
		} else if mediaType != "multipart/form-data" {
			return ErrorInvalidMimeType("expected content type multipart/form-data in Accept header")
		}

		var in Image
		if err := ctx.BindVars(&in); err != nil {
			return err
		}

		mHandler := ctx.Middleware(func(ctx context.Context, req any) (any, error) {
			return srv.GetSingleImage(ctx, req.(*Image))
		})
		out, err := mHandler(ctx, &in)
		if err != nil {
			return err
		}

		reply := out.(*ImageContent)

		rw := ctx.Response()

		mw := multipart.NewWriter(rw)

		rw.Header().Set("Content-Type", mw.FormDataContentType())

		mHeaders := make(textproto.MIMEHeader)
		mHeaders.Set("Content-Disposition", multipart.FileContentDisposition("image", reply.Name))
		mHeaders.Set("Content-Type", "image/"+reply.Type)

		mpw, err := mw.CreatePart(mHeaders)
		if err != nil {
			return err
		}
		if _, err := mpw.Write(reply.Content); err != nil {
			return err
		}

		if err := mw.Close(); err != nil {
			return err
		}

		return nil
	}
}

func getPaginatedImageHandler(srv ImageHTTPServer) func(ctx khttp.Context) error {
	return func(ctx khttp.Context) error {
		var in Pagination
		if err := ctx.BindQuery(&in); err != nil {
			return err
		}

		// TODO: call handler and return images (should I do this in a stream fashion)

		_ = srv
		return nil // TODO: implement
	}
}

func getImageMetaHandler(srv ImageHTTPServer) func(ctx khttp.Context) error {
	return func(ctx khttp.Context) error {
		var in Image
		if err := ctx.BindVars(&in); err != nil {
			return err
		}

		mHandler := ctx.Middleware(func(ctx context.Context, req any) (any, error) {
			return srv.GetImageMeta(ctx, req.(*Image))
		})
		out, err := mHandler(ctx, &in)
		if err != nil {
			return err
		}

		reply := out.(*ImageMeta)
		return ctx.Result(http.StatusOK, reply)
	}
}

func transformImageHandler(srv ImageHTTPServer) func(ctx khttp.Context) error {
	return func(ctx khttp.Context) error {
		var in ImageTransformations
		if err := ctx.Bind(&in); err != nil {
			return err
		}

		mHandler := ctx.Middleware(func(ctx context.Context, req any) (any, error) {
			return srv.TransformImage(ctx, req.(*ImageTransformations))
		})
		out, err := mHandler(ctx, &in)
		if err != nil {
			return err
		}

		reply := out.(*ScheduledImageTransformation)
		return ctx.Result(http.StatusOK, reply)
	}
}

func imageNotificationHandler(srv ImageHTTPServer) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		_ = srv
		_ = w
		_ = r
		// TODO: implement
	}
}
