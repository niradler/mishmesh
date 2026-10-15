# Task 13 — typed services and explicit Save and restart

Date: 2026-10-14. Base: `b52a297`. Branch: `feat/pluggable-access`, active worktree `pluggable-access-main`. Kubernetes remains cancelled.

## Implemented

- Authenticated `GET /api/v1/service-drivers`, using the exact registry shared with the runtime.
- Strict typed `{name,driver,config,ready,requires,idle_timeout_seconds,start_timeout_seconds}` API for Docker, Compose and Command. Legacy request `spec` is rejected. Descriptor validation happens before secret merging, including duplicate config/environment keys. DTOs include same-org `required_by`, Requires, linked endpoints and status.
- Every descriptor connection field is resolved by its own key and expected connection type; all validated IDs are persisted in `Service.ConnectionIDs` for transactional deletion guards. Missing, foreign and wrong-type references give organization-safe validation errors.
- Sensitive descriptor values are redacted for every reader, including host-command users. Omitted unchanged environment values and existing redacted placeholders preserve their persisted secret. Invented/literal credential placeholders are rejected. Service status and operation errors do not return raw driver diagnostics.
- Both old/proposed configurations and transitive requirements require host-command authorization before mutation or control. Service write remains the permission for start/stop. Store-backed snapshot leases reject stale old/transitive authorization before runtime effects and block concurrent configuration writers while an effect executes.
- Context-aware manager admission reservation covers drain, stop, committed save, authoritative apply and start. Already admitted recursive dependency wakes are drained before checking dependents. Ingress wakes/acquisitions and concurrent reloads cannot interleave the old runtime during save. Superseded reload snapshots are discarded.
- Runtime configuration, readiness and start timing edits on running/held services return 409 unless explicitly requested with `?restart=true`. Metadata/idle/Requires-only edits retain their runtime driver. External running state is probed before accepting runtime changes.
- Save and restart uses two transactional leases under one lifecycle reservation: validate/stop/save/commit first, then fresh authoritative validation/apply/start. Stop failure keeps the old saved row; failed save keeps the old row stopped; failed start keeps the committed new row. Responses expose `phase` and `saved` without claiming rollback.
- Stop conflicts return typed dependent IDs/names/states. Delete checks linked endpoints and required-by references again inside the transaction before stopping, so a late endpoint/dependent cannot be stopped or deleted by a stale request. Requires changes retain atomic graph validation.
- Failed starts with uncertain runtime state retain protection; a constructed incapable `brokenDriver` remains recoverable. Metadata/Requires edits do not recreate a runtime driver and discard its state.
- Root implemented the SQLite/PostgreSQL snapshot/transaction adapter slice; included here with its tests. Added a reusable timed validation runner at `.claude/scripts/check-pluggable-service-api.py`.

## RED and corrections

Initial equivalent-contract suite found the newly split no-manager reload needed its own nil guard, driver discovery needed a tenancy-route probe, and idle cascade suppression must apply only to an explicit reservation rather than ordinary Apply. All were fixed.

New coverage identified and fixed state loss from rebuilding a driver during metadata-only Apply, ambiguity lost by canonicalizing config before strict validation, and incorrectly retaining an incapable broken driver after a failed attempt. The old metadata build-count assertion now checks the stronger runtime-preservation contract. A late-endpoint regression initially lacked its proxy-agent foreign key; the fixture was corrected rather than changing store guards.

The full affected race attempt additionally reproduced the existing unrelated `TestReachInHTTPAgentDialFailureIsBadGateway` transport race: HTTP 502 was returned with `tunnel read failed`, where the assertion expects `connection refused`. Twenty focused repetitions passed, but a later full race run reproduced it. No assertion/test was skipped or weakened. Root owns the transport RCA/fix and final whole-branch checks in Task 19. Therefore the final full controlplane run is **not** claimed green by this task.

## Passing evidence

- Earlier complete affected race run: controlplane 87.250s, services 11.970s, authz 1.152s, SQLite 3.935s, PostgreSQL package 1.351s. Later service edits and regressions were validated again with the focused checks below; the unrelated transport failure above prevents treating the earlier run as final whole-branch proof.
- Final focused race command: `go test -race -count=1 -timeout=120s -run 'TestShould|TestNetworkRolePermissions|TestService|TestApply|TestCrossTenantEveryRoute' ./internal/controlplane ./internal/services` — PASS. Covers new typed contracts, reference validation, secrets, all restart phases, stale authorized actions, stale transitive authorization, late delete references, typed stop conflicts and lifecycle reservation races. Retained output: `task-13-focused-checks.log`.
- `go vet ./internal/controlplane ./internal/services ./internal/store/...` — PASS.
- `go build ./cmd/...` — PASS.
- Root's SQLite snapshot/rollback/concurrent-writer race regressions and full affected SQLite race suite — PASS; retained `task-12-task13-store-full.log`.
- Root's live isolated retained PostgreSQL snapshot/rollback/concurrent-writer race regressions — PASS, 1.209s; retained `task-13-store-pg.log`. This is snapshot-store evidence, not Task 19's full historical-schema migration evidence.
- Full services/authz race rerun after the final manager correction — PASS.
- Gofmt applied to changed Go files; scoped `git diff --check` — PASS.

## Security self-review and remaining scope

All new routes carry explicit permissions. Inputs use strict object and descriptor validation; IDs are organization-scoped. Both requested and required host configurations are authorized against transactionally guarded snapshots before effects. Transaction adapters use parameterized existing store queries, bounded caller contexts and existing network write serialization; PostgreSQL additionally locks expected rows. Sensitive values and raw errors are absent from service DTOs/error responses. No dependencies, production writes, publication, deployment, cleanup or private-file changes were introduced.

Task 13 backend implementation and its focused validation are complete. Browser save/reopen, historical-schema live PostgreSQL migration, operational docs, final broad checks and whole-branch independent review remain scheduled tasks, not completed evidence. The unrelated reach-in transport race is tracked for root's Task 19 resolution.
