package postgres

import (
	"context"
	"time"

	"github.com/mishmesh/mishmesh/internal/store"
)

const zeroTrustSchema = `
CREATE TABLE IF NOT EXISTS service_accounts (
  id          TEXT PRIMARY KEY,
  org_id      TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  name        TEXT NOT NULL,
  role        TEXT NOT NULL,
  client_id   TEXT NOT NULL UNIQUE,
  secret_hash TEXT NOT NULL,
  disabled    BOOLEAN NOT NULL DEFAULT FALSE,
  created_at  BIGINT NOT NULL,
  updated_at  BIGINT NOT NULL,
  UNIQUE (org_id, name)
);
`

const serviceAccountCols = `id, org_id, name, role, client_id, secret_hash, disabled, created_at, updated_at`

func (s *Store) CreateServiceAccount(ctx context.Context, sa *store.ServiceAccount) error {
	ctx, cancel := context.WithTimeout(ctx, s.queryTimeout)
	defer cancel()
	_, err := s.db.ExecContext(ctx, `INSERT INTO service_accounts (`+serviceAccountCols+`) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)`,
		sa.ID, sa.OrgID, sa.Name, sa.Role, sa.ClientID, sa.SecretHash, sa.Disabled, ns(sa.CreatedAt), ns(sa.UpdatedAt))
	return uniqueErr("create service account", err)
}

func (s *Store) GetServiceAccount(ctx context.Context, orgID, id string) (*store.ServiceAccount, error) {
	ctx, cancel := context.WithTimeout(ctx, s.queryTimeout)
	defer cancel()
	return scanServiceAccount(s.db.QueryRowContext(ctx, `SELECT `+serviceAccountCols+` FROM service_accounts WHERE org_id = $1 AND id = $2`, orgID, id).Scan)
}

func (s *Store) GetServiceAccountByClientID(ctx context.Context, clientID string) (*store.ServiceAccount, error) {
	ctx, cancel := context.WithTimeout(ctx, s.queryTimeout)
	defer cancel()
	return scanServiceAccount(s.db.QueryRowContext(ctx, `SELECT `+serviceAccountCols+` FROM service_accounts WHERE client_id = $1`, clientID).Scan)
}

func (s *Store) ListServiceAccounts(ctx context.Context, orgID string) ([]*store.ServiceAccount, error) {
	ctx, cancel := context.WithTimeout(ctx, s.queryTimeout)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT `+serviceAccountCols+` FROM service_accounts WHERE org_id = $1 ORDER BY name, id`, orgID)
	if err != nil {
		return nil, wrap("list service accounts", err)
	}
	defer rows.Close()
	var out []*store.ServiceAccount
	for rows.Next() {
		sa, err := scanServiceAccount(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, sa)
	}
	return out, wrap("list service accounts", rows.Err())
}

func (s *Store) UpdateServiceAccount(ctx context.Context, sa *store.ServiceAccount) error {
	ctx, cancel := context.WithTimeout(ctx, s.queryTimeout)
	defer cancel()
	res, err := s.db.ExecContext(ctx,
		`UPDATE service_accounts SET name = $1, role = $2, secret_hash = $3, disabled = $4, updated_at = $5 WHERE org_id = $6 AND id = $7`,
		sa.Name, sa.Role, sa.SecretHash, sa.Disabled, ns(sa.UpdatedAt), sa.OrgID, sa.ID)
	if err != nil {
		return uniqueErr("update service account", err)
	}
	return affected("update service account", res)
}

func (s *Store) DeleteServiceAccount(ctx context.Context, orgID, id string) error {
	ctx, cancel := context.WithTimeout(ctx, s.queryTimeout)
	defer cancel()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return wrap("delete service account", err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM service_accounts WHERE org_id = $1 AND id = $2`, orgID, id)
	if err != nil {
		return wrap("delete service account", err)
	}
	if err := affected("delete service account", res); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM group_members WHERE org_id = $1 AND member_type = $2 AND member_id = $3`,
		orgID, store.GroupMemberServiceAccount, id); err != nil {
		return wrap("delete service account memberships", err)
	}
	return wrap("delete service account", tx.Commit())
}

func scanServiceAccount(scan func(...any) error) (*store.ServiceAccount, error) {
	var sa store.ServiceAccount
	var created, updated int64
	if err := scan(&sa.ID, &sa.OrgID, &sa.Name, &sa.Role, &sa.ClientID, &sa.SecretHash, &sa.Disabled, &created, &updated); err != nil {
		return nil, scanErr("scan service account", err)
	}
	sa.CreatedAt = fromNS(created)
	sa.UpdatedAt = fromNS(updated)
	return &sa, nil
}

const zeroTrustColumns = `
ALTER TABLE memberships ADD COLUMN IF NOT EXISTS disabled_at BIGINT;
ALTER TABLE sessions ADD COLUMN IF NOT EXISTS auth_method TEXT NOT NULL DEFAULT '';
`

func (s *Store) SetMembershipDisabled(ctx context.Context, orgID, userID string, at *time.Time) error {
	ctx, cancel := context.WithTimeout(ctx, s.queryTimeout)
	defer cancel()
	res, err := s.db.ExecContext(ctx, `UPDATE memberships SET disabled_at = $1 WHERE org_id = $2 AND user_id = $3`, nsPtr(at), orgID, userID)
	if err != nil {
		return wrap("set membership disabled", err)
	}
	return affected("set membership disabled", res)
}

func (s *Store) DeleteUserSessions(ctx context.Context, orgID, userID string) error {
	ctx, cancel := context.WithTimeout(ctx, s.queryTimeout)
	defer cancel()
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE org_id = $1 AND user_id = $2`, orgID, userID)
	return wrap("delete user sessions", err)
}
