//go:generate msgp

package msgp

import (
	"encoding/binary"
	"fmt"

	"github.com/tinylib/msgp/msgp"
)

type Resize struct {
	Width  uint32 `msg:"width"`
	Height uint32 `msg:"height"`
}

type Crop struct {
	Width  uint32 `msg:"width"`
	Height uint32 `msg:"height"`
	X      uint32 `msg:"y"`
	Y      uint32 `msg:"x"`
}

type Filter struct {
	Grayscale bool `msg:"grayscale"`
	Sepia     bool `msg:"sepia"`
}

type Transformations struct {
	TransformationId string  `msg:"transformation_id"`
	Username         string  `msg:"username"`
	ImageId          string  `msg:"image_id"`
	Crop             *Crop   `msg:"crop"`
	Resize           *Resize `msg:"resize"`
	Filter           *Filter `msg:"filter"`
	Rotate           *uint32 `msg:"rotate"`
	Format           *string `msg:"format"`
}

type EventType uint32

const (
	TransformedImage EventType = iota
	FailedImageTranformation
)

func (e *EventType) ExtensionType() int8 {
	return 88
}

func (e *EventType) Len() int {
	return 4
}

func (e *EventType) MarshalBinaryTo(b []byte) error {
	binary.BigEndian.PutUint32(b, uint32(*e))

	return nil
}

func (e *EventType) UnmarshalBinary(b []byte) error {
	*e = EventType(binary.BigEndian.Uint32(b))

	return nil
}

type Event interface {
	Type() EventType
}

//msgp:ignore EventPack
type EventPack struct {
	Event
}

func (ep *EventPack) EncodeMsg(w *msgp.Writer) error {
	if err := ep.Event.Type().EncodeMsg(w); err != nil {
		return err
	}

	switch ep.Type() {
	case TransformedImage:
		return ep.Event.(*TransformedImageEvent).EncodeMsg(w)
	case FailedImageTranformation:
		return ep.Event.(*FailedImageTranformationEvent).EncodeMsg(w)
	default:
		return fmt.Errorf("unknown EventType %v", ep.Type())
	}
}

func (ep *EventPack) DecodeMsg(r *msgp.Reader) (err error) {
	var ev EventType
	if err = ev.DecodeMsg(r); err != nil {
		return
	}

	var e Event
	switch ev {
	case TransformedImage:
		ce := &TransformedImageEvent{}
		err = ce.DecodeMsg(r)
		e = ce
	case FailedImageTranformation:
		ce := &FailedImageTranformationEvent{}
		err = ce.DecodeMsg(r)
		e = ce
	default:
		err = fmt.Errorf("unknown EventType %v", ev)
	}
	if err != nil {
		return
	}

	ep.Event = e

	return
}

type TransformedImageEvent struct {
	TransformationId string `msg:"transformation_id"`
	ImageId          string `msg:"image_id"`
}

func (*TransformedImageEvent) Type() EventType {
	return TransformedImage
}

type FailedImageTranformationEvent struct {
	TransformationId string `msg:"transformation_id"`
	ImageId          string `msg:"image_id"`
	Reason           string `msg:"reason"`
}

func (*FailedImageTranformationEvent) Type() EventType {
	return FailedImageTranformation
}
