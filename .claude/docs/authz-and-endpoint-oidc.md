# Authz (Cedar) + Per-Endpoint OIDC — design & plan

Status: BOTH features IMPLEMENTED + tested. `make check` green (fmt/vet/race). Web builds (tsc+vite).

## Progress log
- Feature A done: `internal/authz` (cedar-go v1.0.0, embedded `policy.cedar`, `Authorize`, `CompileMatrix`, `ProbeMatrix`); `store.OrgPolicy` + `GetOrgPolicy`/`SetOrgPolicy` (sqlite+postgres, `org_policies` table); API `require(action,…)` replaced `guard`/`writeGuard`; every route mapped to an action; `GET/PUT /api/v1/policy`; per-org authorizer cache invalidated on PUT. Member read-only by default. Tests: `internal/authz/*_test.go`, `controlplane/authz_routes_test.go`, sqlite policy round-trip.
- `authz.New(src)` compiles src exactly (empty=deny-all); `authz.Default()` is the only path to the embedded default. authorizerFor: ErrNotFound→default, row→New(src).
- Feature B done: `internal/ingress/oidc.go` (`oidcGate`: discovery+JWKS cache, signed state w/ nonce cookie, signed endpoint session cookie, allowlist) + `oidc_jwt.go` (hand-rolled RS256 verify, JWKS→rsa.PublicKey). Callback `/_mishmesh/oidc/callback` intercepted in `ServeHTTP`; `applyPolicyGate` takes `*oidcGate` (nil→503 fail-closed). Config `ENDPOINT_OIDC_KEY` (or derived from API_AUTH_TOKEN). Tests: `oidc_test.go` (state/session round-trip+tamper+expiry, RS256 verify valid/expired/wrong-aud/wrong-iss/forged, full mock-IdP callback flow, allowlist deny).
- UI done: `Policy`/`PolicyUpdate` types, `usePolicy`/`useUpdatePolicy` hooks, Permissions matrix card on Settings (role×action switches, owner/admin-editable). OIDC editor already existed in EndpointDetail; no "not available" text in UI (was server-only 503).

## Decisions (LOCKED)
- owner == admin (identical permission sets).
- empty allowed_emails+allowed_domains = allow-any-verified-identity.
- raw-Cedar textarea editor: deferred (matrix only for now).
- email_verified required for OIDC pass.

## Decisions

- **Control-plane authz → Cedar via `github.com/cedar-policy/cedar-go` (v1.0.0, official, pure Go, in-process).**
  Rejected Cerbos (natural shape is a sidecar PDP service — fights mishmesh's single-binary design) and OPA (heavy, Rego, sidecar). Cedar embeds with no extra process, no CGO, purpose-built for RBAC+ABAC.
- **Per-endpoint OIDC → hand-rolled OAuth2 on `net/http`**, reusing the pattern already in `internal/controlplane/auth.go` (googleStart/googleCallback). No new dependency.
- Roles are NOT deleted — they become the *subject* of default Cedar policies. "member read-only" becomes a shipped default policy instead of a hardcoded `if role == ...`.

## Two distinct policy concepts (keep separate)

1. **Endpoint policy** (`store.EndpointPolicy`) — per-tunnel request gating (auth/IP/headers). Exists. OIDC field is config-only today (ingress returns 503 stub at `internal/ingress/policy.go:52`).
2. **Control-plane authz** — who can do what in an org. This is the "roles" rework → Cedar.

## Feature A — Cedar control-plane authz

### Model
- **Actions** (fixed vocabulary): `agent:read|write`, `endpoint:read|write`, `quota:read|write`, `member:read|manage`, `audit:read`, `status:read`, `policy:read|write`.
- **Principal**: the authenticated user, carrying their org role as a Cedar group/attribute.
- **Resource**: org-scoped resource type (`Agent`, `Endpoint`, `Quota`, `Member`, `Org`).
- **Default policies** (shipped, embedded Cedar): owner→all, admin→all, member→`*:read` only.

### Pieces
- `internal/authz/` — wraps cedar-go: load embedded default policies + per-org overrides, expose `Authorize(ctx, principal, action, resource) bool`.
- Store: new `DataStore` methods + table `org_policies(org_id, cedar_src, updated_at)`; nil → defaults. Keep behind the store interface (sqlite + postgres + memory).
- API: replace `guard`/`writeGuard` call sites with `requirePermission("agent:write")` etc. Map every route → action. `writeGuard` (added this session) is the interim and gets superseded.
- New routes: `GET/PUT /api/v1/policy` (view/edit org policy) gated by `policy:write`.
- UI: structured permission matrix (role × action checkboxes) on Settings/Members; compiles to Cedar. Advanced "raw Cedar" textarea later.

### Auth-disabled dev mode
`MISHMESH_API_AUTH_DISABLED=true` resolves role=owner → all actions allowed. Local tour stays unaffected.

## Feature B — Per-endpoint OIDC enforcement

Replace the 503 stub in `internal/ingress/policy.go` with a real gate.

### Scope: one generic OIDC path, Google as first issuer
Do NOT build per-provider integrations. Implement the **standard OIDC authorization-code
flow driven by issuer discovery** — Google is just a configured issuer
(`https://accounts.google.com`). Any compliant IdP (Okta, Auth0, Azure AD, Keycloak)
then works by setting `issuer` + `client_id` + `client_secret` only — config, not code.
Keep a small `Provider` seam (resolve discovery doc + JWKS for an issuer) so a future
non-discovery IdP can be slotted in, but ship exactly one code path now.

### Flow (per OIDC-enabled endpoint)
1. Request hits endpoint, no valid endpoint-session cookie → 302 to provider `authorization_endpoint` with `state` (signed, carries endpoint id + return path) + `redirect_uri` = ingress callback.
2. Ingress callback route (`/_mishmesh/oidc/callback`) exchanges code → ID token; verify signature via provider JWKS; check `allowed_emails` / `allowed_domains`.
3. On success set a signed, endpoint-scoped session cookie (HttpOnly, Secure when https); redirect to original path.
4. Subsequent requests: valid cookie → pass through `applyPolicyGate`.

### Pieces
- OIDC discovery (`/.well-known/openid-configuration`) + JWKS fetch/cache. Hand-rolled (mirror auth.go) or minimal helper.
- Cookie signing key from config (`MISHMESH_OIDC_COOKIE_KEY` or derive from existing secret).
- Ingress needs its public base URL for `redirect_uri` (config; keep separate from bind addr per project rule).
- Google = OIDC with issuer `https://accounts.google.com`.

### Security checklist (verify before done)
- State param signed + single-use (CSRF on the OAuth flow).
- ID token: verify `iss`, `aud`, `exp`, signature against JWKS.
- Cookie: HttpOnly, SameSite, Secure on https, scoped per endpoint, signed/expiring.
- allowed_emails/allowed_domains enforced server-side; empty list semantics defined (deny-all vs allow-all — decide).

## Sequencing
1. Cedar authz foundation (`internal/authz` + defaults + tests) — no behavior change yet.
2. Wire routes to permissions; make member read-only the default. Verify with token tests.
3. Policy view/edit API + UI matrix.
4. Endpoint OIDC: discovery/JWKS helper → callback route → cookie → gate. TDD around token verification + allowlist.
5. UI: OIDC editor already exists; just remove "not available" implication once enforced.

## Tests
- Cedar: table-driven authorize cases per role × action; default-policy snapshot.
- Routes: member token → 403 on writes, 200 on reads; owner/admin → 200.
- OIDC: state sign/verify, ID-token validation (valid/expired/wrong-aud), allowlist allow/deny, cookie round-trip.

## Open decisions
- Owner vs admin: any owner-only actions, or identical? (Today identical.)
- Empty allowed_emails/allowed_domains = allow-any-authenticated vs deny-all.
- Raw-Cedar editing in UI now or later (recommend later).
