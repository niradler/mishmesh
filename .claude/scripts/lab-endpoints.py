#!/usr/bin/env python3
import argparse
import json
import os
import urllib.request

PINS = {
    "portainer": "yupK0qMkJ9eyCU4kpr0erId2wPNUwuYePH0nSM4Jvz0=",
    "cockpit": "io498pxrlRvyM86mjCl8LdXBBNkr0aOZmCnpsH1fezM=",
    "vault": "j09NosCsIxr3QgDIyC7aZ3WbXxA5hfFArWDD7DstPjs=",
}

LAB = {
    "adguard": ("127.0.0.1:80", None),
    "portainer": ("https://127.0.0.1:9443", "portainer"),
    "cockpit": ("https://127.0.0.1:9090", None),
    "vault": ("https://127.0.0.1:8443", "vaultwarden"),
    "sync": ("127.0.0.1:8384", "syncthing"),
    "code": ("127.0.0.1:8080", "code-server"),
    "hermes": ("127.0.0.1:9119", "hermes"),
    "ollama": ("127.0.0.1:11434", "ollama"),
    "mishmesh": ("127.0.0.1:8091", None),
    "notes": ("127.0.0.1:3010", "affine"),
    "orc": ("127.0.0.1:7700", None),
    "desktop": ("127.0.0.1:8088", "guacamole"),
}

DOMAIN_SUFFIX = ".niradler.com"


def auth_headers():
    headers = {"Content-Type": "application/json"}
    token = os.environ.get("MISHMESH_API_AUTH_TOKEN", "").strip()
    if token:
        headers["Authorization"] = "Bearer " + token
    return headers


def call(base, method, path, body=None):
    data = json.dumps(body).encode() if body is not None else None
    req = urllib.request.Request(base + path, data=data, method=method, headers=auth_headers())
    with urllib.request.urlopen(req, timeout=30) as resp:
        return json.loads(resp.read() or b"null")


def desired_policy(current, name, service_ids, public_access):
    target, service = LAB[name]
    policy = dict(current or {})
    policy.pop("proxy_target", None)
    policy["proxy_targets"] = [target]
    if name in PINS:
        policy["origin_tls"] = {"pin_sha256": [PINS[name]]}
    else:
        policy.pop("origin_tls", None)
    policy.pop("service", None)
    if service and service in service_ids:
        policy["service_id"] = service_ids[service]
    else:
        if service:
            print(f"warn {name}: service {service!r} is not defined; import it first")
        policy.pop("service_id", None)
    if public_access:
        policy["public_access"] = "cloudflare"
    return policy


def main():
    parser = argparse.ArgumentParser(description="Apply the lab endpoint upstream map (targets, origin TLS pins, service links by id) to a mishmesh API. Idempotent. Set MISHMESH_API_AUTH_TOKEN when the API requires auth.")
    parser.add_argument("api", nargs="?", default="http://127.0.0.1:8091")
    parser.add_argument("--dry-run", action="store_true")
    parser.add_argument("--public-access", action="store_true", help="also turn on Public access (needs the Cloudflare connection imported first)")
    opts = parser.parse_args()
    dry_run = opts.dry_run
    base = opts.api.rstrip("/") + "/api/v1"
    service_ids = {s["name"]: s["id"] for s in call(base, "GET", "/services")}
    endpoints = call(base, "GET", "/endpoints")
    seen = set()
    for ep in endpoints:
        domain = ep.get("domain") or ""
        if not domain.endswith(DOMAIN_SUFFIX):
            continue
        name = domain[: -len(DOMAIN_SUFFIX)]
        if name not in LAB:
            print(f"skip {domain}: not in the lab map")
            continue
        seen.add(name)
        policy = desired_policy(ep.get("policy"), name, service_ids, opts.public_access)
        if policy == ep.get("policy"):
            print(f"ok   {domain}")
            continue
        print(f"set  {domain}: {json.dumps(policy)}")
        if not dry_run:
            call(base, "PATCH", f"/endpoints/{ep['id']}", {"policy": policy})
    for name in sorted(set(LAB) - seen):
        print(f"missing endpoint for {name}{DOMAIN_SUFFIX}")


if __name__ == "__main__":
    main()
