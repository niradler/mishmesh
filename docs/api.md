# mishmesh control API — REST contract (v1)

Base: control listener (default `127.0.0.1:8081`), all under `/api/v1` unless noted.
Content-Type `application/json`. Errors: `{"error": "message"}` with appropriate HTTP status.

## Auth model

- **Browser:** httpOnly cookie `mm_session` (Secure when `PUBLIC_SCHEME=https`, SameSite=Lax). Set on login.
- **Programmatic:** `Authorization: Bearer <token>` — the admin/bootstrap token (`MISHMESH_API_AUTH_TOKEN`).
- When `MISHMESH_AUTH_ENABLED=false`: no auth required; everything operates on the default org (`org_default`).
- When enabled: session or admin bearer required. Org is taken from the session's active org; role gates writes.

Every resource is scoped to the caller's active org. A resource outside that org returns 404 (never 403), for sessions. The admin bearer token is a super-admin: it may pass `org_id` to target any org. A session whose user has no membership in any org gets 401. Unknown `/api/*` paths return a JSON 404.

Roles: `owner` > `admin` > `member`. Writes to org/members/quota require `admin`+; agent/endpoint CRUD requires `member`+.

## Auth & identity

| Method | Path | Body / Notes |
|---|---|---|
| POST | `/auth/register` | `{email, password, name, invite_token?}` → 201 me; only when `AUTH_PASSWORD_ENABLED`. Email is normalised and validated; rate limited per IP and per email (429). Signup mode `MISHMESH_SIGNUP_MODE`: `org` (default) creates a new org with the user as owner; `invite` lets only the first user self-register (owner of `org_default`), later registrations need an `invite_token` else 403. An `invite_token` (from `POST /members`) must match the registering email; invalid, wrong, expired or reused tokens give 403 and never grant membership. Sets cookie. |
| POST | `/auth/login` | `{email, password}` → 200 me; sets cookie; rate limited per IP and per email (429). |
| POST | `/auth/logout` | clears cookie → 204 |
| GET | `/auth/me` | → `{id, email, name, active_org_id, role, memberships:[{org_id, org_name, role}]}` or 401 |
| POST | `/auth/accept-invite` | session + `{invite_token}`; the token's email must equal the session user's email; adds the membership with the invited role → me |
| POST | `/auth/switch-org` | `{org_id}` → me; changes the session's active org; 404 if not a member |
| GET | `/auth/google/start` | 302 → Google consent (state cookie) |
| GET | `/auth/google/callback` | `?code&state` → sets cookie, 302 → web UI |
| GET | `/auth/config` | public → `{password_enabled, google_enabled, auth_enabled, password_signup, signup_mode}` (for the login screen) |

## Status (dashboard summary)

| GET | `/status` | → `{agents:{total,connected}, endpoints:{total,by_kind:{http,tcp,tls}}, usage_bytes, quota:{...}}` |

## Agents

| Method | Path | Notes |
|---|---|---|
| GET | `/agents` | list org agents → `[agentDTO]` |
| POST | `/agents` | `{name}` → `{agent: agentDTO, token: "<raw once>"}` |
| GET | `/agents/{id}` | agentDTO |
| PATCH | `/agents/{id}` | `{name?, status?}` |
| DELETE | `/agents/{id}` | must be revoked first → 204 |
| POST | `/agents/{id}/rotate` | → 201 `{token}`; revokes all previous tokens and closes the live session |
| POST | `/agents/{id}/revoke` | live-kills connection → `{status:"revoked"}` |
| GET | `/agents/{id}/endpoints` | `[endpointDTO]` |
| GET | `/agents/{id}/tokens` | `[tokenDTO]` |

`agentDTO`: `{id, org_id, name, status, connected, created_at, last_seen_at?}`

## Endpoints (reserved + policy management)

| Method | Path | Notes |
|---|---|---|
| GET | `/endpoints` | org endpoints → `[endpointDTO]` |
| POST | `/endpoints` | create reserved: `{agent_id, kind, method?, subdomain?, domain?, port?, policy?}` |
| GET | `/endpoints/{id}` | endpointDTO |
| PATCH | `/endpoints/{id}` | `{subdomain?, domain?, port?, policy?}` |
| DELETE | `/endpoints/{id}` | 204 |

`endpointDTO`: `{id, agent_id, org_id, kind, method, lifecycle, subdomain, domain, port, public_url, online, policy}`
`method` (default `native`): `native | ssh | proxy | tailscale | cloudflare`. For `method=proxy` omit `agent_id`
and set `policy.proxy_target` (`host:port`); mishmesh reverse-proxies it directly (no agent). `ssh` endpoints
are created implicitly by the clientless SSH remote-forward server (see deploy guide), not via this API.
`policy` (all optional): `{request_headers_add:{}, request_headers_remove:[], response_headers_add:{}, response_headers_remove:[], host_header, strip_path_prefix, add_path_prefix, basic_auth_user, basic_auth_password (write-only, hashed server-side), ip_allow:[cidr], ip_deny:[cidr], force_https, max_body_bytes, compression, oidc:{...}, mtls:{client_ca_pem, allowed_cns:[]}, proxy_target}`

`mtls`: when set, the HTTPS edge requires a client certificate that chains to `client_ca_pem`
(and whose CN is in `allowed_cns`, if given); otherwise 403. Requires the HTTPS ingress (`TLS_ENABLED`).

## Custom domains (ownership verification)

Enabled by `MISHMESH_DOMAIN_VERIFICATION` (default `true` when `MISHMESH_SIGNUP_MODE=org`, `false` when `invite`; set it explicitly to override). When off the routes below are not registered and any well-formed hostname outside the base domain may be bound to an endpoint (homelab / single-company deployments). When on, `domain` on `POST`/`PATCH /endpoints` is accepted only if the domain is verified for the caller's org (403 otherwise). A verified domain is unique across all orgs; unverified claims by several orgs may coexist but only the org that proves DNS control can verify. Names equal to, or under, the base domain are rejected (400) in both modes. Wildcards are not supported.

| Method | Path | Notes |
|---|---|---|
| GET | `/domains` | org domains -> `[domainDTO]` |
| POST | `/domains` | `{name}` -> 201 `domainDTO`; idempotent for the same org (200); 409 if another org already verified the name |
| POST | `/domains/{id}/verify` | resolves the challenge TXT record; 200 `domainDTO` when found, 422 when not, 409 if another org verified it first |
| DELETE | `/domains/{id}` | 204; 409 while an endpoint of the org is bound to it |

`domainDTO`: `{id, name, verified, verified_at?, challenge?:{type:"TXT", name:"_mishmesh-challenge.<domain>", value:"<token>"}, cname_target, created_at}`. `challenge` is present until verified. Proof of ownership is the TXT record only; `cname_target` is the base domain host the domain's CNAME must point at so traffic reaches the platform (a CNAME alone is not accepted as proof, since anyone can point a CNAME at the platform).

## Quota

| GET | `/quota` | → `{max_agents, max_endpoints, max_bandwidth_bytes, usage:{agents, endpoints, bandwidth_bytes}}` |
| PUT | `/quota` | admin+ `{max_agents, max_endpoints, max_bandwidth_bytes}` → same shape as GET |

## Org & members

| GET | `/orgs` | orgs the caller belongs to → `[{id, name, created_at}]` |
| GET | `/orgs/{id}` | member only, else 404 |
| POST | `/orgs` | `{name}` → creates org, caller becomes owner |
| GET | `/members` | current org memberships → `[{user:{id,email,name}, role, created_at}]` |
| POST | `/members` | admin+ `{email, role}` → 201 `{id, email, role, invited_by, created_at, expires_at, invite_token, invite_url}`. Creates a single-use invite (7 day expiry); the raw token is returned once and only its SHA-256 is stored. The role may not exceed the caller's own (403). Nobody is added until the invite is redeemed via `/auth/register` or `/auth/accept-invite`, or by a Google login whose `email_verified` is true |
| GET | `/invites` | admin+ pending invites (no tokens) |
| DELETE | `/invites/{id}` | admin+ revoke → 204 |
| PATCH | `/members/{user_id}` | admin+ `{role}` |
| DELETE | `/members/{user_id}` | admin+ |

## Audit

| GET | `/audit?limit=200` | → `[{id, actor, action, target, detail, created_at}]` |

## Ops (not under /api/v1)

| GET | `/healthz`, `/readyz` | liveness/readiness |
| GET | `/metrics` | Prometheus exposition (control listener); requires `Authorization: Bearer` of `MISHMESH_METRICS_TOKEN` if set, else the API auth token |

## Reach-in data-plane (enterprise; `MISHMESH_REACHIN_ENABLED`)

| POST | `/api/v1/reach/{agent_id}/http` | `{target:"host:port", tls?, insecure?, method?, path?, headers?, body?}` sends one HTTP request through the agent to an allowlisted target and returns `{status, headers, body}` (body capped at 8MB). If the agent cannot dial the target the response is `502` with the agent's reason. |
| GET | `/api/v1/reach/{agent_id}/stream` | Raw bidirectional TCP stream. Send `Connection: Upgrade` and `Upgrade: mishmesh-stream`, with query `target=host:port` and optional `tls=true`, `insecure=true`. The server answers `101 Switching Protocols`, after which the connection carries raw bytes to and from the target. Half-close is propagated in both directions: closing the write side of your socket half-closes the target, and the target's close ends your read side. |

Both routes need an authenticated caller allowed to write agents in the agent's organization; an agent in another organization returns `404`. Targets are subject to the agent-side allowlist. Raw stream example:

```bash
printf 'GET /api/v1/reach/ag_123/stream?target=db.internal:5432 HTTP/1.1
Host: api
Authorization: Bearer $TOKEN
Connection: Upgrade
Upgrade: mishmesh-stream

' | nc 127.0.0.1 8080
```
