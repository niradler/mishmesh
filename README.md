<div align="center">

<img src="web/public/mishmesh-apricot-v1.svg" alt="mishmesh logo" width="96" height="96">

# mishmesh

**Self-hosted tunnels into private networks. Expose services publicly, or reach in from your own control plane.**

[![Release](https://img.shields.io/github/v/release/niradler/mishmesh?sort=semver)](https://github.com/niradler/mishmesh/releases)
[![Go](https://img.shields.io/github/go-mod/go-version/niradler/mishmesh)](go.mod)
[![License](https://img.shields.io/github/license/niradler/mishmesh)](LICENSE)

[Quickstart](#quickstart) · [Install](#install) · [Agent](#agent) · [Deploy](#deployment-shapes) · [API](docs/api.md) · [Configuration](#server-configuration)

</div>

mishmesh is a transport layer for services that live behind NAT, firewalls or corporate proxies, where only outbound connections are allowed. A small agent inside the private network dials out to the mishmesh server over WebSocket (WSS), and every connection after that is multiplexed back through that single outbound tunnel.

Deploy it once and reuse it for every product, tenant or customer site, instead of building a custom tunnel each time. It works in two directions:

- **Expose**: publish a private service on a public URL (`https://app.tunnel.example.com`), a public TCP or UDP port, or a TLS-passthrough hostname, in the style of ngrok.
- **Reach in**: let your control plane call internal services through a customer's agent over an authenticated API, as HTTP requests or raw TCP streams. No public URL is created.

One Go module builds two binaries: `mishmesh-server` (gateway, public ingress, control API, web UI) and `mishmesh-agent` (the tunnel client).

## Features

- **Every protocol path**: HTTP/HTTPS by subdomain, custom domain or `/tunnel/{id}` path, WebSocket/SSE and gRPC streaming, public TCP and UDP ports, TLS passthrough by SNI, clientless `ssh -R`, and agentless proxy endpoints.
- **Agent-to-agent connects**: private TCP/UDP tunnels with no public port, reachable only by agents you allow in the same org.
- **Reach-in API**: HTTP and raw-stream access to internal targets, gated by a deny-first allowlist on the agent.
- **Egress**: an agent can act as a forward proxy, with every connection decided by the org's Cedar policy ([docs/egress.md](docs/egress.md)).
- **Endpoint policy**: header and path rewrites, basic auth, IP allow/deny, OIDC sign-in, mTLS and rate limits.
- **Multi-tenant control plane**: orgs, roles, per-org Cedar policy, invites, password and Google login, quotas, audit log and a web UI.
- **Pluggable app access**: publish HTTP apps through Cloudflare Tunnel or as private HTTPS over Tailscale Services ([docs/connections.md](docs/connections.md)).
- **Services that sleep when idle**: Docker, Compose or command services woken on demand by HTTP traffic ([docs/services.md](docs/services.md)).
- **Horizontal scaling**: identical pods behind a load balancer, Postgres and Redis for shared state, a pod-to-pod relay, Helm charts and Prometheus metrics.

## When to use it

| | Pick it when | mishmesh differs because |
| --- | --- | --- |
| **ngrok** | you want a hosted, zero-ops tunnel with a global edge and a request inspector | mishmesh runs on your own infrastructure, so traffic and data stay with you. It has no global edge or request inspector. |
| **Cloudflare Tunnel** | your DNS is on Cloudflare and HTTP exposure through their network is enough | mishmesh does not depend on a third-party network. Public raw TCP/UDP ports and a reach-in API are built in. |
| **frp** | you want a mature single-server reverse proxy driven by config files | mishmesh adds a multi-tenant control plane, an API for agents and endpoints, horizontal scaling and the reach-in API. |

Use mishmesh when tunnels are part of your product or platform: many customer sites, many tenants, or a control plane that must call into networks it cannot route to. To show one laptop service to a colleague, a hosted tunnel is less work.

> [!NOTE]
> Not supported: peer-to-peer or WireGuard data paths and multi-region routing. See [Known limitations](#known-limitations).

## Quickstart

The root `docker-compose.yml` runs Postgres, the server, a `traefik/whoami` demo service and an agent that exposes it as `demo.localhost`. All host ports bind to `127.0.0.1`.

```bash
docker compose up -d --build
curl -H 'Host: demo.localhost' http://127.0.0.1:8080/
```

The response is whoami's echo of your request, served through the tunnel. The agent logs show the public URL and endpoint id:

```bash
docker compose logs agent
#   http  http  whoami:80  ->  http://demo.localhost:8080
#               endpoint: ep_...
#               path:     http://localhost:8080/tunnel/ep_...
```

| Address | What |
| --- | --- |
| `http://127.0.0.1:8080` | public ingress, routed by `Host` (`<subdomain>.localhost`) or path (`/tunnel/<endpoint_id>/`) |
| `http://127.0.0.1:8081` | agent WSS, control API (`/api/v1`), web UI, `/metrics` |
| `127.0.0.1:10000-10010` | public TCP endpoint range |

Create an agent token and open a TCP tunnel:

```bash
curl -s http://127.0.0.1:8081/api/v1/endpoints
curl -s -XPOST http://127.0.0.1:8081/api/v1/agents -d '{"name":"laptop"}'
mishmesh-agent tcp 5432 --port 10001 --gateway ws://127.0.0.1:8081 --token <token from the call above>
```

Stop it with `docker compose down` (add `-v` to drop the Postgres volume).

> [!WARNING]
> The quickstart is a local demo. It sets `MISHMESH_API_AUTH_DISABLED=true`, so the API and web UI need no login, and it uses the fixed bootstrap token `mm_dev_bootstrap_change_me`. Never expose it beyond `127.0.0.1`.

To run from source instead, use `make build`, then see [Agent](#agent) and [Server configuration](#server-configuration). With no `MISHMESH_DATA_DSN` the server uses a local SQLite file, which is meant for development only.

## Install

Every `v*` release tag publishes:

| Artifact | Where |
| --- | --- |
| `mishmesh-server` and `mishmesh-agent` binaries (linux, darwin, windows; amd64, arm64), web UI bundle, `checksums.txt` | [GitHub releases](https://github.com/niradler/mishmesh/releases) |
| Container images (linux/amd64, linux/arm64) | `ghcr.io/niradler/mishmesh-server:<version>`, `ghcr.io/niradler/mishmesh-agent:<version>` |
| Helm charts | `oci://ghcr.io/niradler/charts/mishmesh`, `oci://ghcr.io/niradler/charts/mishmesh-agent` |

```bash
helm upgrade --install mishmesh oci://ghcr.io/niradler/charts/mishmesh --version <version> \
  -n mishmesh --create-namespace -f values-company.yaml
```

Chart versions match the release tag without the `v`, and the chart's default image tag is its `appVersion`, so pinning the chart version pins the server version. The examples below use the in-repo chart path (`deploy/helm/mishmesh`); the OCI reference works the same way.

> [!TIP]
> The Go module path is `github.com/mishmesh/mishmesh`, so install from the release binaries or images, not with `go install`.

## Agent

The agent needs a gateway URL and an agent token. It accepts `ws://`, `wss://`, `http://` and `https://` (http maps to ws, https to wss) and appends the connect path `/_mishmesh/agent/connect` itself. Settings resolve in this order: flags (`--gateway`, `--token`, `--allow`), the config file, then the environment (`MISHMESH_GATEWAY_URL`, `MISHMESH_TOKEN`, `MISHMESH_ALLOW`, `MISHMESH_LOG_LEVEL`).

Agent tokens come from `POST /api/v1/agents` (shown once), from `mishmesh-server token create --org NAME --name AGENT`, or from `MISHMESH_BOOTSTRAP_TOKEN` on the server. `mishmesh-agent login` signs in interactively and stores credentials in `~/.mishmesh/credentials.json`.

### One-shot tunnels

```bash
mishmesh-agent http 3000 --subdomain demo                 # http://demo.<base domain>
mishmesh-agent http https://10.0.0.5:8443 --insecure      # local target speaks TLS (self-signed)
mishmesh-agent tcp 5432 --port 10050                      # public TCP port 10050
mishmesh-agent tcp 22                                     # random port from the server's range
mishmesh-agent udp 51820                                  # public UDP port
mishmesh-agent tls 8443 --subdomain api                   # SNI passthrough, TLS ends at your service
mishmesh-agent tls 8443 --domain api.example.com
```

The target is a port (meaning `127.0.0.1:<port>`) or `host:port`. Without `--reserved` the endpoint is ephemeral and is removed when the agent disconnects; `--subdomain`, `--domain` and `--port` imply `--reserved`.

### Config file

`mishmesh-agent start [name ...] [--config path]` runs every tunnel in the file, or only the named ones, over a single connection. Without `--config` the agent looks for `./mishmesh.yml`, then `~/.mishmesh/config.yaml`, then `/etc/mishmesh/agent.yml`. A full example is in [deploy/examples/agent.yml](deploy/examples/agent.yml).

```yaml
gateway: wss://connect.example.com
token: ${MISHMESH_TOKEN}
log_level: info
allow:
  - 10.0.0.0/8:22;5432

tunnels:
  web:
    proto: http
    addr: 3000
    subdomain: app
    policy:
      rate_limit: {requests: 100, period_seconds: 60}
  db:
    proto: tcp
    addr: 127.0.0.1:5432
    port: 10050
  api:
    proto: tls
    addr: 127.0.0.1:8443
    domain: api.example.com
```

- Tunnel keys: `proto` (`http`, `tcp`, `tls`, `udp`), `addr`, `subdomain`, `domain`, `port`, `reserved`, `private`, `target_https`, `insecure`, `policy` (see [Endpoint policy](#endpoint-policy)).
- `${VAR}` and `${VAR:-default}` are expanded. Unknown keys and invalid tunnels are reported with line numbers. Check a file without connecting with `mishmesh-agent validate --config path`.
- `tunnels:` is optional. A config with only `gateway`, `token` and `allow` starts a reach-in-only agent.
- `gateway_headers` adds headers to the connect request, for example a Cloudflare Access service token when the gateway sits behind an access proxy. The agent sets `Authorization`, `X-Mishmesh-User`, `Host`, `Upgrade`, `Connection` and the `Sec-WebSocket-*` headers itself, so those names are rejected.

```yaml
gateway_headers:
  CF-Access-Client-Id: ${CF_ACCESS_CLIENT_ID}
  CF-Access-Client-Secret: ${CF_ACCESS_CLIENT_SECRET}
```

On connect the agent prints each tunnel's public URL and endpoint id. If the gateway refuses a tunnel (subdomain taken, quota exceeded, port in use, kind disabled), the agent prints the reason and exits non-zero. A rejected token exits immediately with `token invalid or revoked` (401) or `agent disabled` (403). Any other failure is retried with jittered exponential backoff.

### UDP and agent-to-agent connects

A `udp` tunnel gets a public UDP port, like `tcp`. Each client address becomes its own flow, closed after `MISHMESH_UDP_IDLE_TIMEOUT` without traffic.

A tunnel marked `private: true` (tcp or udp) gets no public port. Other agents in the same org reach it through a `connects:` entry, which opens a local listener and carries traffic over the gateway. The origin agent decides who may connect with `policy.connect.allow_agents`: agent names, agent ids, or `"*"` for any agent in the org. An empty list denies everyone.

```yaml
# office agent: hosts the VPN, nothing public
tunnels:
  office-vpn:
    proto: udp
    addr: 51820
    private: true
    policy:
      connect: {allow_agents: [laptop]}

# laptop agent: VPN clients point at 127.0.0.1:51820
connects:
  vpn:
    endpoint: office-vpn
    proto: udp
    listen: 51820
    idle_timeout: 2m
```

Connect keys: `endpoint` (connect name or endpoint id; defaults to the connect's own name), `proto` (`tcp`, `udp`), `listen` (a port, meaning `127.0.0.1:<port>`, or `host:port`), `idle_timeout` (udp only).

When the service sits on the server's own network, skip the origin agent: create a `method=proxy` endpoint with `kind: udp`, `proxy_targets: ["udp://host:port"]` and a `connect` policy, and point a connect at it. The proxy guards (`MISHMESH_PROXY_ALLOW_PRIVATE`, `MISHMESH_PROXY_ALLOWED_CIDRS`) still apply.

### Run as a service

```bash
sudo mishmesh-agent service install --config /etc/mishmesh/agent.yml
sudo mishmesh-agent service start
mishmesh-agent service status
sudo mishmesh-agent service stop
sudo mishmesh-agent service uninstall
```

This works with systemd, launchd and the Windows service manager. The service runs `mishmesh-agent start --config <path>` and restarts on failure. `--user` picks the account and `--dry-run` prints the definition without installing.

> [!IMPORTANT]
> The service does not inherit your shell environment. Put the token in the config file itself and restrict the file's permissions.

On Kubernetes, use the `mishmesh-agent` chart ([deploy/helm/README.md](deploy/helm/README.md#agent-chart)).

## Reach-in API

Reach-in lets an authenticated API caller open connections *through* an agent to services on its network. It is off unless the server sets `MISHMESH_REACHIN_ENABLED=true`, and it is denied on the agent side unless the agent's allowlist permits the target.

The agent allowlist (`allow:` in the config, `--allow`, or `MISHMESH_ALLOW`) is deny-first, comma-separated `host|cidr[:port;port]`, for example `10.0.0.0/8:22;5432,db.internal:5432`. An empty allowlist denies everything. Loopback, link-local and cloud-metadata addresses are always denied, and a hostname is resolved once with the dial pinned to that IP.

**HTTP** (one request, response buffered up to 8 MB):

```bash
curl -s -XPOST https://mishmesh.corp.example.com/api/v1/reach/ag_123/http \
  -H "Authorization: Bearer $MISHMESH_API_AUTH_TOKEN" \
  -d '{"target":"grafana.internal:3000","method":"GET","path":"/api/health"}'
# {"status":200,"headers":{...},"body":"..."}
```

**Raw TCP stream** (Postgres, SSH, Redis, anything): send an HTTP Upgrade to `mishmesh-stream`. After `101 Switching Protocols` the connection carries raw bytes to and from the target, with half-close propagated both ways.

```http
GET /api/v1/reach/ag_123/stream?target=db.internal:5432 HTTP/1.1
Host: mishmesh.corp.example.com
Authorization: Bearer <token>
Connection: Upgrade
Upgrade: mishmesh-stream
```

Optional query parameters are `tls=true` (dial the target over TLS) and `insecure=true`. The caller needs write access to agents in the agent's org; an agent in another org returns 404. If the agent cannot dial the target, HTTP reach-in returns 502 with the agent's reason. The agent must be connected, but needs no tunnels, and reach-in also works against a server with `MISHMESH_INGRESS_ENABLED=false`. Full contract: [docs/api.md](docs/api.md#reach-in-data-plane).

## Endpoint policy

Every endpoint carries an optional policy, set with `POST`/`PATCH /api/v1/endpoints` or in the agent config's `policy:` block. It applies to HTTP(S) ingress.

| Field | Effect |
| --- | --- |
| `request_headers_add` / `_remove`, `response_headers_add` / `_remove` | header rewrites |
| `host_header`, `strip_path_prefix`, `add_path_prefix` | upstream Host and path rewrites |
| `basic_auth_user` + `basic_auth_password` | HTTP basic auth. Write-only through the API and stored as a bcrypt hash; in the agent config the agent hashes it before connecting, so the plaintext never leaves the host |
| `ip_allow` / `ip_deny` | CIDR lists. A request matching deny, or missing allow, gets 403 |
| `force_https` | redirect http to https |
| `max_body_bytes`, `compression` | request size cap and response compression |
| `oidc` | `{issuer, client_id, client_secret, allowed_emails, allowed_domains}`: visitors sign in with that provider |
| `mtls` | `{client_ca_pem, allowed_cns}`: requires client certificates on the HTTPS listener |
| `rate_limit` | `{requests, period_seconds, burst, scope}`, see below |

```bash
curl -XPATCH http://127.0.0.1:8081/api/v1/endpoints/ep_123 \
  -H "Authorization: Bearer $MISHMESH_API_AUTH_TOKEN" \
  -d '{"policy":{"ip_allow":["203.0.113.0/24"],"rate_limit":{"requests":100,"period_seconds":60,"burst":20,"scope":"ip"}}}'
```

`rate_limit` is a token bucket: `requests` per `period_seconds` refill steadily and `burst` (default `requests`) is the bucket size. `scope` is `ip` (default, one bucket per client IP, IPv6 grouped per /64) or `endpoint` (one shared bucket). Over the limit, a request gets `429` with `Retry-After`. The check runs after `force_https` and the IP rules and before authentication, so password guessing is throttled too. In cluster mode the buckets live in Redis; if Redis fails, the limiter lets requests through and logs a warning.

The client IP used by `ip_allow`, `ip_deny` and `rate_limit` is the socket peer. Behind a load balancer, set `MISHMESH_TRUSTED_PROXIES` (IPs or CIDRs) and the server uses the rightmost `X-Forwarded-For` hop that is not a trusted proxy, or keep the client IP at L4 (`externalTrafficPolicy: Local` in the chart).

## Deployment shapes

One image and one Helm chart cover three shapes. Postgres is the data store in every shape; Redis and cluster mode matter only once you run more than one server pod.

| | Homelab | Company / on-prem | SaaS |
| --- | --- | --- | --- |
| Who signs up | you (first user owns the org), plus API token | invited colleagues only | anyone; each signup gets its own org |
| `MISHMESH_SIGNUP_MODE` | `invite` | `invite` | `org` (default) |
| Custom domain verification | off | off (optional) | on (DNS TXT) |
| Quotas | none | optional | per-org defaults |
| Data / live state | Postgres / memory | Postgres / memory, or Redis with replicas | Postgres / Redis |
| Server pods | 1 | 1, or 2-6 with HPA | 3-20 with HPA |
| Helm values | `values.yaml` | `values-company.yaml` | `values-saas.yaml` |

Use the published images, or build your own with `make docker docker-agent VERSION=<version>` and set `image.repository`. Chart details (services, secrets, DNS, TLS, every value) are in [deploy/helm/README.md](deploy/helm/README.md). A plain Docker host without Kubernetes is covered in [deploy/README.md](deploy/README.md).

<details>
<summary><strong>Homelab</strong></summary>

One server pod, a bundled single-instance Postgres, the in-memory connection store, password login with invite signup, and API access by bearer token.

```bash
helm upgrade --install mishmesh deploy/helm/mishmesh -n mishmesh --create-namespace \
  --set baseDomain=tunnel.home.example.com

kubectl -n mishmesh get secret mishmesh -o jsonpath='{.data.api-auth-token}' | base64 -d
kubectl -n mishmesh get secret mishmesh -o jsonpath='{.data.bootstrap-token}' | base64 -d
```

Point `tunnel.home.example.com` and `*.tunnel.home.example.com` at the `mishmesh-public` LoadBalancer. The control API stays on a ClusterIP service (`kubectl -n mishmesh port-forward svc/mishmesh-api 8081`); call it with `Authorization: Bearer <api-auth-token>`.

Register right away through the port-forward: in invite mode the first registration becomes owner of the default org (where the bootstrap agent lives), and later ones need an invite. Set `auth.enabled=false` for token-only access.

Without Kubernetes, the root `docker-compose.yml` is a working single-host template. Before using it beyond your own machine, replace `MISHMESH_API_AUTH_DISABLED` with `MISHMESH_API_AUTH_TOKEN`, change the Postgres password and the bootstrap token, and follow [deploy/README.md](deploy/README.md) for DNS and TLS.

</details>

<details>
<summary><strong>Company / on-prem</strong></summary>

For a team, or for installation inside a customer network. Users log in with a password or Google; the first user owns the org and everyone else joins by invite. Postgres is external. `values-company.yaml` turns on cluster mode with an HPA from 2 to 6 pods. For a single pod, set `cluster.enabled=false`, `autoscaling.enabled=false`, `replicaCount=1` and `connStore.backend=memory`, and drop the Redis secret.

```bash
kubectl create ns mishmesh
kubectl -n mishmesh create secret generic mishmesh-db \
  --from-literal=data-dsn='postgres://mishmesh:PASSWORD@pg.corp.example.com:5432/mishmesh?sslmode=require'
kubectl -n mishmesh create secret generic mishmesh-redis \
  --from-literal=redis-url='rediss://:PASSWORD@redis.corp.example.com:6379/0'
kubectl -n mishmesh create secret tls mishmesh-wildcard-tls --cert=wildcard.pem --key=wildcard-key.pem
kubectl -n mishmesh create secret tls mishmesh-connect-tls --cert=connect.pem --key=connect-key.pem

helm upgrade --install mishmesh deploy/helm/mishmesh -n mishmesh \
  -f deploy/helm/mishmesh/values-company.yaml \
  --set baseDomain=tunnel.corp.example.com \
  --set connectIngress.host=connect.corp.example.com \
  --set connectIngress.controlHost=mishmesh.corp.example.com
```

Then:

1. Open `https://mishmesh.corp.example.com` and register the first account. It becomes owner of the default org.
2. Invite colleagues: `POST /api/v1/members {"email","role"}` returns a single-use `invite_token` (7-day expiry).
3. Create an agent token per site (`POST /api/v1/agents`) and run the agent there with `--gateway wss://connect.corp.example.com`.

Only the agent connect path is published on `connect.*`. The control API and UI are on `controlHost`, which you can leave empty to keep them in-cluster. If the control host sits behind an ingress controller, set `MISHMESH_TRUSTED_PROXIES` (via `extraEnv`) to the controller's addresses, or the login rate limiter sees every client as the controller.

</details>

<details>
<summary><strong>SaaS</strong></summary>

A public multi-tenant service. Every signup gets its own org (`MISHMESH_SIGNUP_MODE=org`). Custom domains must be proven with a DNS TXT record before an endpoint can use them (`MISHMESH_DOMAIN_VERIFICATION`, on by default in `org` mode). Per-org default quotas cap agents, endpoints and bandwidth. The cluster runs 3 to 20 pods with Postgres and Redis.

```bash
helm upgrade --install mishmesh deploy/helm/mishmesh -n mishmesh \
  -f deploy/helm/mishmesh/values-saas.yaml \
  --set baseDomain=tunnel.example.com \
  --set connectIngress.host=connect.example.com \
  --set connectIngress.controlHost=app.example.com \
  --set auth.googleClientID=<id> \
  --set secrets.googleClientSecret=<secret> \
  --set auth.oidcRedirectURL=https://app.example.com/api/v1/auth/google/callback
```

It needs the same Secrets as the company shape. Quotas come from `quotas.*` (`MISHMESH_QUOTA_MAX_AGENTS`, `_MAX_ENDPOINTS`, `_MAX_BANDWIDTH_BYTES`; 0 means unlimited). The operator can change one org's quota with the admin bearer token: `PUT /api/v1/quota?org_id=...`.

Custom domain flow for a tenant:

```bash
curl -XPOST https://app.example.com/api/v1/domains -d '{"name":"api.customer.com"}'
# -> challenge: TXT _mishmesh-challenge.api.customer.com = <value>, cname_target: tunnel.example.com
curl -XPOST https://app.example.com/api/v1/domains/<id>/verify
# then bind it: POST /api/v1/endpoints {..., "domain":"api.customer.com"} or agent --domain api.customer.com
```

These calls use the tenant's session cookie or the admin bearer token. Certificates for verified custom domains come from ACME, which runs on a single pod only; in a cluster, serve custom domains with certificates you provide, or terminate TLS in front of mishmesh.

</details>

## Architecture

```mermaid
flowchart LR
  subgraph private["Private network (outbound only)"]
    svc["internal services"]
    agent["mishmesh-agent"]
    agent -->|dial target| svc
  end

  subgraph server["mishmesh-server (N identical pods)"]
    gw["gateway<br/>agent WSS + yamux"]
    ing["ingress<br/>HTTP/HTTPS · TCP/UDP ports · TLS SNI · SSH"]
    cp["control plane<br/>/api/v1 · web UI · reach-in"]
    relay["pod-to-pod relay<br/>HMAC, :7443"]
  end

  pg[("Postgres<br/>orgs, agents, endpoints")]
  redis[("Redis<br/>live sessions, port claims,<br/>rate-limit buckets")]

  agent ==>|"outbound WSS<br/>(one session, many streams)"| gw
  visitor["public visitor"] --> ing
  ctl["your control plane"] -->|reach-in| cp
  ing -->|open stream| gw
  cp -->|open stream| gw
  ing -.->|agent on another pod| relay
  server --- pg
  server --- redis
```

| Component | Role |
| --- | --- |
| `internal/tunnel` | shared transport: WebSocket connection, yamux multiplexing and the wire protocol, used by both binaries |
| gateway | terminates agent sessions, authenticates tokens, registers endpoints, tracks live sessions. Everything else reaches agents through `store.AgentConn.OpenStream` |
| ingress | public HTTP/HTTPS, the TCP and UDP port ranges, TLS passthrough by SNI and the optional `ssh -R` entry point. Applies endpoint policy, then opens a stream to the agent |
| control plane | REST API, auth, orgs, quotas, audit, custom domains, reach-in, services, connections and network topology ([docs/api.md](docs/api.md)) |
| store | `DataStore` for durable data (Postgres, or SQLite for dev) and `ConnectionStore` for live sessions (memory, or Redis for clusters) |

### How it scales

- **Identical pods.** Any pod accepts agent sessions and public traffic on every listener. The load balancer needs no sticky sessions.
- **Shared state.** Postgres holds durable data. Redis holds live routing: which pod owns which agent session, TCP port claims and rate-limit buckets.
- **Pod-to-pod relay.** A request that lands on a pod without the agent is resolved through Redis and relayed to the owner pod. HTTP, TCP, TLS, SSH, proxy and reach-in all use this path.
- **Cluster-wide TCP ports.** Every pod pre-binds the whole `TCP_PORT_MIN..TCP_PORT_MAX` range and a port is claimed once in Redis, so a public TCP port answers on any pod. Keep the range modest.
- **Autoscaling and drain.** CPU-based HPA scales up immediately and down slowly, since each removed pod drops its agent sessions. On SIGTERM a pod flips `/readyz` to 503, lets in-flight streams finish and closes within a bounded time; `/healthz` stays 200.
- **Reconnects.** Agents reconnect with jittered backoff to whichever pod they reach, which takes over ownership; the old session is kicked cluster-wide over Redis pub/sub.

Cluster mode requires `MISHMESH_CLUSTER_ENABLED=true`, `MISHMESH_CONN_BACKEND=redis`, `MISHMESH_REDIS_URL`, a Postgres data store, `MISHMESH_RELAY_ADVERTISE` (`podIP:7443`) and `MISHMESH_CLUSTER_SECRET`; the server refuses to start without them. The chart sets all of this when `cluster.enabled=true`. To try two replicas on one machine, use `deploy/compose.cluster.yml` ([deploy/README.md](deploy/README.md#cluster-mode-multiple-identical-pods)).

### Sizing

Measured in Docker with the server limited to 4 vCPU / 8 GB, Postgres 16, 200 agents and 10 s runs (`c` = concurrent clients):

| Path | 1 KB requests | 1 MB requests |
| --- | --- | --- |
| Single pod | 18.6k req/s, p99 6 ms (c50); 20.2k req/s, p99 37 ms (c200) | ~2.3 GB/s |
| Cluster, agent on the receiving pod | 16.9-17.4k req/s | ~2.0-2.3 GB/s |
| Cluster, relayed to the owner pod | 9.6-11.8k req/s, +2.5 ms p50 | ~1.2-1.4 GB/s |

- An idle agent costs about 70 KB of RSS (5,000 agents: about 350 MB, connected in about 8 s). Plan for 10-20k agents per pod.
- Expect about 5k req/s of small requests per core, and about 60% of that for relayed traffic.
- Keep `MISHMESH_DATA_MAX_CONNS` at the default of 25 and set Postgres `max_connections` to at least `pods × 25 + 20`.
- For N agents, run `max(2, ceil(N / 10,000))` pods, then add pods for request rate at about 15k req/s per pod.
- When a pod dies, its agents are back on another pod within about 3 s; requests for them fail with 503s for about 2 s.

## Security model

- **Two credentials.** Agents use agent tokens: random, stored hashed, shown once. Rotating a token revokes earlier ones and drops the live session. The control API accepts the operator's admin bearer token (`MISHMESH_API_AUTH_TOKEN`) or a browser session (`mm_session` cookie, `Secure` when `PUBLIC_SCHEME=https`). The server refuses to start unless the API has a token or `MISHMESH_API_AUTH_DISABLED=true` is set explicitly.
- **Tenancy.** Every resource belongs to an org and every route is scoped to the caller's active org. A resource in another org returns 404, never 403. Roles are `owner`, `admin` and `member`; by default owner and admin can do everything and member is read-only. Each org can replace the default with its own Cedar policy (`GET`/`PUT /api/v1/policy`). The admin bearer token is the platform operator: it can target any org with `?org_id=` and bypasses org policy. Quota writes are operator-only.
- **Signup and invites.** `org` mode gives every new account its own org; `invite` lets only the first user self-register. Invite tokens are single-use, bound to one email, expire after 7 days and are stored as SHA-256 hashes. An inviter cannot grant a role above their own. Login and registration are rate limited per IP and per email, cluster-wide.
- **Custom domains.** With verification on, a domain binds only after its `_mishmesh-challenge` TXT record proves control, and belongs to exactly one org. A CNAME alone is not proof.
- **Reach-in** has three gates: the server flag, a caller allowed to write agents in that org, and the agent's deny-first allowlist.
- **Agentless proxy endpoints** (`method=proxy`) refuse loopback, link-local, multicast, metadata, RFC1918, CGNAT, ULA targets and cluster-local or single-label names by default, checked at create time and again at dial time. Opt in with `MISHMESH_PROXY_ALLOW_PRIVATE=true` or a narrower `MISHMESH_PROXY_ALLOWED_CIDRS`. Endpoint OIDC refuses private issuers unless `MISHMESH_OIDC_ALLOW_PRIVATE_ISSUERS=true`.
- **CSRF and tunnel domains.** Session-authenticated writes must carry `Content-Type: application/json` and a matching `Origin` (or `Sec-Fetch-Site`); bearer-token requests are exempt. Tenant tunnels are untrusted web content, so for SaaS serve them from a separate registrable domain (app at `mishmesh.example`, tunnels under `*.mishmesh-tunnels.example`).

> [!CAUTION]
> Keep the control listener private. Port `8081` serves agent connects, the API, the UI and `/metrics` (bearer `MISHMESH_METRICS_TOKEN`, or the API token if unset). Publish only `/_mishmesh/agent/connect`, with TLS in front; the chart's `connectIngress` does exactly that. The cluster relay (port 7443) is HMAC-authenticated but **not encrypted**, so keep it on a private network, such as a pod network with NetworkPolicy (enabled in the company and SaaS chart profiles), a private subnet or a service mesh with mTLS.

## Server configuration

All settings are environment variables with the `MISHMESH_` prefix. Defaults below are for the binary. The Docker image overrides the listen addresses for container use and sets `DATA_DSN` to `/data/mishmesh.db`. The Helm chart sets everything from `values.yaml`, and `extraEnv` covers the rest.

| Var | Default | Meaning |
| --- | --- | --- |
| `BASE_DOMAIN` | `localhost:8080` | public URL suffix (`<sub>.<base>`) |
| `PUBLIC_SCHEME` | `http` | `http` or `https`; also marks cookies `Secure` |
| `INGRESS_ADDR` / `HTTPS_ADDR` / `API_ADDR` | `127.0.0.1:8080` / `127.0.0.1:8443` / `127.0.0.1:8081` | listeners |
| `DATA_BACKEND` / `DATA_DSN` | inferred / `mishmesh.db` | `postgres` (a `postgres://` DSN selects it) or `sqlite` for dev |
| `DATA_MAX_CONNS` / `DATA_MAX_IDLE_CONNS` | `25` / | Postgres pool size per pod |
| `DATA_QUERY_TIMEOUT` | `5s` | deadline for each Postgres store operation, including pool waits |
| `CONN_BACKEND` / `REDIS_URL` / `REDIS_POOL_SIZE` | `memory` / empty / `0` | `redis` for clusters |
| `API_AUTH_TOKEN` | empty | admin bearer token for `/api/v1`; required unless `API_AUTH_DISABLED=true` |
| `BOOTSTRAP_TOKEN` | empty | seed one agent token (`ag_bootstrap` in `org_default`) at startup |
| `AUTH_ENABLED` / `AUTH_PASSWORD_ENABLED` | `false` / `true` | browser login; password off means Google only |
| `SIGNUP_MODE` | `org` | `org` or `invite` |
| `DOMAIN_VERIFICATION` | `true` in `org` mode | require TXT proof for custom domains |
| `PATH_ROUTING` | `false` in `org` mode, `true` in `invite` | serve endpoints at `/tunnel/{id}` on the base domain |
| `UPSTREAM_RESPONSE_TIMEOUT` | `5m` | wait for upstream response headers; streaming bodies have no fixed timeout |
| `GOOGLE_CLIENT_ID` / `GOOGLE_CLIENT_SECRET` / `OIDC_REDIRECT_URL` / `OIDC_ISSUER` | | Google login |
| `SESSION_TTL_HOURS` | `168` | login session lifetime |
| `WEBUI_ENABLED` / `WEBUI_DIR` | `false` / empty | serve the SPA (the image bundles it at `/webui`) |
| `INGRESS_ENABLED` | `true` | public ingress (all kinds) |
| `TCP_ENABLED` / `TCP_BIND_HOST` / `TCP_PORT_MIN` / `TCP_PORT_MAX` | `true` / `127.0.0.1` / `10000` / `10100` | public TCP ports |
| `UDP_BIND_HOST` / `UDP_PORT_MIN` / `UDP_PORT_MAX` | `127.0.0.1` / `10200` / `10300` | public UDP ports (single pod only) |
| `UDP_IDLE_TIMEOUT` / `UDP_MAX_FLOWS` | `60s` / `1024` | UDP flow idle timeout and concurrent flows per port |
| `TLS_ENABLED`, `TLS_CERT_FILE` / `TLS_KEY_FILE`, `ACME_ENABLED` / `ACME_EMAIL` / `ACME_CACHE_DIR`, `SELF_SIGNED_TLS` | | HTTPS ingress |
| `TLS_PASSTHROUGH_ENABLED` / `TLS_PASSTHROUGH_ADDR` / `TLS_PASSTHROUGH_PUBLIC_PORT` | `false` / `127.0.0.1:8444` / `8444` | SNI passthrough for `tls` endpoints |
| `SSH_ENABLED` / `SSH_ADDR` / `SSH_HOST_KEY_FILE` | `false` / `127.0.0.1:2222` | clientless `ssh -R` entry point |
| `REACHIN_ENABLED` | `false` | reach-in API |
| `TRUSTED_PROXIES` | empty | IPs/CIDRs whose `X-Forwarded-For` is trusted |
| `ALLOWED_ORIGINS` | empty | extra origins allowed for cookie-authenticated writes |
| `QUOTA_MAX_AGENTS` / `QUOTA_MAX_ENDPOINTS` / `QUOTA_MAX_BANDWIDTH_BYTES` | `0` | default per-org quota (0 = unlimited) |
| `MAX_ORGS_PER_USER` | `3` | orgs one user may own (0 = unlimited) |
| `METRICS_ENABLED` / `METRICS_TOKEN` | `true` / empty | Prometheus `/metrics` on the control listener |
| `ENDPOINT_OIDC_KEY` | derived from `API_AUTH_TOKEN` | signing key for per-endpoint OIDC cookies |
| `OIDC_ALLOW_PRIVATE_ISSUERS` | `false` | allow endpoint OIDC issuers on private addresses |
| `PROXY_ALLOW_LOOPBACK` / `PROXY_ALLOW_PRIVATE` / `PROXY_ALLOWED_CIDRS` | `false` / `false` / empty | what `method=proxy` endpoints may target |
| `HEALTH_INTERVAL` / `HEALTH_TIMEOUT` | `15s` / `3s` | endpoint health probes |
| `SECRET_KEY_FILE` | `mishmesh-secret.key` next to the SQLite file, else the working directory | AES-256 key that seals stored provider tokens; created on first start |
| `HOST_COMMANDS` | `true` in `invite` mode, else `false` | allow services that run commands on the host ([docs/services.md](docs/services.md)) |
| `DOCKER_HOST` | `unix:///var/run/docker.sock` | Docker Engine for `docker` and `compose` services |
| `CLOUDFLARED_BIN` | `cloudflared` | binary for a managed Cloudflare connector ([docs/cloudflare.md](docs/cloudflare.md)) |
| `CLUSTER_ENABLED`, `NODE_ID`, `RELAY_ADDR`, `RELAY_ADVERTISE`, `CLUSTER_SECRET` | | [cluster mode](#how-it-scales) |
| `LOG_LEVEL` | `info` | |

Server CLI: `mishmesh-server [serve]`, `mishmesh-server token create --org NAME --name AGENT [--dsn DSN]`, `mishmesh-server version`, `mishmesh-server help`. On startup the server deletes ephemeral endpoints whose agent has no live session (in cluster mode, only those owned by no pod).

## Operations

- **Back up Postgres.** It holds all durable state: orgs, users, agents, token hashes, endpoints, domains, quotas, services, connections and the audit log. Use `pg_dump -Fc`, managed snapshots or WAL archiving. Redis holds only live state that agents rebuild by reconnecting.
- **Back up the secrets with it.** Keep `SECRET_KEY_FILE` (without it, stored provider tokens cannot be decrypted), the ACME cache or your wildcard certificate Secret, and `API_AUTH_TOKEN`, `CLUSTER_SECRET`, `ENDPOINT_OIDC_KEY` and Google OIDC credentials.
- **Upgrades.** The schema is clean and typed, with no automatic conversion from earlier, untyped releases; unsupported old schemas are rejected unchanged. Upgrade the server before the agents, which reconnect with backoff during a rollout.
- **First owner.** In invite mode the first registration claims the default org, so restrict portal access to operators until that account exists.
- **Before production**, validate the real deployment: DNS, TLS chains without insecure flags, API auth and cross-origin rejection, agent reconnects after a restart, a backup restored into a separate instance, alerting, and Google or endpoint OIDC against the real provider.

## Known limitations

- HTTP reach-in buffers the response (8 MB cap). Use the stream route for large or long transfers.
- ACME runs on a single pod only. Clusters use a wildcard certificate Secret.
- TCP, UDP and TLS-passthrough endpoints have no rate limiting and no in-flight bandwidth metering.
- Public UDP ports run on a single pod; cluster mode turns UDP ingress off.
- Custom domains are verified once and not re-verified periodically.
- Bandwidth quota usage is shared across pods within 1 to 2 s, so a burst can briefly overshoot in cluster mode.
- An unverified password signup can hold an email address; a later Google login for it is refused (409), not merged.
- Endpoint OIDC client secrets are stored unencrypted in Postgres, so protect database access and backups.

Next on the roadmap: request inspector, webhook verification, endpoint pooling and HTTP reach-in streaming. Not planned: P2P or WireGuard data paths, SAML, multi-region, a Kubernetes operator and billing.

## Documentation

| Doc | Covers |
| --- | --- |
| [docs/api.md](docs/api.md) | full REST contract for `/api/v1`, including reach-in |
| [docs/connections.md](docs/connections.md) | Cloudflare and Tailscale app access |
| [docs/cloudflare.md](docs/cloudflare.md) | Cloudflare Tunnel permissions and ownership |
| [docs/services.md](docs/services.md) | wake-on-demand Docker, Compose and command services |
| [docs/egress.md](docs/egress.md) | agent forward proxy, Cedar policy and credential injection |
| [deploy/README.md](deploy/README.md) | single Docker host, SSH and proxy methods, cluster compose |
| [deploy/helm/README.md](deploy/helm/README.md) | Helm charts for the server and the agent |
| [docs/prd.md](docs/prd.md) | product background |

## Development

```bash
make build    # bin/mishmesh-server, bin/mishmesh-agent
make check    # fmt + vet + test (race), run before committing
make test     # go test -race
make ci       # full local CI
make hooks    # install the git hooks
make help     # list every target
```

On Windows, `make test`, `make test-short` and `make check` run the Go tests in Docker with no published ports, so test listeners stay off the host and trigger no firewall prompts. Docker must be running, and test containers are kept for inspection.

CI runs locally. `make ci` runs `.claude/scripts/ci-local.py`, which executes every check from `.github/workflows/ci.yml` (gofmt, vet, race tests, Postgres store tests, govulncheck, web build and audit, Helm lint and security check, both Docker image builds) against a clean export of `HEAD`, in Docker with no published ports. It needs Docker, `helm` and Python. `make hooks` points git at `.githooks`: pre-commit runs gofmt and `go vet`, and pre-push runs the local CI on the pushed commits, skipping stages whose files did not change. Set `MISHMESH_SKIP_PREPUSH=1` to skip it once. The GitHub `ci` workflow runs only when triggered by hand; the `release` workflow still runs on `v*` tags and builds the binaries, images and charts.

Layout: `cmd/mishmesh-server`, `cmd/mishmesh-agent`, `internal/` (tunnel, gateway, agent, ingress, controlplane, cluster, store backends and more), `web/` (React UI), `deploy/` (compose files, Helm charts, examples).
