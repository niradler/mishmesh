# Services: wake on demand, sleep when idle

Services are stored per organization and use typed Docker, Compose or Command configurations. HTTP ingress wakes a linked service after endpoint authentication, IP rules and rate limits pass. Dependencies start before the dependent, share keep-awake holds and are released when it stops. TCP/UDP/TLS traffic keeps its existing transport behavior and does not implicitly wake HTTP services.

## Define a service

```json
{
  "name": "app",
  "driver": "compose",
  "config": {"project": "app", "file": "/srv/app/compose.yaml"},
  "ready": {"http": {"url": "http://127.0.0.1:8080/health", "status": 200}},
  "requires": ["svc_database"],
  "idle_timeout_seconds": 1800,
  "start_timeout_seconds": 120
}
```

`GET /api/v1/service-drivers` provides labels, help and field descriptors. `config` is a flat object owned by the selected driver; the old `spec` wrapper and `exec` driver name are rejected by the live API. `name` is unique in the org and uses1–63 lowercase letters, digits, hyphen, underscore or dot, starting with a letter or digit.

| Driver | Config | Behavior |
| --- | --- | --- |
| `docker` | `containers` or `label` | Controls matching existing containers through the Docker Engine API. Missing selected containers fail start. |
| `compose` | required `project`; optional `services`, `file`, `bin` | Uses Compose labels to find existing containers. An optional file creates missing containers with explicit Compose argv. Stop preserves networks and volumes. |
| `command` | required `start`/`stop` argv; optional `status`, `dir`, `env` | Runs programs without a shell. Argument whitespace is preserved. Status exit0 means running; without status configure explicit readiness. |

Sensitive configuration is redacted for every reader, including owners. An unchanged `<redacted>` placeholder preserves the old sensitive value. Review process environment and inherited credentials before enabling host execution.

## Readiness and Requires

`ready` is empty, `{"tcp":"host:port"}` or `{"http":{"url":"http(s)://host/path","status":200}}`. TCP connections must remain usable through the readiness hold; HTTP checks require the configured status and do not follow redirects. Checks and service startup are bounded by `start_timeout_seconds` (0 defaults to120seconds, maximum604800).

`requires` contains same-org service IDs. Missing services, cycles and foreign-org edges are rejected atomically. A shared requirement stays up while any dependent uses it. Independent requests join a shared start. Manual stop returns409 with running dependents; the UI offers a separate, explicit action to stop dependent services in order, then the requirement. The backend checks permission for each operation and never silently cascades.

`idle_timeout_seconds` accepts-1 through604800. Zero means no automatic idle stop. Positive values stop an inactive service after that timeout. Minus one keeps a requirement alive only while dependents hold it. `required_by` in service responses lists reverse graph links.

## Save and restart

Normal Save changes metadata/Requires without stopping an unchanged runtime. Driver or runtime-config changes on a running service return409. **Save and restart** sends one `PUT /services/{id}?restart=true`: reserve lifecycle admission, check old/proposed transitive permissions and stop guards, stop, commit the new row, apply it, then start. Ingress cannot wake an old definition during this sequence.

Failures report `phase` and `saved`. A stop/save failure leaves the previous saved config; a start failure after commit keeps the new config saved and visible when reopened. A canceled authoritative apply invalidates the old runtime until a successful reload restores the saved definition. The UI shows the phase and saved state. Restart can interrupt traffic and running dependents must be dealt with explicitly first.

Deleting a service is blocked while an endpoint or service requires it. A permitted unreferenced delete stops it before removing its definition. See [API details](api.md#services-scale-to-zero).

## Host execution

Command services and Compose files need both `MISHMESH_HOST_COMMANDS=true` and `host:exec` (owner by default). The check includes the current and proposed transitive requirement graph for create/update/start/stop/delete. An admin's generic service-write permission does not grant host execution. Broken stored configurations fail closed.

`GET /capabilities` returns caller-specific `{host_commands}`. The UI filters prohibited driver choices and disables host fields. Default host execution is enabled in invite mode and disabled in org mode. Docker and label-only Compose do not require this capability.

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

## Importing typed services YAML

```text
python .claude/scripts/import-services.py <private-services.yml> http://127.0.0.1:8081 --dry-run
python .claude/scripts/import-services.py <private-services.yml> http://127.0.0.1:8081
```

PyYAML is needed only for reading YAML. The helper accepts typed driver/config/ready/requires fields and integer `_seconds` timers only. Nested driver blocks, `spec`, `exec` and old timer fields are rejected. It uses names for idempotency, masks configuration values in output and never silently restarts a running service. Omit `--dry-run` only after reviewing its desired rows. Set `MISHMESH_API_AUTH_TOKEN` privately when bearer authentication is needed.

For fresh initialization and current database/key persistence see [Connections](connections.md#clean-slate-initialization-and-validation).
