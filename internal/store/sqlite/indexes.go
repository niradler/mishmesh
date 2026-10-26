package sqlite

const foreignKeyIndexes = `
CREATE INDEX IF NOT EXISTS idx_agents_org ON agents(org_id);
CREATE INDEX IF NOT EXISTS idx_tokens_agent ON tokens(agent_id);
CREATE INDEX IF NOT EXISTS idx_tokens_org ON tokens(org_id);
CREATE INDEX IF NOT EXISTS idx_endpoints_agent ON endpoints(agent_id);
CREATE INDEX IF NOT EXISTS idx_memberships_user ON memberships(user_id);
CREATE INDEX IF NOT EXISTS idx_sessions_user_org ON sessions(user_id, org_id);
CREATE INDEX IF NOT EXISTS idx_sessions_expires ON sessions(expires_at);
CREATE INDEX IF NOT EXISTS idx_invites_org ON invites(org_id);
CREATE INDEX IF NOT EXISTS idx_service_requires_target ON service_requires(requires_id);
CREATE INDEX IF NOT EXISTS idx_service_connections_connection ON service_connections(connection_id);
CREATE INDEX IF NOT EXISTS idx_endpoint_aliases_connection ON endpoint_aliases(connection_id);
CREATE INDEX IF NOT EXISTS idx_endpoint_aliases_endpoint ON endpoint_aliases(endpoint_id);
CREATE INDEX IF NOT EXISTS idx_workload_identities_org ON workload_identities(org_id);
`
