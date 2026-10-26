package sqlite

import (
	"context"
	"database/sql"
	"fmt"
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

var zeroTrustColumns = []struct{ table, column, decl string }{
	{"memberships", "disabled_at", "INTEGER"},
	{"sessions", "auth_method", "TEXT NOT NULL DEFAULT ''"},
	{"access_log", "exit_agent_id", "TEXT NOT NULL DEFAULT ''"},
	{"egress_credentials", "destinations", "TEXT NOT NULL DEFAULT '[]'"},
}

func migrateZeroTrustColumns(ctx context.Context, tx *sql.Tx) error {
	for _, c := range zeroTrustColumns {
		var n int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_table_info(?) WHERE name = ?`, c.table, c.column).Scan(&n); err != nil {
			return err
		}
		if n > 0 {
			continue
		}
		if _, err := tx.ExecContext(ctx, `ALTER TABLE `+c.table+` ADD COLUMN `+c.column+` `+c.decl); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) SetMembershipDisabled(ctx context.Context, orgID, userID string, at *time.Time) error {
	res, err := s.db.ExecContext(ctx, `UPDATE memberships SET disabled_at = ? WHERE org_id = ? AND user_id = ?`, nsPtr(at), orgID, userID)
	if err != nil {
		return wrap("set membership disabled", err)
	}
	return affected("set membership disabled", res)
}

func (s *Store) SetMembershipDisabledKeepingOwner(ctx context.Context, orgID, userID string, at *time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return wrap("set membership disabled", err)
	}
	defer tx.Rollback()
	target, err := scanMembership(tx.QueryRowContext(ctx,
		`SELECT org_id, user_id, role, created_at, disabled_at FROM memberships WHERE org_id = ? AND user_id = ?`, orgID, userID).Scan)
	if err != nil {
		return err
	}
	if at != nil && target.Role == store.RoleOwner && target.DisabledAt == nil {
		var others int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM memberships WHERE org_id = ? AND role = ? AND disabled_at IS NULL AND user_id <> ?`,
			orgID, store.RoleOwner, userID).Scan(&others); err != nil {
			return wrap("count owners", err)
		}
		if others == 0 {
			return fmt.Errorf("organization must keep an active owner: %w", store.ErrConflict)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE memberships SET disabled_at = ? WHERE org_id = ? AND user_id = ?`, nsPtr(at), orgID, userID); err != nil {
		return wrap("update membership", err)
	}
	return wrap("set membership disabled", tx.Commit())
}

func (s *Store) DeleteUserSessions(ctx context.Context, orgID, userID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE org_id = ? AND user_id = ?`, orgID, userID)
	return wrap("delete user sessions", err)
}
