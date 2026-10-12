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


def main():
    parser = argparse.ArgumentParser(description="Import a running cloudflared tunnel into mishmesh at the imported level. The tunnel token is read from a file and never printed. Set MISHMESH_API_AUTH_TOKEN when the API requires auth.")
    parser.add_argument("api", nargs="?", default="http://127.0.0.1:8091")
    parser.add_argument("--token-file", default=os.path.expanduser("~/.cloudflared/lab.token"))
    parser.add_argument("--name", default="lab")
    parser.add_argument("--metrics-addr", default="127.0.0.1:20241")
    parser.add_argument("--origin", default="", help="where the tunnel routes mishmesh hostnames; empty uses the server ingress address")
    opts = parser.parse_args()
    base = opts.api.rstrip("/") + "/api/v1"
    with open(opts.token_file, encoding="utf-8") as fh:
        token = fh.read().strip()
    code, det = call(base, "POST", "/cloudflare/detect", {"metrics_addr": opts.metrics_addr, "tunnel_token": token})
    if code != 200:
        print(f"detect failed: {code} {det}")
        return 1
    print(f"detect  tunnel {det.get('tunnel_id')} reachable={det.get('connector', {}).get('reachable')} hostnames={len(det.get('hostnames') or [])}")
    body = {"name": opts.name, "tunnel_token": token, "connector": "external", "metrics_addr": opts.metrics_addr, "origin": opts.origin}
    code, cf = call(base, "PUT", "/cloudflare", body)
    if code != 200:
        print(f"import failed: {code} {cf}")
        return 1
    st = cf.get("status") or {}
    tunnel = st.get("tunnel") or {}
    print(f"import  level={cf.get('level')} origin={cf['connection']['origin']} tunnel={tunnel.get('status')} connections={tunnel.get('connections')} last_error={st.get('last_error')}")
    for h in st.get("hostnames") or []:
        print(f"  {h['state']:10} {h['host']:32} ep={h.get('endpoint_id', '-')} {h.get('detail', '')}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
