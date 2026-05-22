package data

type ObjectId struct {
	Username string
	ImageId  string
}

func (oid ObjectId) String() string {
	return oid.Username + "." + oid.ImageId
}

func NewObjectId(username, imageId string) ObjectId {
	return ObjectId{
		Username: username,
		ImageId:  imageId,
	}
}

type CachedImageId ObjectId

func (cid CachedImageId) String() string {
	return cid.Username + "." + cid.ImageId + ".image"
}

func NewCachedImageId(username, imageId string) CachedImageId {
	return CachedImageId(NewObjectId(username, imageId))
}

type CachedMetadataId ObjectId

func (cid CachedMetadataId) String() string {
	return cid.Username + "." + cid.ImageId + ".metadata"
}

func NewCachedMetadataId(username, imageId string) CachedMetadataId {
	return CachedMetadataId(NewObjectId(username, imageId))
}
