# Pluggable types (branch feat/pluggable-types)

## State

- Spec approved by Nir 2026-10-13: `docs/superpowers/specs/2026-10-13-pluggable-types-design.md` (commit 1a913c3).
- Implementation plan written and self-reviewed: `docs/superpowers/plans/2026-10-13-pluggable-types.md` (20 testable tasks, including release/lab gates). Execution authorized: subagent-driven (Nir delegated the choice). Isolated worktree on feat/pluggable-types; baseline full Go race suite passed; Task 1 descriptors committed (`af1a3ad`), race tests/vet/all-package compilation passed; independent task review approved without findings. Task 2 store contracts reviewed and committed (`9ff522a`), including six live isolated Postgres race cases. Task 4 driver registry is next; Task 3 schema cutover follows Go consumer Tasks 4–14.
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

1. Execute the plan with per-task implementation/review gates; resume from `.superpowers/sdd/2026-10-13-pluggable-types/progress.md` in the isolated worktree.
2. Implement, `make check`, PR, merge, release v0.1.0-beta.5, roll out to the lab, extend and run `lab-e2e.py`.

## Planning notes (2026-10-13)

- Store opens before the current network controller loads the secret key. Plan moves key loading ahead of migration and tests atomic re-sealing, corrupt/missing keys, malformed rows, reopen/idempotence and Postgres concurrent opens.
- Runtime Requires holds need protection across concurrent/shared wakes, external state refresh, failed stops and config reloads; tests precede lifecycle changes.
- Plan makes idle `-1` = With dependents, argv-list Command fields, generic descriptor defaults/sensitivity, persistent Tailscale ownership, and derived ingress aliases explicit implementation choices.
- Existing uncommitted network-map layout changes and browser artifacts belong to the previous task and must be preserved/separately scoped.
- No lab, sudoers, host-command flag, Tailscale installation, Kubernetes cluster, release or external publication changes made during planning.

## Completed execution tasks

- Task 1: shared typed fields, strict validation, host markers, and redaction; commit `af1a3ad`; focused race tests (97.2% coverage), vet, all-package compilation, and independent spec/quality review passed.

- Task 2: typed store contracts, derived runtime references, aliases and transactional guards; `9ff522a`; SQLite/store/legacy-consumer race suites, six live Postgres cases, Windows/Linux compile and vet passed. Independent review approved with one deferred canonical-hostname fixture correction assigned to Task 14.

Execution order is recorded in the plan and ledger; physical migration runs after Go consumers are converted.

Task 4 in progress: built-in registry, generic Config definitions, pure legacy converter and compatible pinned Kubernetes dependencies. Security regressions verified red/green for raw command output in LastError/logs and host markers discarded by legacy API normalization; implementation committed (`ce31f02`), compatibility race/consumer compile/vet/govulncheck passed, independent review approved; Task 5 readiness is next.

- Task 4: driver registry/generic config and pure legacy service converter (`ce31f02`), reviewed without findings. Raw command output leak and pre-normalize host-marker bypass fixed with red/green regressions; compatibility race/compile/vet and services vulnerability scan passed.
