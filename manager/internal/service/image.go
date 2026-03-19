package service

import (
	"context"

	api "manager/api/image/v1"

	"github.com/go-kratos/kratos/v2/log"
)

type ImageService struct {
	log *log.Helper
}

func (s *ImageService) UploadImage(ctx context.Context, req *api.ImageUpload) (*api.ImageMeta, error) {
	return nil, nil // TODO: implement
}

func (s *ImageService) GetSingleImage(ctx context.Context, req *api.Image) (*api.ImageContent, error) {
	return nil, nil // TODO: implement
}

func (s *ImageService) GetPaginatedImage(ctx context.Context, req *api.Pagination) (api.ImageStream, error) {
	return nil, nil // TODO: implement
}

func (s *ImageService) GetImageMeta(ctx context.Context, req *api.Image) (*api.ImageMeta, error) {
	return nil, nil // TODO: implement
}

func (s *ImageService) TransformImage(ctx context.Context, req *api.ImageTransformations) (*api.ScheduledImageTransformation, error) {
	return nil, nil // TODO: implement
}

func (s *ImageService) ImageNotification(ctx context.Context) (api.ImageNotifier, error) {
	return nil, nil // TODO: implement
}

func NewImageService(logger log.Logger) *ImageService {
	return &ImageService{
		log: log.NewHelper(logger),
	}
}
