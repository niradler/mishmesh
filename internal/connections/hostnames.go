package connections

import (
	"context"

	"github.com/mishmesh/mishmesh/internal/store"
)

type HostnameManager interface {
	AddHostname(ctx context.Context, host, service string) error
	RemoveHostname(ctx context.Context, host string) error
}

type HostnameErrorKind string

const (
	HostnameInvalid     HostnameErrorKind = "invalid"
	HostnameConflict    HostnameErrorKind = "conflict"
	HostnameNotFound    HostnameErrorKind = "not_found"
	HostnameInUse       HostnameErrorKind = "in_use"
	HostnameUnsupported HostnameErrorKind = "unsupported"
)

type HostnameError struct {
	Kind        HostnameErrorKind
	Message     string
	EndpointIDs []string
}

func (e *HostnameError) Error() string { return e.Message }

func (h *Hub) AddHostnameIfCurrent(ctx context.Context, expected store.Connection, host, service string) error {
	manager, err := h.hostnameManager(expected)
	if err != nil {
		return err
	}
	return manager.AddHostname(ctx, host, service)
}

func (h *Hub) RemoveHostnameIfCurrent(ctx context.Context, expected store.Connection, host string) error {
	manager, err := h.hostnameManager(expected)
	if err != nil {
		return err
	}
	return manager.RemoveHostname(ctx, host)
}

func (h *Hub) hostnameManager(expected store.Connection) (HostnameManager, error) {
	h.mu.RLock()
	run := h.runs[expected.ID]
	h.mu.RUnlock()
	switch {
	case run == nil || run.ctx.Err() != nil:
		return nil, ErrNotConnected
	case !store.SameConnectionInput(run.record, expected):
		return nil, store.ErrConflict
	}
	manager, ok := run.instance.(HostnameManager)
	if !ok {
		return nil, &HostnameError{Kind: HostnameUnsupported, Message: "this connection type cannot manage hostnames"}
	}
	return manager, nil
}
