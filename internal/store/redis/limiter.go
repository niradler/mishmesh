package redis

import (
	"log/slog"

	"github.com/mishmesh/mishmesh/internal/ratelimit"
)

func (c *ConnStore) Limiter(log *slog.Logger) ratelimit.Limiter {
	return ratelimit.NewRedis(c.rdb, log)
}
