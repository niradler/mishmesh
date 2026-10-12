# Lab network control (PR #30, merged as 7cccdf0, released v0.1.0-beta.4)

## State

- Shipped: services in the DB (docker/compose/exec, wake + idle stop, `/api/v1/services` CRUD), multi-target proxy + LB, origin TLS (pins/CA), health prober, per-org Cloudflare connection in the DB (`/api/v1/cloudflare`, imported/managed levels, tokens sealed with `mishmesh-secret.key` next to the SQLite DB), `policy.service_id` + `policy.public_access`, connected UI and topology map. Spec: `docs/superpowers/specs/2026-10-12-network-control-ux-design.md`.
- Behavior to remember: `POST /cloudflare/sync` needs `cloudflare:manage` and works at both levels; `PUT /cloudflare` reloads and syncs (20s) before answering; a Cloudflare connection is required only when Public access is newly turned on; host-command services need host access to create, update or delete; hostnames served by an endpoint with Public access off are `unclaimed` with `endpoint_id` and are never pruned; a hostname is `published` only when its tunnel route points at the mishmesh origin.
- Gap issues: #27 TCP/TLS wake, #28 Cloudflare live verification (managed level), #29 services real-docker tests + persisted sleeping state, #23 health deep probes.

## Lab (ssh nir@lab-z170mx) — live on v0.1.0-beta.4 since 2026-10-12

- Server: `~/.config/mishmesh/start.sh` (cron `@reboot`), binary `~/.local/bin/mishmesh-server` (v0.1.0-beta.4), UI `~/.local/share/mishmesh/web-v0.1.0-beta.4/dist`, log `~/logs/mishmesh.log`, API 127.0.0.1:8091, ingress 127.0.0.1:8090 behind cloudflared tunnel `lab` (systemd user `cloudflared.service`, not managed by mishmesh).
- Rollback: `~/.local/bin/mishmesh-server.prev`, `~/.config/mishmesh/env.bak-20261012`, `~/.local/share/mishmesh/mishmesh.db.bak-20261012`.
- `MISHMESH_SERVICES_FILE` is gone from env; the 8 services were imported once from `~/.config/mishmesh/services.yml`.
- Cloudflare: tunnel imported at the `imported` level; 12 hostnames published, 3 unclaimed orphans (2fauth, docker, vaultwarden) routed outside mishmesh.
- Scripts in `~/.local/share/mishmesh/scripts` (copies of `.claude/scripts`): `import-services.py`, `lab-endpoints.py [--public-access]`, `cf-import.py`, `lab-e2e.py [--wake <service>] [--no-public]`. All are idempotent and send `Authorization: Bearer $MISHMESH_API_AUTH_TOKEN` when set.
- E2E (`lab-e2e.py --wake portainer`) passed fully after go-live: UI, services, endpoints health, Cloudflare sync + published hostnames, topology links, ingress per host, public hosts reach Cloudflare Access, service CRUD + 409 guards, portainer stop then wake on request in 1.0s.

## Next

1. Nir creates a Cloudflare API token to move to the managed level → #28.
2. Decide on the orphans (2fauth, docker, vaultwarden hostnames) — add endpoints or remove the routes.
3. Notes: orc runs without compose labels + orphan `077fefaaf02f_orc` container; something polls hermes every minute (keeps it awake); idle stop also stops containers started outside mishmesh; wake is HTTP only (#27); `access_app_id` is not verified against Cloudflare.
