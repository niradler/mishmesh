# Services: wake on demand, sleep when idle

A service is something mishmesh can start and stop on the server host: existing Docker containers, a Compose project, or a command pair. An endpoint linked to a service wakes it on the first request, and the manager stops it again after it has been idle.

Services are stored in the database, one set per org, and managed through the API (`/api/v1/services`, see [api.md](api.md#services-scale-to-zero)) or the web UI. Every server process loads all services at startup and reloads them on every change.

## Defining a service

```json
{
  "name": "affine",
  "driver": "compose",
  "spec": { "compose": { "project": "affine", "file": "/opt/homelab/stacks/affine/compose.yaml" }, "ready_tcp": "127.0.0.1:3010" },
  "idle_timeout_seconds": 1800,
  "start_timeout_seconds": 180
}
```

| Field | Default | Meaning |
| --- | --- | --- |
| `name` | required | Unique in the org. `^[a-z0-9][a-z0-9-_.]{0,62}$` |
| `driver` | required | `docker`, `compose` or `exec` |
| `spec` | required | Exactly one block matching the driver, plus optional `ready_tcp`. Unknown fields are rejected |
| `spec.ready_tcp` | none | `host:port` that must be listening before the service counts as ready |
| `idle_timeout_seconds` | `0` (never) | Idle time before the service is stopped |
| `start_timeout_seconds` | `0` = 120 | How long a wake may take. Both timeouts allow 0 to 604800 |

## Drivers

| Driver | Spec | Behavior |
| --- | --- | --- |
| `docker` | `{containers?: [name], label?: "key" or "key=value"}`, one of them required | Start starts every stopped match, stop stops every running match. State is `running` when all matches run, `stopped` when none do, `partial` otherwise. Start fails when a named container is missing |
| `compose` | `{project, services?: [name], file?, bin?}` | Containers are found by the label `com.docker.compose.project` (and `com.docker.compose.service` when `services` is set) and started through the Engine API. If none exist and `file` is set, mishmesh runs `<bin> compose -f <file> -p <project> up -d [services]`; `bin` defaults to `docker`. Stop only stops containers, never `down`, so volumes and networks stay |
| `exec` | `{start: [argv], stop: [argv], status?: [argv], dir?, env?: {}}` | Commands run without a shell. `dir` sets the working directory and `env` is merged over mishmesh's environment. The first 2 KB of output appears in errors. `status` exit 0 means running, any other exit means stopped; without it the state is `unknown` and readiness decides |

Docker and Compose talk to the Engine API at `MISHMESH_DOCKER_HOST` (default `unix:///var/run/docker.sock`; `unix://`, `tcp://` and `http://` work), with no SDK. The client negotiates the API version on first use.

`exec.env` values are shown as `<redacted>` to callers without the `host:exec` permission.

## Host commands

`exec` services and Compose services with a `file` run commands on the server host. They are gated twice:

| Gate | Effect |
| --- | --- |
| `MISHMESH_HOST_COMMANDS` (default `true` when `MISHMESH_SIGNUP_MODE=invite`, else `false`) | When off, the API refuses to create, update or delete such services (403), and any stored ones load in the `error` state |
| `host:exec` permission (owners only in the default policy) | Needed to create such a service, to update or delete one that already uses host commands, or to turn an existing service into one |

`GET /api/v1/capabilities` returns `{host_commands}` for the caller, and the UI hides the `exec` driver and the Compose file field when it is false. Plain `docker` services and Compose services without a `file` need neither gate.

## Linking endpoints

An endpoint links to a service with `policy.service_id`:

```bash
curl -X PATCH $API/api/v1/endpoints/$EP -H 'content-type: application/json' \
  -d '{"policy": {"service_id": "svc_..."}}'
```

The service must belong to the endpoint's org. A service with linked endpoints cannot be deleted: `DELETE` returns 409 with the linked endpoint IDs in `endpoints`. `GET /services/{id}` lists them too.

Only HTTP(S) ingress wakes a service, after IP rules, rate limits and authentication pass:

- A browser navigation (GET or HEAD accepting `text/html`) gets a 503 "starting" page that refreshes every 2 seconds while the service starts in the background.
- Other requests wait until the service is ready and are then proxied, or get 503 with `Retry-After: 2` if the start fails.

TCP and TLS endpoints do not wake services. The endpoint `health` reports `sleeping` or `waking` from the service state, and `health.service` carries the service ID.

## Lifecycle

**Wake.** Concurrent wakes of one service share a single start. If the driver reports running and the readiness address answers, nothing is started. Otherwise the driver starts the service and mishmesh polls the readiness address every 500ms until it is ready or the start timeout passes. The readiness address is `spec.ready_tcp`, else the endpoint's first proxy target. A connection that is accepted and closed within 300ms does not count as ready. A failure marks the service `error` with the reason in `last_error`. The wake continues even if the caller disconnects. `POST /services/{id}/start` does the same wake and uses the first linked endpoint's first target.

**Track.** Ingress counts in-flight requests and upgraded connections per service and records the last activity time.

**Idle stop.** Every 30 seconds, a running or partial service with `idle_timeout_seconds > 0`, nothing in flight and no activity for longer than the timeout is stopped (60 second limit) and marked `sleeping`. This includes services that were started outside mishmesh (`started_by: external`); they get one full idle period from when mishmesh first saw them running.

**Refresh.** Every 15 seconds, driver states are re-read. A service stopped outside mishmesh shows `stopped`; one stopped by mishmesh shows `sleeping`. Both wake on request.

States: `running`, `stopped`, `sleeping`, `starting`, `partial`, `unknown`, `error`.

## Docker socket guidance

Access to the Docker socket is root on the host.

- **mishmesh installed on the host:** run it as a user in the `docker` group and keep the default socket.
- **mishmesh in a container:** do not mount the raw socket read-write into an internet-facing container. Put a socket proxy in front and set `MISHMESH_DOCKER_HOST=tcp://docker-proxy:2375`:

```yaml
services:
  docker-proxy:
    image: tecnativa/docker-socket-proxy
    environment:
      CONTAINERS: 1
      POST: 1
      ALLOW_START: 1
      ALLOW_STOP: 1
    volumes:
      - /var/run/docker.sock:/var/run/docker.sock:ro
    networks: [internal]
```

The proxy only exposes container list, start and stop. Compose `up -d` for a stack with no containers yet needs the Docker CLI and the real socket, so with a proxy create the stack once beforehand and let mishmesh start and stop the existing containers.

## Migrating from `MISHMESH_SERVICES_FILE`

Earlier builds read services from a host-local YAML file. That variable is gone and the server ignores it. Import the file once:

```bash
python3 .claude/scripts/import-services.py <services.yml> [api] [--dry-run]
```

- `api` is the server base URL (default `http://127.0.0.1:8091`). When `MISHMESH_API_AUTH_TOKEN` is set, the script sends it as `Authorization: Bearer <token>`. Without it, run against a server with `MISHMESH_API_AUTH_DISABLED=true`. Services land in the org the credential resolves to.
- It only imports services. It creates or updates them by name (`POST /services`, `PUT /services/{id}`) and is safe to re-run. It does not touch endpoints; link them by setting `policy.service_id` on each endpoint.
- `exec.env` values are printed as `<redacted>`. `ready.tcp` becomes `spec.ready_tcp`, and durations such as `90s`, `30m` or `2h` become seconds, with `defaults` applied.
- The file's `orgs` and `docker.host` keys are ignored. Set `MISHMESH_DOCKER_HOST` instead.
- It requires PyYAML.
- `--dry-run` prints the changes without making them.
