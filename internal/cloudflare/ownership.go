package cloudflare

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"sort"
	"strings"
	"sync"

	"github.com/mishmesh/mishmesh/internal/store"
)

type ownedRoute struct {
	Ingress string   `json:"ingress,omitempty"`
	DNS     []string `json:"dns,omitempty"`
	Access  string   `json:"access,omitempty"`
}

type ownershipState struct {
	Version     int                   `json:"version"`
	AccountID   string                `json:"account_id"`
	TunnelID    string                `json:"tunnel_id"`
	AccessAppID string                `json:"access_app_id"`
	Origin      string                `json:"origin"`
	Routes      map[string]ownedRoute `json:"routes"`
}

type routeOwnership struct {
	state ownershipState
	data  store.DataStore
	cfg   *Config
}

func openOwnership(raw json.RawMessage, cfg *Config, data store.DataStore) (*routeOwnership, error) {
	state := ownershipState{Version: 1, AccountID: cfg.AccountID, TunnelID: cfg.TunnelID, AccessAppID: cfg.AccessAppID, Origin: cfg.Origin, Routes: make(map[string]ownedRoute)}
	if len(raw) > 0 {
		var stored ownershipState
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&stored) != nil || stored.Version != 1 || stored.Routes == nil || decoder.Decode(new(any)) != io.EOF {
			return nil, errors.New("invalid cloudflare ownership state")
		}
		for host, owned := range stored.Routes {
			if host == "" || host != normalizeHost(host) || !validFingerprint(owned.Ingress) || !validFingerprint(owned.Access) {
				return nil, errors.New("invalid cloudflare ownership state")
			}
			for _, fingerprint := range owned.DNS {
				if fingerprint == "" || !validFingerprint(fingerprint) {
					return nil, errors.New("invalid cloudflare ownership state")
				}
			}
		}
		if stored.AccountID == state.AccountID && stored.TunnelID == state.TunnelID && stored.AccessAppID == state.AccessAppID && stored.Origin == state.Origin {
			state = stored
		}
	}
	return &routeOwnership{state: state, data: data, cfg: cfg}, nil
}

func validFingerprint(value string) bool {
	if value == "" {
		return true
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size
}

func resourceFingerprint(value any) string {
	raw, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	var canonical any
	if json.Unmarshal(raw, &canonical) != nil {
		return ""
	}
	raw, err = json.Marshal(canonical)
	if err != nil {
		return ""
	}
	fingerprint := sha256.Sum256(raw)
	return hex.EncodeToString(fingerprint[:])
}

func (o *routeOwnership) protect(ctx context.Context, rules []IngressRule, observed map[string]string) (map[string]string, error) {
	protected := make(map[string]string, len(observed))
	for host, endpointID := range observed {
		if _, owned := o.state.Routes[host]; !owned {
			protected[host] = endpointID
		}
	}
	claims, err := o.foreignClaims(ctx)
	if err != nil {
		return nil, err
	}
	maps.Copy(protected, claims)
	for _, rule := range rules {
		host := normalizeHost(rule.Hostname())
		if host == "" {
			continue
		}
		owned, exists := o.state.Routes[host]
		if !exists || owned.Ingress == "" || owned.Ingress != resourceFingerprint(rule) || rule.Path() != "" {
			protected[host] = observed[host]
		}
	}
	return protected, nil
}

func (o *routeOwnership) foreignClaims(ctx context.Context) (map[string]string, error) {
	claims := map[string]string{}
	orgs, err := o.data.ListOrgs(ctx)
	if err != nil {
		return nil, errors.New("route ownership claims unavailable")
	}
	for _, org := range orgs {
		if org == nil {
			continue
		}
		endpoints, err := o.data.ListEndpointsByOrg(ctx, org.ID)
		if err != nil {
			return nil, errors.New("route ownership claims unavailable")
		}
		for _, endpoint := range endpoints {
			if endpoint == nil || endpoint.Policy == nil || endpoint.OrgID != org.ID {
				continue
			}
			for _, ref := range endpoint.Policy.Access {
				if endpoint.OrgID == o.cfg.OrgID && ref.ConnectionID == o.cfg.ConnectionID {
					continue
				}
				host := ref.Hostname
				if host == "" {
					host = endpoint.Domain
				}
				if host = normalizeHost(host); host != "" {
					claims[host] = endpoint.ID
				}
			}
		}
	}
	return claims, nil
}

func (o *routeOwnership) recordIngress(host string, rules []IngressRule, applied bool) {
	if !applied {
		return
	}
	var matched *IngressRule
	for index := range rules {
		if normalizeHost(rules[index].Hostname()) != host {
			continue
		}
		if matched != nil || rules[index].Path() != "" || normalizeService(rules[index].Service()) != normalizeService(o.cfg.Origin) {
			return
		}
		matched = &rules[index]
	}
	if matched != nil {
		owned := o.state.Routes[host]
		owned.Ingress = resourceFingerprint(*matched)
		o.state.Routes[host] = owned
	}
}

func (o *routeOwnership) recordDNS(host string, records []DNSRecord, applied bool) {
	owned := o.state.Routes[host]
	if !applied {
		return
	}
	owned.DNS = make([]string, 0, len(records))
	for _, record := range records {
		owned.DNS = append(owned.DNS, resourceFingerprint(record))
	}
	sort.Strings(owned.DNS)
	o.state.Routes[host] = owned
}

func (o *routeOwnership) ownsDNS(host string, record DNSRecord) bool {
	for _, fingerprint := range o.state.Routes[host].DNS {
		if fingerprint == resourceFingerprint(record) {
			return true
		}
	}
	return false
}

func accessFingerprint(app AccessApp, host string) string {
	parts := make([]string, 0)
	if values, ok := app["self_hosted_domains"].([]any); ok {
		for _, value := range values {
			if name, ok := value.(string); ok && strings.EqualFold(name, host) {
				parts = append(parts, "domain:"+resourceFingerprint(value))
			}
		}
	}
	if values, ok := app["destinations"].([]any); ok {
		for _, value := range values {
			if destination, ok := value.(map[string]any); ok {
				if uri, ok := destination["uri"].(string); ok && strings.EqualFold(uri, host) {
					parts = append(parts, "destination:"+resourceFingerprint(destination))
				}
			}
		}
	}
	if len(parts) == 0 {
		return ""
	}
	sort.Strings(parts)
	return resourceFingerprint(parts)
}

func (o *routeOwnership) save(ctx context.Context) error {
	raw, err := json.Marshal(o.state)
	if err != nil || o.data.UpdateConnectionState(ctx, o.cfg.OrgID, o.cfg.ConnectionID, raw) != nil {
		return errors.New("cloudflare ownership state could not be saved")
	}
	return nil
}

func (o *routeOwnership) snapshot() ownershipState {
	state := o.state
	state.Routes = make(map[string]ownedRoute, len(o.state.Routes))
	for host, owned := range o.state.Routes {
		owned.DNS = append([]string(nil), owned.DNS...)
		state.Routes[host] = owned
	}
	return state
}

func (o *routeOwnership) includeResiduals(plan *ingressPlan, routes map[string]string, wanted map[string]bool, protected map[string]string) {
	if !o.cfg.Prune {
		return
	}
	for host, owned := range o.state.Routes {
		_, claimed := protected[host]
		_, routed := routes[host]
		if wanted[host] || claimed || routed || owned.Ingress == "" && len(owned.DNS) == 0 && owned.Access == "" {
			continue
		}
		plan.orphans = append(plan.orphans, host)
		plan.removed[host] = true
	}
	sort.Strings(plan.orphans)
}

type providerLocks struct {
	mu    sync.Mutex
	locks map[string]chan struct{}
}

func (l *providerLocks) acquire(ctx context.Context, cfg *Config) (func(), error) {
	keys := []string{"tunnel:" + cfg.AccountID + ":" + cfg.TunnelID}
	if cfg.AccessAppID != "" {
		keys = append(keys, "access:"+cfg.AccountID+":"+cfg.AccessAppID)
	}
	sort.Strings(keys)
	held := make([]chan struct{}, 0, len(keys))
	release := func() {
		for index := len(held) - 1; index >= 0; index-- {
			<-held[index]
		}
	}
	for _, key := range keys {
		l.mu.Lock()
		if l.locks == nil {
			l.locks = make(map[string]chan struct{})
		}
		lock := l.locks[key]
		if lock == nil {
			lock = make(chan struct{}, 1)
			l.locks[key] = lock
		}
		l.mu.Unlock()
		select {
		case <-ctx.Done():
			release()
			return nil, ctx.Err()
		case lock <- struct{}{}:
			held = append(held, lock)
		}
	}
	return release, nil
}
