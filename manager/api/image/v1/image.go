package v1

import "context"

type ImageContent struct {
	Name    string
	Type    string
	Content []byte
}

type ImageStream interface {
	Next() (ImageContent, error)
}

type ImageUpload struct {
	Image ImageContent
}

type ImageMeta struct {
	ImageId string `json:"image_id"`
	Name    string `json:"name"`
	Type    string `json:"type"`
	Size    uint32 `json:"size"`
	Width   uint32 `json:"width"`
	Height  uint32 `json:"height"`
}

type Image struct {
	ImageId string `json:"image_id"`
}

type Pagination struct {
	Page  uint32 `json:"page"`
	Limit uint32 `json:"limit"`
}

type ResizeImage struct {
	Width  uint32 `json:"width"`
	Height uint32 `json:"height"`
}

type CropImage struct {
	Width  uint32 `json:"width"`
	Height uint32 `json:"height"`
	X      uint32 `json:"x"`
	Y      uint32 `json:"y"`
}

type FilterImage struct {
	Grayscale bool `json:"grayscale"`
	Sepia     bool `json:"sepia"`
}

type TransformImage struct {
	Resize *ResizeImage `json:"resize"`
	Crop   *CropImage   `json:"crop"`
	Rotate *uint32      `json:"rotate"`
	Format *string      `json:"format"`
}

type ImageTransformations struct {
	ImageId         string         `json:"image_id"`
	Transformations TransformImage `json:"transformations"`
}

type ScheduledImageTransformation struct {
	TransformationId string `json:"transformation_id"`
}

type EventType int

const (
	TransformedImage EventType = iota
	FailedImageTranformation
	UnexpectedError
)

func (e EventType) String() string {
	switch e {
	case TransformedImage:
		return "transformation_successful"
	case FailedImageTranformation:
		return "transformation_failure"
	case UnexpectedError:
		return "error"
	default:
		return "unknown"
	}
}

type Event interface {
	Type() EventType
}

type TransformedImageEvent struct {
	ImageId          string `json:"image_id"`
	TransformationId string `json:"transformation_id"`
}

func (*TransformedImageEvent) Type() EventType {
	return TransformedImage
}

type FailedImageTranformationEvent struct {
	ImageId          string `json:"image_id"`
	TransformationId string `json:"transformation_id"`
	Reason           string `json:"reason"`
}

func (*FailedImageTranformationEvent) Type() EventType {
	return FailedImageTranformation
}

type UnexpectedErrorEvent struct {
	Reason string `json:"reason"`
}

func (*UnexpectedErrorEvent) Type() EventType {
	return UnexpectedError
}

type ImageNotifier interface {
	Notify(ctx context.Context) (Event, error)
}
