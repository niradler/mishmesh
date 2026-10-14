package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
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
	return s.replaceConnectionAliases(ctx, orgID, connectionID, aliases, nil)
}

func (s *Store) ReplaceConnectionAliasesIfCurrent(ctx context.Context, expected *store.Connection, aliases []store.EndpointAlias) error {
	if expected == nil {
		return store.ErrConflict
	}
	return s.replaceConnectionAliases(ctx, expected.OrgID, expected.ID, aliases, expected)
}

func (s *Store) replaceConnectionAliases(ctx context.Context, orgID, connectionID string, aliases []store.EndpointAlias, expected *store.Connection) error {
	tx, err := s.beginNetworkWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if expected != nil {
		current, err := scanConnection(tx.QueryRowContext(ctx, `SELECT `+connectionCols+` FROM connections WHERE id = ?`, connectionID))
		if err != nil {
			return err
		}
		if !store.SameConnectionInput(*current, *expected) {
			return store.ErrConflict
		}
	}
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
		if err := validateAliasAccess(ctx, tx, connectionID, alias); err != nil {
			return err
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
	for _, snapshot := range endpoint.ConnectionSnapshots {
		if snapshot == nil || snapshot.OrgID != endpoint.OrgID {
			return store.ErrConflict
		}
		current, err := scanConnection(tx.QueryRowContext(ctx, `SELECT `+connectionCols+` FROM connections WHERE id = ?`, snapshot.ID))
		if err != nil {
			return err
		}
		if !store.SameConnectionInput(*current, *snapshot) {
			return store.ErrConflict
		}
	}
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
	seen := make(map[string]bool)
	for _, ref := range endpoint.Policy.Access {
		if seen[ref.ConnectionID] {
			return store.ErrConflict
		}
		seen[ref.ConnectionID] = true
		if err := tx.QueryRowContext(ctx, `SELECT org_id FROM connections WHERE id = ?`, ref.ConnectionID).Scan(&owner); err != nil {
			return scanErr("get endpoint connection", err)
		}
		if owner != endpoint.OrgID {
			return store.ErrNotFound
		}
		if ref.Hostname != "" {
			if _, err := normalizeAccessHostname(ref.Hostname); err != nil {
				return err
			}
		}
	}
	if err := validateAccessClaims(ctx, tx, endpoint); err != nil {
		return err
	}
	return nil
}

func validateAccessClaims(ctx context.Context, tx *sql.Tx, endpoint *store.Endpoint) error {
	if endpoint.Policy == nil || len(endpoint.Policy.Access) == 0 {
		return nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT policy FROM endpoints WHERE org_id = ? AND id <> ? AND policy IS NOT NULL AND policy <> ''`, endpoint.OrgID, endpoint.ID)
	if err != nil {
		return wrap("read access hostname claims", err)
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return wrap("read access hostname claims", err)
		}
		var policy store.EndpointPolicy
		if err := json.Unmarshal([]byte(raw), &policy); err != nil {
			return wrap("read access hostname claims", err)
		}
		for _, candidate := range endpoint.Policy.Access {
			if candidate.Hostname == "" {
				continue
			}
			host, err := normalizeAccessHostname(candidate.Hostname)
			if err != nil {
				return err
			}
			for _, claimed := range policy.Access {
				if claimed.ConnectionID != candidate.ConnectionID || claimed.Hostname == "" {
					continue
				}
				existing, err := normalizeAccessHostname(claimed.Hostname)
				if err != nil {
					return err
				}
				if existing == host {
					return store.ErrConflict
				}
			}
		}
	}
	return wrap("read access hostname claims", rows.Err())
}

func validateAliasAccess(ctx context.Context, tx *sql.Tx, connectionID string, alias store.EndpointAlias) error {
	var raw sql.NullString
	var kind string
	if err := tx.QueryRowContext(ctx, `SELECT policy, kind FROM endpoints WHERE id = ?`, alias.EndpointID).Scan(&raw, &kind); err != nil {
		return scanErr("read alias access", err)
	}
	if alias.Kind != "" && alias.Kind != kind {
		return store.ErrConflict
	}
	if !raw.Valid {
		return store.ErrConflict
	}
	var policy store.EndpointPolicy
	if err := json.Unmarshal([]byte(raw.String), &policy); err != nil {
		return wrap("read alias access", err)
	}
	requested := alias.AccessHostname
	if requested == "" && !alias.AccessHostnameKnown {
		requested = alias.Hostname
	}
	if alias.AccessHostnameKnown && requested == "" {
		for _, ref := range policy.Access {
			if ref.ConnectionID == connectionID && ref.Hostname == "" {
				return nil
			}
		}
		return store.ErrConflict
	}
	expected, err := normalizeAccessHostname(requested)
	if err != nil {
		return err
	}
	for _, ref := range policy.Access {
		hostname, err := normalizeAccessHostname(ref.Hostname)
		if ref.ConnectionID == connectionID && err == nil && hostname == expected {
			return nil
		}
	}
	return store.ErrConflict
}

func clearChangedEndpointAliases(ctx context.Context, tx *sql.Tx, endpoint *store.Endpoint) error {
	previous, err := scanEndpoint(tx.QueryRowContext(ctx, `SELECT `+endpointCols+` FROM endpoints WHERE id = ?`, endpoint.ID).Scan)
	if err != nil {
		return err
	}
	if previous.Kind != endpoint.Kind {
		_, err := tx.ExecContext(ctx, `DELETE FROM endpoint_aliases WHERE endpoint_id = ?`, endpoint.ID)
		return wrap("invalidate changed endpoint aliases", err)
	}
	if previous.Policy == nil {
		return nil
	}
	for _, before := range previous.Policy.Access {
		unchanged := false
		if endpoint.Policy != nil {
			for _, after := range endpoint.Policy.Access {
				if before.ConnectionID == after.ConnectionID && before.Hostname == "" && after.Hostname == "" {
					unchanged = true
					break
				}
				left, leftErr := normalizeAccessHostname(before.Hostname)
				right, rightErr := normalizeAccessHostname(after.Hostname)
				if before.ConnectionID == after.ConnectionID && leftErr == nil && rightErr == nil && left == right {
					unchanged = true
					break
				}
			}
		}
		if !unchanged {
			if _, err := tx.ExecContext(ctx, `DELETE FROM endpoint_aliases WHERE endpoint_id = ? AND connection_id = ?`, endpoint.ID, before.ConnectionID); err != nil {
				return wrap("invalidate changed access aliases", err)
			}
		}
	}
	return nil
}
