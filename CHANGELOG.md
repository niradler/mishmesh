# Changelog

## Unreleased — pluggable Access (proposed v0.1.0-beta.7)

- Named Cloudflare public access and Tailscale private HTTPS connections over existing networks, with provider-owned hostname/protocol capabilities and per-endpoint Access refs. Preserve Connect, UDP and canonical ingress behavior. NetBird/WireGuard remain extension targets; VPN peer/key/route management and Kubernetes providers/drivers are outside this release.
- Typed Docker/Compose/Command config, readiness and Requires APIs plus descriptor-driven service/Connections/Access UI and topology. Explicit Save and restart reserves lifecycle admission across stop/save/apply/start, blocks stale wakes, and reports failed phase/saved state. Requirements share holds and manual stop/delete guards.
- Atomic legacy database, connection ciphertext/AAD and parsed authorization migration. **Breaking:** old service `spec`/`exec`, endpoint `public_access` and singleton Cloudflare REST writes are retired. Back up the database and matching secret key before upgrading; restoring an old binary also requires its matching pre-migration database/key pair. Missing/wrong keys and malformed records fail before commit.
- Generation-fenced durable aliases, ingress invalidation, always-redacted sensitive API config and presence-only connection secrets. Atomic no-overwrite key publication fixes concurrent first-start reads of incomplete keys.
- Regression scripts, retained isolated PostgreSQL fixtures and real browser/server/database/key save/reopen coverage. Windows/Linux races,12release crossbuilds and dual-OS govulncheck pass; zero reachable/imported-package findings, with an unsuppressed module-only advisory for unused OpenPGP.
- Replace the vulnerable Tailwind3 build dependency chain with Tailwind4; npm audit reports zero vulnerabilities. UI browser floor: Safari16.4+, Chrome111+, Firefox128+. See [Connections and migration](docs/connections.md) and the [reviewable rollout runbook](.claude/scripts/pluggable-types/lab-rollout.md).

This entry is preparation only. No tag, publication or deployment has occurred. Live tailnet HTTPS requires a joined/tagged host, approvals/grants and a second-client check.

## v0.1.0-beta.2

- Bound runtime Postgres operations and connection-pool waits with a configurable five-second deadline (`MISHMESH_DATA_QUERY_TIMEOUT`, Helm `dataPool.queryTimeout`), so a stalled database produces a retryable API error instead of leaving requests waiting indefinitely.
- Preserve shorter caller deadlines; reject invalid/nonpositive timeout settings.
- Avoid including database passwords in startup configuration errors.
- Add repeatable published-image sustained traffic, backup/restore and database-stall validation. Fix the historical profiling harness for the current Redis pool configuration.

## v0.1.0-beta.1

First beta. One server binary, one agent binary, one Helm chart, three deployment shapes:
homelab, company / on-prem, SaaS.

### Tunnels and ingress
- HTTP(S) tunnels on subdomains and verified custom domains, with optional `/tunnel/{id}` path routing
- Raw TCP ports, TLS passthrough, WebSocket and SSE streaming, 100-continue uploads
- Automatic HTTPS via ACME (single pod) or a wildcard certificate secret (cluster)
- Endpoint policy: basic auth, OIDC login, IP allow/deny, mTLS, per-endpoint rate limits
- Forwarded headers with a trusted-proxy allowlist; clear 502/503/504 pages
- gRPC unary and bidirectional streams through HTTP/2 ingress and h2c/HTTPS upstreams
- Configurable upstream response-header timeout (5m default), including long-poll requests
- Path routing disabled consistently in ingress, agent announcements, control API URLs and SSH forwards

### Agent
- `agent.yml` with many tunnels per session, `start`, `validate`, `tls` commands
- `service install` for systemd, launchd and Windows services
- Reach-in-only gateways (no tunnels) for private network access
- Clear errors and exit on rejected tokens; reconnect with jittered backoff

### Private network access (reach-in)
- HTTP reach-in and raw TCP stream reach-in (`Upgrade: mishmesh-stream`) through an agent, org scoped

### Identity and tenancy
- Password and Google login, organisations, roles, invites, org switching
- Signup modes `org` (SaaS) and `invite` (company / homelab)
- Agent tokens hashed at rest, rotation revokes the previous token and drops the live session
- Custom domain ownership verification via DNS TXT
- Quotas (operator managed) with in-flight bandwidth metering

### Operations
- Postgres for durable state, Redis for cluster mode
- Cluster mode: stateless pods behind a load balancer, pod-to-pod relay, cluster-wide TCP port claims, drain on shutdown
- Prometheus metrics (token protected), readiness and liveness endpoints
- Docker Compose and Helm (homelab, company and SaaS profiles)
- Helm values for allowed origins, private proxy target rules and organization creation limits
- Concurrent bootstrap seeding across pods, with shared agent/token conflict validation
- Reserved TCP endpoints rebind to their existing port claims across agent/server restarts
- Go 1.26.9 and x/net v0.60.0 security patches

### Measured (4 vCPU / 8 GB pod)
- ~20k req/s at 1 KB, ~2.3 GB/s at 1 MB on one pod; cross-pod relay ~11k req/s at +2.5 ms p50
- ~70 KB RSS per idle agent; agents reconnect to a healthy pod within ~3 s of a pod failure

See README "Beta limitations" for known gaps.
