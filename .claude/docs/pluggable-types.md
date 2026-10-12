# Pluggable types (branch feat/pluggable-types)

## State

- Spec approved by Nir 2026-10-13: `docs/superpowers/specs/2026-10-13-pluggable-types-design.md` (commit `1a913c3`).
- Implementation plan: `docs/superpowers/plans/2026-10-13-pluggable-types.md`; subagent-driven execution authorized (Nir delegated method choice).
- Tasks 1, 2, 4, 5, 6, 7 and 8 complete with independent review. Task 5 TCP diagnostic leak fixed (`a7c6a41`), regression-tested and scoped review approved. Task 6 isolation fix (`c7a8c6c`) reproduced live red/green and scoped review approved. Task 7 lifecycle fix (`62ab79c`) regression-tested and scoped review approved. Task 8 deletion fix (`d78d997`) regression-tested and scoped review approved. Task 9 is next.
- Active worktree: `C:/Projects/products/mishmesh/.claude/worktrees/pluggable-types` on `feat/pluggable-types`. Original checkout on `work/network-layout` preserves its uncommitted prior map fix.
- Lab still runs `v0.1.0-beta.4`; no rollout, host flag, sudoers, Tailscale install or kind-cluster changes made.

## Scope

- Connections by type (Cloudflare Tunnel, Tailscale tailnet-only, Kubernetes cluster); `connections` table replaces `cloudflare_connections`; `policy.access []AccessRef` replaces `public_access`; `/api/v1/connections*` replaces `/api/v1/cloudflare*`; `connection:manage` replaces `cloudflare:manage`.
- Service drivers from descriptors (Docker, Compose, Command (renamed from exec), Kubernetes); `config` / `ready` columns + `service_requires` table replace `spec`.
- Requires between services (wake order, keep-awake holds, release, 409 guards, idle option "With dependents", map edge).

## Lab facts

- GUI for Guacamole = system units `gdm` + `gnome-remote-desktop` (inactive; default target multi-user). Starting needs root → sudoers drop-in limited to those commands + `MISHMESH_HOST_COMMANDS=true`. Confirm with Nir before applying.
- No tailscale on the lab: Nir installs, joins with a tagged auth key, approves the host.
- No Kubernetes on the lab: validate with a throwaway `kind` cluster, removed afterwards.

## Next

1. Execute Task 9 Cloudflare connection provider. Resume from `.superpowers/sdd/2026-10-13-pluggable-types/progress.md` in the active worktree; do not re-dispatch completed tasks.
2. Execution order: **1,2,4,5,6,7,8,9,10,11,12,13,14,3,15,16,17,18,19,20**. Physical Task 3 cutover follows Go consumers to avoid temporary encrypted legacy-table emulation. No intermediate checkpoint is deployed.
3. Continue to complete validation, PR/merge, beta.5 release and lab E2E, respecting the approval/action gates below.

## Completed work and evidence

- Task 1 (`af1a3ad`): eight shared field kinds, strict JSON/default/required validation, host markers and redaction. Focused race tests (97.2% coverage), vet/all-consumer compile and independent review passed.
- Task 2 (`9ff522a`): additive typed connection/service stores, derived runtime references, requirements/aliases and transactional ownership/delete guards. SQLite/store/legacy-consumer race suites, Windows/Linux compile/vet and six live isolated Postgres race cases passed; independent review approved.
- Task 4 (`ce31f02`): injected driver registry, generic config definitions, pure legacy converter, pinned matching Kubernetes modules v0.35.4. Compatibility race/compile/vet and services govulncheck passed; independent review approved. Command captured output no longer leaks into errors/status/logs; host markers captured before legacy API normalization, both regression-tested red/green.
- Task 5 (`26b4f7f` + `a7c6a41`, review approved): strict TCP/HTTP readiness and bounded probes, no redirects/client mutation/body leaks, TCP hold cancellation correction, shared legacy TCP validation. TCP errors use stable diagnostics and preserve only context causes. Services/health/ingress race suites, focused API, consumer compile/vet/format passed. Health/ingress fixtures now have valid durable service links while preserving fake runtime states.

- Task 6 (`9aa668a` + `c7a8c6c`, review approved): same-org prospective DFS validation in locked store transactions, deterministic dependents lookup, ownership/error guards. Affected race suites, separate-handle SQLite and live Postgres opposing updates, consumer compile/vet/format passed. Explicit PostgreSQL ReadCommitted fixes the reproduced RepeatableRead stale-snapshot cycle; synchronized regression passes.

- Task 7 (`e3d3a5c` + `62ab79c`, review approved): parallel ordered dependency wakes, idempotent keep-awake holds, stop/idle admission, external and reload reconciliation, -1 mode and tenant-scoped linked readiness. Affected services/ingress/health/controlplane race, consumer compile/vet/format passed. Finite reload admission fixes starvation; reconciliation preserves idle timers and owns failed additions correctly. All three findings reproduced red/green and scoped review approved.

- Task 8 (`74b2141` + `d78d997`, review approved): generic connection registry, AAD-bound sealed secrets, instance actors, serial/coalesced sync, replacement/retirement and bounded close. Twenty-one connection tests, hub/Cloudflare/server race suites, consumer compile/vet/format passed. Synchronous callbacks are cooperative; stalled retired instances block further replacement, while deletion retires the active instance correctly. Deletion regression and scoped fix review passed.

## Decisions and open validation

- Exact rulings, task briefs, reports and review packages live in this plan's ignored `.superpowers/sdd/2026-10-13-pluggable-types/` workspace. They are the execution recovery record.
- Internal `ConnectionIDs`/`service_connections` derive runtime references from arbitrary descriptor field names. They do not expand API payloads. API extraction is Task 13.
- Idle `-1` means With dependents; existing0 remains Never. Command input stays argv lists. Generic defaults/sensitivity and persistent provider ownership/derived aliases remain as planned.
- Missing-key creation must inspect existing encrypted rows first; atomic migration/reopen/corruption/concurrent Postgres proof still belongs to Tasks 3/19.
- Task 7 live driver/config edit policy question is pending for Task 13 (recommend reject until stopped vs save desired state). Runtime defense retains live/held objects with LastError; no workload stop or hold loss.
- Runtime Requires lifecycle is complete. API 409/detail/UI translation remains Tasks 13/15; endpoint-link refresh integration remains Task 14.
- Task 2 minor review finding deferred to Task 14: canonical-collision fixture names use underscores and can mask the intended collision validation; replace with valid hyphenated hosts and assert the proper error path in both stores.
- Task 5 minor deferred to Task 19/final review: TCP cancellation regression needs peer EOF and explicit test-owned cleanup completion assertions.
- Full real Postgres legacy migrations, provider APIs/live tailnet, kind wake traffic, generic API tenancy and browser save/reopen validation remain future gates; compilation/skipped cases are not runtime evidence.
- Task 17 will carry the originally requested full-screen map/overflow fix into this feature worktree with dense viewport checks; the original checkout remains preserved.
- Test Postgres container `mishmesh-types-pg-1791842027` is stopped and retained (no published ports). Its unique isolation RED-test org contains an intentional cycle; use a fresh isolated fixture for whole-DB validation. Temporary Go test containers are retained; no destructive cleanup run.

## Explicit human gates

- Confirm with Nir before lab sudoers or `MISHMESH_HOST_COMMANDS`; prepare exact reviewed contents/commands first.
- Nir installs/joins Tailscale and approves the tagged host. Live Tailscale proof stays pending until those prerequisites exist.
- Print exact destructive cleanup/restore commands and effects for Nir unless he explicitly authorizes agent execution. No direct master push.
