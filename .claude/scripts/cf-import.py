#!/usr/bin/env python3
import argparse
import json
import os
import sys
import urllib.error
import urllib.request


def auth_headers():
    headers = {"Content-Type": "application/json"}
    token = os.environ.get("MISHMESH_API_AUTH_TOKEN", "").strip()
    if token:
        headers["Authorization"] = "Bearer " + token
    return headers


def call(base, method, path, body=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(base + path, data=data, method=method, headers=auth_headers())
    try:
        with urllib.request.urlopen(req, timeout=60) as resp:
            return resp.status, json.loads(resp.read() or b"null")
    except urllib.error.HTTPError as err:
        return err.code, json.loads(err.read() or b"null")


def import_connection(base, name, token, metrics_addr, origin):
    code, rows = call(base, "GET", "/connections")
    if code != 200 or not isinstance(rows, list):
        raise RuntimeError(f"connection list failed: HTTP {code}")
    matches = [row for row in rows if row.get("name") == name]
    if len(matches) > 1 or any(row.get("type") != "cloudflare" for row in matches):
        raise RuntimeError("connection name is already used by another type or is ambiguous")
    body = {"type": "cloudflare", "name": name, "config": {"connector": "external", "metrics_addr": metrics_addr, "origin": origin}, "secrets": {"tunnel_token": token}}
    method, path = "POST", "/connections"
    if matches:
        method, path = "PUT", "/connections/" + matches[0]["id"]
    code, record = call(base, method, path, body)
    if code not in (200, 201):
        raise RuntimeError(f"connection import failed: HTTP {code}")
    status = record.get("status") or {}
    print(f"import  id={record['id']} level={status.get('level')} health={status.get('health')} hostnames={len(status.get('hostnames') or [])}")
    return record


def main():
    parser = argparse.ArgumentParser(description="Import a running cloudflared tunnel into mishmesh at the imported level. The tunnel token is read from a file and never printed. Set MISHMESH_API_AUTH_TOKEN when the API requires auth.")
    parser.add_argument("api", nargs="?", default="http://127.0.0.1:8081")
    parser.add_argument("--token-file", required=True)
    parser.add_argument("--name", default="cloudflare")
    parser.add_argument("--metrics-addr", default="127.0.0.1:20241")
    parser.add_argument("--origin", default="", help="where the tunnel routes mishmesh hostnames; empty uses the server ingress address")
    opts = parser.parse_args()
    base = opts.api.rstrip("/") + "/api/v1"
    with open(opts.token_file, encoding="utf-8") as fh:
        token = fh.read().strip()
    try:
        import_connection(base, opts.name, token, opts.metrics_addr, opts.origin)
    except RuntimeError as error:
        print(str(error), file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
