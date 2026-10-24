package sqlite

import (
	"context"

	"github.com/mishmesh/mishmesh/internal/store"
)

const workloadIdentitiesSchema = `
CREATE TABLE IF NOT EXISTS workload_identities (
  id                 TEXT PRIMARY KEY,
  org_id             TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  service_account_id TEXT NOT NULL REFERENCES service_accounts(id) ON DELETE CASCADE,
  issuer             TEXT NOT NULL,
  audience           TEXT NOT NULL,
  subject            TEXT NOT NULL,
  created_at         INTEGER NOT NULL,
  UNIQUE (service_account_id, issuer, subject)
);
CREATE INDEX IF NOT EXISTS workload_identities_issuer ON workload_identities (issuer);
`

const workloadIdentityCols = `id, org_id, service_account_id, issuer, audience, subject, created_at`

func (s *Store) CreateWorkloadIdentity(ctx context.Context, w *store.WorkloadIdentity) error {
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO workload_identities (`+workloadIdentityCols+`)
		 SELECT ?, ?, ?, ?, ?, ?, ? WHERE EXISTS (SELECT 1 FROM service_accounts WHERE org_id = ? AND id = ?)`,
		w.ID, w.OrgID, w.ServiceAccountID, w.Issuer, w.Audience, w.Subject, ns(w.CreatedAt), w.OrgID, w.ServiceAccountID)
	if err != nil {
		return uniqueErr("create workload identity", err)
	}
	return affected("create workload identity", res)
}

func (s *Store) ListWorkloadIdentities(ctx context.Context, orgID, serviceAccountID string) ([]*store.WorkloadIdentity, error) {
	return s.queryWorkloadIdentities(ctx, `SELECT `+workloadIdentityCols+` FROM workload_identities WHERE org_id = ? AND service_account_id = ? ORDER BY created_at, id`, orgID, serviceAccountID)
}

func (s *Store) ListWorkloadIdentitiesByIssuer(ctx context.Context, issuer string) ([]*store.WorkloadIdentity, error) {
	return s.queryWorkloadIdentities(ctx, `SELECT `+workloadIdentityCols+` FROM workload_identities WHERE issuer = ? ORDER BY id`, issuer)
}

func (s *Store) queryWorkloadIdentities(ctx context.Context, query string, args ...any) ([]*store.WorkloadIdentity, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, wrap("list workload identities", err)
	}
	defer rows.Close()
	var out []*store.WorkloadIdentity
	for rows.Next() {
		w, err := scanWorkloadIdentity(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, w)
	}
	return out, wrap("list workload identities", rows.Err())
}

func (s *Store) DeleteWorkloadIdentity(ctx context.Context, orgID, serviceAccountID, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM workload_identities WHERE org_id = ? AND service_account_id = ? AND id = ?`, orgID, serviceAccountID, id)
	if err != nil {
		return wrap("delete workload identity", err)
	}
	return affected("delete workload identity", res)
}

func scanWorkloadIdentity(scan func(...any) error) (*store.WorkloadIdentity, error) {
	var w store.WorkloadIdentity
	var created int64
	if err := scan(&w.ID, &w.OrgID, &w.ServiceAccountID, &w.Issuer, &w.Audience, &w.Subject, &created); err != nil {
		return nil, scanErr("scan workload identity", err)
	}
	w.CreatedAt = fromNS(created)
	return &w, nil
}
