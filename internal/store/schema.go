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
		"orgs":                {"id", "name", "created_at"},
		"agents":              {"id", "org_id", "name", "status", "created_at", "last_seen_at"},
		"tokens":              {"id", "org_id", "agent_id", "hash", "created_at", "revoked_at"},
		"quotas":              {"org_id", "max_agents", "max_endpoints", "max_bandwidth_bytes", "updated_at"},
		"users":               {"id", "email", "name", "password_hash", "google_sub", "created_at"},
		"memberships":         {"org_id", "user_id", "role", "created_at"},
		"sessions":            {"id_hash", "user_id", "org_id", "created_at", "expires_at"},
		"invites":             {"id", "token_hash", "org_id", "email", "role", "invited_by", "created_at", "expires_at"},
		"audit":               {"id", "org_id", "actor", "action", "target", "detail", "created_at"},
		"domains":             {"id", "org_id", "name", "token", "verified_at", "created_at"},
		"org_policies":        {"org_id", "cedar_src", "updated_at"},
		"groups":              {"id", "org_id", "name", "created_at"},
		"group_members":       {"org_id", "group_id", "member_type", "member_id", "created_at"},
		"service_requires":    {"org_id", "service_id", "requires_id"},
		"service_connections": {"org_id", "service_id", "connection_id"},
		"endpoint_aliases":    {"hostname", "org_id", "endpoint_id", "connection_id"},
		"endpoints":           {"id", "agent_id", "org_id", "kind", "lifecycle", "subdomain", "domain", "port", "policy", "created_at", "method"},
		"services":            {"id", "org_id", "name", "driver", "config", "ready", "idle_timeout_seconds", "start_timeout_seconds", "created_at", "updated_at"},
		"connections":         {"id", "org_id", "type", "name", "config", "secrets_enc", "state", "created_at", "updated_at"},
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
