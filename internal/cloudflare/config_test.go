package cloudflare

import (
	"context"
	"encoding/base64"
	"reflect"
	"strings"
	"testing"

	"github.com/mishmesh/mishmesh/internal/store"
)

func encodeToken(t *testing.T, raw string) string {
	t.Helper()
	return base64.StdEncoding.EncodeToString([]byte(raw))
}

func TestDecodeTunnelToken(t *testing.T) {
	good := `{"a":"acct1","t":"tun1","s":"c2VjcmV0"}`
	tests := []struct {
		name    string
		raw     string
		want    TunnelToken
		wantErr string
	}{
		{"standard", encodeToken(t, good), TunnelToken{AccountID: "acct1", TunnelID: "tun1", Secret: "c2VjcmV0"}, ""},
		{"surrounding whitespace", "  " + encodeToken(t, good) + "\n", TunnelToken{AccountID: "acct1", TunnelID: "tun1", Secret: "c2VjcmV0"}, ""},
		{"unpadded", strings.TrimRight(encodeToken(t, good), "="), TunnelToken{AccountID: "acct1", TunnelID: "tun1", Secret: "c2VjcmV0"}, ""},
		{"empty", "", TunnelToken{}, "empty"},
		{"not base64", "%%%", TunnelToken{}, "base64"},
		{"not json", encodeToken(t, "hello"), TunnelToken{}, "credentials"},
		{"missing secret", encodeToken(t, `{"a":"acct1","t":"tun1"}`), TunnelToken{}, "missing"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DecodeTunnelToken(tt.raw)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("err = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("got %+v, %v", got, err)
			}
		})
	}
}

func TestCheckLoopback(t *testing.T) {
	tests := []struct {
		addr string
		ok   bool
	}{
		{"127.0.0.1:20241", true},
		{"localhost:20241", true},
		{"[::1]:20241", true},
		{"127.0.0.2:9", true},
		{"10.0.0.1:20241", false},
		{"0.0.0.0:20241", false},
		{"example.com:20241", false},
		{"169.254.169.254:80", false},
		{"127.0.0.1", false},
		{"127.0.0.1:0", false},
		{"127.0.0.1:99999", false},
		{"127.0.0.1:http", false},
	}
	for _, tt := range tests {
		if err := CheckLoopback(tt.addr); (err == nil) != tt.ok {
			t.Errorf("CheckLoopback(%q) = %v, want ok=%v", tt.addr, err, tt.ok)
		}
	}
}

func TestParseMetrics(t *testing.T) {
	locations, version := parseMetrics(testMetrics)
	if !reflect.DeepEqual(locations, []string{"fra19", "tlv01"}) || version != "2026.10.0" {
		t.Fatalf("locations=%v version=%q", locations, version)
	}
	if locations, version := parseMetrics("garbage\n{}\n"); len(locations) != 0 || version != "" {
		t.Fatalf("garbage parsed as %v %q", locations, version)
	}
}

func TestConfigLevel(t *testing.T) {
	var none *Config
	if none.Level() != LevelNone {
		t.Fatal("nil config must be level none")
	}
	if (&Config{}).Level() != LevelImported || (&Config{APIToken: "x"}).Level() != LevelManaged {
		t.Fatal("level derived from API token")
	}
	if (&Config{}).refreshInterval() != DefaultImportedRefresh || (&Config{APIToken: "x"}).refreshInterval() != DefaultSyncInterval {
		t.Fatal("refresh interval defaults")
	}
}

type stubStore struct {
	store.DataStore
	byOrg map[string][]*store.Endpoint
	err   error
}

func (s stubStore) ListEndpointsByOrg(_ context.Context, org string) ([]*store.Endpoint, error) {
	return s.byOrg[org], s.err
}

func TestStoreSource(t *testing.T) {
	cf := &store.EndpointPolicy{Access: []store.AccessRef{{ConnectionID: "connection"}}}
	data := stubStore{byOrg: map[string][]*store.Endpoint{
		"org_default": {
			{ID: "e1", OrgID: "org_default", Domain: "B.Example.com.", Policy: cf},
			{ID: "e2", OrgID: "org_default", Domain: "", Policy: cf},
			{ID: "e3", OrgID: "org_default", Domain: "c.example.com", Policy: &store.EndpointPolicy{}},
			{ID: "e4", OrgID: "org_default", Domain: "d.example.com"},
			{ID: "e5", OrgID: "org_default", Domain: "a.example.com", Policy: cf},
		},
		"org_other": {{ID: "e6", OrgID: "org_other", Domain: "z.example.com", Policy: cf}},
	}}
	got, err := StoreSource{Data: data, ConnectionID: "connection"}.Hostnames(context.Background(), "org_default")
	if err != nil {
		t.Fatal(err)
	}
	want := []Hostname{
		{Host: "a.example.com", EndpointID: "e5", OrgID: "org_default", Public: true, AccessHostnameKnown: true},
		{Host: "b.example.com", EndpointID: "e1", OrgID: "org_default", Public: true, AccessHostnameKnown: true},
		{Host: "c.example.com", EndpointID: "e3", OrgID: "org_default"},
		{Host: "d.example.com", EndpointID: "e4", OrgID: "org_default"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
}

func TestStoreSourceError(t *testing.T) {
	src := StoreSource{Data: stubStore{err: context.DeadlineExceeded}}
	if _, err := src.Hostnames(context.Background(), "org_default"); err == nil {
		t.Fatal("expected error")
	}
}
