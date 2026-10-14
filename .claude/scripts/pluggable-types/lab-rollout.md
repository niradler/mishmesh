# Pluggable Access release and rollout runbook

This is a reviewable preparation artifact. It does not authorize a push, merge, tag, publication, deployment, sudoers change, Tailscale join or cleanup. Machine-specific hosts, users and private paths belong in ignored `.claude/local` notes. Kubernetes provider/driver/kind work is cancelled; existing deployment charts remain supported.

## Candidate and artifacts

Proposed candidate: `v0.1.0-beta.7`, verified unused locally, by exact remote tag lookup and GitHub release lookup on2026-10-14. Existing beta.5/beta.6 tags are retained. Recheck availability immediately before an authorized tag operation. Tag only the reviewed merged commit after CI passes; do not retag an existing release.

The current `.goreleaser.yaml` produces server/agent archives for Linux, Darwin and Windows, amd64/arm64. Names are `mishmesh-{server,agent}_0.1.0-beta.7_<os>_<arch>.tar.gz`, with `.zip` on Windows. UI is `mishmesh-webui_0.1.0-beta.7.tar.gz`; `checksums.txt` includes it. Existing release workflow publishes server/agent images and both Helm charts. Chart packaging supplies `--version` and `--app-version` from the tag, so development Chart.yaml versions remain unchanged. Verify every publication job and artifact/checksum/version independently after authorization; a binary job alone is insufficient.

## Before any deployment

1. Select the target from the private runbook. Read its current binary/UI versions, startup mechanism, private env paths, data backend/DSN and exact matching secret-key path without printing credentials. Confirm the control/ingress listeners stay loopback. Leave existing VPN/tunnel routing and provider management mode unchanged.
2. Read a baseline with the existing compatible operator checks. Use an explicit organization and a private output location:

```text
python .claude/scripts/pluggable-types/snapshot-api.py <loopback-api> <private-before.json> --org-id <org-id> --legacy
```

`--legacy` is only for the old singleton/spec API. Omit it for an already typed source. Authentication comes from private environment; secrets are never command arguments. The snapshot helper only GETs, redacts legacy env values and refuses to overwrite an existing file.

3. Back up the database consistently: SQLite backup while live or a consistent stopped copy; PostgreSQL consistent database backup. Copy the matching key, binary, UI, startup and env files into a private backup directory outside all served directories. Protect directory/file access with the target OS permissions/ACL. Verify the old binary can read a backup copy without migration. Never test against the original database.
4. Stage the candidate binary/UI beside the current installation and verify checksums. Rehearse migration on an isolated copy with the matching key. Close/reopen twice, compare stable IDs/configs/readiness/timers/Requires/Access and locally decrypt connection secrets without printing them. Missing/wrong key or malformed record must leave the old schema/rows intact.

## Authorized switch

After approval for the concrete target/artifacts, stop under the existing startup mechanism, preserve the prior installation, switch binary/UI atomically, and start with the verified matching key. Startup migrates legacy service specs, Cloudflare encrypted rows/public selections and parsed Cedar actions in one transaction. `exec` becomes `command`; legacy API writes are retired. Do not enable managed Cloudflare, host commands or Tailscale as a side effect.

Capture safe startup/migration logs and verify health, actual binary/UI version, all original service/endpoint/connection identities, selected hostnames and unclaimed/orphan reporting. Capture a new read-only snapshot and compare it:

```text
python .claude/scripts/pluggable-types/snapshot-api.py <loopback-api> <private-after.json> --org-id <org-id>
python .claude/scripts/pluggable-types/check-regression.py <private-before.json> <private-after.json>
```

Run the private operator E2E using its established ingress/public/health checks. Default mode has no service mutations. `--wake <service>` or `--requires-wake <dependent>` explicitly stops/wakes only the selected graph. `--crud` retains its synthetic probe and prints cleanup separately. Verify existing Connect and UDP traffic. Confirm a saved restart failure leaves the new config saved and a retry succeeds after correction.

## Separate host/Tailscale gates

For an optional GUI requirement, verify the actual executable path and exact permitted argv in the private runbook first. Prepare the service payload and minimal sudoers candidate; validate the candidate with `visudo -cf`. Show the exact drop-in/install commands and host-command env change for approval before applying them. Do not enable a desktop/default target independently. Once authorized, link the requirement while preserving all endpoint config/Access and verify order, holds, stop409 and release.

Tailscale must already be installed/joined on the selected tagged host. A human configures tag ownership, approvals and access grants. Imported mode observes existing LocalAPI; managed mode requires explicitly supplied OAuth credentials. Add a private ref without removing Cloudflare. Validate HTTPS from a second authorized tailnet client. Fake HTTP/LocalAPI tests and a configured/pending service are not live traffic proof. NetBird/WireGuard are future existing-network extension targets, with no peer/key/route management.

Private Access uses the existing endpoint policy and canonical ingress routes. Configure endpoint authentication/IP policy for every network path that may reach the canonical ingress; adding a private ref does not automatically revoke other configured access paths.

## Failure and restoration

Stop progression on a failed migration/traffic check. Preserve the failed migrated copy, key and redacted logs for RCA. An old binary needs its matching pre-migration database and key, not the migrated database. Prepare exact target-specific stop/switch/restore commands with their resolved paths and effects for the operator. Under Nir's destructive-command rule, print those commands for him to execute unless he explicitly authorizes the agent to run them. Do not discard failed copies or backups.

PostgreSQL validation containers/databases and browser fixtures are retained. No automatic `DROP DATABASE`, container removal, recursive deletion or rollback cleanup is part of this runbook. Print any proposed cleanup as a separate operation and leave unrelated fixtures intact.

## Release limits

Implementation/validation covers typed APIs, Cloudflare/Tailscale contracts, durable generation-fenced aliases, descriptor UI, permissions, lifecycle races, literal SQLite/live PostgreSQL migrations and real native browser persistence. Live account traffic and production rollout remain separate external gates. npm audit is clean; govulncheck reports zero reachable/imported-package findings and one unsuppressed module-only advisory for unused OpenPGP. Remote resource reconciliation is process-local and cannot provide external compare-and-swap guarantees when the provider API lacks them. Atomic first-key publication requires a hard-link-capable key directory (NTFS/Linux tested); protect it with owner/service-only OS access.
