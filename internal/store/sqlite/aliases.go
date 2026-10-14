package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/mishmesh/mishmesh/internal/store"
)

func normalizeAccessHostname(hostname string) (string, error) {
	hostname = strings.TrimSuffix(strings.ToLower(strings.TrimSpace(hostname)), ".")
	if hostname == "" || len(hostname) > 253 {
		return "", fmt.Errorf("invalid access hostname")
	}
	for _, label := range strings.Split(hostname, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", fmt.Errorf("invalid access hostname")
		}
		for _, char := range label {
			if char != '-' && (char < 'a' || char > 'z') && (char < '0' || char > '9') {
				return "", fmt.Errorf("invalid access hostname")
			}
		}
	}
	return hostname, nil
}

func (s *Store) GetEndpointByAccessHostname(ctx context.Context, hostname string) (*store.Endpoint, error) {
	hostname, err := normalizeAccessHostname(hostname)
	if err != nil {
		return nil, err
	}
	return scanEndpoint(s.db.QueryRowContext(ctx, `SELECT `+endpointCols+` FROM endpoints WHERE id = (SELECT endpoint_id FROM endpoint_aliases WHERE hostname = ? AND org_id = endpoints.org_id)`, hostname).Scan)
}

func (s *Store) ReplaceConnectionAliases(ctx context.Context, orgID, connectionID string, aliases []store.EndpointAlias) error {
	tx, err := s.beginNetworkWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var owner string
	if err := tx.QueryRowContext(ctx, `SELECT org_id FROM connections WHERE id = ?`, connectionID).Scan(&owner); err != nil {
		return scanErr("get alias connection", err)
	}
	if owner != orgID {
		return store.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM endpoint_aliases WHERE org_id = ? AND connection_id = ?`, orgID, connectionID); err != nil {
		return wrap("replace aliases", err)
	}
	for _, alias := range aliases {
		hostname, err := normalizeAccessHostname(alias.Hostname)
		if err != nil {
			return err
		}
		if err := tx.QueryRowContext(ctx, `SELECT org_id FROM endpoints WHERE id = ?`, alias.EndpointID).Scan(&owner); err != nil {
			return scanErr("get alias endpoint", err)
		}
		if owner != orgID {
			return store.ErrNotFound
		}
		var collisions int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM endpoints WHERE lower(rtrim(domain, '.')) = ? AND id <> ?`, hostname, alias.EndpointID).Scan(&collisions); err != nil {
			return wrap("check alias domain", err)
		}
		if collisions > 0 {
			return store.ErrConflict
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO endpoint_aliases (hostname, org_id, endpoint_id, connection_id) VALUES (?, ?, ?, ?)`, hostname, orgID, alias.EndpointID, connectionID); err != nil {
			return uniqueErr("replace aliases", err)
		}
	}
	return wrap("replace aliases", tx.Commit())
}

func validateEndpointReferences(ctx context.Context, tx *sql.Tx, endpoint *store.Endpoint, update bool) error {
	var owner string
	if update {
		if err := tx.QueryRowContext(ctx, `SELECT org_id FROM endpoints WHERE id = ?`, endpoint.ID).Scan(&owner); err != nil {
			return scanErr("get endpoint owner", err)
		}
		if owner != endpoint.OrgID {
			return store.ErrNotFound
		}
	}
	if endpoint.Domain != "" {
		hostname := strings.TrimSuffix(strings.ToLower(endpoint.Domain), ".")
		var collisions int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM endpoint_aliases WHERE hostname = ? AND endpoint_id <> ?`, hostname, endpoint.ID).Scan(&collisions); err != nil {
			return wrap("check endpoint alias", err)
		}
		if collisions > 0 {
			return store.ErrConflict
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM endpoints WHERE lower(rtrim(domain, '.')) = ? AND id <> ?`, hostname, endpoint.ID).Scan(&collisions); err != nil {
			return wrap("check endpoint domain", err)
		}
		if collisions > 0 {
			return store.ErrConflict
		}
	}
	if endpoint.Policy == nil {
		return nil
	}
	if endpoint.Policy.ServiceID != "" {
		if err := tx.QueryRowContext(ctx, `SELECT org_id FROM services WHERE id = ?`, endpoint.Policy.ServiceID).Scan(&owner); err != nil {
			return scanErr("get endpoint service", err)
		}
		if owner != endpoint.OrgID {
			return store.ErrNotFound
		}
	}
	for _, ref := range endpoint.Policy.Access {
		if err := tx.QueryRowContext(ctx, `SELECT org_id FROM connections WHERE id = ?`, ref.ConnectionID).Scan(&owner); err != nil {
			return scanErr("get endpoint connection", err)
		}
		if owner != endpoint.OrgID {
			return store.ErrNotFound
		}
	}
	return nil
}
