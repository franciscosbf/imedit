package data

import (
	"bytes"
	"context"
	"image"
	jpeg "image/jpeg"
	_ "image/png"
	"io"
	"strconv"

	"processor/internal/biz"

	"github.com/anthonynsimon/bild/imgio"
	"github.com/anthonynsimon/bild/transform"
	cbiz "github.com/franciscosbf/imedit/common/pkg/biz"
	cdata "github.com/franciscosbf/imedit/common/pkg/data"
	cminio "github.com/franciscosbf/imedit/common/pkg/minio"
	"github.com/go-kratos/kratos/v2/log"
	"github.com/minio/minio-go/v7"
)

type imageRepo struct {
	data *Data
	log  *log.Helper
}

func (ir *imageRepo) TransformImage(
	ctx context.Context,
	username, imageId string,
	transformations *cbiz.ImageTransformations,
) error {
	objId := cdata.NewObjectId(username, imageId)

	obj, err := ir.data.mdb.GetObject(
		ctx, cminio.ImagesBucket, objId.String(), minio.GetObjectOptions{},
	)
	if err != nil {
		return err
	}
	defer func() { _ = obj.Close() }()

	objStat, err := obj.Stat()
	if err != nil {
		return err
	}
	userMetadata := objStat.UserMetadata

	currFormat := cbiz.FromRawImageEncoding(userMetadata["Encoding"])
	encoding := currFormat
	if format := transformations.Format; format != nil {
		if *format != currFormat {
			encoding = *format
		} else if transformations.Crop == nil &&
			transformations.Resize == nil &&
			transformations.Rotate == nil {
			return nil
		}
	}

	content, err := io.ReadAll(obj)
	if err != nil {
		return err
	}

	currImgBuf := bytes.NewBuffer(content)
	img, _, err := image.Decode(currImgBuf)
	if err != nil {
		return err
	}

	var cropped, resized, rotated bool
	if crop := transformations.Crop; crop != nil {
		cx, cy, cw, ch := int(crop.X), int(crop.Y), int(crop.Width), int(crop.Height)
		if bounds := img.Bounds(); cx != 0 || cy != 0 || cw != bounds.Dx() || ch != bounds.Dy() {
			rect := image.Rect(cx, cy, cw, ch)
			img = transform.Crop(img, rect)
			cropped = true
		}
	}
	if resize := transformations.Resize; resize != nil {
		rw, rh := int(resize.Width), int(resize.Height)
		if bounds := img.Bounds(); rw != bounds.Dx() || rh != bounds.Dy() {
			img = transform.Resize(img, rw, rh, transform.Linear)
			resized = true
		}
	}
	if rotate := transformations.Rotate; rotate != nil {
		img = transform.Rotate(img, float64(*rotate), nil)
		rotated = true
	}

	if !cropped && !resized && !rotated {
		return nil
	}

	imgBounds := img.Bounds()
	userMetadata["Width"] = strconv.Itoa(imgBounds.Dx())
	userMetadata["Height"] = strconv.Itoa(imgBounds.Dy())

	var encode imgio.Encoder
	switch encoding {
	case cbiz.PngImage:
		encode = imgio.PNGEncoder()
	case cbiz.JpegImage:
		encode = imgio.JPEGEncoder(jpeg.DefaultQuality)
	}

	newImgBuf := bytes.Buffer{}
	if err := encode(&newImgBuf, img); err != nil {
		return err
	}

	userMetadata["Encoding"] = encoding.String()
	userMetadata["Size"] = strconv.Itoa(newImgBuf.Len())

	_, err = ir.data.mdb.PutObject(
		ctx,
		cminio.ImagesBucket,
		objId.String(), &newImgBuf, int64(newImgBuf.Len()),
		minio.PutObjectOptions{
			ContentType:  "application/octet-stream",
			UserMetadata: userMetadata,
		},
	)

	return err
}

func (ir *imageRepo) EvictUserImage(ctx context.Context, username, imageId string) error {
	cachedImgId := cdata.NewCachedImageId(username, imageId)

	return ir.data.rdb.Del(ctx, cachedImgId.String()).Err()
}

func (ir *imageRepo) EvictUserImageMetadata(ctx context.Context, username, imageId string) error {
	cachedMetaId := cdata.NewCachedMetadataId(username, imageId)

	return ir.data.rdb.Del(ctx, cachedMetaId.String()).Err()
}

func NewImageRepo(data *Data, logger log.Logger) biz.ImageRepo {
	return &imageRepo{
		data: data,
		log:  log.NewHelper(logger),
	}
}
