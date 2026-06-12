# mishmesh MVP build — progress

**Goal:** Working end-to-end ngrok-style HTTP tunnel MVP. Lean, clean, extensible. WebUI deferred.
**Spec:** docs/superpowers/specs/2026-06-13-mishmesh-saas-design.md
**Module:** github.com/mishmesh/mishmesh · Go 1.26 · deps: coder/websocket, hashicorp/yamux, modernc.org/sqlite

## State: MVP COMPLETE ✅
End-to-end HTTP tunneling works live (real binaries) and under `go test -race ./...`:
public request → ingress (subdomain OR /tunnel/{id} path) → WSS+yamux tunnel → agent → local origin → back.

1. [x] Scaffold: git, go.mod, Makefile (GOEXE-aware), .gitignore, .golangci.yml
2. [x] config: env-var feature flags (binds 127.0.0.1 — Windows firewall)
3. [x] store: DataStore + ConnectionStore interfaces + entities
4. [x] store/sqlite (DataStore, modernc) + store/memory (ConnectionStore)
5. [x] tunnel: protocol (StreamInit, control msgs), yamux session, WSS transport
6. [x] gateway: agent WSS termination, token auth, control channel, registry, reserved-subdomain reuse
7. [x] agent: dial WSS, register endpoints, raw-bridge streams to local target, reconnect/backoff
8. [x] ingress: HTTP host/path router (path prefix stripped) -> tunnel proxy
9. [x] controlplane: minimal read API (agents/endpoints) + health
10. [x] cmd/mishmesh-server (serve + token create CLI) + cmd/mishmesh-agent (http CLI)
11. [x] e2e test + sqlite/ingress unit tests + README

## Style rules (memory)
- NO code comments, ever. Multi-line comments forbidden.
- Bind dev servers to 127.0.0.1 on Windows.

## Verified
- `go vet ./...` clean; `go test -race -count=1 ./...` all pass.
- Live smoke: `curl -H "Host: demo.localhost" http://127.0.0.1:8080/hello` -> `origin /hello`.

## Next (deferred, clean seams left) — pick up per sub-spec order
- Identity & tenancy: Google OAuth + password login (toggle), orgs/users/roles, API tokens, quotas enforcement.
- Public ingress expansion: custom domains + ACME/BYO TLS, TCP port allocation, TLS passthrough, endpoint auth (basic/OIDC).
- Web UI (React SPA) behind MISHMESH_WEBUI_ENABLED.
- Backends: Redis ConnectionStore, Postgres DataStore (implement existing interfaces).
- gRPC control/data-plane API; enterprise reach-in API-initiated data plane (consumer of gateway, no ingress).

## Next session prompt
Context: mishmesh MVP (ngrok-style tunnel SaaS, Go monorepo) is built and tested; control plane on Tunnelmesh core.
State: MVP complete; deferred items listed above.
File: .claude/docs/mvp-build.md ; spec: docs/superpowers/specs/2026-06-13-mishmesh-saas-design.md
Next: start the "Identity & tenancy" sub-spec (auth + orgs/users/roles) — brainstorm then implement.
Issues: none open. git initialized; not committed (commit when Nir asks).
