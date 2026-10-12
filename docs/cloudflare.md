# Cloudflare front door

mishmesh can publish endpoints through a Cloudflare Tunnel. For every endpoint with `front_door: "cloudflare"` and a custom `domain`, it keeps three things in sync: a tunnel ingress rule, a proxied DNS record, and (optionally) an Access application hostname. Cloudflare terminates TLS and applies Access; traffic reaches mishmesh ingress through `cloudflared`.

The tunnel must be remotely managed (created in the Zero Trust dashboard or with the API), because mishmesh edits its configuration through the API.

## API token permissions

Create an API token with:

| Permission | Needed for |
|---|---|
| Account > Cloudflare Tunnel > Edit | reading tunnel status and writing ingress rules |
| Zone > DNS > Edit | creating and pruning CNAME records (include every zone you publish into) |
| Zone > Zone > Read | finding the zone for a hostname |
| Account > Access: Apps and Policies > Edit | only when `MISHMESH_CLOUDFLARE_ACCESS_APP_ID` is set |

Scope the token to the account and to the zones you use.

## Environment variables

| Variable | Default | Meaning |
|---|---|---|
| `MISHMESH_CLOUDFLARE_API_TOKEN` | | Enables the feature when set |
| `MISHMESH_CLOUDFLARE_ACCOUNT_ID` | | Required |
| `MISHMESH_CLOUDFLARE_TUNNEL_ID` | | Required |
| `MISHMESH_CLOUDFLARE_ORIGIN` | `http://<MISHMESH_INGRESS_ADDR>` | The service URL written into ingress rules. It must be reachable from where `cloudflared` runs |
| `MISHMESH_CLOUDFLARE_ACCESS_APP_ID` | | Self-hosted Access app that receives published hostnames |
| `MISHMESH_CLOUDFLARE_PRUNE` | `false` | Remove hostnames mishmesh owns that no endpoint wants |
| `MISHMESH_CLOUDFLARE_SYNC_INTERVAL` | `5m` | Periodic reconcile interval |
| `MISHMESH_CLOUDFLARE_ORGS` | `org_default` | Comma-separated orgs whose endpoints may use the front door |
| `MISHMESH_CLOUDFLARE_BASE_URL` | `https://api.cloudflare.com/client/v4` | Override for testing |
| `MISHMESH_CLOUDFLARED_RUN` | `false` | Supervise a local `cloudflared` |
| `MISHMESH_CLOUDFLARED_BIN` | `cloudflared` | Binary to run |
| `MISHMESH_CLOUDFLARED_TOKEN_FILE` | | File holding the tunnel token; required when `RUN` is true |
| `MISHMESH_CLOUDFLARED_METRICS_ADDR` | `127.0.0.1:20241` | Metrics and readiness address of the supervised process |

Setting the API token without the account or tunnel ID is a startup error.

## When it reconciles

At startup, every sync interval, and shortly after endpoint changes (triggers are debounced by about two seconds). `POST /api/v1/frontdoors/cloudflare/sync` reconciles immediately and `GET /api/v1/frontdoors` returns the last result.

## Ownership

mishmesh only manages what it can prove is its own.

- **Ingress rules.** A rule is owned when its `service` equals the configured origin. Scheme and host are compared case-insensitively and a trailing slash is ignored. Rules with any other service are never modified, never reordered relative to each other, and never removed. A catch-all rule (no hostname, no path) is never owned, even if it points at the origin.
- **New rules.** A missing desired hostname gets a rule inserted before the catch-all. If the catch-all is missing, `http_status:404` is appended. If a hostname already has a rule pointing at another service, it is reported as a `conflict` and left alone.
- **Unknown settings.** The tunnel configuration and every rule are round-tripped as raw JSON, so `warp-routing`, `originRequest` and any field mishmesh does not understand are written back unchanged. The PUT is skipped when nothing changed.
- **Orphans.** An owned rule with no endpoint wanting it is reported as `orphaned`. It is removed only when prune is on.
- **DNS.** The zone is the one with the longest name matching the hostname. A missing record is created as a proxied CNAME to `<tunnel-id>.cfargotunnel.com` with the comment `managed by mishmesh`. A record that is a CNAME to this tunnel is `ok`. Any other existing record is a `conflict`: it is reported and never modified or deleted, even with prune on. Prune deletes only CNAMEs that point at this tunnel.
- **Access.** Missing hostnames are added to the app's `self_hosted_domains`, and to `destinations` (as `{"type":"public","uri":host}`) when the app has that field. The app object is sent back whole with unknown fields preserved. Hostnames are removed only with prune.
- **Errors are per hostname.** A failure on one hostname is recorded on that hostname and does not stop the others.

## Hostname state

| State | Meaning |
|---|---|
| `synced` | ingress rule, DNS and (if configured) Access are all in place |
| `pending` | something is not yet in place, normally resolved by the next sync |
| `conflict` | another service owns the ingress hostname, or a foreign DNS record exists |
| `orphaned` | mishmesh owns it but no endpoint wants it |
| `error` | a Cloudflare call failed; see `detail` and `dns_detail` |

The status also carries the tunnel `status` (`healthy`, `degraded`, `down`, `inactive`), its connections (colo, origin IP, client version, opened at), the last sync time and the last error.

## Migrating from a hand-managed tunnel

1. Point `MISHMESH_CLOUDFLARE_TUNNEL_ID` at the existing tunnel and set `MISHMESH_CLOUDFLARE_ORIGIN` to exactly the service your existing rules use for mishmesh ingress.
2. Keep `MISHMESH_CLOUDFLARE_PRUNE=false`.
3. Set `front_door: "cloudflare"` and the custom `domain` on each endpoint that should be published. Existing rules pointing at the origin for those hostnames become owned and report `synced`. Existing DNS CNAMEs to the tunnel are also adopted.
4. Read `GET /api/v1/frontdoors`. Hostnames in `orphaned` are rules pointing at the mishmesh origin that no endpoint covers. Either add the endpoint or accept that they will be removed. Hostnames in `conflict` need a manual decision: delete the foreign record or rule yourself.
5. Only when no unexpected `orphaned` entries remain, set `MISHMESH_CLOUDFLARE_PRUNE=true`.

Rules that point at anything other than the mishmesh origin are never touched by any of this.

## Supervised or external cloudflared

**External (default).** Run `cloudflared` yourself (systemd, Docker, Kubernetes) with the tunnel token. mishmesh only talks to the Cloudflare API. The origin must be reachable from that host.

**Supervised.** Set `MISHMESH_CLOUDFLARED_RUN=true` and `MISHMESH_CLOUDFLARED_TOKEN_FILE`. mishmesh runs:

```
cloudflared tunnel --no-autoupdate --metrics 127.0.0.1:20241 run
```

The tunnel token is read from the file and passed as `TUNNEL_TOKEN` in the child's environment. It is never put on the command line and never logged. The process output is discarded. Restarts use exponential backoff from 1s up to 60s, and the backoff resets after the process has run for 60s. The process is stopped when mishmesh shuts down. The status reports `running`, `pid`, `restarts`, `last_exit` and `ready`, where `ready` is the result of `GET /ready` on the metrics address.
