# CLAUDE.md — mishmesh

Project guidance for Claude when working in this repo. These instructions override defaults.

## What this is

mishmesh is a SaaS tunnel platform for exposing private networks (ngrok / Cloudflare Tunnel style), in Go, layered on the Tunnelmesh tunnel core (`prd.md`). One product, two use cases: enterprise headless gateway (reach into a private network) and multi-tenant SaaS (expose private services to public URLs). Monorepo, single Go module.

## Hard rules (do not violate)

- **No code comments. Ever.** Multi-line comments are absolutely forbidden. Self-document via naming. (Applies to Go and every language here.)
- **Bind listeners to `127.0.0.1`**, never `0.0.0.0` / `:port` — the Windows dev firewall blocks public binds. Keep bind address separate from public-URL/base-domain config.
- **No AI attribution** in commits/PRs (no "Generated with", no "Co-Authored-By").
- Every shell command gets an explicit timeout.

## Conventions

- Go best practices: `cmd/` binaries, `internal/` packages, interfaces at boundaries, `log/slog`, `context` first arg, wrap errors with `%w`, table-driven tests.
- Two pluggable store interfaces — keep backends swappable behind them:
  - `store.DataStore` — durable (SQLite default; Postgres planned).
  - `store.ConnectionStore` — ephemeral live sessions (in-memory default; Redis planned).
- The gateway's only export to consumers is `store.AgentConn.OpenStream(...)`. Ingress and the (future) enterprise data-plane API are *consumers* of the gateway, not part of it.
- `internal/tunnel` is the shared module both binaries import (WSS transport + yamux mux + wire protocol). Keep it protocol-agnostic.
- Features are gated by `MISHMESH_*` env flags; disabled = not wired in, not dead branches.

## Commands

```bash
make build        # bin/mishmesh-server(.exe), bin/mishmesh-agent(.exe)
make check        # fmt + vet + test (race)  — run before committing
make test         # go test -race ./...
```

## Layout

`cmd/mishmesh-server` · `cmd/mishmesh-agent` · `internal/{tunnel,gateway,agent,ingress,controlplane,store,store/sqlite,store/memory,config}`.

## Status & roadmap

MVP done: end-to-end HTTP tunneling (subdomain + `/tunnel/{id}` path routing). See `.claude/docs/mvp-build.md` and `docs/superpowers/specs/2026-06-13-mishmesh-saas-design.md`.

Building now (headless, no web UI): agent-management API + token CRUD, identity & tenancy (password + Google OIDC, orgs/users/roles), quotas, ingress expansion (TCP ports, HTTPS/ACME + custom domains, TLS passthrough, endpoint auth), Redis + Postgres backends, Prometheus metrics, enterprise reach-in data-plane API.

Deferred: React web UI.
