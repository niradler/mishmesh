# Network control: services, public access and a connected UI — design

**Date:** 2026-10-12
**Status:** approved (Nir: "ur design sounds good … take this to prod")
**Supersedes:** the UI sections of `2026-10-12-lab-network-control-design.md`, the services file, and the env-driven Cloudflare integration.

## 1. Problem

Nir rejected the first UI after using it on the lab:

- Services had no add/edit/delete. They lived in a YAML file on the host.
- Linking an endpoint to a service was a select hidden in a collapsed form section. A service could not be linked from its own side.
- Nothing showed the path a request takes: Cloudflare tunnel → hostname → endpoint → targets → service.
- Method, Lifecycle and Kind showed as bare badges with no explanation.
- The Cloudflare integration had an unclear name and no place in the request path.
- Cloudflare could only be enabled with env vars and a new API token, even though a working tunnel already runs on the lab.

## 2. Model and names

| Thing | What it is | Page |
|---|---|---|
| **Endpoint** | A public address and how traffic reaches it | Endpoints, Endpoint detail |
| **Service** | Something mishmesh starts and stops: container, compose stack, commands | Services, Service detail |
| **Public access** | How the internet reaches an endpoint. Today the only provider is a Cloudflare tunnel | Cloudflare page; a card on each endpoint |
| **Agent** | A mishmesh-agent process that tunnels traffic from another network | Agents, Agent detail |

There are no tabs that hide resources. Every resource reference anywhere in the UI is a link to its detail page, including map nodes.

## 3. Services in the database

### Data

New table `services`:

| Column | Type |
|---|---|
| `id` | `svc_…` |
| `org_id` | |
| `name` | unique per org; same pattern as before |
| `driver` | `docker`, `compose` or `exec` |
| `spec` | JSON (below) |
| `idle_timeout_seconds` | `0` = never stop |
| `start_timeout_seconds` | |
| `created_at`, `updated_at` | |

`spec` holds:

- `docker`: `containers[]` or `label`
- `compose`: `project`, `services[]`, `file`
- `exec`: `start[]`, `stop[]`, `status[]`, `dir`, `env{}`
- `ready_tcp`

`store.DataStore` gains `CreateService`, `GetService`, `ListServices` (per org), `ListAllServices`, `UpdateService` and `DeleteService`. These are implemented on SQLite and Postgres and run through the shared store conformance tests.

An endpoint links a service by ID with `policy.service_id`, so renaming a service never breaks its links.

### Host commands

Two settings make the server run commands from data on the host:

- the `exec` driver;
- compose `file`, because `docker compose up` with a file the caller chooses can mount any host path.

Creating, updating or deleting a service that uses either needs the server flag `MISHMESH_HOST_COMMANDS=true` (default on in `invite` signup mode) and the new action `host:exec`. Updating a service that already uses host commands needs both even when the new definition does not. By default only the owner has `host:exec`. Starting and stopping existing containers needs neither. `exec.env` values are returned as `<redacted>` to callers without `host:exec`.

The Docker engine address is server config `MISHMESH_DOCKER_HOST`, default `unix:///var/run/docker.sock`. It is never per service, so the API cannot point the server at an arbitrary engine.

### Runtime

`services.Manager` is keyed by service ID. `Manager.Apply(records)` reconciles in memory:

- new records get a driver;
- changed records swap config and driver but keep runtime state (`active`, `lastActivity`, `stoppedByUs`);
- deleted records are dropped, and an in-flight start finishes against its own copy.

The control API calls `Apply` after every write. The server loads all records at startup. `VisibleTo(org, id)` checks the record's org.

### Removed

The services file, `services.LoadFile` and the per-file org allowlist. A legacy services YAML (the old `MISHMESH_SERVICES_FILE` input) is imported once with `.claude/scripts/import-services.py`, which only creates or updates services by name. Endpoints are linked separately with `.claude/scripts/lab-endpoints.py`, which sets `policy.service_id`. Both scripts send `Authorization: Bearer $MISHMESH_API_AUTH_TOKEN` when that variable is set.

### API

| Route | Action |
|---|---|
| `GET /api/v1/services` | `service:read`; status plus linked endpoint IDs |
| `POST /api/v1/services` | `service:write` (+ host access for host commands) |
| `GET /api/v1/services/{id}` | `service:read` |
| `PUT /api/v1/services/{id}` | `service:write` (+ host access for host commands) |
| `DELETE /api/v1/services/{id}` | `service:write` (+ host access for host commands); 409 lists the linked endpoints while any remain |
| `POST /api/v1/services/{id}/start` | `service:write` |
| `POST /api/v1/services/{id}/stop` | `service:write` |

Linking stays on the endpoint (`PATCH /endpoints/{id}` with `policy.service_id`). The service page links an endpoint by patching that endpoint.

`GET /api/v1/capabilities` reports `host_commands` so the UI hides exec and compose `file` when the server or role cannot use them.

## 4. Public access through Cloudflare

### Connection

A per-org Cloudflare connection is stored in the database, in table `cloudflare_connections` (one row per org).

| Field | Meaning |
|---|---|
| `name` | Display name, e.g. `lab` |
| `account_id`, `tunnel_id` | Decoded from the tunnel token |
| `tunnel_token` | Encrypted. Required on the first save; the IDs are decoded from it, and a `managed` connector runs cloudflared with it |
| `api_token` | Encrypted, optional. Turns on management |
| `access_app_id` | Optional |
| `origin` | Default derived from `MISHMESH_INGRESS_ADDR`; an unspecified host (`:8080`, `0.0.0.0`, `[::]`) becomes `127.0.0.1` |
| `connector` | `external` (a cloudflared already running) or `managed` (mishmesh runs it) |
| `metrics_addr` | Default `127.0.0.1:20241`. Must be a loopback address, because the server fetches it |
| `prune` | Remove hostnames no endpoint wants |

Secrets are encrypted with AES-256-GCM. The key comes from `MISHMESH_SECRET_KEY_FILE`, default `mishmesh-secret.key` next to the SQLite database (in the working directory for Postgres or an in-memory SQLite DSN), created with mode 0600 on first start. The API never returns secrets; responses carry only `api_token_set` and `tunnel_token_set`.

All `MISHMESH_CLOUDFLARE_*` env vars and the org allowlist are removed. `MISHMESH_CLOUDFLARED_BIN` stays: it names the binary a `managed` connector runs.

### Two levels, picked automatically

| Level | Needs | Gives |
|---|---|---|
| **Imported** | Tunnel token + a reachable cloudflared metrics address | Account and tunnel identity; live hostnames read from cloudflared `/config`; connections and edge locations from `/ready` and `/metrics`; per-endpoint published / not published; exact manual instructions for anything missing |
| **Managed** | Imported + API token (Tunnel Edit, DNS Edit, Access Apps Edit) | Everything above, plus the existing reconciler: ingress rules, DNS CNAMEs, Access hostnames, tunnel name and status from the API |

Import never changes Cloudflare.

Hostnames that already exist on the tunnel are adopted. When an endpoint's domain matches, it is shown as published. Hostnames no endpoint publishes are listed as **unclaimed**. When one routes to the origin and an endpoint with Public access off has that domain, it carries `endpoint_id`, the detail "an endpoint serves this domain but its Public access is off" and a fix to turn Public access on; the others get a "Create endpoint" action. Managed mode creates only missing rules. Prune is off by default; when on, it removes only rules to the origin that no endpoint wants, and never the rule for a hostname served by an endpoint with Public access off.

### Connector

- `external`: mishmesh only reads the metrics address. The lab starts here, with cloudflared still run by `~/.cloudflared/start.sh`.
- `managed`: mishmesh runs cloudflared with the stored tunnel token, passed in the child's environment and never on argv. This is the existing `Supervisor`. Switching from external to managed is Nir's step: he stops his own cloudflared loop.

### Endpoint field

`policy.public_access` is `""` or `"cloudflare"`. It always requires a custom domain, and the domain is the published hostname. A Cloudflare connection is required only when public access is newly turned on (create, or a PATCH that turns it on), so endpoints that are already public stay editable after the connection is removed. The UI calls it **Public access** ("Turn on public access").

### API

| Route | Action |
|---|---|
| `GET /api/v1/cloudflare` | `endpoint:read`; connection (no secrets) + status + hostnames, scoped to the org |
| `PUT /api/v1/cloudflare` | `cloudflare:manage` + host access; create or update; validates by decoding the token and checking the metrics address is loopback, then reloads and syncs once (20 s limit) and returns the fresh status |
| `DELETE /api/v1/cloudflare` | `cloudflare:manage`; forget the connection. Cloudflare itself is untouched |
| `POST /api/v1/cloudflare/sync` | `cloudflare:manage`; both levels: re-inspects cloudflared at imported (UI "Refresh"), reconciles at managed (UI "Sync now") |
| `POST /api/v1/cloudflare/detect` | `cloudflare:manage` + host access; `{metrics_addr, tunnel_token?}` returns what an import would see, without saving |

The earlier env-driven Cloudflare routes are removed.

## 5. Authorization

New actions:

| Action | Default roles |
|---|---|
| `service:read` | member, admin, owner |
| `service:write` | admin, owner |
| `cloudflare:manage` | admin, owner |
| `host:exec` | owner |

Owner keeps every action. The admin rule changes from "any action" to "any action except `host:exec`". The roles matrix in Settings shows the new actions.

## 6. UI

### Sidebar

Dashboard · Network · Endpoints · Services · Cloudflare · Agents · Members · Audit · Settings

### Endpoint detail

The header is the request path, one card per stage. Each card shows its state, links to its resource, and holds its own actions:

```
[Cloudflare lab] → [portainer.example.com] → [Endpoint] → [Targets] → [Service portainer]
```

- An empty stage is a slot with an action: **Turn on public access** or **Wake a service**. Wake a service lets you pick a service or create one in a dialog, which links it on save.
- Agent endpoints show an **Agent** card instead of Targets.
- The header also has Delete, plus an editable address (subdomain, domain, port).
- Tabs below for policy: Routing · Headers · Security · Advanced (load balancing, origin TLS).

### Endpoints list

Columns: address, a compact path (`☁ → targets → service`), kind, method, health. Each row has Delete. Each ⓘ opens a popover.

### Service detail

- Header: state, driver, idle timeout; buttons Start, Stop, Edit, Delete.
- **Woken by**: linked endpoints with health and Unlink; "Link existing endpoint"; "New endpoint for this service", which is prefilled with method Proxy and a target from `ready_tcp`.
- **Driver** and **Readiness** shown read-only; Edit opens the form.

### Services list

Columns: name, driver, state, idle timeout, woken-by endpoints (links); a "New service" button.

### Service form

- Name, driver.
- Driver fields:
  - docker: container names, or a label;
  - compose: project and services, plus file when host commands are allowed;
  - exec: only when allowed.
- Readiness address.
- Idle timeout presets (Never, 5m, 30m, 2h, custom) and start timeout.

### Cloudflare page

- **Not connected:** an import form with the tunnel token (paste the contents of `~/.cloudflared/<name>.token`) and the metrics address. "Detect" previews the account, tunnel, connections and hostnames, then the user saves.
- **Connected:**
  - level badge (Imported / Managed) and connector state;
  - hostnames table: host, endpoint link ("Public access off" for an unclaimed hostname an endpoint serves) or "unclaimed · Create endpoint", published state;
  - a **Refresh** button at Imported and **Sync now** at Managed;
  - in Imported mode, manual-fix text for unpublished hosts;
  - an "Enable management" section to add the API token, with the exact permission list;
  - an Edit or Disconnect section.

### Network map

Every node links to its resource page:

| Node | Opens |
|---|---|
| tunnel | Cloudflare page |
| hostname of a public endpoint | Cloudflare page |
| unclaimed hostname served by an endpoint with Public access off | the endpoint |
| endpoint | the endpoint |
| target | nothing (no `ref`) |
| service | the service |
| agent | the agent |

### Dashboard

Health strip plus a **Needs attention** list (down endpoints, failed services, unpublished hostnames, unclaimed hostnames), each a link.

### Popovers

A Radix popover component is added in `components/ui/popover.tsx`, and a `HelpTip` wraps it. The text lives in `web/src/lib/glossary.ts`.

| Term | Text |
|---|---|
| Kind · HTTP | Routed by hostname. mishmesh sees each request, so auth, headers, wake pages and Cloudflare publishing work. |
| Kind · TCP | Gets its own public port. Raw bytes: no auth, headers or wake page. |
| Kind · TLS passthrough | Routed by hostname (SNI); encryption ends at your service, so mishmesh cannot apply HTTP features. |
| Method · Agent | Traffic travels through a mishmesh-agent's outbound tunnel. Use it when the service runs on a network the server cannot reach. Offline when the agent disconnects. |
| Method · Proxy | The server connects to the targets itself. Use it when the server can reach the service directly. No agent needed. |
| Method · SSH | Created by `ssh -R`; lives as long as that SSH session. Cannot be created here. |
| Lifecycle · Reserved | The address is permanently yours. It survives disconnects and restarts, and the agent reattaches to it. Everything created in the UI is reserved. |
| Lifecycle · Ephemeral | Created by an agent or SSH session and deleted when that connection drops. Do not publish it or link it to a service. |
| Pinned | The target's certificate key must match the pin, so a self-signed origin is verified, not trusted blindly. |
| Load balancing | Round robin spreads requests across healthy targets. Failover uses the first healthy target in order. |
| Idle timeout | After this long with no requests, mishmesh stops the service. The next request starts it again. Never keeps it running. |
| Imported / Managed | Imported reads your tunnel and shows what to change in Cloudflare. Managed makes the changes for you and needs an API token. |
| States | up · sleeping · waking · degraded · down · offline · unknown, each with one line on what to do |

## 7. Endpoint API changes

- `PATCH /endpoints/{id}` accepts `subdomain`, `domain`, `port` and `policy` (already supported), and the UI exposes them.
- Agent registration drops control-plane fields from agent-supplied policy (`proxy_targets`, `load_balance`, `origin_tls`, `service_id`, `public_access`) and logs a warning; only the control API sets them, where they are validated.
- Endpoint delete is exposed in the UI. Deleting an endpoint that is published triggers a sync.

## 8. Testing

- **Store:** service and Cloudflare-connection conformance tests on SQLite and Postgres.
- **Services:** `Apply` add/change/remove keeps runtime state; a delete during an in-flight start does not panic; `VisibleTo` follows the org.
- **Secrets:** encrypt/decrypt round trip, wrong key fails, key file created with 0600.
- **Cloudflare:**
  - token decode;
  - metrics address must be loopback;
  - imported-level status from a fake cloudflared (`/config`, `/ready`, `/metrics`);
  - unclaimed hostnames;
  - managed level still passes the existing reconciler tests.
- **Control plane:**
  - service CRUD;
  - 409 on delete while linked;
  - host-command gating by flag and role;
  - Cloudflare PUT/GET never returns secrets;
  - org scoping;
- **Web:** `npm run build` (tsc) and lint.
- **Live:** preflight on the lab with a DB copy:
  1. Import the `lab` tunnel and see all 15 live hostnames split into claimed and unclaimed.
  2. Import `services.yml` with `import-services.py`, then link endpoints with `lab-endpoints.py`.
  3. Create, edit and delete a service from the UI.
  4. Link and unlink from both sides.
  5. Wake portainer.
  6. Click through every map node.

## 9. Rollout

Single user, no compatibility layer.

1. The preflight runs on a copy of the live DB.
2. Nir swaps the binary and UI. In `~/.config/mishmesh/start.sh` he drops the old services-file variable and sets `MISHMESH_HOST_COMMANDS` only if exec services are wanted.
3. He then runs `import-services.py`, then `lab-endpoints.py`, and imports the tunnel from the Cloudflare page.
