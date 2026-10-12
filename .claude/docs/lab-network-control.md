# Lab network control (PR #30, branch feat/lab-network-control)

## State
- Merged scope: services (docker/compose/exec, wake + idle stop), multi-target proxy + LB, origin TLS (pins/CA), health prober, Cloudflare public access, topology. Fixes #17–#20.
- In progress (uncommitted): services in the DB (`/api/v1/services`, PUT to update), per-org Cloudflare connection in the DB (`/api/v1/cloudflare`, imported/managed levels, tokens sealed with `mishmesh-secret.key` next to the SQLite DB), `policy.service_id` + `policy.public_access`, connected UI (sidebar Dashboard · Network · Endpoints · Services · Cloudflare · Agents · Members · Audit · Settings). Spec: `docs/superpowers/specs/2026-10-12-network-control-ux-design.md`.
- Behavior to remember: `POST /cloudflare/sync` needs `cloudflare:manage` and works at both levels (UI "Refresh" / "Sync now"); `PUT /cloudflare` reloads and syncs (20s) before answering; a Cloudflare connection is required only when Public access is newly turned on; host-command services (exec, compose with `file`) need host access to create, update or delete; exec env is `<redacted>` without `host:exec`; hostnames served by an endpoint with Public access off are `unclaimed` with `endpoint_id` and are never pruned; default origin comes from `MISHMESH_INGRESS_ADDR` with an unspecified host mapped to 127.0.0.1.
- Gap issues: #27 TCP/TLS wake, #28 Cloudflare live verification, #29 services real-docker tests + persisted sleeping state, #23 health deep probes.

## Lab (ssh nir@lab-z170mx)
- Nir's live server: pid from `@reboot` cron, `~/.config/mishmesh/start.sh`, binary `~/.local/bin/mishmesh-server`, log `~/logs/mishmesh.log`, API 127.0.0.1:8091, ingress 127.0.0.1:8090 behind cloudflared tunnel `lab`. Never restart it — give Nir the commands.
- New binary staged at `~/.local/bin/mishmesh-server.next`; UI staged at `~/.local/share/mishmesh/web/dist` (keeps Nir's ~/projects/mishmesh checkout untouched).
- Legacy services file `~/.config/mishmesh/services.yml` (portainer idle 30m, others idle 0 = never stop). The new server ignores `MISHMESH_SERVICES_FILE`; drop it from `start.sh` and import the file once with `.claude/scripts/import-services.py <services.yml> [API]` (services only, idempotent by name, exec env redacted in output).
- Preflight (mine): `timeout 7200 ~/.local/bin/mishmesh-server.next` on 127.0.0.1:18090/18091 with DB copy `/tmp/mm-preflight/mm.db`. Stop: `pkill -f "timeout 7200 /home/nir/.local/bin/[m]ishmesh-server.next"`.
- Endpoint migration: `.claude/scripts/lab-endpoints.py [API] [--public-access]` (idempotent; targets, SPKI pins, `service_id` links by service name; run after import-services.py). Live DB still has the old `proxy_target` form until this runs against 8091.
- Both scripts send `Authorization: Bearer $MISHMESH_API_AUTH_TOKEN` when that env var is set.

## Next
1. Nir swaps binary, sets `MISHMESH_WEBUI_DIR=/home/nir/.local/share/mishmesh/web/dist`, drops `MISHMESH_SERVICES_FILE`, restarts; then run `import-services.py` and `lab-endpoints.py` against 8091.
2. Import the `lab` tunnel from the Cloudflare page (imported level), then turn on Public access per endpoint (`lab-endpoints.py --public-access`).
3. Nir creates the Cloudflare API token to move to managed → #28.
4. Notes for Nir: orc runs without compose labels + orphan `077fefaaf02f_orc` container; something polls hermes every minute (will wake it once it has a service).
