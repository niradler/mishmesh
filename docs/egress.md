# Egress

An agent can act as a forward proxy for clients on its network. Every connection and, where the agent can see HTTP, every request is decided by the org's Cedar policy on the server.

```yaml
egress:
  listen: 127.0.0.1:3128
  originate:
    payments:
      listen: 127.0.0.1:9001
      upstream: https://api.payments.example
```

## Policy actions

| Action | When it is evaluated | Context |
| --- | --- | --- |
| `egress:connect` | every `CONNECT` / SOCKS dial | `resource.host`, `resource.port` |
| `egress:request` | every HTTP request the agent can read: absolute-form `http://` / `https://` proxy requests, `originate` listeners and decrypted TLS | `context.method`, `context.path`, `context.tls`, `context.decrypted`, `context.exit` |

Requests are refused with `400` before policy when the path is not absolute or has dot, empty (`//`) or `;` segments, or when the method is not uppercase. A denied request returns `403` with an `egress refused` body naming the reason.

## Credential injection

A credential names a secret, the header that carries it, the agents allowed to receive it and the destinations it may be sent to:

```http
PUT /api/v1/egress/credentials/api-key
{"header": "Authorization", "prefix": "Bearer ", "secret": "api-secret", "agent_ids": ["<agent id>"], "destinations": ["api.example.com"]}
```

`destinations` is required: 1 to 32 entries, each a host, `*.domain` or an exact IP address. The server releases the secret only when the request's host matches, even if a broader policy rule selects the credential. Credentials are never injected into `TRACE` requests.

Annotate the permit that should inject it:

```cedar
@credential("api-key")
permit (principal, action == Action::"egress:request", resource)
when { resource.host == "api.example.com" && context.decrypted && context.path like "/v1/*" };
```

The secret is sealed with HPKE to a per-session agent key and is released only to listed agents. The client's own copy of the header is stripped. Injection over plaintext `http` is refused. Changing or deleting the secret flushes agent caches.

## TLS decryption

Without decryption a `CONNECT` tunnel is opaque: only `egress:connect` applies. To enforce `egress:request` inside HTTPS, scope decryption for the org:

```http
PUT /api/v1/egress/decrypt
{"allow": ["api.example.com", "*.internal.example"], "bypass": ["bank.internal.example"]}
```

- `allow` entries are a host or `*.domain`. IP addresses are rejected. An empty list turns decryption off.
- `bypass` wins over `allow`. Bypassed and unlisted hosts pass through untouched.
- Clients must trust the org root. Fetch it from the API (`GET /api/v1/egress/ca` returns the PEM and its SHA-256) or from an agent host:

```sh
mishmesh-agent ca > org-egress-ca.pem
curl -x http://127.0.0.1:3128 --cacert org-egress-ca.pem https://api.example.com/v1/items
```

`mishmesh-agent ca` prints the SHA-256 fingerprint on stderr. Compare it with the one from the admin API before you trust the PEM.

How it works:

- The org root key is created on first use and sealed at rest on the server.
- Each agent generates a P-256 key and requests an intermediate over its control channel. The server issues it with path length 0, a 4h lifetime and name constraints built only from the org's `allow` scope, excluding every IP, email and URI name. The agent can mint leaves only for hosts the org allowed.
- Decryption fails closed. While the agent cannot tell whether a host is in scope (not yet connected, intermediate pending or failed) or cannot mint a leaf, `CONNECT` returns `503 egress decryption unavailable` instead of passing through. A decrypt-scoped `CONNECT` that does not start with TLS is closed.
- SOCKS cannot decrypt, so SOCKS connections to decrypt-scoped hosts, or made while the scope is unknown, are refused. Use the HTTP proxy for those hosts.
- `CONNECT` to an IP address is never decrypted; only `egress:connect` applies to it.
- The agent verifies the chain to the org root, then mints one leaf per host, bounded in count and never outliving the intermediate. It rotates the intermediate before expiry.
- The agent only decrypts when the client's SNI matches the `CONNECT` target. On a mismatch it refuses.
- The agent re-originates TLS to the real upstream with strict certificate verification. A failed upstream verification returns `502`.
- HTTP/1.1, HTTP/2 and WebSocket upgrades are supported.
- Changing the policy, a credential or the decrypt scope flushes the agent's cached decisions and certificates within seconds.

Exit agents must run the same release as the server.
