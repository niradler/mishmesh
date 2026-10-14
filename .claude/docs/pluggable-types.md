# Pluggable access — implementation checkpoint

Updated 2026-10-14. Nir authorized finishing, validation and review. Implementation/release is **not finished**.

## Direction and done definition

- App access over existing networks. Ship Cloudflare public access and Tailscale private HTTPS app access. NetBird/existing WireGuard networks are extension cases, not implemented-provider claims. No VPN peers, keys, routes or policies management.
- Nir cancelled Kubernetes: the deployment platform owns workload lifecycle. No Kubernetes provider/driver, credential executor, UI option or kind gate. Partial Task10 source is preserved untouched in the old pluggable-types worktree and must not be imported.
- Keep generic Docker/Compose/Command drivers, readiness, Requires and explicit **Save and restart**. Normal runtime-config changes require stopped service; restart action checks permissions/dependents, reserves lifecycle admission across stop/save/apply/start, and reports failures by phase. No silent restart or dependent cascade.
- Finish generic connection/service APIs, safe legacy migration, descriptor UI and topology; prove save/reopen, SQLite and live PostgreSQL migration, security and compatibility with current main; independent whole-branch review before release.

## Where and source of truth

- Current implementation: C:/Projects/products/mishmesh/.claude/worktrees/pluggable-access-main, feat/pluggable-access. Reviewed provider code HEAD: 0bf452f.
- Main124e5b1 integrated with reviewed providers and service foundations. Conflicts resolved retaining both Connect+Access and privacy policy. Full combined Go race, vet and server/agent compile passed; no tracked file deletion or private lab file resurrection.
- Parked cancelled Kubernetes: ../pluggable-types on feat/pluggable-types, uncommitted Task10 code and task-10-report.md retained. No reset/revert/delete/cleanup executed.
- Spec: docs/superpowers/specs/2026-10-13-pluggable-types-design.md. Plan: docs/superpowers/plans/2026-10-13-pluggable-types.md; its 2026-10-14 cancellation amendment overrides historical Kubernetes text.
- Authoritative execution ledger and amended task briefs/reports/reviews: .superpowers/sdd/2026-10-13-pluggable-types/progress.md and adjacent files. Do not redispatch completed tasks. Root checkout topic file mirrors this checkpoint.
- Private lab scripts/notes remain .claude/local, ignored and preserved. Generic scripts are public; do not republish machine-specific artifacts.

## Reviewed completed work

| Task | Result | Code |
|---|---|---|
| 1 | Strict shared fields/defaults/validation and sensitive-value redaction | af1a3ad |
| 2 | Typed connection/service stores, access aliases, transactional reference guards | 9ff522a |
| 4 | Generic Docker/Compose/Command registry and pure legacy converter; unused Kubernetes forward contracts removed by11 | ce31f02 |
| 5 | Bounded TCP/HTTP readiness and sanitized diagnostics | 26b4f7f, a7c6a41 |
| 6 | Same-org prospective DAG validation and PG isolation correction | 9aa668a, c7a8c6c |
| 7 | Requirements-before-dependent wakes, holds/release, stop guards and finite reload admission | e3d3a5c, 62ab79c |
| 8 | Generic connection registry/hub, sealed secrets, serialized sync, bounded retirement/deletion | 74b2141, d78d997 |
| 9 | Per-connection Cloudflare provider; durable per-resource ownership; no unchanged-resource adoption; post-retirement whole-record refresh | dc13343, 7c9a396 |
| 11 | Tailscale LocalAPI/OAuth/Services, Serve+advertisement, observed approval, ownership/foreign preservation, private scope and naming capability | 2bcc709, 0bf452f |

Task9 independent review found two Important issues, both fixed; scoped spec/quality PASS. Replacement waits for Run+Sync+Close, otherwise bounded ErrDrainPending; no overlapping state writers. Deletion remains allowed while draining.

Task11 review found one Important cleanup issue after partial API creation/Serve failure and an operator-added advertisement. Fix0bf452f protects observed foreign advertisements during cleanup/retries/final observation. Exact RED/GREEN retained; scoped spec/quality PASS, no new findings. Task10 is cancelled, not completed.

## Evidence and limits

- Task9 affected CF/connections/server races, all-consumer compilation, vet/format and earlier isolated live PG state/config concurrency proof passed. Deterministic initial-adoption and late-state-writer regressions included.
- Task11 full available Go race suite: 29 tested packages, 3 no-test packages; final affected race/vet/Linux+Windows amd64 builds passed after final changes. HTTP fixtures implement actual published /services schema and LocalAPI Serve/ETag/preferences/approval contracts. Zero k8s.io and OpenPGP imports in actual server/agent dependency graph.
- govulncheck: zero affected symbols/imported packages; existing module-only GO-2026-5932 for unused unmaintained x/crypto/openpgp, no fixed version. Unsuppressed logs retained. This is not an absolute zero-advisory claim.
- Cloudflare and Tailscale locks are process-local. Remote unconditional PUT/DELETE and advertisement preferences lack full CAS; observed fingerprints/claims protect known edits but cannot promise atomicity against unrelated external writers.
- No live Cloudflare/tailnet, full PG migration, browser feature flow, publication or deployment claim yet. Fake APIs, compile-only and skipped PG tests are not live evidence.
- Prior PostgreSQL fixture mishmesh-types-pg-1791842027 is stopped/retained and has a deliberate RED-test cycle. Use a fresh isolated fixture for full validation. Retain fixtures; avoid destructive cleanup.

## Remaining execution order

1. Main integration validated; dispatch Task12 from the integrated branch.
2. Task12 generic connections API/authz/secret/reference guards and remove live old Cloudflare wiring.
3. Task13 generic services API and explicit restart lifecycle; Task14 provider-owned naming/protocol capabilities, access aliases and preserved Connect/UDP.
4. Task3 atomic schema/key/ciphertext/policy migration and removal of temporary adapters, reopen and missing/wrong-key proof.
5. Tasks15–17 descriptor forms/details, Connections/Access UI, topology/Requires and bounded full-screen Network layout; real API/browser save/reopen.
6. Task18 generic regressions/imports/docs and private local lab scripts.
7. Task19 full race/build/security/dependency/OS-arch/browser and live isolated PG migration gates. Retain PG databases instead of DROP cleanup; no test suppression. No kind gate.
8. Task20 reviewed release notes/runbook/metadata; final independent whole-branch review before merge/tag/publication or lab rollout.

## Human gates

- No direct master/main push without fresh exact consent. No release/deployment claim before reviewed concrete artifacts and external-action authorization.
- Nir installs/joins Tailscale and approves tagged Service hosting. Verify current prerequisites before live-tailnet proof; credentials enter secret form/environment, never chat/logs.
- Exact lab sudoers and MISHMESH_HOST_COMMANDS changes require Nir's explicit confirmation before application. Do not change GUI/default target independently.
- Destructive commands/cleanup/restore must be printed with effects for Nir to run unless explicitly authorized. Private files and unrelated worktrees are preserved.

## Immediate next action

Main-based integration is validated. Dispatch Task12 generic Connections API/authz/secret/reference guards from its amended brief and task-12-context.md in this active worktree. The reviewed predecessor branch feat/pluggable-access-base in ../pluggable-access is retained; cancelled Kubernetes remains ../pluggable-types. Do not reimplement completed providers or revive Kubernetes.
