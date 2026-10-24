package postgres

import (
	"context"

	"github.com/mishmesh/mishmesh/internal/store"
)

const orgSecretsSchema = `
CREATE TABLE IF NOT EXISTS org_secrets (
  org_id     TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  name       TEXT NOT NULL,
  value_enc  BYTEA NOT NULL,
  version    INTEGER NOT NULL,
  created_at BIGINT NOT NULL,
  updated_at BIGINT NOT NULL,
  PRIMARY KEY (org_id, name)
);
`

const orgSecretCols = `org_id, name, value_enc, version, created_at, updated_at`

func (s *Store) PutOrgSecret(ctx context.Context, sec *store.OrgSecret) error {
	ctx, cancel := context.WithTimeout(ctx, s.queryTimeout)
	defer cancel()
	row := s.db.QueryRowContext(ctx,
		`INSERT INTO org_secrets (`+orgSecretCols+`) VALUES ($1, $2, $3, 1, $4, $4)
		 ON CONFLICT (org_id, name) DO UPDATE SET value_enc = excluded.value_enc, version = org_secrets.version + 1, updated_at = excluded.updated_at
		 RETURNING version, created_at`,
		sec.OrgID, sec.Name, sec.ValueEnc, ns(sec.UpdatedAt))
	var created int64
	if err := row.Scan(&sec.Version, &created); err != nil {
		return wrap("put org secret", err)
	}
	sec.CreatedAt = fromNS(created)
	return nil
}

func (s *Store) GetOrgSecret(ctx context.Context, orgID, name string) (*store.OrgSecret, error) {
	ctx, cancel := context.WithTimeout(ctx, s.queryTimeout)
	defer cancel()
	return scanOrgSecret(s.db.QueryRowContext(ctx, `SELECT `+orgSecretCols+` FROM org_secrets WHERE org_id = $1 AND name = $2`, orgID, name).Scan)
}

func (s *Store) ListOrgSecrets(ctx context.Context, orgID string) ([]*store.OrgSecret, error) {
	ctx, cancel := context.WithTimeout(ctx, s.queryTimeout)
	defer cancel()
	rows, err := s.db.QueryContext(ctx, `SELECT `+orgSecretCols+` FROM org_secrets WHERE org_id = $1 ORDER BY name`, orgID)
	if err != nil {
		return nil, wrap("list org secrets", err)
	}
	defer rows.Close()
	var out []*store.OrgSecret
	for rows.Next() {
		sec, err := scanOrgSecret(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, sec)
	}
	return out, wrap("list org secrets", rows.Err())
}

func (s *Store) DeleteOrgSecret(ctx context.Context, orgID, name string) error {
	ctx, cancel := context.WithTimeout(ctx, s.queryTimeout)
	defer cancel()
	res, err := s.db.ExecContext(ctx, `DELETE FROM org_secrets WHERE org_id = $1 AND name = $2`, orgID, name)
	if err != nil {
		return wrap("delete org secret", err)
	}
	return affected("delete org secret", res)
}

func scanOrgSecret(scan func(...any) error) (*store.OrgSecret, error) {
	var sec store.OrgSecret
	var created, updated int64
	if err := scan(&sec.OrgID, &sec.Name, &sec.ValueEnc, &sec.Version, &created, &updated); err != nil {
		return nil, scanErr("scan org secret", err)
	}
	sec.CreatedAt = fromNS(created)
	sec.UpdatedAt = fromNS(updated)
	return &sec, nil
}
