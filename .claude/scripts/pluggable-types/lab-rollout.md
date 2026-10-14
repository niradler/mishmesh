# Clean-slate Access release and rollout runbook

Proposed candidate v0.1.0-beta.7 is prepared and unpublished. Recheck exact remote tag/release availability before an authorized publication. Existing beta tags remain unchanged. This artifact does not authorize publication, deployment, Tailscale join, host permissions or cleanup.

## Artifacts

Existing GoReleaser produces server/agent archives for Linux/Darwin/Windows × amd64/arm64, Windows ZIP and other-platform tar.gz, plus the web UI archive and checksums.txt. The publication workflow also builds images and stamps both Helm charts with tag version/appVersion. Verify every artifact/job after an authorized tag; local crossbuilds are not publication evidence.

## New installation

This release has no legacy or backward compatibility. Provision a dedicated fresh database and key for a new installation. Old schemas are rejected unchanged; there is no converter, Cedar rewrite, old API alias/redirect or old-format import. Recreate definitions with typed Docker/Compose/Command and Connections APIs/forms. Do not point the candidate at an existing old production database. Keep the previous installation and its original database/key untouched and separate.

For an existing current typed installation, preserve the database, matching key, binary/UI/startup/env files in a private backup location outside served directories. Protect OS permissions/ACL. Rehearse current-schema reopening on an isolated copy with the matching key. Missing keys for ciphertext and malformed authenticated secrets fail closed. Never use a backup rehearsal against the original database.

After target-specific authorization, switch the selected binary/UI/startup configuration atomically to the prepared fresh/current typed installation. Keep control/ingress listeners loopback, existing network ownership/mode unchanged, and enable no managed Cloudflare/host commands/Tailscale as a side effect. Validate actual binary/UI versions, org permissions, typed form save/reopen, readiness/Requires, explicit Save and restart failure state, Connect and UDP traffic.

Read-only snapshots and comparisons support typed installations only. Keep outputs private, outside published artifacts; operator authentication comes from the environment:

```text
python .claude/scripts/pluggable-types/snapshot-api.py <loopback-api> <private-before.json> --org-id <org-id>
python .claude/scripts/pluggable-types/snapshot-api.py <loopback-api> <private-after.json> --org-id <org-id>
python .claude/scripts/pluggable-types/check-regression.py <private-before.json> <private-after.json>
```

## Separate external gates

Live Cloudflare traffic needs the actual account/tunnel and DNS/Access permissions, including Zone Read. Tailscale must already be installed/joined on the tagged host; a human configures ownership, approvals and grants. Verify private HTTPS from a second authorized tailnet client. Managed OAuth credentials enter the secret form/environment, never chat/logs. Add private Access without removing intended Cloudflare refs. Endpoint authentication/IP policy applies to every canonical ingress path; adding private Access does not revoke other paths automatically.

Prepare exact executable/argv and minimal sudoers candidates for optional host services. Validate with visudo -cf and show target-specific commands/environment changes for approval before applying. Do not enable a graphical/default target independently. NetBird/WireGuard remain future existing-network app extensions, with no peer/key/route management. Kubernetes provider/driver/kind is cancelled; existing deployment charts remain supported.

## Failure and preservation

Stop rollout progression on failed startup/traffic checks and preserve redacted logs/data for RCA. Prepare exact resolved stop/switch/restoration commands for the operator. Destructive cleanup/restoration remains separately authorized under Nir's rule. Keep original installations and every retained browser/PostgreSQL fixture; no database drop, recursive cleanup or container removal is included.

## Limits

Local validation covers typed initialization/reopening and unchanged unsupported-schema rejection, race/security/API/UI contracts and real browser persistence. Live account and second-client HTTPS remain external gates. npm audit is clean; govulncheck has zero reachable/imported-package findings and one unsuppressed unused OpenPGP module-only advisory. Remote APIs without CAS retain external-writer limits. Atomic key publication needs a hard-link-capable private directory; NTFS/Linux tested. Protect key files with service/owner-only OS access. Modern UI browser floor: Safari16.4+, Chrome111+, Firefox128+.
