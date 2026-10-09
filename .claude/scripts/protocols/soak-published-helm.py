import argparse
import datetime
import json
import pathlib
import subprocess
import time


def run(arguments, timeout=45):
    result = subprocess.run(arguments, capture_output=True, text=True, timeout=timeout)
    if result.returncode:
        raise RuntimeError(f"Command failed: {arguments[0]} ({result.returncode}): {result.stderr[-1000:]}")
    return result.stdout.strip()


def probe(arguments):
    return run(["kubectl", "--context", "k3d-mmhelm", "--request-timeout=40s", "exec", "-n", "mm-beta-proof", "kit", "--", "/proto/kit", *arguments])


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--minutes", type=int, default=30)
    parser.add_argument("--output", type=pathlib.Path, required=True)
    args = parser.parse_args()
    if not 1 <= args.minutes <= 1440:
        parser.error("minutes must be between 1 and 1440")
    context = run(["kubectl", "config", "current-context"])
    if context != "k3d-mmhelm":
        raise RuntimeError("Requires the isolated k3d-mmhelm cluster")
    args.output.parent.mkdir(parents=True, exist_ok=True)
    started = time.monotonic()
    deadline = started + args.minutes * 60
    batches = 0
    with args.output.open("x", encoding="utf-8") as log:
        while time.monotonic() < deadline:
            result = probe(["par", "-url", "http://mm-beta-public:80/gen", "-host", "kit.beta.test", "-n", "20", "-size", "1M"])
            if "ok=20 bad=0" not in result:
                raise RuntimeError(result)
            batches += 1
            record = {"utc": datetime.datetime.now(datetime.timezone.utc).isoformat(), "elapsed_seconds": round(time.monotonic() - started, 2), "batch": batches, "http": result}
            if batches == 1 or batches % 30 == 0:
                record["grpc"] = probe(["grpc", "-addr", "mm-beta-public:443", "-tls", "-authority", "kit.beta.test", "-timeout", "30s"])
                record["websocket"] = probe(["ws", "-url", "ws://mm-beta-public:80/ws", "-host", "kit.beta.test", "-n", "4", "-size", "1M"])
                pods = json.loads(run(["kubectl", "--context", context, "--request-timeout=20s", "get", "pods", "-n", "mm-beta-proof", "-o", "json"]))
                record["pods"] = [{"name": pod["metadata"]["name"], "statuses": [{"ready": item["ready"], "restarts": item["restartCount"]} for item in pod.get("status", {}).get("containerStatuses", [])]} for pod in pods["items"]]
                if any(not item["ready"] or item["restarts"] for pod in record["pods"] for item in pod["statuses"]):
                    raise RuntimeError("Pod became unready or restarted")
                record["resources"] = run(["kubectl", "--context", context, "--request-timeout=20s", "top", "pods", "-n", "mm-beta-proof"])
                print(json.dumps(record), flush=True)
            log.write(json.dumps(record) + "\n")
            log.flush()
            time.sleep(min(1, max(0, deadline - time.monotonic())))
        summary = {"passed": True, "elapsed_seconds": round(time.monotonic() - started, 2), "batches": batches, "verified_downloads": batches * 20, "verified_bytes": batches * 20 * 1048576}
        log.write(json.dumps(summary) + "\n")
        print(json.dumps(summary), flush=True)


if __name__ == "__main__":
    main()
