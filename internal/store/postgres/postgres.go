package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/mishmesh/mishmesh/internal/store"
)

type Store struct {
	db *sql.DB
}

var _ store.DataStore = (*Store)(nil)

func Open(dsn string) (*Store, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("open postgres %q: %w", dsn, err)
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
  created_at BIGINT NOT NULL
);
CREATE TABLE IF NOT EXISTS agents (
  id           TEXT PRIMARY KEY,
  org_id       TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  name         TEXT NOT NULL,
  status       TEXT NOT NULL,
  created_at   BIGINT NOT NULL,
  last_seen_at BIGINT
);
CREATE TABLE IF NOT EXISTS tokens (
  id         TEXT PRIMARY KEY,
  org_id     TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  agent_id   TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
  hash       TEXT NOT NULL UNIQUE,
  created_at BIGINT NOT NULL,
  revoked_at BIGINT
);
CREATE TABLE IF NOT EXISTS endpoints (
  id         TEXT PRIMARY KEY,
  agent_id   TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
  org_id     TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  kind       TEXT NOT NULL,
  lifecycle  TEXT NOT NULL,
  subdomain  TEXT,
  domain     TEXT,
  port       INTEGER NOT NULL DEFAULT 0,
  policy     TEXT,
  created_at BIGINT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_endpoints_subdomain
  ON endpoints(subdomain) WHERE subdomain IS NOT NULL AND subdomain <> '';
CREATE UNIQUE INDEX IF NOT EXISTS idx_endpoints_domain
  ON endpoints(domain) WHERE domain IS NOT NULL AND domain <> '';
CREATE TABLE IF NOT EXISTS quotas (
  org_id              TEXT PRIMARY KEY REFERENCES orgs(id) ON DELETE CASCADE,
  max_agents          INTEGER NOT NULL DEFAULT 0,
  max_endpoints       INTEGER NOT NULL DEFAULT 0,
  max_bandwidth_bytes BIGINT NOT NULL DEFAULT 0,
  updated_at          BIGINT NOT NULL
);
CREATE TABLE IF NOT EXISTS users (
  id            TEXT PRIMARY KEY,
  email         TEXT NOT NULL UNIQUE,
  name          TEXT NOT NULL,
  password_hash TEXT,
  google_sub    TEXT,
  created_at    BIGINT NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_google_sub
  ON users(google_sub) WHERE google_sub IS NOT NULL AND google_sub <> '';
CREATE TABLE IF NOT EXISTS memberships (
  org_id     TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role       TEXT NOT NULL,
  created_at BIGINT NOT NULL,
  PRIMARY KEY (org_id, user_id)
);
CREATE TABLE IF NOT EXISTS sessions (
  id_hash    TEXT PRIMARY KEY,
  user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  org_id     TEXT NOT NULL,
  created_at BIGINT NOT NULL,
  expires_at BIGINT NOT NULL
);
CREATE TABLE IF NOT EXISTS audit (
  id         TEXT PRIMARY KEY,
  org_id     TEXT NOT NULL,
  actor      TEXT NOT NULL,
  action     TEXT NOT NULL,
  target     TEXT,
  detail     TEXT,
  created_at BIGINT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_audit_org ON audit(org_id, created_at);
`
	if _, err := s.db.ExecContext(ctx, schema); err != nil {
		return fmt.Errorf("postgres migrate: %w", err)
	}
	for _, alter := range []string{
		`ALTER TABLE endpoints ADD COLUMN IF NOT EXISTS domain TEXT`,
		`ALTER TABLE endpoints ADD COLUMN IF NOT EXISTS policy TEXT`,
	} {
		if _, err := s.db.ExecContext(ctx, alter); err != nil {
			return fmt.Errorf("postgres migrate alter: %w", err)
		}
	}
	return nil
}

func (s *Store) CreateOrg(ctx context.Context, o *store.Org) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO orgs (id, name, created_at) VALUES ($1, $2, $3)`, o.ID, o.Name, ns(o.CreatedAt))
	return wrap("create org", err)
}

func (s *Store) GetOrg(ctx context.Context, id string) (*store.Org, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, name, created_at FROM orgs WHERE id = $1`, id)
	var o store.Org
	var created int64
	if err := row.Scan(&o.ID, &o.Name, &created); err != nil {
		return nil, scanErr("get org", err)
	}
	o.CreatedAt = fromNS(created)
	return &o, nil
}

func (s *Store) ListOrgs(ctx context.Context) ([]*store.Org, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, created_at FROM orgs ORDER BY created_at`)
	if err != nil {
		return nil, wrap("list orgs", err)
	}
	defer rows.Close()
	var out []*store.Org
	for rows.Next() {
		var o store.Org
		var created int64
		if err := rows.Scan(&o.ID, &o.Name, &created); err != nil {
			return nil, scanErr("scan org", err)
		}
		o.CreatedAt = fromNS(created)
		out = append(out, &o)
	}
	return out, wrap("list orgs", rows.Err())
}

func (s *Store) CreateAgent(ctx context.Context, a *store.Agent) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO agents (id, org_id, name, status, created_at, last_seen_at) VALUES ($1, $2, $3, $4, $5, $6)`,
		a.ID, a.OrgID, a.Name, a.Status, ns(a.CreatedAt), nsPtr(a.LastSeenAt))
	return wrap("create agent", err)
}

func (s *Store) GetAgent(ctx context.Context, id string) (*store.Agent, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id, org_id, name, status, created_at, last_seen_at FROM agents WHERE id = $1`, id)
	return scanAgent(row.Scan)
}

func (s *Store) ListAgents(ctx context.Context, orgID string) ([]*store.Agent, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, org_id, name, status, created_at, last_seen_at FROM agents WHERE org_id = $1 ORDER BY created_at`, orgID)
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
	_, err := s.db.ExecContext(ctx, `UPDATE agents SET name = $1, status = $2 WHERE id = $3`, a.Name, a.Status, a.ID)
	return wrap("update agent", err)
}

func (s *Store) DeleteAgent(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM agents WHERE id = $1`, id)
	return wrap("delete agent", err)
}

func (s *Store) TouchAgent(ctx context.Context, id string, seenAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE agents SET last_seen_at = $1 WHERE id = $2`, ns(seenAt), id)
	return wrap("touch agent", err)
}

func (s *Store) CountAgents(ctx context.Context, orgID string) (int, error) {
	return s.count(ctx, `SELECT COUNT(*) FROM agents WHERE org_id = $1 AND status != $2`, orgID, store.AgentRevoked)
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
		`INSERT INTO tokens (id, org_id, agent_id, hash, created_at, revoked_at) VALUES ($1, $2, $3, $4, $5, $6)`,
		t.ID, t.OrgID, t.AgentID, t.Hash, ns(t.CreatedAt), nsPtr(t.RevokedAt))
	return wrap("create token", err)
}

func (s *Store) GetTokenByHash(ctx context.Context, hash string) (*store.Token, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT id, org_id, agent_id, hash, created_at, revoked_at FROM tokens WHERE hash = $1 AND revoked_at IS NULL`, hash)
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

func (s *Store) ListTokensByAgent(ctx context.Context, agentID string) ([]*store.Token, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, org_id, agent_id, hash, created_at, revoked_at FROM tokens WHERE agent_id = $1 ORDER BY created_at`, agentID)
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

func (s *Store) RevokeToken(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tokens SET revoked_at = $1 WHERE id = $2 AND revoked_at IS NULL`, ns(time.Now()), id)
	return wrap("revoke token", err)
}

func (s *Store) RevokeTokensByAgent(ctx context.Context, agentID string) error {
	_, err := s.db.ExecContext(ctx, `UPDATE tokens SET revoked_at = $1 WHERE agent_id = $2 AND revoked_at IS NULL`, ns(time.Now()), agentID)
	return wrap("revoke agent tokens", err)
}

const endpointCols = `id, agent_id, org_id, kind, lifecycle, subdomain, domain, port, policy, created_at`

func (s *Store) CreateEndpoint(ctx context.Context, e *store.Endpoint) error {
	pol, err := marshalPolicy(e.Policy)
	if err != nil {
		return wrap("create endpoint", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO endpoints (id, agent_id, org_id, kind, lifecycle, subdomain, domain, port, policy, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)`,
		e.ID, e.AgentID, e.OrgID, e.Kind, e.Lifecycle, nullStr(e.Subdomain), nullStr(e.Domain), e.Port, pol, ns(e.CreatedAt))
	return wrap("create endpoint", err)
}

func (s *Store) GetEndpoint(ctx context.Context, id string) (*store.Endpoint, error) {
	return scanEndpoint(s.db.QueryRowContext(ctx, `SELECT `+endpointCols+` FROM endpoints WHERE id = $1`, id).Scan)
}

func (s *Store) GetEndpointBySubdomain(ctx context.Context, subdomain string) (*store.Endpoint, error) {
	return scanEndpoint(s.db.QueryRowContext(ctx, `SELECT `+endpointCols+` FROM endpoints WHERE subdomain = $1`, subdomain).Scan)
}

func (s *Store) GetEndpointByDomain(ctx context.Context, domain string) (*store.Endpoint, error) {
	return scanEndpoint(s.db.QueryRowContext(ctx, `SELECT `+endpointCols+` FROM endpoints WHERE domain = $1`, domain).Scan)
}

func (s *Store) ListEndpointsByAgent(ctx context.Context, agentID string) ([]*store.Endpoint, error) {
	return s.queryEndpoints(ctx, `SELECT `+endpointCols+` FROM endpoints WHERE agent_id = $1 ORDER BY created_at`, agentID)
}

func (s *Store) ListEndpointsByOrg(ctx context.Context, orgID string) ([]*store.Endpoint, error) {
	return s.queryEndpoints(ctx, `SELECT `+endpointCols+` FROM endpoints WHERE org_id = $1 ORDER BY created_at`, orgID)
}

func (s *Store) queryEndpoints(ctx context.Context, query string, args ...any) ([]*store.Endpoint, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
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

func (s *Store) UpdateEndpoint(ctx context.Context, e *store.Endpoint) error {
	pol, err := marshalPolicy(e.Policy)
	if err != nil {
		return wrap("update endpoint", err)
	}
	_, err = s.db.ExecContext(ctx,
		`UPDATE endpoints SET kind = $1, lifecycle = $2, subdomain = $3, domain = $4, port = $5, policy = $6 WHERE id = $7`,
		e.Kind, e.Lifecycle, nullStr(e.Subdomain), nullStr(e.Domain), e.Port, pol, e.ID)
	return wrap("update endpoint", err)
}

func (s *Store) DeleteEndpoint(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM endpoints WHERE id = $1`, id)
	return wrap("delete endpoint", err)
}

func (s *Store) CountEndpoints(ctx context.Context, orgID string) (int, error) {
	return s.count(ctx, `SELECT COUNT(*) FROM endpoints WHERE org_id = $1`, orgID)
}

func scanEndpoint(scan func(...any) error) (*store.Endpoint, error) {
	var e store.Endpoint
	var created int64
	var sub, domain, pol sql.NullString
	if err := scan(&e.ID, &e.AgentID, &e.OrgID, &e.Kind, &e.Lifecycle, &sub, &domain, &e.Port, &pol, &created); err != nil {
		return nil, scanErr("scan endpoint", err)
	}
	e.Subdomain = sub.String
	e.Domain = domain.String
	e.CreatedAt = fromNS(created)
	if pol.Valid && pol.String != "" {
		var p store.EndpointPolicy
		if err := json.Unmarshal([]byte(pol.String), &p); err != nil {
			return nil, wrap("scan endpoint policy", err)
		}
		e.Policy = &p
	}
	return &e, nil
}

func (s *Store) GetQuota(ctx context.Context, orgID string) (*store.Quota, error) {
	row := s.db.QueryRowContext(ctx,
		`SELECT org_id, max_agents, max_endpoints, max_bandwidth_bytes, updated_at FROM quotas WHERE org_id = $1`, orgID)
	var q store.Quota
	var updated int64
	if err := row.Scan(&q.OrgID, &q.MaxAgents, &q.MaxEndpoints, &q.MaxBandwidthBytes, &updated); err != nil {
		return nil, scanErr("get quota", err)
	}
	q.UpdatedAt = fromNS(updated)
	return &q, nil
}

func (s *Store) SetQuota(ctx context.Context, q *store.Quota) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO quotas (org_id, max_agents, max_endpoints, max_bandwidth_bytes, updated_at)
		 VALUES ($1, $2, $3, $4, $5)
		 ON CONFLICT (org_id) DO UPDATE SET max_agents = EXCLUDED.max_agents,
		   max_endpoints = EXCLUDED.max_endpoints, max_bandwidth_bytes = EXCLUDED.max_bandwidth_bytes,
		   updated_at = EXCLUDED.updated_at`,
		q.OrgID, q.MaxAgents, q.MaxEndpoints, q.MaxBandwidthBytes, ns(time.Now()))
	return wrap("set quota", err)
}

func (s *Store) CreateUser(ctx context.Context, u *store.User) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO users (id, email, name, password_hash, google_sub, created_at) VALUES ($1, $2, $3, $4, $5, $6)`,
		u.ID, strings.ToLower(u.Email), u.Name, nullStr(u.PasswordHash), nullStr(u.GoogleSub), ns(u.CreatedAt))
	return wrap("create user", err)
}

func (s *Store) GetUserByID(ctx context.Context, id string) (*store.User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT id, email, name, password_hash, google_sub, created_at FROM users WHERE id = $1`, id).Scan)
}

func (s *Store) GetUserByEmail(ctx context.Context, email string) (*store.User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT id, email, name, password_hash, google_sub, created_at FROM users WHERE email = $1`, strings.ToLower(email)).Scan)
}

func (s *Store) GetUserByGoogleSub(ctx context.Context, sub string) (*store.User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT id, email, name, password_hash, google_sub, created_at FROM users WHERE google_sub = $1`, sub).Scan)
}

func (s *Store) UpdateUser(ctx context.Context, u *store.User) error {
	_, err := s.db.ExecContext(ctx,
		`UPDATE users SET email = $1, name = $2, password_hash = $3, google_sub = $4 WHERE id = $5`,
		strings.ToLower(u.Email), u.Name, nullStr(u.PasswordHash), nullStr(u.GoogleSub), u.ID)
	return wrap("update user", err)
}

func (s *Store) CountUsers(ctx context.Context) (int, error) {
	return s.count(ctx, `SELECT COUNT(*) FROM users`)
}

func scanUser(scan func(...any) error) (*store.User, error) {
	var u store.User
	var created int64
	var pw, sub sql.NullString
	if err := scan(&u.ID, &u.Email, &u.Name, &pw, &sub, &created); err != nil {
		return nil, scanErr("scan user", err)
	}
	u.PasswordHash = pw.String
	u.GoogleSub = sub.String
	u.CreatedAt = fromNS(created)
	return &u, nil
}

func (s *Store) CreateMembership(ctx context.Context, m *store.Membership) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO memberships (org_id, user_id, role, created_at) VALUES ($1, $2, $3, $4)
		 ON CONFLICT (org_id, user_id) DO UPDATE SET role = EXCLUDED.role`,
		m.OrgID, m.UserID, m.Role, ns(m.CreatedAt))
	return wrap("create membership", err)
}

func (s *Store) GetMembership(ctx context.Context, orgID, userID string) (*store.Membership, error) {
	row := s.db.QueryRowContext(ctx, `SELECT org_id, user_id, role, created_at FROM memberships WHERE org_id = $1 AND user_id = $2`, orgID, userID)
	return scanMembership(row.Scan)
}

func (s *Store) ListMembershipsByUser(ctx context.Context, userID string) ([]*store.Membership, error) {
	return s.queryMemberships(ctx, `SELECT org_id, user_id, role, created_at FROM memberships WHERE user_id = $1 ORDER BY created_at`, userID)
}

func (s *Store) ListMembershipsByOrg(ctx context.Context, orgID string) ([]*store.Membership, error) {
	return s.queryMemberships(ctx, `SELECT org_id, user_id, role, created_at FROM memberships WHERE org_id = $1 ORDER BY created_at`, orgID)
}

func (s *Store) queryMemberships(ctx context.Context, query string, args ...any) ([]*store.Membership, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrap("list memberships", err)
	}
	defer rows.Close()
	var out []*store.Membership
	for rows.Next() {
		m, err := scanMembership(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, wrap("list memberships", rows.Err())
}

func (s *Store) UpdateMembership(ctx context.Context, m *store.Membership) error {
	_, err := s.db.ExecContext(ctx, `UPDATE memberships SET role = $1 WHERE org_id = $2 AND user_id = $3`, m.Role, m.OrgID, m.UserID)
	return wrap("update membership", err)
}

func (s *Store) DeleteMembership(ctx context.Context, orgID, userID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM memberships WHERE org_id = $1 AND user_id = $2`, orgID, userID)
	return wrap("delete membership", err)
}

func scanMembership(scan func(...any) error) (*store.Membership, error) {
	var m store.Membership
	var created int64
	if err := scan(&m.OrgID, &m.UserID, &m.Role, &created); err != nil {
		return nil, scanErr("scan membership", err)
	}
	m.CreatedAt = fromNS(created)
	return &m, nil
}

func (s *Store) CreateSession(ctx context.Context, sess *store.Session) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO sessions (id_hash, user_id, org_id, created_at, expires_at) VALUES ($1, $2, $3, $4, $5)`,
		sess.IDHash, sess.UserID, sess.OrgID, ns(sess.CreatedAt), ns(sess.ExpiresAt))
	return wrap("create session", err)
}

func (s *Store) GetSession(ctx context.Context, idHash string) (*store.Session, error) {
	row := s.db.QueryRowContext(ctx, `SELECT id_hash, user_id, org_id, created_at, expires_at FROM sessions WHERE id_hash = $1`, idHash)
	var sess store.Session
	var created, expires int64
	if err := row.Scan(&sess.IDHash, &sess.UserID, &sess.OrgID, &created, &expires); err != nil {
		return nil, scanErr("get session", err)
	}
	sess.CreatedAt = fromNS(created)
	sess.ExpiresAt = fromNS(expires)
	return &sess, nil
}

func (s *Store) DeleteSession(ctx context.Context, idHash string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id_hash = $1`, idHash)
	return wrap("delete session", err)
}

func (s *Store) DeleteExpiredSessions(ctx context.Context, now time.Time) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at < $1`, ns(now))
	return wrap("delete expired sessions", err)
}

func (s *Store) AppendAudit(ctx context.Context, e *store.AuditEvent) error {
	_, err := s.db.ExecContext(ctx,
		`INSERT INTO audit (id, org_id, actor, action, target, detail, created_at) VALUES ($1, $2, $3, $4, $5, $6, $7)`,
		e.ID, e.OrgID, e.Actor, e.Action, nullStr(e.Target), nullStr(e.Detail), ns(e.CreatedAt))
	return wrap("append audit", err)
}

func (s *Store) ListAudit(ctx context.Context, orgID string, limit int) ([]*store.AuditEvent, error) {
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id, org_id, actor, action, target, detail, created_at FROM audit WHERE org_id = $1 ORDER BY created_at DESC LIMIT $2`, orgID, limit)
	if err != nil {
		return nil, wrap("list audit", err)
	}
	defer rows.Close()
	var out []*store.AuditEvent
	for rows.Next() {
		var e store.AuditEvent
		var created int64
		var target, detail sql.NullString
		if err := rows.Scan(&e.ID, &e.OrgID, &e.Actor, &e.Action, &target, &detail, &created); err != nil {
			return nil, scanErr("scan audit", err)
		}
		e.Target = target.String
		e.Detail = detail.String
		e.CreatedAt = fromNS(created)
		out = append(out, &e)
	}
	return out, wrap("list audit", rows.Err())
}

func (s *Store) count(ctx context.Context, query string, args ...any) (int, error) {
	var n int
	if err := s.db.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
		return 0, wrap("count", err)
	}
	return n, nil
}

func marshalPolicy(p *store.EndpointPolicy) (any, error) {
	if p == nil {
		return nil, nil
	}
	b, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	return string(b), nil
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
	return fmt.Errorf("postgres %s: %w", op, err)
}

func scanErr(op string, err error) error {
	if errors.Is(err, sql.ErrNoRows) {
		return store.ErrNotFound
	}
	return wrap(op, err)
}
