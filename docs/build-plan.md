# mishmesh — production build plan

**Goal:** production-ready drop-in for ngrok / Cloudflare Tunnel. Priority order: core tunnel
capabilities + per-endpoint customization first, then multi-tenant/prod backends, then SaaS UI.

## Capability matrix (target)

| Capability | Status |
|---|---|
| HTTP tunnel (subdomain + `/tunnel/{id}` path) | done (MVP) |
| WebSocket / bidirectional streaming / SSE | **core** — replace one-shot ingress exchange |
| TCP tunnel (dynamic port) | done |
| TLS passthrough (kind=tls, SNI routing) | **core** |
| HTTPS edge: BYO cert + ACME | done |
| Self-signed cert generation (dev/local) | **core** |
| HTTPS local target (agent TLS dial, skip-verify opt) | **core** |
| Per-endpoint policy (headers, path, host rewrite, basic-auth, IP allow/deny, force-https, max-body, compression) | **customization** |
| Per-endpoint OIDC auth at edge | customization |
| Quotas (agents/endpoints) + bandwidth metering | prod |
| Prometheus `/metrics` | prod |
| Redis ConnectionStore (shared usage/presence) | prod |
| Postgres DataStore | prod |
| Enterprise reach-in data-plane API + agent allowlist | enterprise |
| Identity: password + Google OIDC, sessions, orgs/roles | SaaS |
| React SPA (react-query + Tailwind + shadcn) | SaaS |

## Architecture seams (unchanged)

- `store.DataStore` (durable) — sqlite default, postgres plug-in.
- `store.ConnectionStore` (ephemeral live conns + usage) — memory default, redis plug-in.
- `gateway` exports `store.AgentConn.OpenStream(ctx, endpointID, kind, meta)`; ingress + reach-in are consumers.
- `internal/tunnel` = shared WSS+yamux protocol; endpoint policy travels in the register protocol.
- Features gated by `MISHMESH_*` env flags; disabled = not wired in.

## Multi-pod note

Redis ConnectionStore shares **usage + presence**, not live connections — a yamux session lives in the
process that holds the agent websocket. Cross-pod stream routing (forwarding an ingress request to the
pod that owns the agent) is deferred; single-process and sticky-routing deployments are fully supported.

## Config reference

See `internal/config/config.go`. Key flags (all `MISHMESH_*`):
`INGRESS_ADDR, API_ADDR, BASE_DOMAIN, PUBLIC_SCHEME, DATA_DSN, DATA_BACKEND(sqlite|postgres),
CONN_BACKEND(memory|redis), REDIS_URL, AUTH_ENABLED, AUTH_PASSWORD_ENABLED, WEBUI_ENABLED, WEBUI_DIR,
INGRESS_ENABLED, TLS_ENABLED, HTTPS_ADDR, TLS_CERT_FILE, TLS_KEY_FILE, SELF_SIGNED_TLS, ACME_ENABLED,
ACME_EMAIL, TCP_ENABLED, TCP_PORT_MIN/MAX, TLS_PASSTHROUGH_ENABLED, TLS_PASSTHROUGH_ADDR,
METRICS_ENABLED, REACHIN_ENABLED, GOOGLE_CLIENT_ID, GOOGLE_CLIENT_SECRET, OIDC_REDIRECT_URL,
OIDC_ISSUER, SESSION_TTL_HOURS, QUOTA_MAX_AGENTS, QUOTA_MAX_ENDPOINTS, QUOTA_MAX_BANDWIDTH_BYTES,
BOOTSTRAP_TOKEN, API_AUTH_TOKEN, API_AUTH_DISABLED`.

REST contract: see `docs/api.md`.
