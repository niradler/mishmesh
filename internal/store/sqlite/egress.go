package sqlite

import (
	"context"
	"encoding/json"

	"github.com/mishmesh/mishmesh/internal/store"
)

const egressSchema = `
CREATE TABLE IF NOT EXISTS egress_credentials (
  org_id     TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  name       TEXT NOT NULL,
  header     TEXT NOT NULL,
  prefix     TEXT NOT NULL,
  secret     TEXT NOT NULL,
  agent_ids  TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  PRIMARY KEY (org_id, name)
);
CREATE TABLE IF NOT EXISTS org_cas (
  org_id     TEXT PRIMARY KEY REFERENCES orgs(id) ON DELETE CASCADE,
  cert_der   BLOB NOT NULL,
  key_enc    BLOB NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS org_decrypt (
  org_id     TEXT PRIMARY KEY REFERENCES orgs(id) ON DELETE CASCADE,
  allow      TEXT NOT NULL,
  bypass     TEXT NOT NULL,
  updated_at INTEGER NOT NULL
);
`

const egressCredentialCols = `org_id, name, header, prefix, secret, agent_ids, created_at, updated_at`

func (s *Store) PutEgressCredential(ctx context.Context, c *store.EgressCredential) error {
	agents, err := json.Marshal(nonNil(c.AgentIDs))
	if err != nil {
		return wrap("put egress credential", err)
	}
	row := s.db.QueryRowContext(ctx,
		`INSERT INTO egress_credentials (`+egressCredentialCols+`) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(org_id, name) DO UPDATE SET header = excluded.header, prefix = excluded.prefix, secret = excluded.secret,
		   agent_ids = excluded.agent_ids, updated_at = excluded.updated_at
		 RETURNING created_at`,
		c.OrgID, c.Name, c.Header, c.Prefix, c.Secret, string(agents), ns(c.UpdatedAt), ns(c.UpdatedAt))
	var created int64
	if err := row.Scan(&created); err != nil {
		return wrap("put egress credential", err)
	}
	c.CreatedAt = fromNS(created)
	return nil
}

func (s *Store) GetEgressCredential(ctx context.Context, orgID, name string) (*store.EgressCredential, error) {
	return scanEgressCredential(s.db.QueryRowContext(ctx, `SELECT `+egressCredentialCols+` FROM egress_credentials WHERE org_id = ? AND name = ?`, orgID, name).Scan)
}

func (s *Store) ListEgressCredentials(ctx context.Context, orgID string) ([]*store.EgressCredential, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+egressCredentialCols+` FROM egress_credentials WHERE org_id = ? ORDER BY name`, orgID)
	if err != nil {
		return nil, wrap("list egress credentials", err)
	}
	defer rows.Close()
	var out []*store.EgressCredential
	for rows.Next() {
		c, err := scanEgressCredential(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, wrap("list egress credentials", rows.Err())
}

func (s *Store) DeleteEgressCredential(ctx context.Context, orgID, name string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM egress_credentials WHERE org_id = ? AND name = ?`, orgID, name)
	if err != nil {
		return wrap("delete egress credential", err)
	}
	return affected("delete egress credential", res)
}

func scanEgressCredential(scan func(...any) error) (*store.EgressCredential, error) {
	var c store.EgressCredential
	var agents string
	var created, updated int64
	if err := scan(&c.OrgID, &c.Name, &c.Header, &c.Prefix, &c.Secret, &agents, &created, &updated); err != nil {
		return nil, scanErr("scan egress credential", err)
	}
	if err := json.Unmarshal([]byte(agents), &c.AgentIDs); err != nil {
		return nil, wrap("decode egress credential agents", err)
	}
	c.CreatedAt = fromNS(created)
	c.UpdatedAt = fromNS(updated)
	return &c, nil
}

func (s *Store) EnsureOrgCA(ctx context.Context, ca *store.OrgCA) (*store.OrgCA, error) {
	if _, err := s.db.ExecContext(ctx,
		`INSERT INTO org_cas (org_id, cert_der, key_enc, created_at) VALUES (?, ?, ?, ?) ON CONFLICT(org_id) DO NOTHING`,
		ca.OrgID, ca.CertDER, ca.KeyEnc, ns(ca.CreatedAt)); err != nil {
		return nil, wrap("ensure org ca", err)
	}
	return s.GetOrgCA(ctx, ca.OrgID)
}

func (s *Store) GetOrgCA(ctx context.Context, orgID string) (*store.OrgCA, error) {
	var ca store.OrgCA
	var created int64
	err := s.db.QueryRowContext(ctx, `SELECT org_id, cert_der, key_enc, created_at FROM org_cas WHERE org_id = ?`, orgID).
		Scan(&ca.OrgID, &ca.CertDER, &ca.KeyEnc, &created)
	if err != nil {
		return nil, scanErr("get org ca", err)
	}
	ca.CreatedAt = fromNS(created)
	return &ca, nil
}

func (s *Store) GetOrgDecrypt(ctx context.Context, orgID string) (*store.OrgDecrypt, error) {
	var d store.OrgDecrypt
	var allow, bypass string
	var updated int64
	err := s.db.QueryRowContext(ctx, `SELECT org_id, allow, bypass, updated_at FROM org_decrypt WHERE org_id = ?`, orgID).
		Scan(&d.OrgID, &allow, &bypass, &updated)
	if err != nil {
		return nil, scanErr("get org decrypt", err)
	}
	if err := json.Unmarshal([]byte(allow), &d.Allow); err != nil {
		return nil, wrap("decode org decrypt allow", err)
	}
	if err := json.Unmarshal([]byte(bypass), &d.Bypass); err != nil {
		return nil, wrap("decode org decrypt bypass", err)
	}
	d.UpdatedAt = fromNS(updated)
	return &d, nil
}

func (s *Store) SetOrgDecrypt(ctx context.Context, d *store.OrgDecrypt) error {
	allow, err := json.Marshal(nonNil(d.Allow))
	if err != nil {
		return wrap("set org decrypt", err)
	}
	bypass, err := json.Marshal(nonNil(d.Bypass))
	if err != nil {
		return wrap("set org decrypt", err)
	}
	_, err = s.db.ExecContext(ctx,
		`INSERT INTO org_decrypt (org_id, allow, bypass, updated_at) VALUES (?, ?, ?, ?)
		 ON CONFLICT(org_id) DO UPDATE SET allow = excluded.allow, bypass = excluded.bypass, updated_at = excluded.updated_at`,
		d.OrgID, string(allow), string(bypass), ns(d.UpdatedAt))
	return wrap("set org decrypt", err)
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
