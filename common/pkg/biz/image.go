package biz

type ImageEncoding int

func (it ImageEncoding) Supported() bool {
	return it != NotSupportedEncoding
}

const (
	PngImage ImageEncoding = iota
	JpegImage
	NotSupportedEncoding
)

func FromRawImageEncoding(raw string) ImageEncoding {
	switch raw {
	case "png":
		return PngImage
	case "jpeg":
		return JpegImage
	default:
		return NotSupportedEncoding
	}
}

func (it ImageEncoding) String() string {
	switch it {
	case PngImage:
		return "png"
	case JpegImage:
		return "jpeg"
	default:
		return "unsupported"
	}
}

type CropImage struct {
	Width  uint32
	Height uint32
	X      uint32
	Y      uint32
}

type ResizeImage struct {
	Width  uint32
	Height uint32
}

type FilterImage struct {
	Grayscale bool
	Sepia     bool
}

type ImageTransformations struct {
	Crop   *CropImage
	Resize *ResizeImage
	Filter *FilterImage
	Rotate *uint32
	Format *ImageEncoding
}
