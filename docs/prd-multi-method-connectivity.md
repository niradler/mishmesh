# PRD — Multi-Method Connectivity Platform ("mishmesh Connect")

**Status:** Draft v1.0
**Owner:** Platform / Product
**Date:** 2026-06-24
**Relationship:** Extends the mishmesh SaaS platform (`docs/superpowers/specs/2026-06-13-mishmesh-saas-design.md`), which is built on the Tunnelmesh core (`docs/prd.md`). This PRD adds connectivity *methods* alongside mishmesh's existing native tunnel.

---

## 1. Overview

### 1.1 Problem Statement

Exposing a private service to the outside world — or reaching into a private network — has many possible answers: ngrok-style agent tunnels, Tailscale's WireGuard mesh, Cloudflare Tunnel, plain reverse proxies, SSH remote forwards. Each has different trade-offs (install footprint, who carries the traffic, NAT behavior, auth model, cost), and today a team must pick one provider, learn its tooling (`tailscaled`, `cloudflared`, ACLs, tunnel tokens), and re-learn it for every other provider.

mishmesh already ships a production native tunnel. This PRD turns mishmesh into a **connectivity control plane**: one dashboard, one identity/policy model, one audit trail — over *multiple* connectivity methods, including third-party providers it manages on the user's behalf. The user chooses *how* a service is reached; mishmesh manages the lifecycle regardless of method.

### 1.2 Goals

- One product that manages **several connectivity methods** behind a single tenancy, identity, policy, quota, and audit model.
- **Orchestrate third-party providers** (Tailscale, Cloudflare) on the user's behalf — provision their resources via API, run/configure their daemon under management — so users never touch provider-specific tooling.
- Add **clientless** access paths (no mishmesh agent install) for users who can't or won't install software.
- Keep the existing native tunnel unchanged and first-class; new methods slot in beside it, not on top of it.
- Each method is an **independent subsystem** with a well-defined interface to the shared control plane, shippable on its own.

### 1.3 Core Architectural Principle — Orchestrate, Don't Carry

For third-party providers (Tailscale, Cloudflare), mishmesh is **control plane only**. mishmesh provisions and manages provider resources and configures the provider's own daemon, but **provider traffic flows through the provider's data plane, never mishmesh's**. mishmesh does not run WireGuard relays or cloudflared endpoints itself.

Consequences:
- Low engineering lift — we integrate APIs, we don't build a competing data plane.
- Nothing new for mishmesh to scale per byte of third-party traffic.
- Honest "managed" story: the user's traffic has the provider's exact properties, mishmesh adds management.

mishmesh carries traffic **only** for its own native tunnel and its own clientless paths (below).

### 1.4 Non-Goals (v1)

- mishmesh becoming a WireGuard/mesh data plane itself (that contradicts §1.3; Tailscale stays the mesh).
- Carrying or relaying Tailscale/Cloudflare traffic through mishmesh.
- Billing/cost reconciliation for third-party provider usage.
- Provider feature-parity dashboards (we manage what mishmesh needs; we don't re-skin all of Tailscale/Cloudflare).
- UDP for the clientless paths (deferred; native tunnel scope unchanged).

---

## 2. Personas & Use Cases

| Persona | Description | Primary Need |
| --- | --- | --- |
| **Platform admin** | Owns the mishmesh org, decides how services are exposed | One console to choose and manage method per service |
| **Developer** | Wants their local/private service reachable | Fastest path to a URL — ideally with no install |
| **Security admin** | Approves what's exposed and how | Uniform auth/policy/audit regardless of underlying method |
| **Existing Tailscale/Cloudflare user** | Already invested in a provider | Manage that provider through mishmesh without re-tooling |

### Core use cases

1. **Pick-your-method exposure** — admin exposes a private HTTP service and chooses native tunnel, clientless SSH, managed Tailscale, or managed Cloudflare from one place.
2. **Clientless quick-expose** — a developer with no rights to install software runs a single stock `ssh -R` (or points mishmesh at an already-reachable target) and gets a managed URL.
3. **Managed Tailscale onboarding** — mishmesh provisions a tailnet device + ACLs and configures `tailscaled` on the agent host, so a service joins the tailnet without the user learning Tailscale.
4. **Managed Cloudflare Tunnel** — mishmesh creates the tunnel + DNS + (optionally) Access policy via Cloudflare API and runs `cloudflared` under management.
5. **Unified policy & audit** — auth, IP allow/deny, and audit events apply consistently across methods where mishmesh is in the control or data path.

---

## 3. Connectivity Methods

| # | Method | Client install | Data plane | mishmesh role |
| - | --- | --- | --- | --- |
| 1 | **Native tunnel** | mishmesh agent | mishmesh | full (built ✅) |
| 2 | **Clientless — SSH remote forward** | none (stock `ssh`) | mishmesh | full |
| 3 | **Clientless — agentless proxy** | none | mishmesh | full |
| 4 | **Managed Tailscale** | `tailscaled` (managed) | Tailscale | orchestrate only |
| 5 | **Managed Cloudflare** | `cloudflared` (managed) | Cloudflare | orchestrate only |

### 3.1 Native tunnel (existing)
Already shipped: WSS + yamux, HTTP/TCP/TLS endpoints, per-endpoint policy. Unchanged by this PRD; it is the reference method other methods conform to.

### 3.2 Clientless — SSH remote forward
mishmesh runs an SSH server. A user with no mishmesh client runs a stock command, e.g. `ssh -R 80:localhost:3000 connect.mishmesh.io`. The reverse forward maps to a mishmesh **Endpoint**, reusing the existing routing, TLS, auth, and policy edge. Strongest "no install" story — SSH is already everywhere. Authn via per-org SSH keys / tokens issued by the control plane.

### 3.3 Clientless — agentless proxy
For targets mishmesh can already reach (a routable host, or one reachable via a managed tailnet), mishmesh reverse-proxies directly: managed DNS + TLS + policy, no tunnel and no install. Pure L7/L4 proxy bound to an Endpoint.

### 3.4 Managed Tailscale (orchestrate-only)
mishmesh, given the org's Tailscale API credentials (OAuth client / API key), provisions a device/auth-key and ACL entries, then instructs the mishmesh agent host to install/run/configure `tailscaled` joined to that tailnet. mishmesh tracks device liveness and can revoke. Traffic flows over Tailscale; mishmesh never sees it.

### 3.5 Managed Cloudflare (orchestrate-only)
mishmesh, given the org's Cloudflare API token, creates a Cloudflare Tunnel, DNS records, and optionally Access policy, then runs `cloudflared` under management on the agent host with the issued tunnel token. mishmesh tracks tunnel health and can tear down. Traffic flows over Cloudflare.

---

## 4. Shared control-plane model

All methods bind to the existing data model: `Org → User/Membership → Agent → Endpoint`, with `Token`, `Quota`, `AuditEvent`, `Domain`.

- **Endpoint gains a `method` field** (`native | ssh | proxy | tailscale | cloudflare`) plus a method-specific config blob. Routing, naming, and lifecycle (ephemeral/reserved) stay shared.
- **Provider credentials** are a new per-org durable entity (encrypted at rest): Tailscale OAuth client / API key, Cloudflare API token. Stored in `DataStore`; never logged.
- **A `Connector` interface** abstracts a method's lifecycle: `Provision(ctx, endpoint) → status`, `Reconcile`, `Teardown`, `Health`. Native/SSH/proxy implement it against mishmesh's own data plane; Tailscale/Cloudflare implement it against provider APIs + a managed daemon supervisor on the agent.
- **Agent gains a managed-daemon supervisor** for methods 4–5: install/launch/configure/monitor `tailscaled` or `cloudflared`, report health up the control channel. Gated behind allowlist + explicit org opt-in.
- **Policy/auth/audit** apply uniformly where mishmesh is in the path (1–3) and at the orchestration boundary for 4–5 (who provisioned/revoked what).

This keeps each method behind one interface so the dashboard, API, and quota logic don't branch per provider.

---

## 5. Decomposition & sequencing

Each method ships as its **own spec → plan → build** cycle. This PRD is the umbrella; it does not design any single method in full.

Recommended order (lowest lift / highest reuse first):

1. **Control-plane seams** — `Endpoint.method`, `Connector` interface, provider-credential entity, audit hooks. (Foundation for everything below.)
2. **Clientless SSH remote forward** — closest to the existing endpoint/edge model; biggest "wow, no install" payoff.
3. **Managed Cloudflare** — well-documented API, single daemon (`cloudflared`), clean tunnel/DNS/Access primitives.
4. **Managed Tailscale** — daemon + ACL provisioning; slightly more host-level (network device) management.
5. **Agentless proxy** — smallest, can land opportunistically once §1 seams exist.

Order is a recommendation, not a commitment; methods 2–5 are independent once §1 lands.

---

## 6. Open questions

1. **Managed-daemon footprint** — do we install `tailscaled`/`cloudflared` into the agent's host/container, or ship agent images that bundle them? (Affects security review and the allowlist model.)
2. **Credential custody** — store provider API tokens (more managed, more liability) vs. short-lived OAuth with the user re-consenting (safer, more friction)?
3. **SSH endpoint auth** — per-org SSH CA, individual authorized keys, or token-in-username? (Drives the clientless onboarding UX.)
4. **Health/liveness for orchestrated methods** — how deep does mishmesh probe provider-side health vs. just daemon-up?
5. **Quota semantics across methods** — what does "bandwidth quota" mean for methods whose bytes mishmesh never sees (4–5)? Likely count endpoints/devices, not bytes.

---

## 7. Success criteria

- A user can expose the same private service via at least two different methods from one dashboard, with identical naming/policy ergonomics.
- A clientless method works end-to-end with **zero mishmesh software** installed on the target host.
- A managed third-party method is provisioned, health-tracked, and torn down entirely through mishmesh, with the user never running a provider CLI.
- Each method is independently testable behind the `Connector` interface; `go test -race ./...` stays green.
