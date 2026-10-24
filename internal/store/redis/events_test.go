package redis

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"
)

func TestEventBusDeliversAcrossStores(t *testing.T) {
	mr := miniredis.RunT(t)
	publisher := newWithClient(goredis.NewClient(&goredis.Options{Addr: mr.Addr()}))
	subscriber := newWithClient(goredis.NewClient(&goredis.Options{Addr: mr.Addr()}))
	t.Cleanup(func() { _ = publisher.Close(); _ = subscriber.Close() })

	got := make(chan string, 1)
	stop, err := subscriber.SubscribeEvents("revoke", func(p []byte) { got <- string(p) })
	if err != nil {
		t.Fatal(err)
	}
	if err := publisher.PublishEvent(context.Background(), "other", []byte("ignored")); err != nil {
		t.Fatal(err)
	}
	if err := publisher.PublishEvent(context.Background(), "revoke", []byte("hello")); err != nil {
		t.Fatal(err)
	}
	select {
	case p := <-got:
		if p != "hello" {
			t.Fatalf("payload = %q, want hello", p)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("event not delivered")
	}
	stop()
	if err := publisher.PublishEvent(context.Background(), "revoke", []byte("late")); err != nil {
		t.Fatal(err)
	}
	select {
	case p := <-got:
		t.Fatalf("event %q delivered after unsubscribe", p)
	case <-time.After(200 * time.Millisecond):
	}
}

func TestEventBusSubscribeFailsWhenRedisDown(t *testing.T) {
	s := newWithClient(goredis.NewClient(&goredis.Options{Addr: "127.0.0.1:1", DialTimeout: 200 * time.Millisecond, MaxRetries: -1}))
	t.Cleanup(func() { _ = s.Close() })
	if _, err := s.SubscribeEvents("revoke", func([]byte) {}); err == nil {
		t.Fatal("subscribe succeeded with redis down")
	}
}
