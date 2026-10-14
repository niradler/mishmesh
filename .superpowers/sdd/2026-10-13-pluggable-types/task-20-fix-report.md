# Task20 review fixes

Independent whole-branch review of124e5b1..54e1c56 requested three concrete changes; no other blocking findings. Root reproduced both disclosure defects before fixing them. task-20-security-red.log records API GET of retained unavailable-provider config/details and topology's credential-bearing service diagnostic. Assertions were not weakened.

## Corrections

- Without a registered descriptor, connection views hide config and runtime details, emit an unavailable/down diagnostic and conservatively redact retained config string values from the name. Identity/type remain usable. Stored config is unchanged, so a restored provider can reopen it. Tests cover both nil and empty registries, GET/list/topology, nested ordinary keys and retained rows.
- Service topology masks nonempty driver diagnostics using the same safe message as service DTOs. Tests check GET/list/topology without changing service state/identity.
- Cloudflare setup guidance now includes Zone · Zone · Read used by managed zone resolution, matching docs/cloudflare.md. Real browser verifies the displayed permission.

## Passing validation

- New disclosure regressions plus affected topology/auth/redaction tests: Windows race3.991s and Linux race2.975s; broader affected connection/topology/service race16.255s. Exact commands/logs task-20-security-green.log, task-20-linux-security-race.log, task-20-affected-race.log.
- Whole vet, profiling harness vet/build and all12server/agent OS/arch crossbuilds pass after the fixes, retained output/pluggable-access-task20-final.
- Web lint/typecheck and production build pass, task-20-web.log. Dependency lockfile unchanged; reuse passing npm audit0 and dual-OS govulncheck evidence.
- Fresh browser session and rebuilt final Windows binary reopen the retained database/key and verify config/secret/Access/Connect/Requires/UDP persistence. task-20-browser-final.log PASS. Actual Cloudflare detail guidance check task-20-browser-permissions.log PASS.
- No storage/migration changes: reuse full isolated live PostgreSQL/SQLite migration evidence, no redundant database rerun or cleanup.

Scoped independent rereview APPROVED c63a46f; all three findings addressed and no new blocking defect. Exact new regressions independently PASS1.473s, correction diffcheck PASS. task-20-rereview.md records the verdict. ORC flow dispatch stayed pending with loop off; own run halted, effective reviewer scope executed manually. No fabricated flow verdict, shared daemon/config mutation or production operation.
