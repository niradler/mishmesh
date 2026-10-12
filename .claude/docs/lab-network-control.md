# Lab network control (PR #30, branch feat/lab-network-control)

## State
- Merged scope: services (docker/compose/exec, wake + idle stop), multi-target proxy + LB, origin TLS (pins/CA), health prober, Cloudflare front door, topology, consolidated UI (Endpoints|Services tabs, Network Map|Front doors tabs, collapsible endpoint form). Fixes #17–#20.
- Gap issues: #27 TCP/TLS wake, #28 Cloudflare live verification, #29 services real-docker tests + persisted sleeping state, #23 health deep probes.

## Lab (ssh nir@lab-z170mx)
- Nir's live server: pid from `@reboot` cron, `~/.config/mishmesh/start.sh`, binary `~/.local/bin/mishmesh-server`, log `~/logs/mishmesh.log`, API 127.0.0.1:8091, ingress 127.0.0.1:8090 behind cloudflared tunnel `lab`. Never restart it — give Nir the commands.
- New binary staged at `~/.local/bin/mishmesh-server.next`; UI staged at `~/.local/share/mishmesh/web/dist` (keeps Nir's ~/projects/mishmesh checkout untouched).
- Services file `~/.config/mishmesh/services.yml` (portainer idle 30m, others idle 0 = never stop). `MISHMESH_SERVICES_FILE` already in env.
- Preflight (mine): `timeout 7200 ~/.local/bin/mishmesh-server.next` on 127.0.0.1:18090/18091 with DB copy `/tmp/mm-preflight/mm.db`. Stop: `pkill -f "timeout 7200 /home/nir/.local/bin/[m]ishmesh-server.next"`.
- Endpoint migration: `.claude/scripts/lab-endpoints.py [API]` (idempotent; targets, SPKI pins, service mapping). Live DB still has the old `proxy_target` form until this runs against 8091.

## Next
1. Nir swaps binary, sets `MISHMESH_WEBUI_DIR=/home/nir/.local/share/mishmesh/web/dist`, restarts; then run `python3 lab-endpoints.py` against 8091.
2. Nir creates the Cloudflare token → #28.
3. Notes for Nir: orc runs without compose labels + orphan `077fefaaf02f_orc` container; something polls hermes every minute (will wake it once it has a service).
