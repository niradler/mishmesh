package main

import (
	"fmt"

	"github.com/mishmesh/mishmesh/internal/config"
	"github.com/mishmesh/mishmesh/internal/identity"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/token"
)

func newIdentityService(cfg config.Server, conns store.ConnectionStore) (*identity.Service, error) {
	keys, err := token.LoadOrCreateKeyring(cfg.EffectiveTokenKeyFile())
	if err != nil {
		return nil, fmt.Errorf("token signing key: %w", err)
	}
	sessions := store.SessionStore(memory.NewSessionStore())
	if provider, ok := conns.(interface{ Sessions() store.SessionStore }); ok {
		sessions = provider.Sessions()
	}
	return identity.New(keys, sessions), nil
}
