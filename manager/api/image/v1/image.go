package v1

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
	Id     string `json:"id"`
	Name   string `json:"name"`
	Type   string `json:"type"`
	Size   uint32 `json:"size"`
	Width  uint32 `json:"width"`
	Height uint32 `json:"height"`
}

type Image struct {
	Id string `json:"id"`
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
	Id              string         `json:"id"`
	Transformations TransformImage `json:"transformations"`
}

type ScheduledTransformation struct {
	TransformationId string `json:"transformation_id"`
}

type EventType int

const (
	TransformedImage EventType = iota
)

type Event interface {
	Type() EventType
}

type TransformedImageEvent struct {
	Id               string `json:"id"`
	TransformationId string `json:"transformation_id"`
}

func (TransformedImageEvent) Type() EventType {
	return TransformedImage
}

type ImageNotifier interface {
	Notify(event Event) error
}
