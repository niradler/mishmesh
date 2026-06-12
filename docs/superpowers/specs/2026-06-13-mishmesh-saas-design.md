# mishmesh — SaaS Tunnel Platform — Design (umbrella)

**Status:** Approved (brainstorm) — building MVP
**Date:** 2026-06-13
**Relationship:** Control plane layered on top of the **Tunnelmesh** core (see `prd.md`). Monorepo. Go backend embeds the core as a library. Tunnelmesh PRD unchanged.

---

## 1. What mishmesh is

A complete SaaS tunnel platform for exposing private networks, comparable to ngrok / Cloudflare Tunnel, built **on top of** the Tunnelmesh tunnel core. Two use cases, served by one product:

1. **Enterprise headless gateway** — vendor reaches *into* a customer private network. Data plane is API-initiated; no public URLs; auth/UI off.
2. **Multi-tenant SaaS (ngrok-style)** — users expose private services to *public* URLs; self-serve, multi-tenant, full auth + UI.

The tunnel transport and stream types are **identical** in both cases (Tunnelmesh: TCP, HTTP forward, TLS passthrough, reverse). The only net-new subsystem is the **public ingress / endpoint-mapping** layer.

## 2. Artifacts & feature flags

Two Docker images:

- **`mishmesh-server`** — one binary bundling control-plane API + public ingress + embedded gateway + (later) the built SPA. No coarse "mode"; granular env-var feature flags:
  - `MISHMESH_AUTH_ENABLED` — off ⇒ headless/trusted gateway; on ⇒ require login.
  - `MISHMESH_AUTH_PASSWORD_ENABLED` — off ⇒ force Google-only.
  - `MISHMESH_WEBUI_ENABLED` — serve the SPA.
  - `MISHMESH_INGRESS_ENABLED` — public HTTP/TCP/TLS ingress on/off.
  - Data-plane API + embedded gateway are always present (the core).
  - Enterprise headless = `AUTH=off, WEBUI=off`. Full SaaS = all on.
- **`mishmesh-agent`** — the tunnel client (Tunnelmesh agent + authtoken/endpoint UX + CLI).

Internally the server is modular packages (`gateway`, `ingress`, `controlplane`, `store`, `tunnel`, `config`) so disabled features are simply not wired in, and components stay independently testable / scalable later.

## 3. Storage — two pluggable interfaces ("replaceable")

- **`DataStore`** (durable): orgs, users, memberships, agents, endpoints, domains, API tokens, audit, quotas. Default **SQLite** (dev); **Postgres** plug-in later. Repository-pattern interface, one impl per backend, chosen by config — no code changes to swap.
- **`ConnectionStore`** (ephemeral): live agent→session connection map + liveness. Default **in-memory** (dev/single-process); **Redis** plug-in later.

Mirrors Tunnelmesh's own two-responsibility store split.

## 4. Identity & auth

- **Browser:** Google OAuth **and** username/password; password toggleable off to force Google. httpOnly secure session cookie for the SPA.
- **Programmatic:** bearer **API tokens** (personal/org) for CLI + REST API.
- **Agents:** tenant-scoped **authtoken** issued by the control plane; presented on WSS connect; revocable; maps to org + agent identity.
- Auth disabled entirely when `AUTH_ENABLED=false` (trusted env).

## 5. Tenancy & data model

`Org` (tenant) → `User`/`Membership` (roles owner/admin/member) → owns → `Agent` (credentialed client) → hosts → `Endpoint`.

- **Endpoint** = binding {public address ↔ agent local target}. Kinds: **HTTP** (subdomain `abc.host` **or** path `host/tunnel/{id}`), **TCP** (public port), **TLS** (passthrough). Lifecycle: **ephemeral** (runtime-declared, random name, dies on disconnect) or **reserved** (pre-created, stable).
- **Domain** (custom domain + ACME/BYO cert), **Quota** (per-org limits), **AuditEvent**, **Token**.
- Enterprise reach-in: `Agent` + allowlist, **no** endpoint; data plane API-initiated (Tunnelmesh model).

## 6. Public ingress (net-new subsystem)

HTTP host+path router · subdomain + custom-domain TLS (ACME + BYO) · TCP port allocator · TLS passthrough · per-endpoint auth (basic/OIDC) · maps inbound public request → core tunnel stream to the owning agent. Quota/bandwidth enforced here.

## 7. Tech stack

- Backend: **Go** (stdlib net/http, `log/slog`), embeds Tunnelmesh core.
- Tunnel transport: **WebSocket (coder/websocket)** + **yamux** multiplexing.
- DataStore: **modernc.org/sqlite** (pure Go, CGO-free) via `database/sql`.
- Frontend (later): **React + TypeScript (Vite)** SPA.
- API: REST first (gRPC later).

## 8. Decomposition (each sub-spec → plan → build)

1. **Foundation** — monorepo, server skeleton + feature flags, config, `DataStore`(SQLite)+`ConnectionStore`(in-mem), embedded gateway, tunnel core.
2. **Identity & tenancy** — Google+password auth, orgs/users/roles, API tokens, agent authtokens, quotas.
3. **Agent + endpoint control API** — unified lifecycle, reserved/ephemeral endpoints, allowlist, liveness.
4. **Public ingress** — HTTP/TCP/TLS edge, domains/ACME, endpoint auth.
5. **Web UI** — React SPA dashboards + management.
6. **CLI / agent UX** — `mishmesh login`, `mishmesh http 3000`.

---

## 9. MVP scope (current build target)

Thinnest end-to-end ngrok-style HTTP slice that proves the value, with clean seams for the deferred work.

**In:**
- `mishmesh-server`: SQLite `DataStore` + in-mem `ConnectionStore`, embedded minimal gateway (WSS + yamux), HTTP public ingress (subdomain **and** `/tunnel/{id}` path), agent-authtoken auth, minimal control REST API, feature-flag env vars, seed CLI (`token create`).
- `mishmesh-agent`: authtoken → declare HTTP endpoint for a local target → connect over WSS → serve gateway-initiated streams to the local target.
- End-to-end: public HTTP request streams through the tunnel to the local service and back.
- Interface seams: `DataStore`, `ConnectionStore`, `tunnel.Session`, ingress router — all swappable. Tests on core pieces + full-loop smoke test.

**Deferred (seams left):** WebUI, Google/password OAuth, custom domains/ACME, TCP/TLS endpoints, Redis/PG, gRPC, quota enforcement, endpoint auth, multi-pod routing.

## 10. MVP architecture & flow

```
public client ──HTTP──► ingress (host/path router)
                          │ resolve endpoint_id by host or /tunnel/{id}
                          ▼
                        ConnectionStore: endpoint_id → live agent session
                          │ session.Open(StreamInit{endpoint_id, kind:http})
                          ▼ (yamux stream over WSS)
                        agent: Accept stream → init.endpoint_id → local target
                          │ dial localhost:3000, proxy HTTP req/resp (streaming)
                          ▼
                        local service
```

- **tunnel.Session**: yamux over a WSS `net.Conn`. `Open(ctx, StreamInit) (net.Conn, error)` (writes a length-prefixed JSON init header); `Accept() (net.Conn, StreamInit, error)` (reads it). Protocol-agnostic labeled streams — reusable.
- **Control channel**: agent opens a dedicated yamux stream on connect; length-prefixed JSON messages: `Register{endpoints}` → `RegisterAck{endpoint_id, public_url}`; periodic ping/pong for liveness.
- **Auth**: WSS upgrade carries `Authorization: Bearer <authtoken>`; gateway resolves token → agent/org via DataStore.
- **Routing**: ingress matches `Host` (subdomain) or `/tunnel/{id}` path → endpoint_id → ConnectionStore session → `Open` → one-shot HTTP/1.1 exchange over the stream (streaming bodies).

## 11. Module layout

```
go.mod  (module github.com/mishmesh/mishmesh)
cmd/mishmesh-server/   server binary + seed CLI
cmd/mishmesh-agent/    agent binary + CLI
internal/tunnel/       protocol, session, ws transport (reusable core)
internal/gateway/      agent WSS termination, control channel, registry, liveness
internal/agent/        client: dial, authtoken, endpoints, local dialer
internal/ingress/      public HTTP router → tunnel
internal/store/        DataStore + ConnectionStore interfaces + entities
internal/store/sqlite/ SQLite DataStore impl
internal/store/memory/ in-mem ConnectionStore (+ DataStore for tests)
internal/config/       env-var feature flags + config
```
