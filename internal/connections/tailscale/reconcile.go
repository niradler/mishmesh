package tailscaleconnection

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"maps"
	"reflect"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mishmesh/mishmesh/internal/connections"
	"github.com/mishmesh/mishmesh/internal/store"
	"tailscale.com/ipn"
	"tailscale.com/ipn/ipnstate"
	"tailscale.com/tailcfg"
)

var errClaimsUnavailable = errors.New("Tailscale ownership claims unavailable")

const claimsTTL = 10 * time.Second

func AccessHostname(endpointDomain, requested string) (string, error) {
	label := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(requested)), ".")
	if label == "" {
		label = strings.SplitN(strings.ToLower(strings.TrimSpace(endpointDomain)), ".", 2)[0]
	}
	if tailcfg.ServiceName("svc:"+label).Validate() != nil {
		return "", errors.New("Tailscale access name must be a DNS service label")
	}
	return label, nil
}

type ownershipState struct {
	Version       int               `json:"version"`
	Socket        string            `json:"socket"`
	Tailnet       string            `json:"tailnet"`
	Origin        string            `json:"origin"`
	OwnedServices map[string]string `json:"owned_services"`
	API           map[string]string `json:"api_fingerprints"`
	Serve         map[string]string `json:"serve_fingerprints"`
	Advertised    map[string]bool   `json:"advertised_services"`
}

func fingerprint(value any) string {
	raw, _ := json.Marshal(value)
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:])
}

func (i *instance) loadState(raw json.RawMessage, tailnet string) (*ownershipState, error) {
	state := &ownershipState{Version: 1, Socket: i.cfg.Socket, Tailnet: tailnet, Origin: i.origin, OwnedServices: map[string]string{}, API: map[string]string{}, Serve: map[string]string{}, Advertised: map[string]bool{}}
	if len(raw) == 0 {
		return state, nil
	}
	var stored ownershipState
	if json.Unmarshal(raw, &stored) != nil || stored.Version != 1 || stored.OwnedServices == nil || stored.API == nil || stored.Serve == nil || stored.Advertised == nil {
		return nil, errors.New("invalid Tailscale ownership state")
	}
	for name, endpoint := range stored.OwnedServices {
		if tailcfg.ServiceName(name).Validate() != nil || endpoint == "" {
			return nil, errors.New("invalid Tailscale ownership state")
		}
	}
	for _, resources := range []map[string]string{stored.API, stored.Serve} {
		for name, digest := range resources {
			decoded, err := hex.DecodeString(digest)
			if stored.OwnedServices[name] == "" || err != nil || len(decoded) != sha256.Size {
				return nil, errors.New("invalid Tailscale ownership fingerprint")
			}
		}
	}
	if tailnet == "" || stored.Socket == state.Socket && strings.EqualFold(stored.Tailnet, tailnet) && stored.Origin == state.Origin {
		return &stored, nil
	}
	return state, nil
}

func (i *instance) saveState(ctx context.Context, state *ownershipState) error {
	raw, err := json.Marshal(state)
	i.provider.claims.invalidate()
	if err != nil || i.deps.Data.UpdateConnectionState(ctx, i.record.OrgID, i.record.ID, raw) != nil {
		return errors.New("Tailscale ownership could not be saved")
	}
	i.provider.claims.invalidate()
	return nil
}

type targetLocks struct {
	mu    sync.Mutex
	locks map[string]chan struct{}
}

func (l *targetLocks) acquire(ctx context.Context, keys ...string) (func(), error) {
	sort.Strings(keys)
	held := []chan struct{}{}
	release := func() {
		for j := len(held) - 1; j >= 0; j-- {
			<-held[j]
		}
	}
	for _, key := range keys {
		l.mu.Lock()
		if l.locks == nil {
			l.locks = map[string]chan struct{}{}
		}
		lock := l.locks[key]
		if lock == nil {
			lock = make(chan struct{}, 1)
			l.locks[key] = lock
		}
		l.mu.Unlock()
		select {
		case lock <- struct{}{}:
			held = append(held, lock)
		case <-ctx.Done():
			release()
			return nil, errors.New("Tailscale target lock cancelled")
		}
	}
	return release, nil
}

func localIdentity(status *ipnstate.Status, configured string) (string, string, error) {
	if status == nil || status.Self == nil || status.CurrentTailnet == nil || status.BackendState != "Running" {
		return "", "", errors.New("Tailscale node is not running")
	}
	version := strings.SplitN(strings.SplitN(status.Version, "-", 2)[0], ".", 3)
	if len(version) != 3 {
		return "", "", errors.New("Tailscale daemon version is unsupported")
	}
	major, first := strconv.Atoi(version[0])
	minor, second := strconv.Atoi(version[1])
	patch, third := strconv.Atoi(version[2])
	if first != nil || second != nil || third != nil || patch < 0 || major < 1 || major == 1 && minor < 86 {
		return "", "", errors.New("Tailscale daemon must be version 1.86 or later")
	}
	if !status.Self.IsTagged() {
		return "", "", errors.New("Tailscale Service host requires a tagged node")
	}
	tailnet := strings.ToLower(status.CurrentTailnet.Name)
	suffix := strings.ToLower(status.CurrentTailnet.MagicDNSSuffix)
	if tailnet == "" || suffix == "" || !strings.HasSuffix(suffix, ".ts.net") || strings.ContainsAny(tailnet, "/\\?#\r\n\x00") || configured != "-" && !strings.EqualFold(configured, tailnet) {
		return "", "", errors.New("Tailscale tailnet does not match the local node")
	}
	return tailnet, suffix, nil
}

func desiredServices(connectionID string, endpoints []*store.Endpoint) (map[string]string, error) {
	desired := map[string]string{}
	for _, endpoint := range endpoints {
		if endpoint == nil || endpoint.Policy == nil || endpoint.Kind != "" && endpoint.Kind != store.KindHTTP {
			continue
		}
		for _, ref := range endpoint.Policy.Access {
			if ref.ConnectionID != connectionID {
				continue
			}
			label, err := AccessHostname(endpoint.Domain, ref.Hostname)
			if err != nil {
				return nil, err
			}
			name := "svc:" + label
			if other := desired[name]; other != "" && other != endpoint.ID {
				return nil, errors.New("Tailscale service name is claimed by multiple endpoints")
			}
			desired[name] = endpoint.ID
		}
	}
	return desired, nil
}

type claimEntry struct {
	id           string
	orgID        string
	tailnet      string
	stateTailnet string
	names        map[string]bool
	err          error
}

type claimsCache struct {
	mu      sync.Mutex
	loaded  time.Time
	entries []claimEntry
	err     error
	valid   bool
}

func (c *claimsCache) invalidate() {
	c.mu.Lock()
	c.valid = false
	c.mu.Unlock()
}

func (c *claimsCache) snapshot(ctx context.Context, data store.DataStore) ([]claimEntry, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.valid && time.Since(c.loaded) < claimsTTL {
		return c.entries, c.err
	}
	entries, err := loadClaimEntries(ctx, data)
	if ctx.Err() != nil {
		return nil, err
	}
	c.entries, c.err, c.loaded, c.valid = entries, err, time.Now(), true
	return entries, err
}

func loadClaimEntries(ctx context.Context, data store.DataStore) ([]claimEntry, error) {
	records, err := data.ListAllConnections(ctx)
	if err != nil {
		return nil, errClaimsUnavailable
	}
	var entries []claimEntry
	byOrg := map[string][]*store.Endpoint{}
	for _, record := range records {
		if record == nil || record.Type != "tailscale" {
			continue
		}
		var cfg configuration
		if json.Unmarshal(record.Config, &cfg) != nil {
			return nil, errClaimsUnavailable
		}
		var state ownershipState
		if len(record.State) > 0 && json.Unmarshal(record.State, &state) != nil {
			return nil, errClaimsUnavailable
		}
		entry := claimEntry{id: record.ID, orgID: record.OrgID, tailnet: cfg.Tailnet, stateTailnet: state.Tailnet, names: map[string]bool{}}
		endpoints, listed := byOrg[record.OrgID]
		var err error
		if !listed {
			endpoints, err = data.ListEndpointsByOrg(ctx, record.OrgID)
			byOrg[record.OrgID] = endpoints
		}
		var desired map[string]string
		if err == nil {
			desired, err = desiredServices(record.ID, endpoints)
		}
		if err != nil {
			entry.err = errClaimsUnavailable
		}
		for name := range desired {
			entry.names[name] = true
		}
		for name := range state.OwnedServices {
			entry.names[name] = true
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

func (i *instance) claims(ctx context.Context, tailnet string) (map[string]bool, error) {
	entries, err := i.provider.claims.snapshot(ctx, i.deps.Data)
	if err != nil {
		return nil, err
	}
	claims := map[string]bool{}
	for _, entry := range entries {
		if entry.id == i.record.ID && entry.orgID == i.record.OrgID {
			continue
		}
		if entry.tailnet != "-" && !strings.EqualFold(entry.tailnet, tailnet) && !strings.EqualFold(entry.stateTailnet, tailnet) {
			continue
		}
		if entry.err != nil {
			return nil, entry.err
		}
		for name := range entry.names {
			claims[name] = true
		}
	}
	return claims, nil
}

func serviceConfig(name, suffix, origin string) *ipn.ServiceConfig {
	host := ipn.HostPort(strings.TrimPrefix(name, "svc:") + "." + suffix + ":443")
	return &ipn.ServiceConfig{TCP: map[uint16]*ipn.TCPPortHandler{443: {HTTPS: true}}, Web: map[ipn.HostPort]*ipn.WebServerConfig{host: {Handlers: map[string]*ipn.HTTPHandler{"/": {Proxy: origin}}}}}
}

func serviceApproved(status *ipnstate.Status, name string) bool {
	if status == nil || status.Self == nil {
		return false
	}
	approved := false
	for _, raw := range status.Self.CapMap[tailcfg.NodeAttrServiceHost] {
		var mappings tailcfg.ServiceIPMappings
		if json.Unmarshal([]byte(raw), &mappings) != nil {
			return false
		}
		if addresses, present := mappings[tailcfg.ServiceName(name)]; present {
			approved = len(addresses) > 0
		}
	}
	return approved
}

func (i *instance) sync(ctx context.Context, status *connections.Status) error {
	observed, err := i.local.StatusWithoutPeers(ctx)
	if err != nil {
		return errors.New("Tailscale LocalAPI unavailable")
	}
	tailnet, suffix, err := localIdentity(observed, i.cfg.Tailnet)
	if err != nil {
		return err
	}
	release, err := i.provider.locks.acquire(ctx, "socket:"+i.cfg.Socket, "tailnet:"+tailnet)
	if err != nil {
		return err
	}
	defer release()
	observed, err = i.local.StatusWithoutPeers(ctx)
	if err != nil {
		return errors.New("Tailscale LocalAPI unavailable")
	}
	actualTailnet, actualSuffix, err := localIdentity(observed, i.cfg.Tailnet)
	if err != nil {
		return err
	}
	if tailnet != actualTailnet || suffix != actualSuffix {
		return errors.New("Tailscale local identity changed during reconciliation")
	}
	record, err := i.deps.Data.GetConnection(ctx, i.record.ID)
	if err != nil || record == nil || record.OrgID != i.record.OrgID || record.Type != i.record.Type || !reflect.DeepEqual(record.Config, i.record.Config) {
		return errors.New("Tailscale connection configuration changed")
	}
	state, err := i.loadState(record.State, tailnet)
	if err != nil {
		return err
	}
	endpoints, err := i.deps.Data.ListEndpointsByOrg(ctx, i.record.OrgID)
	if err != nil {
		return errors.New("Tailscale endpoints unavailable")
	}
	for _, endpoint := range endpoints {
		if endpoint != nil && endpoint.OrgID != i.record.OrgID {
			return errors.New("Tailscale endpoint ownership mismatch")
		}
	}
	desired, err := desiredServices(i.record.ID, endpoints)
	if err != nil {
		return err
	}
	if i.cfg.Level == "managed" {
		if err := i.api.ValidateNode(ctx, tailnet, string(observed.Self.ID), int64(observed.Self.NodeID)); err != nil {
			return err
		}
		claims, err := i.claims(ctx, tailnet)
		if err != nil {
			return err
		}
		for name := range desired {
			if claims[name] {
				return errors.New("Tailscale service name is claimed by another connection")
			}
		}
		for name := range state.OwnedServices {
			if claims[name] {
				return errors.New("Tailscale owned service is claimed by another connection")
			}
		}
		if err := i.manage(ctx, state, desired, tailnet, suffix, string(observed.Self.ID)); err != nil {
			return err
		}
	}
	serve, err := i.local.GetServeConfig(ctx)
	if err != nil || serve == nil {
		return errors.New("Tailscale Serve observation failed")
	}
	prefs, err := i.local.GetPrefs(ctx)
	if err != nil || prefs == nil {
		return errors.New("Tailscale Service advertisement observation failed")
	}
	observed, err = i.local.StatusWithoutPeers(ctx)
	if err != nil {
		return errors.New("Tailscale LocalAPI unavailable")
	}
	finalTailnet, finalSuffix, err := localIdentity(observed, i.cfg.Tailnet)
	if err != nil {
		return err
	}
	if finalTailnet != tailnet || finalSuffix != suffix {
		return errors.New("Tailscale local identity changed during reconciliation")
	}
	status.Health = "healthy"
	names := slices.Sorted(maps.Keys(desired))
	for _, name := range names {
		host := connections.HostStatus{Host: strings.TrimPrefix(name, "svc:") + "." + suffix, EndpointID: desired[name], State: "pending", Detail: "Service-host approval and access-policy grants are required", Fix: "Approve the Service host and grant access in Tailscale", EndpointKind: store.KindHTTP, AccessHostnameKnown: true}
		for _, endpoint := range endpoints {
			if endpoint.ID != desired[name] || endpoint.Policy == nil {
				continue
			}
			for _, ref := range endpoint.Policy.Access {
				label, normalizeErr := AccessHostname(endpoint.Domain, ref.Hostname)
				if ref.ConnectionID == i.record.ID && normalizeErr == nil && "svc:"+label == name {
					host.AccessHostname = ref.Hostname
				}
			}
		}
		if !reflect.DeepEqual(serve.Services[tailcfg.ServiceName(name)], serviceConfig(name, suffix, i.origin)) {
			host.State, host.Detail = "conflict", "Service configuration is missing or routed elsewhere"
		} else if !slices.Contains(prefs.AdvertiseServices, name) {
			host.Detail = "Service is configured but not advertised"
		} else if serviceApproved(observed, name) {
			host.State, host.Detail, host.Fix = "published", "Approved Service host is advertising HTTPS; client access depends on tailnet policy", ""
		}
		if host.State != "published" {
			status.Health = "degraded"
		}
		status.Hostnames = append(status.Hostnames, host)
	}
	return nil
}

func (i *instance) manage(ctx context.Context, state *ownershipState, desired map[string]string, tailnet, suffix, stableID string) error {
	serve, err := i.local.GetServeConfig(ctx)
	if err != nil || serve == nil {
		return errors.New("Tailscale Serve observation failed")
	}
	prefs, err := i.local.GetPrefs(ctx)
	if err != nil || prefs == nil {
		return errors.New("Tailscale Service advertisement observation failed")
	}
	if err := protectAdvertisements(prefs, state, desired); err != nil {
		return err
	}
	for name, endpoint := range desired {
		if owned := state.OwnedServices[name]; owned != "" && owned != endpoint {
			return errors.New("Tailscale service belongs to another endpoint")
		}
		current := serve.Services[tailcfg.ServiceName(name)]
		if current != nil && (state.Serve[name] == "" || state.Serve[name] != fingerprint(current)) {
			return errors.New("Tailscale Serve entry is owned externally or was edited externally")
		}
	}
	for _, name := range slices.Sorted(maps.Keys(desired)) {
		current, err := i.api.GetService(ctx, tailnet, name)
		if err != nil {
			return err
		}
		if current != nil {
			if state.API[name] == "" || state.API[name] != fingerprint(current) {
				return errors.New("Tailscale Service is owned externally or was edited externally")
			}
			if !slices.Equal(current.Tags, i.cfg.ServiceTags) {
				updated := *current
				updated.Tags = append([]string(nil), i.cfg.ServiceTags...)
				if err := i.api.PutService(ctx, tailnet, updated); err != nil {
					return err
				}
				observed, err := i.api.GetService(ctx, tailnet, name)
				if err != nil || observed == nil || !reflect.DeepEqual(*observed, updated) {
					return errors.New("Tailscale Service changed after update")
				}
				state.API[name] = fingerprint(observed)
				if err := i.saveState(ctx, state); err != nil {
					return err
				}
			}
			continue
		}
		resource := Service{Name: name, Ports: []string{"tcp:443"}, Tags: append([]string(nil), i.cfg.ServiceTags...)}
		if err := i.api.PutService(ctx, tailnet, resource); err != nil {
			return err
		}
		created, err := i.api.GetService(ctx, tailnet, name)
		if err != nil {
			return err
		}
		if created == nil {
			return errors.New("Tailscale Service creation was not observed")
		}
		comparison := *created
		comparison.Addrs = nil
		if !reflect.DeepEqual(comparison, resource) {
			return errors.New("Tailscale Service changed after creation")
		}
		state.OwnedServices[name], state.API[name] = desired[name], fingerprint(created)
		if err := i.saveState(ctx, state); err != nil {
			return err
		}
	}
	for _, name := range slices.Sorted(maps.Keys(state.OwnedServices)) {
		if desired[name] != "" {
			continue
		}
		current, err := i.api.GetService(ctx, tailnet, name)
		if err != nil {
			return err
		}
		if current != nil && (state.API[name] == "" || state.API[name] != fingerprint(current)) {
			return errors.New("Tailscale Service changed externally; cleanup refused")
		}
		if current != nil {
			if err := i.api.CheckServiceHosts(ctx, tailnet, name, stableID); err != nil {
				return err
			}
		}
	}
	if err := i.applyServe(ctx, state, desired, suffix); err != nil {
		return err
	}
	if err := i.applyAdvertisements(ctx, state, desired); err != nil {
		return err
	}
	for _, name := range slices.Sorted(maps.Keys(state.OwnedServices)) {
		if desired[name] != "" {
			continue
		}
		current, err := i.api.GetService(ctx, tailnet, name)
		if err != nil {
			return err
		}
		if current != nil {
			if state.API[name] == "" || state.API[name] != fingerprint(current) {
				return errors.New("Tailscale Service changed externally; cleanup refused")
			}
			if err := i.api.CheckServiceHosts(ctx, tailnet, name, stableID); err != nil {
				return err
			}
			if err := i.protectAdvertisements(ctx, state, desired); err != nil {
				return err
			}
			if err := i.api.DeleteService(ctx, tailnet, name); err != nil {
				return err
			}
			if observed, err := i.api.GetService(ctx, tailnet, name); err != nil || observed != nil {
				return errors.New("Tailscale Service deletion was not observed")
			}
		} else if err := i.protectAdvertisements(ctx, state, desired); err != nil {
			return err
		}
		delete(state.OwnedServices, name)
		delete(state.API, name)
		delete(state.Serve, name)
		delete(state.Advertised, name)
		if err := i.saveState(ctx, state); err != nil {
			return err
		}
	}
	return nil
}

func (i *instance) applyServe(ctx context.Context, state *ownershipState, desired map[string]string, suffix string) error {
	for attempt := 0; attempt < 3; attempt++ {
		current, err := i.local.GetServeConfig(ctx)
		if err != nil || current == nil {
			return errors.New("Tailscale Serve observation failed")
		}
		if current.ETag == "" {
			return errors.New("Tailscale Serve ETag is required")
		}
		if err := i.protectAdvertisements(ctx, state, desired); err != nil {
			return err
		}
		next := current.Clone()
		if next.Services == nil {
			next.Services = map[tailcfg.ServiceName]*ipn.ServiceConfig{}
		}
		changed := []string{}
		for _, name := range slices.Sorted(maps.Keys(state.OwnedServices)) {
			existing := current.Services[tailcfg.ServiceName(name)]
			if existing != nil && (state.Serve[name] == "" || state.Serve[name] != fingerprint(existing)) {
				return errors.New("Tailscale Serve entry changed externally")
			}
			if desired[name] == "" {
				if existing != nil {
					delete(next.Services, tailcfg.ServiceName(name))
					changed = append(changed, name)
				}
			} else {
				wanted := serviceConfig(name, suffix, i.origin)
				if !reflect.DeepEqual(existing, wanted) {
					next.Services[tailcfg.ServiceName(name)] = wanted
					changed = append(changed, name)
				}
			}
		}
		if len(changed) == 0 {
			return nil
		}
		if err := i.local.SetServeConfig(ctx, next); err != nil {
			if isETagConflict(err) {
				continue
			}
			return errors.New("Tailscale Serve update failed")
		}
		observed, err := i.local.GetServeConfig(ctx)
		if err != nil || observed == nil {
			return errors.New("Tailscale Serve update was not observed")
		}
		for _, name := range changed {
			actual, wanted := observed.Services[tailcfg.ServiceName(name)], next.Services[tailcfg.ServiceName(name)]
			if !reflect.DeepEqual(actual, wanted) {
				return errors.New("Tailscale Serve entry changed after update")
			}
			if wanted != nil {
				state.Serve[name] = fingerprint(actual)
			} else {
				delete(state.Serve, name)
			}
			if err := i.saveState(ctx, state); err != nil {
				return err
			}
		}
		return nil
	}
	return errors.New("Tailscale Serve changed concurrently after three attempts")
}

func (i *instance) applyAdvertisements(ctx context.Context, state *ownershipState, desired map[string]string) error {
	current, err := i.local.GetPrefs(ctx)
	if err != nil || current == nil {
		return errors.New("Tailscale Service advertisement observation failed")
	}
	if err := protectAdvertisements(current, state, desired); err != nil {
		return err
	}
	next := append([]string(nil), current.AdvertiseServices...)
	added, removed := []string{}, []string{}
	for _, name := range slices.Sorted(maps.Keys(state.OwnedServices)) {
		present := slices.Contains(next, name)
		if desired[name] == "" {
			if state.Advertised[name] && present {
				next = slices.DeleteFunc(next, func(value string) bool { return value == name })
				removed = append(removed, name)
			}
		} else if !present {
			next = append(next, name)
			added = append(added, name)
		} else if !state.Advertised[name] {
			return errors.New("Tailscale Service advertisement is owned externally")
		}
	}
	if len(added) == 0 && len(removed) == 0 {
		return nil
	}
	if _, err := i.local.EditPrefs(ctx, &ipn.MaskedPrefs{AdvertiseServicesSet: true, Prefs: ipn.Prefs{AdvertiseServices: next}}); err != nil {
		return errors.New("Tailscale Service advertisement update failed")
	}
	observed, err := i.local.GetPrefs(ctx)
	if err != nil || observed == nil || !slices.Equal(observed.AdvertiseServices, next) {
		return errors.New("Tailscale Service advertisement changed after update")
	}
	for _, name := range added {
		state.Advertised[name] = true
		if err := i.saveState(ctx, state); err != nil {
			return err
		}
	}
	for _, name := range removed {
		delete(state.Advertised, name)
		if err := i.saveState(ctx, state); err != nil {
			return err
		}
	}
	return nil
}

func protectAdvertisements(prefs *ipn.Prefs, state *ownershipState, desired map[string]string) error {
	for _, name := range prefs.AdvertiseServices {
		_, owned := state.OwnedServices[name]
		_, wanted := desired[name]
		if (owned || wanted) && !state.Advertised[name] {
			return errors.New("Tailscale Service advertisement is owned externally; resolve its ownership in Tailscale before publication or cleanup")
		}
	}
	return nil
}

func (i *instance) protectAdvertisements(ctx context.Context, state *ownershipState, desired map[string]string) error {
	prefs, err := i.local.GetPrefs(ctx)
	if err != nil || prefs == nil {
		return errors.New("Tailscale Service advertisement observation failed")
	}
	return protectAdvertisements(prefs, state, desired)
}

func ReconcileServices(current, owned, desired map[string]string) (map[string]string, error) {
	next := maps.Clone(current)
	if next == nil {
		next = make(map[string]string)
	}
	for name, target := range desired {
		if tailcfg.ServiceName(name).Validate() != nil || target == "" {
			return nil, errors.New("invalid desired service")
		}
		if _, exists := current[name]; exists && owned[name] == "" {
			return nil, errors.New("service is owned externally")
		}
		next[name] = target
	}
	for name, endpointID := range owned {
		if endpointID != "" {
			if _, wanted := desired[name]; !wanted {
				delete(next, name)
			}
		}
	}
	return next, nil
}
