# Deploying mishmesh (headless)

mishmesh ships two images: `mishmesh-server` (gateway + ingress + control API) and `mishmesh-agent` (tunnel client). This guide covers a headless cloud deployment.

## Local demo (one command)

```bash
docker compose up --build
# server seeds a bootstrap token; the agent exposes the `whoami` service.
curl -H "Host: demo.localhost" http://localhost:8080/
```

## Cloud deployment

### 1. DNS

Point a wildcard record at the server's public IP so every endpoint subdomain resolves:

```
*.tunnel.example.com   A   <server-ip>
tunnel.example.com     A   <server-ip>
```

### 2. TLS

Two options:

- **BYO wildcard cert (recommended for many ephemeral subdomains):** obtain a cert for `*.tunnel.example.com` (e.g. via DNS-01) and mount it.
  ```
  MISHMESH_TLS_ENABLED=true
  MISHMESH_TLS_CERT_FILE=/data/certs/fullchain.pem
  MISHMESH_TLS_KEY_FILE=/data/certs/privkey.pem
  ```
- **ACME/autocert (on-demand per host):** good for the apex and a handful of custom domains; beware Let's Encrypt rate limits with many ephemeral subdomains.
  ```
  MISHMESH_TLS_ENABLED=true
  MISHMESH_ACME_ENABLED=true
  MISHMESH_ACME_EMAIL=ops@example.com
  ```
  ACME needs ports 80 and 443 publicly reachable.

### 3. Run the server

```bash
docker run -d --name mishmesh-server \
  -p 80:8080 -p 443:8443 -p 8081:8081 -p 10000-10100:10000-10100 \
  -v mishmesh-data:/data \
  -e MISHMESH_BASE_DOMAIN=tunnel.example.com \
  -e MISHMESH_PUBLIC_SCHEME=https \
  -e MISHMESH_TLS_ENABLED=true \
  -e MISHMESH_TLS_CERT_FILE=/data/certs/fullchain.pem \
  -e MISHMESH_TLS_KEY_FILE=/data/certs/privkey.pem \
  -e MISHMESH_BOOTSTRAP_TOKEN=$(openssl rand -hex 24) \
  mishmesh-server:latest
```

The container binds `0.0.0.0` by default. The API/control listener (`8081`) should NOT be exposed publicly — keep it on a private network or behind auth.

### 4. Issue agent tokens

Either set `MISHMESH_BOOTSTRAP_TOKEN` (idempotent single token), or create per-agent tokens:

```bash
docker exec mishmesh-server mishmesh-server token create --org acme
# or via the control API (internal listener):
curl -X POST http://127.0.0.1:8081/api/v1/agents -d '{"name":"acme-dc1"}'
```

### 5. Run an agent (on the private network)

```bash
MISHMESH_TOKEN=<token> mishmesh-agent http 3000 --subdomain app --gateway wss://tunnel.example.com:8081
# or TCP:
MISHMESH_TOKEN=<token> mishmesh-agent tcp 22 --gateway wss://tunnel.example.com:8081
```

## Key environment variables

| Var | Purpose |
| --- | --- |
| `MISHMESH_BASE_DOMAIN` | public host suffix for URLs (e.g. `tunnel.example.com`) |
| `MISHMESH_PUBLIC_SCHEME` | `https` in production |
| `MISHMESH_INGRESS_ADDR` / `MISHMESH_HTTPS_ADDR` / `MISHMESH_API_ADDR` | bind addresses (default `0.0.0.0:*` in the image) |
| `MISHMESH_TLS_ENABLED` + cert/ACME vars | enable HTTPS ingress |
| `MISHMESH_TCP_ENABLED` / `MISHMESH_TCP_PORT_MIN` / `MISHMESH_TCP_PORT_MAX` | public TCP endpoint range |
| `MISHMESH_DATA_DSN` | SQLite path (default `/data/mishmesh.db`) |
| `MISHMESH_BOOTSTRAP_TOKEN` | seed a fixed agent token on startup |
| `MISHMESH_API_AUTH_TOKEN` | require this bearer token on the control/management API (`/api/v1/*`); health stays open |

## Notes

- Persist `/data` (SQLite + ACME cache) on a volume.
- The control/API port (`8081`) is the agent connect + management surface — restrict it.
- Single-node today: in-memory connection store + SQLite. Redis + Postgres backends are planned behind the existing store interfaces for multi-node.
