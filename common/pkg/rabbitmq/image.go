package rabbitmq

const (
	TransformationsExchange = "transformations"
	TransformationsKind     = "x-consistent-hash"
	EventsExchange          = "events"
	EventsKind              = "topic"
)

func TransformationsRoutingKey(username, imageId string) string {
	return TransformationsExchange + "." + username + "." + imageId
}

func EventsRoutingKey(username string) string {
	return EventsExchange + "." + username
}
