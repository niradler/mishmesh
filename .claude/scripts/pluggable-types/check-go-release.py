import argparse
import concurrent.futures
import os
import pathlib
import subprocess
import time

parser = argparse.ArgumentParser()
parser.add_argument("mode", choices=["windows-race", "linux-race", "build", "vuln", "linux-vuln", "linux-e2e"])
parser.add_argument("--evidence", required=True)
options = parser.parse_args()
workspace = pathlib.Path(__file__).resolve().parents[3]
evidence = pathlib.Path(options.evidence).resolve()
evidence.mkdir(parents=True, exist_ok=True)


def run(name, command, timeout, env=None):
    started = time.monotonic()
    result = subprocess.run(command, cwd=workspace, env=env, timeout=timeout, capture_output=True, text=True)
    output = "COMMAND " + repr(command) + "\n" + result.stdout + result.stderr
    output += f"\nEXIT {result.returncode}; seconds {time.monotonic()-started:.3f}\n"
    (evidence / (name + ".log")).write_text(output, encoding="utf-8")
    print(f"{name}: exit {result.returncode}; seconds {time.monotonic()-started:.3f}", flush=True)
    if result.returncode:
        print(result.stdout + result.stderr, flush=True)
    return result.returncode


if options.mode == "windows-race":
    raise SystemExit(run("windows-race", ["go", "test", "-race", "-count=1", "-timeout=180s", "./..."], 300))
if options.mode == "linux-race":
    raise SystemExit(run("linux-race", ["pwsh", "-NoProfile", "-File", ".claude/scripts/test-go.ps1", "-DockerDeadlineSeconds", "600"], 630))
if options.mode == "vuln":
    raise SystemExit(run("govulncheck", ["go", "run", "golang.org/x/vuln/cmd/govulncheck@v1.8.0", "./..."], 300))
if options.mode in ["linux-vuln", "linux-e2e"]:
    version = next(line.split()[1] for line in (workspace / "go.mod").read_text().splitlines() if line.startswith("go "))
    name = "mishmesh-go-check-" + str(time.time_ns())
    operation = ["go", "run", "golang.org/x/vuln/cmd/govulncheck@v1.8.0", "./..."] if options.mode == "linux-vuln" else ["go", "test", "-race", "-count=1", "-timeout=90s", "./internal/e2e"]
    command = ["docker", "run", "--name", name, "--mount", "type=bind,source=" + str(workspace) + ",target=/src,readonly", "--mount", "type=volume,source=mishmesh-go-modules,target=/go/pkg/mod", "--mount", "type=volume,source=mishmesh-go-build-cache,target=/root/.cache/go-build", "--workdir", "/src", "golang:" + version] + operation
    raise SystemExit(run("linux-govulncheck" if options.mode == "linux-vuln" else "linux-e2e-host-contract", command, 300))

results = [run("vet", ["go", "vet", "./..."], 120)]
results.append(run("loadtest-vet", ["go", "vet", "./.claude/scripts/loadtest/lt", "./.claude/scripts/loadtest/ltserver"], 120))
for name, package in [("loadtest", "./.claude/scripts/loadtest/lt"), ("loadtest-server", "./.claude/scripts/loadtest/ltserver")]:
    results.append(run(name + "-build", ["go", "build", "-o", str(evidence / (name + ".exe")), package], 120))


def crossbuild(platform):
    target_os, architecture = platform
    env = dict(os.environ, GOOS=target_os, GOARCH=architecture, CGO_ENABLED="0", GOMAXPROCS="4")
    status = 0
    for binary in ["mishmesh-server", "mishmesh-agent"]:
        name = f"{binary}-{target_os}-{architecture}"
        filename = name + (".exe" if target_os == "windows" else "")
        status = max(status, run(name, ["go", "build", "-p=4", "-trimpath", "-ldflags=-s -w -X main.version=task19-validation", "-o", str(evidence / filename), "./cmd/" + binary], 240, env))
    return status


with concurrent.futures.ThreadPoolExecutor(max_workers=2) as executor:
    results.extend(executor.map(crossbuild, [(target_os, architecture) for target_os in ["linux", "darwin", "windows"] for architecture in ["amd64", "arm64"]]))
raise SystemExit(max(results))
