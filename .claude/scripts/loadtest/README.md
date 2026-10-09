# mishmesh load tests

Everything runs in Docker (project `w5`); the Windows host is not in the data path.

Use only isolated test networks. The instrumented server exposes unauthenticated profiling and runtime data, and the fixture disables control API authentication. Use the published server image for customer validation; this instrumented server is for profiling only. No ports are published by the fixture.

## Layout

- `lt/` single binary, subcommands `backend`, `agents`, `load`, `lb`
- `ltserver/` product wiring plus `/debug/pprof` and `/debug/rt` (the real server has no pprof); `LT_PG_POOL=N` bounds the Postgres pool
- `compose.yml` postgres, redis, server-a, server-b, lt (tool container)
- `env.sh` exports and the `dc` / `ltx` helpers; `sample.sh` samples RSS, threads, fds, CPU from `/proc/1`
- `run-throughput.sh <variant> <agents> "<concs>" <dur> <node>` runs 1KB and 1MB loads
- `probe.sh <node> <prefix> <n>` prints status code counts across n agent subdomains

## Build (linux/amd64, into $W5_BIN)

    GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o $W5_BIN/lt ./.claude/scripts/loadtest/lt
    GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o $W5_BIN/ltserver ./.claude/scripts/loadtest/ltserver
    GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o $W5_BIN/mishmesh-server ./cmd/mishmesh-server

## Run

    source env.sh
    export CLUSTER=false CONN_BACKEND=memory PG_MAX_CONN=1000 SERVER_BIN=/bin/w5/ltserver LT_PG_POOL=32
    dc up -d postgres redis server-a lt
    ltx backend &
    dc exec -T -d lt sh -c "/bin/w5/lt agents -prefix p -steps 100,1000,5000 -rate 250 -hold 20s"
    ./sample.sh server-a $W5_OUT/samples.csv 1
    ./run-throughput.sh variant 200 "50 200 1000" 10s server-a
    curl server-a:8081/debug/pprof/profile?seconds=10 (from the lt container)

Cluster tests: `CLUSTER=true CONN_BACKEND=redis SERVER_BIN=/bin/w5/mishmesh-server`, `dc up -d server-a server-b`, start server-b a few seconds after server-a (concurrent first start races on migration), run `run-throughput.sh cluster-local ... server-a` and `cluster-relay ... server-b`.

Resilience: start `lt lb -map 127.0.0.1:9080=server-a:8080,server-b:8080 -map 127.0.0.1:9081=server-a:8081,server-b:8081`, agents with `-gateway ws://127.0.0.1:9081 -timeline`, load with `-addr 127.0.0.1:9080 -timeline`, then `docker kill w5-server-a-1` / `docker restart`.

## Clean DB

Token cache lives in `$W5_OUT/tokens-*`. After `dc rm -sfv postgres server-a server-b` delete it, or use a new agent prefix.

## Clean up

    dc down -v
