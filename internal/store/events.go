package store

import "context"

type EventBus interface {
	PublishEvent(ctx context.Context, topic string, payload []byte) error
	SubscribeEvents(topic string, fn func(payload []byte)) (func(), error)
}
