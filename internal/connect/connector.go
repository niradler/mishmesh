package connect

import (
	"context"

	"github.com/mishmesh/mishmesh/internal/store"
)

type Status struct {
	Method  string
	Healthy bool
	Detail  string
}

type Connector interface {
	Method() string
	Provision(ctx context.Context, ep *store.Endpoint) (Status, error)
	Reconcile(ctx context.Context, ep *store.Endpoint) (Status, error)
	Teardown(ctx context.Context, ep *store.Endpoint) error
	Health(ctx context.Context, ep *store.Endpoint) (Status, error)
}

type Registry struct {
	byMethod map[string]Connector
}

func NewRegistry(connectors ...Connector) *Registry {
	r := &Registry{byMethod: make(map[string]Connector, len(connectors))}
	for _, c := range connectors {
		if c != nil {
			r.byMethod[c.Method()] = c
		}
	}
	return r
}

func (r *Registry) For(method string) (Connector, bool) {
	c, ok := r.byMethod[store.MethodOrDefault(method)]
	return c, ok
}
