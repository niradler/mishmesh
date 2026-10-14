import pathlib
import subprocess

workspace = pathlib.Path(__file__).resolve().parents[2]
evidence = workspace / ".superpowers/sdd/2026-10-13-pluggable-types/task-14-checks.log"
commands = [
    (["go", "test", "-race", "-count=1", "-timeout=150s", "./internal/connections/...", "./internal/cloudflare", "./internal/ingress", "./internal/gateway", "./internal/services", "./internal/store/sqlite"], 180),
    (["go", "test", "-race", "-count=1", "-timeout=150s", "./internal/controlplane", "-run", "Test.*(Access|Connection|Service|Requires|Topology|Public|Network|HostCommands|DriverDescriptors|Restart|Typed|PostCommit|Sensitive)"], 180),
    (["go", "vet", "./internal/connections/...", "./internal/cloudflare", "./internal/controlplane", "./internal/ingress", "./internal/gateway", "./internal/services", "./internal/store/..."], 120),
    (["go", "build", "./cmd/..."], 120),
]
with evidence.open("w", encoding="utf-8") as output:
    for command, timeout in commands:
        heading = "COMMAND " + " ".join(command)
        print(heading, flush=True)
        output.write(heading + "\n")
        result = subprocess.run(command, cwd=workspace, timeout=timeout, capture_output=True, text=True)
        combined = result.stdout + result.stderr
        print(combined, end="", flush=True)
        output.write(combined + "EXIT " + str(result.returncode) + "\n")
        output.flush()
        if result.returncode:
            raise SystemExit(result.returncode)
