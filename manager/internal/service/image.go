package service

import (
	"context"
	"fmt"

	api "manager/api/image/v1"
	"manager/internal/biz"

	cbiz "github.com/franciscosbf/imedit/common/pkg/biz"
	"github.com/go-kratos/kratos/v2/log"
)

type ImageService struct {
	uc  *biz.ImageUsecase
	log *log.Helper
}

func (s *ImageService) UploadImage(ctx context.Context, req *api.ImageUpload) (*api.ImageMeta, error) {
	s.log.WithContext(ctx).Infof("UploadImage %s", req.Image.Name)

	image := biz.ImageContent{
		Name:     req.Image.Name,
		Encoding: cbiz.FromRawImageEncoding(req.Image.Encoding),
		Content:  req.Image.Content,
	}

	metadata, err := s.uc.Store(ctx, &image)
	if err != nil {
		return nil, err
	}

	return &api.ImageMeta{
		ImageId:      metadata.ImageId,
		Name:         metadata.Name,
		Encoding:     metadata.Encoding.String(),
		Size:         metadata.Size,
		Width:        metadata.Width,
		Height:       metadata.Height,
		LastModified: metadata.LastModified.UTC().String(),
	}, nil
}

func (s *ImageService) GetSingleImage(ctx context.Context, req *api.Image) (*api.ImageContent, error) {
	s.log.WithContext(ctx).Infof("GetSingleImage %s", req.ImageId)

	content, err := s.uc.Get(ctx, req.ImageId)
	if err != nil {
		return nil, err
	}

	return &api.ImageContent{
		Name:     content.Name,
		Encoding: content.Encoding.String(),
		Content:  content.Content,
	}, nil
}

type imageStream struct {
	next func() *biz.StreamedImage
}

func (s imageStream) Next() (*api.ImageContent, error) {
	if simg := s.next(); simg == nil {
		return nil, nil
	} else if simg.Err != nil {
		return nil, simg.Err
	} else {
		return &api.ImageContent{
			Name:     simg.Image.Name,
			Encoding: simg.Image.Encoding.String(),
			Content:  simg.Image.Content,
		}, nil
	}
}

func (s *ImageService) GetPaginatedImage(ctx context.Context, req *api.Pagination) (api.ImageStream, error) {
	s.log.WithContext(ctx).Infof("GetPaginatedImage page=%d, limit=%d", req.Page, req.Limit)

	pagination := biz.ImagesPagination{Page: req.Page, Limit: req.Limit}

	retrieveImage, err := s.uc.GetPaginated(ctx, &pagination)
	if err != nil {
		return nil, err
	}

	return imageStream{next: retrieveImage}, nil
}

func (s *ImageService) GetImageMeta(ctx context.Context, req *api.Image) (*api.ImageMeta, error) {
	s.log.WithContext(ctx).Infof("GetImageMeta %s", req.ImageId)

	metadata, err := s.uc.GetMetadata(ctx, req.ImageId)
	if err != nil {
		return nil, err
	}

	return &api.ImageMeta{
		ImageId:      metadata.ImageId,
		Name:         metadata.Name,
		Encoding:     metadata.Encoding.String(),
		Size:         metadata.Size,
		Width:        metadata.Width,
		Height:       metadata.Height,
		LastModified: metadata.LastModified.UTC().String(),
	}, nil
}

func (s *ImageService) TransformImage(ctx context.Context, req *api.ImageTransformations) (*api.ScheduledImageTransformation, error) {
	s.log.WithContext(ctx).Infof("TransformImage %s", req.ImageId)

	transformations := cbiz.ImageTransformations{
		Rotate: req.Transformations.Rotate,
	}
	if resize := req.Transformations.Resize; resize != nil {
		transformations.Resize = &cbiz.ResizeImage{
			Width:  resize.Width,
			Height: resize.Height,
		}
	}
	if crop := req.Transformations.Crop; crop != nil {
		transformations.Crop = &cbiz.CropImage{
			Width:  crop.Width,
			Height: crop.Height,
			X:      crop.X,
			Y:      crop.Y,
		}
	}
	if format := req.Transformations.Format; format != nil {
		format := cbiz.FromRawImageEncoding(*format)
		transformations.Format = &format
	}

	schedTransformation, err := s.uc.Transform(
		ctx, req.ImageId, &transformations,
	)
	if err != nil {
		return nil, err
	}

	return &api.ScheduledImageTransformation{
		TransformationId: schedTransformation.TransformationId,
	}, nil
}

type imageNotifiter struct {
	notify func() *biz.StreamedImageEvent
}

func (n imageNotifiter) Notify(ctx context.Context) (api.Event, error) {
	sevent := n.notify()
	if sevent == nil {
		return nil, nil
	}

	if sevent.Err != nil {
		return nil, sevent.Err
	}

	event := sevent.Event
	switch event.Type() {
	case biz.TransformedImage:
		event := event.(*biz.TransformedImageEvent)
		return &api.TransformedImageEvent{
			ImageId:          event.ImageId,
			TransformationId: event.TransformationId,
		}, nil
	case biz.FailedImageTranformation:
		event := event.(*biz.FailedImageTranformationEvent)
		return &api.FailedImageTranformationEvent{
			ImageId:          event.ImageId,
			TransformationId: event.TransformationId,
			Reason:           event.Reason,
		}, nil
	case biz.UnexpectedError:
		event := event.(*biz.UnexpectedErrorEvent)
		return &api.UnexpectedErrorEvent{
			Reason: event.Reason,
		}, nil
	default:
		return nil, fmt.Errorf("unknown event %v", event.Type())
	}
}

func (s *ImageService) ImageNotification(ctx context.Context) (api.ImageNotifier, error) {
	s.log.WithContext(ctx).Infof("ImageNotification")

	notify, err := s.uc.GetEvents(ctx)
	if err != nil {
		return nil, err
	}

	return imageNotifiter{notify: notify}, nil
}

func NewImageService(uc *biz.ImageUsecase, logger log.Logger) *ImageService {
	return &ImageService{
		uc:  uc,
		log: log.NewHelper(logger),
	}
}
