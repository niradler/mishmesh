package sqlshared

import (
	"context"
	"database/sql"

	"github.com/mishmesh/mishmesh/internal/store"
)

type ServiceMutation struct {
	d     Dialect
	tx    *sql.Tx
	orgID string
}

func (m ServiceMutation) ListEndpointsByOrg(ctx context.Context, orgID string) ([]*store.Endpoint, error) {
	rows, err := m.tx.QueryContext(ctx, m.d.rebind(`SELECT `+m.d.EndpointCols+` FROM endpoints WHERE org_id = ? ORDER BY created_at`), orgID)
	if err != nil {
		return nil, m.d.Wrap("list service mutation endpoints", err)
	}
	defer rows.Close()
	result := []*store.Endpoint{}
	for rows.Next() {
		endpoint, err := m.d.ScanEndpoint(rows.Scan)
		if err != nil {
			return nil, err
		}
		result = append(result, endpoint)
	}
	return result, m.d.Wrap("list service mutation endpoints", rows.Err())
}

func (d Dialect) WithServiceSnapshots(ctx context.Context, begin func(context.Context) (*sql.Tx, error), orgID string, expected []*store.Service, action func(store.ServiceMutation) error) error {
	if orgID == "" || action == nil {
		return store.ErrConflict
	}
	tx, err := begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	seen := make(map[string]bool)
	for _, snapshot := range expected {
		if snapshot == nil || snapshot.OrgID != orgID || snapshot.ID == "" || seen[snapshot.ID] {
			return store.ErrConflict
		}
		seen[snapshot.ID] = true
		current, err := d.ScanService(tx.QueryRowContext(ctx, d.rebind(`SELECT `+d.ServiceCols+` FROM services WHERE id = ?`+d.LockSuffix), snapshot.ID))
		if err != nil {
			return err
		}
		if !store.SameServiceInput(*current, *snapshot) {
			return store.ErrConflict
		}
	}
	if err := action(ServiceMutation{d: d, tx: tx, orgID: orgID}); err != nil {
		return err
	}
	return d.Wrap("commit service mutation", tx.Commit())
}

func IncludesServiceSnapshot(expected []*store.Service, orgID, id string) bool {
	for _, service := range expected {
		if service != nil && service.OrgID == orgID && service.ID == id {
			return true
		}
	}
	return false
}

func (m ServiceMutation) CreateService(ctx context.Context, service *store.Service) error {
	if service == nil || service.OrgID != m.orgID {
		return store.ErrConflict
	}
	return m.d.CreateServiceTx(ctx, m.tx, service)
}

func (m ServiceMutation) UpdateService(ctx context.Context, service *store.Service) error {
	if service == nil || service.OrgID != m.orgID {
		return store.ErrConflict
	}
	return m.d.UpdateServiceTx(ctx, m.tx, service)
}

func (m ServiceMutation) DeleteService(ctx context.Context, id string) error {
	current, err := m.d.ScanService(m.tx.QueryRowContext(ctx, m.d.rebind(`SELECT `+m.d.ServiceCols+` FROM services WHERE id = ?`), id))
	if err != nil {
		return err
	}
	if current.OrgID != m.orgID {
		return store.ErrConflict
	}
	return m.d.DeleteServiceTx(ctx, m.tx, id)
}

func (m ServiceMutation) ListAllServices(ctx context.Context) ([]*store.Service, error) {
	rows, err := m.tx.QueryContext(ctx, m.d.rebind(`SELECT `+m.d.ServiceCols+` FROM services ORDER BY org_id, name`))
	if err != nil {
		return nil, m.d.Wrap("list service mutation definitions", err)
	}
	defer rows.Close()
	result := []*store.Service{}
	for rows.Next() {
		service, err := m.d.ScanService(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, service)
	}
	return result, m.d.Wrap("list service mutation definitions", rows.Err())
}

func (m ServiceMutation) CheckDeleteService(ctx context.Context, id string) error {
	current, err := m.d.ScanService(m.tx.QueryRowContext(ctx, m.d.rebind(`SELECT `+m.d.ServiceCols+` FROM services WHERE id = ?`), id))
	if err != nil {
		return err
	}
	if current.OrgID != m.orgID {
		return store.ErrConflict
	}
	return m.d.CheckServiceDeleteTx(ctx, m.tx, id)
}
