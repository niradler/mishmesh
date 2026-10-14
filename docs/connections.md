# Connections and app Access

Connections describe systems Mishmesh already has access to. The shipped access providers are Cloudflare Tunnel for public HTTP apps and Tailscale Services for private HTTPS apps. Both accept HTTP endpoints; TCP, UDP and TLS passthrough remain available through their existing ingress/agent paths. An access provider is not an endpoint transport method.

Use **Connections** to create a named connection, then add it under an endpoint's **Access** section. Each Access row contains a connection ID and hostname. A connection belongs to one organization. Multiple connections of the same type are supported, and one endpoint can retain public and private refs together. Saving Access preserves the endpoint's Connect policy and other settings.

The type descriptors own fields, detection inputs, role, scope and supported protocols. The generic form understands text, list, key/value, secret, select, number, boolean and connection fields. Runtime providers can be added to the registry without adding a provider switch to service forms. NetBird and existing WireGuard networks are extension targets; this release does not ship those providers or manage VPN peers, keys, routes or network policy. Kubernetes support is cancelled.

## Cloudflare

Create `type: cloudflare` with `config` containing optional `account_id`, `tunnel_id`, `access_app_id`, `origin`, `connector`, `metrics_addr` and `prune`. Supply the existing tunnel token in `secrets.tunnel_token`. An API token in `secrets.api_token` enables managed reconciliation. An imported connection observes existing routes and never turns on management automatically. Keep Prune off until you have reviewed ownership and unclaimed hostnames.

The hostname defaults to the endpoint domain and must be a full DNS name. Public domain ownership checks still apply when enabled. Local metrics must use a loopback address. `connector: managed` starts a local cloudflared and requires host execution permission and `MISHMESH_HOST_COMMANDS=true`. Imported external connectors remain available without starting a process. See [Cloudflare permissions and ownership](cloudflare.md).

The operator import helper is idempotent by connection name:

```text
python .claude/scripts/cf-import.py http://127.0.0.1:8081 --token-file <private-token-file> --name public
```

Read the API bearer token from `MISHMESH_API_AUTH_TOKEN`; keep token files and environment private. The helper does not print connection credentials or enable an API token.

## Tailscale

Create `type: tailscale`. Configuration contains `level` (`imported` or `managed`), required `tailnet`, optional `socket` and `service_tags`. Managed mode also needs `secrets.oauth_client_id` and `secrets.oauth_client_secret`. Imported mode observes the selected LocalAPI. Detection reads LocalAPI and requires host permission. Mishmesh does not install or join Tailscale.

Private hostnames are service labels, optionally entered as `svc:<label>`. The provider derives a default label from the endpoint domain and uses the observed tailnet DNS suffix for HTTPS. The connection must point at an already joined, tagged node. Host approval, service tag ownership and access grants are configured in Tailscale. Managed mode reconciles its owned Services, Serve entries and advertisements; it preserves foreign resources and later operator edits. Do not treat a pending approval or a configured OAuth credential as proof that traffic is reachable.

Validate private HTTPS from a second authorized tailnet client after the node/service approvals and grants are in place. The repository's fake HTTP/LocalAPI tests prove provider contracts and ownership behavior. They do not prove a live tailnet or account configuration.

## Saving, secrets and routing

Connection saves commit before bounded reload/sync. A saved row can report degraded/down status if a provider is unavailable; correct its configuration and retry sync. Secrets are sealed together with organization/connection identity as authenticated context. API reads return only `has_<secret>` presence flags. Omitting a secret preserves it; supplying an empty string explicitly clears it.

Published hostnames become durable endpoint aliases only after provider observation and a conditional store write against the loaded connection and Access generation. Editing config or removing/renaming Access clears invalid aliases. Ingress invalidation makes removed private aliases fail closed, including path-routing fallback. Existing endpoint authentication, wake activity and quota accounting apply to aliases too.

Deleting a referenced connection returns409 with endpoint/service references. Disconnect applications first. Deletion closes the local provider; it does not imply removal of every external resource. Provider ownership records are separate from user config and never appear in API responses. Coordination is process-local: remote APIs that lack compare-and-swap cannot guarantee atomicity against simultaneous unrelated external writers.

## Upgrade and validation

Back up the database and matching `mishmesh-secret.key` before first startup with this branch. Startup atomically migrates legacy Cloudflare rows, service specs, endpoint public selections and parsed authorization actions. IDs, timers, readiness, Connect/UDP policies and unclaimed routes are preserved. Missing/wrong keys and malformed records fail before migration commits. Restore an old binary together with its matching pre-migration database and key; an old binary alone is insufficient.

The first key is published atomically without replacing an existing key. The key directory must support hard links, as tested on NTFS and Linux. Unsupported filesystems fail closed; supply a valid key file in a supported private location before startup.

Shared validation scripts live in [.claude/scripts/pluggable-types](../.claude/scripts/pluggable-types). PostgreSQL fixtures have no published ports and use `MISHMESH_TEST_RETAIN_FIXTURES=true`; their databases and containers remain available for inspection. Private operator scripts and machine-specific runbooks stay in ignored `.claude/local` files. See the [release/rollout runbook](../.claude/scripts/pluggable-types/lab-rollout.md).

The web UI uses Tailwind 4 and requires Safari 16.4+, Chrome 111+ or Firefox 128+. Build-time dependency audits are part of validation; update browser clients before upgrading the UI. See the [Tailwind browser requirements](https://tailwindcss.com/docs/upgrade-guide#browser-requirements).
