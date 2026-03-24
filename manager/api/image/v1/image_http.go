package v1

import (
	"context"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net"
	"net/http"
	"net/textproto"
	"strings"

	"manager/internal/conf"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	kerrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/log"
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

func RegisterImageHTTPServer(c *conf.Server, s *khttp.Server, srv ImageHTTPServer, logger log.Logger) {
	log := log.NewHelper(logger)

	r := s.Route("/")
	r.POST("/v1/image/upload", uploadImageHandler(c, srv))
	r.GET("/v1/image/single/{image_id}", getSingleImageHandler(srv, log))
	r.GET("/v1/image/paginated", getPaginatedImageHandler(srv, log))
	r.GET("/v1/image/meta/{image_id}", getImageMetaHandler(srv))
	r.PUT("/v1/image/transform", transformImageHandler(srv))
	r.GET("/v1/image/ws", imageNotificationHandler(srv, log))
}

func uploadImageHandler(c *conf.Server, srv ImageHTTPServer) func(ctx khttp.Context) error {
	multiPartMaxMemory := c.Http.MaxImageSize

	if multiPartMaxMemory == 0 {
		multiPartMaxMemory = defaultMultiPartMaxMemory
	}

	return func(ctx khttp.Context) error {
		req := ctx.Request()

		if err := req.ParseMultipartForm(int64(multiPartMaxMemory)); err != nil {
			return kerrors.BadRequest("CODEC", err.Error())
		}

		file, fHandler, err := req.FormFile("image")
		if err != nil {
			return kerrors.InternalServer("MULTIPART", err.Error())
		}
		defer func() {
			_ = file.Close()
		}()

		imgName := fHandler.Filename
		imgType := fHandler.Header.Get("Content-Type")
		imgContent, err := io.ReadAll(file)
		if err != nil {
			return kerrors.InternalServer("MULTIPART_PARSER", err.Error())
		}

		if !strings.HasPrefix(imgType, "image/") {
			return kerrors.BadRequest("CODEC", "expected image file type in Content-Type header value")
		}

		log.Info(http.DetectContentType(imgContent), imgType)

		if http.DetectContentType(imgContent) != imgType {
			return kerrors.BadRequest("CODEC", "unrecognized image")
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
		return ctx.JSON(http.StatusOK, reply)
	}
}

func checkAcceptHeader(header http.Header) error {
	acceptValue := header.Get("Accept")
	if mediaType, _, err := mime.ParseMediaType(acceptValue); err != nil {
		return kerrors.BadRequest("CODEC", fmt.Sprintf("invalid Accept header value: %v", err))
	} else if mediaType != "multipart/form-data" {
		return kerrors.BadRequest("CODEC", "expected content type multipart/form-data in Accept header")
	}

	return nil
}

func sendImage(resp http.ResponseWriter, next func() (*ImageContent, error), log *log.Helper) (err error) {
	mw := multipart.NewWriter(resp)

	resp.Header().Set("Content-Type", mw.FormDataContentType())

	for {
		var imgContent *ImageContent
		imgContent, err = next()
		if imgContent == nil {
			break
		} else if err != nil {
			log.Warn("Failed to retrieve image: %v", err)

			break
		}

		mHeaders := make(textproto.MIMEHeader)
		mHeaders.Set("Content-Disposition", multipart.FileContentDisposition("image", imgContent.Name))
		mHeaders.Set("Content-Type", "image/"+imgContent.Type)

		var mpw io.Writer
		mpw, err = mw.CreatePart(mHeaders)
		if err != nil {
			return
		}

		if _, err = mpw.Write(imgContent.Content); err != nil {
			return
		}
	}

	return mw.Close()
}

func getSingleImageHandler(srv ImageHTTPServer, log *log.Helper) func(ctx khttp.Context) error {
	return func(ctx khttp.Context) error {
		if err := checkAcceptHeader(ctx.Header()); err != nil {
			return err
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

		sent := false
		if err := sendImage(
			ctx.Response(),
			func() (*ImageContent, error) {
				if sent {
					return nil, nil
				}
				sent = true

				return reply, nil
			},
			log,
		); err != nil {
			log.Warn("Failed to send image to client: %v", err)
		}

		return nil
	}
}

func getPaginatedImageHandler(srv ImageHTTPServer, log *log.Helper) func(ctx khttp.Context) error {
	return func(ctx khttp.Context) error {
		if err := checkAcceptHeader(ctx.Header()); err != nil {
			return err
		}

		var in Pagination
		if err := ctx.BindQuery(&in); err != nil {
			return err
		}

		mHandler := ctx.Middleware(func(ctx context.Context, req any) (any, error) {
			return srv.GetPaginatedImage(ctx, req.(*Pagination))
		})
		out, err := mHandler(ctx, &in)
		if err != nil {
			return err
		}

		reply := out.(ImageStream)

		if err := sendImage(
			ctx.Response(),
			reply.Next,
			log,
		); err != nil {
			log.Warn("Failed to send images to client: %v", err)
		}

		return nil
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
		return ctx.JSON(http.StatusOK, reply)
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
		return ctx.JSON(http.StatusOK, reply)
	}
}

type returnedEvent struct {
	Etype string `json:"type"`
	Event `json:"event"`
}

type notifierClient struct {
	*websocket.Conn
}

func (nc *notifierClient) sendEvent(ctx context.Context, event Event) error {
	retEvent := returnedEvent{event.Type().String(), event}

	return wsjson.Write(ctx, nc.Conn, retEvent)
}

func imageNotificationHandler(srv ImageHTTPServer, log *log.Helper) func(ctx khttp.Context) error {
	return func(kctx khttp.Context) (_ error) {
		conn, err := websocket.Accept(kctx.Response(), kctx.Request(), nil)
		if err != nil {
			log.Warnf("While accepting WebSocket connection: %v", err)
			return
		}
		nCli := notifierClient{conn}

		ctx := nCli.CloseRead(kctx.Request().Context())

		defer func() {
			err := nCli.Close(websocket.StatusNormalClosure, "connection closed")
			if err != nil && err != net.ErrClosed && ctx.Err() != context.Canceled {
				log.Warnf("WebSocket connection wasn't properly closed: %v", err)
			}
		}()

		notifier, err := srv.ImageNotification(ctx)
		if err != nil {
			event := UnexpectedErrorEvent{
				Reason: fmt.Sprintf("failed to initialize notifier: %v", err),
			}
			if err := nCli.sendEvent(ctx, &event); err != nil {
				log.Warnf("Could not notify client on failed notifier initialization: %v", err)
			}

			log.Errorf("Failed to initialize notifier: %v", err)
			return
		}

		for {
			var (
				event Event
				err   error
				serr  error
			)

			if event, err = notifier.Notify(ctx); event == nil {
				break
			} else if err != nil {
				event = &UnexpectedErrorEvent{err.Error()}
			}

			if serr = nCli.sendEvent(ctx, event); serr != nil {
				log.Warnf("Could not notify client: %v", serr)
				break
			}

			if event.Type() == UnexpectedError {
				break
			}
		}

		return
	}
}
