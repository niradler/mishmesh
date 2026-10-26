package sqlshared

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mishmesh/mishmesh/internal/store"
)

func NormalizeAccessHostname(hostname string) (string, error) {
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

func (d Dialect) GetEndpointByAccessHostname(ctx context.Context, db interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, hostname string) (*store.Endpoint, error) {
	hostname, err := NormalizeAccessHostname(hostname)
	if err != nil {
		return nil, err
	}
	return d.ScanEndpoint(db.QueryRowContext(ctx, d.rebind(d.rebind(`SELECT `+d.EndpointCols+` FROM endpoints WHERE id = (SELECT endpoint_id FROM endpoint_aliases WHERE hostname = ? AND org_id = endpoints.org_id)`)), hostname).Scan)
}

func (d Dialect) ReplaceConnectionAliases(ctx context.Context, begin func(context.Context) (*sql.Tx, error), orgID, connectionID string, aliases []store.EndpointAlias, expected *store.Connection) error {
	tx, err := begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if expected != nil {
		current, err := d.ScanConnection(tx.QueryRowContext(ctx, d.rebind(`SELECT `+d.ConnectionCols+` FROM connections WHERE id = ?`+d.LockSuffix), connectionID))
		if err != nil {
			return err
		}
		if !store.SameConnectionInput(*current, *expected) {
			return store.ErrConflict
		}
	}
	var owner string
	if err := tx.QueryRowContext(ctx, d.rebind(`SELECT org_id FROM connections WHERE id = ?`), connectionID).Scan(&owner); err != nil {
		return d.ScanErr("get alias connection", err)
	}
	if owner != orgID {
		return store.ErrNotFound
	}
	if _, err := tx.ExecContext(ctx, d.rebind(`DELETE FROM endpoint_aliases WHERE org_id = ? AND connection_id = ?`), orgID, connectionID); err != nil {
		return d.Wrap("replace aliases", err)
	}
	hostnames := make([]string, len(aliases))
	for i, alias := range aliases {
		hostname, err := NormalizeAccessHostname(alias.Hostname)
		if err != nil {
			return err
		}
		hostnames[i] = hostname
	}
	endpoints, err := d.loadAliasEndpoints(ctx, tx, aliases)
	if err != nil {
		return err
	}
	claims, err := d.loadDomainClaims(ctx, tx, hostnames)
	if err != nil {
		return err
	}
	for i, alias := range aliases {
		endpoint, ok := endpoints[alias.EndpointID]
		if !ok || endpoint.orgID != orgID {
			return store.ErrNotFound
		}
		if err := d.validateAliasAccess(connectionID, alias, endpoint); err != nil {
			return err
		}
		for _, claimant := range claims[hostnames[i]] {
			if claimant != alias.EndpointID {
				return store.ErrConflict
			}
		}
	}
	if err := d.insertAliases(ctx, tx, orgID, connectionID, aliases, hostnames); err != nil {
		return err
	}
	return d.Wrap("replace aliases", tx.Commit())
}

type aliasEndpoint struct {
	orgID  string
	kind   string
	policy sql.NullString
}

func (d Dialect) loadAliasEndpoints(ctx context.Context, tx *sql.Tx, aliases []store.EndpointAlias) (map[string]aliasEndpoint, error) {
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
	rows, err := tx.QueryContext(ctx, d.rebind(`SELECT id, org_id, kind, policy FROM endpoints WHERE id IN (`+d.inList(1, len(ids))+`)`), ids...)
	if err != nil {
		return nil, d.Wrap("get alias endpoints", err)
	}
	defer rows.Close()
	for rows.Next() {
		var id string
		var endpoint aliasEndpoint
		if err := rows.Scan(&id, &endpoint.orgID, &endpoint.kind, &endpoint.policy); err != nil {
			return nil, d.Wrap("get alias endpoints", err)
		}
		out[id] = endpoint
	}
	return out, d.Wrap("get alias endpoints", rows.Err())
}

func (d Dialect) loadDomainClaims(ctx context.Context, tx *sql.Tx, hostnames []string) (map[string][]string, error) {
	out := make(map[string][]string, len(hostnames))
	if len(hostnames) == 0 {
		return out, nil
	}
	args := make([]any, len(hostnames))
	for i, hostname := range hostnames {
		args[i] = hostname
	}
	rows, err := tx.QueryContext(ctx, d.rebind(`SELECT lower(rtrim(domain, '.')), id FROM endpoints WHERE lower(rtrim(domain, '.')) IN (`+d.inList(1, len(args))+`)`), args...)
	if err != nil {
		return nil, d.Wrap("check alias domain", err)
	}
	defer rows.Close()
	for rows.Next() {
		var hostname, id string
		if err := rows.Scan(&hostname, &id); err != nil {
			return nil, d.Wrap("check alias domain", err)
		}
		out[hostname] = append(out[hostname], id)
	}
	return out, d.Wrap("check alias domain", rows.Err())
}

func (d Dialect) insertAliases(ctx context.Context, tx *sql.Tx, orgID, connectionID string, aliases []store.EndpointAlias, hostnames []string) error {
	if len(aliases) == 0 {
		return nil
	}
	rows := make([]string, len(aliases))
	args := make([]any, 0, len(aliases)*4)
	for i, alias := range aliases {
		rows[i] = "(" + d.inList(i*4+1, 4) + ")"
		args = append(args, hostnames[i], orgID, alias.EndpointID, connectionID)
	}
	_, err := tx.ExecContext(ctx, d.rebind(`INSERT INTO endpoint_aliases (hostname, org_id, endpoint_id, connection_id) VALUES `+strings.Join(rows, ", ")), args...)
	return d.UniqueErr("replace aliases", err)
}

func (d Dialect) ValidateEndpointReferences(ctx context.Context, tx *sql.Tx, endpoint *store.Endpoint, update bool) error {
	for _, snapshot := range endpoint.ConnectionSnapshots {
		if snapshot == nil || snapshot.OrgID != endpoint.OrgID {
			return store.ErrConflict
		}
		current, err := d.ScanConnection(tx.QueryRowContext(ctx, d.rebind(`SELECT `+d.ConnectionCols+` FROM connections WHERE id = ?`+d.LockSuffix), snapshot.ID))
		if err != nil {
			return err
		}
		if !store.SameConnectionInput(*current, *snapshot) {
			return store.ErrConflict
		}
	}
	var owner string
	if update {
		if err := tx.QueryRowContext(ctx, d.rebind(`SELECT org_id FROM endpoints WHERE id = ?`), endpoint.ID).Scan(&owner); err != nil {
			return d.ScanErr("get endpoint owner", err)
		}
		if owner != endpoint.OrgID {
			return store.ErrNotFound
		}
	}
	if endpoint.Domain != "" {
		hostname := strings.TrimSuffix(strings.ToLower(endpoint.Domain), ".")
		var collisions int
		if err := tx.QueryRowContext(ctx, d.rebind(`SELECT COUNT(*) FROM endpoint_aliases WHERE hostname = ? AND endpoint_id <> ?`), hostname, endpoint.ID).Scan(&collisions); err != nil {
			return d.Wrap("check endpoint alias", err)
		}
		if collisions > 0 {
			return store.ErrConflict
		}
		if err := tx.QueryRowContext(ctx, d.rebind(`SELECT COUNT(*) FROM endpoints WHERE lower(rtrim(domain, '.')) = ? AND id <> ?`), hostname, endpoint.ID).Scan(&collisions); err != nil {
			return d.Wrap("check endpoint domain", err)
		}
		if collisions > 0 {
			return store.ErrConflict
		}
	}
	if endpoint.Policy == nil {
		return nil
	}
	if endpoint.Policy.ServiceID != "" {
		if err := tx.QueryRowContext(ctx, d.rebind(`SELECT org_id FROM services WHERE id = ?`), endpoint.Policy.ServiceID).Scan(&owner); err != nil {
			return d.ScanErr("get endpoint service", err)
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
		if err := tx.QueryRowContext(ctx, d.rebind(`SELECT org_id FROM connections WHERE id = ?`), ref.ConnectionID).Scan(&owner); err != nil {
			return d.ScanErr("get endpoint connection", err)
		}
		if owner != endpoint.OrgID {
			return store.ErrNotFound
		}
		if ref.Hostname != "" {
			if _, err := NormalizeAccessHostname(ref.Hostname); err != nil {
				return err
			}
		}
	}
	if err := d.validateAccessClaims(ctx, tx, endpoint); err != nil {
		return err
	}
	return nil
}

func (d Dialect) validateAccessClaims(ctx context.Context, tx *sql.Tx, endpoint *store.Endpoint) error {
	if endpoint.Policy == nil || len(endpoint.Policy.Access) == 0 {
		return nil
	}
	rows, err := tx.QueryContext(ctx, d.rebind(`SELECT policy FROM endpoints WHERE org_id = ? AND id <> ? AND policy IS NOT NULL AND policy <> ''`), endpoint.OrgID, endpoint.ID)
	if err != nil {
		return d.Wrap("read access hostname claims", err)
	}
	defer rows.Close()
	for rows.Next() {
		var raw string
		if err := rows.Scan(&raw); err != nil {
			return d.Wrap("read access hostname claims", err)
		}
		var policy store.EndpointPolicy
		if err := json.Unmarshal([]byte(raw), &policy); err != nil {
			return d.Wrap("read access hostname claims", err)
		}
		for _, candidate := range endpoint.Policy.Access {
			if candidate.Hostname == "" {
				continue
			}
			host, err := NormalizeAccessHostname(candidate.Hostname)
			if err != nil {
				return err
			}
			for _, claimed := range policy.Access {
				if claimed.ConnectionID != candidate.ConnectionID || claimed.Hostname == "" {
					continue
				}
				existing, err := NormalizeAccessHostname(claimed.Hostname)
				if err != nil {
					return err
				}
				if existing == host {
					return store.ErrConflict
				}
			}
		}
	}
	return d.Wrap("read access hostname claims", rows.Err())
}

func (d Dialect) validateAliasAccess(connectionID string, alias store.EndpointAlias, endpoint aliasEndpoint) error {
	if alias.Kind != "" && alias.Kind != endpoint.kind {
		return store.ErrConflict
	}
	if !endpoint.policy.Valid {
		return store.ErrConflict
	}
	var policy store.EndpointPolicy
	if err := json.Unmarshal([]byte(endpoint.policy.String), &policy); err != nil {
		return d.Wrap("read alias access", err)
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
	expected, err := NormalizeAccessHostname(requested)
	if err != nil {
		return err
	}
	for _, ref := range policy.Access {
		hostname, err := NormalizeAccessHostname(ref.Hostname)
		if ref.ConnectionID == connectionID && err == nil && hostname == expected {
			return nil
		}
	}
	return store.ErrConflict
}

func (d Dialect) ClearChangedEndpointAliases(ctx context.Context, tx *sql.Tx, endpoint *store.Endpoint) error {
	previous, err := d.ScanEndpoint(tx.QueryRowContext(ctx, d.rebind(`SELECT `+d.EndpointCols+` FROM endpoints WHERE id = ?`), endpoint.ID).Scan)
	if err != nil {
		return err
	}
	if previous.Kind != endpoint.Kind {
		_, err := tx.ExecContext(ctx, d.rebind(`DELETE FROM endpoint_aliases WHERE endpoint_id = ?`), endpoint.ID)
		return d.Wrap("invalidate changed endpoint aliases", err)
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
				left, leftErr := NormalizeAccessHostname(before.Hostname)
				right, rightErr := NormalizeAccessHostname(after.Hostname)
				if before.ConnectionID == after.ConnectionID && leftErr == nil && rightErr == nil && left == right {
					unchanged = true
					break
				}
			}
		}
		if !unchanged {
			if _, err := tx.ExecContext(ctx, d.rebind(`DELETE FROM endpoint_aliases WHERE endpoint_id = ? AND connection_id = ?`), endpoint.ID, before.ConnectionID); err != nil {
				return d.Wrap("invalidate changed access aliases", err)
			}
		}
	}
	return nil
}
