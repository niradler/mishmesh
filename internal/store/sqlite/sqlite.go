package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	_ "modernc.org/sqlite"

	"github.com/mishmesh/mishmesh/internal/store"
)

type Store struct {
	db *sql.DB
}

var _ store.DataStore = (*Store)(nil)

func Open(dsn string) (*Store, error) {
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite %q: %w", dsn, err)
	}
	db.SetMaxOpenConns(1)
	for _, pragma := range []string{
		"PRAGMA journal_mode=WAL",
		"PRAGMA busy_timeout=5000",
		"PRAGMA foreign_keys=ON",
		"PRAGMA synchronous=NORMAL",
	} {
		if _, err := db.Exec(pragma); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("apply %q: %w", pragma, err)
		}
	}
	s := &Store{db: db}
	if err := s.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) migrate(ctx context.Context) error {
	const schema = `
CREATE TABLE IF NOT EXISTS orgs (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS agents (
  id           TEXT PRIMARY KEY,
  org_id       TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  name         TEXT NOT NULL,
  status       TEXT NOT NULL,
  created_at   INTEGER NOT NULL,
  last_seen_at INTEGER
);
CREATE TABLE IF NOT EXISTS tokens (
  id         TEXT PRIMARY KEY,
  org_id     TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  agent_id   TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
  hash       TEXT NOT NULL UNIQUE,
  created_at INTEGER NOT NULL,
  revoked_at INTEGER
);
CREATE TABLE IF NOT EXISTS endpoints (
  id         TEXT PRIMARY KEY,
  agent_id   TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
  org_id     TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  kind       TEXT NOT NULL,
  lifecycle  TEXT NOT NULL,
  subdomain  TEXT,
  created_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_endpoints_subdomain
  ON endpoints(subdomain) WHERE subdomain IS NOT NULL AND subdomain <> '';
`
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("migrate: %w", err)
	}
	return nil
}

func (s *Store) CreateOrg(ctx context.Context, o *store.Org) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO orgs (id, name, created_at) VALUES (?, ?, ?)`,
		o.ID, o.Name, ns(o.CreatedAt))
	return wrap("create org", err)
}

func (s *Store) GetOrg(ctx context.Context, id string) (*store.Org, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, name, created_at FROM orgs WHERE id = ?`, id)
	var o store.Org
	var created int64
	if err := row.Scan(&o.ID, &o.Name, &created); err != nil {
		return nil, scanErr("get org", err)
	}
	o.CreatedAt = fromNS(created)
	return &o, nil
}

func (s *Store) CreateAgent(ctx context.Context, a *store.Agent) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO agents (id, org_id, name, status, created_at, last_seen_at) VALUES (?, ?, ?, ?, ?, ?)`,
		a.ID, a.OrgID, a.Name, a.Status, ns(a.CreatedAt), nsPtr(a.LastSeenAt))
	return wrap("create agent", err)
}

func (s *Store) GetAgent(ctx context.Context, id string) (*store.Agent, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, org_id, name, status, created_at, last_seen_at FROM agents WHERE id = ?`, id)
	return scanAgent(row.Scan)
}

func (s *Store) ListAgents(ctx context.Context, orgID string) ([]*store.Agent, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, org_id, name, status, created_at, last_seen_at FROM agents WHERE org_id = ? ORDER BY created_at`, orgID)
	if err != nil {
		return nil, wrap("list agents", err)
	}
	defer rows.Close()
	var out []*store.Agent
	for rows.Next() {
		a, err := scanAgent(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, wrap("list agents", rows.Err())
}

func (s *Store) UpdateAgent(ctx context.Context, a *store.Agent) error {
	_, err := s.db.ExecContext(ctx, `UPDATE agents SET name = ?, status = ? WHERE id = ?`, a.Name, a.Status, a.ID)
	return wrap("update agent", err)
}

func (s *Store) DeleteAgent(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM agents WHERE id = ?`, id)
	return wrap("delete agent", err)
}

func (s *Store) TouchAgent(ctx context.Context, id string, seenAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE agents SET last_seen_at = ? WHERE id = ?`, ns(seenAt), id)
	return wrap("touch agent", err)
}

func scanAgent(scan func(...any) error) (*store.Agent, error) {
	var a store.Agent
	var created int64
	var lastSeen sql.NullInt64
	if err := scan(&a.ID, &a.OrgID, &a.Name, &a.Status, &created, &lastSeen); err != nil {
		return nil, scanErr("scan agent", err)
	}
	a.CreatedAt = fromNS(created)
	if lastSeen.Valid {
		t := fromNS(lastSeen.Int64)
		a.LastSeenAt = &t
	}
	return &a, nil
}

func (s *Store) CreateToken(ctx context.Context, t *store.Token) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO tokens (id, org_id, agent_id, hash, created_at, revoked_at) VALUES (?, ?, ?, ?, ?, ?)`,
		t.ID, t.OrgID, t.AgentID, t.Hash, ns(t.CreatedAt), nsPtr(t.RevokedAt))
	return wrap("create token", err)
}

func (s *Store) GetTokenByHash(ctx context.Context, hash string) (*store.Token, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, org_id, agent_id, hash, created_at, revoked_at FROM tokens WHERE hash = ? AND revoked_at IS NULL`, hash)
	var t store.Token
	var created int64
	var revoked sql.NullInt64
	if err := row.Scan(&t.ID, &t.OrgID, &t.AgentID, &t.Hash, &created, &revoked); err != nil {
		return nil, scanErr("get token", err)
	}
	t.CreatedAt = fromNS(created)
	if revoked.Valid {
		rt := fromNS(revoked.Int64)
		t.RevokedAt = &rt
	}
	return &t, nil
}

func (s *Store) RevokeToken(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tokens SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL`,
		ns(time.Now()), id)
	return wrap("revoke token", err)
}

func (s *Store) ListTokensByAgent(ctx context.Context, agentID string) ([]*store.Token, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, org_id, agent_id, hash, created_at, revoked_at FROM tokens WHERE agent_id = ? ORDER BY created_at`, agentID)
	if err != nil {
		return nil, wrap("list tokens", err)
	}
	defer rows.Close()
	var out []*store.Token
	for rows.Next() {
		var t store.Token
		var created int64
		var revoked sql.NullInt64
		if err := rows.Scan(&t.ID, &t.OrgID, &t.AgentID, &t.Hash, &created, &revoked); err != nil {
			return nil, scanErr("scan token", err)
		}
		t.CreatedAt = fromNS(created)
		if revoked.Valid {
			rt := fromNS(revoked.Int64)
			t.RevokedAt = &rt
		}
		out = append(out, &t)
	}
	return out, wrap("list tokens", rows.Err())
}

func (s *Store) RevokeTokensByAgent(ctx context.Context, agentID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tokens SET revoked_at = ? WHERE agent_id = ? AND revoked_at IS NULL`,
		ns(time.Now()), agentID)
	return wrap("revoke agent tokens", err)
}

func (s *Store) CreateEndpoint(ctx context.Context, e *store.Endpoint) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO endpoints (id, agent_id, org_id, kind, lifecycle, subdomain, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		e.ID, e.AgentID, e.OrgID, e.Kind, e.Lifecycle, nullStr(e.Subdomain), ns(e.CreatedAt))
	return wrap("create endpoint", err)
}

func (s *Store) GetEndpoint(ctx context.Context, id string) (*store.Endpoint, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, agent_id, org_id, kind, lifecycle, subdomain, created_at FROM endpoints WHERE id = ?`, id)
	return scanEndpoint(row.Scan)
}

func (s *Store) GetEndpointBySubdomain(ctx context.Context, subdomain string) (*store.Endpoint, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, agent_id, org_id, kind, lifecycle, subdomain, created_at FROM endpoints WHERE subdomain = ?`, subdomain)
	return scanEndpoint(row.Scan)
}

func (s *Store) ListEndpointsByAgent(ctx context.Context, agentID string) ([]*store.Endpoint, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, agent_id, org_id, kind, lifecycle, subdomain, created_at FROM endpoints WHERE agent_id = ? ORDER BY created_at`, agentID)
	if err != nil {
		return nil, wrap("list endpoints", err)
	}
	defer rows.Close()
	var out []*store.Endpoint
	for rows.Next() {
		e, err := scanEndpoint(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, wrap("list endpoints", rows.Err())
}

func (s *Store) DeleteEndpoint(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM endpoints WHERE id = ?`, id)
	return wrap("delete endpoint", err)
}

func scanEndpoint(scan func(...any) error) (*store.Endpoint, error) {
	var e store.Endpoint
	var created int64
	var sub sql.NullString
	if err := scan(&e.ID, &e.AgentID, &e.OrgID, &e.Kind, &e.Lifecycle, &sub, &created); err != nil {
		return nil, scanErr("scan endpoint", err)
	}
	e.Subdomain = sub.String
	e.CreatedAt = fromNS(created)
	return &e, nil
}

func ns(t time.Time) int64 { return t.UTC().UnixNano() }

func nsPtr(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC().UnixNano()
}

func fromNS(n int64) time.Time { return time.Unix(0, n).UTC() }

func nullStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func wrap(op string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("sqlite %s: %w", op, err)
}

func scanErr(op string, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrNotFound
	}
	return wrap(op, err)
}
