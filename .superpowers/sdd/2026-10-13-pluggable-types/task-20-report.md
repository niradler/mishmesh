# Task20 — release preparation and final review

Prepared 2026-10-14. Independent whole-branch review completed with three concrete findings, reproduced and fixed. Scoped rereview pending; no publication or deployment. See task-20-review.md and task-20-fix-report.md.

## Artifacts

- Proposed v0.1.0-beta.7: local tag absent, exact remote tag lookup exit2/empty and GitHub release lookup reports release not found. Retained task-20-release-candidate-check.json records the actual checks. Availability must be rechecked immediately before an authorized tag.
- CHANGELOG Unreleased documents shipped behavior, API/schema incompatibility, matching database/key backup, modern browser floor and validation/security limits.
- .claude/scripts/pluggable-types/release-candidate.json records candidate/base/validated code commit, platforms and external gates. No Chart.yaml version churn: existing publication workflow supplies tag version/appVersion to both charts.
- lab-rollout.md covers readonly baseline/post-migration comparison, isolated copy rehearsal, matching database/key/binary/UI backups, explicit target switch, failure preservation/restoration, separate host/Tailscale approval gates and retained fixtures.
- pr-body.md is a local reviewable draft, not a created PR.

## Validation

Task19 report and retained logs are authoritative for implementation checks. No product changes in this preparation unit; reuse those passing races, vet,12release crossbuilds, npm audit/build/lint, browser persistence and live isolated PostgreSQL evidence. Release workflow and GoReleaser artifact/chart versioning were inspected. Final diff/metadata/security check runs before the preparation commit. Independent review follows the committed artifact, covering the branch from main base124e5b10738587c2114a533fb43088a1c377f903.

## Limits and gates

Live Cloudflare/tailnet account traffic and second-client HTTPS require configured external accounts/approved joined host. Remote APIs without CAS retain documented external-writer limits. Unused OpenPGP module-only advisory remains unsuppressed. No branch publication/PR, main push, tag, release, deployment, sudoers/VPN-policy changes or destructive fixture cleanup performed. ORC agentapi backend is unavailable; supported independent review backend is available. No durable memory/wiki writes or hook parity claim.
