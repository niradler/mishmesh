import json
import os
from pathlib import Path
import secrets
import subprocess
import sys
import time


def run(arguments, environment, timeout):
    return subprocess.run(arguments, env=environment, timeout=timeout, capture_output=True, text=True)


def main():
    root = Path(__file__).resolve().parents[3]
    name = "mishmesh-pluggable-pg-" + str(time.time_ns())
    password = secrets.token_hex(24)
    environment = dict(os.environ)
    environment["POSTGRES_PASSWORD"] = password
    environment["MISHMESH_TEST_POSTGRES_DSN"] = f"postgres://postgres:{password}@127.0.0.1:5432/mishmesh?sslmode=disable"
    environment["MISHMESH_TEST_RETAIN_FIXTURES"] = "true"
    directory = root / "output" / name
    directory.mkdir(parents=True)
    private = directory / "fixture-env.json"
    private.write_text(json.dumps({key: environment[key] for key in ("MISHMESH_TEST_POSTGRES_DSN", "MISHMESH_TEST_RETAIN_FIXTURES")}), encoding="utf-8")
    private.chmod(0o600)
    created = run(["docker", "run", "--detach", "--name", name, "--env", "POSTGRES_PASSWORD", "--env", "POSTGRES_DB=mishmesh", "postgres:17"], environment, 90)
    if created.returncode:
        print("isolated PostgreSQL fixture creation failed", file=sys.stderr)
        return created.returncode
    print(f"Retained PostgreSQL fixture {name}; no published ports", flush=True)
    for attempt in range(30):
        ready = run(["docker", "exec", name, "pg_isready", "-U", "postgres", "-d", "mishmesh"], environment, 10)
        if ready.returncode == 0:
            break
        time.sleep(1)
    else:
        raise RuntimeError("isolated PostgreSQL fixture did not become ready; retained for inspection")
    command = ["docker", "run", "--name", name + "-runner", "--network", "container:" + name,
               "--mount", "type=bind,source=" + root.as_posix() + ",target=/src,readonly",
               "--mount", "type=volume,source=mishmesh-go-modules,target=/go/pkg/mod",
               "--mount", "type=volume,source=mishmesh-go-build-cache,target=/root/.cache/go-build",
               "--env", "MISHMESH_TEST_POSTGRES_DSN", "--env", "MISHMESH_TEST_RETAIN_FIXTURES",
               "--workdir", "/src", "golang:1.26.9", "go", "test", "-race", "-count=1", "-timeout=180s", "-v", "./internal/store/postgres"]
    checked = run(command, environment, 240)
    output = (checked.stdout + checked.stderr).replace(password, "<redacted>")
    (directory / "test.log").write_text(output, encoding="utf-8")
    print(output, end="")
    print(f"Fixture and runner retained. Evidence: {directory / 'test.log'}")
    return checked.returncode


if __name__ == "__main__":
    sys.exit(main())
