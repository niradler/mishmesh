# Pluggable types — implementation checkpoint

Updated: 2026-10-14. Nir renewed authorization to finish, validate and review the refactor. Subagent-driven execution remains selected. The overall feature/release is **not finished**.

## Confirmed use case (2026-10-14)

- Nir selected **app access over existing networks**. Finish Cloudflare public access and Tailscale private access; preserve extensibility for NetBird and existing WireGuard networks. VPN peers, keys, routes and network policies are externally managed and outside this release.
- NetBird/WireGuard are architectural extension cases, not implemented provider claims. An existing private network may transport traffic to Mishmesh ingress without Mishmesh managing that network. Provider status must distinguish observed publication, prerequisites and conflicts; merely configuring a network is not proof an app is reachable.
- Kubernetes runtime, service drivers, Requires, migration, generic APIs/UI and validation remain in the approved refactor scope.
- Root checkout topic file was stale on 2026-10-14; the worktree checkpoint and SDD ledger remain authoritative.

## Current state and where to work

- Active implementation: `C:/Projects/products/mishmesh/.claude/worktrees/pluggable-types`, branch `feat/pluggable-types`.
- Current code HEAD: `dc1334344cc840efad7db323641bc86d7c66772a` — Cloudflare provider implementation.
- Tasks **1, 2, 4, 5, 6, 7, 8** completed with independent review. Task **9** review returned Needs fixes: initial adoption of external resources and stale ownership on reload. Fix round 1 dispatched to `/root/fix09` from `dc13343`; independent scoped review follows.
- Original checkout `C:/Projects/products/mishmesh` is currently on `main` at `124e5b1`. Do all feature code work in the linked worktree; preserve other worktrees and historical browser artifacts.
- Lab was reported running `v0.1.0-beta.4`. This implementation has not pushed, created a PR, merged, published, deployed or changed lab configuration.
- Physical schema/data cutover (Task 3) deliberately follows Go consumers 4–14. Legacy adapters are temporary; none may ship in beta.5.

## Source of truth for resuming

Within the active worktree:

- Spec: `docs/superpowers/specs/2026-10-13-pluggable-types-design.md`.
- Plan: `docs/superpowers/plans/2026-10-13-pluggable-types.md` (checkboxes updated after review gates).
- Recovery ledger: `.superpowers/sdd/2026-10-13-pluggable-types/progress.md`. Read it first; it records task bases, commits, rulings, deferred findings and live-agent state.
- Per-task briefs, reports, review packages and verdicts are in the same ignored SDD directory. They are retained, not deleted.
- This topic file is mirrored in the original checkout for convenience. The active worktree ledger/plan controls execution; do not redispatch completed tasks.

## Completed work

| Task | What changed | Code commits / evidence |
|---|---|---|
| 1 | Eight shared field kinds; strict JSON/default/required validation, host-command metadata and secret/sensitive-value redaction | `af1a3ad`; focused race coverage 97.2%, compile/vet/format and review passed |
| 2 | Additive typed connection/service stores, runtime references, requirements, access aliases and transactional same-org/delete/collision guards | `9ff522a`; SQLite/store/legacy races and six live isolated PostgreSQL cases passed; review approved |
| 4 | Injected service-driver registry, Docker/Compose/Command descriptors, generic config definitions, pure legacy converter; pinned matching Kubernetes modules v0.35.4 | `ce31f02`; compatibility races, compile/vet, govulncheck and review passed. Removed command output from public errors; fixed host authorization before normalization |
| 5 | Strict TCP/HTTP service readiness, bounded probes, redirect/body handling, legacy TCP validation and TCP cancellation | `26b4f7f` + `a7c6a41`; services/health/ingress races passed. Review found raw TCP diagnostics; sanitized them, reproduced red/green, scoped review approved |
| 6 | Prospective same-org DAG validation inside store transactions, deterministic dependents lookup and cross-handle concurrency protection | `9aa668a` + `c7a8c6c`; SQLite and live PG opposing updates passed. Review found Repeatable Read stale snapshots before lock wait; live four-node cycle reproduced, Read Committed fix/regression approved |
| 7 | Parallel requirements-before-dependent wakes, idempotent holds, manual-stop/idle admission, external/reload reconciliation, -1 With dependents and linked readiness fallback | `e3d3a5c` + `62ab79c`; affected services/ingress/health/controlplane races passed. Review found refresh idle resets, failed-addition hold leak and reload starvation; all three reproduced/fixed and scoped review approved |
| 8 | Generic connection registry/hub, identity-bound sealed secrets, serialized/coalesced sync, generation retirement and bounded close | `74b2141` + `d78d997`; 21 connection race tests and consumer gates passed. Review found deletion blocked by stalled retirement; fixed and scoped review approved |

## Task 9 — completed after independent fix review

Commit `dc13343` implements the Cloudflare provider and startup registration. Access refs select hosts by connection ID; full-org observation preserves unclaimed reporting. Same-org instances have independent publishers/supervisors. Legacy API/hub wiring stays until Task 12.

Origin matching alone was unsafe for pruning with shared connections. A durable ownership candidate/default now records successful, observed ingress/DNS/host-specific Access fingerprints. Pruning requires matching ownership/fingerprints and no other current claim; unknown orphans and modified foreign resources remain untouched. Selected unchanged sync cannot adopt operator edits. Shared remote target updates use ordered, context-aware process-local locks.

Added private `Connection.State` (`json:"-"`), nullable state storage and atomic same-org state-only updates in both backends. Ordinary config updates preserve state; state writes preserve config/edit timestamps. Hub copies clone state. Tailscale Task 11 will reuse this private state. No new wire field.

TDD red/green includes source selection, provider contracts, state persistence, ownership, selected-resource edits and normalized read-back. Final affected Cloudflare/connections/SQLite/server race suites, consumer compilation, vet/format and the focused live PG state/config test passed. Tests use fake HTTP APIs/helper executables; this is not live Cloudflare evidence. Review package: `review-e51084e..dc13343.diff`; report: `task-9-report.md`; verdict goes to `task-9-review.md`.

Review in progress has flagged two potential Important gaps: initial sync can adopt pre-existing resources without applying a write, and reload can capture stale private state before an old sync finishes saving ownership. Verdict/report still pending; these require assessment and likely a fix/review loop before Task 9 approval.

## Remaining work — execution order

1. Finish Task 9 independent review and any bounded fix/re-review loop.
2. Task 10: Kubernetes connection/config/exec guard, permissions and scale driver; fake API/client tests and cross-platform compilation.
3. Task 11: Tailscale LocalAPI + OAuth/API provider, durable ownership, foreign-resource preservation, approvals and aliases.
4. Task 12: generic connection CRUD/detect/sync/descriptor APIs, tenancy/auth/secret guards; replace `cloudflare:manage` with `connection:manage`, remove old Cloudflare routes/wiring.
5. Task 13: generic service APIs/config/readiness/Requires, recursive host permission checks, stop/delete 409s and edit-policy integration.
6. Task 14: access refs, aliases/ingress precedence, agent stripping, link-refresh integration and deferred collision fixture correction.
7. Task 3: atomic legacy DB/key/secret/policy/action migration and removal of temporary adapters; missing/corrupt-key, rollback and reopen proof in both stores.
8. Task 15: descriptor-based service UI, readiness/Requires/idle controls and stop-dependent flows.
9. Task 16: connections UI, type panels and endpoint access editor.
10. Task 17: typed topology nodes/Requires edges plus the requested full-screen map and hidden outer overflow. Carry the tested layout into this feature worktree without changing the original checkout; browser-test dense maps at desktop/tablet/phone sizes.
11. Task 18: regression/import/scripts/docs updates.
12. Task 19: full backend/security/dependency/browser validation, live PostgreSQL migration gates and a throwaway kind wake/traffic proof.
13. Task 20: release docs/runbook/metadata preparation; task review and whole-branch review **before** merge/tag/publication. Then PR/CI/merge, `v0.1.0-beta.5`, verified artifacts, backed-up lab rollout and baseline/extended E2E.

## Decisions and human gates

- Live driver/config edits: Nir selected explicit **Save and restart** on 2026-10-14. Normal Save requires stopped runtime for driver/startup changes; the explicit action validates old/proposed/transitive permissions and dependents before stopping, reserves lifecycle admission across stop/save/apply/start, and reports each failure phase accurately. Tasks 13/15 own API/runtime/UI and deterministic concurrency/browser proof. Requires-only edits retain existing behavior.
- Cloudflare prune preference: asked durable ownership versus keeping all unclaimed routes. No answer recorded; durable ownership is a reversible implementation candidate/default, not authorization for live deletion. It can still be adjusted before release.
- **Confirm with Nir before** lab sudoers for exact `systemctl start/stop gdm/gnome-remote-desktop` and `MISHMESH_HOST_COMMANDS=true`. Prepare exact reviewed contents/commands first.
- **Nir installs/joins Tailscale himself**, tags the node and approves Service hosting. Live tailnet proof remains pending until those prerequisites exist.
- Kubernetes live validation uses a throwaway kind cluster. Destructive cleanup/restore commands must be printed with effects for Nir unless he explicitly authorizes execution. No direct master push.
- Runtime host-command permission matches Command: API writes require operator flag plus request `host:exec`; background startup uses the operator flag for previously authorized stored definitions. Plugin-backed management/recursive wake API tests remain Tasks 10/12/13.

## Deferred checks and limitations

- Task 2 minor, owned by Task 14: canonical-collision fixtures use underscores and may fail hostname validation first; replace with valid hyphenated hosts and assert the intended collision path in both stores.
- Task 5 minor, owned by Task 19/final review: real TCP cancellation test should assert peer EOF and explicit test-owned cleanup completion.
- Hub callbacks must cooperate with cancellation. Contextless Open/Validate/Status remain synchronous; recursive Reload from admission callbacks is unsupported. Stalled retired generations block additional replacements; active deletion is covered. Providers 9–11 must respect these lifecycle boundaries.
- Cloudflare target locks are process-local; out-of-band remote edits are guarded by observed fingerprints, not claimed remote transaction atomicity. Target identity changes leave historical remote resources untouched.
- Full repository integration, real PG legacy migration, live providers/tailnet, kind traffic, browser save/reopen and lab rollout are still outstanding. Compile-only/skipped tests are not runtime evidence.

## Validation fixtures and original network fix

- Test PostgreSQL container `mishmesh-types-pg-1791842027` is **stopped and retained**, network none/no published ports. Focused DAG/isolation/private-state tests ran live. Its unique isolation RED-test org deliberately retains a cycle; use a fresh isolated fixture for whole-DB validation.
- Go validation containers are retained; no destructive cleanup was run. Docker Go 1.26.9 uses read-only worktree mounts and named module/build caches. Every command uses explicit process/test timeouts.
- Earlier network layout fix remains local on `work/network-layout`: bounded full-height AppShell/Network/TopologyMap, internal map scrolling and visible legend. Web lint/build and dense 61-node/60-edge Playwright checks passed at 1920×1080, 1440×900, 1024×768, 390×844 and 844×390. Live page was Cloudflare-login gated; no deployment claim. Its script/artifacts remain preserved.

## Immediate next action

Task 9 fix round 1 is active (`/root/fix09`). Review its committed fix with a scoped independent reviewer, update Task 9 plan checkbox only after approval, then dispatch Task 10 from its prepared brief/context. Do not start from the original checkout or repeat Tasks 1/2/4–8.

## 2026-10-14 Task9 fix evidence

Both original Important findings addressed in 7c9a396: ownership only after successful per-resource application; reload waits for Run+Sync+Close and refreshes/revalidates the authoritative whole record. No overlapping state writers. Bounded ErrDrainPending preserves independent-connection progress and deletion while draining. Deterministic RED/GREEN regressions include fully pre-existing and mixed resources, real Cloudflare ownership/reload, late writers, repeated retry, config/secrets refresh and identity mismatch. Affected CF/connections/server race, all-consumer compile, vet/format passed. task-9-r1-report.md and task-9-rereview-1.md contain evidence; scoped spec/quality PASS, zero open findings. Historical review-pending paragraphs above describe the pre-fix checkpoint and are superseded. No live provider/lab or full repository runtime claim.
