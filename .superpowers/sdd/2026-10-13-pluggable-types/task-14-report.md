# Task 14 implementation evidence

Implemented provider-owned hostname and protocol capabilities, normalized endpoint Access references, durable effective aliases and ingress cache invalidation. Cloudflare and Tailscale declare HTTP access; a test-only third provider declares UDP and a different naming rule without changes to generic API/store code. Providers also own detection field metadata.

The API checks connection org, role, provider protocol, names, duplicate identities/hostnames and public domain authorization. Unchanged normalized refs permit unrelated edits while provider config is invalid; new/changed refs require validation. Authorized connection snapshots are checked again inside endpoint write transactions. Connect is retained when omitted from policy edits and forwarded to agents; Access and service associations are stripped from agent input. Unsupported historical TCP/UDP/TLS access observations cannot publish.

Provider reconciliation saves observed aliases before exposing published status, including autonomous reconciliation. The Hub supplies a conditional writer closed over the original encrypted connection generation, because provider configuration intentionally masks stored ciphertext. Store writers fence removed/renamed refs, endpoint kind changes, stale connection generations and conflicting claims; connection user config changes clear aliases atomically while provider State changes remain allowed. SQLite and PostgreSQL use their existing transaction serialization. Alias changes, endpoint mutations and connection mutations invalidate ingress caches.

Ingress uses globally unambiguous normalized canonical domains first, then exact persisted aliases. Unknown hosts cannot fall through to path routing. Real SQLite/Hub coverage proves encrypted generation fencing, durable publication, database reopen and stale generation rejection. Real ingress coverage proves endpoint authentication before wake, shared service activity and organization metering, original Host/path, gate credential stripping, replacement/removal invalidation and quota enforcement.

Task 13 follow-up: a postcommit authoritative lease failure previously left the stopped old definition wakeable. A reservation now invalidates that runtime before admission reopens. Ensure, Acquire, visibility, requirement validation and refresh fail closed until successful saved reload. Deterministic canceled-second-lease tests cover committed update and delete, and a real manager regression proves the old driver never starts and saved reload restores the new driver.

Validation passed:

- Affected race: connections 7.437s; Tailscale 6.466s; Cloudflare 4.935s; ingress 4.102s; gateway 4.022s; services 12.131s; full SQLite 5.068s (`task-14-affected-race.log`).
- Focused service/Access/Connections/network/topology API race 30.969s (`task-14-api-race.log`); later valid two-Cloudflare normalized duplicate-hostname regression 2.150s.
- Affected vet, all command builds and diff whitespace checks passed (`task-14-build-vet.log`).
- Root live isolated PostgreSQL: alias/connection CAS/claim matrix 1.420s (`task-14-store-pg-claims.log`), latest endpoint authorized-snapshot guard 1.048s (`task-14-store-pg-snapshot-final.log`). Earlier connection conditional-delete assertion retained with a parameterized external SQL writer, because SDK writes now correctly serialize with the held lease.

Initial provider fixtures failed because their in-memory endpoints did not exist in the durable store; fixture conditional alias adapters now validate their actual fake source snapshots. Existing legacy request tests were converted to generic Access requests. No test assertion was suppressed. Canonical collision fixtures use valid hyphenated names so collisions are actually reached. Scoped security checks cover auth/org boundaries, secret masking, safe error DTOs and stale-publication privacy.

Transitional legacy PublicAccess, Cloudflare adapters and service Spec remain only for Task 3 cutover. Full whole-branch checks, real backend browser validation, final security review and release preparation remain subsequent tasks; nothing deployed or published. Root's reach-in transport fixture and migration fixture-retention edits, web changes and execution ledger updates are intentionally excluded from this commit.
