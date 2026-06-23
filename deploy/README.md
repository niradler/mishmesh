# Deploying mishmesh

mishmesh ships two images: `mishmesh-server` (gateway + ingress + control API + web UI) and `mishmesh-agent` (tunnel client). This guide covers a cloud deployment. Disabled features are simply not wired in, so the same image runs as a headless enterprise gateway (`AUTH=off, WEBUI=off`) or a full multi-tenant SaaS (`AUTH=on, WEBUI=on`).

Capabilities: HTTP (subdomain / path / custom domain) + WebSocket/streaming, TCP, TLS passthrough; HTTPS via BYO cert, ACME, or self-signed; per-endpoint policy (header rewrite, host/path, basic-auth, IP allow/deny, force-https, compression); quotas + bandwidth; Prometheus `/metrics`; SQLite/Postgres + in-mem/Redis backends; enterprise reach-in API with an agent allowlist; password + Google-OIDC login with org/role RBAC; and a React web UI.

## Local demo (one command)

```bash
docker compose up --build
# server seeds a bootstrap token; the agent exposes the `whoami` service.
curl -H "Host: demo.localhost" http://localhost:8080/
```

## Cloud deployment

### 1. DNS

Point a wildcard record at the server's public IP so every endpoint subdomain resolves:

```
*.tunnel.example.com   A   <server-ip>
tunnel.example.com     A   <server-ip>
```

### 2. TLS

Two options:

- **BYO wildcard cert (recommended for many ephemeral subdomains):** obtain a cert for `*.tunnel.example.com` (e.g. via DNS-01) and mount it.
  ```
  MISHMESH_TLS_ENABLED=true
  MISHMESH_TLS_CERT_FILE=/data/certs/fullchain.pem
  MISHMESH_TLS_KEY_FILE=/data/certs/privkey.pem
  ```
- **ACME/autocert (on-demand per host):** good for the apex and a handful of custom domains; beware Let's Encrypt rate limits with many ephemeral subdomains.
  ```
  MISHMESH_TLS_ENABLED=true
  MISHMESH_ACME_ENABLED=true
  MISHMESH_ACME_EMAIL=ops@example.com
  ```
  ACME needs ports 80 and 443 publicly reachable.

### 3. Run the server

```bash
docker run -d --name mishmesh-server \
  -p 80:8080 -p 443:8443 -p 8081:8081 -p 10000-10100:10000-10100 \
  -v mishmesh-data:/data \
  -e MISHMESH_BASE_DOMAIN=tunnel.example.com \
  -e MISHMESH_PUBLIC_SCHEME=https \
  -e MISHMESH_TLS_ENABLED=true \
  -e MISHMESH_TLS_CERT_FILE=/data/certs/fullchain.pem \
  -e MISHMESH_TLS_KEY_FILE=/data/certs/privkey.pem \
  -e MISHMESH_BOOTSTRAP_TOKEN=$(openssl rand -hex 24) \
  mishmesh-server:latest
```

The container binds `0.0.0.0` by default. The API/control listener (`8081`) should NOT be exposed publicly — keep it on a private network or behind auth.

### 4. Issue agent tokens

Either set `MISHMESH_BOOTSTRAP_TOKEN` (idempotent single token), or create per-agent tokens:

```bash
docker exec mishmesh-server mishmesh-server token create --org acme
# or via the control API (internal listener):
curl -X POST http://127.0.0.1:8081/api/v1/agents -d '{"name":"acme-dc1"}'
```

### 5. Run an agent (on the private network)

```bash
MISHMESH_TOKEN=<token> mishmesh-agent http 3000 --subdomain app --gateway wss://tunnel.example.com:8081
# or TCP:
MISHMESH_TOKEN=<token> mishmesh-agent tcp 22 --gateway wss://tunnel.example.com:8081
```

## Key environment variables

| Var | Purpose |
| --- | --- |
| `MISHMESH_BASE_DOMAIN` | public host suffix for URLs (e.g. `tunnel.example.com`) |
| `MISHMESH_PUBLIC_SCHEME` | `https` in production |
| `MISHMESH_INGRESS_ADDR` / `MISHMESH_HTTPS_ADDR` / `MISHMESH_API_ADDR` | bind addresses (default `0.0.0.0:*` in the image) |
| `MISHMESH_TLS_ENABLED` + cert/ACME vars | enable HTTPS ingress |
| `MISHMESH_TCP_ENABLED` / `MISHMESH_TCP_PORT_MIN` / `MISHMESH_TCP_PORT_MAX` | public TCP endpoint range |
| `MISHMESH_DATA_DSN` | SQLite path (default `/data/mishmesh.db`) |
| `MISHMESH_BOOTSTRAP_TOKEN` | seed a fixed agent token on startup |
| `MISHMESH_API_AUTH_TOKEN` | require this bearer token on the control/management API (`/api/v1/*`); health stays open |
| `MISHMESH_API_AUTH_DISABLED` | explicit opt-out: run the control API without auth. The server refuses to start if neither this nor `API_AUTH_TOKEN` is set (fail-closed). |
| `MISHMESH_SELF_SIGNED_TLS` | mint an in-memory self-signed cert for the apex + wildcard (dev/local TLS) when no BYO/ACME cert is set |
| `MISHMESH_TLS_PASSTHROUGH_ENABLED` / `MISHMESH_TLS_PASSTHROUGH_ADDR` | SNI-routed TLS passthrough listener for `kind=tls` endpoints |
| `MISHMESH_SSH_ENABLED` / `MISHMESH_SSH_ADDR` / `MISHMESH_SSH_HOST_KEY_FILE` | clientless SSH remote-forward front door (stock `ssh -R`); host key persisted if set |
| `MISHMESH_AUTH_ENABLED` / `MISHMESH_AUTH_PASSWORD_ENABLED` | require browser login; toggle password auth (off ⇒ Google-only) |
| `MISHMESH_WEBUI_ENABLED` / `MISHMESH_WEBUI_DIR` | serve the React SPA (image bundles it at `/webui`) |
| `MISHMESH_GOOGLE_CLIENT_ID` / `MISHMESH_GOOGLE_CLIENT_SECRET` / `MISHMESH_OIDC_REDIRECT_URL` | Google OIDC login |
| `MISHMESH_DATA_BACKEND` / `MISHMESH_DATA_DSN` | `sqlite` (default) or `postgres` (e.g. `postgres://user:pw@host/db`) |
| `MISHMESH_CONN_BACKEND` / `MISHMESH_REDIS_URL` | `memory` (default) or `redis` (shared usage/presence) |
| `MISHMESH_METRICS_ENABLED` | expose Prometheus `/metrics` on the control listener |
| `MISHMESH_REACHIN_ENABLED` | enable the enterprise reach-in data-plane API |
| `MISHMESH_QUOTA_MAX_AGENTS` / `_MAX_ENDPOINTS` / `_MAX_BANDWIDTH_BYTES` | default per-org quotas (0 = unlimited) |

Agent reach-in allowlist: `MISHMESH_ALLOW` / `--allow` (deny-first, comma-separated `host|cidr[:port;port]`). Loopback, link-local, and cloud-metadata IPs are always hard-denied.

## Connectivity methods

Beyond the native agent tunnel, endpoints carry a `method` (`native | ssh | proxy | tailscale | cloudflare`).

### Clientless SSH remote-forward (no install)

Enable a stock-SSH front door — users expose a service with the `ssh` already on their machine, no
mishmesh agent:

```
MISHMESH_SSH_ENABLED=true
MISHMESH_SSH_ADDR=0.0.0.0:2222        # default 127.0.0.1:2222
MISHMESH_SSH_HOST_KEY_FILE=/data/ssh_host_ed25519   # optional; generated in-memory if unset
```

```bash
# password = an agent token (POST /api/v1/agents); the SSH username becomes the subdomain.
ssh -N -R 80:localhost:3000 myapp@tunnel.example.com -p 2222
#   -> https://myapp.tunnel.example.com   (HTTP, method=ssh)
# non-80 bind ports allocate a public TCP port (needs TCP ingress enabled).
```

The reverse forward maps to a normal mishmesh Endpoint, so all routing, policy, TLS, quota, and metering
apply unchanged. Persist the host key file so the server identity is stable across restarts.

### Agentless proxy

For a target mishmesh can already reach, create a `method=proxy` endpoint — mishmesh reverse-proxies it
directly (managed DNS/TLS/policy, no agent, no tunnel):

```bash
curl -X POST http://127.0.0.1:8081/api/v1/endpoints \
  -d '{"method":"proxy","subdomain":"internal","policy":{"proxy_target":"10.0.0.5:8080"}}'
```

Targets resolving to cloud-metadata, loopback, link-local, multicast, or unspecified addresses are
refused, and the resolved IP is pinned for the dial (no DNS-rebinding). Private/LAN ranges are allowed —
that is the method's purpose. Set `MISHMESH_PROXY_ALLOW_LOOPBACK=true` only if you must proxy to the
server's own loopback.

### mTLS at the edge

Require client certificates per endpoint (HTTPS ingress only). Add to an endpoint's policy:

```json
{"mtls": {"client_ca_pem": "-----BEGIN CERTIFICATE-----\n...", "allowed_cns": ["svc-a"]}}
```

Requests without a certificate chaining to `client_ca_pem` (and matching `allowed_cns`, if set) get 403.

### Managed Tailscale / Cloudflare

Orchestrate-only methods (mishmesh provisions provider resources, traffic flows over the provider) are
scaffolded behind the `method` field but require live provider API credentials; not enabled in this build.

## Web UI

With `MISHMESH_WEBUI_ENABLED=true` the SPA is served from the control listener (same origin as `/api/v1`). Browse to `http://<api-host>:8081/`. Put the control listener behind TLS / a reverse proxy in production; the session cookie is `Secure` when `PUBLIC_SCHEME=https`.

## Live network e2e (isolated Docker networks)

`deploy/compose.e2e.yml` proves real tunnels across isolated networks: an `internal` `private`
network holds the backend (`echo`) + agents with no route to the host/`edge`; the `edge` network
holds the server + a `tester`. Traffic reaches the private backend **only** through the tunnel.

```bash
cd deploy
docker compose -f compose.e2e.yml up -d --build server echo tester
# mint two agent tokens (HTTP + TCP need distinct agent identities), then start agents:
HTTP_TOKEN=$(curl -s -XPOST localhost:18081/api/v1/agents -d '{"name":"http"}' | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')
TCP_TOKEN=$(curl -s -XPOST localhost:18081/api/v1/agents -d '{"name":"tcp"}'  | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')
HTTP_TOKEN=$HTTP_TOKEN TCP_TOKEN=$TCP_TOKEN docker compose -f compose.e2e.yml up -d agent-http agent-tcp

# HTTP tunnel:  docker compose -f compose.e2e.yml exec -T tester curl -s -H "Host: demo.localhost" http://server:8080/
# TCP tunnel:   docker compose -f compose.e2e.yml exec -T tester curl -s http://server:10000/
# isolation:    docker compose -f compose.e2e.yml exec -T tester curl -m5 http://echo:8080/   # must fail
docker compose -f compose.e2e.yml down
```

The WebSocket upgrade path also has an in-process regression test: `go test ./internal/e2e -run WebSocket`.

## Notes

- Persist `/data` (SQLite + ACME cache) on a volume.
- The control/API port (`8081`) is the agent connect + management + UI surface — restrict it or front it with TLS.
- Multi-node: select `postgres` + `redis` backends. Redis shares per-org usage/presence; cross-pod stream routing (forwarding ingress to the pod owning an agent) is not yet implemented, so route agents/ingress with session affinity per node for now.
