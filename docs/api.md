# mishmesh control API — REST contract (v1)

Base: control listener (default `127.0.0.1:8081`). Every route is under `/api/v1` unless noted.
Content-Type `application/json`. Errors: `{"error": "message"}` with the matching HTTP status.

- [Auth model](#auth-model)
- [Roles and policy](#roles-and-policy)
- [Auth & identity](#auth--identity)
- [Status](#status-dashboard-summary)
- [Agents](#agents)
- [Endpoints](#endpoints-reserved--policy-management)
- [Custom domains](#custom-domains-ownership-verification)
- [Quota](#quota)
- [Org & members](#org--members)
- [Audit](#audit)
- [Ops](#ops-not-under-apiv1)
- [Reach-in data-plane](#reach-in-data-plane)

## Auth model

The server refuses to start unless `MISHMESH_API_AUTH_TOKEN` is set or `MISHMESH_API_AUTH_DISABLED=true` is set explicitly.

- **Programmatic:** `Authorization: Bearer <MISHMESH_API_AUTH_TOKEN>`. This is the operator's admin token. It acts as the platform operator: `?org_id=` picks any org, otherwise it uses `org_default`. The operator bypasses the org's policy on every org-scoped route, so a tenant cannot lock the operator out by editing its own policy (see [Roles and policy](#roles-and-policy)).
- **Browser:** httpOnly cookie `mm_session` (`Secure` when `PUBLIC_SCHEME=https`, `SameSite=Lax`), set by login or registration. Accepted only when `MISHMESH_AUTH_ENABLED=true`. The org is the session's active org, and the user's role in it gates every route.

| `API_AUTH_TOKEN` | `AUTH_ENABLED` | What `/api/v1` accepts |
| --- | --- | --- |
| set | `false` | the bearer token only; anything else gets 401 |
| set | `true` | the bearer token or a session |
| unset, `API_AUTH_DISABLED=true` | `false` | anything, as owner of `org_default` (local demo only) |
| unset, `API_AUTH_DISABLED=true` | `true` | a session |

Session (cookie) requests that are not GET, HEAD or OPTIONS must send `Content-Type: application/json` whenever they have a body (415 otherwise). When the browser supplies `Origin` or `Sec-Fetch-Site`, the request must come from the API's own origin, `BASE_DOMAIN`, the `OIDC_REDIRECT_URL` host or `MISHMESH_ALLOWED_ORIGINS` (403 otherwise). This also applies to `/auth/login`, `/auth/register`, `/auth/logout`, `/auth/switch-org` and `/auth/accept-invite`. Bearer-token requests are exempt.

Every resource is scoped to the caller's active org. A resource outside that org returns 404, never 403. A session whose user has no membership in any org gets 401. Unknown `/api/*` paths return a JSON 404.

## Roles and policy

Roles: `owner`, `admin`, `member`. Each route requires one action. A caller without it gets `403 permission denied: <action>`. The platform operator (admin bearer token) bypasses the policy entirely; `quota:write` is the one action that only the operator may use.

| Action | Routes | Default roles |
| --- | --- | --- |
| `agent:read` | `GET /agents`, `/agents/{id}`, `/agents/{id}/tokens` | owner, admin, member |
| `agent:write` | agent create/patch/delete/rotate/revoke, reach-in | owner, admin |
| `endpoint:read` | `GET /endpoints`, `/endpoints/{id}`, `/agents/{id}/endpoints`, `GET /domains` | owner, admin, member |
| `endpoint:write` | endpoint create/patch/delete, domain create/verify/delete | owner, admin |
| `quota:read` / `quota:write` | `GET` / `PUT /quota` | all / operator only (see below) |
| `member:read` | `GET /members` | owner, admin, member |
| `member:manage` | `POST`/`PATCH`/`DELETE /members`, `GET`/`DELETE /invites` | owner, admin |
| `audit:read` | `GET /audit` | owner, admin, member |
| `status:read` | `GET /status` | owner, admin, member |
| `policy:read` / `policy:write` | `GET` / `PUT /policy` | all / owner, admin |

`/orgs` routes need only an authenticated caller.

Each org can replace the default matrix (compiled to Cedar):

| Method | Path | Notes |
| --- | --- | --- |
| GET | `/policy` | → `{is_default, roles, actions, matrix:{role:{action:bool}}, cedar_src}` |
| PUT | `/policy` | `{matrix:{role:[action]}}` replaces the org's whole policy. A role that is left out gets no actions. Unknown roles or actions → 400. Returns the GET shape. Audited as `policy.update`. Each node caches an org's compiled policy for at most 5 seconds, so in a cluster a change reaches every node within 5s. Removing `policy:write` from `owner` also locks out the admin bearer token for that org |

## Auth & identity

| Method | Path | Body / Notes |
| --- | --- | --- |
| POST | `/auth/register` | `{email, password, name, invite_token?}` → 201 me. Only when `AUTH_PASSWORD_ENABLED`. The email is normalised and validated, and registration is rate limited per IP and per IP+email pair (429). Signup mode `MISHMESH_SIGNUP_MODE`: `org` (default) creates a new org with the user as owner. `invite` lets only the first user self-register (as owner of `org_default`); later registrations need an `invite_token`, else 403. An `invite_token` (from `POST /members`) must match the registering email. Invalid, mismatched, expired or reused tokens give 403 and never grant membership. Sets the cookie. |
| POST | `/auth/login` | `{email, password}` → 200 me. Sets the cookie. Rate limited per IP and per IP+email pair (429), so an attacker cannot lock a victim out from another address. Unknown emails still pay a bcrypt comparison so response time does not reveal whether an account exists. In cluster mode the counters are shared across nodes, and the client IP honours `MISHMESH_TRUSTED_PROXIES`. |
| POST | `/auth/logout` | clears the cookie → 204 |
| GET | `/auth/me` | → `{id, email, name, active_org_id, role, memberships:[{org_id, org_name, role}]}` or 401 |
| POST | `/auth/accept-invite` | session + `{invite_token}`. The token's email must equal the session user's email. Adds the membership with the invited role → me |
| POST | `/auth/switch-org` | `{org_id}` → me. Changes the session's active org; 404 if not a member |
| GET | `/auth/google/start` | 302 → Google consent (state cookie) |
| GET | `/auth/google/callback` | `?code&state` → sets the cookie, 302 → web UI. Never attaches to an existing password account with the same email |
| GET | `/auth/config` | public → `{password_enabled, google_enabled, auth_enabled, password_signup, signup_mode}` (for the login screen) |

## Status (dashboard summary)

| Method | Path | Notes |
| --- | --- | --- |
| GET | `/status` | → `{agents:{total, connected}, endpoints:{total, online, by_kind:{http, tcp, tls}}, usage_bytes, quota:{...}}` |

## Agents

| Method | Path | Notes |
| --- | --- | --- |
| GET | `/agents` | org agents → `[agentDTO]` |
| POST | `/agents` | `{name}` → `{agent: agentDTO, token: "<raw, shown once>"}` |
| GET | `/agents/{id}` | agentDTO |
| PATCH | `/agents/{id}` | `{name?, status?}` |
| DELETE | `/agents/{id}` | must be revoked first → 204 |
| POST | `/agents/{id}/rotate` | → 201 `{token}`. Revokes all previous tokens and closes the live session |
| POST | `/agents/{id}/revoke` | kills the live connection → `{status:"revoked"}` |
| GET | `/agents/{id}/endpoints` | `[endpointDTO]` |
| GET | `/agents/{id}/tokens` | `[{id, created_at, revoked_at?}]` |

`agentDTO`: `{id, org_id, name, status, connected, created_at, last_seen_at?}`

## Endpoints (reserved + policy management)

| Method | Path | Notes |
| --- | --- | --- |
| GET | `/endpoints` | org endpoints → `[endpointDTO]` |
| POST | `/endpoints` | create reserved: `{agent_id, kind?, method?, subdomain?, domain?, port?, policy?}`. `kind` defaults to `http` |
| GET | `/endpoints/{id}` | endpointDTO |
| PATCH | `/endpoints/{id}` | `{subdomain?, domain?, port?, policy?}` |
| DELETE | `/endpoints/{id}` | 204 |

`endpointDTO`: `{id, agent_id, org_id, kind, method, lifecycle, subdomain, domain, port, public_url, online, policy}`

`subdomain` is lowercased and must be a DNS label (letters, digits, hyphens, 1-63 chars, no leading or trailing hyphen). The names `app api www admin login auth mail status docs static assets cdn connect dashboard console` and the first label of the configured base domain are reserved. Violations return 400 on POST and PATCH; agents registering the same names are refused too.

`method` (default `native`): `native | ssh | proxy | tailscale | cloudflare`. For `method=proxy`, omit `agent_id` and set `policy.proxy_target` (`host:port`); a target in a blocked range returns 400 (see `MISHMESH_PROXY_ALLOW_PRIVATE`, `MISHMESH_PROXY_ALLOWED_CIDRS`), and PATCH of the policy re-validates it. mishmesh then reverse-proxies the target directly, with no agent. `ssh` endpoints are created implicitly by the clientless SSH remote-forward server (see the deploy guide), not through this API.

`policy` (all fields optional): `{request_headers_add:{}, request_headers_remove:[], response_headers_add:{}, response_headers_remove:[], host_header, strip_path_prefix, add_path_prefix, basic_auth_user, basic_auth_password (write-only, bcrypt-hashed server-side), ip_allow:[cidr], ip_deny:[cidr], force_https, max_body_bytes, compression, oidc:{issuer, client_id, client_secret, allowed_emails, allowed_domains}, mtls:{client_ca_pem, allowed_cns:[]}, rate_limit:{requests, period_seconds, burst?, scope?}, proxy_target}`

`rate_limit` is a token-bucket limit on HTTP(S) ingress requests:

- `requests` per `period_seconds` refill steadily. `burst` (default = `requests`) is the bucket size.
- `scope` is `ip` (default: one bucket per client IP, with IPv6 clients sharing a bucket per /64) or `endpoint` (one bucket for the whole endpoint).
- Exceeding it returns `429` with a `Retry-After` header (in seconds). Invalid values (`requests` or `period_seconds` < 1, unknown `scope`) are rejected with 400.
- It runs after `force_https` and `ip_allow`/`ip_deny` and before authentication, so credential guessing is throttled.
- TCP and TLS endpoints are not rate limited.
- With `MISHMESH_CLUSTER_ENABLED=true` the buckets live in Redis and every node shares them. If Redis errors, the limiter fails open and logs a warning. Otherwise buckets are kept per node, in memory.

Client IP for `ip_allow`/`ip_deny`/`rate_limit` and `X-Forwarded-For` is the socket peer, unless that peer is inside `MISHMESH_TRUSTED_PROXIES` (comma-separated IPs/CIDRs). In that case the rightmost `X-Forwarded-For` hop that is not itself a trusted proxy is used. Set it whenever ingress sits behind a load balancer, otherwise every client appears as the balancer's address.

`mtls`: when set, the HTTPS edge requires a client certificate that chains to `client_ca_pem` (and whose CN is in `allowed_cns`, if given), otherwise 403. Requires the HTTPS ingress (`TLS_ENABLED`).

## Custom domains (ownership verification)

Enabled by `MISHMESH_DOMAIN_VERIFICATION`. The default is `true` when `MISHMESH_SIGNUP_MODE=org` and `false` when it is `invite`; set it explicitly to override.

- **When off:** the routes below are not registered, and any well-formed hostname outside the base domain can be bound to an endpoint (homelab or single-company deployments).
- **When on:** `domain` on `POST`/`PATCH /endpoints` is accepted only if the domain is verified for the caller's org (403 otherwise). A verified domain is unique across all orgs. Unverified claims by several orgs may coexist, but only the org that proves DNS control can verify.
- **In both modes:** names equal to, or under, the base domain are rejected (400). Wildcards are not supported.

| Method | Path | Notes |
| --- | --- | --- |
| GET | `/domains` | org domains → `[domainDTO]` |
| POST | `/domains` | `{name}` → 201 `domainDTO`. Idempotent for the same org (200). 409 if another org already verified the name |
| POST | `/domains/{id}/verify` | resolves the challenge TXT record. 200 `domainDTO` when found, 422 when not, 409 if another org verified it first |
| DELETE | `/domains/{id}` | 204. 409 while an endpoint of the org is bound to it |

`domainDTO`: `{id, name, verified, verified_at?, challenge?:{type:"TXT", name:"_mishmesh-challenge.<domain>", value:"<token>"}, cname_target, created_at}`. `challenge` is present until the domain is verified.

Proof of ownership is the TXT record only. `cname_target` is the base-domain host that the domain's CNAME must point at so traffic reaches the platform. A CNAME alone is not accepted as proof, because anyone can point a CNAME at the platform.

## Quota

| Method | Path | Notes |
| --- | --- | --- |
| GET | `/quota` | → `{max_agents, max_endpoints, max_bandwidth_bytes, usage:{agents, endpoints, bandwidth_bytes}}` |
| PUT | `/quota` | `quota:write`: `{max_agents, max_endpoints, max_bandwidth_bytes}` → the GET shape. 0 means unlimited |

New orgs start from the server defaults `MISHMESH_QUOTA_MAX_*`. Quota writes are platform-operator-only in every mode: `PUT /quota` with an org session returns `403 permission denied: quota:write`, whatever the org's policy says. Org owners can still read their quota. The operator sets one org's quota with the admin bearer token: `PUT /quota?org_id=...`.

## Org & members

| Method | Path | Notes |
| --- | --- | --- |
| GET | `/orgs` | orgs the caller belongs to → `[{id, name, created_at}]` |
| GET | `/orgs/{id}` | members only, else 404 |
| POST | `/orgs` | `{name}` → 201. Creates the org, and the caller becomes owner. A user may own at most `MISHMESH_MAX_ORGS_PER_USER` orgs (default 3); beyond that → 409. The operator token is exempt |
| GET | `/members` | current org's memberships → `[{user:{id, email, name}, role, created_at}]` |
| POST | `/members` | `{email, role}` → 201 `{id, email, role, invited_by, created_at, expires_at, invite_token, invite_url}`. See below |
| GET | `/invites` | pending invites (no tokens) |
| DELETE | `/invites/{id}` | revoke → 204 |
| PATCH | `/members/{user_id}` | `{role}` |
| DELETE | `/members/{user_id}` | remove the member |

`POST /members` creates a single-use invite with a 7-day expiry:

- The raw token is returned once, and only its SHA-256 is stored.
- The role may not exceed the caller's own (403).
- `PATCH` and `DELETE /members/{user_id}` apply the same ranking (`owner` > `admin` > `member`): a caller cannot change or remove a member who outranks them, or grant a role above their own (403). An org's last owner cannot be demoted or removed (409). `PUT /policy` is owner-only (403 otherwise), whatever the org's policy says.
- Nobody is added until the invite is redeemed: through `/auth/register`, `/auth/accept-invite`, or a Google login whose `email_verified` is true.

## Audit

| Method | Path | Notes |
| --- | --- | --- |
| GET | `/audit` | the org's latest 200 events → `[{id, actor, action, target, detail, created_at}]` |

## Ops (not under /api/v1)

| Method | Path | Notes |
| --- | --- | --- |
| GET | `/healthz` | liveness, always 200 while the process runs |
| GET | `/readyz` | readiness. 503 `{status:"draining"}` after SIGTERM |
| GET | `/metrics` | Prometheus exposition (control listener, when `METRICS_ENABLED`). Requires `Authorization: Bearer` with `MISHMESH_METRICS_TOKEN` if set, else the API auth token |

## Reach-in data-plane

Registered only when `MISHMESH_REACHIN_ENABLED=true`.

| Method | Path | Notes |
| --- | --- | --- |
| POST | `/reach/{agent_id}/http` | `{target:"host:port", tls?, insecure?, method?, path?, headers?, body?}` sends one HTTP request through the agent to an allowlisted target and returns `{status, headers, body}`. The body is capped at 8 MB. If the agent cannot dial the target, the response is `502` with the agent's reason |
| GET | `/reach/{agent_id}/stream` | raw bidirectional TCP stream. See below |

The stream route takes the query `target=host:port`, plus optional `tls=true` and `insecure=true`. Send `Connection: Upgrade` and `Upgrade: mishmesh-stream`. The server answers `101 Switching Protocols`, and after that the connection carries raw bytes to and from the target. Half-close is propagated in both directions: closing the write side of your socket half-closes the target, and the target's close ends your read side.

Both routes need `agent:write` in the agent's org. An agent in another org returns `404`, and an agent that is not connected returns `502 agent offline`. Targets are subject to the agent-side allowlist (deny-first; loopback, link-local and metadata addresses are always denied). Raw stream example against the control listener:

```bash
{ printf 'GET /api/v1/reach/ag_123/stream?target=db.internal:5432 HTTP/1.1\r\n'
  printf 'Host: 127.0.0.1:8081\r\n'
  printf 'Authorization: Bearer %s\r\n' "$MISHMESH_API_AUTH_TOKEN"
  printf 'Connection: Upgrade\r\nUpgrade: mishmesh-stream\r\n\r\n'
  cat
} | nc 127.0.0.1 8081
```
