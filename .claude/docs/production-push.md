# mishmesh — production push (2026-06-24)

Goal: production-ready deploy; validate all endpoints; prove cross-Docker-network tunneling;
real user flows; API-key usage; complete connectivity capabilities (priority core feature) from
`docs/prd-multi-method-connectivity.md`. Release to first users 2026-06-25.

## Phase 1 — validation evidence (DONE, all from real Docker stacks)

Baseline: `go build ./...` clean; `go test -race ./...` green (10 pkgs).

Cross-network proof (`deploy/compose.e2e.yml`, isolated `edge` + internal `private`):
- HTTP subdomain tunnel: tester→server→tunnel→echo (on private net) → 200, body served by echo.
- TCP tunnel (server:10000): → echo. 
- Isolation: tester→echo:8080 directly = timeout (curl rc=28). Backend reachable ONLY via tunnel.
- API-key lifecycle: mint via `POST /api/v1/agents`; agent connects; `POST /agents/{id}/revoke`
  live-kills the session (connected 2→1, endpoint online 2→1, public URL → 502). Bandwidth metered
  (`usage_bytes` increments).

Performance / protocol proof (`deploy/compose.perf.yml`, identical client on both nets):
- **git clone linux (280MB pack)**: direct ~7.94s vs **tunnel ~7.97s → 0.4% overhead**. Server
  pack-objects CPU is the ceiling (~35MB/s), hit equally both paths ⇒ tunnel transparent.
- **scp 500MB single stream**: direct ~400MB/s vs tunnel ~245MB/s (~2Gbps); **sha256 identical**
  (byte-perfect). Far above any real link; tunnel not the bottleneck.
- **ssh interactive + exec**: works direct and via tunnel (ed25519 key auth).
- Protocols proven over tunnel: HTTP, WebSocket (regression test), TCP, git-native (git://), SSH, SCP.

TLS passthrough (`kind=tls`): covered by `internal/ingress/tls_test.go` + e2e; enable via
`MISHMESH_TLS_PASSTHROUGH_ENABLED`. Not separately docker-proven this pass.

## Phase 2 — connectivity core feature (multi-method PRD §5)

Architecture seam confirmed: ingress routes purely via `store.AgentConn.OpenStream` +
`ConnectionStore.{AddAgent,BindEndpoint,ResolveEndpoint}`. New methods that implement `AgentConn`
reuse all routing/policy/TLS/quota/metering unchanged.

Build order (each TDD, no comments, commit per feature, `make check` green):
1. **Seams**: `Endpoint.Method` (native|ssh|proxy|tailscale|cloudflare) + store migrations + DTO;
   `Connector` interface (Provision/Reconcile/Teardown/Health); audit hooks. [foundation]
2. **Clientless SSH remote-forward** (`internal/connect/ssh`): x/crypto/ssh server; token-as-password
   auth → agent/org; `tcpip-forward` → Endpoint(method=ssh); SSH session implements `AgentConn`
   (`forwarded-tcpip` channel = OpenStream). HTTP (subdomain) first-class; TCP via PortOpener.
   Gated `MISHMESH_SSH_ENABLED` + `MISHMESH_SSH_ADDR` + host key. **THE core "no install" feature.**
3. **mTLS edge** (networking common, Nir): per-endpoint client-cert verification on HTTPS ingress.
4. **Agentless proxy** (`method=proxy`): mishmesh reverse-proxies an already-reachable target;
   managed DNS/TLS/policy, no agent. Connector implements `AgentConn` by dialing the target.

Deferred (need live provider creds — flag for Nir): Managed Tailscale (#4), Managed Cloudflare (#5).
Self-signed certs already shipped (`MISHMESH_SELF_SIGNED_TLS`).

## Status
- [x] Phase 1 validation
- [ ] Seams
- [ ] SSH remote-forward
- [ ] mTLS edge
- [ ] Agentless proxy
