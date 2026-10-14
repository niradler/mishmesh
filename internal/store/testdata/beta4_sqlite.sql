
CREATE TABLE IF NOT EXISTS orgs (
  id         TEXT PRIMARY KEY,
  name       TEXT NOT NULL,
  created_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS agents (
  id           TEXT PRIMARY KEY,
  org_id       TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  name         TEXT NOT NULL,
  status       TEXT NOT NULL,
  created_at   INTEGER NOT NULL,
  last_seen_at INTEGER
);
CREATE TABLE IF NOT EXISTS tokens (
  id         TEXT PRIMARY KEY,
  org_id     TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  agent_id   TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
  hash       TEXT NOT NULL UNIQUE,
  created_at INTEGER NOT NULL,
  revoked_at INTEGER
);
CREATE TABLE IF NOT EXISTS endpoints (
  id         TEXT PRIMARY KEY,
  agent_id   TEXT NOT NULL REFERENCES agents(id) ON DELETE CASCADE,
  org_id     TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  kind       TEXT NOT NULL,
  lifecycle  TEXT NOT NULL,
  subdomain  TEXT,
  domain     TEXT,
  port       INTEGER NOT NULL DEFAULT 0,
  policy     TEXT,
  created_at INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_endpoints_subdomain
  ON endpoints(subdomain) WHERE subdomain IS NOT NULL AND subdomain <> '';
CREATE UNIQUE INDEX IF NOT EXISTS idx_endpoints_domain
  ON endpoints(domain) WHERE domain IS NOT NULL AND domain <> '';
CREATE TABLE IF NOT EXISTS quotas (
  org_id              TEXT PRIMARY KEY REFERENCES orgs(id) ON DELETE CASCADE,
  max_agents          INTEGER NOT NULL DEFAULT 0,
  max_endpoints       INTEGER NOT NULL DEFAULT 0,
  max_bandwidth_bytes INTEGER NOT NULL DEFAULT 0,
  updated_at          INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS users (
  id            TEXT PRIMARY KEY,
  email         TEXT NOT NULL UNIQUE,
  name          TEXT NOT NULL,
  password_hash TEXT,
  google_sub    TEXT,
  created_at    INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_users_google_sub
  ON users(google_sub) WHERE google_sub IS NOT NULL AND google_sub <> '';
CREATE TABLE IF NOT EXISTS memberships (
  org_id     TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role       TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  PRIMARY KEY (org_id, user_id)
);
CREATE TABLE IF NOT EXISTS sessions (
  id_hash    TEXT PRIMARY KEY,
  user_id    TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  org_id     TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS invites (
  id         TEXT PRIMARY KEY,
  token_hash TEXT NOT NULL UNIQUE,
  org_id     TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  email      TEXT NOT NULL,
  role       TEXT NOT NULL,
  invited_by TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_invites_email ON invites(email);
CREATE TABLE IF NOT EXISTS audit (
  id         TEXT PRIMARY KEY,
  org_id     TEXT NOT NULL,
  actor      TEXT NOT NULL,
  action     TEXT NOT NULL,
  target     TEXT,
  detail     TEXT,
  created_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_audit_org ON audit(org_id, created_at);
CREATE TABLE IF NOT EXISTS domains (
  id          TEXT PRIMARY KEY,
  org_id      TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  name        TEXT NOT NULL,
  token       TEXT NOT NULL,
  verified_at INTEGER,
  created_at  INTEGER NOT NULL,
  UNIQUE (org_id, name)
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_domains_verified_name
  ON domains(name) WHERE verified_at IS NOT NULL;
CREATE TABLE IF NOT EXISTS org_policies (
  org_id     TEXT PRIMARY KEY REFERENCES orgs(id) ON DELETE CASCADE,
  cedar_src  TEXT NOT NULL,
  updated_at INTEGER NOT NULL
);


CREATE TABLE IF NOT EXISTS services (
  id                    TEXT PRIMARY KEY,
  org_id                TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  name                  TEXT NOT NULL,
  driver                TEXT NOT NULL,
  spec                  TEXT NOT NULL,
  idle_timeout_seconds  INTEGER NOT NULL DEFAULT 0,
  start_timeout_seconds INTEGER NOT NULL DEFAULT 0,
  created_at            INTEGER NOT NULL,
  updated_at            INTEGER NOT NULL,
  UNIQUE (org_id, name)
);
CREATE TABLE IF NOT EXISTS cloudflare_connections (
  org_id           TEXT PRIMARY KEY REFERENCES orgs(id) ON DELETE CASCADE,
  name             TEXT NOT NULL,
  account_id       TEXT NOT NULL,
  tunnel_id        TEXT NOT NULL,
  tunnel_token_enc BLOB,
  api_token_enc    BLOB,
  access_app_id    TEXT NOT NULL DEFAULT '',
  origin           TEXT NOT NULL DEFAULT '',
  connector        TEXT NOT NULL,
  metrics_addr     TEXT NOT NULL DEFAULT '',
  prune            INTEGER NOT NULL DEFAULT 0,
  updated_at       INTEGER NOT NULL
);
