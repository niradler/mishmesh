# Tasks15–17: descriptor UI, Connections, Access and topology

Implemented generic eight-kind descriptor fields, type picker, configuration display, service config/ready/requires forms, reverse Requires links and explicit Save and restart. List argv preserve whitespace. Sensitive values stay masked; connection secrets are omitted until replacement/explicit clear. Host field/driver choices follow capabilities. Requires409 offers a separately confirmed ordered dependent stop with accurate Stop labels.

Connections routes/list/detail/forms/detection/sync/delete guards replace the singleton page/API. Provider panels are registered separately. Access supports compatible same-org Public/Private refs and retains unsupported existing refs visibly. Saves preserve Connect and other endpoint policy. Endpoint create no longer submits the server-owned lifecycle field that the strict API rejected. Old singleton DTOs/helper payloads and topology fallback wire variants retired.

Topology backend was committed with Task3 because it removed a live legacy consumer. Client renders connection refs, connection-qualified hosts, all services and same-column Requires curves; expanded viewport/ESC and bounded tooltips work. Dense synthetic topology checks pass at1920×1080,1024×768,390×844 and844×390.

Validation:

- `npm run lint` and productionbuild PASS; final logtask-15-17-web-build.log. Prettier3.9.10 formatted touched web files.
- Synthetic fourth driver exercises all8field kinds, runtime connection filtering, whitespace, Requires,-1 and masked secret/ref preservation: check-forms.cjs PASS. Synthetic Detect tests type ABA, edits during pending detect and accepted current results: check-detect.cjs PASS.
- Generic backend topology races PASS1.442s; two same-type connections/runtime/down/unknown, foreign status filtering and Requires covered.
- Real nativeWindows Command/SQLite browserflow: typed create/start, normal Save409, single explicit restart, saved-start502 phase, reopen and masked env preservation PASS. Actual marker/version files prove executed saved argv.
- Real Cloudflare/Tailscale imported config saves, secret retain/clear, two Access refs, Connect preservation, referenced connection409 and generic topology PASS. Providers deliberately point at absent local fixtures; no live Cloudflare/tailnet claim.
- Real owner/admin/member and two-org browser/API checks PASS: transitive host403, foreign404/DAG rejection, masked sensitive reads, member controls hidden, admin Command picker filtered, Requires409 and explicit dependent stop.
- Real Docker/Compose descriptor config forms and HTTP readiness/-1 reopen PASS. HTTP endpoint create and reserved UDP port39960 PASS. No live Docker/Compose workload lifecycle claimed by those form checks; existing driver integration tests cover Engine behavior.
- Closed real browser; native fixture terminated at its bounded30minute deadline. Reopened finalWindows binary against same SQLite/key and a new browser/login: stable service/connection/endpoint/UDP identities, decrypted secret presence, explicit-clear absence and Requires graph/form values PASS. Logoutput/browser-real/check-real-reopen.log.
- Real logs and synthetic screenshots retained under ignoredoutput/browser-real andoutput/playwright. Expected401/403/409/502 HTTP responses occur in denial/failure scenarios; tested pages had no JavaScript exceptions.

Tailwind3 buildchain had an unpatched braces advisory. Compatible updates left7advisories, so PostCSS integration migrated to Tailwind4.3.3, preserving JS theme config. Finalnpm audit reports0vulnerabilities, no suppression/forced audit fix. Browser floor Safari16.4/Chrome111/Firefox128 is documented. Real and synthetic browser flows and production builds pass after migration.

Final whole-branch review remains Task20. Nothing published or deployed.
