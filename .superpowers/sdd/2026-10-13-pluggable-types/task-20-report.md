# Task20 — release preparation and final review

Complete 2026-10-14. Independent whole-branch review completed with three concrete findings, reproduced and fixed in c63a46f. Scoped independent rereview APPROVED, no blocking findings. No publication or deployment. See task-20-review.md, task-20-fix-report.md and task-20-rereview.md.

## Artifacts

- Proposed v0.1.0-beta.7: local tag absent, exact remote tag lookup exit2/empty and GitHub release lookup reports release not found. Retained task-20-release-candidate-check.json records the actual checks. Availability must be rechecked immediately before an authorized tag.
- CHANGELOG Unreleased documents shipped behavior, API/schema incompatibility, matching database/key backup, modern browser floor and validation/security limits.
- .claude/scripts/pluggable-types/release-candidate.json records candidate/base/validated code commit, platforms and external gates. No Chart.yaml version churn: existing publication workflow supplies tag version/appVersion to both charts.
- lab-rollout.md covers readonly baseline/post-migration comparison, isolated copy rehearsal, matching database/key/binary/UI backups, explicit target switch, failure preservation/restoration, separate host/Tailscale approval gates and retained fixtures.
- pr-body.md is a local reviewable draft, not a created PR.

## Validation

Task19 report and retained logs are authoritative for implementation checks. Release preparation54e1c56 did not change product code; release workflow and GoReleaser artifact/chart versioning were inspected. Whole-branch review covered main base124e5b10738587c2114a533fb43088a1c377f903 through54e1c56. Its three corrections c63a46f passed new exact RED/GREEN regressions, broader affected Windows races16.255s/Linux security races2.975s, whole vet,12crossbuilds and web lint/build. Fresh browser/final rebuilt native binary reopened the same retained database/key successfully; actual Cloudflare Zone Read guidance passed. Dependency/migration evidence reused because those paths were unchanged. Independent scoped rereview approved all three fixes; exact two regressions independently PASS1.473s and correction diffcheck PASS. No new blocking findings.

## Limits and gates

Live Cloudflare/tailnet account traffic and second-client HTTPS require configured external accounts/approved joined host. Remote APIs without CAS retain documented external-writer limits. Unused OpenPGP module-only advisory remains unsuppressed. No branch publication/PR, main push, tag, release, deployment, sudoers/VPN-policy changes or destructive fixture cleanup performed. ORC agentapi backend is unavailable; supported independent review backend is available. No durable memory/wiki writes or hook parity claim.
