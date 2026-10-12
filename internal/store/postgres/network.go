package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"errors"

	"github.com/jackc/pgx/v5/pgconn"

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
  created_at            BIGINT NOT NULL,
  updated_at            BIGINT NOT NULL,
  UNIQUE (org_id, name)
);

CREATE TABLE IF NOT EXISTS connections (
 id TEXT PRIMARY KEY,
 org_id TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
 type TEXT NOT NULL,
 name TEXT NOT NULL,
 config TEXT NOT NULL,
 secrets_enc BYTEA,
 created_at BIGINT NOT NULL,
 updated_at BIGINT NOT NULL,
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
  tunnel_token_enc BYTEA,
  api_token_enc    BYTEA,
  access_app_id    TEXT NOT NULL DEFAULT '',
  origin           TEXT NOT NULL DEFAULT '',
  connector        TEXT NOT NULL,
  metrics_addr     TEXT NOT NULL DEFAULT '',
  prune            BOOLEAN NOT NULL DEFAULT FALSE,
  updated_at       BIGINT NOT NULL
);
`

const serviceCols = `id, org_id, name, driver, spec, config, ready, idle_timeout_seconds, start_timeout_seconds, created_at, updated_at,
 (SELECT json_agg(requires_id ORDER BY requires_id)::text FROM service_requires WHERE service_id = services.id),
 (SELECT json_agg(connection_id ORDER BY connection_id)::text FROM service_connections WHERE service_id = services.id)`

func (s *Store) CreateService(ctx context.Context, svc *store.Service) error {
	ctx, cancel := context.WithTimeout(ctx, s.queryTimeout)
	defer cancel()
	tx, err := s.beginNetworkWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	spec, config, ready, err := serviceDocuments(svc)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO services (id, org_id, name, driver, spec, config, ready, idle_timeout_seconds, start_timeout_seconds, created_at, updated_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)`, svc.ID, svc.OrgID, svc.Name, svc.Driver, spec, config, ready, svc.IdleTimeoutSeconds, svc.StartTimeoutSeconds, ns(svc.CreatedAt), ns(svc.UpdatedAt))
	if err != nil {
		return uniqueErr("create service", err)
	}
	if err := replaceServiceReferences(ctx, tx, svc); err != nil {
		return err
	}
	return wrap("create service", tx.Commit())
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
	ctx, cancel := context.WithTimeout(ctx, s.queryTimeout)
	defer cancel()
	return scanService(s.db.QueryRowContext(ctx, `SELECT `+serviceCols+` FROM services WHERE id = $1`, id))
}

func (s *Store) ListServices(ctx context.Context, orgID string) ([]*store.Service, error) {
	ctx, cancel := context.WithTimeout(ctx, s.queryTimeout)
	defer cancel()
	return s.queryServices(ctx, `SELECT `+serviceCols+` FROM services WHERE org_id = $1 ORDER BY name`, orgID)
}

func (s *Store) ListAllServices(ctx context.Context) ([]*store.Service, error) {
	ctx, cancel := context.WithTimeout(ctx, s.queryTimeout)
	defer cancel()
	return s.queryServices(ctx, `SELECT `+serviceCols+` FROM services ORDER BY org_id, name`)
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
	ctx, cancel := context.WithTimeout(ctx, s.queryTimeout)
	defer cancel()
	tx, err := s.beginNetworkWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	spec, config, ready, err := serviceDocuments(svc)
	if err != nil {
		return err
	}
	res, err := tx.ExecContext(ctx, `UPDATE services SET name = $1, driver = $2, spec = $3, config = $4, ready = $5, idle_timeout_seconds = $6, start_timeout_seconds = $7, updated_at = $8 WHERE id = $9 AND org_id = $10`, svc.Name, svc.Driver, spec, config, ready, svc.IdleTimeoutSeconds, svc.StartTimeoutSeconds, ns(svc.UpdatedAt), svc.ID, svc.OrgID)
	if err != nil {
		return uniqueErr("update service", err)
	}
	if err := affected("update service", res); err != nil {
		return err
	}
	if err := replaceServiceReferences(ctx, tx, svc); err != nil {
		return err
	}
	return wrap("update service", tx.Commit())
}

func replaceServiceReferences(ctx context.Context, tx *sql.Tx, svc *store.Service) error {
	if _, err := tx.ExecContext(ctx, `DELETE FROM service_requires WHERE service_id = $1`, svc.ID); err != nil {
		return wrap("replace requirements", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM service_connections WHERE service_id = $1`, svc.ID); err != nil {
		return wrap("replace service connections", err)
	}
	seen := make(map[string]bool)
	for _, id := range svc.Requires {
		if seen[id] {
			continue
		}
		seen[id] = true
		if id == svc.ID {
			return fmt.Errorf("service cannot require itself: %w", store.ErrConflict)
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO service_requires (org_id, service_id, requires_id) VALUES ($1, $2, $3)`, svc.OrgID, svc.ID, id); err != nil {
			return wrap("replace requirements", err)
		}
	}
	seen = make(map[string]bool)
	for _, id := range svc.ConnectionIDs {
		if seen[id] {
			continue
		}
		seen[id] = true
		if _, err := tx.ExecContext(ctx, `INSERT INTO service_connections (org_id, service_id, connection_id) VALUES ($1, $2, $3)`, svc.OrgID, svc.ID, id); err != nil {
			return wrap("replace service connections", err)
		}
	}
	return nil
}

func (s *Store) DeleteService(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(ctx, s.queryTimeout)
	defer cancel()
	tx, err := s.beginNetworkWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var references int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM service_requires WHERE requires_id = $1`, id).Scan(&references); err != nil {
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
	res, err := tx.ExecContext(ctx, `DELETE FROM services WHERE id = $1`, id)
	if err != nil {
		return wrap("delete service", err)
	}
	if err := affected("delete service", res); err != nil {
		return err
	}
	return wrap("delete service", tx.Commit())
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

const connectionCols = `id, org_id, type, name, config, secrets_enc, created_at, updated_at`

func (s *Store) CreateConnection(ctx context.Context, c *store.Connection) error {
	ctx, cancel := context.WithTimeout(ctx, s.queryTimeout)
	defer cancel()
	if !json.Valid(c.Config) {
		return fmt.Errorf("connection config must be valid JSON")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO connections (`+connectionCols+`) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)`, c.ID, c.OrgID, c.Type, c.Name, string(c.Config), nullBytes(c.SecretsEnc), ns(c.CreatedAt), ns(c.UpdatedAt))
	return uniqueErr("create connection", err)
}

func (s *Store) GetConnection(ctx context.Context, id string) (*store.Connection, error) {
	ctx, cancel := context.WithTimeout(ctx, s.queryTimeout)
	defer cancel()
	return scanConnection(s.db.QueryRowContext(ctx, `SELECT `+connectionCols+` FROM connections WHERE id = $1`, id))
}

func (s *Store) ListConnections(ctx context.Context, orgID string) ([]*store.Connection, error) {
	ctx, cancel := context.WithTimeout(ctx, s.queryTimeout)
	defer cancel()
	return s.queryConnections(ctx, `SELECT `+connectionCols+` FROM connections WHERE org_id = $1 ORDER BY name`, orgID)
}

func (s *Store) ListAllConnections(ctx context.Context) ([]*store.Connection, error) {
	ctx, cancel := context.WithTimeout(ctx, s.queryTimeout)
	defer cancel()
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
	ctx, cancel := context.WithTimeout(ctx, s.queryTimeout)
	defer cancel()
	if !json.Valid(c.Config) {
		return fmt.Errorf("connection config must be valid JSON")
	}
	res, err := s.db.ExecContext(ctx, `UPDATE connections SET type = $1, name = $2, config = $3, secrets_enc = $4, updated_at = $5 WHERE id = $6 AND org_id = $7`, c.Type, c.Name, string(c.Config), nullBytes(c.SecretsEnc), ns(c.UpdatedAt), c.ID, c.OrgID)
	if err != nil {
		return uniqueErr("update connection", err)
	}
	return affected("update connection", res)
}

func (s *Store) DeleteConnection(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(ctx, s.queryTimeout)
	defer cancel()
	tx, err := s.beginNetworkWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var references int
	if err := tx.QueryRowContext(ctx, `SELECT (SELECT COUNT(*) FROM service_connections WHERE connection_id = $1) + (SELECT COUNT(*) FROM endpoint_aliases WHERE connection_id = $2)`, id, id).Scan(&references); err != nil {
		return wrap("check connection references", err)
	}
	if references > 0 {
		return store.ErrConflict
	}
	policies, err := endpointPolicies(ctx, tx)
	if err != nil {
		return err
	}
	for _, policy := range policies {
		for _, ref := range policy.Access {
			if ref.ConnectionID == id {
				return store.ErrConflict
			}
		}
	}
	res, err := tx.ExecContext(ctx, `DELETE FROM connections WHERE id = $1`, id)
	if err != nil {
		return wrap("delete connection", err)
	}
	if err := affected("delete connection", res); err != nil {
		return err
	}
	return wrap("delete connection", tx.Commit())
}

func scanConnection(row interface{ Scan(...any) error }) (*store.Connection, error) {
	var c store.Connection
	var config string
	var created, updated int64
	if err := row.Scan(&c.ID, &c.OrgID, &c.Type, &c.Name, &config, &c.SecretsEnc, &created, &updated); err != nil {
		return nil, scanErr("scan connection", err)
	}
	c.Config = json.RawMessage(config)
	c.CreatedAt, c.UpdatedAt = fromNS(created), fromNS(updated)
	return &c, nil
}

const networkWriteLockKey int64 = 731907244

func (s *Store) beginNetworkWrite(ctx context.Context) (*sql.Tx, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, wrap("begin network write", err)
	}
	if _, err := tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock($1)`, networkWriteLockKey); err != nil {
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
	ctx, cancel := context.WithTimeout(ctx, s.queryTimeout)
	defer cancel()
	return scanCloudflare(s.db.QueryRowContext(ctx, `SELECT `+cloudflareCols+` FROM cloudflare_connections WHERE org_id = $1`, orgID))
}

func (s *Store) ListCloudflareConnections(ctx context.Context) ([]*store.CloudflareConnection, error) {
	ctx, cancel := context.WithTimeout(ctx, s.queryTimeout)
	defer cancel()
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
	ctx, cancel := context.WithTimeout(ctx, s.queryTimeout)
	defer cancel()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO cloudflare_connections (`+cloudflareCols+`) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12)
		 ON CONFLICT (org_id) DO UPDATE SET name = EXCLUDED.name, account_id = EXCLUDED.account_id, tunnel_id = EXCLUDED.tunnel_id,
		   tunnel_token_enc = EXCLUDED.tunnel_token_enc, api_token_enc = EXCLUDED.api_token_enc, access_app_id = EXCLUDED.access_app_id,
		   origin = EXCLUDED.origin, connector = EXCLUDED.connector, metrics_addr = EXCLUDED.metrics_addr, prune = EXCLUDED.prune,
		   updated_at = EXCLUDED.updated_at`,
		c.OrgID, c.Name, c.AccountID, c.TunnelID, nullBytes(c.TunnelTokenEnc), nullBytes(c.APITokenEnc), c.AccessAppID, c.Origin,
		c.Connector, c.MetricsAddr, c.Prune, ns(c.UpdatedAt))
	return wrap("set cloudflare connection", err)
}

func (s *Store) DeleteCloudflareConnection(ctx context.Context, orgID string) error {
	ctx, cancel := context.WithTimeout(ctx, s.queryTimeout)
	defer cancel()
	res, err := s.db.ExecContext(ctx, `DELETE FROM cloudflare_connections WHERE org_id = $1`, orgID)
	if err != nil {
		return wrap("delete cloudflare connection", err)
	}
	return affected("delete cloudflare connection", res)
}

func scanCloudflare(row interface{ Scan(...any) error }) (*store.CloudflareConnection, error) {
	var c store.CloudflareConnection
	var updated int64
	if err := row.Scan(&c.OrgID, &c.Name, &c.AccountID, &c.TunnelID, &c.TunnelTokenEnc, &c.APITokenEnc, &c.AccessAppID, &c.Origin,
		&c.Connector, &c.MetricsAddr, &c.Prune, &updated); err != nil {
		return nil, scanErr("scan cloudflare connection", err)
	}
	c.UpdatedAt = fromNS(updated)
	return &c, nil
}

func uniqueErr(op string, err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
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
