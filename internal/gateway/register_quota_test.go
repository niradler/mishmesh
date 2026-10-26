package gateway

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/tunnel"
)

type quotaFailingStore struct {
	store.DataStore
	getQuotaErr error
	countErr    error
}

func (s quotaFailingStore) GetQuota(ctx context.Context, orgID string) (*store.Quota, error) {
	if s.getQuotaErr != nil {
		return nil, s.getQuotaErr
	}
	return s.DataStore.GetQuota(ctx, orgID)
}

func (s quotaFailingStore) CountEndpoints(ctx context.Context, orgID string) (int, error) {
	if s.countErr != nil {
		return 0, s.countErr
	}
	return s.DataStore.CountEndpoints(ctx, orgID)
}

func TestRegisterFailsClosedWhenQuotaLookupFails(t *testing.T) {
	storeDown := errors.New("store down")
	tests := []struct {
		name string
		wrap func(store.DataStore) store.DataStore
	}{
		{"quota read fails", func(d store.DataStore) store.DataStore {
			return quotaFailingStore{DataStore: d, getQuotaErr: storeDown}
		}},
		{"endpoint count fails", func(d store.DataStore) store.DataStore {
			if err := d.SetQuota(context.Background(), &store.Quota{OrgID: "org1", MaxEndpoints: 5}); err != nil {
				t.Fatal(err)
			}
			return quotaFailingStore{DataStore: d, countErr: storeDown}
		}},
	}
	reqs := map[string]tunnel.EndpointRequest{
		"host":    {Ref: "0", Kind: store.KindHTTP, Subdomain: "shop"},
		"private": {Ref: "0", Kind: store.KindTCP, Private: true, Name: "db"},
		"port":    {Ref: "0", Kind: store.KindTCP},
	}
	for _, tc := range tests {
		for name, req := range reqs {
			t.Run(tc.name+"/"+name, func(t *testing.T) {
				g, ag1, _, data := newRegisterFixture(t, Options{Ports: &fakePorts{}})
				g.data = tc.wrap(data)
				ack := g.handleRegister(context.Background(), ag1, &tunnel.RegisterPayload{Endpoints: []tunnel.EndpointRequest{req}})
				b := ack.Endpoints[0]
				if b.Error == "" || b.EndpointID != "" {
					t.Fatalf("binding = %+v, want refusal", b)
				}
				if !strings.Contains(b.Error, "quota") {
					t.Fatalf("error = %q, want it to mention quota", b.Error)
				}
				n, err := data.CountEndpoints(context.Background(), "org1")
				if err != nil || n != 0 {
					t.Fatalf("endpoints created = %d (err %v), want 0", n, err)
				}
			})
		}
	}
}

func TestRegisterAllowsUnlimitedWhenNoQuotaConfigured(t *testing.T) {
	g, ag1, _, _ := newRegisterFixture(t, Options{})
	ack := g.handleRegister(context.Background(), ag1, &tunnel.RegisterPayload{Endpoints: []tunnel.EndpointRequest{{Ref: "0", Kind: store.KindHTTP, Subdomain: "shop"}}})
	if b := ack.Endpoints[0]; b.Error != "" {
		t.Fatalf("error = %q", b.Error)
	}
}
