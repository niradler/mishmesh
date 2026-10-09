import subprocess
import time


def docker(*arguments, timeout=30, check=True):
    return subprocess.run(
        ["docker", *arguments],
        capture_output=True,
        text=True,
        timeout=timeout,
        check=check,
    )


def main():
    transfer = subprocess.Popen([
        "docker", "exec", "proto-client-1", "curl", "-fsS",
        "--max-time", "35", "--limit-rate", "1M",
        "-H", "Host: kit.mm.test", "http://server-a:8080/gen?size=1G",
        "-o", "/tmp/interrupted-download",
    ], stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
    deadline = time.monotonic() + 10
    while time.monotonic() < deadline:
        result = docker("exec", "proto-client-1", "stat", "-c", "%s", "/tmp/interrupted-download", check=False)
        if result.returncode == 0 and int(result.stdout.strip()) > 0:
            break
        if transfer.poll() is not None:
            raise RuntimeError("transfer ended before agent kill")
        time.sleep(0.2)
    else:
        raise RuntimeError("transfer did not start within 10 seconds")
    try:
        docker("kill", "proto-agent-1")
        _, error = transfer.communicate(timeout=40)
        assert transfer.returncode != 0, "interrupted 1 GiB download unexpectedly succeeded"
        print(f"PASS interrupted transfer fails explicitly: curl exit {transfer.returncode}; {error.strip()}")
    finally:
        docker("start", "proto-agent-1")
    deadline = time.monotonic() + 30
    while time.monotonic() < deadline:
        result = docker("exec", "proto-client-1", "curl", "-fsS", "--max-time", "3", "-H", "Host: kit.mm.test", "http://server-a:8080/", check=False)
        if result.returncode == 0 and "kit ok" in result.stdout:
            break
        time.sleep(0.5)
    else:
        raise RuntimeError("agent did not recover within 30 seconds")
    result = docker("exec", "proto-client-1", "/proto/kit", "par", "-url", "http://server-a:8080/gen", "-host", "kit.mm.test", "-n", "20", "-size", "1M", timeout=60)
    print("PASS agent reconnect and fresh transfers:", result.stdout.strip())


if __name__ == "__main__":
    main()
