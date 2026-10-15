# Clean-slate amendment

Nir explicitly cancelled legacy/backward compatibility after the prior completed refactor. That decision supersedes Task3 conversion and historical release/upgrade requirements. Base635a7ad. This unit removes compatibility, preserves retained fixtures/private files and never erases an existing database.

## Implementation

Removed schema/service converters, legacy ciphertext/AAD migration, Cedar action rewrites and migration-only store/key interfaces. Fresh schema contains config/ready/state/method directly; there are no ALTER-based old-schema upgrades. Existing unsupported schemas fail closed before SQLite journal/schema changes and before missing-key creation. PostgreSQL checks inside its serialized initialization transaction. Current typed databases reopen normally.

Current sealed-secret validation belongs to startup, using the shared matching box and strict connections.OpenConfig. Missing/wrong key and authenticated null/number/duplicate secret values still fail without ciphertext changes. Ordinary typed CRUD, authorization snapshots, aliases, readiness/Requires and explicit Save and restart remain intact.

Removed old Cloudflare page/redirect, re-export/cache/action-label remnants and an empty singleton source file. Operational service imports accept typed config/ready/requires and integer `_seconds` fields only; old nested blocks/spec/exec/timer fields are rejected. Snapshots/comparisons support typed data only. Config output is conservatively masked for extensible sensitive fields. Current docs/release runbook now require fresh installation and contain no migration promise.

Historical literal SQL/data fixtures are retained solely as negative rejection evidence. Old conversion assertions are replaced by unchanged-database rejection plus fresh/current reopen assertions, not suppressed. Original edited sources were retained in ignored output/clean-slate-retained. That archive is a separate Go module because an initial vet correctly discovered copied .go fragments beneath the root module; final root vet passes after isolating the archive. No production data/file cleanup.

## Executed validation

- SQLite full race PASS4.268s: literal old schema refused unchanged, incomplete current state column refused unchanged, fresh CRUD/claims/snapshots and current private-state reopen.
- Services/authz/connections/cmd races PASS12.218s/1.155s/5.061s/2.502s. Additional current wrong-key/malformed-secret/missing-key/startup and sensitive-env tests PASS2.556s/2.319s. Unsupported database refuses before replacement-key creation.
- Affected controlplane connection/service/topology/typed/Requires/restart races PASS25.833s, output/clean-slate-controlplane.log.
- Fresh retained isolated PostgreSQL17 full suite PASS2.997s, no skips: initialization/concurrent opens, CRUD/CAS/snapshot/alias races and8concurrent unsupported-schema refusals with original rows/tables unchanged. output/mishmesh-pluggable-pg-1791996158459734000/test.log. Fixture and databases retained, no host ports or DROP cleanup.
- Six Python script regressions PASS1.203s including typed imports, old-format rejection, exact snapshot identity/config/Access checks and read-only secret-safe output.
- Whole vet final PASS. All12server/agent OS/arch crossbuilds PASS before the final removal of an unreachable config fallback; final source compile/build follows. Web format/lint/typecheck/build PASS. Dependency graph/lockfiles unchanged, reuse prior npm audit0 and dual-OS govulncheck reachability evidence.

## Final correction and approval

Amendment eaa41ce was reviewed independently. One concrete finding: the guard initially covered only3tables, allowing incomplete orgs/Requires/alias schemas to create a key or change schema before failing. Exact new regression failed for all3cases before correction (output/clean-slate-schema-red.log). Correction5b559d9 covers every known table. SQLite fullrace3.776s and cmd fullrace3.103s PASS; fresh full live PostgreSQL3.083s, no skips, including new incomplete-known-table probe/Open unchanged-table/column tests. Final fixture output/mishmesh-pluggable-pg-1791996844136415700/test.log retained. No cleanup.

Whole final vet and Windows native build PASS after correction; dependency/platform code unchanged from the12passing crossbuilds at eaa41ce. Fresh browser plus corrected native binary reopen the same typed SQLite/key and verify secret masks/retention, Requires, Access+Connect and UDP. Old singleton API404 and /cloudflare page404 without redirect PASS, output/clean-slate-browser-final.log.

Final Linux SQLite/cmd affected rejection/key/current-state races also PASS1.734s/1.591s, output/clean-slate-linux-final.log. The retained runner has no published ports. Final browser/server owned processes stopped, with database/key and all PostgreSQL fixtures retained.

Scoped independent rereview APPROVED5b559d939e278b8af7775c3cb8b17392dbcd132a, exact regression independently PASS1.403s, schema inventory independently matches actual SQLite DDL17/17tables/allcolumns, no new blockers. clean-slate-review.md and clean-slate-rereview.md retain both verdicts. ORC effective guidance available; manual native reviewer reused because prior review dispatch stayed queued. No fabricated flow outcome, daemon change or durable memory/wiki write.

Original Task19/20 migration results are historical evidence for the superseded contract, not claims about this clean-slate build. No local implementation remains, publication/deployment/live account traffic or destructive data cleanup. External gates remain actual provider accounts/approved joined tagged tailnet host+secondclient and authorized publication/CI/release/target rollout.
