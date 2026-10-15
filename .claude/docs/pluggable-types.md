# Pluggable access — implementation checkpoint

Updated 2026-10-14. COMPLETE under Nir's clean-slate direction: no legacy/backward compatibility. Conversion/ALTER-upgrade paths, ciphertext/Cedar rewrites, old routes/redirect and import formats are removed. Fresh/current typed databases only; unsupported old or incomplete schemas remain unchanged and cannot create a replacement key. Current key/ciphertext safeguards, Docker/Compose/Command, readiness/Requires, explicit Save and restart, Access/Connect/UDP and provider extensibility remain.

Product commit5b559d939e278b8af7775c3cb8b17392dbcd132a independently APPROVED after amendment eaa41ce and one reproduced/fixed schema-guard finding. The guard matches all17known tables/columns. Fresh/current SQLite and full isolated live PostgreSQL races, current key/secret rejection, controlplane races, six script regressions, vet/build/web checks and final native-binary/browser database/key reopening pass. Old singleton API404 and old page404/no redirect are proven in the browser. Historical fixture data is retained solely for negative rejection tests; production imports it zero times. Reports clean-slate-report.md, clean-slate-review.md and clean-slate-rereview.md record exact evidence. Nothing published or deployed; live account/second-client and authorized publication/CI/release/rollout gates remain external.

## Prior completed scope — superseded by clean-slate amendment above

Tasks13/14/3 are committed b9b1b2, d772082, 199e67a with backend follow-up308e04e. Typed services, explicit atomic Save and restart, fail-closed postcommit invalidation, provider-owned hostname/protocol capabilities, scoped Access refs, durable generation-fenced aliases and Connect preservation are implemented. Legacy schema/ciphertext/key/Cedar migration is atomic. Concurrent first-key publication now stages/syncs before atomic no-overwrite publication; NTFS/Linux regressions pass.

Tasks15–17 committed d9a562e: descriptor-driven services/Connections/Access UI and generic topology. Synthetic fourth-type/eight-kind forms, stale Detect protection, dense topology at four viewport sizes, real owner/admin/member authorization, service restart/failure/readiness/Requires, secret retention/clear/redaction, multi-Access/Connect and HTTP/UDP forms pass. A new browser and final native server binary reopen the retained SQLite database/key and verify persisted values. Visual inspection passes after Tailwind4 migration. Browser floor is documented: Safari16.4+, Chrome111+, Firefox128+.

Task18 committed817dd10: typed operational scripts, readonly snapshots, literal beta4 regression fixtures and API/service/connection docs. Five Python regressions pass. Private scripts stay ignored. Task19 committed6b50b6b: all Windows/Linux Go packages pass race checks using full-suite evidence plus corrected affected E2E reruns; vet, module tidy and12server/agent crossbuilds pass. Web build/lint/npm audit pass with0advisories. Dual-OS govulncheck has0reachable/imported-package findings and one unsuppressed unused OpenPGP module-only advisory. Fresh isolated PostgreSQL17 full suite passes without skips3.787s; final literal migration/concurrent-open/rollback/key/secret-shape matrix passes2.735s. Retained fixtures and exact reports/logs remain available.

Task20 preparation54e1c56 includes proposed unused v0.1.0-beta.7, CHANGELOG, release-candidate.json, local draft PR body and lab-rollout.md. Exact remote tag/release absence was verified2026-10-14. Independent whole-branch review found three concrete issues: unavailable-provider config/runtime redaction, raw service topology diagnostics and missing Cloudflare Zone Read guidance. Root reproduced both disclosures, corrected all three in c63a46f and passed affected Windows/Linux races,12crossbuilds/vet/web build plus fresh browser/final-binary persistence reopening and the actual permission guidance check. Scoped independent rereview APPROVED c63a46f; no blocking findings remain. See task-20-review.md, task-20-fix-report.md and task-20-rereview.md. Live account traffic, joined/tagged Tailscale host approvals/grants and second-client HTTPS proof, authorized publication/CI/tagging and target-specific rollout remain external gates.

## Direction and done definition

- App access over existing networks. Ship Cloudflare public access and Tailscale private HTTPS app access. NetBird/existing WireGuard networks are extension cases, not implemented-provider claims. No VPN peers, keys, routes or policies management.
- Nir cancelled Kubernetes: the deployment platform owns workload lifecycle. No Kubernetes provider/driver, credential executor, UI option or kind gate. Partial Task10 source is preserved untouched in the old pluggable-types worktree and must not be imported.
- Keep generic Docker/Compose/Command drivers, readiness, Requires and explicit **Save and restart**. Normal runtime-config changes require stopped service; restart action checks permissions/dependents, reserves lifecycle admission across stop/save/apply/start, and reports failures by phase. No silent restart or dependent cascade.
- Finish generic connection/service APIs, clean typed initialization, descriptor UI and topology; prove save/reopen, SQLite/live PostgreSQL initialization and unsupported-schema rejection, security and current-main integration; independent review before release. Nir cancelled legacy conversion/backward compatibility after the initial completed implementation.

## Where and source of truth

- Current implementation: C:/Projects/products/mishmesh/.claude/worktrees/pluggable-access-main, feat/pluggable-access. Current validated and independently approved product/test commit c63a46f84c6e01b70c4c79898189534eb89ae338; review base124e5b10738587c2114a533fb43088a1c377f903.
- Main124e5b1 integrated with reviewed providers and service foundations. Conflicts resolved retaining both Connect+Access and privacy policy. Full combined Go race, vet and server/agent compile passed; no tracked file deletion or private lab file resurrection.
- Integration390f77a independent merge-resolution spec/quality PASS. Task12 completedb52a297: original3+N1+N2 verified addressed; final scoped spec/quality PASS,0newfindings. Nir subsequently renewed execution through20; Tasks13–19 are now complete.
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
| 12 | Generic Connections API/authz/bootstrap, conditional writes/deletes, authorized runtime snapshots and safe persistent diagnostics; independently reviewed | 029fe60, 2eb9bb3, 5c23070, b52a297 |

Task9 independent review found two Important issues, both fixed; scoped spec/quality PASS. Replacement waits for Run+Sync+Close, otherwise bounded ErrDrainPending; no overlapping state writers. Deletion remains allowed while draining.

Task11 review found one Important cleanup issue after partial API creation/Serve failure and an operator-added advertisement. Fix0bf452f protects observed foreign advertisements during cleanup/retries/final observation. Exact RED/GREEN retained; scoped spec/quality PASS, no new findings. Task10 is cancelled, not completed.

## Evidence and limits

- Task9 affected CF/connections/server races, all-consumer compilation, vet/format and earlier isolated live PG state/config concurrency proof passed. Deterministic initial-adoption and late-state-writer regressions included.
- Task11 full available Go race suite: 29 tested packages, 3 no-test packages; final affected race/vet/Linux+Windows amd64 builds passed after final changes. HTTP fixtures implement actual published /services schema and LocalAPI Serve/ETag/preferences/approval contracts. Zero k8s.io and OpenPGP imports in actual server/agent dependency graph.
- govulncheck: zero affected symbols/imported packages; existing module-only GO-2026-5932 for unused unmaintained x/crypto/openpgp, no fixed version. Unsuppressed logs retained. This is not an absolute zero-advisory claim.
- Web build/lint and final npm audit pass with0advisories after compatible updates and the tested Tailwind4 migration. Original12-advisory baseline is retained, not the current result.
- Cloudflare and Tailscale locks are process-local. Remote unconditional PUT/DELETE and advertisement preferences lack full CAS; observed fingerprints/claims protect known edits but cannot promise atomicity against unrelated external writers.
- Live isolated PG migration and real browser feature/persistence flows pass. Cloudflare/tailnet HTTP fixtures prove contracts, not live account traffic. No publication/deployment or live second-client HTTPS claim.
- Prior PostgreSQL fixture mishmesh-types-pg-1791842027 is stopped/retained and has a deliberate RED-test cycle. Use a fresh isolated fixture for full validation. Retain fixtures; avoid destructive cleanup.

## Completion and delivery

Implementation, validation, release artifacts and final independent review are complete. There is no remaining local implementation step. The branch and draft PR/runbook are ready for an authorized publication/CI/release/rollout sequence. No kind gate or external write was part of local completion.

## Execution correction from Nir

Nir requested a next-session prompt to FINISH, and explicitly objected to excessive running/repetitive summaries. The previous run spent too much time on serialized review rounds and minute-by-minute waiting narration. Real defects still require fixes, but process activity is not completion. Resume from verified ledger/HEAD, never repeat completed reviews or valid checks without changed code or a concrete new concern. Use bounded work units, cover related lifecycle interleavings together, reuse evidence, and finish implementation/UI/migration before final end-to-end validation. Do not ask whether to continue or stop at a partial milestone. Report exact remaining work and material limits; never label the refactor done because one task passed.

Instruction-source audit: plan line5 recommends subagent-driven-development or executing-plans; ledger line5 records Nir delegated method choice. Controller chose Superpowers6.4.1 subagent-driven-development. Its SKILL.md lines310-314 forbid skipping per-task reviews,372-403 prescribe up to5 fix/review rounds with new fix-introduced findings extending the loop,408-409 forbid controller fixes,498 mandates rereview even small fixes. This local plugin skill drove the serial cycle, not a system requirement for repeated independent reviews. Plan line607 explicitly offers native implementation plus one whole-branch review. Session guidance required validation and frequent communication but did not require this particular review architecture. Do not mechanically reselect the expensive workflow from the historical plan; choose an efficient execution method consistent with Nir's renewed finish/avoid-waste direction, with meaningful checks and bounded independent review. Local plugin files were inspected, not modified.

## Human gates

- No direct master/main push without fresh exact consent. No release/deployment claim before reviewed concrete artifacts and external-action authorization.
- Existing local beta.5/beta.6 tags supersede the historical beta.5 plan target. Verify the next unused candidate during Task20 preparation; no retagging, publication or deployment executed.
- Nir installs/joins Tailscale and approves tagged Service hosting. Verify current prerequisites before live-tailnet proof; credentials enter secret form/environment, never chat/logs.
- Exact lab sudoers and MISHMESH_HOST_COMMANDS changes require Nir's explicit confirmation before application. Do not change GUI/default target independently.
- Destructive commands/cleanup/restore must be printed with effects for Nir to run unless explicitly authorized. Private files and unrelated worktrees are preserved.

## Historical continuation notes — superseded by Active continuation above

Nir renewed authorization to finish all remaining implementation and validation. Active worktree and reviewed baseline b52a297 verified; existing topic-document edits preserved. ORC MCP is available in this session: context(mishmesh) and effective orc-sdlc/agent-protocol/main-base/worker-base/coder loaded. Manual continuation tracked by ORC task zsin1u; no flow node is assigned and no invented flow outcome is reported.

Task13 implemented b9b1b232895872051ec921c57018ce7487dfa8ec. Typed service DTOs/routes, readiness/Requires, transaction-backed authorized snapshots and manager lifecycle admission span explicit Save and restart. Persistence commits before apply/start; phase failures report saved state. Sensitive fields are always redacted. Focused controlplane/services races28.696s/4.362s, fullservices/authz12.266s/1.139s, vet/cmd build/diffcheck pass; store SQLite fullrace3.486s and live retained PG guards1.209s pass. Report task-13-report.md records a preexisting intermittent reach-in transport test error-frame/close race; this remains a Task19 full-suite gate, not a hidden passing claim. Task14 now active with same backend implementer. Root prepares descriptor/service/Connections/Access UI concurrently, to finish after14+3, with TypeScript checks passing so far; browser validation not yet run. No publication, deployment, destructive cleanup or production action.

Use bounded implementation checks and final whole-branch independent review; do not mechanically repeat the historical serial per-task review loop. Next remains Task13 completion, then14,3,15–17,18,19,20 in Nir's requested order.

Continuation evidence: Task14 root store slice now passes live retained PostgreSQL race matrix1.420s (alias/access claim concurrency, record snapshots, provider State independence, stale deletion, canonical collision), log task-14-store-pg-claims.log. Descriptor UI preparation passes TypeScript and production Vite build. Real Playwright synthetic-descriptor renderer scenario passes all eight field kinds plus a fourth fake driver, config/Requires/argv preservation, Connections secret retention and two Access refs with Connect fields retained after reload. Exact .claude/scripts/pluggable-types/check-forms.cjs and output/playwright/descriptor-forms.png; screenshot inspected. This is renderer/mock persistence evidence only: actual backend save/close/reopen and topology/dense/mobile proof remain19/UI gates. The browser assertion first found an accessible label mismatch caused by required-star text; explicit field labels corrected it. Pre-fixture browser open logged expected API500 (no backend yet)/favicon404; scenario has no page errors. Files normalized UTF-8/LF after Windows newline warnings. Nothing deployed/published.

Task19 reach-in regression preparation: existing streamAgent fake used full TCP Close immediately after the error frame while the request remained unread, risking TCP RST/frame loss; real yamux Close is a half-close. Updated only the fixture to CloseWrite then bounded drain, retaining the same 502 and connection-refused assertion. Baseline300 focused repetitions passed25.977s (intermittent full-suite RED logs retained); changed fixture100 combined error/half-close/early-byte race repetitions pass25.503s. Final broad suite confirmation still required. File excluded from14 backend commit and belongs19. Task14 endpoint writes now also carry transient authorized connection snapshots, checked in the store transaction to close role/config read-write races; focusedSQLite1.283s passes. Worker owns API population/provider routing and14 final checks.

## Immediate next action

No further local implementation is pending. ORC MCP/effective guidance were available; its accepted review-only run remained queued while the agent loop was off and was halted without daemon/config changes. The declared independent review scope was completed manually using the native reviewer; no flow outcome was fabricated. agentapi localhost:3284 was unavailable. Native hook-enforcement parity is not claimed. Retained fixtures/worktrees are preserved; nothing published or deployed.
