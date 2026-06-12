# PRD — Open Source Agent Tunnel ("Tunnelmesh")

**Status:** Draft v1.0
**Owner:** Platform / Infrastructure
**License:** Apache 2.0 (proposed)
**Language:** Go

---

## 1. Overview

### 1.1 Problem Statement

Every SaaS company that serves enterprise customers eventually rebuilds the same component: a reverse-connection tunnel that lets the vendor reach services inside a customer's private network without requiring inbound firewall rules, VPNs, or public exposure of customer services.

The pattern is always the same:

1. The customer installs a lightweight **agent** inside their private network.
2. The agent dials **out** to the vendor's **gateway** over a single WebSocket connection (outbound 443, firewall-friendly).
3. The tunnel is multiplexed and bidirectional, so the vendor can initiate requests _into_ the customer network (vendor → agent → private service) and the agent can push data _out_ (agent → gateway → vendor services).

This component is rebuilt from scratch at every company, with inconsistent quality, security posture, and operability. This project delivers a **reusable, production-grade, open source agent tunnel** that any company can adopt, embed, and operate.

### 1.2 Goals

- One reusable, batteries-included tunnel solution: **Agent** + **Gateway**, both in Go.
- Transparent, protocol-agnostic proxying (HTTP/1.1, HTTP/2, gRPC, SSH, raw TCP, WebSocket, database wire protocols, git, etc.).
- Bidirectional communication over a single outbound WebSocket connection.
- Support for very large / long-lived transfers (e.g., `git clone` of multi-GB repos, DB dumps, log streaming) without buffering the full payload in memory.
- Enterprise-grade security by default (mTLS, agent-side allowlists, least privilege).
- Horizontally scalable, multi-pod gateway; simple single-process agent.
- First-class Kubernetes story: official Docker images + Helm charts.
- Embeddable as a Go library and consumable as standalone binaries, so adopting companies can integrate with minimal effort.

### 1.3 Non-Goals (v1)

- Mesh / peer-to-peer agent-to-agent connectivity.
- UDP tunneling (deferred; design must not preclude it).
- Built-in billing, tenancy management UI, or SaaS control plane (we expose hooks/APIs; companies build their own control plane).
- Replacing full VPN solutions (WireGuard, Tailscale). This is an application-level reverse tunnel, not a network-layer VPN.

---

## 2. Personas & Use Cases

| Persona                           | Description                                               | Primary Needs                                                                                             |
| --------------------------------- | --------------------------------------------------------- | --------------------------------------------------------------------------------------------------------- |
| **Vendor platform engineer**      | Operates the gateway as part of a SaaS platform           | Easy Helm deployment, multi-pod scaling, observability, agent liveness APIs                               |
| **Vendor product developer**      | Builds features that need to reach customer-side services | Simple SDK/API: "send this HTTP/TCP request to agent X, target host Y"                                    |
| **Customer infra/security admin** | Installs and approves the agent in the private network    | Single binary/container, outbound-only, explicit allowlist control, auditability, proxy/TLS compatibility |
| **OSS adopter / integrator**      | A company embedding the project into their product        | Stable Go library APIs, extension points (auth, routing), consistent configuration                        |

### Core use cases

1. **Reverse access to private HTTP/gRPC APIs** — vendor backend calls `https://internal-api.customer.local` through the tunnel.
2. **Git operations at scale** — vendor clones/fetches large repositories (10 GB+) hosted on customer-internal Git servers (HTTPS and SSH transports), with stable throughput and no payload size limits.
3. **Database connectivity** — raw TCP proxying to Postgres/MySQL/Mongo inside the customer network for ETL/scanning products.
4. **Agent-initiated streams** — agent pushes events, logs, or job results up to the vendor without polling.
5. **Restricted environments** — agent connects out through corporate HTTP(S) proxies, TLS-intercepting middleboxes, and private CA chains.

---

## 3. Architecture

```
 Customer private network                    Vendor cloud (Kubernetes)
┌──────────────────────────┐                ┌─────────────────────────────────┐
│  internal services       │                │   Gateway (N pods, stateless*)  │
│  ┌────────┐  ┌────────┐  │   outbound     │  ┌─────────┐   ┌─────────┐      │
│  │ git    │  │ postgres│ │   wss://443    │  │ gw-pod-1│   │ gw-pod-2│ ...  │
│  └───▲────┘  └───▲────┘  │  ───────────►  │  └────▲────┘   └────▲────┘      │
│      │           │       │                │       │  routing /  │           │
│   ┌──┴───────────┴──┐    │                │       │  discovery  │           │
│   │     AGENT       │────┼────────────────┼───────┘  (Redis/    │           │
│   │ (single process)│    │  one WS conn   │           coord svc)│           │
│   └─────────────────┘    │  multiplexed   │       ┌─────────────┴────────┐  │
│   allowlist enforced     │  bidirectional │       │ Vendor services /SDK │  │
└──────────────────────────┘                └───────┴──────────────────────┘
```

### 3.1 Components

**Agent (customer side)**

- Single static Go binary / single container; one process per network segment (no HA pairs in v1 — restart-based recovery; design keeps room for active-passive later).
- Dials out to the gateway over `wss://` (WebSocket over TLS, port 443).
- Maintains exactly one control connection; reconnects with exponential backoff + jitter.
- Enforces the **agent-side allowlist** (the customer decides what is reachable).
- Opens local connections to target services and pipes bytes over multiplexed streams.

**Gateway (vendor side)**

- Stateless\* Go service, horizontally scalable (N pods behind a LoadBalancer/Ingress that supports WebSocket).
- Terminates agent WebSocket connections, authenticates agents, registers them in a shared **coordination layer**.
- Exposes a **data-plane API** for vendor services: open a stream to `agent-id` targeting `host:port`/URL.
- Routes requests to the pod currently holding the target agent's connection (cross-pod forwarding).
- Tracks agent liveness and exposes it via API, events, and metrics.

\* Stateless except for in-memory agent connections; agent→pod mapping lives in the coordination layer.

### 3.2 Tunnel Protocol

- **Transport:** WebSocket over TLS (RFC 6455). Chosen for universal firewall/proxy traversal and middlebox compatibility.
- **Multiplexing:** logical streams over the single WS connection using a yamux-style framing layer (stream open/close, data frames, flow-control window updates, ping). Either side can open a stream → true bidirectional initiation.
- **Flow control:** per-stream credit-based windows so one huge transfer (git clone) cannot starve other streams; backpressure propagates end-to-end. All data paths are streaming `io.Copy`-style — **no full-message buffering**, no max payload size.
- **Framing limits:** data chunked into bounded frames (default 64 KiB) regardless of total transfer size; total stream size is unlimited.
- **Keepalive & liveness:** protocol-level ping/pong (default every 15 s, dead after 3 missed) independent of WS-level pings, to survive proxies that swallow control frames.
- **Versioning:** protocol version negotiated at handshake; gateway supports N and N-1 agent protocol versions to enable rolling upgrades.
- **Optional compression:** per-stream negotiated (off by default; useless for git packfiles, useful for logs/JSON).

### 3.3 Stream Types (transparent proxying)

| Mode                | Description                                                                                                                                                                                                                                                                                                                                               |
| ------------------- | --------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **TCP stream**      | Raw byte pipe to `host:port` inside customer network. Supports any TCP protocol: SSH, Postgres, Redis, SMTP, custom. This is the universal fallback that makes the tunnel protocol-agnostic.                                                                                                                                                              |
| **HTTP(S) forward** | Convenience mode: gateway accepts an HTTP request addressed to an agent + target URL, streams request/response bodies. Supports HTTP/1.1, HTTP/2 (incl. gRPC), chunked transfer, server-sent events, WebSocket upgrade pass-through. Optional TLS termination at the agent toward the target with custom CA bundles, or full passthrough (CONNECT-style). |
| **Reverse stream**  | Agent-initiated stream to a named vendor endpoint registered on the gateway (e.g., event ingestion).                                                                                                                                                                                                                                                      |

Long-running streams (hours) and large transfers (100 GB+) are explicitly in scope: idle timeouts are configurable per stream type and disabled by default for active (data-flowing) streams. Long-idle-but-alive streams (e.g., SSE/MCP sessions) are kept healthy by protocol-level per-stream keepalives.

**Credential-agnostic pass-through (core principle):** the tunnel never manages, stores, injects, or rewrites application credentials. Headers and payloads transit verbatim — the vendor side attaches whatever auth the target needs (e.g., `Authorization` header, DB password in the wire protocol), and the agent forwards it untouched. The only exception is RFC-7230-mandated hop-by-hop header handling (`Connection`, `Transfer-Encoding`, `Upgrade`) in HTTP forward mode, which is handled correctly rather than blindly copied so clients don't break. Auth between vendor↔gateway and agent↔gateway is infrastructure auth and is entirely separate from target-application auth.

### 3.4 Multi-Pod Gateway Design

- **Persistent store (pluggable `Store` interface), Redis as default backend.** The store has two responsibilities behind one interface, so future backends (Postgres, etcd, DynamoDB) can be added without touching core logic; an in-memory implementation ships for dev/single-pod:
  1. **ConnectionMap (ephemeral):** `agent_id → {pod_id, pod_epoch, connected_at, last_heartbeat, protocol_version}` with TTL refreshed on heartbeat.
  2. **AgentStore (durable):** persistent agent records — identity, credential reference/hash, status (`active | disabled | revoked`), first_seen, last_seen, last_version, and a free-form **`metadata` JSON field**. Metadata carries adopter-defined dimensions — tenant ID, customer name, environment, region — and is filterable in list APIs. **This is the v1 multi-tenancy mechanism:** tenancy is adopter-defined via metadata rather than a built-in tenant model.
- **Redis deployment guidance:** AOF persistence for AgentStore keys; Sentinel or Cluster for HA documented in the chart. **Data-loss recovery is self-healing:** connected agents re-assert their records on the next heartbeat, so a wiped ConnectionMap converges within one heartbeat interval.
- **Stale-mapping protection:** each gateway pod registers itself with an instance epoch + TTL; mappings pointing at a dead/restarted pod are detected via epoch mismatch and treated as disconnected, instead of routing into a black hole.
- **Registry outage (degraded mode):** existing streams continue (they don't touch the store); routing uses a short-lived local cache of last-known mappings; new agent handshakes are rejected with retry-after until the store recovers.
- **Cross-pod routing:** if a data-plane request lands on pod A but the agent is on pod B, pod A forwards the stream to pod B over internal gRPC (pod-to-pod, mTLS inside the cluster). Optional optimization: clients can ask the gateway API "which pod owns agent X" for direct routing.
- **Single-connection guarantee:** registry enforces one live connection per `agent_id`; a new connection from the same agent ID atomically supersedes the old (configurable: `supersede` | `reject`), preventing split-brain after agent restarts.
- **Graceful pod drain:** on SIGTERM the pod stops accepting new agents, signals connected agents to reconnect (they land on other pods), waits for stream drain up to a deadline.

### 3.5 Agent Liveness

- **Two-tier liveness** (fast detection, cheap on the store):
  1. **Transport ping (pod-local):** protocol ping every 15 s, dead after 3 missed — detected by the owning pod in seconds, **zero store writes**; on death the pod immediately publishes the disconnect to the store and event stream.
  2. **Store heartbeat (backstop):** ConnectionMap TTL refreshed every **5 minutes** (TTL = 2× interval) — only catches the pod-crash case where no one is left to publish; the reconnect flow restores the agent on surviving pods anyway.
- **Version awareness:** agent and gateway exchange component versions (not just protocol version) at handshake. The gateway enforces a configurable `min_agent_version` / compatibility matrix and rejects incompatible agents with a typed, human-readable error. Fleet-wide version visibility via `GET /v1/agents` (per-agent) and `GET /v1/fleet/versions` (aggregate) for managing upgrades across many customers.
- Exposed via:
  - **REST/gRPC API:** `GET /v1/agents`, `GET /v1/agents/{id}` → `CONNECTED | DISCONNECTED | DEGRADED` + last_seen, RTT, version.
  - **Event stream / webhooks:** connect/disconnect events for vendor control planes.
  - **Prometheus metrics:** per-agent up/down, RTT, stream counts, throughput.
- Data-plane calls to a disconnected agent fail fast with a typed error (no hanging).

### 3.6 Reconnect Flow (resilience)

Reconnect is a first-class protocol feature, not just client retry logic:

- **Backoff:** exponential with full jitter (floor 1 s, cap 60 s, configurable); immediate first retry on clean network errors.
- **Fast resume:** on successful auth, the gateway issues a short-lived signed **resume ticket**. Reconnects within the ticket window skip the full auth exchange (one round trip), re-register the ConnectionMap, and restore liveness in < 5 s. Critical for networks where corporate proxies kill long-lived connections every N minutes.
- **In-flight streams are not resumed** — they fail fast with a typed `ErrTunnelReset` so callers can retry idempotently (documented contract; transparent stream resumption is an explicit non-goal).
- **Server-directed reconnect:** during pod drain or rebalancing, the gateway sends a `RECONNECT` directive carrying a **per-agent stagger delay**, preventing thundering-herd on rolling deploys.
- **Storm protection:** handshake rate limiting is per-tenant token bucket; resume-ticket reconnects use a cheaper path so a mass reconnect doesn't lock out legitimate agents.
- **Half-open detection:** heartbeats are verified in both directions; an agent-side watchdog force-redials if the gateway stops acking even while TCP looks alive (NAT/firewall silent drops).
- **Stateless agent:** the agent persists nothing across restarts; identity + config is sufficient to rejoin.

### 3.7 Agent Management API (control plane)

The gateway is the system of record for agent lifecycle, exposed as a CRUD API on the **internal** listener (never public), backed by the AgentStore:

| Endpoint                      | Behavior                                                                                                                                                                                                                                                                                                                                                 |
| ----------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| `POST /v1/agents`             | Register a new agent: `{name, metadata, auth_mode}` → returns **`agent_id`** + a **one-time credential** (bootstrap token or cert bundle, per `auth_mode`). The creator stores the `agent_id` to address this agent on the data-plane, and delivers the credential to the customer (install snippet). Credential is shown once, never retrievable again. |
| `GET /v1/agents`              | List agents; filter by status, liveness, and **metadata fields** (e.g., `?metadata.tenant=acme`).                                                                                                                                                                                                                                                        |
| `GET /v1/agents/{id}`         | Full record: status, liveness, last_seen, versions, metadata.                                                                                                                                                                                                                                                                                            |
| `PATCH /v1/agents/{id}`       | Update name/metadata; `POST /v1/agents/{id}/rotate` issues a new credential (old one valid for a configurable overlap window).                                                                                                                                                                                                                           |
| `POST /v1/agents/{id}/revoke` | Revoke credential + **live-kill** the active connection; future handshakes rejected.                                                                                                                                                                                                                                                                     |
| `DELETE /v1/agents/{id}`      | Remove the record (requires revoked status first).                                                                                                                                                                                                                                                                                                       |

Available as REST + gRPC; same `Authenticator`/`Revoker` hooks apply, so credential issuance can be delegated to an external CA/IdP while keeping this API shape.

---

## 4. Network Environment Compatibility (Customer-Side Constraints)

The agent must connect from hostile/restricted enterprise networks:

| Constraint                                     | Requirement                                                                                                                                                                                                                                                          |
| ---------------------------------------------- | -------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| **Corporate HTTP(S) proxy**                    | Honor standard env vars **by default**: `HTTPS_PROXY`, `HTTP_PROXY`, `NO_PROXY`, `ALL_PROXY` (and lowercase variants), plus explicit config that overrides them; HTTP CONNECT proxies with Basic auth in v1, pluggable proxy-auth interface for Kerberos/NTLM later. |
| **Standard TLS env vars**                      | Honor `SSL_CERT_FILE` / `SSL_CERT_DIR` by default in addition to system trust store and explicit `tls.ca_file`; explicit config wins.                                                                                                                                |
| **TLS-intercepting middleboxes / private CAs** | Configurable CA bundle for the gateway connection (`tls.ca_file`), plus optional system trust store. Option to pin gateway cert (SPKI pin).                                                                                                                          |
| **Private TLS to internal targets**            | Per-target or global custom CA bundles, client certificates for mTLS to internal services, and explicit (logged, discouraged) `insecure_skip_verify` per target.                                                                                                     |
| **Egress allowlisting**                        | Single stable gateway hostname + port 443 only; documented IP/hostname requirements for firewall teams.                                                                                                                                                              |
| **DNS**                                        | Targets resolved **on the agent side** (customer DNS), never on the gateway.                                                                                                                                                                                         |
| **No inbound ports**                           | Agent never listens externally by default (only optional localhost admin/health endpoint).                                                                                                                                                                           |

---

## 5. Security Requirements

Security is a first-class requirement; defaults must be safe.

### 5.1 Authentication & Identity

- **Agent → Gateway:** pluggable `Authenticator` interface. Built-in:
  1. **Bootstrap token + issued mTLS cert** (recommended): one-time registration token exchanges for a per-agent client certificate (built-in lightweight CA or external CA hook); subsequent connections use mTLS.
  2. **Static bearer token** (simple mode) with constant-time comparison and rotation support (two active tokens during rotation).
  3. **JWT/OIDC** validation hook for companies with existing identity systems.
- Every agent has a unique, stable `agent_id`; identity is bound to the credential, not self-asserted.
- **Revocation (first-class):** every credential is individually revocable, enabling clean customer offboarding:
  - **mTLS mode:** per-agent certificate; revocation status lives in the AgentStore and is checked at every handshake — no CRL/OCSP infrastructure needed. Certs are **short-lived** (default 30 days, auto-renewed over the tunnel) so any revocation gap is bounded.
  - **JWT mode:** tokens carry `jti` + `agent_id`; gateway checks a Redis revocation set at handshake.
  - **Live kill:** revoking an agent immediately terminates its active connection (gateway-pushed close), not just future handshakes. Admin API: `POST /v1/agents/{id}/revoke`.
  - **Pluggable hooks:** public `Authenticator` and `Revoker` interfaces so adopters can wire their own IdP/PKI and revocation source without forking.
- **Vendor services → Gateway data-plane API:** authenticated (mTLS or token) and **never exposed publicly**; separate listener/port from the agent-facing WS endpoint.

### 5.2 Authorization — Agent-Side Allowlist (customer controls their side)

- The **agent** is the enforcement point for what the tunnel may reach. Rule model:
  - **Deny rules and allow rules**, both supporting: exact host, wildcard DNS (`*.internal.corp`), IP, CIDR (`10.20.0.0/16`), port lists/ranges. **Deny is evaluated first and always wins**; if any allow rules exist, default is deny; `allow_all: true` is an explicit opt-in (prominent warning, off by default) and still subject to deny rules.
  - **Implicit built-in denies** (overridable only by explicit allow): agent loopback, link-local, and cloud metadata addresses (`169.254.169.254`, etc.) — SSRF protection.
  - **DNS-rebinding protection:** the agent resolves the target, validates the **resolved IPs** against deny/allow rules, and dials the validated IP directly (no re-resolution between check and connect).
- Denied attempts are rejected with a typed error to the gateway **and logged/auditable on the agent** so the customer sees what the vendor attempted.
- Optional **gateway-side policy** layer (per-agent target restrictions) for defense in depth — both sides must allow.

### 5.3 Transport & Data Security

- TLS 1.2 minimum, TLS 1.3 preferred; modern cipher suites only; no plaintext `ws://` in production mode (allowed only with explicit `--insecure-dev`).
- mTLS for gateway pod-to-pod forwarding.
- No payload persistence: gateway and agent are pass-through; payloads never written to disk or logs.
- **Log redaction:** request/response **bodies are never logged or audited, ever**. Common auth headers are redacted by default (`Authorization`, `Proxy-Authorization`, `Cookie`, `Set-Cookie`, `X-Api-Key` and similar); users can extend the list via `telemetry.redact_headers`. Tunnel/infra secrets (tokens, keys) never logged; redaction middleware applies to all log output **and audit sinks** — audit records carry metadata only (agent, target, bytes, duration), never header values or payloads. This matters because the tunnel is credential-agnostic: customer application credentials transit it and must never land in vendor logs.

### 5.4 Hardening & Supply Chain

- Distroless/scratch-based container images, non-root user, read-only root filesystem, no shell.
- Static binaries, `CGO_ENABLED=0`; **optional FIPS release variant** built with Go's FIPS 140-validated crypto module for regulated environments.
- **Zero known critical/high CVEs at release** is a hard release gate (Trivy + `govulncheck` in CI); slim distroless images keep the surface near zero. IPv6 targets supported.
- Signed releases and images (cosign/Sigstore), SBOM (SPDX) published per release, SLSA provenance.
- Dependency scanning + `govulncheck` in CI; documented CVE response SLA.
- Rate limiting and connection limits on the gateway (per-IP handshake rate, max agents per token, max streams per agent, per-stream and per-agent bandwidth caps — all configurable).
- Audit log (structured JSON) of: agent connects/disconnects, auth failures, stream opens (agent_id, target, initiator, bytes, duration), allowlist denials.
- `SECURITY.md`, private vulnerability disclosure process, and a third-party security review before v1.0 GA.

---

## 6. Configuration & Integration

### 6.1 Consistent Configuration Model

Single, consistent precedence across both components: **flags > env vars > config file (YAML) > defaults**. Env vars follow `TUNNEL_<SECTION>_<KEY>` mapping 1:1 to YAML keys. Hot-reload (SIGHUP / file watch) for allowlists and log level.

**Agent `agent.yaml` (illustrative):**

```yaml
agent:
  id: "customer-a-dc1"
gateway:
  url: "wss://tunnel.vendor.com"
  auth:
    mode: bootstrap_token # bootstrap_token | token | mtls
    token_file: /etc/tunnel/token
  tls:
    ca_file: /etc/tunnel/corp-ca.pem # for TLS-intercepting proxies
  proxy:
    url: "http://proxy.corp.local:3128" # or honor env vars
deny: # evaluated first, always wins
  - cidr: "10.20.99.0/24" # e.g. management subnet
allow:
  - host: "git.internal.corp"
    ports: [443, 22]
  - host: "mcp.internal.corp"
    ports: [8443]
    tls:
      pin_sha256: ["<spki-pin>", "<next-rotation-pin>"] # self-signed target
  - cidr: "10.20.0.0/16"
    ports: ["5432", "8000-8999"]
targets_tls:
  default_ca_file: /etc/tunnel/internal-ca.pem
telemetry:
  log_level: info
  metrics_listen: "127.0.0.1:9090"
```

**Gateway `gateway.yaml` (illustrative):**

```yaml
listen:
  agents: ":8443" # public WS endpoint
  api: ":9443" # internal data-plane + admin API
registry:
  backend: redis
  redis: { addr: "redis:6379" }
auth:
  mode: bootstrap_token
limits:
  max_streams_per_agent: 1000
  handshake_rate_per_ip: 10/s
heartbeat:
  transport_ping: 15s # pod-local, no store writes
  dead_after: 45s
  store_heartbeat: 5m # Redis TTL refresh
compat:
  min_agent_version: "1.0.0"
```

### 6.2 Integration Surfaces (for adopting companies)

1. **Go library (`pkg/`)** — embed gateway or agent in an existing service; stable public API: `tunnel.Dial(ctx, agentID, "tcp", "host:port") (net.Conn, error)` and an `http.RoundTripper` implementation (`tunnel.Transport(agentID)`) so existing HTTP clients work unchanged — this is what makes `git clone` over HTTPS via standard tooling trivial on the vendor side.
2. **Gateway data-plane API** — gRPC + REST for non-Go stacks: open stream, HTTP forward, list agents, liveness.
3. **Local forward mode (agent & CLI)** — `tunnelctl forward --agent customer-a --target git.internal:22 --listen 127.0.0.1:2222` exposes a local port on the vendor side mapped through the tunnel (enables `git clone ssh://...` and any TCP client without code changes).
4. **Extension points (Go interfaces):** `Authenticator`, `AgentRegistry`, `PolicyEngine` (gateway-side authz), `AuditSink`, metrics hooks.
5. **White-labeling:** binary/image naming, user-facing strings, and default endpoints configurable so companies can ship it under their product brand.

---

## 7. Deployment & Operations

### 7.1 Artifacts

- **Docker images (2):** `ghcr.io/<org>/tunnel-agent` and `ghcr.io/<org>/tunnel-gateway`; multi-arch (amd64/arm64); distroless; semver + `latest` + immutable digest tags; signed.
- **Binaries:** static builds for linux/darwin/windows (agent especially, for VM installs) via GitHub Releases.
- **Helm charts (2):** `tunnel-gateway` and `tunnel-agent` in a published Helm repo / OCI registry.

### 7.2 Helm — Gateway

- `Deployment` with `replicaCount` (HPA support), `PodDisruptionBudget`, anti-affinity defaults.
- `Service` + optional `Ingress` (with WebSocket annotations for common controllers: nginx, ALB, Traefik) — long timeout guidance baked into chart docs/values.
- Optional bundled Redis subchart or external Redis config.
- Separate Services for agent endpoint vs internal API; `NetworkPolicy` templates.
- TLS via cert-manager integration or provided secrets.
- Standard values conventions: `resources`, `nodeSelector`, `tolerations`, `extraEnv`, `extraVolumes`, `podAnnotations` — fully customizable without forking.

### 7.3 Helm — Agent

- `Deployment` with `replicas: 1` **enforced** (single-agent model): chart fails rendering if `replicas > 1`; `strategy: Recreate` to guarantee no two instances run concurrently.
- ConfigMap-driven config + Secret for credentials; checksum annotations for auto-restart on config change.
- Also documented: plain Docker (`docker run`), docker-compose, and systemd unit for non-Kubernetes customers.

### 7.4 Observability

- **Metrics (Prometheus):** connected agents, agent RTT, streams open/total, bytes in/out (per agent, per stream type), handshake failures, allowlist denials, registry latency, cross-pod forward rate. Grafana dashboards shipped in-repo; ServiceMonitor in chart.
- **Logging:** structured JSON (zap/slog), consistent fields (`agent_id`, `stream_id`, `target`), levels, sampling for high-volume events.
- **Tracing:** OpenTelemetry spans across gateway API → pod forward → agent → target dial (W3C trace context over the tunnel protocol).
- **Health endpoints:** `/healthz` (liveness) and `/readyz` (readiness — registry reachable, listener up) on both components.

### 7.6 Multi-Region

- A region is an **independent gateway deployment with its own Redis** — no shared store, no cross-region routing in v1.
- Each agent is **statically assigned its regional gateway URL** at install time (part of the install snippet the vendor generates).
- Vendor services call the data-plane API of the region that owns the agent; mapping "agent → region" is a thin directory concern left to the adopter's control plane (we expose per-region fleet APIs to feed it).

### 7.7 Reliability Targets (v1)

| Property                           | Target                                                                                                |
| ---------------------------------- | ----------------------------------------------------------------------------------------------------- |
| Agent reconnect after network blip | < 5 s (backoff floor), in-flight streams fail fast with typed error                                   |
| Gateway pod rolling restart        | zero failed _new_ requests; existing streams on the pod terminate gracefully with drain window        |
| Liveness detection of dead agent   | ≤ 45 s pod-local (transport ping); ≤ 10 min worst case only on gateway pod crash (store TTL backstop) |
| Single tunnel throughput           | ≥ 80% of underlying TLS connection throughput; saturate 1 Gbps with default settings                  |
| Concurrent streams per agent       | 1,000 (default cap, configurable)                                                                     |
| Agents per gateway pod             | 5,000+ connections per pod (2 vCPU / 4 GiB reference)                                                 |
| Large transfer                     | 100 GB single stream, constant memory (< 64 MiB per stream buffers)                                   |

---

## 8. Acceptance Criteria (v1.0)

1. `git clone` of a 10 GB repository over the tunnel (both HTTPS via `tunnel.Transport` and SSH via local-forward mode) completes with stable memory and no protocol timeouts.
2. Postgres `psql` session over raw TCP stream works end-to-end, including long-idle sessions with keepalive.
3. gRPC bidirectional streaming and WebSocket pass-through verified through the HTTP forward mode.
4. Agent connects successfully through: HTTP CONNECT proxy, TLS-intercepting proxy with private CA, and direct egress — covered by integration tests in CI (testcontainers-based environments).
5. 3-pod gateway behind nginx ingress: agent connected to pod B is reachable from a request entering pod A; killing pod B causes agent reconnect and recovery < 10 s.
6. Allowlist: request to a non-allowlisted host is denied, audited on the agent, and surfaced as a typed error on the gateway API.
7. Second connection with same `agent_id` supersedes the first atomically; no duplicate registrations in Redis.
8. Helm install of both charts on a vanilla Kubernetes cluster with default values yields a working tunnel in ≤ 10 minutes following the quickstart.
9. Images pass Trivy scan with zero critical CVEs at release time; releases are signed with SBOMs attached.
10. Public Go API covered by examples: embed gateway, embed agent, `RoundTripper` usage, raw `Dial` usage.
11. Full lifecycle via management API: create agent → receive one-time credential → agent connects → status `CONNECTED` → revoke → live connection killed within seconds and reconnect attempts rejected with a typed error.

---

## 9. Milestones

| Milestone                               | Scope                                                                                                                      |
| --------------------------------------- | -------------------------------------------------------------------------------------------------------------------------- |
| **M1 — Core tunnel (6 wks)**            | WS transport, mux/flow control, TCP + HTTP forward streams, single-pod gateway, token auth, agent allowlist, Docker images |
| **M2 — Scale & security (6 wks)**       | Redis registry, multi-pod routing & drain, liveness API/events, bootstrap-token→mTLS, rate limits, audit log               |
| **M3 — Enterprise environment (4 wks)** | Corporate proxy support, private CA/TLS options, local-forward CLI, reverse streams, OTel tracing                          |
| **M4 — Packaging & GA (4 wks)**         | Helm charts, Grafana dashboards, docs site, signed releases/SBOM, security review, perf benchmarks vs targets              |

---

## 10. Resolved Decisions

| Decision        | Resolution                                                                                                                                                                    |
| --------------- | ----------------------------------------------------------------------------------------------------------------------------------------------------------------------------- |
| Store backend   | **Redis** is the default persistent store for both connection mapping and durable agent records, behind a pluggable `Store` interface for future backends (Postgres, etcd, …) |
| Agent replicas  | **Single replica, final** — one agent per network segment; `strategy: Recreate`, supersede-on-reconnect handles restarts. No HA pair.                                         |
| Target policy   | **Allow + deny rules** on hosts, wildcards, IPs, CIDRs, ports; deny wins; SSRF + DNS-rebinding protections built in                                                           |
| Credentials     | **Fully agnostic** — both sides attach their own application auth (headers, wire-protocol creds); tunnel passes through verbatim                                              |
| Env vars        | Standard `HTTP(S)_PROXY` / `NO_PROXY` / `ALL_PROXY` / `SSL_CERT_FILE` / `SSL_CERT_DIR` honored by default                                                                     |
| Reconnect       | First-class protocol flow: jittered backoff, resume tickets, server-directed staggered reconnect, half-open detection                                                         |
| Revocation      | Per-agent revocable credentials (short-lived auto-renewed certs, or JWT `jti` denylist in Redis); live connection kill; pluggable `Authenticator`/`Revoker` hooks             |
| Heartbeat       | Transport ping 15 s pod-local (no store writes) + store heartbeat every 5 min; reconnect flow covers transient failures                                                       |
| Log redaction   | Bodies never logged; common auth headers redacted by default, user-extensible redaction list; audit is metadata-only                                                          |
| Versioning      | Agent & gateway exchange versions at handshake; gateway enforces compatibility matrix; fleet version APIs                                                                     |
| Multi-region    | Independent gateway deployment + dedicated Redis per region; static agent→region assignment; no cross-region routing in v1                                                    |
| Compliance      | FIPS build variant, distroless/slim images, zero critical/high CVE release gate, IPv6 targets                                                                                 |
| Agent lifecycle | Gateway management API (CRUD): create → one-time credential + `agent_id`; list/status/rotate/revoke (live-kill)/delete, backed by AgentStore                                  |
| Cert issuance   | Gateway issues per-agent credentials (built-in lightweight CA for cert mode); pluggable external CA/IdP via `Authenticator` hook                                              |
| Multi-tenancy   | Adopter-defined via free-form `metadata` JSON on the agent record, filterable in APIs; no built-in tenant model in v1                                                         |

## 11. Open Questions

1. QUIC/HTTP3 transport as a future alternative when middleboxes allow it — keep transport abstraction now?
2. Project naming + OSS governance (CNCF sandbox ambitions?).
