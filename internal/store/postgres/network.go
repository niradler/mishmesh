package postgres

import (
	"context"
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

const serviceCols = `id, org_id, name, driver, spec, idle_timeout_seconds, start_timeout_seconds, created_at, updated_at`

func (s *Store) CreateService(ctx context.Context, svc *store.Service) error {
	ctx, cancel := context.WithTimeout(ctx, s.queryTimeout)
	defer cancel()
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO services (`+serviceCols+`) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		svc.ID, svc.OrgID, svc.Name, svc.Driver, string(svc.Spec), svc.IdleTimeoutSeconds, svc.StartTimeoutSeconds, ns(svc.CreatedAt), ns(svc.UpdatedAt))
	return uniqueErr("create service", err)
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
	res, err := s.db.ExecContext(ctx,
		`UPDATE services SET name = $1, driver = $2, spec = $3, idle_timeout_seconds = $4, start_timeout_seconds = $5, updated_at = $6 WHERE id = $7`,
		svc.Name, svc.Driver, string(svc.Spec), svc.IdleTimeoutSeconds, svc.StartTimeoutSeconds, ns(svc.UpdatedAt), svc.ID)
	if err != nil {
		return uniqueErr("update service", err)
	}
	return affected("update service", res)
}

func (s *Store) DeleteService(ctx context.Context, id string) error {
	ctx, cancel := context.WithTimeout(ctx, s.queryTimeout)
	defer cancel()
	res, err := s.db.ExecContext(ctx, `DELETE FROM services WHERE id = $1`, id)
	if err != nil {
		return wrap("delete service", err)
	}
	return affected("delete service", res)
}

func scanService(row interface{ Scan(...any) error }) (*store.Service, error) {
	var svc store.Service
	var spec string
	var created, updated int64
	if err := row.Scan(&svc.ID, &svc.OrgID, &svc.Name, &svc.Driver, &spec, &svc.IdleTimeoutSeconds, &svc.StartTimeoutSeconds, &created, &updated); err != nil {
		return nil, scanErr("scan service", err)
	}
	svc.Spec = []byte(spec)
	svc.CreatedAt = fromNS(created)
	svc.UpdatedAt = fromNS(updated)
	return &svc, nil
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
