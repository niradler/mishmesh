package postgres

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
	ctx, cancel := context.WithTimeout(ctx, s.queryTimeout)
	defer cancel()
	hostname, err := normalizeAccessHostname(hostname)
	if err != nil {
		return nil, err
	}
	return scanEndpoint(s.db.QueryRowContext(ctx, `SELECT `+endpointCols+` FROM endpoints WHERE id = (SELECT endpoint_id FROM endpoint_aliases WHERE hostname = $1 AND org_id = endpoints.org_id)`, hostname).Scan)
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
	ctx, cancel := context.WithTimeout(ctx, s.queryTimeout)
	defer cancel()
	tx, err := s.beginNetworkWrite(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if expected != nil {
		current, err := scanConnection(tx.QueryRowContext(ctx, `SELECT `+connectionCols+` FROM connections WHERE id = $1 FOR UPDATE`, connectionID))
		if err != nil {
			return err
		}
		if !store.SameConnectionInput(*current, *expected) {
			return store.ErrConflict
		}
	}
	var owner string
	if err := tx.QueryRowContext(ctx, `SELECT org_id FROM connections WHERE id = $1`, connectionID).Scan(&owner); err != nil {
		return scanErr("get alias connection", err)
	}
	if owner != orgID {
		return store.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM endpoint_aliases WHERE org_id = $1 AND connection_id = $2`, orgID, connectionID); err != nil {
		return wrap("replace aliases", err)
	}
	hostnames := make([]string, len(aliases))
	for i, alias := range aliases {
		hostname, err := normalizeAccessHostname(alias.Hostname)
		if err != nil {
			return err
		}
		hostnames[i] = hostname
	}
	endpoints, err := loadAliasEndpoints(ctx, tx, aliases)
	if err != nil {
		return err
	}
	claims, err := loadDomainClaims(ctx, tx, hostnames)
	if err != nil {
		return err
	}
	for i, alias := range aliases {
		endpoint, ok := endpoints[alias.EndpointID]
		if !ok || endpoint.orgID != orgID {
			return store.ErrNotFound
		}
		if err := validateAliasAccess(connectionID, alias, endpoint); err != nil {
			return err
		}
		for _, claimant := range claims[hostnames[i]] {
			if claimant != alias.EndpointID {
				return store.ErrConflict
			}
		}
	}
	if err := insertAliases(ctx, tx, orgID, connectionID, aliases, hostnames); err != nil {
		return err
	}
	return wrap("replace aliases", tx.Commit())
}

func placeholder(n int) string { return fmt.Sprintf("$%d", n) }

type aliasEndpoint struct {
	orgID  string
	kind   string
	policy sql.NullString
}

func inList(first, count int) string {
	marks := make([]string, count)
	for i := range marks {
		marks[i] = placeholder(first + i)
	}
	return strings.Join(marks, ", ")
}

func loadAliasEndpoints(ctx context.Context, tx *sql.Tx, aliases []store.EndpointAlias) (map[string]aliasEndpoint, error) {
	out := make(map[string]aliasEndpoint, len(aliases))
	ids := make([]any, 0, len(aliases))
	seen := make(map[string]bool, len(aliases))
	for _, alias := range aliases {
		if !seen[alias.EndpointID] {
			seen[alias.EndpointID] = true
			ids = append(ids, alias.EndpointID)
		}
	}
	if len(ids) == 0 {
		return out, nil
	}
	rows, err := tx.QueryContext(ctx, `SELECT id, org_id, kind, policy FROM endpoints WHERE id IN (`+inList(1, len(ids))+`)`, ids...)
	if err != nil {
		return nil, wrap("get alias endpoints", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var endpoint aliasEndpoint
		if err := rows.Scan(&id, &endpoint.orgID, &endpoint.kind, &endpoint.policy); err != nil {
			return nil, wrap("get alias endpoints", err)
		}
		out[id] = endpoint
	}
	return out, wrap("get alias endpoints", rows.Err())
}

func loadDomainClaims(ctx context.Context, tx *sql.Tx, hostnames []string) (map[string][]string, error) {
	out := make(map[string][]string, len(hostnames))
	if len(hostnames) == 0 {
		return out, nil
	}
	args := make([]any, len(hostnames))
	for i, hostname := range hostnames {
		args[i] = hostname
	}
	rows, err := tx.QueryContext(ctx, `SELECT lower(rtrim(domain, '.')), id FROM endpoints WHERE lower(rtrim(domain, '.')) IN (`+inList(1, len(args))+`)`, args...)
	if err != nil {
		return nil, wrap("check alias domain", err)
	}
	defer rows.Close()
	for rows.Next() {
		var hostname, id string
		if err := rows.Scan(&hostname, &id); err != nil {
			return nil, wrap("check alias domain", err)
		}
		out[hostname] = append(out[hostname], id)
	}
	return out, wrap("check alias domain", rows.Err())
}

func insertAliases(ctx context.Context, tx *sql.Tx, orgID, connectionID string, aliases []store.EndpointAlias, hostnames []string) error {
	if len(aliases) == 0 {
		return nil
	}
	rows := make([]string, len(aliases))
	args := make([]any, 0, len(aliases)*4)
	for i, alias := range aliases {
		rows[i] = "(" + inList(i*4+1, 4) + ")"
		args = append(args, hostnames[i], orgID, alias.EndpointID, connectionID)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO endpoint_aliases (hostname, org_id, endpoint_id, connection_id) VALUES `+strings.Join(rows, ", "), args...)
	return uniqueErr("replace aliases", err)
}

func validateEndpointReferences(ctx context.Context, tx *sql.Tx, endpoint *store.Endpoint, update bool) error {
	for _, snapshot := range endpoint.ConnectionSnapshots {
		if snapshot == nil || snapshot.OrgID != endpoint.OrgID {
			return store.ErrConflict
		}
		current, err := scanConnection(tx.QueryRowContext(ctx, `SELECT `+connectionCols+` FROM connections WHERE id = $1 FOR UPDATE`, snapshot.ID))
		if err != nil {
			return err
		}
		if !store.SameConnectionInput(*current, *snapshot) {
			return store.ErrConflict
		}
	}
	var owner string
	if update {
		if err := tx.QueryRowContext(ctx, `SELECT org_id FROM endpoints WHERE id = $1`, endpoint.ID).Scan(&owner); err != nil {
			return scanErr("get endpoint owner", err)
		}
		if owner != endpoint.OrgID {
			return store.ErrNotFound
		}
	}
	if endpoint.Domain != "" {
		hostname := strings.TrimSuffix(strings.ToLower(endpoint.Domain), ".")
		var collisions int
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM endpoint_aliases WHERE hostname = $1 AND endpoint_id <> $2`, hostname, endpoint.ID).Scan(&collisions); err != nil {
			return wrap("check endpoint alias", err)
		}
		if collisions > 0 {
			return store.ErrConflict
		}
		if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM endpoints WHERE lower(rtrim(domain, '.')) = $1 AND id <> $2`, hostname, endpoint.ID).Scan(&collisions); err != nil {
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
		if err := tx.QueryRowContext(ctx, `SELECT org_id FROM services WHERE id = $1`, endpoint.Policy.ServiceID).Scan(&owner); err != nil {
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
		if err := tx.QueryRowContext(ctx, `SELECT org_id FROM connections WHERE id = $1`, ref.ConnectionID).Scan(&owner); err != nil {
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
	rows, err := tx.QueryContext(ctx, `SELECT policy FROM endpoints WHERE org_id = $1 AND id <> $2 AND policy IS NOT NULL AND policy <> ''`, endpoint.OrgID, endpoint.ID)
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

func validateAliasAccess(connectionID string, alias store.EndpointAlias, endpoint aliasEndpoint) error {
	if alias.Kind != "" && alias.Kind != endpoint.kind {
		return store.ErrConflict
	}
	if !endpoint.policy.Valid {
		return store.ErrConflict
	}
	var policy store.EndpointPolicy
	if err := json.Unmarshal([]byte(endpoint.policy.String), &policy); err != nil {
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
	previous, err := scanEndpoint(tx.QueryRowContext(ctx, `SELECT `+endpointCols+` FROM endpoints WHERE id = $1`, endpoint.ID).Scan)
	if err != nil {
		return err
	}
	if previous.Kind != endpoint.Kind {
		_, err := tx.ExecContext(ctx, `DELETE FROM endpoint_aliases WHERE endpoint_id = $1`, endpoint.ID)
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
			if _, err := tx.ExecContext(ctx, `DELETE FROM endpoint_aliases WHERE endpoint_id = $1 AND connection_id = $2`, endpoint.ID, before.ConnectionID); err != nil {
				return wrap("invalidate changed access aliases", err)
			}
		}
	}
	return nil
}
