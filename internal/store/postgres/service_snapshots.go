package postgres

import (
	"context"
	"database/sql"

	"github.com/mishmesh/mishmesh/internal/store"
)

type serviceMutation struct {
	tx    *sql.Tx
	orgID string
}

func (m serviceMutation) ListEndpointsByOrg(ctx context.Context, orgID string) ([]*store.Endpoint, error) {
	rows, err := m.tx.QueryContext(ctx, `SELECT `+endpointCols+` FROM endpoints WHERE org_id = $1 ORDER BY created_at`, orgID)
	if err != nil {
		return nil, wrap("list service mutation endpoints", err)
	}
	defer rows.Close()
	result := []*store.Endpoint{}
	for rows.Next() {
		endpoint, err := scanEndpoint(rows.Scan)
		if err != nil {
			return nil, err
		}
		result = append(result, endpoint)
	}
	return result, wrap("list service mutation endpoints", rows.Err())
}

func (s *Store) WithServiceSnapshots(ctx context.Context, orgID string, expected []*store.Service, action func(store.ServiceMutation) error) error {
	if orgID == "" || action == nil {
		return store.ErrConflict
	}
	ctx, cancel := context.WithTimeout(ctx, s.queryTimeout)
	defer cancel()
	tx, err := s.beginNetworkWrite(ctx)
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
		current, err := scanService(tx.QueryRowContext(ctx, `SELECT `+serviceCols+` FROM services WHERE id = $1 FOR UPDATE`, snapshot.ID))
		if err != nil {
			return err
		}
		if !store.SameServiceInput(*current, *snapshot) {
			return store.ErrConflict
		}
	}
	if err := action(serviceMutation{tx: tx, orgID: orgID}); err != nil {
		return err
	}
	return wrap("commit service mutation", tx.Commit())
}

func (s *Store) ValidateServiceSnapshots(ctx context.Context, orgID string, expected []*store.Service) error {
	return s.WithServiceSnapshots(ctx, orgID, expected, func(store.ServiceMutation) error { return nil })
}

func (s *Store) UpdateServiceIfCurrent(ctx context.Context, service *store.Service, expected []*store.Service) error {
	if service == nil || !includesServiceSnapshot(expected, service.OrgID, service.ID) {
		return store.ErrConflict
	}
	return s.WithServiceSnapshots(ctx, service.OrgID, expected, func(mutation store.ServiceMutation) error { return mutation.UpdateService(ctx, service) })
}

func (s *Store) DeleteServiceIfCurrent(ctx context.Context, orgID, id string, expected []*store.Service) error {
	if !includesServiceSnapshot(expected, orgID, id) {
		return store.ErrConflict
	}
	return s.WithServiceSnapshots(ctx, orgID, expected, func(mutation store.ServiceMutation) error { return mutation.DeleteService(ctx, id) })
}

func includesServiceSnapshot(expected []*store.Service, orgID, id string) bool {
	for _, service := range expected {
		if service != nil && service.OrgID == orgID && service.ID == id {
			return true
		}
	}
	return false
}

func (m serviceMutation) CreateService(ctx context.Context, service *store.Service) error {
	if service == nil || service.OrgID != m.orgID {
		return store.ErrConflict
	}
	return createServiceTx(ctx, m.tx, service)
}

func (m serviceMutation) UpdateService(ctx context.Context, service *store.Service) error {
	if service == nil || service.OrgID != m.orgID {
		return store.ErrConflict
	}
	return updateServiceTx(ctx, m.tx, service)
}

func (m serviceMutation) DeleteService(ctx context.Context, id string) error {
	current, err := scanService(m.tx.QueryRowContext(ctx, `SELECT `+serviceCols+` FROM services WHERE id = $1`, id))
	if err != nil {
		return err
	}
	if current.OrgID != m.orgID {
		return store.ErrConflict
	}
	return deleteServiceTx(ctx, m.tx, id)
}

func (m serviceMutation) ListAllServices(ctx context.Context) ([]*store.Service, error) {
	rows, err := m.tx.QueryContext(ctx, `SELECT `+serviceCols+` FROM services ORDER BY org_id, name`)
	if err != nil {
		return nil, wrap("list service mutation definitions", err)
	}
	defer rows.Close()
	result := []*store.Service{}
	for rows.Next() {
		service, err := scanService(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, service)
	}
	return result, wrap("list service mutation definitions", rows.Err())
}

func (m serviceMutation) CheckDeleteService(ctx context.Context, id string) error {
	current, err := scanService(m.tx.QueryRowContext(ctx, `SELECT `+serviceCols+` FROM services WHERE id = $1`, id))
	if err != nil {
		return err
	}
	if current.OrgID != m.orgID {
		return store.ErrConflict
	}
	return checkServiceDeleteTx(ctx, m.tx, id)
}
