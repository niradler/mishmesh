package postgres

import (
	"context"
	"database/sql"

	"github.com/mishmesh/mishmesh/internal/store"
	"github.com/mishmesh/mishmesh/internal/store/sqlshared"
)

var dialect = sqlshared.Dialect{
	Placeholder:          sqlshared.Numbered,
	LockSuffix:           " FOR UPDATE",
	Wrap:                 wrap,
	ScanErr:              scanErr,
	UniqueErr:            uniqueErr,
	ConnectionCols:       connectionCols,
	EndpointCols:         endpointCols,
	ServiceCols:          serviceCols,
	ScanConnection:       scanConnection,
	ScanEndpoint:         scanEndpoint,
	ScanService:          scanService,
	CreateServiceTx:      createServiceTx,
	UpdateServiceTx:      updateServiceTx,
	DeleteServiceTx:      deleteServiceTx,
	CheckServiceDeleteTx: checkServiceDeleteTx,
}

func (s *Store) GetEndpointByAccessHostname(ctx context.Context, hostname string) (*store.Endpoint, error) {
	ctx, cancel := context.WithTimeout(ctx, s.queryTimeout)
	defer cancel()
	return dialect.GetEndpointByAccessHostname(ctx, s.db, hostname)
}

func (s *Store) ReplaceConnectionAliases(ctx context.Context, orgID, connectionID string, aliases []store.EndpointAlias) error {
	ctx, cancel := context.WithTimeout(ctx, s.queryTimeout)
	defer cancel()
	return dialect.ReplaceConnectionAliases(ctx, s.beginNetworkWrite, orgID, connectionID, aliases, nil)
}

func (s *Store) ReplaceConnectionAliasesIfCurrent(ctx context.Context, expected *store.Connection, aliases []store.EndpointAlias) error {
	if expected == nil {
		return store.ErrConflict
	}
	ctx, cancel := context.WithTimeout(ctx, s.queryTimeout)
	defer cancel()
	return dialect.ReplaceConnectionAliases(ctx, s.beginNetworkWrite, expected.OrgID, expected.ID, aliases, expected)
}

func validateEndpointReferences(ctx context.Context, tx *sql.Tx, endpoint *store.Endpoint, update bool) error {
	return dialect.ValidateEndpointReferences(ctx, tx, endpoint, update)
}

func clearChangedEndpointAliases(ctx context.Context, tx *sql.Tx, endpoint *store.Endpoint) error {
	return dialect.ClearChangedEndpointAliases(ctx, tx, endpoint)
}
