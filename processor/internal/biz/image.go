package biz

import (
	"context"

	cbiz "github.com/franciscosbf/imedit/common/pkg/biz"
	"github.com/go-kratos/kratos/v2/log"
)

type ImageRepo interface {
	TransformImage(
		ctx context.Context,
		username, imageId string,
		transformations *cbiz.ImageTransformations) error

	EvictUserImage(ctx context.Context, username, imageId string) error
	EvictUserImageMetadata(ctx context.Context, username, imageId string) error
}

type ImageUsecase struct {
	repo ImageRepo
	log  *log.Helper
}

func (iu *ImageUsecase) Transform(
	ctx context.Context,
	username, imageId string,
	transformations *cbiz.ImageTransformations,
) error {
	if err := iu.repo.TransformImage(ctx, username, imageId, transformations); err != nil {
		return err
	}

	if err := iu.repo.EvictUserImage(ctx, username, imageId); err != nil {
		iu.log.Warnf("Failed to evict user image %s for user %s: %v", imageId, username, err)
	}

	if err := iu.repo.EvictUserImageMetadata(ctx, username, imageId); err != nil {
		iu.log.Warnf("Failed to evict user image metadata %s for user %s: %v", imageId, username, err)
	}

	return nil
}

func NewImageUsecase(repo ImageRepo, logger log.Logger) *ImageUsecase {
	return &ImageUsecase{
		repo: repo,
		log:  log.NewHelper(logger),
	}
}
