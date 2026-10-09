import argparse
import json
import pathlib
import subprocess
import time


def docker(arguments, timeout=25):
    result = subprocess.run(["docker", *arguments], capture_output=True, text=True, timeout=timeout)
    if result.returncode:
        raise RuntimeError(f"Docker command failed ({result.returncode}): {result.stderr[-1000:]}")
    return result.stdout.strip()


def curl(arguments):
    return docker(["exec", "proto-cluster-client-1", "curl", "--max-time", "10", "-sS", *arguments])


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", type=pathlib.Path, required=True)
    parser.add_argument("--server", choices=["mm-beta-deadline-server", "mm-beta-published-deadline", "mm-beta-published-binary"], default="mm-beta-deadline-server")
    args = parser.parse_args()
    api = f"http://{args.server}:8081"
    cookie = "/tmp/mm-deadline-cookie"
    login = '{"email":"beta-proof@example.test","password":"beta-proof-test-only-password"}'
    status = curl(["-o", "/dev/null", "-w", "%{http_code}", "-c", cookie, "-H", "Content-Type: application/json", "--data-binary", login, api + "/api/v1/auth/login"])
    if status != "200":
        raise RuntimeError(f"Baseline login status {status}")
    docker(["pause", "mm-beta-restore-pg"])
    try:
        started = time.monotonic()
        status = curl(["-o", "/tmp/mm-stall-body", "-D", "/tmp/mm-stall-headers", "-w", "%{http_code}", "-b", cookie, api + "/api/v1/endpoints"])
        elapsed = time.monotonic() - started
        body = docker(["exec", "proto-cluster-client-1", "cat", "/tmp/mm-stall-body"])
        headers = docker(["exec", "proto-cluster-client-1", "cat", "/tmp/mm-stall-headers"])
        if status != "503" or not 4 <= elapsed <= 8:
            raise RuntimeError(f"Stall status {status}, elapsed {elapsed:.2f}s")
        if "Retry-After: 1" not in headers or "temporarily unavailable" not in body or "postgres" in body or "password" in body:
            raise RuntimeError("Stall response lacks safe retry semantics")
    finally:
        docker(["unpause", "mm-beta-restore-pg"])
    recovered = curl(["-f", "-b", cookie, api + "/api/v1/endpoints"])
    if len(json.loads(recovered)) != 1:
        raise RuntimeError("Endpoint did not recover")
    summary = {"passed": True, "server": args.server, "stalled_api_status": status, "stalled_api_seconds": round(elapsed, 2), "retry_after": 1, "generic_error": True, "recovered_endpoint_count": 1}
    args.output.write_text(json.dumps(summary, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(summary), flush=True)


if __name__ == "__main__":
    main()
