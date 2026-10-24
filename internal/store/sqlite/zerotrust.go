package sqlite

import (
	"context"

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
  disabled    INTEGER NOT NULL DEFAULT 0,
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL,
  UNIQUE (org_id, name)
);
`

const serviceAccountCols = `id, org_id, name, role, client_id, secret_hash, disabled, created_at, updated_at`

func (s *Store) CreateServiceAccount(ctx context.Context, sa *store.ServiceAccount) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO service_accounts (`+serviceAccountCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		sa.ID, sa.OrgID, sa.Name, sa.Role, sa.ClientID, sa.SecretHash, boolInt(sa.Disabled), ns(sa.CreatedAt), ns(sa.UpdatedAt))
	return uniqueErr("create service account", err)
}

func (s *Store) GetServiceAccount(ctx context.Context, orgID, id string) (*store.ServiceAccount, error) {
	return scanServiceAccount(s.db.QueryRowContext(ctx, `SELECT `+serviceAccountCols+` FROM service_accounts WHERE org_id = ? AND id = ?`, orgID, id).Scan)
}

func (s *Store) GetServiceAccountByClientID(ctx context.Context, clientID string) (*store.ServiceAccount, error) {
	return scanServiceAccount(s.db.QueryRowContext(ctx, `SELECT `+serviceAccountCols+` FROM service_accounts WHERE client_id = ?`, clientID).Scan)
}

func (s *Store) ListServiceAccounts(ctx context.Context, orgID string) ([]*store.ServiceAccount, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+serviceAccountCols+` FROM service_accounts WHERE org_id = ? ORDER BY name, id`, orgID)
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
	res, err := s.db.ExecContext(ctx,
		`UPDATE service_accounts SET name = ?, role = ?, secret_hash = ?, disabled = ?, updated_at = ? WHERE org_id = ? AND id = ?`,
		sa.Name, sa.Role, sa.SecretHash, boolInt(sa.Disabled), ns(sa.UpdatedAt), sa.OrgID, sa.ID)
	if err != nil {
		return uniqueErr("update service account", err)
	}
	return affected("update service account", res)
}

func (s *Store) DeleteServiceAccount(ctx context.Context, orgID, id string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return wrap("delete service account", err)
	}
	defer tx.Rollback()
	res, err := tx.ExecContext(ctx, `DELETE FROM service_accounts WHERE org_id = ? AND id = ?`, orgID, id)
	if err != nil {
		return wrap("delete service account", err)
	}
	if err := affected("delete service account", res); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM group_members WHERE org_id = ? AND member_type = ? AND member_id = ?`,
		orgID, store.GroupMemberServiceAccount, id); err != nil {
		return wrap("delete service account memberships", err)
	}
	return wrap("delete service account", tx.Commit())
}

func scanServiceAccount(scan func(...any) error) (*store.ServiceAccount, error) {
	var sa store.ServiceAccount
	var disabled int
	var created, updated int64
	if err := scan(&sa.ID, &sa.OrgID, &sa.Name, &sa.Role, &sa.ClientID, &sa.SecretHash, &disabled, &created, &updated); err != nil {
		return nil, scanErr("scan service account", err)
	}
	sa.Disabled = disabled != 0
	sa.CreatedAt = fromNS(created)
	sa.UpdatedAt = fromNS(updated)
	return &sa, nil
}
