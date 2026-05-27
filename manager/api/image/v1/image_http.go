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
	"path"
	"strings"

	"manager/internal/conf"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	kerrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/log"
	khttp "github.com/go-kratos/kratos/v2/transport/http"
)

const (
	defaultMultiPartMaxMemory = 32 << 20 // 32 MB
)

const (
	ImageOperations = "/api.image.v1.Image"
	ImageBasePath   = "/v1/image"
)

func operationJoin(o string) string {
	return path.Join(ImageOperations, o)
}

func pathJoin(p string) string {
	return path.Join(ImageBasePath, p)
}

var (
	OperationUploadImage       = operationJoin("/UploadImage")
	OperationGetSingleImage    = operationJoin("/GetSingleImage")
	OperationGetPaginatedImage = operationJoin("/GetPaginatedImage")
	OperationGetImageMeta      = operationJoin("/GetImageMeta")
	OperationTransformImage    = operationJoin("/TransformImage")
	OperationImageNotification = operationJoin("/ImageNotification")
)

var (
	uploadImagePath       = pathJoin("/upload")
	getSingleImagePath    = pathJoin("/single/{image_id}")
	getPaginatedImagePath = pathJoin("/paginated")
	getImageMetaPath      = pathJoin("/meta/{image_id}")
	transformImagePath    = pathJoin("/transform")
	imageNotificationPath = pathJoin("/ws")
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

	route := s.Route("/")
	route.POST(uploadImagePath, uploadImageHandler(c, srv))
	route.GET(getSingleImagePath, getSingleImageHandler(srv, log))
	route.GET(getPaginatedImagePath, getPaginatedImageHandler(srv, log))
	route.GET(getImageMetaPath, getImageMetaHandler(srv))
	route.PUT(transformImagePath, transformImageHandler(srv))
	route.GET(imageNotificationPath, imageNotificationHandler(srv, log))
}

func uploadImageHandler(c *conf.Server, srv ImageHTTPServer) func(ctx khttp.Context) error {
	var multiPartMaxMemory int
	if maxImageSize := c.Http.MaxImageSize; maxImageSize > 0 {
		multiPartMaxMemory = int(c.Http.MaxImageSize)
	} else {
		multiPartMaxMemory = defaultMultiPartMaxMemory
	}

	return func(ctx khttp.Context) error {
		req := ctx.Request()

		khttp.SetOperation(ctx, OperationUploadImage)
		mHandler := ctx.Middleware(func(ctx context.Context, _ any) (any, error) {
			if err := req.ParseMultipartForm(int64(multiPartMaxMemory)); err != nil {
				return nil, kerrors.BadRequest("CODEC", err.Error())
			}

			file, fHandler, err := req.FormFile("image")
			if err != nil {
				return nil, kerrors.InternalServer("MULTIPART", err.Error())
			}
			defer func() {
				_ = file.Close()
			}()

			imgName := fHandler.Filename
			imgType := fHandler.Header.Get("Content-Type")
			imgContent, err := io.ReadAll(file)
			if err != nil {
				return nil, kerrors.InternalServer("MULTIPART_PARSER", err.Error())
			}

			if !strings.HasPrefix(imgType, "image/") {
				return nil, kerrors.BadRequest("CODEC", "expected image file type in Content-Type header value")
			}

			if http.DetectContentType(imgContent) != imgType {
				return nil, kerrors.BadRequest("CODEC", "unrecognized image")
			}

			imgType = strings.TrimLeft(imgType, "image/")

			in := ImageUpload{
				Image: ImageContent{imgName, imgType, imgContent},
			}

			return srv.UploadImage(ctx, &in)
		})
		out, err := mHandler(ctx, nil)
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

			continue
		}

		mHeaders := make(textproto.MIMEHeader)
		mHeaders.Set("Content-Disposition", multipart.FileContentDisposition("image", imgContent.Name))
		mHeaders.Set("Content-Type", "image/"+imgContent.Encoding)

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

		khttp.SetOperation(ctx, OperationGetSingleImage)
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

		khttp.SetOperation(ctx, OperationGetPaginatedImage)
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

		khttp.SetOperation(ctx, OperationGetImageMeta)
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

		khttp.SetOperation(ctx, OperationTransformImage)
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
	return func(ctx khttp.Context) (_ error) {
		w := ctx.Response()
		r := ctx.Request()

		khttp.SetOperation(ctx, OperationImageNotification)
		mHandler := ctx.Middleware(func(ctx context.Context, _ any) (_ any, _ error) {
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				log.Warnf("While accepting WebSocket connection: %v", err)

				return
			}
			nCli := notifierClient{conn}

			ctx = nCli.CloseRead(ctx)

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
				)

				if event, err = notifier.Notify(ctx); event == nil {
					break
				} else if err != nil {
					event = &UnexpectedErrorEvent{err.Error()}
				}

				if err := nCli.sendEvent(ctx, event); err != nil {
					log.Warnf("Failed to notify client with event: %v", err)

					break
				}

				if event.Type() == UnexpectedError {
					break
				}
			}

			return
		})

		_, err := mHandler(ctx, nil)

		return err
	}
}
