# Deploying mishmesh

This guide covers running `mishmesh-server` on a plain Docker host, plus the local compose setups in this directory. For Kubernetes, use the Helm charts in [helm/README.md](helm/README.md). For an overview of what mishmesh is, the three deployment shapes and the security model, start with the [root README](../README.md).

There are two images, built from this repo (none are published yet):

```bash
make docker docker-agent VERSION=0.1.0    # mishmesh-server:0.1.0, mishmesh-agent:0.1.0
```

The server image is distroless and runs as nonroot. It sets `MISHMESH_INGRESS_ADDR`, `HTTPS_ADDR`, `API_ADDR`, `TLS_PASSTHROUGH_ADDR`, `SSH_ADDR` and `TCP_BIND_HOST` to `0.0.0.0` (the container's own interfaces), sets `DATA_DSN=/data/mishmesh.db` and `ACME_CACHE_DIR=/data/certs`, and bundles the web UI at `/webui`. Which interfaces are reachable from outside is decided by your `-p` mappings.

Postgres is the data store for every real deployment. The SQLite default (`/data/mishmesh.db`) is a zero-dependency fallback for development only.

## Local demo

```bash
docker compose up -d --build            # from the repo root
curl -H 'Host: demo.localhost' http://127.0.0.1:8080/
docker compose down
```

See the [quickstart](../README.md#quickstart-5-minutes-docker). It runs with `MISHMESH_API_AUTH_DISABLED=true` and binds only to `127.0.0.1`.

## Single Docker host

### 1. DNS

Point the base domain and a wildcard at the host, so every endpoint subdomain resolves:

```text
tunnel.example.com     A   <server-ip>
*.tunnel.example.com   A   <server-ip>
```

Agents need a separate connect hostname (for example `connect.example.com`), served by a TLS proxy in front of the control listener. See step 5.

### 2. TLS for public ingress

Pick one:

- **Your own wildcard certificate.** Recommended when you have many ephemeral subdomains. Get a certificate for `tunnel.example.com` and `*.tunnel.example.com` (for example via DNS-01) and mount it.

  ```text
  MISHMESH_TLS_ENABLED=true
  MISHMESH_TLS_CERT_FILE=/data/certs/fullchain.pem
  MISHMESH_TLS_KEY_FILE=/data/certs/privkey.pem
  ```

- **ACME** (per-host certificates on demand). Good for the apex and a handful of custom domains. Mind Let's Encrypt rate limits if you have many ephemeral subdomains.

  ```text
  MISHMESH_TLS_ENABLED=true
  MISHMESH_ACME_ENABLED=true
  MISHMESH_ACME_EMAIL=ops@example.com
  ```

  ACME needs ports 80 and 443 reachable from the internet. Certificates are issued for the base domain, subdomains that belong to an existing endpoint (single label only) and custom domains: verified domains when `MISHMESH_DOMAIN_VERIFICATION` is on, otherwise domains bound to an endpoint (lookups are cached for 30 s). ACME runs on a single pod only, so clusters use a wildcard certificate.

### 3. Run Postgres and the server

```bash
docker network create mishmesh

docker run -d --name mishmesh-postgres --network mishmesh --restart unless-stopped \
  -e POSTGRES_USER=mishmesh -e POSTGRES_PASSWORD="$PG_PASSWORD" -e POSTGRES_DB=mishmesh \
  -v mishmesh-pg:/var/lib/postgresql/data \
  postgres:16-alpine

export MISHMESH_API_AUTH_TOKEN=$(openssl rand -hex 32)

docker run -d --name mishmesh-server --network mishmesh --restart unless-stopped \
  -p 80:8080 -p 443:8443 \
  -p 10000-10049:10000-10049 \
  -p 127.0.0.1:8081:8081 \
  -v mishmesh-data:/data \
  -e MISHMESH_DATA_DSN="postgres://mishmesh:$PG_PASSWORD@mishmesh-postgres:5432/mishmesh?sslmode=disable" \
  -e MISHMESH_API_AUTH_TOKEN \
  -e MISHMESH_BASE_DOMAIN=tunnel.example.com \
  -e MISHMESH_PUBLIC_SCHEME=https \
  -e MISHMESH_TLS_ENABLED=true \
  -e MISHMESH_TLS_CERT_FILE=/data/certs/fullchain.pem \
  -e MISHMESH_TLS_KEY_FILE=/data/certs/privkey.pem \
  -e MISHMESH_TCP_PORT_MIN=10000 -e MISHMESH_TCP_PORT_MAX=10049 \
  mishmesh-server:0.1.0
```

- Public: HTTP `80`, HTTPS `443`, and the TCP endpoint range. Keep `-p` and `TCP_PORT_MIN`/`TCP_PORT_MAX` identical. The binary's default range is 10000-10100.
- Private: the control listener `8081` (agent connect, `/api/v1`, web UI, `/metrics`) is published on `127.0.0.1` only.
- The server refuses to start without `MISHMESH_API_AUTH_TOKEN`, unless `MISHMESH_API_AUTH_DISABLED=true` is set explicitly. Use the latter only for local demos.
- Postgres is selected because the DSN starts with `postgres://` (`MISHMESH_DATA_BACKEND=postgres` does the same explicitly). For a managed database, use its DSN with `sslmode=require` and drop the Postgres container.
- `/data` holds certificates, the ACME cache and, if SSH is enabled, the SSH host key. Keep it on a volume. Without `MISHMESH_SSH_HOST_KEY_FILE` the host key is created next to the SQLite file, or, with a Postgres DSN, next to the ACME cache directory (`/data` in the image). If that directory is not writable the server stops at startup and names the path.

Check it:

```bash
curl -s http://127.0.0.1:8081/healthz
curl -s -H "Authorization: Bearer $MISHMESH_API_AUTH_TOKEN" http://127.0.0.1:8081/api/v1/status
```

### 4. Issue agent tokens

```bash
curl -s -XPOST http://127.0.0.1:8081/api/v1/agents \
  -H "Authorization: Bearer $MISHMESH_API_AUTH_TOKEN" \
  -d '{"name":"acme-dc1"}'
# {"agent":{"id":"ag_...",...},"token":"<shown once>"}
```

The token is shown only once. Alternatives: `docker exec mishmesh-server mishmesh-server token create --org default --name acme-dc1` (it uses the server's `MISHMESH_DATA_DSN`), or `MISHMESH_BOOTSTRAP_TOKEN` to seed one fixed token at startup.

### 5. Give agents a connect URL

The control listener speaks plain HTTP/WebSocket. It has no TLS of its own, and the API, web UI and metrics share it. Choose one:

- **Agents on the same private network or VPN** connect directly: `--gateway ws://<private-ip>:8081`. Publish `8081` on that private interface (`-p 10.0.0.10:8081:8081`), never on all interfaces.
- **Agents across the internet** go through a TLS-terminating reverse proxy on a separate hostname, which forwards **only** the connect path to `127.0.0.1:8081`, with WebSocket upgrade:

  ```nginx
  server {
      listen 443 ssl;
      server_name connect.example.com;
      ssl_certificate     /etc/ssl/connect.pem;
      ssl_certificate_key /etc/ssl/connect-key.pem;

      location = /_mishmesh/agent/connect {
          proxy_pass http://127.0.0.1:8081;
          proxy_http_version 1.1;
          proxy_set_header Upgrade $http_upgrade;
          proxy_set_header Connection "upgrade";
          proxy_read_timeout 1h;
      }
      location / { return 404; }
  }
  ```

  Because the mishmesh container already owns `443` on the host, run the proxy on a second IP address or on your load balancer. The Helm chart's `connectIngress` sets up the same thing on Kubernetes.

### 6. Run an agent on the private network

```bash
MISHMESH_TOKEN=<token> mishmesh-agent http 3000 --subdomain app --gateway wss://connect.example.com
#   -> https://app.tunnel.example.com
MISHMESH_TOKEN=<token> mishmesh-agent tcp 22 --gateway wss://connect.example.com
#   -> tcp://tunnel.example.com:100xx
```

Or as a container:

```bash
docker run -d --name mishmesh-agent --restart unless-stopped \
  -e MISHMESH_TOKEN=<token> -e MISHMESH_GATEWAY_URL=wss://connect.example.com \
  mishmesh-agent:0.1.0 http host.docker.internal:3000 --subdomain app
```

For many tunnels, a config file and `mishmesh-agent service install`, see [Agent](../README.md#agent).

### 7. Web UI and login (optional)

Set `MISHMESH_WEBUI_ENABLED=true` and `MISHMESH_AUTH_ENABLED=true`. The UI is served from the control listener (same origin as `/api/v1`), so put it behind TLS too, for example on another `server_name` in the proxy above, routed to all of `127.0.0.1:8081`. The UI authenticates with a session cookie, not the bearer token. Choose who can register with `MISHMESH_SIGNUP_MODE`: `invite` for a company (the first registration owns the default org) or `org` for public signup. When the control host sits behind a proxy, set `MISHMESH_TRUSTED_PROXIES` to the proxy's address so per-IP login throttling sees real clients.

## Key environment variables

The full list with defaults is in the [root README](../README.md#server-configuration).

| Var | Purpose |
| --- | --- |
| `MISHMESH_BASE_DOMAIN` | public host suffix for URLs (`tunnel.example.com`) |
| `MISHMESH_PUBLIC_SCHEME` | `https` in production; also marks the session cookie `Secure` |
| `MISHMESH_DATA_BACKEND` / `MISHMESH_DATA_DSN` | `postgres` (inferred from a `postgres://` DSN). `sqlite` with a file path for development only |
| `MISHMESH_CONN_BACKEND` / `MISHMESH_REDIS_URL` | `memory` (default) or `redis`, required for cluster mode |
| `MISHMESH_API_AUTH_TOKEN` | admin bearer token for `/api/v1/*`. Health checks stay open |
| `MISHMESH_API_AUTH_DISABLED` | explicit opt-out for local demos. The server refuses to start if neither this nor `API_AUTH_TOKEN` is set |
| `MISHMESH_BOOTSTRAP_TOKEN` | seed one fixed agent token at startup |
| `MISHMESH_INGRESS_ADDR` / `HTTPS_ADDR` / `API_ADDR` | listen addresses (`0.0.0.0:*` in the image, `127.0.0.1:*` for the binary) |
| `MISHMESH_TLS_ENABLED` + cert or ACME vars | HTTPS ingress |
| `MISHMESH_SELF_SIGNED_TLS` | in-memory self-signed cert for the apex and wildcard (dev) |
| `MISHMESH_TCP_ENABLED` / `TCP_PORT_MIN` / `TCP_PORT_MAX` | public TCP endpoint range |
| `MISHMESH_TLS_PASSTHROUGH_ENABLED` / `TLS_PASSTHROUGH_ADDR` | SNI-routed passthrough listener for `tls` endpoints (default port 8444) |
| `MISHMESH_SSH_ENABLED` / `SSH_ADDR` / `SSH_HOST_KEY_FILE` | clientless `ssh -R` front door |
| `MISHMESH_AUTH_ENABLED` / `AUTH_PASSWORD_ENABLED` | browser login; password on/off (off means Google only) |
| `MISHMESH_SIGNUP_MODE` | `org` (default, every signup gets an org) or `invite` |
| `MISHMESH_DOMAIN_VERIFICATION` | require DNS TXT proof for custom domains (default on in `org` mode) |
| `MISHMESH_TRUSTED_PROXIES` | IPs/CIDRs whose `X-Forwarded-For` is trusted for client IP |
| `MISHMESH_WEBUI_ENABLED` / `WEBUI_DIR` | serve the React SPA (the image bundles it at `/webui`) |
| `MISHMESH_GOOGLE_CLIENT_ID` / `GOOGLE_CLIENT_SECRET` / `OIDC_REDIRECT_URL` | Google login |
| `MISHMESH_METRICS_ENABLED` / `METRICS_TOKEN` | Prometheus `/metrics` on the control listener. The bearer is `METRICS_TOKEN`, or the API token if that is unset |
| `MISHMESH_REACHIN_ENABLED` | reach-in data-plane API |
| `MISHMESH_QUOTA_MAX_AGENTS` / `_MAX_ENDPOINTS` / `_MAX_BANDWIDTH_BYTES` | default per-org quotas (0 = unlimited) |

Agent reach-in allowlist: `MISHMESH_ALLOW`, `--allow` or `allow:` in the agent config (deny-first, comma-separated `host|cidr[:port;port]`). Loopback, link-local and cloud-metadata IPs are always denied.

## Connectivity methods

Besides the native agent tunnel, endpoints carry a `method`: `native | ssh | proxy | tailscale | cloudflare`.

### Clientless SSH remote-forward (no install)

A stock-SSH front door: users expose a service with the `ssh` client already on their machine, with no mishmesh agent.

```text
MISHMESH_SSH_ENABLED=true
MISHMESH_SSH_ADDR=0.0.0.0:2222                       # image; the binary defaults to 127.0.0.1:2222
MISHMESH_SSH_HOST_KEY_FILE=/data/ssh_host_ed25519.pem
```

Always set `SSH_HOST_KEY_FILE` to a path on a persistent, writable volume. The key is generated on first start and reused after that. With a SQLite DSN the default is next to the database file. With a Postgres DSN the default is `ssh_host_ed25519.pem` in the working directory, which is not writable in the distroless image, so the server fails to start. Publish the port with `-p 2222:2222`.

```bash
# password = an agent token (POST /api/v1/agents); the SSH username becomes the subdomain.
ssh -N -R 80:localhost:3000 myapp@tunnel.example.com -p 2222
#   -> https://myapp.tunnel.example.com   (HTTP, method=ssh)
# bind ports other than 80 allocate a public TCP port (needs TCP ingress enabled).
```

The reverse forward becomes a normal mishmesh endpoint, so routing, policy, TLS, quota and metering apply unchanged.

### Agentless proxy

For a target the server can already reach, create a `method=proxy` endpoint. mishmesh reverse-proxies it directly, with managed DNS, TLS and policy and no agent:

```bash
curl -XPOST http://127.0.0.1:8081/api/v1/endpoints \
  -H "Authorization: Bearer $MISHMESH_API_AUTH_TOKEN" \
  -d '{"kind":"http","method":"proxy","subdomain":"internal","policy":{"proxy_target":"10.0.0.5:8080"}}'
```

Targets that resolve to cloud-metadata, link-local, multicast or unspecified addresses are always refused. Loopback, private/LAN (RFC1918, CGNAT, ULA) addresses and internal names (`*.svc`, `*.cluster.local`, single-label) are refused by default too, because any tenant can create proxy endpoints. The target is checked when the endpoint is saved and again when dialling, and the checked IP is pinned for the dial, so DNS rebinding does not work. To proxy into your own network, set `MISHMESH_PROXY_ALLOWED_CIDRS=10.0.0.0/8` (preferred) or `MISHMESH_PROXY_ALLOW_PRIVATE=true`; use `MISHMESH_PROXY_ALLOW_LOOPBACK=true` only to reach the server's own loopback. The example above needs one of these opt-ins.

### mTLS at the edge

Require client certificates per endpoint (HTTPS ingress only) with the endpoint policy:

```json
{"mtls": {"client_ca_pem": "-----BEGIN CERTIFICATE-----\n...", "allowed_cns": ["svc-a"]}}
```

A request without a certificate that chains to `client_ca_pem` (and matches `allowed_cns`, if set) gets 403.

### Managed Tailscale / Cloudflare

These are orchestrate-only methods: mishmesh would provision the provider's resources and traffic would flow over the provider. They are scaffolded behind the `method` field but need live provider API credentials, and they are not enabled in this build.

## Cluster mode (multiple identical pods)

Run N identical `mishmesh-server` replicas behind a load balancer. Any pod can accept an agent, and any pod can serve ingress for that agent. When the agent is on another pod, the request is relayed pod to pod over an authenticated TCP link and spliced into the agent's tunnel. This covers HTTP, TCP, TLS passthrough, SSH, proxy and reach-in. Routing, policy, quotas and metering are unchanged. When cluster mode is off, it is not wired in at all.

Requirements: `MISHMESH_CONN_BACKEND=redis` with `MISHMESH_REDIS_URL`, and a shared Postgres. The server refuses to start in cluster mode with SQLite, the in-memory connection store, or a missing relay setting.

| Var | Purpose |
| --- | --- |
| `MISHMESH_CLUSTER_ENABLED` | turn cluster mode on (default `false`) |
| `MISHMESH_NODE_ID` | unique pod identity (defaults to the hostname) |
| `MISHMESH_RELAY_ADDR` | pod-to-pod relay listener (default `127.0.0.1:7443`; `0.0.0.0:7443` in a container) |
| `MISHMESH_RELAY_ADVERTISE` | `host:port` that other pods dial to reach this pod (e.g. `$(POD_IP):7443`); required |
| `MISHMESH_CLUSTER_SECRET` | shared HMAC key for the relay, at least 32 characters; required |

How it works:

- Each agent session is owned by one pod, recorded in Redis (`mm:agent:{id}`) with a TTL that the owner refreshes (every ttl/3) and reclaims if it lapses. Removal is compare-and-delete, so a stale pod cannot evict a newer owner.
- To resolve an endpoint, a pod tries itself first, then Redis, then opens a relay stream to the owner. The relay header is `{agent_id, endpoint_id, kind, meta, ts, mac}`. It is HMAC-SHA256 signed and checked with a constant-time compare, a ±60 s skew window and a size cap. The owner replies with a status byte (ok, not-here or error) and then splices bytes.
- When an agent reconnects to a different pod, the stale session is kicked cluster-wide over Redis pub/sub (`mm:kick`). Cleanup on the stale pod does not delete endpoints that the new owner still holds.
- TCP: every pod pre-binds the whole `TCP_PORT_MIN..TCP_PORT_MAX` range, and ports are claimed cluster-wide in Redis (`SET mm:port:{p} {endpointID} NX`), so a public TCP port works on any pod. Size the range with the replica count in mind, because each pod holds every port in it.
- Rate-limit buckets and the login throttle live in Redis too, so limits hold across pods.
- Shutdown: on SIGTERM the pod flips `/readyz` to 503 so the load balancer stops sending it traffic. It then drains and closes listeners within a bounded time (about 10 s for servers plus 5 s for the cluster). `/healthz` stays 200. Agents reconnect with jittered exponential backoff, so a rolling restart does not stampede the surviving pods.

Security: only other pods should be able to reach the relay port (a private network or NetworkPolicy). The HMAC secret authenticates callers, but the link is **not encrypted**. Keep it on a trusted network or a service mesh with mTLS.

Try it locally (everything bound to 127.0.0.1):

```bash
cd deploy
docker compose -p mishmesh-cluster -f compose.cluster.yml up -d --build postgres redis server-a server-b echo
HTTP_TOKEN=$(curl -s -XPOST 127.0.0.1:18081/api/v1/agents -d '{"name":"http"}' | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')
TCP_TOKEN=$(curl -s -XPOST 127.0.0.1:28081/api/v1/agents -d '{"name":"tcp"}'  | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')
HTTP_TOKEN=$HTTP_TOKEN TCP_TOKEN=$TCP_TOKEN docker compose -p mishmesh-cluster -f compose.cluster.yml up -d agent-http agent-tcp

curl -H "Host: demo.localhost" http://127.0.0.1:18080/   # replica A (owns the agent)
curl -H "Host: demo.localhost" http://127.0.0.1:28080/   # replica B (relayed to A)
curl http://127.0.0.1:19000/                             # TCP via A
curl http://127.0.0.1:29000/                             # TCP via B
docker compose -p mishmesh-cluster -f compose.cluster.yml down -v
```

The demo compose uses a throwaway `MISHMESH_CLUSTER_SECRET` and disables API auth. Change both for real use. On Kubernetes, `cluster.enabled=true` in the Helm chart wires all of this, including the pod IP and a NetworkPolicy for the relay port.

## Live network e2e (isolated Docker networks)

`deploy/compose.e2e.yml` proves real tunnels across isolated networks. An `internal` `private` network holds the backend (`echo`) and the agents, with no route to the host or to `edge`. The `edge` network holds the server and a `tester`. Traffic reaches the private backend **only** through the tunnel. Host ports are bound to 127.0.0.1 and API auth is disabled for the test.

```bash
cd deploy
docker compose -f compose.e2e.yml up -d --build server echo tester
HTTP_TOKEN=$(curl -s -XPOST 127.0.0.1:18081/api/v1/agents -d '{"name":"http"}' | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')
TCP_TOKEN=$(curl -s -XPOST 127.0.0.1:18081/api/v1/agents -d '{"name":"tcp"}'  | sed -n 's/.*"token":"\([^"]*\)".*/\1/p')
HTTP_TOKEN=$HTTP_TOKEN TCP_TOKEN=$TCP_TOKEN docker compose -f compose.e2e.yml up -d agent-http agent-tcp

docker compose -f compose.e2e.yml exec -T tester curl -s -H "Host: demo.localhost" http://server:8080/   # HTTP tunnel
docker compose -f compose.e2e.yml exec -T tester curl -s http://server:10000/                            # TCP tunnel
docker compose -f compose.e2e.yml exec -T tester curl -m5 http://echo:8080/                              # must fail
docker compose -f compose.e2e.yml down
```

HTTP and TCP use two agents because each agent identity holds its own set of tunnels. The WebSocket upgrade path also has an in-process regression test: `go test ./internal/e2e -run WebSocket`.

`compose.ssh.yml` (clientless SSH) and `compose.perf.yml` (throughput) follow the same pattern on ports 38080/38081/2222 and 28080/28081/20000-20001, also bound to 127.0.0.1.

## Operational notes

- Back up Postgres. `/data` holds certificates, the ACME cache and the SSH host key, so keep it on a volume.
- The control port (`8081`) serves agent connects, the API, the UI and metrics. Never publish it on all interfaces. Expose only `/_mishmesh/agent/connect` through TLS, and the UI behind TLS if you use it.
- Single node: the in-memory connection store is enough. For several nodes, use cluster mode. Running several nodes with Redis but without cluster mode only shares usage and presence, and an agent is then reachable only through the node it is connected to.
- On restart, the server removes ephemeral endpoints whose agent is not connected anywhere. Reserved endpoints stay and come back online when their agent reconnects.
