# Lab network control — design

**Date:** 2026-10-12
**Status:** approved for build (Nir: "implement all of this")
**Driver:** run every network path of a self-hosted lab from mishmesh: what is published, through which front door, to which backend, whether that backend is up, asleep, or down, and wake it on demand.

## 1. Problem

On the reference homelab (`lab-z170mx`) traffic is:

```
browser → Cloudflare Access → tunnel "lab" → cloudflared (user systemd)
        → mishmesh ingress 127.0.0.1:8090 → proxy endpoint → 127.0.0.1:<port>
```

Gaps observed live on 2026-10-12:

- `online: true` for portainer, syncthing and hermes while their containers had exited. For `method=proxy` "online" means "the synthetic `ag_proxy` agent exists", never "the backend answers".
- Publishing a service is four manual steps across mishmesh, tunnel ingress, DNS and the Access application.
- Services that are rarely used run all the time. There is no way to stop them when idle and start them on the next request.
- TLS origins (Portainer, Cockpit, Vaultwarden) are self-signed. Issue #19: `proxy_target` cannot be `https://`.
- No single place shows what connects to what.

## 2. Scope

| # | Piece | Package |
|---|---|---|
| A | Backend health: probe every endpoint target, report `up / down / sleeping / waking / offline / unknown` | `internal/health` |
| B | Load balancing and failover across several proxy targets | `internal/connect/proxy` |
| C | Origin TLS: `https://` targets, verified by default; custom CA, SPKI pin, or explicit insecure per endpoint (#19) | `internal/connect/proxy` |
| D | Services: host-local lifecycle drivers (`docker`, `compose`, `exec`), wake on request, stop when idle | `internal/services` |
| E | Cloudflare front door: tunnel status, sync of tunnel ingress, DNS and Access hostnames for published endpoints, optional `cloudflared` supervision | `internal/cloudflare` |
| F | Topology and control API: `/api/v1/topology`, `/api/v1/services`, `/api/v1/frontdoors` | `internal/controlplane` |
| G | Web UI: status badges, Services page, Topology page, proxy-aware reserve dialog (#17) | `web/` |
| H | Bug fixes: #18 reject unimplemented methods, #20 metrics auth | `controlplane`, `cmd/mishmesh-server` |

Out of scope, tracked as issues: services on remote agent hosts, Tailscale method, L4 (TCP) load balancing, active HTTP health paths beyond a status-code check, Cloudflare Access policy authoring.

## 3. Endpoint policy additions

Stored in the existing JSON `policy` column, so no migration on SQLite or Postgres.

```json
{
  "proxy_target": "https://127.0.0.1:9443",
  "proxy_targets": ["10.0.0.5:8080", "10.0.0.6:8080"],
  "load_balance": "round_robin | failover",
  "origin_tls": {
    "server_name": "portainer.local",
    "ca_pem": "-----BEGIN CERTIFICATE-----…",
    "pin_sha256": ["base64 SHA-256 of the leaf SPKI"],
    "insecure_skip_verify": false
  },
  "service": "portainer",
  "front_door": "cloudflare"
}
```

`EndpointPolicy.Targets()` returns `proxy_target` followed by `proxy_targets`, deduplicated.

## 4. Health (A)

- A prober loop runs every `MISHMESH_HEALTH_INTERVAL` (default 15s) with a per-probe timeout (default 3s).
- Proxy endpoints: TCP connect to every target, through the proxy guard. `https://` targets add a TLS handshake under the endpoint's origin TLS rules, so a certificate problem shows up as `down` with a reason.
- Native and SSH endpoints: `offline` when no agent is bound, else `up`. Deep probing through agents is a follow-up.
- An endpoint with a `service` whose manager state is `stopped` because of idle shutdown reports `sleeping`. While a start is in flight it reports `waking`.
- The state lives in memory, keyed by endpoint ID, with a per-target breakdown, latency, last check time and last error. The API reads it. Load balancing reads per-target health.

## 5. Load balancing (B)

- Targets come from `Targets()`. `round_robin` (default when there is more than one target) rotates across healthy targets. `failover` always takes the first healthy target in order.
- Unknown health counts as healthy. When every target is unhealthy, all of them are tried in order.
- A dial error moves on to the next target in the same request. This is safe because nothing has been written yet.
- HTTP only for v1. TCP and TLS-passthrough endpoints use the first target.

## 6. Origin TLS (C)

- An `https://` target makes the proxy connector wrap the dialed connection in `tls.Client`. It dials the IP that the guard resolved and validated. `ServerName` comes from `origin_tls.server_name`, or else from the configured host. When the host is an IP literal, verification uses IP SANs.
- Verification order:
  1. `insecure_skip_verify` skips all checks. It is explicit and visible in the API.
  2. Otherwise, when `pin_sha256` is set, the leaf SPKI hash must match one pin. The chain is not checked, which is how a self-signed origin is trusted without blanket insecurity.
  3. Otherwise the chain is checked against `ca_pem` when set, else against system roots.
- ALPN is `http/1.1`. The ingress keeps speaking HTTP/1.1 over the wrapped stream.
- The API returns `origin_tls` unredacted. Nothing in it is a secret.

## 7. Services (D)

### Config: host-local, never via API

`MISHMESH_SERVICES_FILE` (YAML). The API can only list, start and stop services by name. Commands cannot be defined or changed remotely, because that would turn a control-API credential into a shell on the host.

```yaml
orgs: [org_default]
docker:
  host: unix:///var/run/docker.sock
defaults:
  idle_timeout: 30m
  start_timeout: 120s
services:
  portainer:
    driver: docker
    docker: { containers: [portainer] }
    idle_timeout: 2h
  affine:
    driver: compose
    compose: { project: affine, file: /opt/homelab/stacks/affine/compose.yaml }
  notebook:
    driver: exec
    exec:
      start: ["/usr/local/bin/notebook-up"]
      stop:  ["/usr/local/bin/notebook-down"]
      status: ["/usr/local/bin/notebook-status"]
      dir: /srv/notebook
      env: { FOO: bar }
  never-sleep:
    driver: docker
    docker: { label: "mishmesh.service=cockpit" }
    idle_timeout: 0
```

### Drivers

| Driver | Start | Stop | State |
|---|---|---|---|
| `docker` | Engine API `POST /containers/{id}/start` for each container (by name or label) | `POST /containers/{id}/stop` | inspect: all running = running, none = stopped, mixed = partial |
| `compose` | Engine API start of containers labelled `com.docker.compose.project=<project>`. If none exist and `file` is set: `docker compose -f file -p project up -d` | stop by label | by label, like `docker` |
| `exec` | run `start` argv | run `stop` argv | `status` argv exit 0 = running, else stopped. Without `status`: unknown, so the readiness probe decides |

The Docker client speaks the Engine API over the unix socket (or `tcp://` for a socket proxy) with `net/http`. It has no SDK dependency, so it runs the same on the host or in a container. When mishmesh runs in a container, mount the socket through a socket proxy that only allows `containers/*/start|stop|json` and `containers/json`. A raw socket mount gives root on the host.

### Lifecycle

- **Wake.** `EnsureRunning(ctx, name)` is collapsed per service so only one start runs at a time. It calls the driver's `Start`, then polls readiness (the service's `ready.tcp`, else the endpoint's first target) until it answers or `start_timeout` passes.
- **Track.** Ingress calls `Acquire(name)` when a request or upgraded connection starts and the returned release function when it ends. The manager keeps an active count and the time of last activity.
- **Idle stop.** Every 30s, a service with `idle_timeout > 0` that has zero active connections, has been idle past the timeout, and was not started outside mishmesh within the timeout is stopped and marked `sleeping`. A service mishmesh never saw traffic for gets one full idle period of grace after mishmesh starts. A service that was manually stopped is not restarted except by a request.
- **Failures.** A start failure or readiness timeout is recorded with its reason. Requests get 503 and the last error.

### Ingress behaviour

When an endpoint has a `service` and the service is not running:

- **Browser navigation** (`GET`, `Accept` includes `text/html`, not an upgrade): start in the background and answer 503 with a small "Starting *name*…" page that refreshes every 2s, plus `Retry-After: 2`.
- **Anything else** (APIs, websockets, gRPC): hold the request until ready, up to `start_timeout` (default 120s, under Cloudflare's 100s edge timeout is the operator's call), then proxy. On timeout: 503 with `Retry-After`.
- Policy gates (IP allow, basic auth, OIDC, rate limit) run before the wake. An unauthenticated request never wakes anything.

## 8. Cloudflare front door (E)

### Config

```
MISHMESH_CLOUDFLARE_API_TOKEN      required to enable
MISHMESH_CLOUDFLARE_ACCOUNT_ID     required
MISHMESH_CLOUDFLARE_TUNNEL_ID      required
MISHMESH_CLOUDFLARE_ORIGIN         default http://<MISHMESH_INGRESS_ADDR>
MISHMESH_CLOUDFLARE_ACCESS_APP_ID  optional: add published hostnames to this Access app
MISHMESH_CLOUDFLARE_PRUNE          default false: remove hostnames mishmesh owns but no endpoint wants
MISHMESH_CLOUDFLARE_SYNC_INTERVAL  default 5m
MISHMESH_CLOUDFLARE_ORGS           default org_default
MISHMESH_CLOUDFLARED_RUN           default false: supervise a local cloudflared
MISHMESH_CLOUDFLARED_BIN           default cloudflared
MISHMESH_CLOUDFLARED_TOKEN_FILE    tunnel token for supervised mode
```

Token permissions needed: Account › Cloudflare Tunnel: Edit, Zone › DNS: Edit, and Account › Access: Apps and Policies: Edit when an Access app is set.

### Desired state

Every endpoint in an allowed org with `front_door: "cloudflare"` and a custom `domain`.

### Reconcile

Runs at startup, every sync interval, and after each endpoint create, patch or delete.

1. **Tunnel ingress** (`GET/PUT /accounts/{a}/cfd_tunnel/{t}/configurations`). mishmesh owns rules whose `service` equals the origin. It adds a rule per desired hostname, keeps every rule it does not own untouched and in order, and keeps the catch-all last. Owned rules with no endpoint are reported as `orphaned` and removed only when prune is on. The PUT is skipped when nothing changed.
2. **DNS.** The zone is found by the longest matching zone name. A missing record gets a proxied CNAME to `<tunnel>.cfargotunnel.com`. A record that exists with other content is a `conflict`, reported and never overwritten. Prune deletes only CNAMEs that point at this tunnel.
3. **Access** (optional). The hostname is added to the app's `self_hosted_domains` (and `destinations` when present). Hostnames are removed only with prune.

### Status

The tunnel's `status` (`healthy`, `degraded`, `down`, `inactive`), its connections (colo, origin IP, client version, opened at), and per hostname: `ingress`, `dns`, `access`, `state` (`synced`, `pending`, `conflict`, `orphaned`, `error`) with a detail. The last sync time and error are included too.

### Supervisor

Optional. It runs `cloudflared tunnel --no-autoupdate --metrics 127.0.0.1:<port> run` with `TUNNEL_TOKEN` taken from the token file and passed in the environment, never on argv. It restarts with exponential backoff capped at 60s. It reports the local process state and the result of `GET /ready` on the metrics port.

## 9. API (F)

| Route | Authz | What |
|---|---|---|
| `GET /api/v1/endpoints[/{id}]` | endpoint:read | adds a `health` object: `state`, `detail`, `checked_at`, `latency_ms`, `targets[]`, `service` |
| `GET /api/v1/services` | endpoint:read | configured services visible to the org: driver, state, active, last_activity, idle_timeout, last_error, endpoints |
| `POST /api/v1/services/{name}/start` | endpoint:write | starts and waits until ready, up to start_timeout |
| `POST /api/v1/services/{name}/stop` | endpoint:write | stops and marks the service sleeping |
| `GET /api/v1/frontdoors` | endpoint:read | Cloudflare status (§8). `enabled: false` when not configured |
| `POST /api/v1/frontdoors/cloudflare/sync` | endpoint:write | reconciles now and returns the status |
| `GET /api/v1/topology` | endpoint:read | nodes (`frontdoor`, `hostname`, `endpoint`, `target`, `service`, `agent`) and edges, each node with a state |

Validation:

- `service` must name a configured service visible to the org.
- `front_door` must be `cloudflare`, with Cloudflare enabled for the org and a custom `domain` set.
- Every target in `Targets()` passes the guard.
- `origin_tls.ca_pem` must parse.
- Each pin is a 32-byte base64 value.
- `load_balance` is empty or one of the two modes.

## 10. Fixes

- **#18:** `method` `tailscale` and `cloudflare` are rejected with 400 "not implemented". Cloudflare is a front door, not an endpoint method.
- **#19:** covered by §6.
- **#20:** without a metrics token, `/metrics` is served only when the API listener is loopback-bound **and** the request arrives from loopback with no forwarding headers. Otherwise it is 401. Cloudflared and an ingress hop add forwarding headers, which closes the "published UI publishes metrics" hole.
- **#17:** the reserve dialog offers Agent or Proxy. Proxy asks for target(s), domain or subdomain, optional service and front door.

## 11. Testing

- **Unit:** guard plus multi-target selection, the TLS verify modes against an `httptest` TLS server (good chain, pin match and mismatch, CA, insecure), docker driver against a fake Engine API over a unix socket or `httptest`, the exec driver, manager wake collapsing and idle stop with a fake clock, Cloudflare reconcile against a fake API (rule ownership and order, conflict, prune off and on, no-op PUT skipped), and the ingress waking page and hold path.
- **Live on `lab-z170mx`:** deploy the branch build, point the 12 endpoints at services and the front door, then:
  - stop portainer and see `down`
  - set a 1m idle on syncthing, watch it go to `sleeping`, open the URL, and see the waking page, then the app
  - see per-target health on a two-target endpoint with one target dead
  - see the https Portainer origin verified with a pin.
