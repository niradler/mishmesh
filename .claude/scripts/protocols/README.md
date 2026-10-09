# mishmesh protocol validation harness

Black-box end-to-end checks of real customer protocols through the tunnel. Compose projects `proto` and `proto-cluster`, plus the isolated published-chart proof namespace.

## Status

Validation completed on 2026-10-09 for v0.1.0-beta.1, including published binaries, images and OCI charts. See [results-2026-10-09.md](results-2026-10-09.md) for measured results, fixes and deployment evidence.

## Layout

- `kit/` is a separate Go module (`protokit`), outside the main module, with gRPC and WebSocket dependencies
  - `kit serve` is the backend: `:8080` HTTP/1.1+h2c (`/gen?size=&seed=`, `/upload`, `/sse?n=&every=`, `/slow?d=`, `/ws` echo, gRPC on `application/grpc`), `:8443` TLS with its own cert (CN `kit-backend`), `:9000` raw half-close sha service, `:9090` gRPC h2c, `:9443` gRPC TLS
  - client subcommands: `gen`, `gensha`, `upload [-chunked]`, `ws [-idle]`, `sse`, `grpc [-tls -authority]`, `par`, `stream` (raw reach-in bridge for SSH ProxyCommand), `halfclose`
- `compose.yml` has these networks: `infra` (postgres and redis for mishmesh), `edge` (servers, agent, client, lan) and `private` (kit, sshd, pgtarget, redistarget, agent, lan). The client is only on `edge`.
- `agent.yml` uses the bootstrap token (`ag_bootstrap`) and defines these tunnels: http `kit`, http `kits` (target_https), tls `pt`, and tcp ports 10001 ssh, 10002 pg, 10003 redis, 10004 raw, 10005 grpc h2c, 10006 grpc tls. The reach-in allowlist is `172.31.77.0/24`.

## Build

    export PROTO_BIN=<scratch>/proto/bin
    GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o $PROTO_BIN/mishmesh-server ./cmd/mishmesh-server
    GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o $PROTO_BIN/mishmesh-agent ./cmd/mishmesh-agent
    (cd .claude/scripts/protocols/kit && GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o $PROTO_BIN/kit .)

## Run

    cd .claude/scripts/protocols
    timeout 300 docker compose -p proto up -d
    CLUSTER=true CONN_BACKEND=redis timeout 300 docker compose -p proto --profile cluster up -d
    timeout 60 docker compose -p proto exec -T client curl -s -H 'Host: kit.mm.test' http://server-a:8080/

The single-pod rows (1-6, 8, 9) run against `server-a` with the memory connection store. Row 7 needs the cluster profile, with requests sent to `server-b` while the agent stays on `server-a`. To read RSS, run `docker exec proto-<svc>-1 grep VmRSS /proc/1/status` during a transfer.

Example commands from the client container:

    /proto/kit upload -url http://server-a:8080/upload -host kit.mm.test -size 1G [-chunked]
    curl -s -H 'Host: kit.mm.test' 'http://server-a:8080/gen?size=1G' | sha256sum ; /proto/kit gensha -size 1G
    /proto/kit sse -url 'http://server-a:8080/sse?n=300' -host kit.mm.test -n 300
    curl -s -m 400 -H 'Host: kit.mm.test' 'http://server-a:8080/slow?d=3m'
    /proto/kit ws -url ws://server-a:8080/ws -host kit.mm.test -size 16M -n 4 [-idle 10m]
    curl -k --http2 --connect-to kit.mm.test:8443:server-a:8443 https://kit.mm.test:8443/
    /proto/kit grpc -addr server-a:10005 ; /proto/kit grpc -addr server-a:10006 -tls
    /proto/kit grpc -addr server-a:8443 -tls -authority kit.mm.test
    ssh -i /keys/id -p 10001 tester@server-a
    psql postgres://app:app@server-a:10002/app ; redis-cli -h server-a -p 10003
    /proto/kit halfclose -addr server-a:10004 -size 1M
    openssl s_client -connect server-a:8444 -servername pt.mm.test
    ssh -o ProxyCommand='/proto/kit stream -api server-a:8081 -agent ag_bootstrap -target sshd:22 -token proto-admin-token-0123456789abcdef' -i /keys/id tester@sshd
    /proto/kit par -url http://server-a:8080/gen -host kit.mm.test -n 200 -size 10M

To test the clientless front door (row 5), run this from the `lan` container: `sshpass -p mm_proto_bootstrap_token_0123456789 ssh -N -R 80:kit:8080 front@server-a -p 2222`.

## Published chart smoke check

`check-published-helm.ps1` checks the existing isolated `k3d-mmhelm` cluster, namespace `mm-beta-proof`, published server release `mm-beta`, and a `kit` fixture pod. It verifies authenticated API access, owner login, invitation enforcement, CSRF rejection, endpoint URLs, and bundled UI assets. It uses a test-only account and refuses other cluster contexts.

    pwsh -NoProfile -File .claude/scripts/protocols/check-published-helm.ps1

## Sustained traffic and database recovery

`soak-published-helm.py` sends continuous batches of 20 concurrent checksum-verified 1 MiB downloads through the published Helm deployment. It repeats gRPC and WebSocket probes, records resource samples, and fails on transfer errors or pod restarts. It requires the same isolated cluster and fixture pod as the smoke check. The output must be a new file.

    python .claude/scripts/protocols/soak-published-helm.py --minutes 30 --output <scratch>/soak.jsonl

`check-backup-restore.py` dumps the isolated Helm database, restores it into fresh Docker containers, compares every durable table by row count and sorted row-content hash, logs in using the existing fixture owner, and reconnects the backed-up agent identity for verified tunnel downloads. It uses the existing `proto-cluster_edge` and `proto-cluster_private` networks and client/backend fixtures. The output directory and `mm-beta-restore-pg`, `mm-beta-restored-server`, `mm-beta-restored-agent` container names must be unused. It does not modify the source database or publish any host ports. Backups contain credentials and must be stored securely; this harness contains only test accounts.

    python .claude/scripts/protocols/check-backup-restore.py --output <scratch>/recovery

`check-database-stall.py` pauses only the isolated `mm-beta-restore-pg` fixture, proves the candidate `mm-beta-deadline-server` returns a safe retryable 503 at the default five-second query deadline, always resumes the fixture, and checks recovery. The candidate must use the restored database and the fixture account.

    python .claude/scripts/protocols/check-database-stall.py --output <scratch>/database-stall.json

These runs prove the stated duration and fixture behavior. Public DNS, publicly trusted TLS, identity-provider credentials, off-site backup retention, native ARM hardware, and multi-day capacity require separate deployment validation.

## Clean up

    timeout 120 docker compose -p proto down -v
