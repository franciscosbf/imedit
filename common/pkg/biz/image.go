package biz

type ImageEncoding int

func (it ImageEncoding) Supported() bool {
	return it != notSupportedEncoding
}

const (
	pngImage ImageEncoding = iota
	jpegImage
	notSupportedEncoding
)

func FromRawImageEncoding(raw string) ImageEncoding {
	switch raw {
	case "png":
		return pngImage
	case "jpeg":
		return jpegImage
	default:
		return notSupportedEncoding
	}
}

func (it ImageEncoding) String() string {
	switch it {
	case pngImage:
		return "png"
	case jpegImage:
		return "jpeg"
	default:
		return "unsupported"
	}
}

type ResizeImage struct {
	Width  uint32
	Height uint32
}

type CropImage struct {
	Width  uint32
	Height uint32
	X      uint32
	Y      uint32
}

type FilterImage struct {
	Grayscale bool
	Sepia     bool
}

type ImageTransformations struct {
	Resize *ResizeImage
	Crop   *CropImage
	Rotate *uint32
	Format *ImageEncoding
}
