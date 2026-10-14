package migrationtest

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"

	"github.com/mishmesh/mishmesh/internal/authz"
	"github.com/mishmesh/mishmesh/internal/secrets"
	"github.com/mishmesh/mishmesh/internal/store"
)

func AssertConverted(ctx context.Context, data store.DataStore, box *secrets.Box) (string, error) {
	records, err := data.ListConnections(ctx, OrgID)
	if err != nil || len(records) != 1 {
		return "", fmt.Errorf("connections %d %v", len(records), err)
	}
	record := records[0]
	if !strings.HasPrefix(record.ID, "conn_") || record.Type != "cloudflare" || record.Name != "Imported access" || record.CreatedAt.UnixNano() != Updated || record.UpdatedAt.UnixNano() != Updated || len(record.State) != 0 {
		return "", fmt.Errorf("connection identity or ownership changed: %+v", record)
	}
	var config map[string]any
	if err := json.Unmarshal(record.Config, &config); err != nil {
		return "", err
	}
	wantConfig := map[string]any{"account_id": "account", "tunnel_id": "tunnel", "access_app_id": "access-app", "origin": "http://127.0.0.1:8090", "connector": "external", "metrics_addr": "127.0.0.1:20241", "prune": false}
	if !reflect.DeepEqual(config, wantConfig) {
		return "", fmt.Errorf("connection configuration changed: %v", config)
	}
	plain, err := box.Open(record.SecretsEnc, "connection:"+OrgID+":"+record.ID)
	if err != nil {
		return "", err
	}
	var values map[string]string
	if err := json.Unmarshal(plain, &values); err != nil || !reflect.DeepEqual(values, map[string]string{"tunnel_token": TunnelToken, "api_token": APIToken}) {
		return "", fmt.Errorf("secrets changed %v", err)
	}
	for _, aad := range []string{"connection:other:" + record.ID, "connection:" + OrgID + ":another"} {
		if _, err := box.Open(record.SecretsEnc, aad); err == nil {
			return "", fmt.Errorf("ciphertext accepted incorrect identity")
		}
	}
	for _, expected := range Services {
		got, err := data.GetService(ctx, expected.ID)
		if err != nil {
			return "", err
		}
		driver := expected.Driver
		if driver == "exec" {
			driver = "command"
		}
		if got.ID != expected.ID || got.OrgID != OrgID || got.Name != expected.ID || got.Driver != driver || !EqualJSON(got.Config, []byte(expected.Config)) || !EqualJSON(got.Ready, []byte(expected.Ready)) || got.IdleTimeoutSeconds != expected.Idle || got.StartTimeoutSeconds != expected.Start || got.CreatedAt.UnixNano() != Created || got.UpdatedAt.UnixNano() != Updated || len(got.Requires) != 0 || len(got.ConnectionIDs) != 0 {
			return "", fmt.Errorf("service changed %+v", got)
		}
	}
	endpoints, err := data.ListEndpointsByOrg(ctx, OrgID)
	if err != nil || len(endpoints) != 13 {
		return "", fmt.Errorf("endpoints %d %v", len(endpoints), err)
	}
	for index := 0; index < 12; index++ {
		id := fmt.Sprintf("legacy-endpoint-%02d", index)
		ep, err := data.GetEndpoint(ctx, id)
		if err != nil {
			return "", err
		}
		if ep.ID != id || ep.OrgID != OrgID || ep.AgentID != "legacy-agent" || ep.Kind != store.KindHTTP || ep.Domain != fmt.Sprintf("app-%02d.example.org", index) || ep.CreatedAt.UnixNano() != Created || ep.Policy == nil || !reflect.DeepEqual(ep.Policy.Access, []store.AccessRef{{ConnectionID: record.ID, Hostname: ep.Domain}}) || ep.Policy.ServiceID != Services[index%len(Services)].ID || ep.Policy.Connect == nil || ep.Policy.Connect.Name != fmt.Sprintf("app-%02d", index) || !reflect.DeepEqual(ep.Policy.Connect.AllowAgents, []string{"legacy-agent"}) || ep.Policy.RequestHeadersAdd["X-Legacy"] != "preserved" {
			return "", fmt.Errorf("endpoint changed %+v", ep)
		}
	}
	udp, err := data.GetEndpoint(ctx, "legacy-udp")
	if err != nil || udp.Kind != store.KindUDP || udp.Policy == nil || len(udp.Policy.Access) != 0 || udp.Policy.Connect == nil || udp.Policy.Connect.Name != "udp-app" {
		return "", fmt.Errorf("UDP invented reachability or lost Connect %+v %v", udp, err)
	}
	policy, err := data.GetOrgPolicy(ctx, OrgID)
	if err != nil {
		return "", err
	}
	old, err := authz.New([]byte(Policy))
	if err != nil {
		return "", err
	}
	current, err := authz.New([]byte(policy.CedarSrc))
	if err != nil {
		return "", err
	}
	for _, role := range []string{authz.RoleOwner, authz.RoleAdmin, authz.RoleMember} {
		p := authz.Principal{ID: "user", Role: role, Org: OrgID}
		if old.Authorize(p, authz.Action("cloudflare:manage")) != current.Authorize(p, authz.ActionConnectionManage) || old.Authorize(p, authz.ActionEndpointRead) != current.Authorize(p, authz.ActionEndpointRead) {
			return "", fmt.Errorf("authorization changed for %s", role)
		}
	}
	if policy.UpdatedAt.UnixNano() != Updated || !strings.Contains(policy.CedarSrc, `@note("cloudflare:manage")`) {
		return "", fmt.Errorf("policy metadata changed")
	}
	return record.ID, nil
}

func EqualJSON(left, right []byte) bool {
	var a, b any
	return json.Unmarshal(left, &a) == nil && json.Unmarshal(right, &b) == nil && reflect.DeepEqual(a, b)
}
