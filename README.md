# mishmesh

A SaaS tunnel platform for exposing private networks — comparable to ngrok / Cloudflare Tunnel — built on top of the Tunnelmesh tunnel core (see [prd.md](prd.md)).

One product, two use cases:

1. **Enterprise headless gateway** — reach *into* a customer's private network (API-initiated, no public URLs).
2. **Multi-tenant SaaS** — users expose private services to *public* URLs (ngrok-style).

> **Status:** MVP. End-to-end HTTP tunneling works (subdomain + path routing). Web UI, Google/password auth, custom domains/ACME, TCP/TLS endpoints, Redis/Postgres backends, and quota enforcement are designed-for but not yet built. See the design doc: [docs/superpowers/specs/2026-06-13-mishmesh-saas-design.md](docs/superpowers/specs/2026-06-13-mishmesh-saas-design.md).

## Architecture

```
public client ──HTTP──► ingress (host/path router)
                          │  resolve endpoint by subdomain or /tunnel/{id}
                          ▼
                        ConnectionStore: endpoint → live agent session
                          │  open a labeled stream
                          ▼ (yamux over WSS)
                        agent: accept stream → bridge to local target
                          ▼
                        local service (e.g. 127.0.0.1:3000)
```

- **gateway** — agent-facing: terminates agent WSS connections, authenticates agent tokens, runs the multiplexed tunnel, registers endpoints, tracks live sessions. Its only export is "open a stream to agent X."
- **ingress** — public-facing: routes inbound public HTTP by subdomain (`abc.host`) or path (`host/tunnel/{id}`), then reverse-proxies through the tunnel. Optional (`MISHMESH_INGRESS_ENABLED=false` for the enterprise reach-in case).
- **tunnel** — the shared module both the server and agent build on: WebSocket transport + yamux multiplexing + the wire protocol.
- **store** — two pluggable interfaces: `DataStore` (durable; SQLite now, Postgres later) and `ConnectionStore` (ephemeral; in-memory now, Redis later).

## Layout

```
cmd/mishmesh-server   server binary + seed CLI
cmd/mishmesh-agent    tunnel client CLI
internal/tunnel       protocol, session, WSS transport (shared)
internal/gateway      agent termination, control channel, registry
internal/agent        client: dial, register endpoints, serve streams
internal/ingress      public HTTP router → tunnel proxy
internal/controlplane minimal REST API (agents/endpoints, health)
internal/store        DataStore + ConnectionStore interfaces + entities
internal/store/sqlite SQLite DataStore
internal/store/memory in-memory ConnectionStore
internal/config       env-var feature flags + config
```

## Quickstart

```bash
make build

./bin/mishmesh-server token create --org demo
# prints: org_id, agent_id, and a one-time token

# terminal 1 — run a local service to expose
python -m http.server 3000

# terminal 2 — run the server
MISHMESH_BASE_DOMAIN=localhost:8080 ./bin/mishmesh-server

# terminal 3 — run the agent
MISHMESH_TOKEN=<token> ./bin/mishmesh-agent http 3000 --subdomain demo

# reach the local service through the tunnel
curl -H "Host: demo.localhost" http://127.0.0.1:8080/
curl http://127.0.0.1:8080/tunnel/<endpoint_id>/
```

`*.localhost` resolves to loopback on most systems; for a real deployment point a wildcard DNS record and `MISHMESH_BASE_DOMAIN` at the ingress.

## Configuration

Server (env, prefix `MISHMESH_`):

| Var | Default | Meaning |
| --- | --- | --- |
| `INGRESS_ADDR` | `127.0.0.1:8080` | public traffic listener |
| `API_ADDR` | `127.0.0.1:8081` | agent WSS + control API |
| `BASE_DOMAIN` | `localhost:8080` | suffix for public URLs |
| `PUBLIC_SCHEME` | `http` | `http` or `https` |
| `DATA_DSN` | `mishmesh.db` | SQLite path |
| `AUTH_ENABLED` | `false` | user login (web) |
| `AUTH_PASSWORD_ENABLED` | `true` | allow password login |
| `WEBUI_ENABLED` | `false` | serve the SPA |
| `INGRESS_ENABLED` | `true` | enable public ingress |
| `LOG_LEVEL` | `info` | slog level |

Agent: `GATEWAY_URL` (default `ws://localhost:8081`), `TOKEN`, `LOG_LEVEL` — overridable by `--gateway` / `--token`.

## Development

```bash
make check   # fmt + vet + test (race)
make test
make lint    # if golangci-lint is installed
```
