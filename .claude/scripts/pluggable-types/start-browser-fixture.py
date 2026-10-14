import argparse
import json
import os
from pathlib import Path
import secrets
import socket
import subprocess


def main():
    parser = argparse.ArgumentParser(description="Start an isolated retained local browser fixture")
    parser.add_argument("--binary", default="mishmesh-server.exe")
    options = parser.parse_args()
    root = Path(__file__).resolve().parents[3]
    directory = root / "output" / "browser-real"
    binary = directory / options.binary
    if binary.parent != directory or binary.suffix != ".exe":
        raise RuntimeError("select a built fixture executable inside output/browser-real")
    if not binary.exists():
        raise RuntimeError("build the Windows server into output/browser-real first")
    for port in (8081, 5189):
        with socket.socket() as listener:
            listener.bind(("127.0.0.1", port))
    environment = {key: value for key, value in os.environ.items() if not key.startswith("MISHMESH_")}
    token = secrets.token_hex(32)
    environment.update({"MISHMESH_API_ADDR": "127.0.0.1:8081", "MISHMESH_INGRESS_ADDR": "127.0.0.1:5189",
                        "MISHMESH_BASE_DOMAIN": "localhost:5189", "MISHMESH_DATA_DSN": str(directory / "fixture.db"),
                        "MISHMESH_SECRET_KEY_FILE": str(directory / "fixture.key"), "MISHMESH_DATA_BACKEND": "sqlite",
                        "MISHMESH_AUTH_ENABLED": "true", "MISHMESH_SIGNUP_MODE": "org", "MISHMESH_HOST_COMMANDS": "true",
                        "MISHMESH_API_AUTH_TOKEN": token, "MISHMESH_WEBUI_ENABLED": "true", "MISHMESH_WEBUI_DIR": str(root / "web" / "dist"),
                        "MISHMESH_DOMAIN_VERIFICATION": "false", "MISHMESH_PATH_ROUTING": "true", "MISHMESH_PROXY_ALLOW_LOOPBACK": "true",
                        "MISHMESH_TCP_ENABLED": "false", "MISHMESH_UDP_PORT_MIN": "39960", "MISHMESH_UDP_PORT_MAX": "39963"})
    metadata = directory / "fixture.json"
    metadata.write_text(json.dumps({"api": "http://127.0.0.1:8081", "ingress": "http://127.0.0.1:5189", "operator_token": token, "python": os.sys.executable, "directory": str(directory)}), encoding="utf-8")
    metadata.chmod(0o600)
    print("Starting retained real SQLite fixture on loopback API8081/ingress5189 for at most30minutes", flush=True)
    with (directory / "server.log").open("a", encoding="utf-8") as log:
        try:
            subprocess.run([str(binary), "serve"], env=environment, stdout=log, stderr=log, timeout=1800, check=True)
        except subprocess.TimeoutExpired:
            print("Owned fixture deadline reached; process stopped and database/key retained", flush=True)


if __name__ == "__main__":
    main()
