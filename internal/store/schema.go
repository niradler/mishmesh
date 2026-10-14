package store

import "errors"

var ErrUnsupportedSchema = errors.New("unsupported database schema; initialize a fresh database for typed services and connections")

func ValidateSchema(tables map[string]map[string]bool) error {
	if _, exists := tables["cloudflare_connections"]; exists {
		return ErrUnsupportedSchema
	}
	if columns := tables["services"]; columns["spec"] {
		return ErrUnsupportedSchema
	}
	for table, required := range map[string][]string{
		"endpoints":   {"id", "agent_id", "org_id", "kind", "lifecycle", "subdomain", "domain", "port", "policy", "created_at", "method"},
		"services":    {"id", "org_id", "name", "driver", "config", "ready", "idle_timeout_seconds", "start_timeout_seconds", "created_at", "updated_at"},
		"connections": {"id", "org_id", "type", "name", "config", "secrets_enc", "state", "created_at", "updated_at"},
	} {
		columns, exists := tables[table]
		if !exists {
			continue
		}
		for _, column := range required {
			if !columns[column] {
				return ErrUnsupportedSchema
			}
		}
	}
	return nil
}
