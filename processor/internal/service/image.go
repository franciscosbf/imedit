package service

import (
	"context"

	"processor/internal/biz"

	api "processor/api/image/v1"

	cbiz "github.com/franciscosbf/imedit/common/pkg/biz"
	"github.com/go-kratos/kratos/v2/log"
)

type ImageService struct {
	uc  *biz.ImageUsecase
	log *log.Helper
}

func (s *ImageService) TransformImage(ctx context.Context, req *api.ImageTransformations) error {
	s.log.WithContext(ctx).Infof("TransformImage %s", req.ImageId)

	transformations := cbiz.ImageTransformations{
		Rotate: req.Transformations.Rotate,
	}
	if crop := req.Transformations.Crop; crop != nil {
		transformations.Crop = &cbiz.CropImage{
			Width:  crop.Width,
			Height: crop.Height,
			X:      crop.X,
			Y:      crop.Y,
		}
	}
	if resize := req.Transformations.Resize; resize != nil {
		transformations.Resize = &cbiz.ResizeImage{
			Width:  resize.Width,
			Height: resize.Height,
		}
	}
	if format := req.Transformations.Format; format != nil {
		format := cbiz.FromRawImageEncoding(*format)
		transformations.Format = &format
	}

	return s.uc.Transform(ctx, req.Username, req.ImageId, &transformations)
}

func NewImageService(uc *biz.ImageUsecase, logger log.Logger) *ImageService {
	return &ImageService{
		uc:  uc,
		log: log.NewHelper(logger),
	}
}
