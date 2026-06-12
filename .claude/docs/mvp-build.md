# mishmesh build — progress

**Goal:** Working headless ngrok replacement, deployable to cloud. Lean, clean, extensible. No web UI.
**Repo:** https://github.com/niradler/mishmesh (public, branch main)
**Spec:** docs/superpowers/specs/2026-06-13-mishmesh-saas-design.md
**Module:** github.com/mishmesh/mishmesh · Go 1.26

## State: DEPLOYABLE HEADLESS PRODUCT ✅ (validated in Docker)
HTTP + TCP tunnels, subdomain + /tunnel/{id} routing, HTTPS (BYO + ACME),
management API (auth-gated), Docker images + compose, bootstrap token.
`go test -race ./...` all pass; `docker compose up` smoke verified.

## Done
- [x] MVP: WSS+yamux tunnel core, gateway, ingress (HTTP subdomain+path), agent, SQLite DataStore, in-mem ConnectionStore, CLIs
- [x] Control-plane management API: agents/tokens CRUD, rotate, revoke (live-kill), delete, orgs; bearer auth (MISHMESH_API_AUTH_TOKEN)
- [x] HTTPS ingress: BYO cert (wildcard) + ACME/autocert; HTTP/HTTPS listeners
- [x] TCP endpoints: dynamic per-endpoint listeners + port allocation (gateway PortOpener seam)
- [x] Docker (server+agent, distroless) + docker-compose demo + deploy/README.md + MISHMESH_BOOTSTRAP_TOKEN
- [x] CLAUDE.md, .gitattributes (LF)

## Remaining roadmap (each ~self-contained; pick up next session)
1. Quotas: per-org max agents/endpoints; enforce in controlplane.createAgent + gateway.handleRegister. Add Quota entity/table + DataStore methods.
2. TLS passthrough endpoints (kind=tls) + endpoint auth (basic/OIDC) at ingress (per-endpoint config; add to Endpoint + protocol + ingress).
3. Redis ConnectionStore (implement store.ConnectionStore) + Postgres DataStore (implement store.DataStore). Select via config (e.g. MISHMESH_CONN_BACKEND, MISHMESH_DATA_BACKEND / DSN scheme).
4. Prometheus metrics (/metrics): connected agents, streams, bytes, handshake failures. Wire through gateway/ingress; ServiceMonitor later.
5. Enterprise reach-in data-plane API: authed API on the control listener to open a stream to agent+target (TCP/HTTP), API-initiated (consumer of gateway.AgentConn), plus agent-side allowlist.
6. (Later, with UI) Google OAuth + password login + sessions; users/memberships/roles.

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
Context: mishmesh — deployable headless ngrok replacement (Go monorepo) is built, tested, dockerized, pushed to github.com/niradler/mishmesh.
State: see "Done" / "Remaining roadmap" above.
File: .claude/docs/mvp-build.md ; spec: docs/superpowers/specs/2026-06-13-mishmesh-saas-design.md
Next: implement roadmap item 1 (quotas) then 2-5; keep no-comments rule; commit+push per feature.
Issues: none. control/API listener (8081) must stay private or use MISHMESH_API_AUTH_TOKEN.
