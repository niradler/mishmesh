# mishmesh

mishmesh is a self-hosted transport layer for reaching services that live in private networks: behind NAT, firewalls or corporate proxies, where only outbound connections are allowed. A small agent inside the private network dials out to the mishmesh server over WebSocket (WSS), and every connection after that is multiplexed back through that one outbound tunnel.

You deploy it once and reuse it everywhere, instead of building a custom tunnel for each product or customer site. It works in two directions:

- **Expose**: publish a private service on a public URL (`https://app.tunnel.example.com`), a public TCP port, or a TLS-passthrough hostname, in the style of ngrok.
- **Reach in**: let your own control plane call internal services through a customer's agent over an authenticated API, as HTTP requests or raw TCP streams. No public URL is created.

One Go module builds two binaries: `mishmesh-server` (gateway, public ingress, control API, web UI) and `mishmesh-agent` (the tunnel client).

## Contents

- [When to use it](#when-to-use-it)
- [Quickstart (5 minutes, Docker)](#quickstart-5-minutes-docker)
- [Deployment shapes](#deployment-shapes)
- [Agent](#agent)
- [Reach-in API](#reach-in-api)
- [Endpoint policy](#endpoint-policy)
- [Security model](#security-model)
- [Architecture](#architecture)
- [How it scales](#how-it-scales)
- [Server configuration](#server-configuration)
- [Status and roadmap](#status-and-roadmap)
- [Development](#development)

## When to use it

| | Pick it when | mishmesh differs because |
| --- | --- | --- |
| **ngrok** | you want a hosted, zero-ops tunnel with a global edge and a request inspector | mishmesh runs on your own infrastructure, so traffic and data stay with you. It has no global edge network or request inspector. |
| **Cloudflare Tunnel** | your DNS is already on Cloudflare and HTTP exposure through their network is enough | mishmesh does not depend on a third-party network or account. Public raw TCP ports and a reach-in API are built in. |
| **frp** | you want a mature, single-server reverse proxy driven by config files | mishmesh adds a multi-tenant control plane (orgs, roles, invites, quotas, audit), an API to manage agents and endpoints, horizontal scaling across identical pods, and the reach-in API. |

Use mishmesh when tunnels are part of your product or platform: many customer sites, many tenants, or a control plane that must call into networks it cannot route to. If you only need to show one laptop service to a colleague, a hosted tunnel is less work.

Not supported: UDP, peer-to-peer or WireGuard data paths, multi-region routing. mishmesh is version 0.1.0 and no images are published yet, so you build them from this repo (see [Deployment shapes](#deployment-shapes)).

## Quickstart (5 minutes, Docker)

The root `docker-compose.yml` runs Postgres, the server, a `traefik/whoami` demo service and an agent that exposes it as `demo.localhost`. All host ports are bound to `127.0.0.1`.

```bash
docker compose up -d --build
curl -H 'Host: demo.localhost' http://127.0.0.1:8080/
```

The response is whoami's echo of the request, served through the tunnel. The agent logs show the public URL and endpoint id:

```bash
docker compose logs agent
#   http  http  whoami:80  ->  http://demo.localhost:8080
#               endpoint: ep_...
#               path:     http://localhost:8080/tunnel/ep_...
```

What is running:

| URL | What |
| --- | --- |
| `http://127.0.0.1:8080` | public ingress. Routes by `Host` (`<subdomain>.localhost`) or by path (`/tunnel/<endpoint_id>/`) |
| `http://127.0.0.1:8081` | agent WSS, control API (`/api/v1`), web UI, `/metrics` |
| `127.0.0.1:10000-10010` | public TCP endpoint range |

Try the API and a TCP tunnel:

```bash
curl -s http://127.0.0.1:8081/api/v1/endpoints
curl -s -XPOST http://127.0.0.1:8081/api/v1/agents -d '{"name":"laptop"}'
mishmesh-agent tcp 5432 --port 10001 --gateway ws://127.0.0.1:8081 --token <token from the call above>
```

The quickstart is a local demo. It sets `MISHMESH_API_AUTH_DISABLED=true` so the API and web UI work without a login, and it uses the fixed bootstrap token `mm_dev_bootstrap_change_me`. Never expose it beyond `127.0.0.1`. Stop it with `docker compose down` (add `-v` to also drop the Postgres volume).

To run from source instead: `make build`, then see [Agent](#agent) and [Server configuration](#server-configuration). With no `MISHMESH_DATA_DSN` the server falls back to a local SQLite file, which is only meant for development.

## Deployment shapes

One image and one Helm chart cover all three shapes. Postgres is the data store in every shape. Redis and cluster mode only come into play once you run more than one server pod.

| | Homelab | Company / on-prem | SaaS |
| --- | --- | --- | --- |
| Who signs up | nobody, API token only (login optional) | invited colleagues only | anyone, each signup gets its own org |
| `MISHMESH_SIGNUP_MODE` | n/a (or `invite` if you enable login) | `invite` | `org` (default) |
| Custom domain verification | off | off (on if you want it) | on (DNS TXT) |
| Quotas | none | optional | per-org defaults |
| Data / live state | Postgres / memory | Postgres / memory, or Redis with replicas | Postgres / Redis |
| Server pods | 1 | 1, or 2-6 with HPA | 3-20 with HPA |
| Helm values | `values.yaml` | `values-company.yaml` | `values-saas.yaml` |

Images: build and push them to your registry, then point the chart at it.

```bash
make docker docker-agent VERSION=0.1.0
docker tag mishmesh-server:0.1.0 registry.example.com/mishmesh-server:0.1.0
docker tag mishmesh-agent:0.1.0 registry.example.com/mishmesh-agent:0.1.0
docker push registry.example.com/mishmesh-server:0.1.0
docker push registry.example.com/mishmesh-agent:0.1.0
```

Chart details (services, secrets, DNS, TLS options, every value) are in [deploy/helm/README.md](deploy/helm/README.md). Running on a plain Docker host without Kubernetes is covered in [deploy/README.md](deploy/README.md).

### Homelab

One server pod, a bundled single-instance Postgres, the in-memory connection store, and API access by bearer token.

```bash
helm upgrade --install mishmesh deploy/helm/mishmesh -n mishmesh --create-namespace \
  --set image.repository=registry.example.com/mishmesh-server \
  --set baseDomain=tunnel.home.example.com

kubectl -n mishmesh get secret mishmesh -o jsonpath='{.data.api-auth-token}' | base64 -d
kubectl -n mishmesh get secret mishmesh -o jsonpath='{.data.bootstrap-token}' | base64 -d
```

Point `tunnel.home.example.com` and `*.tunnel.home.example.com` at the `mishmesh-public` LoadBalancer. The control API stays on a ClusterIP service (`kubectl -n mishmesh port-forward svc/mishmesh-api 8081`); call it with `Authorization: Bearer <api-auth-token>`.

The web UI uses login sessions, not the bearer token. To use it, enable login and invite mode, so the first account you register owns the default org (where the bootstrap agent lives):

```bash
--set auth.enabled=true \
--set 'extraEnv[0].name=MISHMESH_SIGNUP_MODE' --set 'extraEnv[0].value=invite'
```

Then register right away through the port-forward. In invite mode the first registration becomes the owner, and later ones need an invite.

Without Kubernetes: the root `docker-compose.yml` is a working single-host template. Before using it beyond your own machine, replace `MISHMESH_API_AUTH_DISABLED` with `MISHMESH_API_AUTH_TOKEN`, change the Postgres password and the bootstrap token, and follow [deploy/README.md](deploy/README.md) for DNS and TLS.

### Company / on-prem (multi-account)

For a team or for installation inside a customer network. Users log in with a password or Google. The first user owns the org, and everyone else joins by invite. Postgres is external. Redis and replicas are optional: `values-company.yaml` turns on cluster mode with an HPA from 2 to 6 pods. For a single pod, set `cluster.enabled=false`, `autoscaling.enabled=false`, `replicaCount=1` and `connStore.backend=memory`, and drop the Redis secret.

```bash
kubectl create ns mishmesh
kubectl -n mishmesh create secret generic mishmesh-db \
  --from-literal=data-dsn='postgres://mishmesh:PASSWORD@pg.corp.internal:5432/mishmesh?sslmode=require'
kubectl -n mishmesh create secret generic mishmesh-redis \
  --from-literal=redis-url='rediss://:PASSWORD@redis.corp.internal:6379/0'
kubectl -n mishmesh create secret tls mishmesh-wildcard-tls --cert=wildcard.pem --key=wildcard-key.pem
kubectl -n mishmesh create secret tls mishmesh-connect-tls --cert=connect.pem --key=connect-key.pem

helm upgrade --install mishmesh deploy/helm/mishmesh -n mishmesh \
  -f deploy/helm/mishmesh/values-company.yaml \
  --set image.repository=registry.example.com/mishmesh-server \
  --set baseDomain=tunnel.corp.example.com \
  --set connectIngress.host=connect.corp.example.com \
  --set connectIngress.controlHost=mishmesh.corp.example.com
```

Then:

1. Open `https://mishmesh.corp.example.com`, register the first account. It becomes owner of the default org.
2. Invite colleagues: `POST /api/v1/members {"email","role"}` returns a single-use `invite_token` (7-day expiry). They register with it, or accept it while logged in.
3. Create an agent token per site (`POST /api/v1/agents`) and run the agent there with `--gateway wss://connect.corp.example.com`.

Only the agent connect path is published on `connect.*`. The control API and UI are on `controlHost`, which you can leave empty to keep them in-cluster. If the control host sits behind an ingress controller, set `MISHMESH_TRUSTED_PROXIES` (via `extraEnv`) to the controller's addresses. Otherwise the login rate limiter sees every client as the controller.

### SaaS

Public multi-tenant service. Every signup gets its own org as owner (`MISHMESH_SIGNUP_MODE=org`, the default). Custom domains must be proven with a DNS TXT record before an endpoint can use them (`MISHMESH_DOMAIN_VERIFICATION`, on by default in `org` mode). Per-org default quotas cap agents, endpoints and bandwidth. The cluster runs 3 to 20 pods, with Postgres and Redis.

```bash
helm upgrade --install mishmesh deploy/helm/mishmesh -n mishmesh \
  -f deploy/helm/mishmesh/values-saas.yaml \
  --set image.repository=registry.example.com/mishmesh-server \
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

These calls use the tenant's session cookie or the admin bearer token. The certificate for a verified custom domain comes from ACME, which runs on a single pod only. In a cluster, serve custom domains with certificates you provide, or terminate TLS in front of mishmesh.

## Agent

The agent needs a gateway URL and an agent token. It accepts `ws://`, `wss://`, `http://` and `https://` (http maps to ws and https to wss) and appends the connect path `/_mishmesh/agent/connect` itself. Settings are resolved in this order: flags (`--gateway`, `--token`, `--allow`), then the config file, then the environment (`MISHMESH_GATEWAY_URL`, `MISHMESH_TOKEN`, `MISHMESH_ALLOW`, `MISHMESH_LOG_LEVEL`).

Agent tokens come from `POST /api/v1/agents` (shown once), from `mishmesh-server token create --org NAME --name AGENT`, or from `MISHMESH_BOOTSTRAP_TOKEN` on the server.

### One-shot tunnels

```bash
mishmesh-agent http 3000 --subdomain demo                 # http://demo.<base domain>
mishmesh-agent http https://10.0.0.5:8443 --insecure      # local target speaks TLS (self-signed)
mishmesh-agent tcp 5432 --port 10050                      # public TCP port 10050
mishmesh-agent tcp 22                                     # random port from the server's range
mishmesh-agent tls 8443 --subdomain api                   # SNI passthrough, TLS ends at your service
mishmesh-agent tls 8443 --domain api.example.com
```

The target is a port (meaning `127.0.0.1:<port>`) or `host:port`. Flags: `--subdomain`, `--domain`, `--port` (tcp), `--reserved` (keep the endpoint after disconnect; implied by `--subdomain`, `--domain` and `--port`), `--target-https`, `--insecure`. Without `--reserved` the endpoint is ephemeral and is removed when the agent disconnects.

### Config file (many tunnels, one session)

`mishmesh-agent start [name ...] [--config path]` runs every tunnel in the file, or only the named ones, over a single connection. Without `--config` the agent looks for `./mishmesh.yml`, then `~/.config/mishmesh/agent.yml`, then `/etc/mishmesh/agent.yml`. A full example is in [deploy/examples/agent.yml](deploy/examples/agent.yml).

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

Tunnel keys: `proto` (`http`, `tcp`, `tls`), `addr`, `subdomain`, `domain`, `port`, `reserved`, `target_https`, `insecure`, `policy` (the [endpoint policy](#endpoint-policy) object). `${VAR}` and `${VAR:-default}` are expanded. Unknown keys and invalid tunnels are reported with line numbers. To check a file without connecting, run `mishmesh-agent validate --config path`.

On connect the agent prints each tunnel's public URL and endpoint id. If the gateway refuses a tunnel (subdomain taken, quota exceeded, port in use, kind disabled on the server), the agent prints the reason and exits non-zero. A rejected token exits immediately with `token invalid or revoked` (401) or `agent disabled` (403). Any other connection failure is retried with jittered exponential backoff.

### Run as a service

```bash
sudo mishmesh-agent service install --config /etc/mishmesh/agent.yml
sudo mishmesh-agent service start
mishmesh-agent service status
sudo mishmesh-agent service stop
sudo mishmesh-agent service uninstall
```

This works with systemd, launchd and the Windows service manager. The service runs `mishmesh-agent start --config <path>` and restarts on failure. `--user` picks the account and `--dry-run` prints the definition without installing. The service does not inherit your shell environment, so put the token in the file itself and restrict the file's permissions.

On Kubernetes, use the `mishmesh-agent` chart (see [deploy/helm/README.md](deploy/helm/README.md#agent-chart)).

## Reach-in API

Reach-in lets an authenticated API caller (your control plane) open connections *through* an agent to services on its network. It is off unless the server sets `MISHMESH_REACHIN_ENABLED=true`, and it is denied on the agent side unless the agent's allowlist permits the target.

Agent allowlist (`allow:` in the config, `--allow`, or `MISHMESH_ALLOW`): deny-first, comma-separated `host|cidr[:port;port]`, for example `10.0.0.0/8:22;5432,db.internal:5432`. An empty allowlist denies everything. Loopback, link-local and cloud-metadata addresses are always denied, and a hostname is resolved once and the dial is pinned to that IP.

HTTP (one request, response buffered up to 8 MB):

```bash
curl -s -XPOST https://mishmesh.corp.example.com/api/v1/reach/ag_123/http \
  -H "Authorization: Bearer $MISHMESH_API_AUTH_TOKEN" \
  -d '{"target":"grafana.internal:3000","method":"GET","path":"/api/health"}'
# {"status":200,"headers":{...},"body":"..."}
```

Raw TCP stream (any protocol: Postgres, SSH, Redis, ...). Send an HTTP Upgrade to `mishmesh-stream`. After `101 Switching Protocols` the connection carries raw bytes to and from the target, and half-close is propagated in both directions.

```http
GET /api/v1/reach/ag_123/stream?target=db.internal:5432 HTTP/1.1
Host: mishmesh.corp.example.com
Authorization: Bearer <token>
Connection: Upgrade
Upgrade: mishmesh-stream
```

Optional query parameters: `tls=true` (dial the target over TLS) and `insecure=true`. The caller needs write access to agents in the agent's org, and an agent in another org returns 404. If the agent cannot dial the target, HTTP reach-in returns 502 with the agent's reason. Full contract: [docs/api.md](docs/api.md#reach-in-data-plane).

The agent must be connected to be reachable, and it currently needs at least one tunnel in its config to connect (see [Status and roadmap](#status-and-roadmap)).

## Endpoint policy

Every endpoint carries an optional policy, set with `POST`/`PATCH /api/v1/endpoints` or in the agent config's `policy:` block. It applies to HTTP(S) ingress:

| Field | Effect |
| --- | --- |
| `request_headers_add` / `_remove`, `response_headers_add` / `_remove` | header rewrites |
| `host_header`, `strip_path_prefix`, `add_path_prefix` | upstream Host and path rewrites |
| `basic_auth_user` + `basic_auth_password` | HTTP basic auth. The password is write-only through the API and stored as a bcrypt hash |
| `ip_allow` / `ip_deny` | CIDR lists. A request matching deny, or missing allow, gets 403 |
| `force_https` | redirect http to https |
| `max_body_bytes`, `compression` | request size cap and response compression |
| `oidc` | `{issuer, client_id, client_secret, allowed_emails, allowed_domains}`. Visitors must sign in with that OIDC provider |
| `mtls` | `{client_ca_pem, allowed_cns}`. Requires client certificates on the HTTPS listener |
| `rate_limit` | `{requests, period_seconds, burst, scope}`, see below |

```bash
curl -XPATCH http://127.0.0.1:8081/api/v1/endpoints/ep_123 \
  -H "Authorization: Bearer $MISHMESH_API_AUTH_TOKEN" \
  -d '{"policy":{"ip_allow":["203.0.113.0/24"],"rate_limit":{"requests":100,"period_seconds":60,"burst":20,"scope":"ip"}}}'
```

**`rate_limit`** is a token bucket. `requests` per `period_seconds` refill steadily, and `burst` (default = `requests`) is the bucket size. `scope` is `ip` (default: one bucket per client IP, with IPv6 grouped per /64) or `endpoint` (one shared bucket). When the limit is exceeded the request gets `429` with `Retry-After`. The limit is checked after `force_https` and the IP rules and before authentication, so password guessing is throttled too. In cluster mode the buckets live in Redis and are shared by all pods. If Redis fails, the limiter lets requests through and logs a warning. TCP and TLS-passthrough endpoints are not rate limited.

The client IP used by `ip_allow`, `ip_deny` and `rate_limit` is the socket peer. When the server sits behind a load balancer, set `MISHMESH_TRUSTED_PROXIES` (IPs or CIDRs). The server then uses the rightmost `X-Forwarded-For` hop that is not itself a trusted proxy. Alternatively, keep the client IP at L4 (`externalTrafficPolicy: Local` in the chart).

## Security model

- **Two credentials.** Agents authenticate with agent tokens: random, stored hashed, shown once. Rotating a token revokes all earlier tokens and drops the live session. Revoking an agent kills its connection. The control API accepts either the operator's admin bearer token (`MISHMESH_API_AUTH_TOKEN`) or a browser session (`mm_session` cookie, `Secure` when `PUBLIC_SCHEME=https`). The server refuses to start unless the API has a token or `MISHMESH_API_AUTH_DISABLED=true` is set explicitly.
- **Tenancy.** Every resource belongs to an org, and every route is scoped to the caller's active org. A resource in another org returns 404, never 403. A user with no membership gets 401. Roles are `owner`, `admin` and `member`. In the default policy, owner and admin can do everything and member is read-only. Each org can replace the default with its own Cedar role/action matrix (`GET`/`PUT /api/v1/policy`). The admin bearer token is the platform operator and can target any org with `?org_id=`.
- **Signup and invites.** `MISHMESH_SIGNUP_MODE=org` gives every new account its own org. `invite` lets only the first user self-register, and everyone after that needs an invite. An invite token is single-use, bound to one email address, expires after 7 days, and is stored only as a SHA-256 hash. An inviter cannot grant a role higher than their own. Login and registration are rate limited per IP and per email, cluster-wide. A Google login will not attach to an existing password account with the same email.
- **Custom domains.** With domain verification on, a domain can be bound only after its `_mishmesh-challenge` TXT record proves control, and a verified domain belongs to exactly one org. A CNAME alone is not accepted as proof.
- **Trusted proxies.** `X-Forwarded-For` is ignored unless the peer is listed in `MISHMESH_TRUSTED_PROXIES`, so clients cannot spoof their IP to get around allow/deny lists or rate limits.
- **Reach-in.** Reach-in has three gates: the server flag, an API caller allowed to write agents in that org, and the agent's own deny-first allowlist. The agent always blocks loopback, link-local and metadata addresses.
- **Agentless proxy endpoints** (`method=proxy`) refuse loopback, link-local, multicast and metadata targets and pin the resolved IP. Per-endpoint OIDC refuses private issuer addresses unless `MISHMESH_OIDC_ALLOW_PRIVATE_ISSUERS=true`.
- **Keep the control listener private.** Port `8081` serves agent connects, the API, the UI and `/metrics` (bearer `MISHMESH_METRICS_TOKEN`, or the API token if that is unset). Publish only `/_mishmesh/agent/connect`, with TLS in front of it. The chart's `connectIngress` does exactly that.
- **The cluster relay is authenticated but not encrypted.** Pod-to-pod relay frames carry an HMAC-SHA256 signature (`MISHMESH_CLUSTER_SECRET`, at least 32 characters, with a ±60 s clock-skew window), but the tunnelled bytes travel in cleartext. Keep the relay port (7443) on a private network: a pod network with NetworkPolicy (the chart enables one in the company and SaaS profiles), a private VPC subnet, or a service mesh with mTLS.

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
    ing["ingress<br/>HTTP/HTTPS · TCP ports · TLS SNI · SSH"]
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

- **tunnel** (`internal/tunnel`): the shared transport. WebSocket connection, yamux multiplexing and the wire protocol. Both binaries use it.
- **gateway**: terminates agent sessions, authenticates tokens, registers endpoints and tracks live sessions. Everything else reaches agents through one seam: `store.AgentConn.OpenStream`.
- **ingress**: public HTTP/HTTPS (by subdomain, custom domain, or `/tunnel/{id}` path), the TCP port range, TLS passthrough by SNI, and an optional clientless `ssh -R` front door. It applies endpoint policy, then opens a stream to the agent.
- **control plane**: REST API, auth, orgs, quotas, audit, custom domains and reach-in. See [docs/api.md](docs/api.md).
- **store**: `DataStore` holds durable data (Postgres, or SQLite for dev). `ConnectionStore` holds live sessions (in memory, or Redis for clusters).

## How it scales

- **Identical pods behind a load balancer.** Any pod accepts agent sessions and public traffic on every listener. The load balancer needs no sticky sessions.
- **Shared state.** Postgres holds durable data. Redis holds live routing: which pod owns which agent session (TTL heartbeat, compare-and-delete on removal), TCP port claims and rate-limit buckets.
- **Pod-to-pod relay.** A request that lands on a pod that does not hold the agent is resolved through Redis and relayed to the owner pod, which opens the yamux stream and splices the bytes. HTTP, TCP, TLS, SSH, proxy and reach-in all work the same through this path.
- **Cluster-wide TCP ports.** Every pod pre-binds the whole `TCP_PORT_MIN..TCP_PORT_MAX` range, and a port is claimed once in Redis (`SET NX`), so a public TCP port answers on any pod. Keep the range modest: every pod holds every port, and cloud load balancers limit listeners.
- **HPA.** CPU-based autoscaling (memory optional). Scale-up is immediate. Scale-down is deliberately slow, one pod per period after a stabilization window, because each removed pod drops its agent sessions.
- **Graceful drain.** On SIGTERM a pod flips `/readyz` to 503 so the load balancer stops sending it traffic, lets in-flight streams finish, then closes within a bounded time. `/healthz` stays 200 throughout.
- **Agents reconnect.** Agents reconnect with jittered exponential backoff (no stampede after a rollout) and re-register on whichever pod they reach. That pod takes over ownership, and the old session is kicked cluster-wide over Redis pub/sub.

Cluster mode requires `MISHMESH_CLUSTER_ENABLED=true`, `MISHMESH_CONN_BACKEND=redis`, a `MISHMESH_REDIS_URL`, a Postgres data store, `MISHMESH_RELAY_ADVERTISE` (`podIP:7443`) and `MISHMESH_CLUSTER_SECRET`. The server refuses to start in cluster mode without them. The chart sets all of this when `cluster.enabled=true`. To try two replicas on one machine, use `deploy/compose.cluster.yml` (see [deploy/README.md](deploy/README.md#cluster-mode-multiple-identical-pods)).

### Sizing

TBD: measured numbers from load test

## Server configuration

All settings are environment variables with the `MISHMESH_` prefix. The defaults below are for the binary. The Docker image overrides the listen addresses to `0.0.0.0` and `DATA_DSN` to `/data/mishmesh.db`. The Helm chart sets everything from `values.yaml`, and `extraEnv` covers the rest.

| Var | Default | Meaning |
| --- | --- | --- |
| `BASE_DOMAIN` | `localhost:8080` | public URL suffix (`<sub>.<base>`) |
| `PUBLIC_SCHEME` | `http` | `http` or `https`; also marks cookies `Secure` |
| `INGRESS_ADDR` / `HTTPS_ADDR` / `API_ADDR` | `127.0.0.1:8080` / `:8443` / `:8081` | listeners |
| `DATA_BACKEND` / `DATA_DSN` | inferred / `mishmesh.db` | `postgres` (a `postgres://` DSN selects it) or `sqlite` for dev |
| `CONN_BACKEND` / `REDIS_URL` | `memory` / empty | `redis` for clusters |
| `API_AUTH_TOKEN` | empty | admin bearer token for `/api/v1`; required unless `API_AUTH_DISABLED=true` |
| `BOOTSTRAP_TOKEN` | empty | seed one agent token (`ag_bootstrap` in `org_default`) at startup |
| `AUTH_ENABLED` / `AUTH_PASSWORD_ENABLED` | `false` / `true` | browser login; password on/off (off means Google only) |
| `SIGNUP_MODE` | `org` | `org` or `invite` |
| `DOMAIN_VERIFICATION` | `true` in `org` mode | require TXT proof for custom domains |
| `GOOGLE_CLIENT_ID` / `GOOGLE_CLIENT_SECRET` / `OIDC_REDIRECT_URL` / `OIDC_ISSUER` | | Google login |
| `SESSION_TTL_HOURS` | `168` | login session lifetime |
| `WEBUI_ENABLED` / `WEBUI_DIR` | `false` / empty | serve the SPA (the image bundles it at `/webui`) |
| `INGRESS_ENABLED` | `true` | public ingress (all kinds) |
| `TCP_ENABLED` / `TCP_BIND_HOST` / `TCP_PORT_MIN` / `TCP_PORT_MAX` | `true` / `127.0.0.1` / `10000` / `10100` | public TCP ports |
| `TLS_ENABLED`, `TLS_CERT_FILE` / `TLS_KEY_FILE`, `ACME_ENABLED` / `ACME_EMAIL` / `ACME_CACHE_DIR`, `SELF_SIGNED_TLS` | | HTTPS ingress |
| `TLS_PASSTHROUGH_ENABLED` / `TLS_PASSTHROUGH_ADDR` | `false` / `127.0.0.1:8444` | SNI passthrough for `tls` endpoints |
| `SSH_ENABLED` / `SSH_ADDR` / `SSH_HOST_KEY_FILE` | `false` / `127.0.0.1:2222` | clientless `ssh -R` front door |
| `REACHIN_ENABLED` | `false` | reach-in API |
| `TRUSTED_PROXIES` | empty | IPs/CIDRs whose `X-Forwarded-For` is trusted |
| `QUOTA_MAX_AGENTS` / `QUOTA_MAX_ENDPOINTS` / `QUOTA_MAX_BANDWIDTH_BYTES` | `0` | default per-org quota (0 = unlimited) |
| `METRICS_ENABLED` / `METRICS_TOKEN` | `true` / empty | Prometheus `/metrics` on the control listener |
| `ENDPOINT_OIDC_KEY` | derived from `API_AUTH_TOKEN` | signing key for per-endpoint OIDC cookies |
| `OIDC_ALLOW_PRIVATE_ISSUERS` | `false` | allow endpoint OIDC issuers on private addresses |
| `PROXY_ALLOW_LOOPBACK` | `false` | let `method=proxy` endpoints target the server's loopback |
| `CLUSTER_ENABLED`, `NODE_ID`, `RELAY_ADDR`, `RELAY_ADVERTISE`, `CLUSTER_SECRET` | | [cluster mode](#how-it-scales) |
| `LOG_LEVEL` | `info` | |

Server CLI: `mishmesh-server [serve]`, `mishmesh-server token create --org NAME --name AGENT [--dsn DSN]`, `mishmesh-server version`, `mishmesh-server help`. On startup the server deletes ephemeral endpoints whose agent has no live session. In cluster mode it only deletes those whose agent is owned by no pod.

## Status and roadmap

Version 0.1.0. Built and tested:

- HTTP/HTTPS (subdomain, path, custom domain), WebSocket/SSE streaming, public TCP ports, TLS passthrough, clientless SSH, agentless proxy endpoints
- HTTPS with your own certificate, ACME, or self-signed; custom-domain ownership verification
- endpoint policy, including OIDC, mTLS and rate limits
- orgs, roles, a Cedar policy per org, invites, password and Google login, quotas, audit log, web UI
- agent YAML config with many tunnels per session, `tls` subcommand, `service install`
- HTTP and raw-stream reach-in
- Postgres and Redis backends, cluster mode with pod-to-pod relay, Helm charts for the three shapes, Prometheus metrics

The ship-blocking gaps from the productionization pass are closed. Known limitations:

- No published images or chart repository yet; build them from this repo.
- HTTP reach-in buffers the response (8 MB cap). Use the stream route for large or long transfers.
- An agent needs at least one tunnel to connect, and the server rejects every tunnel when `INGRESS_ENABLED=false`. Until this is fixed, a reach-in-only agent cannot stay connected to an ingress-less server.
- ACME runs on a single pod only. Clusters use a wildcard certificate Secret.
- TCP and TLS-passthrough endpoints have no rate limiting and no in-flight bandwidth metering (HTTP has both).
- Custom domains are not re-verified periodically.
- In `org` signup mode an org owner can change their own org's quota with `PUT /api/v1/quota`. Until that route is restricted to the operator, quotas are a guardrail rather than an enforcement boundary for SaaS tenants.

Next: load-test numbers (see [Sizing](#sizing)), request inspector, webhook verification, endpoint pooling. Not planned for now: UDP, P2P or WireGuard data paths, SAML, multi-region, a Kubernetes operator, billing.

## Development

```bash
make build    # bin/mishmesh-server, bin/mishmesh-agent
make check    # fmt + vet + test (race), run before committing
make test
```

Layout: `cmd/mishmesh-server`, `cmd/mishmesh-agent`, `internal/{tunnel,gateway,agent,ingress,controlplane,cluster,ratelimit,clientip,authz,connect,metrics,config}`, `internal/store/{sqlite,postgres,memory,redis}`, `web/` (React UI), `deploy/` (compose files, Helm charts, examples). Product background: [docs/prd.md](docs/prd.md).
