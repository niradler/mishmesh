# Services: wake on demand, sleep when idle

A service is something mishmesh can start and stop on the host: a Docker container, a Compose stack, or an arbitrary command pair. Endpoints that reference a service wake it on the first request and the manager stops it after it has been idle.

Services are defined only in a host-local YAML file named by `MISHMESH_SERVICES_FILE`. The API can list, start and stop services by name. It cannot create or change them, so a control-API credential never becomes a shell on the host.

## File format

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
    ready: { tcp: "127.0.0.1:3010" }
  notebook:
    driver: exec
    exec:
      start: ["/usr/local/bin/notebook-up"]
      stop: ["/usr/local/bin/notebook-down"]
      status: ["/usr/local/bin/notebook-status"]
      dir: /srv/notebook
      env: { FOO: bar }
  never-sleep:
    driver: docker
    docker: { label: "mishmesh.service=cockpit" }
    idle_timeout: 0
```

| Key | Default | Meaning |
|---|---|---|
| `orgs` | `[org_default]` | Organizations allowed to see and control these services |
| `docker.host` | `unix:///var/run/docker.sock` | Engine address: `unix:///path`, `tcp://host:port` or `http://host:port` |
| `defaults.idle_timeout` | `0` (never) | Idle time before a service is stopped. `0` disables idle stop |
| `defaults.start_timeout` | `120s` | How long a wake may take |
| `services.<name>` | required | Name must match `^[a-z0-9][a-z0-9-_.]{0,62}$` |
| `idle_timeout`, `start_timeout` | from `defaults` | Per-service overrides. Durations look like `30m`, `120s`, `2h` |
| `ready.tcp` | none | `host:port` that must accept a TCP connection before the service counts as running |

Unknown keys are rejected so typos fail at startup.

## Drivers

**docker** needs a `docker` block with `containers` (names, matched exactly) or `label` (`key=value`). Start starts every stopped match, stop stops every running match. State is `running` when all matches run, `stopped` when none do, `partial` otherwise. Start fails if a named container does not exist.

**compose** needs `compose.project`. Containers are found by the label `com.docker.compose.project=<project>`, optionally narrowed with `services: [web, db]` (label `com.docker.compose.service`). Start starts the existing containers through the Engine API. If none exist and `file` is set, mishmesh runs `docker compose -f <file> -p <project> up -d` (plus the listed services). Set `compose.bin` to use another binary. Stop stops the labelled containers; it never runs `down`, so volumes and networks stay.

**exec** needs non-empty `start` and `stop` argv lists. They run without a shell. `dir` sets the working directory and `env` is merged on top of mishmesh's own environment. Output is captured and the first 2 KB is included in errors. Start is bound by the start timeout, and an idle stop by a 60s limit. The optional `status` argv reports exit code 0 as running and any other exit as stopped. Without `status` the state is `unknown` and the readiness probe decides.

The Docker client speaks the Engine API over `net/http` with no SDK. On first use it calls `GET /version` and prefixes every later call with the reported `ApiVersion`. If that call fails the operation fails with a clear error and is retried on the next attempt.

## Docker socket guidance

Access to the Docker socket is root on the host.

- **mishmesh installed on the host:** run it as a user in the `docker` group and keep the default `unix:///var/run/docker.sock`.
- **mishmesh in a container:** do not mount the raw socket read-write into an internet-facing container. Put a socket proxy in front and point `docker.host` at it:

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

```yaml
docker:
  host: tcp://docker-proxy:2375
```

The proxy only exposes container list, start and stop. Compose `up -d` for a stack that has no containers yet needs the real Docker CLI and socket, so with a proxy create the stack once beforehand and let mishmesh start and stop the existing containers.

## Lifecycle

**Wake.** `EnsureRunning` is collapsed per service: concurrent requests share one start. If the driver reports running and the readiness address answers, nothing is started. Otherwise the driver starts the service and mishmesh polls the readiness address every 500ms until it answers or `start_timeout` passes. The readiness address is `ready.tcp`, else the endpoint's first target (scheme stripped). With neither, the driver state must be `running`. A failure marks the service `error` with the reason in `last_error`. The wake continues even if the caller disconnects.

**Track.** Ingress acquires the service when a request or upgraded connection starts and releases it when it ends. The manager keeps the active count and last activity time.

**Idle stop.** Every 30s a service with `idle_timeout > 0`, zero active connections, and no activity for longer than the timeout is stopped and marked `sleeping`. A service mishmesh has seen no traffic for gets one full idle period of grace after mishmesh starts, and a service found running that mishmesh did not start (`started_by: external`) gets a full period from when it was first seen running. A sleeping service is only restarted by a request or an explicit start.

**State refresh.** Every 15s driver states are re-read. States reported are `running`, `stopped`, `sleeping` (stopped by mishmesh), `starting`, `partial`, `unknown` and `error`.

## Homelab example

Compose stacks under `/opt/homelab/stacks`, each in its own directory:

```yaml
orgs: [org_default]
defaults:
  idle_timeout: 30m
  start_timeout: 180s
services:
  affine:
    driver: compose
    compose: { project: affine, file: /opt/homelab/stacks/affine/compose.yaml }
    ready: { tcp: "127.0.0.1:3010" }
  paperless:
    driver: compose
    compose:
      project: paperless
      file: /opt/homelab/stacks/paperless/compose.yaml
      services: [webserver, db, broker]
    idle_timeout: 1h
  cockpit:
    driver: docker
    docker: { label: "mishmesh.service=cockpit" }
    idle_timeout: 0
```

Endpoints then reference `affine`, `paperless` or `cockpit` as their service. The first visit to a sleeping stack starts it, and the stack stops after 30 minutes without traffic.
