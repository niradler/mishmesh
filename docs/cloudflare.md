# Cloudflare public access

mishmesh can publish endpoints through a Cloudflare Tunnel. Cloudflare terminates TLS (and applies Access, when configured), `cloudflared` carries the traffic, and the tunnel routes each published hostname to the mishmesh ingress, which serves the endpoint as usual.

Each organization can have multiple named Cloudflare connections. They are stored in the database and managed through `/api/v1/connections` or the Connections UI. Provider credentials belong to the selected connection.

## Publishing an endpoint

Add `policy.access: [{"connection_id":"<connection-id>","hostname":"app.example.test"}]` to an HTTP endpoint. The requested hostname must be a full DNS name; an empty hostname defaults to the endpoint domain. Enabled domain authorization still applies. New refs require a valid same-org connection; an existing ref stays editable while its provider is unavailable. Referenced connections cannot be deleted. Endpoint changes trigger provider refresh and alias invalidation.

In the UI, add a row under the endpoint's **Access** section. Public and private rows can coexist.

## Levels

| Level | You provide | mishmesh does |
| --- | --- | --- |
| `imported` | Tunnel token and the cloudflared metrics address | Reads the tunnel's routes from the local cloudflared and reports each hostname. Changes nothing in Cloudflare. Every missing or wrong route comes with a `fix`: the exact Zero Trust dashboard change to make |
| `managed` | Also an API token | Reconciles through the Cloudflare API: adds ingress rules, creates DNS records, adds hostnames to the Access app, and with `prune` removes what it owns and no endpoint wants |

Start at `imported` to see what the tunnel already serves, then add an API token to move to `managed`. Explicitly clear the API token to move back; the wire payload uses `secrets.api_token: ""`.

Providers refresh periodically, on endpoint changes, after saves and on `POST /connections/{id}/sync`. Imported refresh inspects local cloudflared routes; managed refresh reconciles owned resources. The UI provides **Sync now** for the selected connection.

## Connection fields

| Field | Default | Meaning |
| --- | --- | --- |
| `name` | required | Display name, at most 100 characters |
| `secrets.tunnel_token` | required | The token from the Zero Trust dashboard; account and tunnel IDs are read from it |
| `secrets.api_token` | none | Enables `managed` reconciliation |
| `connector` | `external` | `external`: you run cloudflared. `managed`: mishmesh runs it |
| `metrics_addr` | `127.0.0.1:20241` | cloudflared's metrics address. Must be loopback, so cloudflared runs on the server host (or in its network namespace) |
| `origin` | `http://<MISHMESH_INGRESS_ADDR>`, with an unspecified host replaced by `127.0.0.1` | The ingress URL the tunnel routes hostnames to. No path, query or credentials |
| `access_app_id` | none | Self-hosted Access application that receives published hostnames (`managed` only) |
| `prune` | `false` | Remove owned hostnames no endpoint wants. Needs an API token |

Connection config and secrets are separate in the generic API (`POST /connections`, `PUT /connections/{id}`). Both require `connection:manage`; local managed connector execution additionally needs host access. Detection is `POST /connections/detect` with `{type:"cloudflare",input:{metrics_addr?,tunnel_token?}}`. A save commits first, then reloads/syncs within20seconds and returns safe status even when a provider is down. Referenced connections return409 on deletion; deleting an unreferenced row closes its local instance without silently pruning external resources.

## Routing to ingress

The tunnel sends a hostname's traffic to `origin`. mishmesh treats a route as its own when its service equals the origin (scheme and host compared case-insensitively, trailing slash ignored). The ingress then picks the endpoint by `Host` as for any custom domain.

The default origin is derived from `MISHMESH_INGRESS_ADDR`. An unspecified host (`:8080`, `0.0.0.0:8080`, `[::]:8080`) becomes `127.0.0.1`, so the default is right whenever cloudflared runs on the same host or in the server's network namespace. If cloudflared reaches the ingress through another address, set `origin` explicitly.

## Token sealing

Tokens are encrypted together with AES-256-GCM, bound to organization and connection ID. API reads return `has_api_token` and `has_tunnel_token`, never their values. User config and private ownership state are stored separately.

The key lives in `MISHMESH_SECRET_KEY_FILE`, by default `mishmesh-secret.key` beside SQLite or in the working directory for PostgreSQL/in-memory stores. First creation publishes a complete key atomically with restrictive file permissions. Back up the matching key with the database. Missing/wrong keys fail before migration commits; restore the matching key rather than creating a replacement over ciphertext.

## API token permissions (managed level)

| Permission | Needed for |
| --- | --- |
| Account > Cloudflare Tunnel > Edit | Reading tunnel status and writing ingress rules |
| Zone > DNS > Edit | Creating and pruning CNAME records, for every zone you publish into |
| Zone > Zone > Read | Finding the zone for a hostname |
| Account > Access: Apps and Policies > Edit | Only with `access_app_id` |

The tunnel must be remotely managed (created in the dashboard or the API), since mishmesh edits its configuration through the API.

## Ownership (managed level)

mishmesh changes only what it can prove is its own.

- **Ingress rules.** A missing hostname gets a rule to the origin, inserted before the catch-all (`http_status:404` is added if there is none). A hostname already routed elsewhere is a `conflict` and is left alone. Unknown settings and fields are written back unchanged, and nothing is written when nothing changed.
- **DNS.** The longest matching zone is selected. Created records have durable per-resource ownership. A matching pre-existing record is not silently adopted; foreign or operator-modified records remain intact.
- **Access.** Added app domains/destinations are tracked separately. Pre-existing entries are retained.
- **Prune.** Removes only resources supported by durable ownership and matching observed fingerprints. Unknown settings, unclaimed hostnames and operator edits are preserved. Dropping an Access ref clears its local alias; it does not grant blanket ownership of the external hostname.

Errors are per hostname: one failure does not stop the others.

## Hostname status

| `state` | Meaning |
| --- | --- |
| `published` | Routed to the origin, and at `managed` also DNS and Access in place |
| `pending` | Something is not in place yet. At `imported`, see `fix` |
| `conflict` | Routed to another service, or a DNS record mishmesh does not own exists |
| `unclaimed` | On the tunnel but no endpoint publishes it. `detail` says whether it goes through mishmesh. When it routes to the origin and an endpoint with Public access off has this domain, `endpoint_id` names that endpoint, `detail` is "an endpoint serves this domain but its Public access is off" and `fix` says to turn Public access on. The topology links that hostname node to the endpoint |
| `error` | A call failed; see `detail` and `dns_detail` |

`dns` and `access` are `unchecked` at the `imported` level. `fix` is set at `imported` for every pending or conflicting route and at `managed` for conflicts mishmesh will not resolve.

## Connector

**External (default).** Run cloudflared yourself (systemd, Docker, Kubernetes) with the tunnel token and `--metrics <metrics_addr>`. mishmesh reads `/ready`, `/metrics` and `/config` from that address for status and, at `imported`, for the route list.

**Managed.** mishmesh runs:

```text
cloudflared tunnel --no-autoupdate --metrics <metrics_addr> run
```

The binary is `cloudflared` on the PATH, or `MISHMESH_CLOUDFLARED_BIN`. The tunnel token is passed as `TUNNEL_TOKEN` in the environment, never on the command line or in logs. Restarts back off from 1s to 60s, and the backoff resets after 60s of healthy running. Status reports `running`, `pid`, `restarts`, `last_exit` and `ready`.

## Moving from a hand-managed tunnel

1. Import the tunnel with its token and `connector: external`. Set `origin` to exactly the service your existing routes use for mishmesh.
2. Add an Access row referencing the imported connection ID and desired hostname on each endpoint. Existing routes to the origin for those hostnames show `published`.
3. Check `unclaimed` hostnames: turn on Public access for the endpoint named in `endpoint_id`, add an endpoint, or accept that prune will remove the ones no endpoint serves.
4. Add an API token to manage the tunnel. Leave `prune` off until no unexpected `unclaimed` routes to the origin remain.

The new connection API, encrypted key migration, multi-network refs and private Tailscale provider are documented in [connections.md](connections.md). Historical REST singleton routes and `public_access` writes are rejected.
