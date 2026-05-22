package biz

import (
	"context"
	"time"

	iv1 "manager/api/image/v1"
	"manager/internal/auth"

	cbiz "github.com/franciscosbf/imedit/common/pkg/biz"
	"github.com/go-kratos/kratos/v2/log"
)

type ImageContent struct {
	Name     string
	Encoding cbiz.ImageEncoding
	Content  []byte
}

type StreamedImage struct {
	Image ImageContent
	Err   error
}

type StreamedImageId struct {
	ImageId string
	Err     error
}

type ImageMetadata struct {
	ImageId      string
	Name         string
	Encoding     cbiz.ImageEncoding
	Size         uint32
	Width        uint32
	Height       uint32
	LastModified time.Time
}

type ImagesPagination struct {
	Page  uint32
	Limit uint32
}

type ScheduledImageTransformation struct {
	TransformationId string
}

type EventType int

const (
	TransformedImage EventType = iota
	FailedImageTranformation
	UnexpectedError
)

type ImageEvent interface {
	Type() EventType
}

type TransformedImageEvent struct {
	ImageId          string
	TransformationId string
}

func (*TransformedImageEvent) Type() EventType {
	return TransformedImage
}

type FailedImageTranformationEvent struct {
	ImageId          string
	TransformationId string
	Reason           string
}

func (*FailedImageTranformationEvent) Type() EventType {
	return FailedImageTranformation
}

type UnexpectedErrorEvent struct {
	Reason string
}

func (*UnexpectedErrorEvent) Type() EventType {
	return UnexpectedError
}

type StreamedImageEvent struct {
	Event ImageEvent
	Err   error
}

type ImageRepo interface {
	StoreUserImage(ctx context.Context, username string, image *ImageContent) (*ImageMetadata, error)
	GetStoredUserImage(ctx context.Context, username, imageId string) (*ImageContent, error)
	GetStoredUserImageIds(
		ctx context.Context,
		username string,
		pagination *ImagesPagination) (func() *StreamedImageId, error)
	GetStoredUserImageMetadata(ctx context.Context, username, imageId string) (*ImageMetadata, error)
	TransformStoredUserImage(
		ctx context.Context,
		username, imageId string,
		transformations *cbiz.ImageTransformations) (*ScheduledImageTransformation, error)

	GetUserImageEvents(ctx context.Context, username string) (notify func() *StreamedImageEvent, err error)

	CacheUserImage(ctx context.Context, username, imageId string, image *ImageContent) error
	GetCachedUserImage(ctx context.Context, username, imageId string) (*ImageContent, error)
	CacheUserImageMetadata(ctx context.Context, username, imageId string, metadata *ImageMetadata) error
	GetCachedUserImageMetadata(ctx context.Context, username, imageId string) (*ImageMetadata, error)
}

type ImageUsecase struct {
	jwtAuth auth.JwtAuthenticator
	repo    ImageRepo
	log     *log.Helper
}

func (iu *ImageUsecase) extractUsername(ctx context.Context) string {
	return iu.jwtAuth.ExtractSub(ctx)
}

func (iu *ImageUsecase) Store(ctx context.Context, image *ImageContent) (*ImageMetadata, error) {
	if !image.Encoding.Supported() {
		return nil, iv1.ErrorImageNotSupported("image type is not supported")
	}

	username := iu.extractUsername(ctx)

	return iu.repo.StoreUserImage(ctx, username, image)
}

func (iu *ImageUsecase) Get(ctx context.Context, imageId string) (*ImageContent, error) {
	username := iu.extractUsername(ctx)

	if image, err := iu.repo.GetCachedUserImage(ctx, username, imageId); image != nil {
		return image, nil
	} else if err != nil {
		iu.log.Warnf("Failed to retrieve from cache image %s of user %s: %v", username, imageId, err)
	}

	image, err := iu.repo.GetStoredUserImage(ctx, username, imageId)
	if err != nil {
		return nil, err
	}

	return image, nil
}

func (iu *ImageUsecase) GetPaginated(
	ctx context.Context,
	pagination *ImagesPagination,
) (func() *StreamedImage, error) {
	username := iu.extractUsername(ctx)

	sImgIds, err := iu.repo.GetStoredUserImageIds(ctx, username, pagination)
	if err != nil {
		return nil, err
	}

	return func() *StreamedImage {
		sImgId := sImgIds()
		if sImgId == nil {
			return nil
		}

		if err := sImgId.Err; err != nil {
			return &StreamedImage{Err: err}
		}

		img, err := iu.Get(ctx, sImgId.ImageId)
		if err != nil {
			return &StreamedImage{Err: err}
		}

		return &StreamedImage{Image: *img}
	}, nil
}

func (iu *ImageUsecase) GetMetadata(ctx context.Context, imageId string) (*ImageMetadata, error) {
	username := iu.extractUsername(ctx)

	if metadata, err := iu.repo.GetCachedUserImageMetadata(ctx, username, imageId); metadata != nil {
		return metadata, nil
	} else if err != nil {
		iu.log.Warnf("Failed to retrieve from cache image metadata %s of user %s: %v", username, imageId, err)
	}

	metadata, err := iu.repo.GetStoredUserImageMetadata(ctx, username, imageId)
	if err != nil {
		return nil, err
	}

	if err := iu.repo.CacheUserImageMetadata(ctx, username, imageId, metadata); err != nil {
		iu.log.Warn("Failed to cache image metadata %s of user %s: %v", username, imageId, err)
	}

	return metadata, nil
}

func (iu *ImageUsecase) Transform(
	ctx context.Context,
	imageId string,
	transformations *cbiz.ImageTransformations,
) (*ScheduledImageTransformation, error) {
	if transformations.Format != nil && !transformations.Format.Supported() {
		return nil, iv1.ErrorImageNotSupported("image format is not supported")
	}

	username := iu.extractUsername(ctx)

	scheduled, err := iu.repo.TransformStoredUserImage(ctx, username, imageId, transformations)
	if err != nil {
		return nil, err
	}

	return scheduled, err
}

func (iu *ImageUsecase) GetEvents(ctx context.Context) (notify func() *StreamedImageEvent, err error) {
	username := iu.extractUsername(ctx)

	return iu.repo.GetUserImageEvents(ctx, username)
}

func NewImageUsecase(jwtAuth auth.JwtAuthenticator, repo ImageRepo, logger log.Logger) *ImageUsecase {
	return &ImageUsecase{
		jwtAuth: jwtAuth,
		repo:    repo,
		log:     log.NewHelper(logger),
	}
}
