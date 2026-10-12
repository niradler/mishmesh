# Cloudflare public access

mishmesh can publish endpoints through a Cloudflare Tunnel. Cloudflare terminates TLS (and applies Access, when configured), `cloudflared` carries the traffic, and the tunnel routes each published hostname to the mishmesh ingress, which serves the endpoint as usual.

Each org has at most one tunnel connection. It is stored in the database and managed through the API (`/api/v1/cloudflare`, see [api.md](api.md#cloudflare-public-access)) or the web UI. No environment variable configures it.

## Publishing an endpoint

Set `policy.public_access: "cloudflare"` and a custom `domain` on the endpoint. The domain is the published hostname. A public endpoint must always have a domain, else 400. The org must have a connection only when public access is newly turned on (on create, or a PATCH that turns it on), else 400. An endpoint that is already public stays editable after the connection is removed. Endpoint changes trigger a refresh within about two seconds.

In the web UI this is the endpoint's **Public access** toggle ("Turn on public access").

## Levels

| Level | You provide | mishmesh does |
| --- | --- | --- |
| `imported` | Tunnel token and the cloudflared metrics address | Reads the tunnel's routes from the local cloudflared and reports each hostname. Changes nothing in Cloudflare. Every missing or wrong route comes with a `fix`: the exact Zero Trust dashboard change to make |
| `managed` | Also an API token | Reconciles through the Cloudflare API: adds ingress rules, creates DNS records, adds hostnames to the Access app, and with `prune` removes what it owns and no endpoint wants |

Start at `imported` to see what the tunnel already serves, then add an API token to move to `managed`. `clear_api_token: true` moves back.

Refresh runs every minute at `imported` and every five minutes at `managed`, plus on endpoint changes, after every save, and on `POST /cloudflare/sync`. A refresh at `imported` re-inspects the local cloudflared and its route list; at `managed` it reconciles. The UI labels the button **Refresh** at `imported` and **Sync now** at `managed`.

## Connection fields

| Field | Default | Meaning |
| --- | --- | --- |
| `name` | required | Display name, at most 100 characters |
| `tunnel_token` | required once | The token from the Zero Trust dashboard. The account and tunnel IDs are read from it. One tunnel can be connected to only one org |
| `api_token` | none | Turns on `managed` |
| `connector` | `external` | `external`: you run cloudflared. `managed`: mishmesh runs it |
| `metrics_addr` | `127.0.0.1:20241` | cloudflared's metrics address. Must be loopback, so cloudflared runs on the server host (or in its network namespace) |
| `origin` | `http://<MISHMESH_INGRESS_ADDR>`, with an unspecified host replaced by `127.0.0.1` | The ingress URL the tunnel routes hostnames to. No path, query or credentials |
| `access_app_id` | none | Self-hosted Access application that receives published hostnames (`managed` only) |
| `prune` | `false` | Remove owned hostnames no endpoint wants. Needs an API token |

Saving a connection (`PUT`) and reading a local cloudflared (`POST /cloudflare/detect`) need the `cloudflare:manage` permission and host access, because they make the server talk to a local process. `PUT` reloads the connection, then syncs once (up to 20 seconds) and returns the fresh status. Syncing (`POST /cloudflare/sync`) and removing the connection (`DELETE`) need only `cloudflare:manage`. `DELETE` stops a managed cloudflared and changes nothing in Cloudflare.

## Routing to ingress

The tunnel sends a hostname's traffic to `origin`. mishmesh treats a route as its own when its service equals the origin (scheme and host compared case-insensitively, trailing slash ignored). The ingress then picks the endpoint by `Host` as for any custom domain.

The default origin is derived from `MISHMESH_INGRESS_ADDR`. An unspecified host (`:8080`, `0.0.0.0:8080`, `[::]:8080`) becomes `127.0.0.1`, so the default is right whenever cloudflared runs on the same host or in the server's network namespace. If cloudflared reaches the ingress through another address, set `origin` explicitly.

## Token sealing

Tokens are encrypted with AES-256-GCM before they are stored, each bound to its org and kind. They are never returned by the API; `api_token_set` and `tunnel_token_set` show whether one is stored.

The key lives in `MISHMESH_SECRET_KEY_FILE`, by default `mishmesh-secret.key` next to the SQLite database (or in the working directory for Postgres and in-memory stores). It is created on first start with mode 0600. Back it up with the database: without it the stored tokens cannot be opened and the connection must be saved again with fresh tokens.

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
- **DNS.** The zone is the longest matching zone in the account. A missing record is created as a proxied CNAME to `<tunnel-id>.cfargotunnel.com` with the comment `managed by mishmesh`. Any other existing record is a `conflict` and is never changed.
- **Access.** Hostnames are added to the app's domains and destinations.
- **Prune.** Removes owned ingress rules no endpoint wants, deletes only CNAMEs that point at this tunnel, and removes the hostname from the Access app. It never removes the rule for a hostname that is the domain of an endpoint with Public access off.

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
2. Set `public_access: "cloudflare"` and the domain on each endpoint to publish. Existing routes to the origin for those hostnames show `published`.
3. Check `unclaimed` hostnames: turn on Public access for the endpoint named in `endpoint_id`, add an endpoint, or accept that prune will remove the ones no endpoint serves.
4. Add an API token to manage the tunnel. Leave `prune` off until no unexpected `unclaimed` routes to the origin remain.
