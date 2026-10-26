package sqlite

import (
	"context"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/sqlshared"
)

func (s *Store) WithServiceSnapshots(ctx context.Context, orgID string, expected []*store.Service, action func(store.ServiceMutation) error) error {
	return dialect.WithServiceSnapshots(ctx, s.beginNetworkWrite, orgID, expected, action)
}

func (s *Store) ValidateServiceSnapshots(ctx context.Context, orgID string, expected []*store.Service) error {
	return s.WithServiceSnapshots(ctx, orgID, expected, func(store.ServiceMutation) error { return nil })
}

func (s *Store) UpdateServiceIfCurrent(ctx context.Context, service *store.Service, expected []*store.Service) error {
	if service == nil || !sqlshared.IncludesServiceSnapshot(expected, service.OrgID, service.ID) {
		return store.ErrConflict
	}
	return s.WithServiceSnapshots(ctx, service.OrgID, expected, func(mutation store.ServiceMutation) error { return mutation.UpdateService(ctx, service) })
}

func (s *Store) DeleteServiceIfCurrent(ctx context.Context, orgID, id string, expected []*store.Service) error {
	if !sqlshared.IncludesServiceSnapshot(expected, orgID, id) {
		return store.ErrConflict
	}
	return s.WithServiceSnapshots(ctx, orgID, expected, func(mutation store.ServiceMutation) error { return mutation.DeleteService(ctx, id) })
}
