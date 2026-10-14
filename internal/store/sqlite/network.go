package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mishmesh/mishmesh/internal/store"
)

const networkSchema = `
CREATE TABLE IF NOT EXISTS services (
  id                    TEXT PRIMARY KEY,
  org_id                TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  name                  TEXT NOT NULL,
  driver                TEXT NOT NULL,
  spec                  TEXT NOT NULL,
  idle_timeout_seconds  INTEGER NOT NULL DEFAULT 0,
  start_timeout_seconds INTEGER NOT NULL DEFAULT 0,
  created_at            INTEGER NOT NULL,
  updated_at            INTEGER NOT NULL,
  UNIQUE (org_id, name)
);

CREATE TABLE IF NOT EXISTS connections (
 id TEXT PRIMARY KEY,
 org_id TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
 type TEXT NOT NULL,
 name TEXT NOT NULL,
 config TEXT NOT NULL,
 secrets_enc BLOB,
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL,
 UNIQUE (org_id, name),
 UNIQUE (org_id, id)
);
CREATE UNIQUE INDEX IF NOT EXISTS services_org_id ON services (org_id, id);
CREATE UNIQUE INDEX IF NOT EXISTS endpoints_org_id ON endpoints (org_id, id);
CREATE TABLE IF NOT EXISTS service_requires (
 org_id TEXT NOT NULL,
 service_id TEXT NOT NULL,
 requires_id TEXT NOT NULL,
 PRIMARY KEY (service_id, requires_id),
 CHECK (service_id <> requires_id),
 FOREIGN KEY (org_id, service_id) REFERENCES services (org_id, id) ON DELETE CASCADE,
 FOREIGN KEY (org_id, requires_id) REFERENCES services (org_id, id) ON DELETE RESTRICT
);
CREATE TABLE IF NOT EXISTS service_connections (
 org_id TEXT NOT NULL,
 service_id TEXT NOT NULL,
 connection_id TEXT NOT NULL,
 PRIMARY KEY (service_id, connection_id),
 FOREIGN KEY (org_id, service_id) REFERENCES services (org_id, id) ON DELETE CASCADE,
 FOREIGN KEY (org_id, connection_id) REFERENCES connections (org_id, id) ON DELETE RESTRICT
);
CREATE TABLE IF NOT EXISTS endpoint_aliases (
 hostname TEXT PRIMARY KEY,
 org_id TEXT NOT NULL,
 endpoint_id TEXT NOT NULL,
 connection_id TEXT NOT NULL,
 FOREIGN KEY (org_id, endpoint_id) REFERENCES endpoints (org_id, id) ON DELETE CASCADE,
 FOREIGN KEY (org_id, connection_id) REFERENCES connections (org_id, id) ON DELETE RESTRICT
);
CREATE TABLE IF NOT EXISTS cloudflare_connections (
  org_id           TEXT PRIMARY KEY REFERENCES orgs(id) ON DELETE CASCADE,
  name             TEXT NOT NULL,
  account_id       TEXT NOT NULL,
  tunnel_id        TEXT NOT NULL,
  tunnel_token_enc BLOB,
  api_token_enc    BLOB,
  access_app_id    TEXT NOT NULL DEFAULT '',
  origin           TEXT NOT NULL DEFAULT '',
  connector        TEXT NOT NULL,
  metrics_addr     TEXT NOT NULL DEFAULT '',
  prune            INTEGER NOT NULL DEFAULT 0,
  updated_at       INTEGER NOT NULL
);
`

const serviceCols = `id, org_id, name, driver, spec, config, ready, idle_timeout_seconds, start_timeout_seconds, created_at, updated_at,
 (SELECT json_group_array(requires_id) FROM (SELECT requires_id FROM service_requires WHERE service_id = services.id ORDER BY requires_id)),
 (SELECT json_group_array(connection_id) FROM (SELECT connection_id FROM service_connections WHERE service_id = services.id ORDER BY connection_id))`

func (s *Store) CreateService(ctx context.Context, svc *store.Service) error {
	tx, err := s.beginNetworkWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := createServiceTx(ctx, tx, svc); err != nil {
		return err
	}
	return wrap("create service", tx.Commit())
}

func createServiceTx(ctx context.Context, tx *sql.Tx, svc *store.Service) error {
	spec, config, ready, err := serviceDocuments(svc)
	if err != nil {
		return err
	}
	if err := validateServiceRequirements(ctx, tx, svc, false); err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO services (id, org_id, name, driver, spec, config, ready, idle_timeout_seconds, start_timeout_seconds, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`, svc.ID, svc.OrgID, svc.Name, svc.Driver, spec, config, ready, svc.IdleTimeoutSeconds, svc.StartTimeoutSeconds, ns(svc.CreatedAt), ns(svc.UpdatedAt))
	if err != nil {
		return uniqueErr("create service", err)
	}
	if err := replaceServiceReferences(ctx, tx, svc); err != nil {
		return err
	}
	return nil
}

func serviceDocuments(svc *store.Service) (string, any, any, error) {
	spec := string(svc.Spec)
	if svc.Config != nil {
		if !json.Valid(svc.Config) {
			return "", nil, nil, fmt.Errorf("service config must be valid JSON")
		}
		spec = ""
	}
	if svc.Ready != nil && !json.Valid(svc.Ready) {
		return "", nil, nil, fmt.Errorf("service readiness must be valid JSON")
	}
	return spec, nullableJSON(svc.Config), nullableJSON(svc.Ready), nil
}

func nullableJSON(value json.RawMessage) any {
	if value == nil {
		return nil
	}
	return string(value)
}

func (s *Store) GetService(ctx context.Context, id string) (*store.Service, error) {
	return scanService(s.db.QueryRowContext(ctx, `SELECT `+serviceCols+` FROM services WHERE id = ?`, id))
}

func (s *Store) ListServices(ctx context.Context, orgID string) ([]*store.Service, error) {
	return s.queryServices(ctx, `SELECT `+serviceCols+` FROM services WHERE org_id = ? ORDER BY name`, orgID)
}

func (s *Store) ListAllServices(ctx context.Context) ([]*store.Service, error) {
	return s.queryServices(ctx, `SELECT `+serviceCols+` FROM services ORDER BY org_id, name`)
}

func (s *Store) ListServiceDependents(ctx context.Context, orgID, serviceID string) ([]*store.Service, error) {
	return s.queryServices(ctx, `SELECT `+serviceCols+` FROM services WHERE org_id = ? AND id IN (SELECT service_id FROM service_requires WHERE org_id = ? AND requires_id = ?) ORDER BY name, id`, orgID, orgID, serviceID)
}

func (s *Store) queryServices(ctx context.Context, query string, args ...any) ([]*store.Service, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrap("list services", err)
	}
	defer rows.Close()
	var out []*store.Service
	for rows.Next() {
		svc, err := scanService(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, svc)
	}
	return out, wrap("list services", rows.Err())
}

func (s *Store) UpdateService(ctx context.Context, svc *store.Service) error {
	tx, err := s.beginNetworkWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := updateServiceTx(ctx, tx, svc); err != nil {
		return err
	}
	return wrap("update service", tx.Commit())
}

func updateServiceTx(ctx context.Context, tx *sql.Tx, svc *store.Service) error {
	spec, config, ready, err := serviceDocuments(svc)
	if err != nil {
		return err
	}
	if err := validateServiceRequirements(ctx, tx, svc, true); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE services SET name = ?, driver = ?, spec = ?, config = ?, ready = ?, idle_timeout_seconds = ?, start_timeout_seconds = ?, updated_at = ? WHERE id = ? AND org_id = ?`, svc.Name, svc.Driver, spec, config, ready, svc.IdleTimeoutSeconds, svc.StartTimeoutSeconds, ns(svc.UpdatedAt), svc.ID, svc.OrgID)
	if err != nil {
		return uniqueErr("update service", err)
	}
	if err := affected("update service", res); err != nil {
		return err
	}
	if err := replaceServiceReferences(ctx, tx, svc); err != nil {
		return err
	}
	return nil
}

func replaceServiceReferences(ctx context.Context, tx *sql.Tx, svc *store.Service) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM service_requires WHERE service_id = ?`, svc.ID); err != nil {
		return wrap("replace requirements", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM service_connections WHERE service_id = ?`, svc.ID); err != nil {
		return wrap("replace service connections", err)
	}
	seen := make(map[string]bool)
	for _, id := range svc.Requires {
		if seen[id] {
			continue
		}
		seen[id] = true
		if _, err := tx.ExecContext(ctx, `INSERT INTO service_requires (org_id, service_id, requires_id) VALUES (?, ?, ?)`, svc.OrgID, svc.ID, id); err != nil {
			return wrap("replace requirements", err)
		}
	}
	seen = make(map[string]bool)
	for _, id := range svc.ConnectionIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		if _, err := tx.ExecContext(ctx, `INSERT INTO service_connections (org_id, service_id, connection_id) VALUES (?, ?, ?)`, svc.OrgID, svc.ID, id); err != nil {
			return wrap("replace service connections", err)
		}
	}
	return nil
}

func validateServiceRequirements(ctx context.Context, tx *sql.Tx, svc *store.Service, requireExisting bool) error {
	rows, err := tx.QueryContext(ctx, `SELECT `+serviceCols+` FROM services WHERE org_id = ? ORDER BY name, id`, svc.OrgID)
	if err != nil {
		return wrap("read requirement graph", err)
	}
	defer rows.Close()
	var services []*store.Service
	exists := false
	for rows.Next() {
		service, err := scanService(rows)
		if err != nil {
			return err
		}
		services = append(services, service)
		if service.ID == svc.ID {
			exists = true
		}
	}
	if err := rows.Err(); err != nil {
		return wrap("read requirement graph", err)
	}
	if requireExisting && !exists {
		return store.ErrNotFound
	}
	return store.ValidateRequirements(svc.OrgID, svc.ID, svc.Requires, append(services, svc))
}

func (s *Store) DeleteService(ctx context.Context, id string) error {
	tx, err := s.beginNetworkWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := deleteServiceTx(ctx, tx, id); err != nil {
		return err
	}
	return wrap("delete service", tx.Commit())
}

func checkServiceDeleteTx(ctx context.Context, tx *sql.Tx, id string) error {
	var references int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM service_requires WHERE requires_id = ?`, id).Scan(&references); err != nil {
		return wrap("check required service", err)
	}
	if references > 0 {
		return store.ErrConflict
	}
	policies, err := endpointPolicies(ctx, tx)
	if err != nil {
		return err
	}
	for _, policy := range policies {
		if policy.ServiceID == id {
			return store.ErrConflict
		}
	}
	return nil
}

func deleteServiceTx(ctx context.Context, tx *sql.Tx, id string) error {
	if err := checkServiceDeleteTx(ctx, tx, id); err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM services WHERE id = ?`, id)
	if err != nil {
		return wrap("delete service", err)
	}
	if err := affected("delete service", res); err != nil {
		return err
	}
	return nil
}

func scanService(row interface{ Scan(...any) error }) (*store.Service, error) {
	var svc store.Service
	var spec string
	var config, ready, requires, connections sql.NullString
	var created, updated int64
	if err := row.Scan(&svc.ID, &svc.OrgID, &svc.Name, &svc.Driver, &spec, &config, &ready, &svc.IdleTimeoutSeconds, &svc.StartTimeoutSeconds, &created, &updated, &requires, &connections); err != nil {
		return nil, scanErr("scan service", err)
	}
	if config.Valid {
		svc.Config = json.RawMessage(config.String)
	} else {
		svc.Spec = json.RawMessage(spec)
	}
	if ready.Valid {
		svc.Ready = json.RawMessage(ready.String)
	}
	if requires.Valid {
		if err := json.Unmarshal([]byte(requires.String), &svc.Requires); err != nil {
			return nil, wrap("scan requirements", err)
		}
	}
	if connections.Valid {
		if err := json.Unmarshal([]byte(connections.String), &svc.ConnectionIDs); err != nil {
			return nil, wrap("scan service connections", err)
		}
	}
	svc.CreatedAt, svc.UpdatedAt = fromNS(created), fromNS(updated)
	return &svc, nil
}

const connectionCols = `id, org_id, type, name, config, secrets_enc, created_at, updated_at, state`

func (s *Store) CreateConnection(ctx context.Context, c *store.Connection) error {
	if !json.Valid(c.Config) {
		return fmt.Errorf("connection config must be valid JSON")
	}
	if len(c.State) > 0 && !json.Valid(c.State) {
		return fmt.Errorf("connection state must be valid JSON")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO connections (`+connectionCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`, c.ID, c.OrgID, c.Type, c.Name, string(c.Config), nullBytes(c.SecretsEnc), ns(c.CreatedAt), ns(c.UpdatedAt), nullableJSON(c.State))
	return uniqueErr("create connection", err)
}

func (s *Store) GetConnection(ctx context.Context, id string) (*store.Connection, error) {
	return scanConnection(s.db.QueryRowContext(ctx, `SELECT `+connectionCols+` FROM connections WHERE id = ?`, id))
}

func (s *Store) ListConnections(ctx context.Context, orgID string) ([]*store.Connection, error) {
	return s.queryConnections(ctx, `SELECT `+connectionCols+` FROM connections WHERE org_id = ? ORDER BY name`, orgID)
}

func (s *Store) ListAllConnections(ctx context.Context) ([]*store.Connection, error) {
	return s.queryConnections(ctx, `SELECT `+connectionCols+` FROM connections ORDER BY org_id, name`)
}

func (s *Store) queryConnections(ctx context.Context, query string, args ...any) ([]*store.Connection, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrap("list connections", err)
	}
	defer rows.Close()
	var out []*store.Connection
	for rows.Next() {
		c, err := scanConnection(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, wrap("list connections", rows.Err())
}

func (s *Store) UpdateConnection(ctx context.Context, c *store.Connection) error {
	if !json.Valid(c.Config) {
		return fmt.Errorf("connection config must be valid JSON")
	}
	res, err := s.db.ExecContext(ctx, `UPDATE connections SET type = ?, name = ?, config = ?, secrets_enc = ?, updated_at = ? WHERE id = ? AND org_id = ?`, c.Type, c.Name, string(c.Config), nullBytes(c.SecretsEnc), ns(c.UpdatedAt), c.ID, c.OrgID)
	if err != nil {
		return uniqueErr("update connection", err)
	}
	return affected("update connection", res)
}

func (s *Store) UpdateConnectionIfCurrent(ctx context.Context, c, expected *store.Connection) error {
	if c.ID != expected.ID || c.OrgID != expected.OrgID || c.Type != expected.Type || !json.Valid(c.Config) {
		return store.ErrConflict
	}
	result, err := s.db.ExecContext(ctx, `UPDATE connections SET name = ?, config = ?, secrets_enc = ?, updated_at = ? WHERE id = ? AND org_id = ? AND type = ? AND name = ? AND config = ? AND secrets_enc IS ? AND updated_at = ?`, c.Name, string(c.Config), nullBytes(c.SecretsEnc), ns(c.UpdatedAt), expected.ID, expected.OrgID, expected.Type, expected.Name, string(expected.Config), nullBytes(expected.SecretsEnc), ns(expected.UpdatedAt))
	if err != nil {
		return uniqueErr("update connection", err)
	}
	count, err := result.RowsAffected()
	if err != nil {
		return wrap("update connection", err)
	}
	if count == 0 {
		return store.ErrConflict
	}
	return nil
}

func (s *Store) DeleteConnectionIfCurrent(ctx context.Context, expected *store.Connection) error {
	return s.deleteConnection(ctx, expected.ID, expected)
}

func (s *Store) DeleteConnection(ctx context.Context, id string) error {
	return s.deleteConnection(ctx, id, nil)
}

func (s *Store) deleteConnection(ctx context.Context, id string, expected *store.Connection) error {
	tx, err := s.beginNetworkWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if expected != nil {
		current, err := scanConnection(tx.QueryRowContext(ctx, `SELECT `+connectionCols+` FROM connections WHERE id = ?`, id))
		if err != nil {
			return err
		}
		if current.OrgID != expected.OrgID || current.Type != expected.Type || current.Name != expected.Name || !bytes.Equal(current.Config, expected.Config) || !bytes.Equal(current.SecretsEnc, expected.SecretsEnc) || !current.UpdatedAt.Equal(expected.UpdatedAt) {
			return store.ErrConflict
		}
	}
	var references int
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM service_connections WHERE connection_id = ?) + (SELECT COUNT(*) FROM endpoint_aliases WHERE connection_id = ?)`, id, id).Scan(&references); err != nil {
		return wrap("check connection references", err)
	}
	if references > 0 {
		return connectionInUse(ctx, tx, id)
	}
	policies, err := endpointPolicies(ctx, tx)
	if err != nil {
		return err
	}
	for _, policy := range policies {
		for _, ref := range policy.Access {
			if ref.ConnectionID == id {
				return connectionInUse(ctx, tx, id)
			}
		}
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM connections WHERE id = ?`, id)
	if err != nil {
		return wrap("delete connection", err)
	}
	if err := affected("delete connection", res); err != nil {
		return err
	}
	return wrap("delete connection", tx.Commit())
}

func connectionInUse(ctx context.Context, tx *sql.Tx, id string) error {
	references := []store.ConnectionReference{}
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT 'service', s.id, s.name FROM services s JOIN service_connections sc ON sc.service_id = s.id WHERE sc.connection_id = ? AND s.org_id = (SELECT org_id FROM connections WHERE id = ?) UNION SELECT DISTINCT 'endpoint', e.id, COALESCE(e.subdomain, e.domain, '') FROM endpoints e JOIN endpoint_aliases a ON a.endpoint_id = e.id WHERE a.connection_id = ? AND e.org_id = (SELECT org_id FROM connections WHERE id = ?)`, id, id, id, id)
	if err != nil {
		return wrap("read connection references", err)
	}
	for rows.Next() {
		var reference store.ConnectionReference
		if err := rows.Scan(&reference.Kind, &reference.ID, &reference.Name); err != nil {
			rows.Close()
			return wrap("read connection references", err)
		}
		references = append(references, reference)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return wrap("read connection references", err)
	}
	rows, err = tx.QueryContext(ctx, `SELECT id, COALESCE(subdomain, domain, ''), policy FROM endpoints WHERE org_id = (SELECT org_id FROM connections WHERE id = ?) AND policy IS NOT NULL AND policy <> ''`, id)
	if err != nil {
		return wrap("read connection references", err)
	}
	defer rows.Close()
	for rows.Next() {
		var reference store.ConnectionReference
		var raw string
		reference.Kind = "endpoint"
		if err := rows.Scan(&reference.ID, &reference.Name, &raw); err != nil {
			return wrap("read connection references", err)
		}
		var policy store.EndpointPolicy
		if err := json.Unmarshal([]byte(raw), &policy); err != nil {
			return wrap("read connection references", err)
		}
		for _, access := range policy.Access {
			if access.ConnectionID == id {
				duplicate := false
				for _, existing := range references {
					duplicate = duplicate || existing.Kind == reference.Kind && existing.ID == reference.ID
				}
				if !duplicate {
					references = append(references, reference)
				}
				break
			}
		}
	}
	if err := rows.Err(); err != nil {
		return wrap("read connection references", err)
	}
	return &store.ConnectionInUseError{References: references}
}

func (s *Store) UpdateConnectionState(ctx context.Context, orgID, connectionID string, state json.RawMessage) error {
	if len(state) > 0 && !json.Valid(state) {
		return fmt.Errorf("connection state must be valid JSON")
	}
	result, err := s.db.ExecContext(ctx, `UPDATE connections SET state = ? WHERE org_id = ? AND id = ?`, nullableJSON(state), orgID, connectionID)
	if err != nil {
		return wrap("update connection state", err)
	}
	return affected("update connection state", result)
}

func scanConnection(row interface{ Scan(...any) error }) (*store.Connection, error) {
	var c store.Connection
	var config string
	var state sql.NullString
	var created, updated int64
	if err := row.Scan(&c.ID, &c.OrgID, &c.Type, &c.Name, &config, &c.SecretsEnc, &created, &updated, &state); err != nil {
		return nil, scanErr("scan connection", err)
	}
	c.Config = json.RawMessage(config)
	if state.Valid {
		c.State = json.RawMessage(state.String)
	}
	c.CreatedAt, c.UpdatedAt = fromNS(created), fromNS(updated)
	return &c, nil
}

func (s *Store) beginNetworkWrite(ctx context.Context) (*sql.Tx, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, wrap("begin network write", err)
	}
	if _, err := tx.ExecContext(ctx, `UPDATE orgs SET name = name WHERE id = (SELECT id FROM orgs ORDER BY id LIMIT 1)`); err != nil {
		tx.Rollback()
		return nil, wrap("lock network write", err)
	}
	return tx, nil
}

func endpointPolicies(ctx context.Context, tx *sql.Tx) ([]store.EndpointPolicy, error) {
	rows, err := tx.QueryContext(ctx, `SELECT policy FROM endpoints WHERE policy IS NOT NULL AND policy <> ''`)
	if err != nil {
		return nil, wrap("read endpoint references", err)
	}
	defer rows.Close()
	var out []store.EndpointPolicy
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return nil, wrap("read endpoint references", err)
		}
		var policy store.EndpointPolicy
		if err := json.Unmarshal([]byte(raw), &policy); err != nil {
			return nil, wrap("decode endpoint references", err)
		}
		out = append(out, policy)
	}
	return out, wrap("read endpoint references", rows.Err())
}

const cloudflareCols = `org_id, name, account_id, tunnel_id, tunnel_token_enc, api_token_enc, access_app_id, origin, connector, metrics_addr, prune, updated_at`

func (s *Store) GetCloudflareConnection(ctx context.Context, orgID string) (*store.CloudflareConnection, error) {
	return scanCloudflare(s.db.QueryRowContext(ctx, `SELECT `+cloudflareCols+` FROM cloudflare_connections WHERE org_id = ?`, orgID))
}

func (s *Store) ListCloudflareConnections(ctx context.Context) ([]*store.CloudflareConnection, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+cloudflareCols+` FROM cloudflare_connections ORDER BY org_id`)
	if err != nil {
		return nil, wrap("list cloudflare connections", err)
	}
	defer rows.Close()
	var out []*store.CloudflareConnection
	for rows.Next() {
		c, err := scanCloudflare(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, wrap("list cloudflare connections", rows.Err())
}

func (s *Store) SetCloudflareConnection(ctx context.Context, c *store.CloudflareConnection) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO cloudflare_connections (`+cloudflareCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(org_id) DO UPDATE SET name = excluded.name, account_id = excluded.account_id, tunnel_id = excluded.tunnel_id,
		   tunnel_token_enc = excluded.tunnel_token_enc, api_token_enc = excluded.api_token_enc, access_app_id = excluded.access_app_id,
		   origin = excluded.origin, connector = excluded.connector, metrics_addr = excluded.metrics_addr, prune = excluded.prune,
		   updated_at = excluded.updated_at`,
		c.OrgID, c.Name, c.AccountID, c.TunnelID, nullBytes(c.TunnelTokenEnc), nullBytes(c.APITokenEnc), c.AccessAppID, c.Origin,
		c.Connector, c.MetricsAddr, boolInt(c.Prune), ns(c.UpdatedAt))
	return wrap("set cloudflare connection", err)
}

func (s *Store) DeleteCloudflareConnection(ctx context.Context, orgID string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM cloudflare_connections WHERE org_id = ?`, orgID)
	if err != nil {
		return wrap("delete cloudflare connection", err)
	}
	return affected("delete cloudflare connection", res)
}

func scanCloudflare(row interface{ Scan(...any) error }) (*store.CloudflareConnection, error) {
	var c store.CloudflareConnection
	var prune int
	var updated int64
	if err := row.Scan(&c.OrgID, &c.Name, &c.AccountID, &c.TunnelID, &c.TunnelTokenEnc, &c.APITokenEnc, &c.AccessAppID, &c.Origin,
		&c.Connector, &c.MetricsAddr, &prune, &updated); err != nil {
		return nil, scanErr("scan cloudflare connection", err)
	}
	c.Prune = prune != 0
	c.UpdatedAt = fromNS(updated)
	return &c, nil
}

func uniqueErr(op string, err error) error {
	if err != nil && strings.Contains(err.Error(), "UNIQUE constraint failed") {
		return store.ErrConflict
	}
	return wrap(op, err)
}

func affected(op string, res interface{ RowsAffected() (int64, error) }) error {
	n, err := res.RowsAffected()
	if err != nil {
		return wrap(op, err)
	}
	if n == 0 {
		return store.ErrNotFound
	}
	return nil
}

func nullBytes(b []byte) any {
	if len(b) == 0 {
		return nil
	}
	return b
}

func boolInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
