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

Real browser/current database-key reopen and scoped independent review follow the committed final source. Original Task19/20 migration results are historical evidence for the superseded contract, not claims about this clean-slate build. No publication/deployment/live account traffic.
