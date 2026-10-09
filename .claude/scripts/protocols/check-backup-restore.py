import argparse
import hashlib
import json
import pathlib
import subprocess
import time


def run(arguments, data=None, timeout=60):
    result = subprocess.run(arguments, input=data, capture_output=True, timeout=timeout)
    if result.returncode:
        raise RuntimeError(f"{arguments[0]} failed ({result.returncode}): {result.stderr.decode(errors='replace')[-1000:]}")
    return result.stdout


def source_query(query):
    return run(["kubectl", "--context", "k3d-mmhelm", "--request-timeout=40s", "exec", "-n", "mm-beta-proof", "mm-beta-postgres-0", "--", "psql", "-U", "mishmesh", "-d", "mishmesh", "-At", "-c", query]).decode().strip()


def restored_query(query):
    return run(["docker", "exec", "mm-beta-restore-pg", "psql", "-U", "mishmesh", "-d", "mishmesh", "-At", "-c", query]).decode().strip()


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", type=pathlib.Path, required=True)
    args = parser.parse_args()
    if run(["kubectl", "config", "current-context"]).decode().strip() != "k3d-mmhelm":
        raise RuntimeError("Requires isolated k3d-mmhelm test cluster")
    args.output.mkdir(parents=True, exist_ok=False)
    started = time.monotonic()
    tables = source_query("SELECT tablename FROM pg_tables WHERE schemaname='public' ORDER BY tablename").splitlines()
    snapshots = {}
    for table in tables:
        if not table.replace('_', '').isalnum():
            raise RuntimeError("Unexpected table name")
        query = f'SELECT count(*), md5(coalesce(string_agg(row_to_json(t)::text, E\'\\n\' ORDER BY row_to_json(t)::text), \'\')) FROM "{table}" t'
        snapshots[table] = source_query(query)
    backup = run(["kubectl", "--context", "k3d-mmhelm", "--request-timeout=60s", "exec", "-n", "mm-beta-proof", "mm-beta-postgres-0", "--", "pg_dump", "-U", "mishmesh", "-d", "mishmesh", "-Fc", "--no-owner", "--no-acl"], timeout=75)
    backup_path = args.output / "mishmesh.dump"
    backup_path.write_bytes(backup)
    run(["docker", "run", "-d", "--name", "mm-beta-restore-pg", "--network", "proto-cluster_edge", "-e", "POSTGRES_USER=mishmesh", "-e", "POSTGRES_DB=mishmesh", "-e", "POSTGRES_PASSWORD=restore-test-only-password", "postgres:16-alpine"])
    deadline = time.monotonic() + 45
    while True:
        result = subprocess.run(["docker", "exec", "mm-beta-restore-pg", "pg_isready", "-U", "mishmesh"], capture_output=True, timeout=10)
        if not result.returncode:
            break
        if time.monotonic() >= deadline:
            raise RuntimeError("Restored database startup deadline exceeded")
        time.sleep(1)
    run(["docker", "exec", "-i", "mm-beta-restore-pg", "pg_restore", "-U", "mishmesh", "-d", "mishmesh", "--no-owner", "--no-acl", "--exit-on-error"], data=backup)
    verified = {}
    for table in tables:
        query = f'SELECT count(*), md5(coalesce(string_agg(row_to_json(t)::text, E\'\\n\' ORDER BY row_to_json(t)::text), \'\')) FROM "{table}" t'
        actual = restored_query(query)
        if actual != snapshots[table]:
            raise RuntimeError(f"Restored table mismatch: {table}")
        verified[table] = int(actual.split('|')[0])
    run(["docker", "run", "-d", "--name", "mm-beta-restored-server", "--network", "proto-cluster_edge", "-e", "MISHMESH_DATA_BACKEND=postgres", "-e", "MISHMESH_DATA_DSN=postgres://mishmesh:restore-test-only-password@mm-beta-restore-pg:5432/mishmesh?sslmode=disable", "-e", "MISHMESH_API_ADDR=0.0.0.0:8081", "-e", "MISHMESH_BASE_DOMAIN=beta.test", "-e", "MISHMESH_AUTH_ENABLED=true", "-e", "MISHMESH_API_AUTH_TOKEN=restore-test-only-api-token-0123456789", "-e", "MISHMESH_BOOTSTRAP_TOKEN=mm_beta_proof_test_only_0123456789", "-e", "MISHMESH_PATH_ROUTING=false", "ghcr.io/niradler/mishmesh-server:0.1.0-beta.1"])
    payload = '{"email":"beta-proof@example.test","password":"beta-proof-test-only-password"}'
    deadline = time.monotonic() + 45
    while True:
        result = subprocess.run(["docker", "exec", "proto-cluster-client-1", "curl", "--max-time", "5", "-fsS", "-c", "/tmp/mm-restored-cookie", "-H", "Content-Type: application/json", "--data-binary", payload, "http://mm-beta-restored-server:8081/api/v1/auth/login"], capture_output=True, timeout=10)
        if not result.returncode:
            break
        if time.monotonic() >= deadline:
            raise RuntimeError("Restored application login deadline exceeded")
        time.sleep(1)
    me = json.loads(run(["docker", "exec", "proto-cluster-client-1", "curl", "--max-time", "10", "-fsS", "-b", "/tmp/mm-restored-cookie", "http://mm-beta-restored-server:8081/api/v1/auth/me"]))
    endpoints = json.loads(run(["docker", "exec", "proto-cluster-client-1", "curl", "--max-time", "10", "-fsS", "-b", "/tmp/mm-restored-cookie", "http://mm-beta-restored-server:8081/api/v1/endpoints"]))
    if me.get("role") != "owner" or me.get("active_org_id") != "org_default" or len(endpoints) != 1:
        raise RuntimeError("Restored application identity or endpoint mismatch")
    run(["docker", "run", "-d", "--name", "mm-beta-restored-agent", "--network", "proto-cluster_edge", "-e", "MISHMESH_GATEWAY_URL=ws://mm-beta-restored-server:8081", "-e", "MISHMESH_TOKEN=mm_beta_proof_test_only_0123456789", "ghcr.io/niradler/mishmesh-agent:0.1.0-beta.1", "http", "kit:8080", "--subdomain", "kit", "--reserved"])
    run(["docker", "network", "connect", "proto-cluster_private", "mm-beta-restored-agent"])
    deadline = time.monotonic() + 45
    while True:
        result = subprocess.run(["docker", "exec", "proto-cluster-client-1", "/proto/kit", "par", "-url", "http://mm-beta-restored-server:8080/gen", "-host", "kit.beta.test", "-n", "20", "-size", "1M"], capture_output=True, timeout=15)
        if not result.returncode and b"ok=20 bad=0" in result.stdout:
            break
        if time.monotonic() >= deadline:
            raise RuntimeError("Restored tunnel traffic deadline exceeded")
        time.sleep(1)
    summary = {"passed": True, "elapsed_seconds": round(time.monotonic() - started, 2), "backup_bytes": len(backup), "backup_sha256": hashlib.sha256(backup).hexdigest(), "tables_verified": verified, "published_server_owner_login": True, "published_server_endpoint_access": True, "restored_tunnel_verified_downloads": 20, "source_database_untouched": True}
    (args.output / "result.json").write_text(json.dumps(summary, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(summary), flush=True)


if __name__ == "__main__":
    main()
