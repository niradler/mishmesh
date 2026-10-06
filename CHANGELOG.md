# Changelog

## v0.1.0-beta.1

First beta. One server binary, one agent binary, one Helm chart, three deployment shapes:
homelab, company / on-prem, SaaS.

### Tunnels and ingress
- HTTP(S) tunnels on subdomains and verified custom domains, with optional `/tunnel/{id}` path routing
- Raw TCP ports, TLS passthrough, WebSocket and SSE streaming, 100-continue uploads
- Automatic HTTPS via ACME (single pod) or a wildcard certificate secret (cluster)
- Endpoint policy: basic auth, OIDC login, IP allow/deny, mTLS, per-endpoint rate limits
- Forwarded headers with a trusted-proxy allowlist; clear 502/503/504 pages

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

### Measured (4 vCPU / 8 GB pod)
- ~20k req/s at 1 KB, ~2.3 GB/s at 1 MB on one pod; cross-pod relay ~11k req/s at +2.5 ms p50
- ~70 KB RSS per idle agent; agents reconnect to a healthy pod within ~3 s of a pod failure

See README "Beta limitations" for known gaps.
