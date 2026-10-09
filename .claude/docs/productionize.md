# mishmesh — productionize (started 2026-10-05)

Goal: mishmesh is the one reusable transport layer for reaching private networks (behind NAT /
firewall / proxy / no inbound). Ready to share as a working product in 3 deployment shapes, one
binary, one chart:

| Shape | Data | Conns | Replicas | Auth |
|---|---|---|---|---|
| Homelab | Postgres (bundled) | memory | 1 | API token (optionally web login) |
| Company / on-prem in customer network (multi-account) | Postgres | Redis | 1..N + HPA | password/OIDC, orgs |
| SaaS | Postgres | Redis | N + HPA | password/OIDC, orgs, quotas |

Decision (Nir, 2026-10-05): Postgres everywhere for packaged deployments (compose + Helm).
Redis only when replicas > 1. SQLite stays only as the zero-dependency dev/test fallback, not a
documented deployment shape. TODO: root docker-compose.yml gets a postgres service.

## Scaling model (decision)

Identical stateless pods behind a load balancer. Agents connect to any pod (WSS through LB).
Shared state: Postgres (durable) + Redis (live routing). Cross-pod routing via an internal
**pod-to-pod relay**:

- Owner pod registers `agent -> {node, relay addr}` and `endpoint -> agent` in Redis with TTL heartbeat.
- A request landing on any pod resolves endpoint -> agent. Local session -> direct. Otherwise the
  pod dials the owner's relay listener (pod IP, internal port, HMAC with cluster secret), sends
  `{endpoint, kind, meta}`, owner opens the yamux stream and splices. Exposed via the existing
  `store.AgentConn` seam, so ingress / TLS / TCP / reach-in / SSH / proxy work unchanged.
- TCP endpoints: port claimed cluster-wide in Redis; all pods open the listener (pub/sub + reconcile).
- Autoscale: HPA on CPU (and optionally active streams). Scale-down: SIGTERM -> drain, agents
  reconnect with backoff to another pod and re-register. No sticky sessions needed.

Env: `MISHMESH_CLUSTER_ENABLED`, `MISHMESH_NODE_ID` (default hostname), `MISHMESH_RELAY_ADDR`
(bind, default 127.0.0.1:7443), `MISHMESH_RELAY_ADVERTISE` (pod IP:port), `MISHMESH_CLUSTER_SECRET`.

## Workstreams

- [x] W1 Real user testing (black-box, homelab + company flows) -> bug list (below)
- [ ] W2 Bug fixes from W1: group A tenancy/control/UI (worktree agent), group B data plane (worktree agent), agent/CLI folded into W8 (worktree agent)
- [x] W3 Cluster mode: merged to main (relay, redis cluster store, port claims, kick, drain, readyz, backoff jitter); two-node e2e + docker 2-replica proof
- [x] W4 Helm charts committed (1bbdb76); homelab profile proven on k3d `mmhelm` (still running, delete: k3d cluster delete mmhelm); deploy/compose.cluster.yml from W3
- [ ] W5 Scale test: many agents, concurrent req/s, cross-pod; memory/goroutines per agent
- [x] W7 Competitive gap analysis (ngrok primary; CF Tunnel, Tailscale Funnel, frp, zrok, Pangolin)
- [ ] W8 Ship-blocking gaps (start after W3 merges — overlaps internal/agent, gateway, controlplane):
  1. Agent YAML config + many tunnels per agent session + `tls` subcommand (M) — cmd/mishmesh-agent, internal/agent
  2. ACME for custom domains: acmeHostPolicy allows store-known domains (S) — cmd/mishmesh-server/tls.go
  3. Custom domain ownership verification TXT/CNAME (S-M) — SaaS hostname hijack otherwise
  4. TCP/stream reach-in route (hijacked conn), current /reach/{id}/http buffers body (M) — core "internal access" use case
  5. `mishmesh-agent service install` systemd/launchd/windows (S)
  6. Per-endpoint rate limit in policy (S)
  Later: webhook verification, request inspector, endpoint pooling (fits W3 redis registry).
  Not now: tailscale/cf connectors, UDP/P2P, WireGuard, CEL policy, SAML, multi-region, k8s operator, billing.
- [ ] W6 Docs: README quickstart per shape, positioning as the reusable transport layer

## Log
- 2026-10-05: plan written. W1 (black-box tester, no repo edits), W3 (go-expert, worktree branch),
  W4 (Helm chart, deploy/helm only, uncommitted) running in parallel. Known race folded into W3:
  old pod's cleanupEphemeral can delete endpoints after the agent reconnects to a new pod.

## W1 user-testing findings (2026-10-05, HEAD a65c07a)

P0
- T1 Cross-org agent takeover/IDOR: agent handlers (controlplane/api.go ~230-330, service.go) look up by ID, never check ag.OrgID vs orgScope(r); create trusts req.OrgID; GET /agents ignores session org; /orgs/{id} any org.
- T2 Removed member keeps access: resolveSession falls back to role member (auth.go:118).
- T3 Every signup joins org_default (auth.go:251 bootstrapMembership); no org switch.
- T4 Web UI crashes after login: Me shape mismatch (web/src/api/types.ts vs server /me).
P1 data plane (group B, after W3 merge — overlaps ingress/tcp.go)
- Expect: 100-continue uploads empty (ingress.go:171); SSE/streaming not flushed (ingress.go:194);
  TCP half-close drops reply + data race on byte counters (tcp.go:147, proxyUpgrade); /tunnel/ path
  shadows app paths on subdomain hosts; no X-Forwarded-*; streams_active never decrements;
  bandwidth quota only checked pre-request; 502 messages unclear.
P1 control/UX (group A)
- Token rotate leaves old token valid -> two sessions flap; /metrics unauthenticated; PascalCase JSON
  on /orgs, PUT quota; vite base "./" breaks deep-link reload; UI agent command wrong arg order;
  unknown /api/v1/* returns SPA 200; no register page; no email validation / login rate limit; api.md drift.
P1 agent/CLI (fold into W8 agent rework)
- Agent retries forever on 401 with no clear message; backoff not reset; no scheme validation;
  doesn't print endpoint id/path URL; `token create --org` ignored; `server --help` starts server;
  ephemeral endpoints survive server restart (need startup sweep).
Docs/deploy (W6)
- README quickstart needs API auth var; deploy/README docker run exits; agent wss on non-TLS 8081;
  compose port range mismatch + unauth API on 0.0.0.0; README status stale.

## Decisions
- Signup mode `MISHMESH_SIGNUP_MODE`: `org` (default, SaaS: each signup gets own org as owner) |
  `invite` (company/on-prem: first user owns org_default, others only via member invite). Org switch
  via POST /auth/switch-org. Cross-org resources return 404.
- 2026-10-05: group A (tenancy P0 + control/UI) dispatched to worktree agent.
- 2026-10-05 (session 2): resumed agents; merged W3 (ff), root compose -> Postgres (b6f75bb, docker-proven),
  Helm committed. Running: group A tenancy, group B data plane, W8 agent CLI rework.
  Helm follow-ups: /metrics auth (group A), collapse agent chart to one Deployment w/ config file after W8,
  image registry placeholders, ACME single-pod only (cluster uses wildcard secret), NLB port limits.
  W3 follow-ups: relay is HMAC-auth not encrypted (private net only); deploy/README cluster section added.
- W8 agent rework merged (540545b): agent.yml + multi-tunnel one session, start/validate/tls, register
  failure reasons, 401/403 exit, URL validation, backoff reset, service install (kardianos/service),
  token create --org fix, --help, startup sweep of orphaned ephemeral endpoints. Open: agent Helm chart
  can collapse to one Deployment w/ config; banner reprints on reconnect; tls URL assumes :443.
  Remaining W8: ACME custom domains + domain verification, TCP/stream reach-in, rate limit.
- 2026-10-05 (session 3): group A tenancy merged ff to main (ba3b1a9): org scoping on every route,
  membership required, signup modes, rotate revokes old tokens + drops live session, /metrics token,
  web /me/register/switch-org/base path, invite tokens (secret, single-use, 7d, hashed, role cap).
  make check green. Security follow-ups: unverified password account later linked by Google login
  with same email (pre-registration takeover); login rate limiter is per-node.
  Root compose -> Postgres done (TODO above closed).
  Dispatched W8b (worktree agent): domain ownership verification + ACME for verified custom domains,
  and raw stream reach-in. Rate limit queued until group B merges (both touch ingress policy).
- 2026-10-06: group B data plane merged ff (c4b0568), make check green. ReverseProxy-based HTTP
  (100-continue, SSE flush), tunnel.Splice half-close (race fixed, also cluster relay), host beats
  /tunnel path, X-Forwarded-* + MISHMESH_TRUSTED_PROXIES, streams_active decrements, in-flight
  bandwidth quota (HTTP only; TCP/TLS not metered in flight), 502/503/504 pages via agent error frame.
  Follow-ups: reach-in must read error frame (sent to W8b); policy IP used socket peer (in rate-limit
  agent); 2m ResponseHeaderTimeout; idle half-closed stream torn down after 2m.
  Dispatched rate-limit agent: per-endpoint limit in policy, memory/Redis limiter, trusted-proxy
  client IP, cluster-wide login limiter.
- 2026-10-06: W8b merged ff (9b52491), make check green. Domains API with TXT challenge
  `_mishmesh-challenge.<domain>` (CNAME alone not accepted), verified-only binding, first verifier
  wins (409), MISHMESH_DOMAIN_VERIFICATION (default on in signup mode org). ACME allows verified
  domains (30s cache; single-pod). Raw stream reach-in GET /api/v1/reach/{agent}/stream
  (Upgrade: mishmesh-stream). HTTP reach-in reads agent error frame -> 502.
  Follow-ups: HTTP reach-in still buffered (8MB); no periodic re-verification; postgres tests use
  fixed IDs (fail on reused DB).
- 2026-10-06: rate limit merged ff (31161e9), make check green. internal/ratelimit token bucket
  (memory; Redis Lua w/ server TIME in cluster, fail-open), internal/clientip trusted-proxy resolver
  used by policy IP allow/deny + rate limit, policy.rate_limit {requests, period_seconds, burst,
  scope ip|endpoint} -> 429 Retry-After, login limiter now cluster-wide. Open: TCP/TLS not rate
  limited; 429s not in status metrics.
- b39eb3a: Google login refuses to link to an existing password account (409) — closes the
  pre-registration takeover. 0337af2: web policy form keeps fields it doesn't edit (mtls,
  proxy_target, rate_limit were being dropped).
- W8 done except: HTTP reach-in streaming (8MB buffered). Next: W5 results, then W6 docs.
- W6 docs agent dispatched (README per shape, deploy/README fixes, api.md coherence; Sizing TBD).
- W5 results (docker, 4 vCPU/8GB server): ~65-98KB RSS per idle agent, 20k agents ~2GB / 1.1 core
  idle; 1KB ~1.2k req/s with unbounded PG pool vs 9-11k at pool 32 (CPU-bound ~400%); 1MB
  ~1.5-1.8 GB/s. Cluster: local 2.6k, relay 1.8k req/s at c50 (+7ms p50); relay 100% 502 at c200.
  Kill node: agents back in 1-2s, ~2-3s error window. Bugs: unbounded PG pool; DB errors surfaced
  as 401/404; relay dials per stream (ephemeral port exhaustion); migration race on concurrent
  start; stale ephemeral subdomain dup-key on re-register; late 404 window after restart (cause
  open). Hotspots: syscalls 50%, per-request endpoint/quota DB lookups, yamux readData growSlice,
  io.Copy 32KB buffers. Tooling: .claude/scripts/loadtest (README). Fix agent dispatched (W5-fix),
  re-measures after.
- W6 docs merged ff (6efc200): README rewrite (shapes, quickstart docker-proven, scaling, security,
  Sizing TBD), deploy/README fixed, api.md coherence, compose ports 127.0.0.1, helm signup modes.
  Docs review found code issues -> dispatched fix agent. Decision: quota writes are platform-operator
  only (admin bearer token) in every mode; operator bypasses org authz policy. Also: reach-in-only
  agent (zero tunnels), homelab Helm UI usable (password login + invite mode), first-class chart
  values, SSH host key default under data dir, agent.yml basic_auth_password. Control listener TLS
  stays proxy-only (documented).

## Beta release readiness (2026-10-06, session 4)

Merged: docs-review fixes ff (24cbd96: operator-only quota writes, reach-in-only agent, homelab Helm UI,
ssh host key default, agent basic_auth_password); W5-fix rebased + ff (bounded PG pool
MISHMESH_DATA_MAX_CONNS default 25, PG errors -> 503, migration advisory lock, relay multiplexed per
peer, idempotent re-register, lookup cache, splice buffer pool). make check green after each.
bf4ca10 deps: go 1.26.6, x/crypto 0.57.0, x/text 0.42.0, react-router 7.18.4 (govulncheck: 11
reachable stdlib/x/crypto ssh vulns; npm audit now 0).

W5 re-measure after fixes (real server, 4 vCPU/8GB, 200 agents, default pool):

| | before | after c50 / c200 / c1000 |
|---|---|---|
| single 1KB req/s | 9-11k (pool 32), 1.2k default | 18.6k / 20.2k / 19.2k |
| single 1MB MB/s | 1.5-1.8k | 2368 / 2389 / 1043 |
| cluster-local 1KB | 2.6k | 16.9k / 17.4k / 16.1k |
| cluster-relay 1KB | 1.8k c50, 100% 502 c200 | 9.6k / 11.8k / 11.2k (+2.5ms p50) |

Zero errors in throughput runs; ~68KB RSS/agent (5000 agents 354MB); concurrent fresh-DB start
clean 4/4; kill node -> agents back +3.3s, ~2s error window; no late-404 or dup-key repro.
New: cluster usage metering does sync Redis GET+INCRBY per request -> 280k timeout WARNs at load.

Pre-beta security review (verified): P0 endpoint OIDC bypass (state token accepted as session);
P1 CSRF from tenant subdomains, admin can demote/remove owner, SSRF via agentless proxy_target,
ACME issuance flood (any *.apex SNI), path routing shares one origin (mm_oidc forwarded upstream),
no http.Server timeouts (slowloris), vulnerable deps (fixed bf4ca10); P2 unlimited orgs per user,
basic-auth bcrypt DoS, login lockout + user enumeration, per-node authz cache, unvalidated/reserved
API subdomains, Google OIDC nonce/state cookie.
Dispatched (worktree agents):
- [x] SEC-A data plane merged ff (675d754), make check green: OIDC token domain separation + empty
      email reject, ACME host policy (apex + existing single-label + custom), MISHMESH_PATH_ROUTING
      (off in org mode) + strip mm_oidc/Authorization upstream, ReadHeaderTimeout/IdleTimeout,
      basic-auth success cache, async batched Redis usage (1s flush, MISHMESH_REDIS_POOL_SIZE).
      Offline agent already 503. Follow-ups: register.go / controlplane endpoints.go / sshfwd still
      hand out /tunnel/{id} URLs when path routing is off (fix after SEC-B); ephemeral endpoints
      deleted on disconnect -> 404 in reconnect window (by design for now); load not re-run.
- [ ] SEC-B control plane: CSRF (JSON + Origin), role hierarchy/last owner, SSRF deny private +
      opt-in, orgs-per-user cap, login limiter IP+email + dummy hash, authz cache invalidation,
      subdomain validation + reserved list, OIDC nonce.

Release (decided: images/charts at ghcr.io/niradler; LICENSE proprietary beta terms):
- [x] 6680ec6: CI (.github/workflows/ci.yml: gofmt, vet, race tests, govulncheck, web build + npm
      audit, helm lint/template, docker builds), release.yml on tag v* (goreleaser binaries + web UI
      bundle, multi-arch ghcr images with VERSION, charts to oci://ghcr.io/niradler/charts),
      .goreleaser.yaml (validated with goreleaser check), Helm -> ghcr.io/niradler, LICENSE
      (proprietary beta terms; have counsel review), CHANGELOG, README Install / Sizing /
      Operations (backup, upgrade) / Beta limitations.
- [x] Protocol validation matrix (agent, harness in .claude/scripts/protocols): HTTP 1GB up/down,
      SSE, long-poll >2m, WebSocket, h2/gRPC, SSH/scp over TCP, psql, TLS passthrough, ssh -R,
      reach-in stream as SSH ProxyCommand, cluster relay, agent kill mid-transfer, 200 parallel.
- [x] CI runs verified and all four ghcr packages public for beta customers
      (or customers given read tokens) after the first release.
- [x] a3704a3: actions pinned to commit SHAs, goreleaser v2.18.2, govulncheck v1.8.0, release
      permissions per job, token via env, dependabot (actions, gomod, npm). actionlint clean.
- [x] Tag v0.1.0-beta.1 + push (completed)

### Wind-down state (2026-10-06 end of session 4)

- main is local only (nothing pushed). HEAD f83ac38, make check green.
- [x] SEC-B merged (dc4222c..f83ac38), all 8 findings fixed, each with a regression test:
  - CSRF: JSON content type + same-origin on cookie writes.
  - Role hierarchy + last-owner guard + owner-only policy writes.
  - SSRF guard with IP pinned at dial time.
  - MISHMESH_MAX_ORGS_PER_USER (default 3).
  - Login limiter keyed on IP+email, plus a dummy bcrypt for unknown users.
  - Authz cache TTL 5s.
  - internal/subdomain validation + reserved names.
  - Google OIDC nonce/aud/sub check + single-use state cookies.
  - New env vars: MISHMESH_ALLOWED_ORIGINS, MISHMESH_PROXY_ALLOW_PRIVATE, MISHMESH_PROXY_ALLOWED_CIDRS,
    MISHMESH_MAX_ORGS_PER_USER.
- SEC-B follow-ups:
  - Add the new proxy/origin env vars to the Helm values/templates.
  - The vite dev server needs MISHMESH_ALLOWED_ORIGINS.
  - Subdomain "app" is now reserved (could break existing users).
  - No per-email limit across IPs (accepted).
  - ID token signature not verified (OK per spec for a token obtained directly from the token endpoint).
- Protocol matrix: harness built (.claude/scripts/protocols: kit/ Go tool in its own module,
  compose.yml, agent.yml, README) but ZERO rows run. linux/amd64 binaries were built into the
  session scratchpad (may be gone; rebuild per README). Next: compose -p proto up, run rows 1-9,
  start long-running ones (5 min SSE, 3 min long-poll, 10 min WS/SSH idle) in parallel first.
- Suspected from code reading, not confirmed:
  - P1: ingress ResponseHeaderTimeout is fixed at 2 min (internal/ingress/proxy.go:23,:99) and
    UpstreamResponseTimeout isn't wired in cmd/mishmesh-server, so long-poll over 2 min gets a 504.
    Fix: expose an env setting, or document it as a beta limitation.
  - P1: gRPC over the HTTP ingress is likely broken, because the ReverseProxy goes to the backend
    over HTTP/1.1 only. Should work via TCP ports and TLS passthrough. Confirm, then either support h2c
    upstream or document it.
  - P2: half-closed raw TCP is force-closed after 2 min idle (internal/tunnel/splice.go:13).
- After SEC-B: fix /tunnel/{id} URLs handed out when MISHMESH_PATH_ROUTING is off
  (gateway/register.go, controlplane/endpoints.go, connect/sshfwd).
- Then: push main, watch first CI run, tag v0.1.0-beta.1, make ghcr packages visible.
- Stale worktrees for already-merged branches can be removed (Nir): a23335d, a39030b, a4950709,
  a5d8cad, ac6e960, ace8635, ad1b765, a1c2a44, a92667e, aa7a01c.

Known limitations to document for beta: relay plaintext after HMAC hello (private net only); custom
domains not re-verified; TCP/TLS not rate limited or metered in flight; HTTP reach-in buffers 8MB;
ACME single-pod (cluster uses wildcard secret); unverified password signup can squat an email
(Google login returns 409); endpoint OIDC client secrets stored plaintext; Go module path
github.com/mishmesh/mishmesh does not match repo (use release binaries, not go install).

### Resumed release validation (2026-10-09)

- Confirmed local main at f83ac38, 66 commits ahead of origin/main. Protocol harness and this tracker remain untracked; preserve unrelated worktrees.
- Read protocol README and repo/user reference instructions. No project skills installed; find-skills search completed, but candidate source quality could not be verified, so no installation recommended yet.
- In progress: Linux binaries into the Windows temp directory `mishmesh-proto-20261009/bin`, isolated compose project `proto`. First checks: >2-minute long-poll and gRPC through HTTP ingress, with direct/TCP controls.
- Remaining: complete protocol matrix; consult on confirmed protocol fixes versus documented beta limitations; Helm security settings; path-routing URL behavior; full checks; commit/push/CI/tag/package visibility.
- Confirmed failures on f83ac38: HTTPS-ingress gRPC returns 505 (`gRPC requires HTTP/2`), while direct h2c and TCP h2c/TLS unary + 100-message bidi controls pass. Ingress sends HTTP/1.1 upstream. Three-minute long-poll returns 504 at 119.998s; direct control returns 200 at 179.994s. Async direction questions pending for both.
- Passed: fixed/chunked 1 GiB uploads with SHA256; 1 GiB download with SHA256; PostgreSQL SELECT 42; Redis PING; raw TCP half-close 1 MiB reply hash; HTTPS upstream; HTTP/2 TLS passthrough; SSH command; 16 MiB SCP round-trip; Expect:100-continue 16 MiB upload; SSH ProxyCommand through reach-in; 200 parallel 10 MiB downloads (0 failures).
- Implemented path-routing flag propagation to gateway bindings, API URLs, SSH forward announcements. Focused package tests and server compilation passed. Helm values now expose pathRouting, proxyAllowPrivate, proxyAllowedCIDRs, allowedOrigins, maxOrgsPerUser; all three profile lints pass (icon recommendation only). Render validation and full checks remain.
- Running: 5-minute SSE, 10-minute WebSocket idle, 10-minute SSH idle. Cluster relay and kill-mid-transfer remain after these finish.
- SSE passed: 300 events in 4m59s, worst gap 1.011s. Clientless ssh -R and gRPC TLS passthrough passed. `make check` passed. Helm render validator passed all three defaults and explicit overrides.
- New release blocker: fresh govulncheck reports 10 reachable Go 1.26.6 standard-library vulnerabilities (fixed in 1.26.9), plus x/net issues fixed in v0.60.0. Full scan saved under protocol scripts. Updating minimum patch and build-image pins, then revalidating; web checks did not run because the initial scan failed.
- Security patch complete: Go minimum and Docker builders 1.26.9, x/net v0.60.0, harness dependency alignment. Main and harness govulncheck pass with zero reachable vulnerabilities. Main has a module-only OpenPGP advisory (GO-2026-5932) with no patched version; package is not imported. Patched `make check` passes. Web build and production audit pass (0 vulnerabilities); existing Vite 511 KB chunk warning remains.
- WebSocket passed after 10m idle (4 x 16 MiB verified messages). SSH idle still running.
- Separate `proto-cluster` stack uses subnet 172.31.78.0/24, API loopback port 38182, patched binaries, and path routing off. All nine endpoint API URLs and agent banners exclude path URLs.
- Cluster startup exposed a real bootstrap race: server-b exited on duplicate ag_bootstrap primary key while server-a started. Fixed EnsureBootstrap to reread and validate the winning agent/token after concurrent insert conflict; deterministic concurrent agent and token regression passes 10 times under race detector. Fresh PostgreSQL simultaneous-start runtime proof and relay matrix pending.
- Final independent validation: full make check passed after bootstrap fix. Fresh simultaneous PostgreSQL startup produced exactly one bootstrap agent and token. Relay matrix passed against server-b with agent on server-a: 200 parallel downloads, 1 GiB chunked upload, gRPC h2c/TLS, half-close, PostgreSQL/Redis, TLS passthrough, WS, SSE, SSH and stream reach-in. Fresh fixture uses Redis DB 1 to match the new PostgreSQL database; stale Redis DB 0 state was preserved.
- SSH 10-minute idle passed. Agent kill during a throttled 1 GiB transfer caused explicit curl exit 18; restart recovered, followed by 20/20 hash-verified downloads. Both compose stacks retained; no cleanup run.
- New Helm render verifier added to CI. Detailed evidence in `.claude/scripts/protocols/results-2026-10-09.md`. Non-blocking follow-ups: existing Vite 511 KB chunk warning; TLS passthrough banner incorrectly uses public HTTP scheme (connection works on the TLS listener).
- Awaiting Nir's direction on confirmed gRPC HTTP-ingress failure and the fixed 2-minute long-poll timeout. Recommendations remain: document gRPC TCP/TLS alternatives for beta; expose configurable upstream response timeout while retaining the 2m default. Do not push/tag until those choices are implemented and validated.
- Local commit e9f33ae contains validated fixes, Helm settings/render CI check, dependency patches, bootstrap regression, and protocol harness/results. Main is 67 commits ahead; nothing pushed or tagged. Tracker, loadtest scripts, full pre-patch scan and unrelated worktrees remain local/untracked. Next action depends on the two pending direction choices; stacks remain available for retesting.
- Nir directed continued work until proven ready for beta customers. Pending choices superseded: implement gRPC HTTP ingress and configurable upstream timeout, then validate and publish. Implementation in progress: standard HTTP/2 transport selected for application/grpc media types; per-stream metadata tells agent to negotiate h2 on TLS targets; ordinary HTTP remains HTTP/1.1. Endpoint isolation/trailers regression added. Ingress listeners accept h2c; control listener retains its existing protocol behavior. Timeout setting defaults to 5m and rejects invalid/nonpositive values at startup; Helm maps the setting.
- New restart blocker confirmed: reserved TCP endpoints retained Redis claims, but re-registration allocated new endpoint IDs and the agent exited on occupied ports. Fixed by rebinding a requested port only among the authenticated agent's existing endpoints. Regression verifies stable endpoint ID, one stored endpoint, correct URL, and another agent cannot reuse it. Cluster restart now restores all nine tunnels.
- Runtime gRPC now passes unary + 100 x 64 KiB bidi messages through local HTTPS ingress, relay HTTPS ingress, HTTPS backend ALPN h2, and plaintext h2c ingress. Three-minute relay long-poll running with 5m default. Final suite/security/publish checks remain.
- Three-minute long-poll passed through relay with 5m default: 200 after 180.892s.
- Nir reported Windows firewall approval during tests. Root cause: local e2e tests bind the host LAN IP for reach-in and 127.0.0.2 for the second cluster node. Windows make test/check now runs full Go tests in an isolated Go Linux container with read-only source mount and no published ports; no tests skipped. Host runs fmt/vet/build only. make test-short uses the same isolation. Container retained rather than invoking destructive cleanup.
- Full suite found one stale TLS URL expectation and an intermittent agent failure classification race. Updated TLS expectation to new correct URL; error classification waits briefly for the initial response/error-frame read to finish after concurrent transport write failure. Container full suite running.
- Windows-isolated full make check passed. Agent failure classification regression passed 100 race-enabled repetitions in Docker. Confirmed no published test-container ports and no Windows Go test listeners. Fresh Linux govulncheck found zero reachable vulnerabilities (one unused module advisory remains).
- Final candidate binaries in temporary final-proven-bin passed relay gRPC against both h2c and HTTPS backends, 200 checksum-verified 10 MiB downloads, chunked 1 GiB upload, half-close hash, four 16 MiB WS echoes, SSE, and HTTP/2 TLS passthrough. Agent banner now advertises tls://pt.mm.test:8444. Helm security render validation and agent chart lint pass.
- Published main at 116cc5b. First GitHub CI run 37932240375: Helm, web and Docker jobs passed; Go race/security job still running. Do not tag until all CI jobs pass. Windows firewall bind correction committed in 116cc5b.
- CI 37932240375 passed all four jobs. Final binary three-minute relay long-poll passed 200 after 183.740s. Tagged and pushed v0.1.0-beta.1 at 116cc5b; release workflow and customer artifact/install verification are next.
- Repository follow-ups surfaced: main branch protection is not configured, Dependabot opened routine upgrades, and Vite retains the existing ~511 KB bundle warning. These are separate from the passing release code/security checks.
- Binary release succeeded, all 12 binary archive hashes verified, both Windows binaries report v0.1.0-beta.1 without listeners. Agent multiarch image public and anonymous pull succeeded. Found web UI bundle absent from checksums.txt; published additive webui-checksums.txt for this beta and added checksum.extra_files to GoReleaser config for future tags.
- Release warnings surfaced: pinned GitHub Actions declare deprecated Node 20 and are being forced to Node 24; ubuntu-latest migration announced for Oct 19. Jobs currently pass. Review Dependabot action upgrades and pin runner baseline before next maintenance release. Docker restart --time is deprecated; use --timeout for subsequent checks.
- Downloaded published Linux archives replaced both cluster servers and the agent. Actual release binaries pass relay unary/bidi gRPC to h2c/HTTPS targets, 200/200 x 10 MiB checksummed downloads, chunked 1 GiB upload hash, and 4 x 16 MiB WS echoes. Published binary long-poll still running. Server multiarch release image build remains in progress.
- Release server image stalled >25m in ARM64 npm ci under QEMU; live logs confirm ARM64 Go build completed in 430.8s while web dependency install remained active. Fixed Docker server web and Go builders to native BUILDPLATFORM, cross-compiling CGO-free Go via TARGETOS/TARGETARCH. Both local architectures build and report beta version; native npm ci takes 4s. Added dispatch image/chart recovery mode with strict tag validation and application-source diff guard. actionlint v1.7.12 passes. Direction question sent; no answer after >60s, proceeding with recommended recovery under existing publish authorization.
- Local Docker context also included Windows web/node_modules and built web/dist, allowing COPY web to overwrite freshly installed Linux dependencies. Added both to .dockerignore; rebuilt both architectures successfully with clean inputs and identical final image digests. Recovery workflow and GoReleaser config lint/check pass.
- Build/release recovery fixes committed and pushed at 8d3ff37. Original stalled release run 37932710283 cancelled; existing published binaries and agent package preserved. New CI 37936071232 running; then dispatch release.yml with release_tag=v0.1.0-beta.1. Tag remains 116cc5b, application source diff guard passes.
- Recovery commit CI 37936071232 passed all jobs. Dispatch image/chart publication run 37936372881 from 8d3ff37 with release_tag=v0.1.0-beta.1 is running. Original run is fully cancelled. Native builder fix preserves tagged application source; published archives remain unchanged.

### Beta release complete (2026-10-09)

- v0.1.0-beta.1 is published at https://github.com/niradler/mishmesh/releases/tag/v0.1.0-beta.1; immutable tag points to 116cc5b. Build fixes on main at 8d3ff37 preserve tagged application source.
- Full protocol matrix completed, including fixed HTTP-ingress gRPC and >2m long-poll, real cluster relay, ten-minute idle, checksummed large transfers, 200 parallel downloads, and interruption/recovery.
- Windows tests run inside Docker with no published ports. No Windows Go test listeners remain; no firewall exception needed for Go tests.
- All four GHCR packages are public. Published binary hashes, anonymous downloads/pulls, multiarch manifests and image version commands verified.
- CI 37936071232 and publication recovery 37936372881 passed. Original stalled publication was cancelled after identifying ARM64 web installation under QEMU; native build stages fix it. No tag/archive replacement occurred.
- Published OCI server and agent charts installed anonymously into fresh mm-beta-proof namespace in k3d-mmhelm. No pull credentials; Postgres PVC bound; server/agent UID 65532; all pods ready with zero restarts. Actual images passed gRPC, 1 GiB upload hash, 200 downloads, WS and authenticated API/UI-asset checks.
- First owner registration and existing session pass; subsequent registration requires invite; unauthenticated API and cross-origin writes are rejected. check-published-helm.ps1 saves repeatable smoke checks, guarded to the isolated test cluster.
- Published Helm rolling upgrade preserved generated API token and existing owner session, restored tunnel traffic and passed gRPC plus 20/20 verified downloads afterward.
- Detailed evidence: .claude/scripts/protocols/results-2026-10-09.md. Test certificates are self-signed inside isolated fixtures; customer deployments need trusted TLS. ARM64 runtime version checks use emulation, not native hardware.
- Remaining documented beta limits: private-network/mesh-protected plaintext relay, single-pod ACME, 8 MB HTTP reach-in cap, no TCP/TLS rate/bandwidth metering, periodic domain reverification absent, email squatting behavior, plaintext endpoint OIDC secrets in protected Postgres. No claim of GA readiness or unlimited production coverage.
- Repository maintenance follow-ups: configure main branch protection; review Dependabot updates and Node 20 action deprecation/Ubuntu runner migration; existing Vite ~511 KB bundle warning. Test stacks, namespaces and unrelated local worktrees retained; no cleanup requested.

- Final evidence/helper commit 6e64d98 pushed to main. Published beta tag remains 116cc5b. Final proof-only CI pending; release artifacts, public visibility, customer installation and upgrade checks are complete.
- Final CI 37938192793 passed all four jobs for main 6e64d98. Release notes updated with published installation and rolling-upgrade evidence. Release work complete; only documented beta limitations and repository maintenance follow-ups remain. Tracked worktree clean, origin/main current, test fixtures and unrelated untracked work preserved.

### Customer readiness follow-through (2026-10-09)

- Nir requested continued work after challenging the broad readiness claim. Customer readiness remains conditional on the actual deployment; no 100% or GA claim is supported.
- [x] Database recovery: custom-format pg_dump from the published Helm Postgres restored into a separate fresh Postgres 16 instance. All 12 durable tables match source row counts and sorted row-content hashes. Published server starts on the restored DB; existing owner login and stored endpoint access pass. Original DB untouched.
- [x] Restored data plane: published agent reconnects with the backed-up bootstrap identity; 20/20 checksum-verified downloads and gRPC unary/100-message bidi pass. Initial 502 was fixture wiring: restored agent lacked the private backend network; attaching it resolves the confirmed target-dial failure.
- [ ] Thirty-minute sustained published-image run: 20 concurrent 1 MiB hash-verified downloads per batch, repeated gRPC and WS, resource/readiness/restart samples. Running; results in temporary mishmesh-beta-published-proof/soak-30m.jsonl. Not a multi-day soak.
- [ ] Git hygiene: commit this source-of-truth tracker and reusable load harness. Old harness constructor missing Redis pool-size parameter; corrected after isolated compile failure. Keep old scan/worktree metadata local without deleting anything.
- [ ] Customer public deployment: requested hosting target/hostname for trusted TLS/public DNS and live Google/OIDC. Cannot validate customer-owned credentials or infrastructure without deployment details. Optional provider features require their own live validation before being enabled.
- Next: complete sustained run, record evidence and deployment gate, run required checks, push evidence/harness changes and verify CI. Beta tag/artifacts stay immutable.
- New release blocker confirmed by fault injection: paused Postgres keeps its TCP connections open; authenticated endpoint API waits until curl gives up (12.18s, exit 28, no response). Root cause: store operations have no deadline. Implementing per-operation context deadline, configurable MISHMESH_DATA_QUERY_TIMEOUT/Helm dataPool.queryTimeout (5s default, strict positive validation). Real Postgres lock regression proves read/list/write cancellation, shorter caller deadline preservation, and recovery. A new beta build is required; beta.1 remains immutable.
