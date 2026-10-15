package migrationtest

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/mishmesh/mishmesh/internal/secrets"
)

const OrgID = "legacy-org"
const Created int64 = 1700000000123456789
const Updated int64 = 1700000000987654321
const TunnelToken = "legacy-tunnel-secret-sentinel"
const APIToken = "legacy-api-secret-sentinel"
const Policy = `@note("cloudflare:manage") permit(principal in Role::"member",action in [Action::"cloudflare:manage",Action::"endpoint:read"],resource); forbid(principal in Role::"admin",action == Action::"cloudflare:manage",resource);`

type Service struct {
	ID, Driver, Spec, Config, Ready string
	Idle, Start                     int
}

var Services = []Service{
	{"legacy-docker-1", "docker", `{"docker":{"containers":["web","db"]},"ready_tcp":"127.0.0.1:8001"}`, `{"containers":["web","db"]}`, `{"tcp":"127.0.0.1:8001"}`, -1, 120},
	{"legacy-docker-2", "docker", `{"docker":{"containers":["cache"]}}`, `{"containers":["cache"]}`, `{}`, 0, 0},
	{"legacy-docker-3", "docker", `{"docker":{"containers":["notes"]}}`, `{"containers":["notes"]}`, `{}`, 1800, 60},
	{"legacy-compose-1", "compose", `{"compose":{"project":"notes","file":"/apps/notes/compose.yml","bin":"docker"}}`, `{"project":"notes","file":"/apps/notes/compose.yml","bin":"docker"}`, `{}`, -1, 120},
	{"legacy-compose-2", "compose", `{"compose":{"project":"wiki","services":["web"]},"ready_tcp":"127.0.0.1:8002"}`, `{"project":"wiki","services":["web"]}`, `{"tcp":"127.0.0.1:8002"}`, 0, 30},
	{"legacy-exec-1", "exec", `{"exec":{"start":["sudo","systemctl","start","gdm"],"stop":["sudo","systemctl","stop","gdm"],"env":{"TOKEN":"command-env-sentinel"}},"ready_tcp":"127.0.0.1:3389"}`, `{"start":["sudo","systemctl","start","gdm"],"stop":["sudo","systemctl","stop","gdm"],"env":{"TOKEN":"command-env-sentinel"}}`, `{"tcp":"127.0.0.1:3389"}`, 900, 120},
	{"legacy-exec-2", "exec", `{"exec":{"start":["app","--arg with spaces"],"stop":["app","stop"]}}`, `{"start":["app","--arg with spaces"],"stop":["app","stop"]}`, `{}`, 0, 45},
	{"legacy-exec-3", "exec", `{"exec":{"start":["true"],"stop":["true"],"dir":"/tmp/app","env":{"A":"B"}}}`, `{"start":["true"],"stop":["true"],"dir":"/tmp/app","env":{"A":"B"}}`, `{}`, -1, 60},
}

func Populate(ctx context.Context, db *sql.DB, postgres bool, box *secrets.Box) error {
	backend := "sqlite"
	if postgres {
		backend = "postgres"
	}
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		return fmt.Errorf("literal migration schema location unavailable")
	}
	schema, err := os.ReadFile(filepath.Join(filepath.Dir(source), "../testdata/beta4_"+backend+".sql"))
	if err != nil {
		return err
	}
	if _, err := db.ExecContext(ctx, string(schema)); err != nil {
		return err
	}
	query := func(text string) string {
		if !postgres {
			return text
		}
		index := 0
		for strings.Contains(text, "?") {
			index++
			text = strings.Replace(text, "?", fmt.Sprintf("$%d", index), 1)
		}
		return text
	}
	exec := func(text string, args ...any) error { _, err := db.ExecContext(ctx, query(text), args...); return err }
	if err := exec(`INSERT INTO orgs(id,name,created_at)VALUES(?,?,?)`, OrgID, "Imported network", Created); err != nil {
		return err
	}
	if err := exec(`INSERT INTO agents(id,org_id,name,status,created_at)VALUES('legacy-agent',?,'agent','active',?)`, OrgID, Created); err != nil {
		return err
	}
	tunnel, err := box.Seal([]byte(TunnelToken), "cloudflare:"+OrgID+":tunnel_token")
	if err != nil {
		return err
	}
	api, err := box.Seal([]byte(APIToken), "cloudflare:"+OrgID+":api_token")
	if err != nil {
		return err
	}
	prune := any(0)
	if postgres {
		prune = false
	}
	if err := exec(`INSERT INTO cloudflare_connections(org_id,name,account_id,tunnel_id,tunnel_token_enc,api_token_enc,access_app_id,origin,connector,metrics_addr,prune,updated_at)VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, OrgID, "Imported access", "account", "tunnel", tunnel, api, "access-app", "http://127.0.0.1:8090", "external", "127.0.0.1:20241", prune, Updated); err != nil {
		return err
	}
	for _, service := range Services {
		if err := exec(`INSERT INTO services(id,org_id,name,driver,spec,idle_timeout_seconds,start_timeout_seconds,created_at,updated_at)VALUES(?,?,?,?,?,?,?,?,?)`, service.ID, OrgID, service.ID, service.Driver, service.Spec, service.Idle, service.Start, Created, Updated); err != nil {
			return err
		}
	}
	for index := 0; index < 12; index++ {
		policy, err := json.Marshal(map[string]any{"public_access": "cloudflare", "service_id": Services[index%len(Services)].ID, "connect": map[string]any{"name": fmt.Sprintf("app-%02d", index), "allow_agents": []string{"legacy-agent"}}, "request_headers_add": map[string]string{"X-Legacy": "preserved"}})
		if err != nil {
			return err
		}
		if err := exec(`INSERT INTO endpoints(id,agent_id,org_id,kind,lifecycle,domain,policy,created_at)VALUES(?,'legacy-agent',?,'http','reserved',?,?,?)`, fmt.Sprintf("legacy-endpoint-%02d", index), OrgID, fmt.Sprintf("app-%02d.example.org", index), string(policy), Created); err != nil {
			return err
		}
	}
	if err := exec(`INSERT INTO endpoints(id,agent_id,org_id,kind,lifecycle,domain,policy,created_at)VALUES('legacy-udp','legacy-agent',?,'udp','reserved','udp.example.org',?,?)`, OrgID, `{"public_access":"cloudflare","connect":{"name":"udp-app","allow_agents":["legacy-agent"]}}`, Created); err != nil {
		return err
	}
	return exec(`INSERT INTO org_policies(org_id,cedar_src,updated_at)VALUES(?,?,?)`, OrgID, Policy, Updated)
}
