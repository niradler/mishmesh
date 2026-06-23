# mishmesh control API — REST contract (v1)

Base: control listener (default `127.0.0.1:8081`), all under `/api/v1` unless noted.
Content-Type `application/json`. Errors: `{"error": "message"}` with appropriate HTTP status.

## Auth model

- **Browser:** httpOnly cookie `mm_session` (Secure when `PUBLIC_SCHEME=https`, SameSite=Lax). Set on login.
- **Programmatic:** `Authorization: Bearer <token>` — the admin/bootstrap token (`MISHMESH_API_AUTH_TOKEN`).
- When `MISHMESH_AUTH_ENABLED=false`: no auth required; everything operates on the default org (`org_default`).
- When enabled: session or admin bearer required. Org is taken from the session's active org; role gates writes.

Roles: `owner` > `admin` > `member`. Writes to org/members/quota require `admin`+; agent/endpoint CRUD requires `member`+.

## Auth & identity

| Method | Path | Body / Notes |
|---|---|---|
| POST | `/auth/register` | `{email, password, name}` → 201 `{user, org}`; only when `AUTH_PASSWORD_ENABLED`. First user of a new org becomes `owner`. Sets cookie. |
| POST | `/auth/login` | `{email, password}` → 200 `{user, org, role}`; sets cookie. |
| POST | `/auth/logout` | clears cookie → 204 |
| GET | `/auth/me` | → `{user, org, role, memberships:[{org, role}]}` or 401 |
| GET | `/auth/google/start` | 302 → Google consent (state cookie) |
| GET | `/auth/google/callback` | `?code&state` → sets cookie, 302 → web UI |
| GET | `/auth/config` | public → `{password_enabled, google_enabled, auth_enabled}` (for the login screen) |

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
| POST | `/agents/{id}/rotate` | → `{token}` |
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

## Quota

| GET | `/quota` | → `{max_agents, max_endpoints, max_bandwidth_bytes, usage:{agents, endpoints, bandwidth_bytes}}` |
| PUT | `/quota` | admin+ `{max_agents, max_endpoints, max_bandwidth_bytes}` |

## Org & members

| GET | `/orgs` | orgs the caller belongs to |
| POST | `/orgs` | `{name}` → creates org, caller becomes owner |
| GET | `/members` | current org memberships → `[{user:{id,email,name}, role, created_at}]` |
| POST | `/members` | admin+ `{email, role}` — add existing user to org |
| PATCH | `/members/{user_id}` | admin+ `{role}` |
| DELETE | `/members/{user_id}` | admin+ |

## Audit

| GET | `/audit?limit=200` | → `[{id, actor, action, target, detail, created_at}]` |

## Ops (not under /api/v1)

| GET | `/healthz`, `/readyz` | liveness/readiness |
| GET | `/metrics` | Prometheus exposition (control listener) |

## Reach-in data-plane (enterprise; `MISHMESH_REACHIN_ENABLED`)

| POST | `/api/v1/reach/{agent_id}` | `{target:"host:port", kind:"tcp"|"http", ...}` opens an API-initiated stream to the agent's allowlisted target. HTTP variant proxies a single request; TCP variant hijacks for raw bytes. Subject to agent-side allowlist. |
