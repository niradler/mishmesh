import argparse
import json
import pathlib
import subprocess


def run(args, timeout=40):
    return subprocess.run(args, capture_output=True, text=True, check=True, timeout=timeout).stdout


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", type=pathlib.Path, required=True)
    parser.add_argument("--repetitions", type=int, default=20)
    args = parser.parse_args()
    if not 1 <= args.repetitions <= 100:
        raise ValueError("repetitions must be between 1 and 100")
    if args.output.exists():
        raise ValueError("output must be a new file")
    if run(["kubectl", "config", "current-context"]).strip() != "k3d-mmhelm":
        raise RuntimeError("Requires isolated k3d-mmhelm cluster")
    kube = ["kubectl", "--context", "k3d-mmhelm", "--request-timeout=30s"]
    address = run(kube + ["get", "svc", "traefik", "-n", "kube-system", "-o", "jsonpath={.spec.clusterIP}"]).strip()
    client = kube + ["exec", "-n", "mm-beta-proof", "kit", "--"]
    host = "edgekit.tunnel.beta.test"
    body = run(client + ["curl", "--max-time", "15", "-fsS", "--cacert", "/tmp/mm-proof-ca.crt", "--connect-to", f"{host}:443:{address}:443", f"https://{host}/"])
    if not body.startswith("kit ok"):
        raise RuntimeError("Verified HTTPS front door failed")
    for _ in range(args.repetitions):
        result = run(client + ["/proto/kit", "grpc", "-addr", f"{address}:443", "-tls", "-authority", host, "-timeout", "20s"])
        if "unary ok" not in result or "bidi ok msgs=100 size=65536" not in result:
            raise RuntimeError("gRPC front-door probe failed")
    pod = json.loads(run(kube + ["get", "pod", "edge-agent", "-n", "mm-beta-edge-proof", "-o", "json"]))
    if not all(s["ready"] and s["restartCount"] == 0 for s in pod["status"]["containerStatuses"]):
        raise RuntimeError("Verified WSS agent is not healthy")
    summary = {"passed": True, "https_chain_and_hostname_verified": True, "grpc_unary_runs": args.repetitions, "grpc_bidi_messages": args.repetitions * 100, "grpc_message_bytes": 65536, "wss_agent_ready_without_restarts": True, "publicly_trusted_ca": False}
    args.output.write_text(json.dumps(summary, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(summary), flush=True)


if __name__ == "__main__":
    main()
