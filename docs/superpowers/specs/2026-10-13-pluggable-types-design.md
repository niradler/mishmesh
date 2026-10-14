# Pluggable types: connections, service drivers, requirements

Status: approved 2026-10-13; app-access scope confirmed 2026-10-14 · follows `2026-10-12-network-control-ux-design.md`

2026-10-14 direction: expose applications over existing networks. Cloudflare and Tailscale ship as access providers; NetBird and existing WireGuard networks are extension cases. VPN peer/key/route/policy management is outside this release. Generic private scope is `private`; provider-specific tailnet configuration remains owned by Tailscale. Existing agent-to-agent and UDP access on main must survive integration.

## Why

Two parts of mishmesh are hardcoded to one implementation each.

- **Cloudflare is a page, not a type.** There is one Cloudflare connection per org, its own table, its own API (`/api/v1/cloudflare`) and its own sidebar entry. Endpoints store `public_access: "cloudflare"`. Adding Tailscale today means copying all of it.
- **Services know three drivers by name.** `services.Spec` is a closed struct (`Docker`, `Compose`, `Exec`), validation and `UsesHostCommands` switch on the driver, and the UI form, the service page card and `web/src/lib/services.ts` each branch on `docker | compose | exec`. Adding Kubernetes touches about six places. The service page shows Compose words ("Compose file", "Services") as if they were the service's own.

A third need came up: some services depend on others. Guacamole is only useful while the desktop (`gdm` + `gnome-remote-desktop`) runs, and the desktop should only run while Guacamole does.

## Goals

1. Connections are added by type. Cloudflare Tunnel, Tailscale and Kubernetes cluster ship as the first three types; a new type is one Go package plus registration.
2. Service drivers are added by type. Each driver describes its own fields; the UI renders forms and detail cards from those descriptions with no per-driver code. Docker, Compose, Command and Kubernetes ship.
3. A service can require other services. Waking it wakes its requirements first; when it sleeps, requirements nobody else needs sleep too.
4. Readiness is generic: TCP or HTTP, owned by the service, not the driver.

Non-goals: Tailscale Funnel (see Tailscale), Docker on remote hosts, React UI redesign beyond the pages named here, backward compatibility of the API or DB layout (single user; data is migrated once).

## Vocabulary

| Term | Meaning |
|---|---|
| Connection | An external system mishmesh talks to, of one **type**. Has a role: **access** (carries traffic to endpoints) or **runtime** (runs services). |
| Access | An endpoint reachable through an access connection. Scope is **public** or **private**. Replaces "Public access" as the section name; provider-specific tailnet facts belong to the Tailscale panel. |
| Driver | How a service is started, stopped and inspected. Driver fields use the driver's own words (Compose services, Kubernetes workload). |
| Requires | Service A requires service B: B runs whenever A runs. |

## Connections

### Data

```
connections (
  id          TEXT PRIMARY KEY,           -- conn_xxx
  org_id      TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  type        TEXT NOT NULL,              -- cloudflare | tailscale | kubernetes
  name        TEXT NOT NULL,
  config      TEXT NOT NULL,              -- JSON, type-owned, no secrets
  secrets_enc BLOB,                       -- secrets.Box sealed JSON, type-owned
  created_at  INTEGER NOT NULL,
  updated_at  INTEGER NOT NULL,
  UNIQUE (org_id, name)
)
```

`cloudflare_connections` is dropped. A startup migration copies its row into `connections` (type `cloudflare`, same name, secrets re-sealed as one JSON blob). Several connections of the same type per org are allowed.

`store.DataStore` swaps the four Cloudflare methods for `CreateConnection`, `GetConnection`, `ListConnections(orgID)`, `ListAllConnections`, `UpdateConnection`, `DeleteConnection`. SQLite and Postgres both implement it.

### Provider interface (`internal/connections`)

```go
type Provider interface {
    Describe() Descriptor
    Validate(cfg Config) error
    Detect(ctx context.Context, in json.RawMessage) (json.RawMessage, error)
    Open(cfg Config, deps Deps) (Instance, error)
}

type Instance interface {
    Run(ctx context.Context)
    Sync(ctx context.Context) (Status, error)
    Status() Status
    Close()
}
```

`Config` is the stored row with secrets opened. `Deps` carries the endpoint lister, the ingress origin and the logger. `Detect` is optional per type (Cloudflare reads a running cloudflared; Tailscale reads the local tailscaled).

`Descriptor` drives the UI:

```go
type Descriptor struct {
    Type    string   // "cloudflare"
    Label   string   // "Cloudflare Tunnel"
    Role    string   // "access" | "runtime"
    Scope   string   // "public" | "private" | "" for runtime
    Levels  []string // e.g. imported, managed
    Fields  []Field  // see Field below, shared with drivers
    Help    string
}
```

`Status` generalises today's `cloudflare.Status`: `level`, `health` (`healthy | degraded | down | unknown`), `last_error`, `last_sync`, `hostnames []HostStatus` (host, state `published|pending|unclaimed|conflict|error`, endpoint_id, detail, fix) and `details map[string]any` for type-only facts (Cloudflare tunnel connections, Tailscale node name, cluster version).

The Cloudflare `Hub` becomes `connections.Hub`: one `Instance` per connection instead of per org, same reload, sync serialisation (`syncMu` per instance) and async `Run`.

### Endpoint access

`EndpointPolicy.PublicAccess string` is replaced by:

```go
Access []AccessRef `json:"access,omitempty"`

type AccessRef struct {
    ConnectionID string `json:"connection_id"`
    Hostname     string `json:"hostname,omitempty"`
}
```

- Cloudflare: `hostname` defaults to the endpoint domain.
- Tailscale: `hostname` defaults to the endpoint's first domain label (`notes.niradler.com` → `svc:notes` → `notes.<tailnet>.ts.net`).
- An endpoint can have several refs (public through Cloudflare and tailnet through Tailscale at once).
- Validation: the connection exists in the org, has role `access`, and the hostname is unique per connection. The "connection required only when newly turned on" rule from the previous spec applies per ref.
- Ingress learns access hostnames as aliases of the endpoint, so a request with `Host: notes.<tailnet>.ts.net` routes to the same endpoint as `notes.niradler.com`.
- `gateway.dropControlPlaneFields` strips `access` like it stripped `public_access`.
- The migration rewrites `public_access: "cloudflare"` to an `access` ref to the migrated Cloudflare connection.

### API

| Route | Action |
|---|---|
| `GET /api/v1/connection-types` | descriptors |
| `GET /api/v1/connections` | list with status |
| `POST /api/v1/connections` | create (`type`, `name`, `config`, `secrets`) |
| `GET/PUT/DELETE /api/v1/connections/{id}` | read, replace, delete (409 while any endpoint or service references it) |
| `POST /api/v1/connections/detect` | `type` + type input |
| `POST /api/v1/connections/{id}/sync` | sync now |

Authz: `connection:manage` replaces `cloudflare:manage` (owner and admin). Reads need `endpoint:read`. Secrets are never returned; responses carry `has_<secret>` flags.

`/api/v1/cloudflare*` is removed. `cf-import.py` moves to the new routes.

### Type: Cloudflare Tunnel

Today's behaviour, moved behind the interface: levels imported and managed, cloudflared supervisor for the managed connector, DNS + Access reconcile, unclaimed and conflict reporting. No behaviour change.

### Type: Tailscale (tailnet access)

- **Mechanism.** Each access ref becomes a Tailscale Service `svc:<name>` whose host is the machine running mishmesh, serving `https:443` to the ingress origin. Clients in the tailnet reach `https://<name>.<tailnet>.ts.net`; tailscaled terminates TLS with its own certificate and forwards to mishmesh ingress with the original Host.
- **Local control.** mishmesh talks to tailscaled through its LocalAPI (`tailscale.com/client/local`, the Unix socket) to read and write the serve config. No shell commands.
- **Levels.**
  - imported: reads serve config and service status, reports which endpoints are served, never writes.
  - managed: also writes the serve config for `svc:` entries it owns and, with an OAuth client secret, defines and removes the Services in the tailnet through the Tailscale API (`/tailnet/{tailnet}/services/{name}`). It never touches serve entries it did not create.
- **Preconditions shown as fixes:** tailscaled ≥ 1.86 reachable on the socket, the node is tagged (Services cannot be hosted by a user-owned node), the host is approved for each Service or an autoApprover covers the tag.
- **Funnel is out of scope.** Funnel only serves the node's own hostname on ports 443, 8443 and 10000, so it cannot give each endpoint its own name. Cloudflare stays the public path.

### Type: Kubernetes cluster (runtime)

- Config: `mode` `in_cluster | kubeconfig`, `context`, `default_namespace`. Secret: `kubeconfig` (sealed).
- Uses `k8s.io/client-go`. A kubeconfig whose user runs an `exec` credential plugin is a host command and is refused unless host access (`MISHMESH_HOST_COMMANDS` + `host:exec`) is available, matching the existing rule.
- Status: API server reachable, server version, permission check (`SelfSubjectAccessReview` for `get/patch` on `deployments/scale` and `statefulsets/scale` in the default namespace).

## Service drivers

### Field descriptions (shared with connections)

```go
type Field struct {
    Key         string   // "file"
    Label       string   // "Compose file"
    Kind        string   // text | list | keyvalue | secret | select | number | bool | connection
    Options     []string // select
    ConnType    string   // connection: which connection type to pick from
    Required    bool
    Placeholder string
    Help        string
    HostCommand bool     // setting this field makes the service run host commands
}
```

`HostCommand` replaces `UsesHostCommands`: the server checks host access when any marked field is set (Command `start/stop/status`, Compose `file`, Compose `bin`).

### Driver interface (`internal/services`)

```go
type DriverType interface {
    Describe() DriverDescriptor        // type, label, help, fields
    Validate(cfg json.RawMessage) error
    New(cfg json.RawMessage, deps Deps) (Driver, error)
}
```

`Driver` stays `Start`, `Stop`, `State`. `Deps` gives the Docker client and the connection resolver (Kubernetes looks up its connection). The manager builds drivers through a registry keyed by type; `hostDrivers.build`'s switch disappears.

### Data

`services` keeps its table with `spec` replaced by three columns:

| Column | Content |
|---|---|
| `config` | driver-owned JSON (today's `spec.docker` / `spec.compose` / `spec.exec` object, flattened) |
| `ready` | `{"tcp":"127.0.0.1:8088"}` or `{"http":{"url":"http://127.0.0.1:8088/","status":200}}` or empty (use the linked endpoint's first target, as today) |
| `requires` | separate table `service_requires(service_id, requires_id)`, both FK to `services` |

The migration splits existing `spec` rows. Driver id `exec` is renamed `command`.

### Drivers

| Driver | Fields | Start / Stop / State |
|---|---|---|
| Docker | Containers (list) or Label | as today |
| Compose | Project, Compose services (list, empty = whole project), Compose file*, Binary* | as today |
| Command | Start*, Stop*, Status*, Working dir, Environment (keyvalue, secret values redacted) | as today (`exec`) |
| Kubernetes | Cluster (connection), Namespace, Kind (Deployment, StatefulSet), Name, Replicas when awake (default 1) | scale subresource to N / to 0; Running when ready replicas ≥ N > 0, Stopped when spec and ready are 0, Partial otherwise |

\* host-command field.

For Kubernetes the endpoint's proxy target must be reachable from mishmesh (cluster service DNS when mishmesh runs in-cluster, otherwise a NodePort, an ingress or an agent). The service page says so when the target is a cluster-internal name and mishmesh is not in-cluster.

### API

`GET /api/v1/service-drivers` returns descriptors. Service bodies become `{name, driver, config, ready, requires: [ids], idle_timeout_seconds, start_timeout_seconds}`.

## Requires

- **Model.** `requires` is a set of service ids in the same org. Saving rejects unknown ids, self-reference and cycles (DFS over the stored graph plus the change).
- **Wake.** `EnsureRunning(A)` first runs `EnsureRunning` on each requirement (in parallel, each with its own readiness and start timeout), then starts A. If a requirement fails, A fails with `requirement "desktop-gui" failed: <error>` and is not started.
- **Keep awake.** A running service counts as activity on each requirement, so a requirement does not idle-stop while a dependent runs (implemented as an `Acquire` held from the dependent's start until its stop).
- **Sleep.** When A stops (idle or manual), it releases its holds. A requirement with no other holds and no recent own traffic stops on the next idle pass if its own idle timeout is set, or immediately if its idle timeout is "With dependents" (new idle option, the default for services that are only ever required).
- **Manual stop of a requirement** while dependents run returns 409 `required by guacamole (running)`; the UI offers "Stop guacamole too".
- **Delete** of a service another service requires returns 409, like the existing linked-endpoint guard.
- **Map.** New edge kind `requires` (service → service).

## UI

- **Sidebar:** Dashboard · Network · Endpoints · Services · Connections · Agents · Members · Audit · Settings. "Cloudflare" is removed.
- **Connections page:** table (name, type badge, role, level, health, served hostnames). "Add connection" opens a type picker (cards from descriptors), then a form rendered from fields, with Detect where the type supports it.
- **Connection detail:** generic header (type, level, health, last sync, Sync now), the hostnames table (today's `HostnamesTable`, generalised), then a type panel. Cloudflare keeps its management section as its type panel; Tailscale and Kubernetes get small panels for node or cluster facts.
- **Endpoint detail, Access section:** one row per access ref ("Public access · Cloudflare lab · niradler.com hostname", "Tailnet access · Tailscale home · notes.tail1234.ts.net") with add/remove; adding picks from access connections.
- **Add service:** driver picker, then the form from fields; generic sections below (Readiness, Requires, Idle timeout, Start timeout).
- **Service detail:** generic header, a "Driver: Compose" card rendered from fields, a Requires card (requires and required-by, both linked), linked endpoints.
- **Map:** connection nodes per connection (linked to their pages), `requires` edges.
- Help popovers come from descriptor help text; glossary entries for Connection, Access, Driver, Requires.

## Lab rollout

1. Deploy and migrate: the Cloudflare connection and the 12 access refs carry over; `lab-e2e.py` passes unchanged except for the new routes.
2. **Desktop for Guacamole** (needs your approval): a Command service `desktop-gui` with start `sudo systemctl start gdm gnome-remote-desktop`, stop `sudo systemctl stop gnome-remote-desktop gdm`, status `systemctl is-active gnome-remote-desktop`, idle "With dependents"; guacamole requires it. Needs `MISHMESH_HOST_COMMANDS=true` and a sudoers drop-in allowing only those two commands for `nir` without a password.
3. **Tailscale** (needs your action): tailscale is not installed on the lab. Install it, join with a tagged auth key, approve the host (or add an autoApprover for the tag); then add a Tailscale connection and turn on tailnet access for one endpoint.
4. **Kubernetes:** no cluster on the lab. Validate with a throwaway `kind` cluster in Docker running a one-replica nginx Deployment, removed afterwards.

## Testing

- Store: connections and services round-trip on SQLite and Postgres; migration from the current schema with real lab-shaped rows.
- Connections: Cloudflare tests move unchanged behind the interface; Tailscale tested against a fake LocalAPI and fake Tailscale API; Kubernetes connection status against client-go fake.
- Drivers: registry builds each driver; descriptor fields match `Validate`; host-command fields enforced; Kubernetes driver against client-go fake clientset (scale up/down, partial, missing workload).
- Requires: cycle detection, wake order, failed requirement, keep-awake hold, release and stop, 409 guards, shared requirement stays up while one dependent runs.
- API: authz matrix updated (`connection:manage`), tenancy tests for every new route.
- Web: forms rendered from descriptors for each driver and connection type.
- Lab: `lab-e2e.py` extended with connections, a Requires wake (guacamole wakes desktop-gui) and the kind cluster wake.

## Open points for review

- Name of the sidebar page: **Connections** (proposed).
- Funnel stays out; public access is Cloudflare only.
- Docker stays local-only (no "Docker host" connection type yet).
