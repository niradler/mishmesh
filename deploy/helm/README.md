# mishmesh on Kubernetes (Helm)

Two charts:

| Chart | Installed by | What it runs |
| --- | --- | --- |
| `mishmesh` | the operator of the gateway | `mishmesh-server`: public ingress (HTTP/HTTPS/TLS passthrough/TCP range/SSH), agent WSS + control API + web UI, optional built-in Postgres |
| `mishmesh-agent` | whoever owns the private network | one `mishmesh-agent` Deployment per tunnel, dialing the gateway and exposing an internal service |

Requires Kubernetes 1.30+ (the drain hook uses the native `preStop.sleep` action because the images are distroless and have no shell).

## Three deployment shapes

Postgres is the data store in every shape. Redis and cluster mode are only needed with more than one server pod.

| | `values.yaml` (homelab) | `values-company.yaml` | `values-saas.yaml` |
| --- | --- | --- | --- |
| Use case | one box / small cluster, try it out | self-hosted inside a company or customer network | public multi-tenant service |
| Server pods | 1 | HPA 2-6 | HPA 3-20 |
| Postgres | built-in single-instance StatefulSet (`postgres:16-alpine`, PVC) | external (`existingSecret`) | external (`existingSecret`) |
| Connection store | memory | Redis (external) | Redis (external) |
| Cluster mode / relay | off | on | on |
| Auth (login) | off, API bearer token on | on (password, optional Google) | on + per-org quotas |
| TLS | plain HTTP (enable as needed) | wildcard cert from a Secret | wildcard cert from a Secret |
| PDB / topology spread / NetworkPolicy | off | on | on |
| ServiceMonitor | off | off | on |

### Homelab

```bash
helm upgrade --install mishmesh deploy/helm/mishmesh -n mishmesh --create-namespace \
  --set baseDomain=tunnel.home.example.com
```

This creates the server, a built-in Postgres with a generated password, a `LoadBalancer` service for public traffic, and a `ClusterIP` API service. A bootstrap agent token is generated:

```bash
kubectl -n mishmesh get secret mishmesh -o jsonpath='{.data.bootstrap-token}' | base64 -d
kubectl -n mishmesh get secret mishmesh -o jsonpath='{.data.api-auth-token}' | base64 -d
```

To use your own Postgres instead, set `postgres.external.url` (or `postgres.external.existingSecret`); the built-in one is then turned off automatically.

### Company and SaaS

Create the prerequisite Secrets first (the values files reference these names):

```bash
kubectl create ns mishmesh
kubectl -n mishmesh create secret generic mishmesh-db \
  --from-literal=data-dsn='postgres://mishmesh:PASSWORD@pg.example.internal:5432/mishmesh?sslmode=require'
kubectl -n mishmesh create secret generic mishmesh-redis \
  --from-literal=redis-url='rediss://:PASSWORD@redis.example.internal:6379/0'
kubectl -n mishmesh create secret tls mishmesh-wildcard-tls --cert=fullchain.pem --key=privkey.pem
kubectl -n mishmesh create secret tls mishmesh-connect-tls --cert=connect.pem --key=connect-key.pem

helm upgrade --install mishmesh deploy/helm/mishmesh -n mishmesh \
  -f deploy/helm/mishmesh/values-company.yaml \
  --set baseDomain=tunnel.corp.example.com \
  --set connectIngress.host=connect.corp.example.com \
  --set connectIngress.controlHost=mishmesh.corp.example.com
```

For SaaS use `-f values-saas.yaml` and also set `auth.googleClientID`, `secrets.googleClientSecret`, and `auth.oidcRedirectURL` (`https://<controlHost>/api/v1/auth/google/callback`).

Managed Postgres/Redis (RDS, Cloud SQL, ElastiCache, Memorystore, ...) is the intended path. The chart does not bundle Postgres/Redis subcharts for these profiles; run them however your platform team prefers and hand the chart a URL.

## Services

| Service | Type (default) | Ports | Purpose |
| --- | --- | --- | --- |
| `<release>-public` | LoadBalancer | 80 -> 8080 http, 443 -> 8443 https, 8444 TLS passthrough, 2222 SSH, one entry per TCP port in `listeners.tcp.portMin..portMax` | everything tunnel users and their visitors reach |
| `<release>-api` | ClusterIP | 8081 | agent WSS (`/_mishmesh/agent/connect`), control API (`/api/v1`), web UI, `/metrics`, `/healthz`, `/readyz` |
| `<release>-relay` | headless | 7443 | pod-to-pod relay, cluster mode only |
| `<release>-http` | ClusterIP | 80 -> 8080 | backend for the optional HTTP `Ingress` |
| `<release>-postgres` | ClusterIP | 5432 | built-in Postgres only |

The TCP range is rendered as one Service port per public port. Keep it small: many cloud load balancers cap listeners per LB (for example AWS NLB defaults to 50). Widen `listeners.tcp.portMax` only as far as your LB allows.

`service.public.externalTrafficPolicy: Local` (company/SaaS) preserves the client source IP, which per-endpoint IP allow/deny policies rely on.

## Exposing agents' WSS publicly while keeping the control API private

The API listener multiplexes agent connections and the management API. To let agents outside the cluster connect without exposing `/api/v1`, enable `connectIngress`:

```yaml
connectIngress:
  enabled: true
  className: nginx
  host: connect.example.com
  exposeControlAPI: false
  controlHost: ""
  tls:
    secretName: mishmesh-connect-tls
```

Only the exact path `/_mishmesh/agent/connect` is routed on `connect.example.com`. The default annotations raise ingress-nginx read/send timeouts to one hour so idle tunnels are not cut; ingress-nginx handles the WebSocket upgrade itself. For other controllers, set the equivalent long-lived-connection timeouts.

Agents then use `--gateway wss://connect.example.com` (the agent appends the connect path).

`controlHost` adds a second host that routes everything to the API service (web UI + `/api/v1`), for deployments where users log in through a browser. Leave it empty and `exposeControlAPI: false` to keep the control plane reachable only in-cluster (`kubectl port-forward svc/<release>-api 8081`) or behind your own internal Ingress.

Pick `connect.*` / `controlHost` names outside the wildcard tunnel domain (for example `connect.example.com` rather than `connect.tunnel.example.com`) so they never collide with a tenant subdomain.

## How scaling works (cluster mode)

- Every server pod is equal. Any pod accepts agent WSS sessions and any pod accepts public traffic on every listener, including the whole TCP port range.
- Session ownership lives in Redis: when an agent connects to pod A, pod A records itself (`MISHMESH_NODE_ID` = pod name, `MISHMESH_RELAY_ADVERTISE` = `podIP:7443`) as the owner.
- When a public request lands on pod B for an endpoint whose agent is on pod A, pod B opens a stream to pod A over the relay port (7443, authenticated with `MISHMESH_CLUSTER_SECRET`) and pod A forwards it into the agent tunnel. No session affinity is needed at the load balancer.
- The HPA scales on CPU (and optionally memory). Scale-up adds pods immediately; scale-down is deliberately slow (one pod per period after a stabilization window) because each removed pod drops its agent sessions.
- On scale-down or rollout the pod receives SIGTERM after `drain.preStopSleepSeconds` (so endpoints/LB stop sending new traffic), then has the rest of `drain.terminationGracePeriodSeconds` to finish in-flight streams. Agents on that pod reconnect (through the Service) to a surviving pod, which takes over ownership in Redis.
- The PodDisruptionBudget keeps at least one pod (company) or allows at most one down at a time (SaaS) during node drains; topology spread distributes pods across nodes and zones.

The homelab profile runs a single pod with the in-memory connection store; it uses a rolling update (`maxSurge: 1`, `maxUnavailable: 0`), so agents reconnect to the new pod during upgrades.

## DNS and TLS

DNS, pointing at the external address of `<release>-public`:

```text
tunnel.example.com      A/CNAME  <public LB>
*.tunnel.example.com    A/CNAME  <public LB>
```

Plus `connect.example.com` (and `controlHost`) at your Ingress controller if `connectIngress` is enabled.

TLS options for the public listener:

1. **Wildcard cert in a Secret (recommended, required for multi-pod):** issue `*.tunnel.example.com` + `tunnel.example.com` via DNS-01 (cert-manager `Certificate` with a DNS01 solver works well), then set `listeners.https.enabled=true` and `tls.existingSecret=<secret>`. The server terminates TLS itself, so mTLS endpoint policies keep working.
2. **ACME in the server (`tls.acme.enabled=true`):** single-pod only. The cert cache is in the pod's `emptyDir`, so certs are re-issued on restart; watch Let's Encrypt rate limits.
3. **Ingress-terminated HTTP (`ingress.enabled=true`):** puts an Ingress in front of the HTTP listener for `tunnel.example.com` and `*.tunnel.example.com`. Example values:

   ```yaml
   ingress:
     enabled: true
     className: nginx
     annotations:
       cert-manager.io/cluster-issuer: letsencrypt-dns01
     tls:
       secretName: mishmesh-wildcard-tls
   ```

   With this mode TLS ends at the Ingress, so endpoint mTLS policies do not apply; TCP, TLS passthrough and SSH still go through `<release>-public`, which keeps exposing port 80 as well (set `service.public.enabled=false` if you only need HTTP).

## Secrets

All generated values live in one Secret `<release>` (annotated `helm.sh/resource-policy: keep`), reused across upgrades via `lookup`:

| Key | Env | Generated when |
| --- | --- | --- |
| `api-auth-token` | `MISHMESH_API_AUTH_TOKEN` | always, unless `apiAuth.disabled=true` |
| `endpoint-oidc-key` | `MISHMESH_ENDPOINT_OIDC_KEY` | always |
| `cluster-secret` | `MISHMESH_CLUSTER_SECRET` | `cluster.enabled` |
| `bootstrap-token` | `MISHMESH_BOOTSTRAP_TOKEN` | `secrets.generateBootstrapToken` or `secrets.bootstrapToken` |
| `google-client-secret` | `MISHMESH_GOOGLE_CLIENT_SECRET` | `secrets.googleClientSecret` set |
| `data-dsn` | `MISHMESH_DATA_DSN` | `postgres.external.url` set |
| `redis-url` | `MISHMESH_REDIS_URL` | `connStore.redis.url` set |
| `ssh-host-key` | `MISHMESH_SSH_HOST_KEY_FILE` (mounted) | SSH enabled and `ssh.hostKey.generate` (ed25519, shared by all pods) |

The built-in Postgres keeps its password and DSN in `<release>-postgres`.

Bring your own: set `secrets.existingSecret` to a Secret containing the same keys. `postgres.external.existingSecret`, `connStore.redis.existingSecret`, `ssh.hostKey.existingSecret` and `tls.existingSecret` override individual pieces.

With GitOps tools that render without cluster access (Argo CD), `lookup` returns nothing and generated values would rotate on every sync. Use `secrets.existingSecret` there.

## Agent chart

```bash
kubectl -n tunnels create secret generic mishmesh-agent --from-literal=token=<agent token>
helm upgrade --install edge deploy/helm/mishmesh-agent -n tunnels --create-namespace \
  --set gatewayURL=wss://connect.example.com \
  --set existingSecret=mishmesh-agent \
  -f my-tunnels.yaml
```

```yaml
tunnels:
  - name: grafana
    kind: http
    target: grafana.monitoring.svc:3000
    subdomain: grafana
  - name: db
    kind: tcp
    target: postgres.data.svc:5432
    port: 10005
    existingSecret: mishmesh-agent-db
    existingSecretKey: token
```

Each tunnel becomes its own Deployment running `mishmesh-agent <kind> <target> [--subdomain x | --port N] [--reserved] [--target-https] [--insecure]`. `extraArgs` appends raw flags. Different tunnels should use different agent tokens (per-tunnel `token` or `existingSecret`); one agent identity per connection. Agents in the same cluster as the server can use `gatewayURL: ws://<release>-api.<ns>.svc:8081`. `allow` sets the reach-in allowlist (`MISHMESH_ALLOW`).

## Validate locally

```bash
helm lint deploy/helm/mishmesh
helm lint deploy/helm/mishmesh -f deploy/helm/mishmesh/values-company.yaml
helm lint deploy/helm/mishmesh -f deploy/helm/mishmesh/values-saas.yaml
helm template t deploy/helm/mishmesh-agent --set token=x
```

## Key values

| Value | Default | Notes |
| --- | --- | --- |
| `baseDomain` / `publicScheme` | `tunnel.example.com` / `http` | public URL suffix and scheme |
| `listeners.*.enabled` / `containerPort` / `servicePort` | http, api, tcp on | https, tlsPassthrough, ssh opt-in |
| `listeners.tcp.portMin` / `portMax` | 10000 / 10009 | one Service port each |
| `postgres.builtin.enabled` | `true` | off automatically when `postgres.external.*` is set |
| `connStore.backend` | `memory` | `redis` required for cluster mode |
| `cluster.enabled` / `cluster.relayPort` | `false` / 7443 | required for replicas > 1 or HPA |
| `auth.*`, `quotas.*`, `features.*` | | map 1:1 to `MISHMESH_*` env |
| `drain.preStopSleepSeconds` / `terminationGracePeriodSeconds` | 10 / 45 | |
| `extraEnv` | `[]` | any other `MISHMESH_*` variable |
