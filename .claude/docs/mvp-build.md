# mishmesh build — progress

**Goal:** Working headless ngrok replacement, deployable to cloud. Lean, clean, extensible. No web UI.
**Repo:** https://github.com/niradler/mishmesh (public, branch main)
**Spec:** docs/superpowers/specs/2026-06-13-mishmesh-saas-design.md
**Module:** github.com/mishmesh/mishmesh · Go 1.26

## State: FULL PRODUCT ✅ (prod-ready drop-in; `go test -race ./...` green)
Core tunneling + per-endpoint customization + multi-tenant SaaS + React UI all built.
`go test -race ./...` all pass (10 pkgs). SPA builds (`web/dist`). Server boots, serves
SPA + /metrics + control API; smoke-verified end-to-end.

### Capabilities (all done unless noted)
- HTTP tunnels (subdomain + /tunnel/{id} + custom domain), **WebSocket/upgrade + streaming** passthrough
- TCP tunnels (dynamic ports), **TLS passthrough (kind=tls, SNI routing)**
- HTTPS edge: BYO cert + ACME + **self-signed generation**; **HTTPS local targets** (agent TLS dial, --insecure)
- **Per-endpoint policy**: req/resp header rewrite, host header, path strip/add, basic-auth (bcrypt),
  IP allow/deny CIDR, force-https, max-body, gzip compression. (Edge **OIDC fails closed** — see deferred.)
- **Quotas** (agents/endpoints/bandwidth) enforced in createAgent/gateway/ingress; quota CRUD API
- **Prometheus /metrics** (agents, streams, bytes, handshake failures)
- **Postgres DataStore + Redis ConnectionStore** (config-selected; redis shares usage/presence)
- **Reach-in data-plane API** + **agent allowlist** (deny-first, IP-pinned, hard-deny loopback/link-local/metadata)
- **Identity**: password + Google OIDC login, httpOnly sessions, users/orgs/roles, org-scoping middleware, audit
- **React SPA** (Vite + react-query + Tailwind + shadcn), served from MISHMESH_WEBUI_DIR; multi-stage Docker

### Deferred / known gaps (intentional)
- **Edge OIDC** (end-user OAuth gating on published endpoints, ngrok/Cloudflare-Access style): policy
  field exists, fails closed (503) until implemented; reuses login OIDC plumbing. **Awaiting Nir's go/no-go.**
- **TCP-endpoint bandwidth** not metered into per-org usage quota (HTTP+TLS are). TCP metrics counted.
- **Multi-pod stream routing** deferred: redis shares usage/presence only; a live yamux session lives in the
  process holding the agent WS. Single-process / sticky-routing fully supported.

## Done
- [x] MVP: WSS+yamux tunnel core, gateway, ingress (HTTP subdomain+path), agent, SQLite DataStore, in-mem ConnectionStore, CLIs
- [x] Control-plane management API: agents/tokens CRUD, rotate, revoke (live-kill), delete, orgs; bearer auth (MISHMESH_API_AUTH_TOKEN)
- [x] HTTPS ingress: BYO cert (wildcard) + ACME/autocert; HTTP/HTTPS listeners
- [x] TCP endpoints: dynamic per-endpoint listeners + port allocation (gateway PortOpener seam)
- [x] Docker (server+agent, distroless) + docker-compose demo + deploy/README.md + MISHMESH_BOOTSTRAP_TOKEN
- [x] CLAUDE.md, .gitattributes (LF)

## Remaining roadmap (all core items DONE; these are follow-ups)
1. Edge OIDC end-user gating on published endpoints (ngrok/Cloudflare-Access style) — pending Nir go/no-go.
2. Meter TCP-endpoint bytes into per-org bandwidth usage (ingress/tcp.go needs endpoint org; carry org at Open).
3. Multi-pod stream routing (forward ingress request to the pod owning the agent) — needs inter-pod data plane.
4. WebSocket e2e test in internal/e2e (manual smoke confirms it works; add automated coverage).
5. Personal/org API tokens for programmatic access beyond the admin bearer (optional).
6. Cleanup: remove leftover agent worktrees under .claude/worktrees (git worktree remove ...).

## Style rules (memory + CLAUDE.md)
- NO code comments, ever. Multi-line comments forbidden.
- Bind dev servers to 127.0.0.1 (Windows firewall); containers bind 0.0.0.0 via image env.
- No AI attribution in commits.

## Architecture seams (for extension)
- store.DataStore (durable) + store.ConnectionStore (ephemeral) — add backends behind these.
- gateway exports store.AgentConn.OpenStream; ingress + (future) reach-in API are consumers.
- gateway.PortOpener — TCP ingress seam.
- internal/tunnel = shared protocol/session/transport (both binaries).

## Next session prompt
Context: mishmesh — full ngrok/Cloudflare-Tunnel drop-in (Go monorepo + React SPA). Core tunneling,
per-endpoint customization, quotas, metrics, Postgres/Redis, reach-in, identity, and the web UI are
all built and `go test -race ./...` is green. Repo github.com/niradler/mishmesh, branch main.
State: see "State" / "Remaining roadmap" above. Docs: docs/api.md (REST contract), docs/build-plan.md.
Next: pick a follow-up from "Remaining roadmap" (edge-OIDC pending Nir's decision); keep no-comments rule;
commit+push per feature; run `make check` before committing.
Issues: control/API listener (8081) must stay private or use MISHMESH_API_AUTH_TOKEN; with AUTH_ENABLED
the SPA session cookie protects it. Two agent worktrees left under .claude/worktrees (safe to remove).
