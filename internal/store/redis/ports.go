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

func (p *PortClaims) Claim(ctx context.Context, endpointID string, requested int) (int, error) {
	if existing, err := p.rdb.Get(ctx, endpointPortKeyPrefix+endpointID).Int(); err == nil {
		return existing, nil
	} else if !errors.Is(err, goredis.Nil) {
		return 0, fmt.Errorf("redis: read port claim: %w", err)
	}
	if requested != 0 {
		if requested < p.portMin || requested > p.portMax {
			return 0, fmt.Errorf("tcp port %d outside cluster range %d-%d", requested, p.portMin, p.portMax)
		}
		ok, err := p.tryClaim(ctx, endpointID, requested)
		if err != nil {
			return 0, err
		}
		if !ok {
			return 0, fmt.Errorf("tcp port %d already in use", requested)
		}
		return requested, nil
	}
	for port := p.portMin; port <= p.portMax; port++ {
		ok, err := p.tryClaim(ctx, endpointID, port)
		if err != nil {
			return 0, err
		}
		if ok {
			return port, nil
		}
	}
	return 0, fmt.Errorf("no free tcp port in range %d-%d", p.portMin, p.portMax)
}

func (p *PortClaims) tryClaim(ctx context.Context, endpointID string, port int) (bool, error) {
	ok, err := p.rdb.SetNX(ctx, portKeyPrefix+strconv.Itoa(port), endpointID, 0).Result()
	if err != nil {
		return false, fmt.Errorf("redis: claim port %d: %w", port, err)
	}
	if !ok {
		return false, nil
	}
	if err := p.rdb.Set(ctx, endpointPortKeyPrefix+endpointID, port, 0).Err(); err != nil {
		p.rdb.Del(ctx, portKeyPrefix+strconv.Itoa(port))
		return false, fmt.Errorf("redis: record port claim: %w", err)
	}
	return true, nil
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
