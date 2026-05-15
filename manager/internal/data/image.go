package data

import (
	"bytes"
	"context"
	"fmt"
	imd "image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"strconv"
	"time"

	iv1 "manager/api/image/v1"
	"manager/ent"
	"manager/ent/user"
	"manager/internal/biz"
	"manager/msgp/image"

	"github.com/cloudresty/go-rabbitmq"
	cminio "github.com/franciscosbf/imedit/common/pkg/minio"
	cmsgp "github.com/franciscosbf/imedit/common/pkg/msgp"
	crabbitmq "github.com/franciscosbf/imedit/common/pkg/rabbitmq"
	"github.com/go-kratos/kratos/v2/log"

	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"
	"github.com/tinylib/msgp/msgp"
)

func genUUID() (string, error) {
	id, err := uuid.NewRandom()
	if err != nil {
		return "", err
	}

	return id.String(), nil
}

type messageEvent struct {
	event cmsgp.Event
	err   error
}

type objectId struct {
	username string
	imageId  string
}

func (oid objectId) String() string {
	return oid.username + "." + oid.imageId
}

func newObjectId(username, imageId string) objectId {
	return objectId{
		username: username,
		imageId:  imageId,
	}
}

type cachedImageId objectId

func (coid cachedImageId) String() string {
	return coid.username + "." + coid.imageId + ".image"
}

func newCachedImageId(username, imageId string) cachedImageId {
	return cachedImageId(newObjectId(username, imageId))
}

type cachedMetadataId objectId

func (coid cachedMetadataId) String() string {
	return coid.username + "." + coid.imageId + ".metadata"
}

func newCachedMetadataId(username, imageId string) cachedMetadataId {
	return cachedMetadataId(newObjectId(username, imageId))
}

func isObjectNotFoundErr(err error) bool {
	return minio.ToErrorResponse(err).Code == "NoSuchKey"
}

type objectContent struct {
	name     string
	encoding string
	content  []byte
}

type imageRepo struct {
	data                  *Data
	log                   *log.Helper
	transformationTimeout time.Duration
}

func (ir *imageRepo) getObject(ctx context.Context, objId objectId) (objectContent, error) {
	obj, err := ir.data.mdb.GetObject(ctx, cminio.ImagesBucket, objId.String(), minio.GetObjectOptions{})
	if err != nil {
		if isObjectNotFoundErr(err) {
			return objectContent{}, iv1.ErrorImageNotFound("image %s wasn't found", objId.imageId)
		}

		return objectContent{}, err
	}
	defer func() { _ = obj.Close() }()

	stat, err := obj.Stat()
	if err != nil {
		return objectContent{}, err
	}

	content, err := io.ReadAll(obj)
	if err != nil {
		return objectContent{}, err
	}

	return objectContent{
		name:     stat.UserMetadata["Name"],
		encoding: stat.UserMetadata["Encoding"],
		content:  content,
	}, nil
}

func (ir *imageRepo) StoreUserImage(
	ctx context.Context,
	username string, image *biz.ImageContent,
) (*biz.ImageMetadata, error) {
	imgId, err := genUUID()
	if err != nil {
		return nil, err
	}
	objId := newObjectId(username, imgId)

	imgConf, _, err := imd.DecodeConfig(bytes.NewBuffer(image.Content))
	if err != nil {
		return nil, err
	}

	imgBuf := bytes.NewBuffer(image.Content)
	imgBufLen := imgBuf.Len()
	userMetadata := map[string]string{
		"Image-Id": imgId,
		"Name":     image.Name,
		"Encoding": image.Encoding.String(),
		"Size":     strconv.Itoa(imgBufLen),
		"Width":    strconv.Itoa(imgConf.Width),
		"Height":   strconv.Itoa(imgConf.Height),
	}

	if err := ir.data.withEntTx(ctx, func(tx *ent.Tx) error {
		i, err := tx.Image.Create().
			SetImageID(imgId).
			Save(ctx)
		if err != nil {
			return err
		}

		u, err := tx.User.Query().
			Where(user.Username(username)).
			Only(ctx)
		if err != nil {
			return err
		}

		if _, err := u.Update().
			AddImages(i).
			Save(ctx); err != nil {
			return err
		}

		if _, err := ir.data.mdb.PutObject(
			ctx,
			cminio.ImagesBucket,
			objId.String(), imgBuf, int64(imgBuf.Len()),
			minio.PutObjectOptions{
				ContentType:  "application/octet-stream",
				UserMetadata: userMetadata,
			},
		); err != nil {
			return err
		}

		return nil
	}); err != nil {
		return nil, err
	}

	return &biz.ImageMetadata{
		ImageId:  imgId,
		Name:     image.Name,
		Encoding: image.Encoding,
		Size:     uint32(imgBufLen),
		Width:    uint32(imgConf.Width),
		Height:   uint32(imgConf.Height),
	}, nil
}

func (ir *imageRepo) GetStoredUserImage(
	ctx context.Context,
	username, imageId string,
) (*biz.ImageContent, error) {
	objId := newObjectId(username, imageId)

	objContent, err := ir.getObject(ctx, objId)
	if err != nil {
		return nil, err
	}

	return &biz.ImageContent{
		Name:     objContent.name,
		Encoding: biz.FromRawImageEncoding(objContent.encoding),
		Content:  objContent.content,
	}, nil
}

func (ir *imageRepo) GetStoredUserImageIds(
	ctx context.Context,
	username string,
	pagination *biz.ImagesPagination,
) (func() *biz.StreamedImageId, error) {
	u, err := ir.data.edb.User.Query().
		Where(user.Username(username)).
		Only(ctx)
	if err != nil {
		return nil, err
	}
	is, err := u.QueryImages().
		Offset(int((pagination.Page - 1) * pagination.Limit)).
		Limit(int(pagination.Limit)).
		All(ctx)
	if err != nil {
		return nil, err
	}

	terminated := len(is) == 0
	imgIdx := 0
	return func() (sImg *biz.StreamedImageId) {
		if terminated {
			return nil
		}

		defer func() {
			if sImg == nil || sImg.Err != nil || imgIdx == len(is) {
				terminated = true
			}
		}()

		sImg = &biz.StreamedImageId{ImageId: is[imgIdx].ImageID}
		imgIdx++

		return
	}, nil
}

func (ir *imageRepo) GetStoredUserImageMetadata(
	ctx context.Context,
	username, imageId string,
) (*biz.ImageMetadata, error) {
	objId := newObjectId(username, imageId)

	objInfo, err := ir.data.mdb.StatObject(ctx, cminio.ImagesBucket, objId.String(), minio.StatObjectOptions{})
	if err != nil {
		if isObjectNotFoundErr(err) {
			return nil, iv1.ErrorImageNotFound("image metadata %s wasn't found", objId.imageId)
		}

		return nil, err
	}

	usrMeta := objInfo.UserMetadata
	size, _ := strconv.ParseUint(usrMeta["Size"], 10, 32)
	width, _ := strconv.ParseUint(usrMeta["Width"], 10, 32)
	height, _ := strconv.ParseUint(usrMeta["Height"], 10, 32)
	return &biz.ImageMetadata{
		ImageId:      usrMeta["Image-Id"],
		Name:         usrMeta["Name"],
		Encoding:     biz.FromRawImageEncoding(usrMeta["Encoding"]),
		Size:         uint32(size),
		Width:        uint32(width),
		Height:       uint32(height),
		LastModified: objInfo.LastModified.UTC(),
	}, nil
}

func (ir *imageRepo) TransformStoredUserImage(
	ctx context.Context,
	username, imageId string,
	transformations *biz.ImageTransformations,
) (*biz.ScheduledImageTransformation, error) {
	tId, err := genUUID()
	if err != nil {
		return nil, err
	}
	mT := cmsgp.Transformations{
		TransformationId: tId,
		Username:         username,
		ImageId:          imageId,
		Rotate:           transformations.Rotate,
	}
	if transformations.Resize != nil {
		mT.Resize = &cmsgp.Resize{
			Width:  transformations.Resize.Width,
			Height: transformations.Resize.Height,
		}
	}
	if transformations.Crop != nil {
		mT.Crop = new(cmsgp.Crop)
		mT.Crop.Width = transformations.Crop.Width
		mT.Crop.Height = transformations.Crop.Height
		mT.Crop.X = transformations.Crop.X
		mT.Crop.Y = transformations.Crop.Y
	}
	if transformations.Format != nil {
		mT.Format = new(string)
		*mT.Format = transformations.Format.String()
	}

	rawBuf := bytes.Buffer{}
	if err := msgp.Encode(&rawBuf, &mT); err != nil {
		return nil, err
	}

	client, err := ir.data.rmq.Get()
	if err != nil {
		return nil, err
	}

	publisher, err := client.NewPublisher(rabbitmq.WithDeliveryAssurance())
	if err != nil {
		return nil, err
	}
	defer func() { _ = publisher.Close() }()

	routingKey := crabbitmq.TransformationsRoutingKey(username, imageId)
	message := &rabbitmq.Message{
		ContentType: "application/octet-stream",
		Body:        rawBuf.Bytes(),
	}
	deliveryOpts := rabbitmq.DeliveryOptions{
		MessageID: uuid.NewString(),
		Mandatory: true,
		Callback: func(_ string, outcome rabbitmq.DeliveryOutcome, errorMessage string) {
			var msg string
			switch outcome {
			case rabbitmq.DeliverySuccess:
				return
			case rabbitmq.DeliveryFailed:
				msg = "No worker available to process transformation"
			case rabbitmq.DeliveryNacked:
				msg = "Worker aborted transformation"
			case rabbitmq.DeliveryTimeout:
				msg = "Unable process transformation on time"
			}
			msg = fmt.Sprintf("%s: %s", msg, errorMessage)

			client, err := ir.data.rmq.Get()
			if err != nil {
				log.Warnf(
					"Failed to obtain RabbitMQ client to "+
						"deliver transformation error '%s': %v", msg, err,
				)

				return
			}

			publisher, err := client.NewPublisher()
			if err != nil {
				log.Warnf(
					"Failed to create RabbitMQ event publisher to "+
						"deliver transformation error '%s': %v", msg, err,
				)

				return
			}
			defer func() { _ = publisher.Close() }()

			event := cmsgp.EventPack{
				Event: &cmsgp.FailedImageTranformationEvent{
					ImageId:          imageId,
					TransformationId: tId,
					Reason:           msg,
				},
			}
			rawBuf := bytes.Buffer{}
			if err := msgp.Encode(&rawBuf, &event); err != nil {
				log.Warnf(
					"Failed to encode UnexpectedErrorEvent with "+
						"deliver transformation error '%s': %v", msg, err,
				)

				return
			}

			routingKey := crabbitmq.EventsRoutingKey(username)
			if err := publisher.Publish(
				context.Background(), crabbitmq.EventsExchange, routingKey, message,
			); err != nil {
				log.Warnf(
					"Failed to publish UnexpectedErrorEvent with "+
						"deliver transformation error '%s' to '%s': %v",
					msg, routingKey, err,
				)
			}
		},
		Timeout: ir.transformationTimeout,
	}
	if err := publisher.PublishWithDeliveryAssurance(
		ctx, crabbitmq.TransformationsExchange, routingKey, message, deliveryOpts,
	); err != nil {
		return nil, err
	}

	return &biz.ScheduledImageTransformation{
		TransformationId: tId,
	}, nil
}

func (ir *imageRepo) GetUserImageEvents(
	ctx context.Context,
	username string,
) (notify func() *biz.StreamedImageEvent, err error) {
	var cli *rabbitmq.Client
	cli, err = ir.data.rmq.Get()
	if err != nil {
		return
	}

	admin := cli.Admin()

	var q *rabbitmq.Queue
	q, err = admin.DeclareQueue(ctx, "", rabbitmq.WithAutoDelete(), rabbitmq.WithClassicQueue())
	if err != nil {
		return
	}

	routingKey := crabbitmq.EventsRoutingKey(username)
	if err = admin.BindQueue(
		ctx, q.Name, crabbitmq.EventsExchange, routingKey,
	); err != nil {
		return
	}

	consumer, err := cli.NewConsumer(
		rabbitmq.WithExclusiveConsumer(), rabbitmq.WithAutoAck(),
	)
	if err != nil {
		return
	}

	msgs := make(chan messageEvent)
	cancelCh := make(chan struct{})

	go func() {
		routingKey := crabbitmq.EventsRoutingKey(username)
		queue := routingKey
		handler := func(_ context.Context, delivery *rabbitmq.Delivery) error {
			var event cmsgp.EventPack
			buf := bytes.NewBuffer(delivery.Body)
			if err := msgp.Decode(buf, &event); err != nil {
				return err
			}

			select {
			case <-cancelCh:
			case msgs <- messageEvent{event: event}:
			}

			return nil
		}
		if err := consumer.Consume(context.Background(), queue, handler); err != nil {
			msgs <- messageEvent{err: err}
			close(cancelCh)
		}
	}()

	notify = func() *biz.StreamedImageEvent {
		var (
			msg    messageEvent
			sevent biz.StreamedImageEvent
		)

		select {
		case msg = <-msgs:
		case <-cancelCh:
			return nil
		}

		if msg.event != nil {
			switch msg.event.Type() {
			case cmsgp.TransformedImage:
				e := msg.event.(*cmsgp.TransformedImageEvent)
				sevent.Event = &biz.TransformedImageEvent{
					ImageId:          e.ImageId,
					TransformationId: e.TransformationId,
				}
			case cmsgp.FailedImageTranformation:
				e := msg.event.(*cmsgp.FailedImageTranformationEvent)
				sevent.Event = &biz.FailedImageTranformationEvent{
					ImageId:          e.ImageId,
					TransformationId: e.TransformationId,
					Reason:           e.Reason,
				}
			default:
				sevent.Event = &biz.UnexpectedErrorEvent{
					Reason: "Received unknown event",
				}
			}
		} else if msg.err != nil {
			sevent.Err = msg.err
		} else {
			return nil
		}

		return &sevent
	}

	return
}

func (ir *imageRepo) CacheUserImage(
	ctx context.Context,
	username, imageId string,
	image *biz.ImageContent,
) error {
	cachedImgId := newCachedImageId(username, imageId)

	return ir.data.rdb.Set(ctx, cachedImgId.String(), image.Content, ir.data.ch.eviction).Err()
}

func (ir *imageRepo) EvictUserImage(ctx context.Context, username, imageId string) error {
	cachedImgId := newCachedImageId(username, imageId)

	return ir.data.rdb.Del(ctx, cachedImgId.String()).Err()
}

func (ir *imageRepo) GetCachedUserImage(
	ctx context.Context,
	username, imageId string,
) (*biz.ImageContent, error) {
	cachedImgId := newCachedImageId(username, imageId)

	cmd := ir.data.rdb.Get(ctx, cachedImgId.String())
	if err := cmd.Err(); err != nil {
		if err == redis.Nil {
			return nil, nil
		}

		return nil, err
	}

	content, err := cmd.Bytes()
	if err != nil {
		return nil, err
	}

	return &biz.ImageContent{Content: content}, nil
}

func (ir *imageRepo) CacheUserImageMetadata(
	ctx context.Context,
	username, imageId string,
	metadata *biz.ImageMetadata,
) error {
	cachedMetaId := newCachedMetadataId(username, imageId)

	cMeta := image.Metadata{
		ImageId:      metadata.ImageId,
		Name:         metadata.Name,
		Type:         metadata.Encoding.String(),
		Size:         metadata.Size,
		Width:        metadata.Width,
		Height:       metadata.Height,
		LastModified: metadata.LastModified,
	}
	rawBuf := bytes.Buffer{}
	if err := msgp.Encode(&rawBuf, &cMeta); err != nil {
		return err
	}

	return ir.data.rdb.Set(ctx, cachedMetaId.String(), rawBuf.Bytes(), ir.data.ch.eviction).Err()
}

func (ir *imageRepo) EvictUserImageMetadata(ctx context.Context, username, imageId string) error {
	cachedMetaId := newCachedMetadataId(username, imageId)

	return ir.data.rdb.Del(ctx, cachedMetaId.String()).Err()
}

func (ir *imageRepo) GetCachedUserImageMetadata(
	ctx context.Context,
	username, imageId string,
) (*biz.ImageMetadata, error) {
	cachedMetaId := newCachedMetadataId(username, imageId)

	cmd := ir.data.rdb.Get(ctx, cachedMetaId.String())
	if err := cmd.Err(); err != nil {
		return nil, err
	}

	rawMeta, err := cmd.Bytes()
	if err != nil {
		return nil, err
	}

	rawBuf := bytes.NewBuffer(rawMeta)
	cMeta := image.Metadata{}
	if err := msgp.Decode(rawBuf, &cMeta); err != nil {
		return nil, err
	}

	return &biz.ImageMetadata{
		ImageId:  cMeta.ImageId,
		Name:     cMeta.Name,
		Encoding: biz.FromRawImageEncoding(cMeta.Type),
		Size:     cMeta.Size,
		Width:    cMeta.Width,
		Height:   cMeta.Height,
	}, nil
}

func NewImageRepo(data *Data, logger log.Logger) biz.ImageRepo {
	repo := &imageRepo{
		data: data,
		log:  log.NewHelper(logger),
	}

	if timeout := data.config.Event.TransformationTimeout; timeout != nil {
		repo.transformationTimeout = timeout.AsDuration()
	}

	return repo
}
