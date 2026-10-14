package controlplane

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/mishmesh/mishmesh/internal/connections"
	"github.com/mishmesh/mishmesh/internal/store"
)

func TestShouldKeepConnectionHostnameNodesDistinctAndScoped(t *testing.T) {
	f := newNetworkFixture(t)
	f.connectCloudflare(t)
	required := f.createService(t, `{"name":"database","driver":"docker","config":{"containers":["database"]}}`)
	dependent := f.createService(t, `{"name":"web","driver":"docker","config":{"containers":["web"]},"requires":["`+required.ID+`"]}`)
	ep := f.createEndpoint(t, `{"method":"proxy","domain":"notes.example.test","policy":{"proxy_targets":["93.184.216.34:80"],"service_id":"`+dependent.ID+`","access":[{"connection_id":"legacy-trigger","hostname":"notes.example.test"}]}}`)
	ctx := context.Background()
	for _, row := range []*store.Connection{
		{ID: "second-cf", OrgID: defaultOrgID, Name: "Second Cloudflare", Type: "cloudflare", Config: json.RawMessage(`{}`)},
		{ID: "runtime", OrgID: defaultOrgID, Name: "Runtime provider", Type: "third", Config: json.RawMessage(`{}`)},
	} {
		row.CreatedAt, row.UpdatedAt = time.Now(), time.Now()
		if err := f.api.data.CreateConnection(ctx, row); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.api.data.CreateOrg(ctx, &store.Org{ID: "foreign", Name: "Foreign", CreatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	if err := f.api.data.CreateConnection(ctx, &store.Connection{ID: "foreign-connection", OrgID: "foreign", Name: "Foreign secret", Type: "third", Config: json.RawMessage(`{}`), CreatedAt: time.Now(), UpdatedAt: time.Now()}); err != nil {
		t.Fatal(err)
	}
	f.hub.mu.Lock()
	f.hub.statuses["legacy-trigger"] = connections.Status{Health: "up", Hostnames: []connections.HostStatus{{Host: "notes.example.test", EndpointID: ep.ID, State: "published"}}}
	f.hub.statuses["second-cf"] = connections.Status{Health: "down", Hostnames: []connections.HostStatus{{Host: "notes.example.test", State: "unclaimed"}, {Host: "foreign-name.example.test", EndpointID: "foreign-endpoint", State: "published"}}}
	f.hub.statuses["runtime"] = connections.Status{Health: "unrecognized"}
	f.hub.mu.Unlock()
	topology := fetchTopology(t, f.srv)
	nodes := map[string]topologyNode{}
	for _, node := range topology.Nodes {
		nodes[node.ID] = node
	}
	for _, identity := range []string{"connection:legacy-trigger", "connection:second-cf", "connection:runtime", "hostname:legacy-trigger:notes.example.test", "hostname:second-cf:notes.example.test", "service:" + required.ID} {
		if _, exists := nodes[identity]; !exists {
			t.Fatalf("missing node %s", identity)
		}
	}
	if nodes["connection:second-cf"].State != "down" || nodes["connection:runtime"].State != "unknown" {
		t.Fatalf("provider states: %+v", nodes)
	}
	found := false
	for _, edge := range topology.Edges {
		if _, ok := nodes[edge.From]; !ok {
			t.Fatalf("dangling source %+v", edge)
		}
		if _, ok := nodes[edge.To]; !ok {
			t.Fatalf("dangling destination %+v", edge)
		}
		if edge == (topologyEdge{From: "service:" + dependent.ID, To: "service:" + required.ID, Kind: edgeRequires}) {
			found = true
		}
	}
	if !found {
		t.Fatal("missing dependent-to-required edge")
	}
	raw, err := json.Marshal(topology)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "foreign") || strings.Contains(string(raw), "hidden") || strings.Contains(string(raw), "private_key") {
		t.Fatalf("topology leaked foreign or secret metadata: %s", raw)
	}
}
