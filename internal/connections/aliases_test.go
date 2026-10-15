package connections_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/mishmesh/mishmesh/internal/cloudflare"
	"github.com/mishmesh/mishmesh/internal/connections"
	tailscaleconnection "github.com/mishmesh/mishmesh/internal/connections/tailscale"
	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/typedfields"
)

type capabilityProvider struct{ protocol string }

func (p *capabilityProvider) Describe() connections.Descriptor {
	return connections.Descriptor{Type: "edge-network", Role: "access", Scope: "private", Protocols: []string{p.protocol}, Detect: true, DetectFields: []typedfields.Field{{Key: "dial", Kind: "select", Options: []string{"one"}, Default: json.RawMessage(`"one"`)}}}
}
func (*capabilityProvider) Validate(connections.Config) error { return nil }
func (*capabilityProvider) Detect(context.Context, json.RawMessage) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}
func (*capabilityProvider) Open(connections.Config, connections.Deps) (connections.Instance, error) {
	return nil, errors.New("capability test only")
}
func (*capabilityProvider) AccessHostname(domain, requested string) (string, error) {
	if requested == "" {
		requested = strings.SplitN(domain, ".", 2)[0]
	}
	return connections.NormalizeAccessHostname("edge-" + requested)
}

func TestShouldDispatchAccessNamingAndProtocolsThroughRegisteredProviders(t *testing.T) {
	registry := connections.NewRegistry()
	for _, provider := range []connections.Provider{cloudflare.NewProvider(cloudflare.ProviderOptions{}), tailscaleconnection.NewProvider(tailscaleconnection.Options{}), &capabilityProvider{protocol: store.KindUDP}} {
		if err := registry.Register(provider); err != nil {
			t.Fatal(err)
		}
	}
	for _, test := range []struct{ provider, domain, requested, protocol, want string }{
		{"cloudflare", "Notes.Niradler.Com.", "", store.KindHTTP, "notes.niradler.com"},
		{"tailscale", "notes.niradler.com", "", store.KindHTTP, "notes"},
		{"tailscale", "notes.niradler.com", "OTHER.", store.KindHTTP, "other"},
		{"edge-network", "notes.niradler.com", "", store.KindUDP, "edge-notes"},
	} {
		host, err := registry.AccessHostname(test.provider, test.domain, test.requested, test.protocol)
		if err != nil || host != test.want {
			t.Fatalf("%s naming=%q %v", test.provider, host, err)
		}
	}
	for _, test := range []struct{ provider, domain, requested, protocol string }{
		{"cloudflare", "notes", "", store.KindHTTP}, {"cloudflare", "127.0.0.1", "", store.KindHTTP}, {"tailscale", "notes.example.org", "invalid.name", store.KindHTTP}, {"cloudflare", "notes.example.org", "", store.KindUDP}, {"tailscale", "notes.example.org", "", store.KindTLS}, {"edge-network", "notes.example.org", "", store.KindHTTP},
	} {
		if _, err := registry.AccessHostname(test.provider, test.domain, test.requested, test.protocol); err == nil {
			t.Fatalf("unsupported naming/protocol accepted: %+v", test)
		}
	}
}

func TestShouldFreezeProtocolAndDetectionCapabilities(t *testing.T) {
	provider := &capabilityProvider{protocol: store.KindUDP}
	registry := connections.NewRegistry()
	if err := registry.Register(provider); err != nil {
		t.Fatal(err)
	}
	provider.protocol = store.KindHTTP
	first := registry.Descriptors()
	first[0].Protocols[0] = store.KindTLS
	first[0].DetectFields[0].Options[0] = "modified"
	first[0].DetectFields[0].Default[1] = 'x'
	second := registry.Descriptors()
	if second[0].Protocols[0] != store.KindUDP || second[0].DetectFields[0].Options[0] != "one" || string(second[0].DetectFields[0].Default) != `"one"` {
		t.Fatalf("mutable descriptor capabilities: %+v", second)
	}
	for _, protocol := range []string{"unknown", ""} {
		if err := connections.NewRegistry().Register(&capabilityProvider{protocol: protocol}); err == nil {
			t.Fatalf("invalid protocol accepted: %q", protocol)
		}
	}
}

func TestShouldPersistObservedAliasesBeforePublicationAndDowngradeFailedSave(t *testing.T) {
	for _, fail := range []bool{false, true} {
		status := connections.Status{Health: "healthy", Hostnames: []connections.HostStatus{{Host: "notes.tailnet.ts.net", EndpointID: "endpoint", State: "published", EndpointKind: store.KindHTTP, AccessHostname: "notes", AccessHostnameKnown: true}}}
		saved, invalidated := false, false
		deps := connections.Deps{PublishAliases: func(ctx context.Context, aliases []store.EndpointAlias) error {
			if len(aliases) != 1 || aliases[0].Hostname != "notes.tailnet.ts.net" || aliases[0].AccessHostname != "notes" || !aliases[0].AccessHostnameKnown || aliases[0].Kind != store.KindHTTP {
				t.Fatalf("observed snapshot lost: %+v", aliases)
			}
			if fail {
				return store.ErrConflict
			}
			saved = true
			return nil
		}, OnAliasesChanged: func() {
			if !saved {
				t.Fatal("invalidated before durable save")
			}
			invalidated = true
		}}
		err := connections.RecordAccessAliases(context.Background(), store.Connection{ID: "connection", OrgID: "org"}, connections.Descriptor{Role: "access", Protocols: []string{store.KindHTTP}}, &status, deps)
		if fail {
			if err == nil || status.Hostnames[0].State == "published" || saved || invalidated {
				t.Fatalf("failed routing still published: %+v %v", status, err)
			}
		} else if err != nil || !saved || !invalidated {
			t.Fatalf("publication not saved: %+v %v", status, err)
		}
	}
}
