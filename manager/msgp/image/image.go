//go:generate msgp

package image

import "time"

type Metadata struct {
	ImageId      string    `msg:"image_id"`
	Name         string    `msg:"name"`
	Type         string    `msg:"type"`
	Size         uint32    `msg:"size"`
	Width        uint32    `msg:"width"`
	Height       uint32    `msg:"height"`
	LastModified time.Time `msg:"last_modified"`
}
