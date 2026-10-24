package redis

import (
	"testing"

	"github.com/alicebob/miniredis/v2"
	goredis "github.com/redis/go-redis/v9"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/storetest"
)

func TestSessionStore(t *testing.T) {
	storetest.RunSessionStore(t, func(t *testing.T) store.SessionStore {
		mr := miniredis.RunT(t)
		rdb := goredis.NewClient(&goredis.Options{Addr: mr.Addr()})
		t.Cleanup(func() { _ = rdb.Close() })
		return newWithClient(rdb).Sessions()
	})
}
