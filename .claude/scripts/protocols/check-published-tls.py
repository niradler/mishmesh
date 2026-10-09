import argparse
import json
import pathlib
import subprocess


def curl(arguments):
    return subprocess.run(["kubectl", "--context", "k3d-mmhelm", "--request-timeout=30s", "exec", "-n", "mm-beta-proof", "kit", "--", "curl", "--max-time", "15", "-fsS", *arguments], capture_output=True, text=True, timeout=40)


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", type=pathlib.Path, required=True)
    args = parser.parse_args()
    context = subprocess.run(["kubectl", "config", "current-context"], capture_output=True, text=True, check=True, timeout=15).stdout.strip()
    if context != "k3d-mmhelm":
        raise RuntimeError("Requires isolated k3d-mmhelm cluster")
    trusted = curl(["--cacert", "/tmp/mm-proof-ca.crt", "--connect-to", "kit.beta.test:443:mm-beta-public:443", "https://kit.beta.test/"])
    if trusted.returncode or not trusted.stdout.startswith("kit ok"):
        raise RuntimeError("Verified TLS request failed")
    untrusted = curl(["--connect-to", "kit.beta.test:443:mm-beta-public:443", "https://kit.beta.test/"])
    if untrusted.returncode != 60:
        raise RuntimeError(f"Untrusted certificate accepted or unrelated error: {untrusted.returncode}")
    wrong_host = curl(["--cacert", "/tmp/mm-proof-ca.crt", "--connect-to", "wrong.example.test:443:mm-beta-public:443", "https://wrong.example.test/"])
    if wrong_host.returncode != 60:
        raise RuntimeError(f"Wrong certificate hostname accepted or unrelated error: {wrong_host.returncode}")
    summary = {"passed": True, "certificate_chain_and_hostname_verified": True, "untrusted_certificate_rejected": True, "wrong_hostname_rejected": True, "publicly_trusted_ca": False}
    args.output.write_text(json.dumps(summary, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(summary), flush=True)


if __name__ == "__main__":
    main()
