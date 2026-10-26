package proxy

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/memory"
	"github.com/mishmesh/mishmesh/internal/store/sqlite"
)

type failingStore struct {
	store.DataStore
	listOrgs      error
	listEndpoints error
	createOrg     error
	createAgent   error
}

func (f failingStore) ListOrgs(ctx context.Context) ([]*store.Org, error) {
	if f.listOrgs != nil {
		return nil, f.listOrgs
	}
	return f.DataStore.ListOrgs(ctx)
}

func (f failingStore) ListEndpointsByOrg(ctx context.Context, orgID string) ([]*store.Endpoint, error) {
	if f.listEndpoints != nil {
		return nil, f.listEndpoints
	}
	return f.DataStore.ListEndpointsByOrg(ctx, orgID)
}

func (f failingStore) CreateOrg(ctx context.Context, org *store.Org) error {
	if f.createOrg != nil {
		return f.createOrg
	}
	return f.DataStore.CreateOrg(ctx, org)
}

func (f failingStore) CreateAgent(ctx context.Context, agent *store.Agent) error {
	if f.createAgent != nil {
		return f.createAgent
	}
	return f.DataStore.CreateAgent(ctx, agent)
}

func TestRegisterLogsStoreErrors(t *testing.T) {
	cases := []struct {
		name string
		fail func(*failingStore, error)
		want string
	}{
		{"list orgs", func(f *failingStore, e error) { f.listOrgs = e }, "list orgs"},
		{"list endpoints", func(f *failingStore, e error) { f.listEndpoints = e }, "list endpoints"},
		{"create org", func(f *failingStore, e error) { f.createOrg = e }, "create system org"},
		{"create agent", func(f *failingStore, e error) { f.createAgent = e }, "create proxy agent"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			data, err := sqlite.Open(t.TempDir() + "/r.db")
			if err != nil {
				t.Fatal(err)
			}
			defer data.Close()
			sentinel := errors.New("boom-" + tc.name)
			fs := &failingStore{DataStore: data}
			tc.fail(fs, sentinel)
			if tc.name == "list endpoints" {
				if err := data.CreateOrg(context.Background(), &store.Org{ID: "org_x", Name: "x"}); err != nil {
					t.Fatal(err)
				}
			}
			var buf bytes.Buffer
			log := slog.New(slog.NewTextHandler(&buf, nil))
			Register(context.Background(), fs, memory.NewConnStore(), log, nil)
			out := buf.String()
			if !strings.Contains(out, tc.want) || !strings.Contains(out, sentinel.Error()) {
				t.Fatalf("log missing %q / %q:\n%s", tc.want, sentinel, out)
			}
		})
	}
}
