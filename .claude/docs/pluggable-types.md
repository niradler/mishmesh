# Pluggable types (branch feat/pluggable-types)

## State

- Spec approved by Nir 2026-10-13: `docs/superpowers/specs/2026-10-13-pluggable-types-design.md` (commit 1a913c3).
- No implementation plan yet, no code yet.
- Lab runs v0.1.0-beta.4 (see `lab-network-control.md`); `lab-e2e.py` is the regression check.

## Scope

- Connections by type (Cloudflare Tunnel, Tailscale tailnet-only, Kubernetes cluster); `connections` table replaces `cloudflare_connections`; `policy.access []AccessRef` replaces `public_access`; `/api/v1/connections*` replaces `/api/v1/cloudflare*`; `connection:manage` replaces `cloudflare:manage`.
- Service drivers from descriptors (Docker, Compose, Command (renamed from exec), Kubernetes); `config` / `ready` columns + `service_requires` table replace `spec`.
- Requires between services (wake order, keep-awake holds, release, 409 guards, idle option "With dependents", map edge).

## Lab facts

- GUI for Guacamole = system units `gdm` + `gnome-remote-desktop` (inactive; default target multi-user). Starting needs root → sudoers drop-in limited to those commands + `MISHMESH_HOST_COMMANDS=true`. Confirm with Nir before applying.
- No tailscale on the lab: Nir installs, joins with a tagged auth key, approves the host.
- No Kubernetes on the lab: validate with a throwaway `kind` cluster, removed afterwards.

## Next

1. Write the implementation plan (superpowers:writing-plans) from the spec; Nir picks the execution method.
2. Implement, `make check`, PR, merge, release v0.1.0-beta.5, roll out to the lab, extend and run `lab-e2e.py`.
