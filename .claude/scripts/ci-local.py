import argparse
import io
import json
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile
import time
import uuid
from pathlib import Path

ROOT = Path(__file__).resolve().parents[2]
STAGES = ["go", "web", "helm", "docker"]
STAGE_DEADLINES = {"go": 1500, "web": 900, "helm": 300, "docker": 1500}
STAGE_PATHS = {
    "go": [r"\.go$", r"^go\.(mod|sum)$", r"^\.claude/scripts/loadtest/"],
    "web": [r"^web/"],
    "helm": [r"^deploy/helm/", r"^\.claude/scripts/verify-helm-security\.py$"],
    "docker": [r"^Dockerfile", r"^\.dockerignore$", r"^go\.(mod|sum)$", r"^web/package(-lock)?\.json$"],
}
GOVULNCHECK = "golang.org/x/vuln/cmd/govulncheck@v1.8.0"
POSTGRES_IMAGE = "postgres:16-alpine"
NODE_IMAGE = "node:22"

GO_SCRIPT = f"""
set -euo pipefail
unformatted="$(gofmt -l $(find . -name '*.go' -not -path './web/*'))"
if [ -n "$unformatted" ]; then echo "gofmt needed:"; echo "$unformatted"; exit 1; fi
go vet ./...
go vet ./.claude/scripts/loadtest/lt ./.claude/scripts/loadtest/ltserver
go build -o /tmp/mishmesh-loadtest ./.claude/scripts/loadtest/lt
go build -o /tmp/mishmesh-loadtest-server ./.claude/scripts/loadtest/ltserver
go test -race -count=1 -timeout=300s $(go list ./... | grep -v '/internal/store/postgres$')
go test -race -count=1 -timeout=300s -v ./internal/store/postgres/ | tee /tmp/pg-tests.log
if grep -q -- '--- SKIP' /tmp/pg-tests.log; then echo "postgres store tests were skipped"; exit 1; fi
go run {GOVULNCHECK} ./...
"""

WEB_SCRIPT = """
set -euo pipefail
cd web
npm ci --no-fund --no-audit
npm run build
npm audit --omit=dev --audit-level=high
"""


class StageFailed(Exception):
    pass


def run(args, timeout, cwd=None, capture=False):
    print("+ " + " ".join(args), flush=True)
    try:
        result = subprocess.run(args, cwd=cwd, timeout=timeout, text=True, capture_output=capture)
    except subprocess.TimeoutExpired as exc:
        raise StageFailed(f"timed out after {timeout}s: {' '.join(args)}") from exc
    if result.returncode != 0:
        if capture:
            sys.stdout.write(result.stdout)
            sys.stderr.write(result.stderr)
        raise StageFailed(f"exit {result.returncode}: {' '.join(args)}")
    return result.stdout if capture else ""


def git(*args, timeout=30):
    return subprocess.run(["git", *args], cwd=ROOT, timeout=timeout, text=True, capture_output=True)


def go_version():
    for line in (ROOT / "go.mod").read_text().splitlines():
        if line.startswith("go "):
            return line.split()[1]
    raise StageFailed("go.mod is missing its Go version")


def export_tree(rev, dest):
    archive = subprocess.run(["git", "archive", "--format=tar", rev], cwd=ROOT, timeout=120, capture_output=True)
    if archive.returncode != 0:
        raise StageFailed("git archive failed: " + archive.stderr.decode(errors="replace"))
    with tarfile.open(fileobj=io.BytesIO(archive.stdout)) as tar:
        tar.extractall(dest, filter="data")


def resolve_base(base):
    if base != "auto":
        return base
    for candidate in ("@{upstream}", "origin/main"):
        if git("rev-parse", "--verify", "--quiet", candidate).returncode == 0:
            return candidate
    return None


def changed_files(base, rev):
    if base is None:
        return None
    merge_base = git("merge-base", base, rev)
    if merge_base.returncode != 0:
        return None
    diff = git("diff", "--name-only", merge_base.stdout.strip(), rev)
    if diff.returncode != 0:
        return None
    return [line for line in diff.stdout.splitlines() if line]


def relevant(stage, files):
    if files is None:
        return True
    patterns = [re.compile(p) for p in STAGE_PATHS[stage]]
    return any(p.search(f) for f in files for p in patterns)


def wait_healthy(container, deadline):
    end = time.monotonic() + deadline
    while time.monotonic() < end:
        state = run(["docker", "inspect", "--format", "{{json .State.Health.Status}}", container], timeout=30, capture=True)
        if json.loads(state) == "healthy":
            return
        time.sleep(2)
    raise StageFailed(f"{container} did not become healthy within {deadline}s")


def stage_go(tree, run_id):
    network = f"mishmesh-ci-{run_id}"
    postgres = f"mishmesh-ci-pg-{run_id}"
    run(["docker", "network", "create", network], timeout=60, capture=True)
    try:
        run([
            "docker", "run", "-d", "--rm", "--name", postgres, "--network", network,
            "-e", "POSTGRES_PASSWORD=postgres",
            "--health-cmd", "pg_isready -U postgres", "--health-interval", "2s",
            POSTGRES_IMAGE,
        ], timeout=300, capture=True)
        wait_healthy(postgres, 120)
        run([
            "docker", "run", "--rm", "--name", f"mishmesh-ci-go-{run_id}", "--network", network,
            "--mount", f"type=bind,source={tree},target=/src",
            "--mount", "type=volume,source=mishmesh-go-modules,target=/go/pkg/mod",
            "--mount", "type=volume,source=mishmesh-go-build-cache,target=/root/.cache/go-build",
            "-e", f"MISHMESH_TEST_POSTGRES_DSN=postgres://postgres:postgres@{postgres}:5432/postgres?sslmode=disable",
            "--workdir", "/src", f"golang:{go_version()}", "bash", "-c", GO_SCRIPT,
        ], timeout=STAGE_DEADLINES["go"])
    finally:
        subprocess.run(["docker", "rm", "-f", postgres], timeout=60, capture_output=True)
        subprocess.run(["docker", "network", "rm", network], timeout=60, capture_output=True)


def stage_web(tree, run_id):
    run([
        "docker", "run", "--rm", "--name", f"mishmesh-ci-web-{run_id}",
        "--mount", f"type=bind,source={tree},target=/src",
        "--mount", "type=volume,source=mishmesh-npm-cache,target=/root/.npm",
        "--workdir", "/src", NODE_IMAGE, "bash", "-c", WEB_SCRIPT,
    ], timeout=STAGE_DEADLINES["web"])


def stage_helm(tree, run_id):
    if shutil.which("helm") is None:
        raise StageFailed("helm is not on PATH; install it from https://helm.sh/docs/intro/install/")
    run(["helm", "lint", "deploy/helm/mishmesh", "deploy/helm/mishmesh-agent"], timeout=120, cwd=tree)
    for values in sorted((tree / "deploy/helm/mishmesh").glob("values-*.yaml")):
        run(["helm", "template", "t", "deploy/helm/mishmesh", "-f", str(values)], timeout=60, cwd=tree, capture=True)
    run([sys.executable, ".claude/scripts/verify-helm-security.py"], timeout=180, cwd=tree)


def stage_docker(tree, run_id):
    for dockerfile, tag in (("Dockerfile", "mishmesh-server:ci"), ("Dockerfile.agent", "mishmesh-agent:ci")):
        run(["docker", "build", "-q", "-f", dockerfile, "-t", tag, "."], timeout=STAGE_DEADLINES["docker"], cwd=tree)


STAGE_RUNNERS = {"go": stage_go, "web": stage_web, "helm": stage_helm, "docker": stage_docker}


def parse_args():
    parser = argparse.ArgumentParser(description="Run the ci.yml checks locally against a clean export of a commit.")
    parser.add_argument("--rev", default="HEAD", help="commit to check (default HEAD)")
    parser.add_argument("--stages", default=",".join(STAGES), help="comma list of " + ",".join(STAGES))
    parser.add_argument("--changed-since", default=None, help="skip stages with no changed files since this ref; 'auto' uses the upstream or origin/main")
    return parser.parse_args()


def main():
    args = parse_args()
    stages = [s.strip() for s in args.stages.split(",") if s.strip()]
    unknown = [s for s in stages if s not in STAGES]
    if unknown:
        print("unknown stages: " + ", ".join(unknown), file=sys.stderr)
        return 2
    files = changed_files(resolve_base(args.changed_since), args.rev) if args.changed_since else None
    run_id = uuid.uuid4().hex[:10]
    results = {}
    with tempfile.TemporaryDirectory(prefix="mishmesh-ci-") as tmp:
        tree = Path(tmp)
        export_tree(args.rev, tree)
        for stage in stages:
            if not relevant(stage, files):
                results[stage] = "skipped (no relevant changes)"
                continue
            print(f"\n=== {stage} ===", flush=True)
            started = time.monotonic()
            try:
                STAGE_RUNNERS[stage](tree, run_id)
                results[stage] = f"ok ({time.monotonic() - started:.0f}s)"
            except StageFailed as exc:
                results[stage] = f"FAILED: {exc}"
                break
    print("\n=== summary ===")
    for stage in stages:
        print(f"{stage:7} {results.get(stage, 'not run')}")
    return 1 if any(r.startswith("FAILED") for r in results.values()) else 0


if __name__ == "__main__":
    sys.exit(main())
