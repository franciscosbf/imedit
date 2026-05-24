package v1

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

type TransformImage struct {
	Crop   *CropImage
	Resize *ResizeImage
	Rotate *uint32
	Format *string
}

type ImageTransformations struct {
	Username        string
	ImageId         string
	Transformations TransformImage
}
