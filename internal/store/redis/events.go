package redis

import (
	"context"
	"fmt"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

const (
	eventChannelPrefix    = "mm:events:"
	eventSubscribeTimeout = 5 * time.Second
)

var _ store.EventBus = (*ConnStore)(nil)

func (c *ConnStore) PublishEvent(ctx context.Context, topic string, payload []byte) error {
	if err := c.rdb.Publish(ctx, eventChannelPrefix+topic, payload).Err(); err != nil {
		return fmt.Errorf("redis: publish %s: %w", topic, err)
	}
	return nil
}

func (c *ConnStore) SubscribeEvents(topic string, fn func(payload []byte)) (func(), error) {
	ctx, cancel := context.WithCancel(context.Background())
	sub := c.rdb.Subscribe(ctx, eventChannelPrefix+topic)
	recvCtx, recvCancel := context.WithTimeout(ctx, eventSubscribeTimeout)
	defer recvCancel()
	if _, err := sub.Receive(recvCtx); err != nil {
		cancel()
		_ = sub.Close()
		return nil, fmt.Errorf("redis: subscribe %s: %w", topic, err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		for m := range sub.Channel() {
			fn([]byte(m.Payload))
		}
	}()
	return func() {
		cancel()
		_ = sub.Close()
		<-done
	}, nil
}
