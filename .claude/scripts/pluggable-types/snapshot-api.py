import argparse
import json
import os
from pathlib import Path
import sys
import urllib.error
import urllib.parse
import urllib.request


def snapshot(base, org_id, legacy=False):
    headers = {"Accept": "application/json"}
    token = os.environ.get("MISHMESH_API_AUTH_TOKEN", "")
    if token:
        headers["Authorization"] = "Bearer " + token

    def read(path):
        url = base.rstrip("/") + "/api/v1" + path + "?" + urllib.parse.urlencode({"org_id": org_id})
        try:
            with urllib.request.urlopen(urllib.request.Request(url, headers=headers), timeout=30) as response:
                raw = response.read(32 * 1024 * 1024 + 1)
                if len(raw) > 32 * 1024 * 1024:
                    raise RuntimeError("snapshot response exceeds size limit")
                return json.loads(raw)
        except urllib.error.HTTPError as error:
            raise RuntimeError(f"snapshot read {path} failed: HTTP {error.code}") from None

    result = {"org_id": org_id, "services": read("/services"), "endpoints": read("/endpoints")}
    if legacy:
        result["legacy_cloudflare"] = read("/cloudflare")
        connections = [result["legacy_cloudflare"]]
    else:
        result["connections"] = read("/connections")
        for row in result["connections"]:
            row["org_id"] = org_id
        connections = result["connections"]
    result["unclaimed"] = sorted({host["host"] for row in connections for host in (row.get("status") or {}).get("hostnames") or [] if host.get("state") == "unclaimed"})
    for row in result["services"]:
        for config in (row.get("config") or {}, (row.get("spec") or {}).get("exec") or {}):
            if config.get("env"):
                config["env"] = {key: "<redacted>" for key in config["env"]}
    return result


def main():
    parser = argparse.ArgumentParser(description="Read-only org snapshot for migration comparison; credentials read from environment")
    parser.add_argument("api")
    parser.add_argument("output")
    parser.add_argument("--org-id", required=True)
    parser.add_argument("--legacy", action="store_true", help="capture old singleton/spec API before upgrade")
    options = parser.parse_args()
    data = snapshot(options.api, options.org_id, options.legacy)
    destination = Path(options.output)
    descriptor = os.open(destination, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
    with os.fdopen(descriptor, "w", encoding="utf-8") as output:
        json.dump(data, output, indent=2)
    print(f"Read-only migration snapshot saved: {destination}")


if __name__ == "__main__":
    sys.exit(main())
