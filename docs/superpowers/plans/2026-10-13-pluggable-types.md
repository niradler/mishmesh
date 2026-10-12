# Pluggable Types Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ship typed connections, descriptor-driven service drivers, and service requirements through PR, merge, release `v0.1.0-beta.5`, lab migration, and verified end-to-end traffic.

**Architecture:** Keep provider and driver behavior behind registries; expose shared field descriptors to a generic React renderer. Persist connection identity, sealed secrets, service config/readiness, and an org-scoped requirement graph in both stores. Preserve the current Cloudflare reconciler and service singleflight behavior while replacing their hardcoded selection paths.

**Tech Stack:** Go 1.26.9, SQLite/modernc, Postgres/pgx, React 19, TypeScript, Vite, Tailwind, TanStack Query, `tailscale.com/client/local`, Kubernetes `client-go`, Docker, kind, Python regression scripts.

**Spec:** `docs/superpowers/specs/2026-10-13-pluggable-types-design.md`, approved by Nir on 2026-10-13 at `1a913c3`. Its draft/open-review wording is historical; the session explicitly records approval.

## Global Constraints

- Connections: `cloudflare | tailscale | kubernetes`; roles `access | runtime`; scopes `public | tailnet | ""`.
- Service drivers: Docker, Compose, Command (rename `exec` to `command`), Kubernetes.
- `connection:manage` replaces `cloudflare:manage`; connection reads require `endpoint:read`; `host:exec` remains an additional restriction.
- No compatibility API. `/api/v1/cloudflare*` and `policy.public_access` disappear after migration.
- Cloudflare imported/managed behavior, including unclaimed/conflicting routes, stays intact.
- Tailscale is tailnet-only, uses LocalAPI, requires reachable tailscaled ≥ 1.86, a tagged node, and Service-host approval. Funnel is excluded. Docker stays local-only.
- Kubernetes supports `in_cluster | kubeconfig`, Deployment and StatefulSet scale subresources, and refuses exec credential plugins without `MISHMESH_HOST_COMMANDS` plus `host:exec` authorization.
- Connection secrets are sealed, never returned. Responses expose `has_<secret>` flags. Command environment values are redacted.
- Requires are same-org sets; reject unknown services, self-reference, and cycles. Requirements wake before their dependent, stay awake while held, and release when the dependent stops.
- Readiness is TCP or HTTP and belongs to the service. An empty setting uses the linked endpoint's first target as today.
- Confirm with Nir before changing lab sudoers or `MISHMESH_HOST_COMMANDS`. Nir installs and joins Tailscale himself. Kubernetes is proven on a throwaway kind cluster.
- Follow `CLAUDE.md`: no new code comments, bind dev listeners to `127.0.0.1`, explicit shell deadlines, no AI attribution.
- Preserve the original checkout's uncommitted Network layout fix and browser artifacts. Task17 carries the requested bounded full-screen layout into the feature worktree as reviewed topology work, without staging the original checkout.
- For destructive commands, print the exact command and effect and ask Nir to run it, unless he explicitly authorizes the agent to run it. This includes kind deletion, regression fixture deletion, and rollback/restoration. Prepare scripts with opt-in cleanup; do not silently execute their cleanup on the lab.
- Nir delegated the execution method and authorized implementation; subagent-driven execution is active. Explicit lab gates above remain required.

## Review Focus

1. A corrupt/missing secret key or malformed legacy row must abort migration atomically and leave the old data usable (Task 3).
2. Cross-org references and concurrent graph/hostname updates must fail without leaking identifiers or committing a cycle/alias collision (Tasks 2, 6, 12, 14).
3. Shared/diamond requirements, canceled waits, failed starts/stops, and reloads must not leak holds or stop a still-needed service (Task 7).
4. A managed Tailscale sync must preserve foreign Serve entries and ownership across restart, including concurrent external edits (Task 11).
5. Secret-bearing edits and kubeconfig credential plugins must not disclose credentials or execute commands through Detect, Sync, refresh, or a transitive service wake (Tasks 10, 12, 13, 15).

## Execution boundaries and decisions

This is one coordinated migration/release plan with separately reviewable tasks. Splitting providers and Requires into separate releases is possible, but would require multiple migrations and would defer the approved beta.5 scope; this plan keeps one release and explicit interface dependencies.

The following are implementation choices, not changes to the approved product behavior:

- Use `idle_timeout_seconds = -1` for **With dependents**, `0` for **Never**, positive values for timed idle. Migrate existing zero values unchanged. UI defaults to -1 when creating a service intended only as a requirement; do not change existing timeout values when attaching requirements.
- Keep commands as argv lists, not shell strings. Descriptor `list` fields for Start/Stop/Status explain “one argument per row.” Use existing `dir` and `env` keys to preserve migration fidelity.
- Add descriptor metadata `detect`, `default`, and `sensitive` so capability, defaults, and redaction are generic. `sensitive` covers key-value environment values as well as secret fields.
- Load the existing secret box before store migration using explicit `OpenWithBox` constructors; retain `Open` for fresh test stores without legacy encrypted rows. Do not silently generate a replacement key for encrypted legacy data.
- Persist effective ingress aliases in a derived `endpoint_aliases` index, updated transactionally with endpoint access or connection changes. Per-connection hostname uniqueness is required by the spec; also reject ambiguity between effective ingress hosts and canonical domains across orgs.
- Keep ownership for Tailscale managed resources in provider-owned persisted metadata under `config.owned_services`; client input cannot set or overwrite this field. Read/write Serve config with ETag conflict retry and a per-local-socket lock. Do not claim an existing foreign Service solely because its name matches.
- Runtime holds are not DB rows. Rebuild them during startup/refresh for running dependents before idle processing. Separate requirement holds from traffic activity so releasing a dependency does not manufacture recent own traffic.
- The UI's “Stop <dependent> too” submits explicit reverse-order stop requests after confirmation; no implicit cascade in the existing stop route. Keep the requirement's 409 guard server-side throughout.
- Register concrete implementations only in `cmd/mishmesh-server/network.go`. Core registries do not import their provider implementations.

## Execution order

Execute tasks in this dependency order: **1, 2, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 3, 15, 16, 17, 18, 19, 20**. Task numbers remain stable for report/ledger identity. Task 3 is the atomic physical-schema cutover after all Go runtime/API consumers are migrated; this avoids building temporary encrypted Cloudflare table emulation just to delete it later. Task 4 produces the pure legacy service conversion helper for transitional record loading. No intermediate checkpoint is deployed.

## File and responsibility map

| Area | Files | Responsibility |
|---|---|---|
| Shared descriptors | new `internal/typedfields/{fields,validate,redact}.go` | wire field shapes, defaults, host markers, strict JSON validation/redaction |
| Connection contracts | new `internal/connections/{provider,registry,hub,secrets,aliases}.go` | type lookup, instance lifecycle, sealed config, effective hostnames |
| Cloudflare adapter | new `internal/cloudflare/provider.go`; existing `config.go`, `source.go`, `publisher.go`, `hub.go` | preserve reconciler, scope it per connection |
| Tailscale provider | new `internal/connections/tailscale/{provider,local,api,reconcile}.go` | LocalAPI/API adapters, ownership, reconcile and fixes |
| Kubernetes provider | new `internal/connections/kubernetes/{provider,config,client}.go` | guarded config loading, client resolution, status and permissions |
| Storage | `internal/store/store.go`, both `network.go`; new both `pluggable_migrate.go`, `aliases.go` | records, relational requirements/aliases, atomic legacy migration |
| Drivers | new `internal/services/{registry,driver_types,kubernetes_driver}.go`; existing `spec.go`, `docker_driver.go`, `exec_driver.go` | type-owned configs, descriptors, reusable existing driver behavior |
| Service lifecycle | new `internal/services/{readiness,requirements}.go`; existing `manager.go` | readiness, DAG validation, wake order, hold ownership |
| API | new `internal/controlplane/network_connections.go`; existing `network.go`, `network_services.go`, `api.go`, `endpoints.go`, `topology.go` | scoped DTOs, authz, reference guards, new routes |
| Routing/authz | `internal/ingress/{cache,ingress}.go`, `internal/gateway/gateway.go`, `internal/authz/{authz.go,policy.cedar}` | alias resolution, cache invalidation, protected control-plane fields |
| Startup | `cmd/mishmesh-server/{main,network,token}.go` | load key before migration, register types, shutdown ordering |
| UI generic fields | new `web/src/components/types/{TypePicker,DescriptorFields,DescriptorDetails}.tsx`; new `web/src/lib/typed-fields.ts` | render field metadata without type switches |
| UI connections/access | new `web/src/pages/{Connections,ConnectionDetail}.tsx`; new `web/src/components/connections/{ConnectionForm,HostnamesTable,ConnectionTypePanel}.tsx`; new endpoint `AccessSection.tsx` | generic connections and per-ref Access flows |
| UI service/map | existing service components/pages, `TopologyMap.tsx`, API types/hooks, glossary, App/Sidebar | descriptor-driven drivers, Requires, type connections and edges |
| Regression/release | `.claude/scripts/lab-e2e.py`, `cf-import.py`, `import-services.py`, `lab-endpoints.py`; new `.claude/scripts/pluggable-types/`; README, CHANGELOG, Helm chart metadata | reproducible checks, migration rehearsal, release/lab handoff |

Create matching `_test.go` files beside new Go units. Extend existing network/API/manager/provider tests rather than replacing their behavior assertions. Each task updates `.claude/docs/pluggable-types.md` after its passing verification chunk.

## Common test and commit discipline

For every implementation task: add the listed failing tests, run the focused suite to see the failure, implement, format, rerun focused tests, compile affected consumers, then commit only that task's files. Do not suppress a failing baseline test. Test fixtures/fakes are for deterministic external interfaces; real integration gates remain mandatory.

On Linux run focused suites as `go test -race -count=1 -timeout=180s <packages>`. On Windows use the existing Docker runner:

```powershell
pwsh -NoProfile -File .claude/scripts/test-go.ps1 -GoTestArgs @('-race','-count=1','-timeout=180s','./internal/typedfields')
```

Apply an outer process deadline (600s for focused Go suites, 1800s for the full suite, 300s for web checks/builds) using the execution harness or a bounded process wrapper. The existing `test-go.ps1` has the Go test timeout but no outer Docker startup deadline; add an optional process deadline in Task 19 without changing its result handling. Ordinary Git/read commands get 30s deadlines. Never start an unbounded polling loop.

Use explicit path lists for `git add`, then `git commit -m '<task message>'`. No `git add .`, resets, deletes, forced pushes, or automatic cleanup. The snippets below show contract/test anchors; imports belong at file top, and all prose-listed assertions are required alongside them.

### Task 1: Shared field descriptors with strict validation and redaction

**Files:** Create `internal/typedfields/fields.go`, `validate.go`, `redact.go`, `fields_test.go`.

**Interfaces:** Produce the shared wire type and `Validate(fields []Field, raw json.RawMessage) error`, `UsesHostCommands(fields []Field, raw json.RawMessage) (bool, error)`, `Redact(fields []Field, raw json.RawMessage) (json.RawMessage, error)`.

- [x] Add failing tests for all eight kinds, missing required values, wrong types, trailing JSON, unknown keys, empty host-command fields, numeric bounds/defaults, and sensitive key-value redaction. Anchor:

```go
func TestShouldRejectUnknownField(t *testing.T) {
    fields := []Field{{Key: "project", Label: "Project", Kind: "text", Required: true}}
    if err := Validate(fields, json.RawMessage(`{"project":"lab","bin":"sh"}`)); err == nil {
        t.Fatal("unknown field accepted")
    }
}
```

- [x] Run `./internal/typedfields`; expect undefined `Field`/`Validate` first.
- [x] Implement the contract below, strict one-value decoding, typed empty/default handling, and secret redaction without mutating caller data:

```go
type Field struct {
    Key string `json:"key"`
    Label string `json:"label"`
    Kind string `json:"kind"`
    Options []string `json:"options,omitempty"`
    ConnType string `json:"conn_type,omitempty"`
    Required bool `json:"required"`
    Placeholder string `json:"placeholder,omitempty"`
    Help string `json:"help,omitempty"`
    HostCommand bool `json:"host_command,omitempty"`
    Sensitive bool `json:"sensitive,omitempty"`
    Default json.RawMessage `json:"default,omitempty"`
}
```

- [x] Run focused tests and `go vet ./internal/typedfields`; expected pass. Commit `feat: add shared type field descriptors`.

### Task 2: Connection, service, requirement, and alias store contracts

**Files:** Modify `internal/store/store.go`, `internal/store/sqlite/network.go`, `internal/store/postgres/network.go`; create both `aliases.go`; extend both `network_test.go`.

**Interfaces:** Produce `store.Connection`, updated `store.Service`, `store.AccessRef`, and the six connection CRUD methods from the spec. Service Create/Update/Get/List include `Requires []string` and internal derived `ConnectionIDs []string`; writes replace these sets atomically in `service_requires` and `service_connections`. ConnectionIDs are populated from descriptor connection fields, never guessed from JSON key names; they are not added to service API bodies/DTOs. Produce `GetEndpointByAccessHostname(ctx context.Context, hostname string) (*Endpoint, error)` and `ReplaceConnectionAliases(ctx context.Context, orgID, connectionID string, aliases []EndpointAlias) error`; `EndpointAlias` has `Hostname, EndpointID, ConnectionID, OrgID string`. Alias writers must validate ownership, not trust these fields.

- [x] Add round-trip tests for multiple same-type connections in an org, duplicate org/name, same name in another org, sealed blob bytes, flattened config/readiness, requirement set replacement, rollback on invalid FK, and org-scoped alias lookup/collision. Anchor using the existing `newStore(t)` helper in each backend:

```go
func TestShouldKeepMultipleConnectionsOfOneType(t *testing.T) {
    s := newStore(t)
    ctx := context.Background()
    now := time.Now().UTC()
    if err := s.CreateOrg(ctx, &store.Org{ID: "org_types", Name: "types", CreatedAt: now}); err != nil { t.Fatal(err) }
    for _, name := range []string{"lab", "backup"} {
        c := &store.Connection{ID: store.NewID("conn"), OrgID: "org_types", Type: "cloudflare", Name: name, Config: json.RawMessage(`{}`), CreatedAt: now, UpdatedAt: now}
        if err := s.CreateConnection(ctx, c); err != nil { t.Fatal(err) }
    }
    rows, err := s.ListConnections(ctx, "org_types")
    if err != nil || len(rows) != 2 { t.Fatalf("rows=%d err=%v", len(rows), err) }
}
```

- [x] Run `./internal/store/sqlite ./internal/store/postgres`; expect missing connection methods. The Postgres gate later must run with its DSN set, not count skips as evidence.
- [x] Implement `Connection{ID, OrgID, Type, Name string; Config json.RawMessage; SecretsEnc []byte; CreatedAt, UpdatedAt time.Time}`. Add service `Config, Ready json.RawMessage; Requires []string` and policy `Access []AccessRef`, where `AccessRef{ConnectionID, Hostname string}` has snake-case JSON tags. Keep `Spec` and `PublicAccess` only as temporary in-branch adapter fields until their consumers move in Tasks 4/13/14. Create the approved tables and derived alias table with unique effective hostnames and composite ownership/FK constraints.
- [x] Keep the legacy table layout operational for this preparatory commit; add nullable new service columns and new tables without running the destructive data conversion. Store read/write adapters select the representation that the caller actually supplied, so old tests/consumers remain equivalent. New service tests use the new shape. Use transactions for requirements, alias writes, reference guards, and graph writes; do not rely on an API-only process mutex for concurrency across Postgres instances. Temporary adapters are removed at the Task 3 cutover after Task 14; none ship in beta.5.
- [x] Run store suites plus `go test -run '^$' ./... -timeout=180s`, fixing affected compile fixtures with mechanically equivalent shapes. Commit `feat: persist typed connections and service requirements`.

### Task 3: Atomic legacy migration with the existing secret key

**Files:** Create both stores' `pluggable_migrate.go` and `pluggable_migrate_test.go`; modify both store constructors/migration hooks; extend `internal/store/pluggable.go`, `pluggable_test.go` from Task 4; modify `cmd/mishmesh-server/main.go`, `network.go`, `token.go`.

**Interfaces:** Consume the pure `store.MigrateService` helper from Task 4. Produce `sqlite.OpenWithBox(dsn string, box *secrets.Box) (*Store, error)`, `postgres.OpenWithBox(dsn string, pool PoolConfig, box *secrets.Box) (*Store, error)`, and shared `store.MigrateService(driver string, raw json.RawMessage) (newDriver string, config, ready json.RawMessage, err error)`. Existing `Open` delegates with nil box and errors for encrypted legacy rows. Add `openDataStoreWithBox(cfg config.Server) (store.DataStore, *secrets.Box, error)` for serve startup; preserve `openDataStore(cfg)` as a wrapper for token/tests. The serve path passes the returned box to `newNetworkControl` instead of creating another. Read the existing key before migration. When the key is missing, inspect whether the existing DB has encrypted legacy/current connection rows before allowing key creation; a missing key with existing ciphertext must not create a replacement file.

- [ ] Add failing migrations from literal beta.4 schema/fixtures: one imported Cloudflare row, 12 public endpoints, eight Docker/Compose/exec services, populated secrets, and custom authz matrix/Cedar source. Add fresh DB, reopen/idempotence, concurrent Postgres open, wrong/missing key, malformed JSON, transaction rollback, and no plaintext persistence tests. Anchor the shared converter:

```go
func TestShouldMigrateExecWithoutChangingArgv(t *testing.T) {
    driver, config, ready, err := MigrateService("exec", json.RawMessage(`{"exec":{"start":["sudo","systemctl","start","gdm"],"stop":["sudo","systemctl","stop","gdm"]},"ready_tcp":"127.0.0.1:3389"}`))
    if err != nil { t.Fatal(err) }
    if driver != "command" || !bytes.Contains(config, []byte(`"sudo"`)) || string(ready) != `{"tcp":"127.0.0.1:3389"}` { t.Fatalf("%s %s %s", driver, config, ready) }
}
```

- [ ] Run `./internal/store/... ./cmd/mishmesh-server`; expect migration tests to fail against the legacy tables.
- [ ] Implement one transaction per backend: inspect old schema, allocate/persist one `conn_` ID per migrated org, read old token ciphertext with `cloudflare:<org>:<kind>` AAD, seal one JSON object using `connection:<org>:<id>` AAD, rewrite endpoint access refs, flatten driver config, split readiness, preserve IDs/timestamps/timeouts, and replace old structures only after all conversion succeeds. Run migration under the existing Postgres advisory lock. Fresh installs must not recreate old tables on every startup.
- [ ] Migrate saved authorization actions as parsed policy/matrix action names (`cloudflare:manage` → `connection:manage`), not an arbitrary text replacement of unrelated strings. Invalid stored policy conversion aborts; no silent default-permission widening.
- [ ] With runtime consumers converted by Tasks 4–14, remove transitional legacy record fields/adapters and old physical structures in this cutover. Test `token create` against an already migrated and a legacy DB; no daemon boot is required just to preserve the correct key path. Missing key with nonempty legacy encrypted blobs must fail without creating a new file. Reopen and decrypt the new blob under its connection ID; decrypting under another org/ID must fail.
- [ ] Run migration suites plus compilation of all packages. Commit `feat: migrate legacy network records atomically`.

### Task 4: Register Docker, Compose, and Command drivers

**Files:** Create `internal/services/registry.go`, `driver_types.go`, `registry_test.go`, `readiness.go` (types only at this step), `internal/store/pluggable.go`, `pluggable_test.go`; modify `spec.go`, `manager.go`, `exec_driver.go` and affected tests; consume unchanged `docker_driver.go`/Docker tests where existing constructors already preserve behavior; modify `internal/controlplane/network_services.go`, its regression tests, `cmd/mishmesh-server/network.go`, `go.mod`, `go.sum`.

**Interfaces:** Produce `NewRegistry() *Registry`, `DriverDescriptor{Type, Label, Help string; Fields []typedfields.Field}`, `DriverType` from the spec, `Registry.Register(DriverType) error`, `Registry.Lookup(string) (DriverType, bool)`, `Registry.Descriptors() []DriverDescriptor`. `Deps` carries the definition's `OrgID string`, lazy Docker access and `KubernetesClient(ctx context.Context, orgID, connectionID string) (kubernetes.Interface, error)` through an injected resolver; keep type contracts out of provider implementations. `Definition` uses `Config json.RawMessage`, `Ready Readiness`, `Requires []string`, and existing identity/timeouts. Define `Readiness{TCP string; HTTP *HTTPReadiness}` and `HTTPReadiness{URL string; Status int}` with the spec's JSON tags here; Task 5 adds decoding and HTTP probes. Produce pure `store.MigrateService(driver string, raw json.RawMessage) (newDriver string, config, ready json.RawMessage, err error)` here and use it when FromRecord sees a legacy Spec with absent Config; existing legacy API adapters stay until their consumers move. Pin mutually compatible Kubernetes API/apimachinery/client-go versions here so the resolver's type compiles; Task 10 adds the implementation, not a missing forward dependency.

- [x] Add tests for duplicate/unknown registrations, deterministic descriptors, valid/invalid config for each driver, and host markers for Command start/stop/status plus Compose file/bin. Preserve the existing Compose-without-file Docker API path. Anchor:

```go
func TestShouldRejectUnknownDriver(t *testing.T) {
    registry := NewRegistry()
    if _, ok := registry.Lookup("remote-docker"); ok { t.Fatal("unknown driver registered") }
}
```

- [x] Run `./internal/services`; expect undefined registry constructor.
- [x] Implement `NewRegistry() *Registry`, descriptors and `Validate`/`New` methods for each concrete driver type. Replace `hostDrivers.build`'s switch with lookup. Keep lazy Docker creation so Command/Kubernetes do not need a local daemon. Rename public driver ID to `command`, retaining existing argv execution behavior internally.
- [x] Remove captured command stdout/stderr from error strings that reach LastError/logs; preserve internal bounded output return and errors.As/Is exit/context causes. Test a helper printing configured/inherited secrets before failure and verify no public status/error/log leakage.
- [x] Use shared metadata for host checks; check explicit input before normalization and defaults. Capture marked raw legacy API input before Normalize so no-file bin/whitespace cannot disappear before authorization. Compose custom bin is a host command even if a file was omitted; never normalize away a value before enforcing permission.
- [x] Run the existing Docker/exec manager tests adapted to equivalent configs and all focused registry tests. Commit `refactor: build services through driver registry`.

### Task 5: Generic TCP and HTTP readiness

**Files:** Extend `internal/services/readiness.go`; create `readiness_test.go`; modify `spec.go`, `manager.go`, `manager_test.go`; extend targeted `internal/controlplane/network_test.go`, `internal/health/health_test.go`, `internal/ingress/wake_test.go` fixtures/regressions as required.

**Interfaces:** Produce `Readiness{TCP string; HTTP *HTTPReadiness}`, `HTTPReadiness{URL string; Status int}` with lower-case JSON tags; `DecodeReadiness(json.RawMessage) (Readiness, error)`, `ValidateReadiness(Readiness) error`. Preserve `EnsureRunning(ctx, id, readyAddr)` so ingress/health consumers need no unrelated redesign. Add `Options.HTTPClient *http.Client` for deterministic probes.

- [x] Add tests for exclusive TCP/HTTP, malformed host/port, invalid schemes/status, exact HTTP status, retry/timeout, cancellation, empty fallback to endpoint target, and no target falling back to driver state. Anchor:

```go
func TestShouldRejectTwoReadinessModes(t *testing.T) {
    if err := ValidateReadiness(Readiness{TCP: "127.0.0.1:8080", HTTP: &HTTPReadiness{URL: "http://127.0.0.1:8080/", Status: 200}}); err == nil { t.Fatal("ambiguous readiness accepted") }
}
```

- [x] Run `./internal/services`; expected missing readiness types.
- [x] Implement strict decoding and bounded probes. HTTP uses per-request context, closes response bodies, does not follow redirects to make a configured status appear successful, and does not send credentials to a redirected host. Validate numeric TCP port and HTTP status in 100–599; use status 200 as the UI/API default.
- [x] Fix the observed TCP hold/read cancellation issue: a canceled bounded probe must close its connection and return the context cause instead of succeeding after the hold; test a real TCP peer and goroutine cleanup. Keep the existing shared wake call independent of an individual request's canceled wait; driver start/readiness still have bounded lifetime. Seed durable same-org linked service records in older health/ingress fixtures exposed by Task2 validation, preserving their fake runtime behavior. Run `./internal/services ./internal/health ./internal/ingress`. Commit `feat: support service-owned tcp and http readiness`.

### Task 6: Persist and validate an org-scoped Requires DAG

**Files:** Create `internal/services/requirements.go`, `requirements_test.go` and shared `internal/store/requirements.go`/tests; modify `DataStore` and both stores' `network.go`/tests. Legacy API integration remains Task13.

**Interfaces:** Produce `ValidateRequirements(orgID, serviceID string, requires []string, services []*store.Service) error`; service stores enforce the same graph invariant within the transaction that replaces the set. Produce `ListServiceDependents(ctx context.Context, orgID, serviceID string) ([]*Service, error)` on `DataStore` for delete/stop guards and detail DTOs.

- [x] Add empty/set deduplication, missing ID, cross-org, self-cycle, long-cycle, diamond-valid, update-removes-old-edge, and concurrent opposing updates tests. Anchor:

```go
func TestShouldRejectCycleOnUpdate(t *testing.T) {
    graph := []*store.Service{{ID: "a", OrgID: "org", Requires: []string{"b"}}, {ID: "b", OrgID: "org"}}
    if err := ValidateRequirements("org", "b", []string{"a"}, graph); err == nil { t.Fatal("cycle accepted") }
}
```

- [x] Run `./internal/services ./internal/store/sqlite ./internal/store/postgres`; expect missing graph validator/dependents lookup.
- [x] Implement DFS using the prospective replacement set and same-org service snapshot, deterministic names in failures, DB FK/transaction enforcement, and the existing cross-process network-write transaction lock in each backend; reuse it for graph snapshot and replacement without a second lock order. Pin PostgreSQL network writes to ReadCommitted so a configured RepeatableRead default cannot retain a pre-lock snapshot. Verify a database-observed waiter and longer-cycle schedule with those defaults. Concurrent A→B/B→A must yield at most one success, even through two server/store instances. Delete required services returns `store.ErrConflict`, without first deleting inbound edges.
- [x] Run focused suites with the race detector; verify no cross-org service name/ID appears in client errors. Commit `feat: validate service requirement graphs`.

### Task 7: Wake requirements and hold them through the dependent lifecycle

**Files:** Modify `internal/services/requirements.go`, `manager.go`, `manager_test.go`, Definition and API `reloadServices`; create `requirements_manager_test.go`; extend the control interface in `internal/controlplane/network.go`.

**Interfaces:** Keep `EnsureRunning`, `Acquire`, `Stop` signatures. Produce `RunningDependents(id string) []Status`; typed `RequirementInUseError{Dependents []Status}` for API 409 translation. Each runtime service owns a map of idempotent release functions keyed by requirement ID.

- [ ] Write tests for ordered readiness before dependent start, requirements starting in parallel, requirement failure preventing dependent start, 25 concurrent callers, diamond/shared requirements, caller cancel, partial start/failed stop, external running services, reload/config update, and idempotent release. Use existing `fakeDriver` and `newManager`; anchor:

```go
func TestShouldNotStartDependentAfterRequirementFailure(t *testing.T) {
    required := &fakeDriver{state: Stopped, startErr: errors.New("offline")}
    dependent := &fakeDriver{state: Stopped}
    drivers := map[string]*fakeDriver{"desktop": required, "guacamole": dependent}
    manager := newManager(Options{}, func(def Definition) (Driver, error) { return drivers[def.ID], nil })
    manager.Apply([]Definition{
        {ID: "desktop", OrgID: "org", Name: "desktop-gui", StartTimeout: time.Second},
        {ID: "guacamole", OrgID: "org", Name: "guacamole", Requires: []string{"desktop"}, StartTimeout: time.Second},
    })
    err := manager.EnsureRunning(context.Background(), "guacamole", "")
    starts, _ := dependent.counts()
    if err == nil || !strings.Contains(err.Error(), `requirement "desktop-gui" failed`) || starts != 0 { t.Fatalf("starts=%d err=%v", starts, err) }
}
```

- [ ] Run `./internal/services`; expect the dependent to start despite its unprocessed Requires before implementation.
- [ ] Add requirement holds before each wake to protect the readiness/start interval. Never hold `Manager.mu` while recursing or invoking drivers. Each requirement keeps its own start deadline; start the dependent's deadline after requirements are ready. Transfer holds to the running dependent exactly once; unwind on failure. Runtime graph validation protects against corrupt stored cycles before any goroutine starts.
- [ ] Populate internal `Definition.FallbackReadyAddr` from same-org linked endpoints for dependency wakes. Probe precedence: explicit service readiness, caller endpoint target, linked fallback target, driver state. Endpoint link-refresh integration is verified in Task14.
- [ ] Separate own traffic activity from dependency holds. For -1 stop after last requirement hold disappears only if there is no active/recent own traffic; use the existing 30s idle interval as the recent-traffic grace. Positive timeout keeps timed idle behavior; zero never stops automatically. Do not release on a failed stop while the driver remains running.
- [ ] Refresh/startup must reconcile running dependents and holds before any idle pass, release holds when an externally stopped dependent is observed, and preserve unchanged holds through `Apply`. Requirement changes on a running dependent acquire/wake additions before releasing removals; failed change leaves prior runtime requirements protected and exposes an error. Manual stop cannot race with a newly acquired dependent hold.
- [ ] Run `./internal/services ./internal/ingress ./internal/health` with race checks. Commit `feat: hold service requirements while dependents run`.

### Task 8: Generic connection registry and instance hub

**Files:** Create `internal/connections/provider.go`, `registry.go`, `hub.go`, `secrets.go`, `registry_test.go`, `hub_test.go`, `secrets_test.go`; modify `cmd/mishmesh-server/network.go`.

**Interfaces:** Produce the spec's `Provider` and `Instance` interfaces, `Descriptor{Type, Label, Role, Scope, Help string; Levels []string; Fields []typedfields.Field; Detect bool}`, `Config{Record store.Connection; Secrets map[string]string; HostCommands bool}`, `HostStatus{Host, State, EndpointID, Detail, Fix string}`, `Status{Level, Health, LastError string; LastSync time.Time; Hostnames []HostStatus; Details map[string]any}` with snake-case JSON tags. `Deps` carries `Data store.DataStore`, `Origin string`, `Log *slog.Logger`, and the secret box/permission policy needed for persisted ownership and guarded runtime use. Produce registry `Register`, `Lookup`, `Descriptors` equivalents and `Hub.Start(ctx) error`, `Reload(ctx, connectionID) error`, `Sync(ctx, connectionID) (Status, error)`, `Status(connectionID) Status`, `Trigger(connectionID)`, `Close()`.

- [ ] Add duplicate/unknown type, AAD mismatch, redacted-status, independent same-org instances, failed open, sync serialization, reload during sync, bounded close, and trigger coalescing tests. Anchor:

```go
func TestShouldBindSecretsToConnectionAndOrg(t *testing.T) {
    box, err := secrets.New(make([]byte, 32))
    if err != nil { t.Fatal(err) }
    sealed, err := box.Seal([]byte(`{"api_token":"secret"}`), SecretContext("org-a", "conn-a"))
    if err != nil { t.Fatal(err) }
    if _, err := box.Open(sealed, SecretContext("org-b", "conn-a")); err == nil { t.Fatal("cross-org decrypt succeeded") }
}
```

- [ ] Run `./internal/connections`; expect missing contracts.
- [ ] Implement `SecretContext(orgID, connectionID string) string` as `connection:<org>:<id>`, bounded cancellation/draining of instances, and per-instance sync mutexes. Hub owns lifecycle/identity; provider owns remote behavior. Return a down/error status for a single bad connection without logging opened secrets; store-list failure prevents startup.
- [ ] Use the injected registry in startup but keep old Cloudflare wiring until its adapter and API replacement are ready. Run hub tests and command package compilation. Commit `feat: add typed connection instance hub`.

### Task 9: Move Cloudflare behind the connection provider interface

**Files:** Create `internal/cloudflare/provider.go`, `provider_test.go`; modify `source.go`, `config.go`, `publisher.go`, `hub.go` and their tests; modify `cmd/mishmesh-server/network.go`.

**Interfaces:** Produce `cloudflare.NewProvider(options ProviderOptions) connections.Provider`, where `ProviderOptions` contains `CloudflaredBin, BaseURL string` and injectable current client/detector dependencies. Provider config fields retain `account_id`, `tunnel_id`, `access_app_id`, `origin`, `connector`, `metrics_addr`, `prune`; secret keys retain `tunnel_token`, `api_token`. Level remains derived from existing token/connector behavior. Source is filtered by connection ID and selected access refs, not merely org ID.

- [ ] Add tests preserving Detect/imported observation, managed supervisor startup/restart, DNS/Access reconcile, pruning ownership, pending/unclaimed/conflict fixes, and two Cloudflare connections in one org. Anchor the published/unclaimed distinction:

```go
func TestShouldSelectPublicHostsPerConnection(t *testing.T) {
    ep := &store.Endpoint{ID: "ep", OrgID: "org", Domain: "notes.example.test", Policy: &store.EndpointPolicy{Access: []store.AccessRef{{ConnectionID: "conn-a"}}}}
    hosts := AccessHosts("conn-b", []*store.Endpoint{ep})
    if len(hosts) != 0 { t.Fatalf("foreign connection hosts=%v", hosts) }
}
```

- [ ] Define `AccessHosts(connectionID string, endpoints []*store.Endpoint) []Hostname` in `source.go`; this gives selected public hosts only. Retain a separate full-org observer list for unclaimed reporting so endpoints with access off still report their current external routes.
- [ ] Run `./internal/cloudflare`; expected old source ignores per-connection access.
- [ ] Adapt existing publisher/supervisor instances without duplicating their reconciliation logic. Replace old hub responsibilities with the generic hub; do not change provider ownership interpretation or enable managed level during migration. Keep unclaimed orphans unpruned. Type panel details expose only existing nonsecret facts.
- [ ] Run all Cloudflare tests, generic hub tests, and server compilation. Commit `refactor: register cloudflare as a connection type`.

### Task 10: Kubernetes runtime connection and scale driver

**Files:** Create `internal/connections/kubernetes/provider.go`, `config.go`, `client.go`, their tests; create `internal/services/kubernetes_driver.go`, `kubernetes_driver_test.go`; modify registry bootstrap, `go.mod`, `go.sum`.

**Interfaces:** Produce `kubernetesconnection.NewProvider(options Options) connections.Provider` (import alias avoids confusion with clientset package), `BuildRESTConfig(raw json.RawMessage, secretKubeconfig string, hostCommands bool) (*rest.Config, error)`, and an injected connection resolver implementing Task 4's `KubernetesClient`. Driver config is `{connection_id, namespace, kind, name, replicas}` with Kind `Deployment | StatefulSet` and replicas default 1.

- [ ] Add tests for in-cluster/kubeconfig, selected/missing context, missing namespace, exact scale permission denial, version status, forbidden exec plugin before any process runs, credential/CA redaction, Deployment/StatefulSet scaling, missing workload, partial state, N-ready-running, and zero-spec-but-nonzero-ready partial. Anchor:

```go
func TestShouldRefuseExecCredentialPluginBeforeClientConstruction(t *testing.T) {
    kubeconfig := "apiVersion: v1\nkind: Config\nclusters:\n- name: c\n  cluster:\n    server: https://127.0.0.1:6443\ncontexts:\n- name: c\n  context:\n    cluster: c\n    user: u\ncurrent-context: c\nusers:\n- name: u\n  user:\n    exec:\n      apiVersion: client.authentication.k8s.io/v1\n      command: forbidden-program\n      interactiveMode: Never\n"
    _, err := BuildRESTConfig(json.RawMessage(`{"mode":"kubeconfig","context":"c","default_namespace":"default"}`), kubeconfig, false)
    if err == nil { t.Fatal("exec plugin accepted") }
}
```

- [ ] Verify the mutually compatible `k8s.io/api`, `k8s.io/apimachinery`, `k8s.io/client-go` versions pinned in Task 4 against the workload/scale APIs used here. Run focused suite; expect missing provider/config loader.
- [ ] Parse kubeconfig without executing its plugin, reject unauthorized exec before constructing a transport, and enforce the same check on detect/sync/refresh/reload/driver resolution. No local file references from submitted kubeconfig; require embedded credentials/CA or reject with a field error so the server does not read arbitrary host files. Scope the selected namespace and derive empty driver namespace from the connection's default.
- [ ] Implement SelfSubjectAccessReview for get/patch of deployments/scale and statefulsets/scale, plus the parent workload reads actually needed for ready replicas. Patch replicas through scale with resource/version conflict handling; do not claim an API success proves readiness. Fake clientset reactors must model scale subresources explicitly rather than assuming its object tracker behaves like an API server.
- [ ] Gate create/update/delete and management actions for plugin-backed connections with `hostAccess`; the runtime gate checks the stored authorized config and current server flag. Service wake traversing into a plugin-backed requirement must not bypass permission checks in the API. Ingress uses only previously authorized stored definitions.
- [ ] Run `./internal/connections/kubernetes ./internal/services ./cmd/mishmesh-server`; cross-compile Linux/Darwin/Windows amd64 and arm64 with `CGO_ENABLED=0`. Commit `feat: add kubernetes runtime connections and services`.

### Task 11: Tailscale LocalAPI/API access provider

**Files:** Create `internal/connections/tailscale/{provider,local,api,reconcile}.go`, their tests; modify registry bootstrap, `go.mod`, `go.sum`.

**Interfaces:** Produce `tailscaleconnection.NewProvider(options Options) connections.Provider`. Inject the LocalAPI and HTTP adapters rather than shelling out. Config fields: `level`, `socket`, `tailnet`, `service_tags`; secrets `oauth_client_id`, `oauth_client_secret`. Provider-owned `owned_services` maps `svc:` names to owning endpoint IDs and cannot be supplied by clients.

- [ ] Add tests for imported zero-writes, managed HTTPS 443 mapping, default service name, socket-down, old version, untagged node, approval pending, OAuth/API failure, foreign entry preservation, ownership persistence, deleted ref cleanup, ETag retry, and two connections to one socket. Anchor the pure reconciliation contract:

```go
func TestShouldPreserveForeignServeEntries(t *testing.T) {
    current := map[string]string{"svc:foreign": "http://127.0.0.1:9000", "svc:notes": "http://127.0.0.1:8090"}
    next, err := ReconcileServices(current, map[string]string{"svc:notes": "ep-notes"}, map[string]string{"svc:notes": "http://127.0.0.1:8090", "svc:wiki": "http://127.0.0.1:8090"})
    if err != nil || next["svc:foreign"] != current["svc:foreign"] { t.Fatalf("foreign entry changed: %v %v", next, err) }
}
```

- [ ] Produce `ReconcileServices(current, owned, desired map[string]string) (map[string]string, error)` in `reconcile.go`, using the map anchor to test identity/ownership decisions before adapting actual `ipn.ServeConfig`. Run `./internal/connections/tailscale`; expected missing reconcile/provider.
- [ ] Pin a compatible `tailscale.com` module. Use LocalAPI status plus Serve config get/set, preserving returned ETag; lock by socket, reread on conflicts, retry at most three times, then expose a degraded status. Use bounded OAuth client-credentials requests, cache token until expiry, and never place tokens in errors/status.
- [ ] Define managed Services with the authenticated Tailscale API; persist ownership only for entries this connection successfully creates. On an existing foreign resource, report conflict, do not overwrite. Remove only owned resources no longer referenced, preserving foreign Serve, Funnel, node-level, and service entries. Ordinary Close cancels work; it does not erase published configuration.
- [ ] Preserve original Host to ingress and return effective aliases only from observed connection status. Report pending approval/access-policy prerequisites instead of published. Imported mode never writes either API. Test API and LocalAPI with HTTP fake servers, not fake successful callbacks.
- [ ] Run provider/registry suites, dependency vulnerability checks, and cross-platform compilation. Commit `feat: add tailscale tailnet access connections`.

### Task 12: Generic connection API, authz, and reference guards

**Files:** Create `internal/controlplane/network_connections.go`, `connections_test.go`; modify `api.go`, `network.go`, `network_cloudflare.go`, `network_test.go`, `authz_routes_test.go`, `tenancy_test.go`, `internal/authz/authz.go`, `policy.cedar`, `authz_test.go`.

**Interfaces:** Replace `SetCloudflare` with `SetConnections(hub ConnectionControl, registry *connections.Registry, box *secrets.Box)`. `ConnectionControl` exposes hub methods from Task 8. Produce DTO `{id,name,type,config,status,has_<secret>}`; create input `{type,name,config,secrets}`; PUT replaces name/config but omitted secret keys retain stored values, explicit empty secret strings clear them. Type is immutable; create a different connection to change type. Descriptor route uses `endpoint:read`.

- [ ] Add CRUD/detect/sync tests for every type, unknown type/config, secret retention/clearing, no secret echo in read/error/status/Detect, same-type multi-connection, duplicate names, immutable type, connection referenced by endpoint/service → 409, and removed old routes → 404. Run owner/admin/member matrices and org-A credentials against every org-B ID.

```go
func TestShouldRemoveOldCloudflareRoute(t *testing.T) {
    _, server := newNetworkTestAPI(t)
    code, body := statusAndBody(t, server, http.MethodGet, "/api/v1/cloudflare", "")
    if code != http.StatusNotFound { t.Fatalf("code=%d body=%s", code, body) }
}
```

- [ ] Run `./internal/controlplane ./internal/authz`; expect the old route still returns success before replacement.
- [ ] Register exactly the spec's connection routes. Use existing CSRF, request-size limits, strict JSON and org scoping. Generate IDs server-side. Seal complete secret JSON with org/ID AAD. Preserve provider-owned metadata separately from submitted config. Unsupported Detect returns a clear 400, not a fake success. Detect requires manage and the plugin/host gate where relevant.
- [ ] Guard both old and proposed configuration on update/delete, so a user cannot strip a host-command field to avoid authorization. Reference checks and delete run atomically with store writes; response 409 lists only same-org references. A committed record whose runtime reload/sync fails stays persisted with down/degraded status and an actionable error; do not report rolled-back data when it was committed.
- [ ] Replace `ActionCloudflare` with `ActionConnectionManage`; update default Cedar, all-action lists, stored-policy migration tests, and authz cache behavior. Remove old Cloudflare API implementation and public store interface methods only after `rg` confirms remaining uses are migration fixtures/provider internals.
- [ ] Run focused API/authz tests and all-package compilation. Commit `feat: expose typed connection api and permissions`.

### Task 13: Generic service API and Requires conflict responses

**Files:** Modify `internal/controlplane/network_services.go`, `network.go`, `network_test.go`, `authz_routes_test.go`; create `service_types_test.go`.

**Interfaces:** Produce `GET /api/v1/service-drivers`; service input `{name,driver,config,ready,requires,idle_timeout_seconds,start_timeout_seconds}`. DTO includes Requires IDs and same-org `required_by` IDs plus existing linked endpoints/status. Registry validates selected config; API resolves connection references and transitive host permissions. Walk every driver descriptor field with kind `connection`, validate its ConnType and same-org referenced record, and populate internal Service.ConnectionIDs for transaction-backed delete guards; do not assume the field is named connection_id.

- [ ] Add driver descriptor route tests, all driver round trips, wrong-type/foreign/missing connection refs, sensitive env read/edit preservation, -1/0/positive idle, missing/self/cyclic Requires, and typed stop/delete 409 guards. Anchor:

```go
func TestShouldAcceptWithDependentsIdleMode(t *testing.T) {
    f := newNetworkFixture(t)
    body := `{"name":"desktop-gui","driver":"command","config":{"start":["true"],"stop":["true"]},"requires":[],"idle_timeout_seconds":-1}`
    code, response := statusAndBody(t, f.srv, http.MethodPost, "/api/v1/services", body)
    if code != http.StatusCreated { t.Fatalf("code=%d response=%s", code, response) }
}
```

- [ ] Run focused controlplane tests; expect the old spec request shape to reject config/idle -1.
- [ ] Rewrite DTO/build/load paths around descriptors, retain old + proposed config authorization checks, preserve redacted env values on unchanged edits without accepting literal `<redacted>` as a real credential, and apply all dependency writes atomically. Descriptor fields and sensitive values are never raw HTML.
- [ ] Stop returns 409 with `dependents` IDs/names and “required by guacamole (running)” while active/starting dependents hold the service. Delete checks both linked endpoints and required-by services. Start checks transitive host-command/exec-plugin access, not only the requested driver. Unknown foreign references return a same-org validation failure without names.
- [ ] Run `./internal/controlplane ./internal/services ./internal/authz`; expected all old behavior assertions updated to equivalent new contracts, no skipped tests. Commit `feat: expose typed services and requirement guards`.

### Task 14: Endpoint Access refs and ingress hostname aliases

**Files:** Create `internal/connections/aliases.go`, `aliases_test.go`, `internal/controlplane/access_test.go`, `internal/ingress/access_alias_test.go`; modify `network.go`, `endpoints.go`, endpoint-policy tests, both stores' alias writers, `internal/ingress/cache.go`, `ingress.go`, `internal/gateway/gateway.go`, gateway policy tests.

**Interfaces:** Produce `DefaultAccessHostname(connectionType, endpointDomain string) (string, error)` and connection-specific effective hostname normalization. Replace `wasPublic bool` with prior `[]store.AccessRef` comparison in network policy validation. Alias lookup uses Task 2's store method and existing ingress invalidation hook.

- [ ] Add tests for Cloudflare default full domain, Tailscale first-label default, invalid labels, multiple refs, access vs runtime role, foreign refs, duplicate refs/hostnames, public+tailnet traffic to one endpoint, unchanged ref while a provider is down, new ref requiring a valid same-org connection, alias/canonical collisions, deletion/replacement invalidation, and gateway stripping `access`. Anchor:

```go
func TestShouldDefaultTailscaleToFirstDomainLabel(t *testing.T) {
    host, err := DefaultAccessHostname("tailscale", "notes.niradler.com")
    if err != nil || host != "notes" { t.Fatalf("host=%q err=%v", host, err) }
}
```

- [ ] Run `./internal/connections ./internal/controlplane ./internal/ingress ./internal/gateway`; expect the old public_access-only contract to fail.
- [ ] Validate connection identity/role and hostname uniqueness in the store transaction, compare prior refs per connection, normalize lowercase/trailing dot, and never let hostname changes count as an unchanged ref. Down providers do not prevent unrelated edits to previously valid access refs; new/changed refs still need validation. Removing access triggers provider reconciliation and alias removal.
- [ ] Resolve canonical domains first only when globally unambiguous, then exact persisted effective aliases; unknown aliases never fall through to another org's similarly named endpoint. Keep original Host, endpoint auth, policy enforcement, quotas, wake, SSRF guard, and health behavior identical on both access paths. Invalidate aliases on endpoint and connection changes. Sync persists observed Tailscale `<name>.<tailnet>.ts.net` aliases before reporting published.
- [ ] Strip `access` from agent control-plane input just as `public_access` was stripped; migrated old JSON is not accepted from an agent. Run ingress routing/auth isolation regression plus gateway tests. Commit `feat: route endpoint access hostnames safely`.
- [ ] At the subsequent Task 3 cutover, remove transitional legacy fields and unused adapters after migration has populated every record. Before beta.5, `rg -n 'PublicAccess|\.Spec|UsesHostCommands|ActionCloudflare' internal cmd` must find no live consumer outside explicit legacy-migration fixture/conversion code. Compile the entire repo after removal.
- [ ] Correct the Task 2 review's deferred minor fixture: both canonical-collision tests build underscored hostnames with NewID, which can fail hostname validation before testing collision. Use valid hyphenated hostnames and prove the canonical collision error path in both stores.

### Task 15: Generic UI field renderer and service forms/details

**Files:** Create `web/src/components/types/TypePicker.tsx`, `DescriptorFields.tsx`, `DescriptorDetails.tsx`, `web/src/lib/typed-fields.ts`; modify `web/src/api/types.ts`, `hooks.ts`, `lib/services.ts`, service form/config components and `pages/ServiceDetail.tsx`; create `.claude/scripts/pluggable-types/check-forms.cjs`.

**Interfaces:** TS `Field` matches Go wire metadata. `DescriptorFields({fields,values,onChange,connections,hostCommands,secretPresence})` renders all eight kinds; values are `Record<string, unknown>`. Connection pickers filter by `conn_type`. `DescriptorDetails` renders the selected type's labels; forms never branch on a driver's ID. `useServiceDrivers()` fetches descriptors; service hooks use config/ready/requires.

- [ ] Write a Playwright CLI scenario against a local server with synthetic API descriptors. Assert each driver picker generates its own labels/fields, then add a synthetic fifth driver descriptor without changing React and assert its fields render and submit. Verify Compose labels, Command argv round-trip, Kubernetes filtered cluster/namespace/replicas default, TCP/HTTP/driver-state readiness, linked Requires and required-by, and -1/0/timed idle.

```js
await page.getByRole("button", { name: "Add service", exact: true }).click();
await page.getByRole("button", { name: "Compose", exact: true }).click();
await page.getByLabel("Project", { exact: true }).fill("lab");
await page.getByLabel("Compose file", { exact: true }).fill("/srv/lab/compose.yml");
await page.getByLabel("Name", { exact: true }).fill("guacamole");
await page.getByRole("button", { name: "Create service", exact: true }).click();
```

- [ ] Capture the POST request and assert `{driver:"compose",config:{project:"lab",file:"/srv/lab/compose.yml"}}` rather than a spec wrapper; the scenario intentionally fails before implementation. Use fake descriptors only for renderer behavior, real API flows in Task 19.
- [ ] Implement shared field coercion/default/required checks, list argument editors, key-value editors, masked sensitive fields, select/number/bool/connection controls, accessible help, and “unchanged secret” state that is not submitted as a value. Keep server errors visible per field; frontend checks do not substitute for server validation.
- [ ] Replace per-driver state branching in `lib/services.ts`, forms and detail card with metadata. Add Readiness/Requires generic sections and explicit reverse-topological “Stop dependent too” actions for 409. Check permissions for every requested stop; a failed dependent stop aborts before stopping its requirement. Show the cluster-DNS reachability warning when the endpoint is cluster-internal and the runtime is outside the cluster.
- [ ] Run `npm run lint`, `npm run build`, and the browser script using `playwright-cli run-code --filename .claude/scripts/pluggable-types/check-forms.cjs`. Verify saved values after close/reopen. Commit `feat: render service drivers from descriptors`.

### Task 16: Connections UI and endpoint Access section

**Files:** Create `web/src/pages/Connections.tsx`, `ConnectionDetail.tsx`, connection components from the file map, `web/src/components/endpoints/AccessSection.tsx`, `web/src/lib/connections.ts`; modify `App.tsx`, Sidebar, API types/hooks, EndpointDetail/CreateEndpointDialog/EndpointPolicyForm, `PublishControl.tsx`, `PublicAccessStages.tsx` and its consumers, `lib/endpoints.ts`, `lib/glossary.ts`, dashboard/Cloudflare consumers; extend `check-forms.cjs`.

**Interfaces:** Produce `useConnectionTypes`, `useConnections`, `useConnection`, create/update/delete/detect/sync hooks scoped by org and connection ID. Add `/connections` and `/connections/:id`; old `/cloudflare` page has no sidebar/API dependency. Generic type panels use an explicit panel registration map; Cloudflare can reuse its existing ManagementSection without special-casing the generic form.

- [ ] Add browser cases for Add connection type picker, Detect capability, all three forms, list type/role/level/health, scoped detail navigation, secret retain/clear, sync failure fixes, referenced-delete 409, runtime connection excluded from Access, and two access rows on one endpoint. Anchor:

```js
await page.getByRole("link", { name: "Connections", exact: true }).click();
await page.getByRole("button", { name: "Add connection", exact: true }).click();
await page.getByRole("button", { name: "Tailscale", exact: true }).click();
await page.getByLabel("Name", { exact: true }).fill("home");
await page.getByLabel("Level", { exact: true }).selectOption("imported");
```

- [ ] Run the scenario; expect no Connections nav/picker before implementation.
- [ ] Implement typed connection table/detail/form with shared metadata. Generalize hostname table and reuse Cloudflare management UI as its registered panel; Tailscale/Kubernetes panels show only nonsecret node/cluster facts. Scope all query keys/invalidation to org/connection, including detect results when switching type/org.
- [ ] Replace Public access with Access refs in create/edit/detail flows. Picker filters access-role connections; each row identifies Public/Tailnet scope, connection and hostname, with independent add/remove and backend validation. Saving one ref preserves the others. Replace old URL prefill with `connection_id`/hostname selection, not a hidden public_access flag.
- [ ] Add glossary terms Connection, Access, Driver, Requires. Update dashboard links/health counts and all remaining Cloudflare hook consumers; remove unreachable old page components only under the user's destructive-command rule. No wider navigation redesign.
- [ ] Run lint/build and browser scenario at desktop/mobile widths, verify save/reopen and no credential values in DOM, request logs or displayed status. Commit `feat: add typed connections and endpoint access ui`.

### Task 17: Connection topology nodes and Requires edges

**Files:** Modify `internal/controlplane/topology.go`, `network_test.go`, `web/src/api/types.ts`, `components/network/TopologyMap.tsx`, network page and AppShell/Topbar layout; extend `.claude/scripts/pluggable-types/check-forms.cjs` and copy/adapt the prior `.claude/scripts/check-network-layout.cjs` in this feature worktree. Preserve original checkout and artifacts.

**Interfaces:** Connection node IDs `connection:<connectionID>` with `ref:{kind:"connection",id:<connectionID>}`; hostname IDs include connection ID to avoid collisions. Add edge kind `requires`, directed dependent service → required service. Retain endpoint/service/agent IDs and route/serves/proxies/tunnels/wakes behavior.

- [ ] Add API tests for two same-type connections, runtime nodes, public+tailnet host chains, unknown/down provider status, service Requires edge, and org isolation. Add browser checks for connection links and within-column service→service connectors; the current right-to-left edge routing assumes adjacent columns and must be corrected for same-column dependencies.

```go
func TestShouldKeepConnectionHostnameNodesDistinct(t *testing.T) {
    b := newTopologyBuilder()
    b.node(topologyNode{ID: "hostname:conn-a:notes", Kind: nodeHostname})
    b.node(topologyNode{ID: "hostname:conn-b:notes", Kind: nodeHostname})
    if len(b.result().Nodes) != 2 { t.Fatal("hostnames from different connections collapsed") }
}
```

- [ ] Run API/browser checks; expect missing connection/requires wire types before implementation.
- [ ] Render generic connection icons/labels and link paths from reference kind. Add distinguishable Requires legend/dash style and side-routed same-column curves; keep SVG paths and tooltip bounds inside the map viewport. Derive service requirements from stored graph without revealing other-org IDs.
- [ ] Carry the requested full-screen Network layout into this feature worktree: bounded flex page/map fills available screen; main hides overflow; map scrolls internally, legend remains visible; other routes retain existing layout. Use original checkout diff/script as reference, preserving it.
- [ ] Run focused topology tests, web lint/build, and browser selection/link/resize checks at desktop/tablet/phone sizes with overflow assertions and dense map fixture. Commit `feat: show typed connections and service requirements in network map`.

### Task 18: Update operational scripts and deterministic regression fixtures

**Files:** Modify `.claude/scripts/cf-import.py`, `import-services.py`, `lab-endpoints.py`, `lab-e2e.py`; create `.claude/scripts/pluggable-types/check-regression.py`, `scripts_test.py`, `kind-config.yaml`, `nginx.yaml`; update README API/operator docs and `.claude/docs/pluggable-types.md`.

**Interfaces:** Preserve `lab-e2e.py [api] --ingress <url> --wake <name> --no-public`; add explicit `--requires-wake <dependent>` and `--kubernetes-wake <service>` modes. Default mode does not stop GUI/services. Fixture cleanup is a separate printed operation, not unconditional finally-block deletion.

- [ ] Add Python regression tests using a local fake HTTP server that records request method/path/body. Verify cf-import finds/creates/updates the named typed Cloudflare connection without duplicating it, service imports send flattened config and `command`, endpoint script preserves unrelated Access refs, and E2E fails when any of the 12 migrated refs/8 service configurations is lost.

```python
def validate_migrated_snapshot(before, after):
    assert {row["id"] for row in before["services"]} == {row["id"] for row in after["services"]}
    for endpoint in before["endpoints"]:
        migrated = next(row for row in after["endpoints"] if row["id"] == endpoint["id"])
        if endpoint.get("policy", {}).get("public_access") == "cloudflare":
            assert len(migrated["policy"]["access"]) >= 1
```

- [ ] Expand the snapshot validator to compare actual linked connection identity, hostname, service driver/config/readiness/timers, and unclaimed orphan reporting; counts alone are insufficient. Run `python -m unittest discover -s .claude/scripts/pluggable-types -p '*_test.py'`; tests first fail on old payloads/routes.
- [ ] Implement new payloads and ID-based topology checks. Keep all existing ingress/public/health/CRUD guards. Secret handling stays in environment/files with restrictive permissions, never command-line arguments/logs. Do not automatically turn on managed Cloudflare.
- [ ] Add a kind fixture with loopback API binding and a mapped nginx NodePort, one namespace, one Deployment and one StatefulSet, default-one-replica behavior, plus minimum role permissions. The host mishmesh points at the mapped NodePort, not cluster-internal DNS. Register the cluster using embedded kubeconfig; create its services/endpoints via the real API.
- [ ] Compile Python sources in memory (`compile(path.read_text(), str(path), 'exec')`) and run unit tests plus lint/build after script/docs edits. Commit `test: update lab regressions for typed network control`.

### Task 19: Full validation, migration rehearsal, and review gate

**Files:** Extend `.claude/scripts/test-go.ps1` with optional outer Docker deadline; create `.claude/scripts/pluggable-types/compose.postgres.yml`, `check-kind.py`; update `.claude/docs/pluggable-types.md` with exact evidence and open failures.

**Interfaces:** No product API changes. Validation records source commit, versions, commands, exit codes, actual executed/skipped tests and real browser results. Docker/kind cleanup stays opt-in.

- [ ] Capture baseline `git status`, branch, and tool versions. Build a read-only beta.4-schema fixture, migrate a copy with the release candidate, close/reopen twice, and compare connection secrets by decrypting locally without printing plaintext. Test interruption/failure before commit leaves the original schema/rows intact. Do not copy the real lab DB/secret key into the repository.
- [ ] Start a disposable Postgres container on a loopback port or isolated Compose network. Set `MISHMESH_TEST_POSTGRES_DSN` in the test process; run `./internal/store/postgres` explicitly and assert output has executed CRUD/migration/concurrent tests. Existing tests create/drop temporary DBs: print those effects and get Nir's explicit authorization before running this destructive suite, or prepare it for Nir to run. Do not report skipped tests as a backend pass.
- [ ] Run the full gate with deadlines:

```text
make check
go build ./cmd/mishmesh-server ./cmd/mishmesh-agent
go test -race -count=1 -timeout=180s ./internal/store/postgres
npm --prefix web run lint
npm --prefix web run build
govulncheck ./...
npm --prefix web audit --audit-level=high
git diff --check
```

- [ ] Cross-compile server/agent for all existing release OS/arch pairs; new provider dependencies must not break Windows/Darwin builds. Check `go mod tidy` introduces only required dependencies. Fix code, do not disable tests or downgrade security expectations to pass.
- [ ] Start the real local server with a temporary DB and UI bundle on loopback. Run browser create/save/close/reopen/edit/delete-guard flows against actual APIs; cover owner/admin/member and two orgs, all driver descriptors, all connection descriptors, access row preservation, transitive host gates, and Requires 409 action. Use fakes only for remote providers not yet installed, with those limitations recorded.
- [ ] Create `mishmesh-pluggable` kind cluster (`kind create cluster --name mishmesh-pluggable --config .claude/scripts/pluggable-types/kind-config.yaml --wait 120s`), apply the nginx fixture, and run `check-kind.py` with a 600s outer deadline. Prove connection status, Deployment and StatefulSet scale-to-zero, wake through ingress, readiness, returned nginx response, permission denial and wrong namespace/workload. Prepare `kind delete cluster --name mishmesh-pluggable` for Nir, or execute only after explicit authorization; ensure unrelated clusters remain intact.
- [ ] Re-run the Requires shared/diamond/reload race suites after any lifecycle fixes. Review missing auth, unvalidated inputs, hardcoded secrets, logs/status leaks, config file reads, provider resource ownership, and user-visible inaccessible controls. Evidence must distinguish fake API, kind, and live-tailnet tests.
- [ ] Run the selected execution method's review gate. If subagent-driven, per-task review plus final branch review; if native, final independent review after Nir authorizes that method. Resolve findings and repeat only affected tests plus final gates. Commit `test: verify pluggable types migration and integration`.

### Task 20: PR, merge, release beta.5, and lab rollout

**Files:** Update CHANGELOG, README migration notes, `deploy/helm/mishmesh/Chart.yaml`/values only where release metadata requires it, `.claude/docs/pluggable-types.md`, `.claude/docs/lab-network-control.md`; create `.claude/scripts/pluggable-types/lab-rollout.md` as the concrete reviewable runbook.

**Interfaces:** Release tag `v0.1.0-beta.5`; existing `.github/workflows/release.yml` and GoReleaser publish server/agent, UI archive, images and chart. Lab remains `nir@lab-z170mx`, API `127.0.0.1:8091`, ingress `127.0.0.1:8090`, existing cloudflared tunnel.

- [ ] Verify beta.5 is unused with `git tag --list v0.1.0-beta.5` and `gh release view v0.1.0-beta.5`; distinguish “not found” from auth/network failures. Read current lab start script/env paths and binary version without exposing credentials. Prepare exact release assets/checksum paths from the existing GoReleaser conventions.
- [ ] Write CHANGELOG describing API/DB breakage and restore requirements. Update tracking with all validation evidence. Stage only feature paths; push `feat/pluggable-types`, create the PR with an exact body file describing behavior, migration, validation and live-test limits. Merge through the PR after review/CI pass; do not push master directly.
- [ ] Tag the reviewed merged commit `v0.1.0-beta.5`, push that exact tag, wait for all release jobs with bounded polls, and verify published artifacts/checksums/version. A successful binary job alone does not prove images/chart/UI publication.
- [ ] Before replacing lab files, collect beta.4 baseline using its existing E2E script and a read-only API snapshot. Back up the DB using SQLite backup while live or a consistent stopped copy, plus the matching secret key, start/env files, binary and UI. Keep backups outside served directories with restrictive permissions. Verify the DB backup opens and the old binary can read the backup without migrating it.
- [ ] Prepare and apply an atomic binary/UI switch, stop/start under the existing user startup mechanism, inspect migration/startup status, and run updated `lab-e2e.py --wake portainer`. Confirm the Cloudflare connection is still imported, all 12 selected hostnames route correctly, the three orphans stay unclaimed/unpruned, all eight existing services/IDs/configs survived, and public Access redirects are acceptable only where expected. No managed Cloudflare or Tailscale installation is implied by this rollout.
- [ ] If migration/traffic verification fails, stop progression and diagnose. Restoring beta.4 needs its matching pre-migration DB and key, not only its binary. Print the exact restoration commands/effects and ask Nir to run them unless he explicitly authorizes the agent. Preserve the failed migrated copy/logs for RCA with credentials redacted.
- [ ] Prepare this exact GUI service payload after confirming the actual `systemctl` executable path on the lab:

```json
{
  "name": "desktop-gui",
  "driver": "command",
  "config": {
    "start": ["sudo", "-n", "systemctl", "start", "gdm", "gnome-remote-desktop"],
    "stop": ["sudo", "-n", "systemctl", "stop", "gnome-remote-desktop", "gdm"],
    "status": ["systemctl", "is-active", "gnome-remote-desktop"]
  },
  "requires": [],
  "idle_timeout_seconds": -1,
  "start_timeout_seconds": 120
}
```

- [ ] Prepare a sudoers candidate allowing only `nir ALL=(root) NOPASSWD: /usr/bin/systemctl start gdm gnome-remote-desktop, /usr/bin/systemctl stop gnome-remote-desktop gdm` if `/usr/bin/systemctl` is the verified path. Validate candidate with `visudo -cf` before installation. Show Nir the exact drop-in content, installation command and `MISHMESH_HOST_COMMANDS=true` change; wait for his confirmation before applying either. Do not enable the GUI/default graphical target independently.
- [ ] After confirmation, apply the two approved changes, create `desktop-gui`, attach it to guacamole using the preserved existing config/access, and run `lab-e2e.py --requires-wake guacamole`. Prove desktop starts before guacamole, stays up while required, requirement manual stop returns 409, then guacamole stop releases it and desktop sleeps. Any DELETE/cleanup is separately handled under the destructive-command rule.
- [ ] When Nir has installed/joined Tailscale and approved the tagged host, detect/create an imported or managed connection as he directs, add tailnet access to one existing endpoint while retaining its Cloudflare ref, and check HTTPS traffic from a second tailnet client. Managed credentials must come through the secret form/environment, not chat/logs. If those prerequisites are absent, record live Tailscale validation as pending; do not call fake-provider evidence a live-tailnet pass.
- [ ] Complete the kind proof from Task 19 and its authorized cleanup. Finish with source/release IDs, PR link, lab binary/UI version, baseline and extended E2E results, GUI approval outcome, Tailscale live/pending status, and any remaining issues in the two tracking documents. Commit docs evidence separately if rollout happened after the release commit.

## Spec coverage and self-review

| Spec requirement | Tasks |
|---|---|
| Shared descriptors, no per-driver UI branching | 1, 4, 15 |
| Connections table, stores, sealed secrets, migration | 2, 3, 8 |
| Access refs, per-ref validation, alias ingress, agent stripping | 2, 14, 16 |
| Generic API/routes/authz/remove legacy routes | 12, 13, 18 |
| Cloudflare behavior/supervisor unchanged | 9, 12, 16, 20 |
| Tailscale imported/managed/ownership/preconditions/no Funnel | 11, 16, 20 |
| Kubernetes connection permissions/exec guard/driver/reachability | 10, 13, 15, 19 |
| Generic TCP/HTTP readiness | 5, 13, 15 |
| Requires DAG/wake/hold/release/409/delete/UI | 6, 7, 13, 15 |
| Typed connection nodes and Requires map edge | 17 |
| Both stores and migration test evidence | 2, 3, 19 |
| PR/merge/beta.5/lab regression + gated GUI/Tailscale/kind | 18, 19, 20 |

Self-review performed before handoff: every spec section has an owning task; shared signatures/wire keys are defined before consumers; command argv/idle sentinel/ownership/alias decisions are explicit; destructive and host permission gates are preserved. No implementation has been performed by writing this plan. Green tests, publication, and lab behavior are future execution gates, not claims about current state.

## Primary references for execution

- [Tailscale Services prerequisites, host approval and HTTPS examples](https://tailscale.com/docs/features/tailscale-services): establishes the Services mechanism and live-client validation requirements. Check the selected release's LocalAPI/capabilities as well as the server version floor.
- [Tailscale LocalAPI Serve client](https://github.com/tailscale/tailscale/blob/main/client/local/serve.go): get/set Serve config behavior, including ETag handling. Use the pinned module's source when implementing.
- [Kubernetes client-go credential plugins](https://kubernetes.io/docs/reference/access-authn-authz/authentication/#client-go-credential-plugins): exec credentials can launch a host program; inspect config before transport/client creation.
- Local baseline: `internal/cloudflare/*_test.go`, `internal/services/manager_test.go`, `internal/controlplane/network_test.go`, both `internal/store/*/network_test.go`, `.claude/scripts/lab-e2e.py`, `.github/workflows/release.yml`, `.goreleaser.yaml`.

## Handoff

Recommended execution method: **subagent-driven**, because migration correctness, remote resource ownership and concurrent requirement holds each warrant an independent review before their consumers land. Native execution is also available, with one whole-branch review after implementation. Nir chooses the method before Task 1 starts.
