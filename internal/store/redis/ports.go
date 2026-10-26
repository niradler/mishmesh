package redis

import (
	"context"
	"errors"
	"fmt"
	"strconv"

	goredis "github.com/redis/go-redis/v9"
)

const (
	portKeyPrefix         = "mm:port:"
	endpointPortKeyPrefix = "mm:epport:"
)

type PortClaims struct {
	rdb     *goredis.Client
	portMin int
	portMax int
}

func (c *ClusterConnStore) PortClaims(portMin, portMax int) *PortClaims {
	return &PortClaims{rdb: c.rdb, portMin: portMin, portMax: portMax}
}

var claimPortScript = goredis.NewScript(`
local existing = redis.call('GET', KEYS[1])
if existing then
  return tonumber(existing)
end
local id = ARGV[1]
local requested = tonumber(ARGV[2])
local first = tonumber(ARGV[3])
local last = tonumber(ARGV[4])
local prefix = ARGV[5]
local function claim(port)
  if redis.call('SET', prefix .. port, id, 'NX') then
    redis.call('SET', KEYS[1], port)
    return true
  end
  return false
end
if requested ~= 0 then
  if requested < first or requested > last then
    return -2
  end
  if claim(requested) then
    return requested
  end
  return -1
end
for port = first, last do
  if claim(port) then
    return port
  end
end
return 0
`)

func (p *PortClaims) Claim(ctx context.Context, endpointID string, requested int) (int, error) {
	port, err := claimPortScript.Run(ctx, p.rdb, []string{endpointPortKeyPrefix + endpointID}, endpointID, requested, p.portMin, p.portMax, portKeyPrefix).Int()
	if err != nil {
		return 0, fmt.Errorf("redis: claim port: %w", err)
	}
	switch {
	case port == -2:
		return 0, fmt.Errorf("tcp port %d outside cluster range %d-%d", requested, p.portMin, p.portMax)
	case port == -1:
		return 0, fmt.Errorf("tcp port %d already in use", requested)
	case port == 0:
		return 0, fmt.Errorf("no free tcp port in range %d-%d", p.portMin, p.portMax)
	}
	return port, nil
}

func (p *PortClaims) Release(ctx context.Context, endpointID string) error {
	port, err := p.rdb.Get(ctx, endpointPortKeyPrefix+endpointID).Int()
	if errors.Is(err, goredis.Nil) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("redis: read port claim: %w", err)
	}
	if err := releaseOwnerScript.Run(ctx, p.rdb, []string{portKeyPrefix + strconv.Itoa(port)}, endpointID).Err(); err != nil {
		return fmt.Errorf("redis: release port %d: %w", port, err)
	}
	if err := p.rdb.Del(ctx, endpointPortKeyPrefix+endpointID).Err(); err != nil {
		return fmt.Errorf("redis: clear port claim: %w", err)
	}
	return nil
}

func (p *PortClaims) Lookup(ctx context.Context, port int) (string, bool) {
	id, err := p.rdb.Get(ctx, portKeyPrefix+strconv.Itoa(port)).Result()
	if err != nil {
		return "", false
	}
	return id, true
}
