package orgsecret

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

const (
	invalidateTopic   = "secret"
	busPublishTimeout = 2 * time.Second
)

type invalidation struct {
	Origin string `json:"origin"`
	OrgID  string `json:"org"`
	Name   string `json:"name"`
}

func (v *Vault) AttachBus(bus store.EventBus, log *slog.Logger) (func(), error) {
	if log == nil {
		log = slog.Default()
	}
	stop, err := bus.SubscribeEvents(invalidateTopic, v.handleRemoteInvalidation)
	if err != nil {
		return nil, fmt.Errorf("orgsecret: subscribe invalidations: %w", err)
	}
	v.mu.Lock()
	v.bus, v.busLog = bus, log
	v.mu.Unlock()
	return func() {
		v.mu.Lock()
		v.bus = nil
		v.mu.Unlock()
		stop()
	}, nil
}

func (v *Vault) invalidateEverywhere(key secretKey) {
	v.invalidate(key)
	v.mu.Lock()
	bus, log := v.bus, v.busLog
	v.mu.Unlock()
	if bus == nil {
		return
	}
	body, err := json.Marshal(invalidation{Origin: v.origin, OrgID: key.orgID, Name: key.name})
	if err != nil {
		log.Error("encode secret invalidation failed", "err", err)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), busPublishTimeout)
	defer cancel()
	if err := bus.PublishEvent(ctx, invalidateTopic, body); err != nil {
		log.Error("publish secret invalidation failed; other nodes serve the old value until their cache expires", "org", key.orgID, "secret", key.name, "err", err)
	}
}

func (v *Vault) handleRemoteInvalidation(payload []byte) {
	var msg invalidation
	if json.Unmarshal(payload, &msg) != nil || msg.Origin == "" || msg.Origin == v.origin || msg.OrgID == "" || !store.ValidCredentialName(msg.Name) {
		return
	}
	v.invalidate(secretKey{msg.OrgID, msg.Name})
}
