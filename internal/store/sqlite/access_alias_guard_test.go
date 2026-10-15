package sqlite

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

func TestShouldRejectLateAliasPublicationAfterAccessEdit(t *testing.T) {
	s := newStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	orgID := createTypedOrg(t, s)
	connection := createTypedConnection(t, s, orgID)
	aliasDomain := strings.ReplaceAll(store.NewID("tail"), "_", "-") + ".ts.net"
	endpoint := createTypedEndpoint(t, s, orgID, "")
	endpoint.Policy = &store.EndpointPolicy{Access: []store.AccessRef{{ConnectionID: connection.ID, Hostname: "notes"}}}
	if err := s.UpdateEndpoint(ctx, endpoint); err != nil {
		t.Fatal(err)
	}
	old := []store.EndpointAlias{{Hostname: "notes." + aliasDomain, AccessHostname: "notes", Kind: store.KindHTTP, EndpointID: endpoint.ID}}
	if err := s.ReplaceConnectionAliases(ctx, orgID, connection.ID, old); err != nil {
		t.Fatal(err)
	}
	endpoint.Policy.Access[0].Hostname = "renamed"
	if err := s.UpdateEndpoint(ctx, endpoint); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetEndpointByAccessHostname(ctx, old[0].Hostname); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("old alias survived changed ref: %v", err)
	}
	if err := s.ReplaceConnectionAliases(ctx, orgID, connection.ID, old); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("late hostname publication: %v", err)
	}
	current := []store.EndpointAlias{{Hostname: "renamed." + aliasDomain, AccessHostname: "renamed", Kind: store.KindHTTP, EndpointID: endpoint.ID}}
	if err := s.ReplaceConnectionAliases(ctx, orgID, connection.ID, current); err != nil {
		t.Fatal(err)
	}
	endpoint.Kind = store.KindUDP
	if err := s.UpdateEndpoint(ctx, endpoint); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetEndpointByAccessHostname(ctx, current[0].Hostname); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("alias survived changed kind: %v", err)
	}
	if err := s.ReplaceConnectionAliases(ctx, orgID, connection.ID, current); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("late protocol publication: %v", err)
	}
	endpoint.Kind = store.KindHTTP
	endpoint.Policy.Access = nil
	if err := s.UpdateEndpoint(ctx, endpoint); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceConnectionAliases(ctx, orgID, connection.ID, current); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("removed access republished: %v", err)
	}
}

func TestShouldPreserveUnchangedAliasesAndRollbackInvalidEndpointPolicy(t *testing.T) {
	s := newStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	orgID := createTypedOrg(t, s)
	connection := createTypedConnection(t, s, orgID)
	aliasDomain := strings.ReplaceAll(store.NewID("tail"), "_", "-") + ".ts.net"
	endpoint := createTypedEndpoint(t, s, orgID, "")
	endpoint.Policy = &store.EndpointPolicy{Access: []store.AccessRef{{ConnectionID: connection.ID, Hostname: "Notes"}}, Connect: &store.ConnectPolicy{Name: "private-notes", AllowAgents: []string{"*"}}}
	if err := s.UpdateEndpoint(ctx, endpoint); err != nil {
		t.Fatal(err)
	}
	aliases := []store.EndpointAlias{{Hostname: "notes." + aliasDomain, AccessHostname: "notes", Kind: store.KindHTTP, EndpointID: endpoint.ID}}
	if err := s.ReplaceConnectionAliases(ctx, orgID, connection.ID, aliases); err != nil {
		t.Fatal(err)
	}
	endpoint.Policy.Access[0].Hostname = "notes."
	endpoint.Policy.Compression = true
	if err := s.UpdateEndpoint(ctx, endpoint); err != nil {
		t.Fatal(err)
	}
	got, err := s.GetEndpointByAccessHostname(ctx, aliases[0].Hostname)
	if err != nil || got.Policy.Connect == nil || got.Policy.Connect.Name != "private-notes" {
		t.Fatalf("unchanged alias/Connect lost: %+v %v", got, err)
	}
	endpoint.Policy.Access[0].ConnectionID = "conn_missing"
	if err := s.UpdateEndpoint(ctx, endpoint); err == nil {
		t.Fatal("missing connection accepted")
	}
	if _, err := s.GetEndpointByAccessHostname(ctx, aliases[0].Hostname); err != nil {
		t.Fatalf("failed policy write lost alias: %v", err)
	}
}

func TestShouldFenceAliasPublicationByConnectionSnapshot(t *testing.T) {
	s := newStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	orgID := createTypedOrg(t, s)
	connection := createTypedConnection(t, s, orgID)
	endpoint := createTypedEndpoint(t, s, orgID, "")
	host := strings.ReplaceAll(store.NewID("alias"), "_", "-") + ".example"
	endpoint.Policy = &store.EndpointPolicy{Access: []store.AccessRef{{ConnectionID: connection.ID, Hostname: ""}}}
	if err := s.UpdateEndpoint(ctx, endpoint); err != nil {
		t.Fatal(err)
	}
	snapshot, err := s.GetConnection(ctx, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	aliases := []store.EndpointAlias{{Hostname: host, AccessHostnameKnown: true, Kind: store.KindHTTP, EndpointID: endpoint.ID}}
	if err := s.ReplaceConnectionAliasesIfCurrent(ctx, snapshot, aliases); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateConnectionState(ctx, orgID, connection.ID, []byte(`{"private":"state"}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceConnectionAliasesIfCurrent(ctx, snapshot, aliases); err != nil {
		t.Fatalf("provider state invalidated authorized snapshot: %v", err)
	}
	endpoint.Policy.Compression = true
	if err := s.UpdateEndpoint(ctx, endpoint); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetEndpointByAccessHostname(ctx, host); err != nil {
		t.Fatalf("unchanged default ref lost alias: %v", err)
	}
	changed := *snapshot
	changed.Config = []byte(`{"network":"changed"}`)
	if err := s.UpdateConnectionIfCurrent(ctx, &changed, snapshot); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetEndpointByAccessHostname(ctx, host); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("changed connection retained alias: %v", err)
	}
	if err := s.ReplaceConnectionAliasesIfCurrent(ctx, snapshot, aliases); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("old generation republished: %v", err)
	}
	if _, err := s.GetEndpointByAccessHostname(ctx, host); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("stale publication changed aliases: %v", err)
	}
	current, err := s.GetConnection(ctx, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceConnectionAliasesIfCurrent(ctx, current, aliases); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceConnectionAliasesIfCurrent(ctx, snapshot, nil); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("old cleanup accepted: %v", err)
	}
	if _, err := s.GetEndpointByAccessHostname(ctx, host); err != nil {
		t.Fatalf("old cleanup erased current aliases: %v", err)
	}
	endpoint.Policy.Access[0].Hostname = "explicit"
	if err := s.UpdateEndpoint(ctx, endpoint); err != nil {
		t.Fatal(err)
	}
	if err := s.ReplaceConnectionAliasesIfCurrent(ctx, current, aliases); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("raw empty ref matched changed name: %v", err)
	}
}

func TestShouldSerializeAccessHostnameClaimsPerConnection(t *testing.T) {
	s := newStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	orgID := createTypedOrg(t, s)
	connection := createTypedConnection(t, s, orgID)
	separate := createTypedConnection(t, s, orgID)
	first, second := createTypedEndpoint(t, s, orgID, ""), createTypedEndpoint(t, s, orgID, "")
	host := strings.ReplaceAll(store.NewID("claim"), "_", "-") + ".example"
	first.Policy = &store.EndpointPolicy{Access: []store.AccessRef{{ConnectionID: connection.ID, Hostname: host}}}
	second.Policy = &store.EndpointPolicy{Access: []store.AccessRef{{ConnectionID: connection.ID, Hostname: strings.ToUpper(host) + "."}}}
	start := make(chan struct{})
	results := make(chan error, 2)
	for _, endpoint := range []*store.Endpoint{first, second} {
		go func(row *store.Endpoint) { <-start; results <- s.UpdateEndpoint(ctx, row) }(endpoint)
	}
	close(start)
	successes, conflicts := 0, 0
	for range 2 {
		err := <-results
		if err == nil {
			successes++
		} else if errors.Is(err, store.ErrConflict) {
			conflicts++
		} else {
			t.Fatal(err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("claim results successes=%d conflicts=%d", successes, conflicts)
	}
	var winner, loser *store.Endpoint
	for _, endpoint := range []*store.Endpoint{first, second} {
		row, err := s.GetEndpoint(ctx, endpoint.ID)
		if err != nil {
			t.Fatal(err)
		}
		if row.Policy != nil && len(row.Policy.Access) > 0 {
			winner = row
		} else {
			loser = row
		}
	}
	if winner == nil || loser == nil {
		t.Fatal("claim state not atomic")
	}
	loser.Policy = &store.EndpointPolicy{Access: []store.AccessRef{{ConnectionID: separate.ID, Hostname: host}}}
	if err := s.UpdateEndpoint(ctx, loser); err != nil {
		t.Fatalf("separate network hostname rejected: %v", err)
	}
	duplicate := *winner
	duplicate.Policy = &store.EndpointPolicy{Access: []store.AccessRef{{ConnectionID: connection.ID, Hostname: host}, {ConnectionID: connection.ID, Hostname: "another.example"}}}
	if err := s.UpdateEndpoint(ctx, &duplicate); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("duplicate connection accepted: %v", err)
	}
	unchanged, err := s.GetEndpoint(ctx, winner.ID)
	if err != nil || len(unchanged.Policy.Access) != 1 {
		t.Fatalf("duplicate mutation altered original: %+v %v", unchanged, err)
	}
}

func TestShouldFenceEndpointAccessByAuthorizedConnectionSnapshot(t *testing.T) {
	s := newStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	orgID := createTypedOrg(t, s)
	connection := createTypedConnection(t, s, orgID)
	endpoint := createTypedEndpoint(t, s, orgID, "")
	snapshot, err := s.GetConnection(ctx, connection.ID)
	if err != nil {
		t.Fatal(err)
	}
	endpoint.Policy = &store.EndpointPolicy{Access: []store.AccessRef{{ConnectionID: connection.ID, Hostname: "notes"}}}
	endpoint.ConnectionSnapshots = []*store.Connection{snapshot}
	if err := s.UpdateConnectionState(ctx, orgID, connection.ID, []byte(`{"private":"changed"}`)); err != nil {
		t.Fatal(err)
	}
	if err := s.UpdateEndpoint(ctx, endpoint); err != nil {
		t.Fatalf("State change rejected user snapshot: %v", err)
	}
	changed := *snapshot
	changed.Type = "runtime"
	if err := s.UpdateConnection(ctx, &changed); err != nil {
		t.Fatal(err)
	}
	endpoint.Policy.Access[0].Hostname = "altered"
	if err := s.UpdateEndpoint(ctx, endpoint); !errors.Is(err, store.ErrConflict) {
		t.Fatalf("stale role validation accepted: %v", err)
	}
	current, err := s.GetEndpoint(ctx, endpoint.ID)
	if err != nil || current.Policy.Access[0].Hostname != "notes" {
		t.Fatalf("stale validation changed persisted endpoint: %+v %v", current, err)
	}
}
