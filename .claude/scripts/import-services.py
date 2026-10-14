#!/usr/bin/env python3
import argparse
import json
import os
import sys
import urllib.error
import urllib.request

try:
    import yaml
except ModuleNotFoundError:
    yaml = None

def seconds(value):
    if value is None:
        return 0
    if type(value) is not int:
        raise ValueError("timeouts must be integer seconds")
    return value


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
        with urllib.request.urlopen(req, timeout=30) as resp:
            return json.loads(resp.read() or b"null")
    except urllib.error.HTTPError as err:
        raise SystemExit(f"{method} {path}: {err.code} {err.read().decode(errors='replace')}")


def config_for(name, raw):
    driver = raw.get("driver", "")
    config = raw.get("config")
    if not isinstance(driver, str) or not driver or driver == "exec" or not isinstance(config, dict):
        raise ValueError(f"service {name}: use a typed driver and config object")
    forbidden = {"spec", "docker", "compose", "exec", "command", "ready_tcp", "idle_timeout", "start_timeout"}
    if forbidden.intersection(raw):
        raise ValueError(f"service {name}: unsupported service format")
    return driver, config


def desired_services(doc):
    defaults = doc.get("defaults") or {}
    if set(defaults) - {"idle_timeout_seconds", "start_timeout_seconds"}:
        raise ValueError("unsupported service defaults")
    out = {}
    for name, raw in (doc.get("services") or {}).items():
        driver, config = config_for(name, raw)
        out[name] = {
            "name": name,
            "driver": driver,
            "config": config,
            "ready": raw.get("ready") or {},
            "requires": raw.get("requires") or [],
            "idle_timeout_seconds": seconds(raw.get("idle_timeout_seconds", defaults.get("idle_timeout_seconds"))),
            "start_timeout_seconds": seconds(raw.get("start_timeout_seconds", defaults.get("start_timeout_seconds"))),
        }
    return out


def covers(have, want):
    if have == "<redacted>":
        return True
    if isinstance(want, dict):
        return isinstance(have, dict) and all(covers(have.get(k), v) for k, v in want.items())
    return have == want


def same(existing, want):
    return all(covers(existing.get(k), want[k]) for k in ("driver", "config", "ready", "requires", "idle_timeout_seconds", "start_timeout_seconds"))


def printable(body):
    shown = json.loads(json.dumps(body))
    shown["config"] = {key: "<redacted>" for key in shown.get("config") or {}}
    return json.dumps(shown)


def import_services(base, want, dry_run):
    existing = {s["name"]: s for s in call(base, "GET", "/services")}
    for name, body in sorted(want.items()):
        current = existing.get(name)
        if current and same(current, body):
            print(f"ok      service {name}")
            continue
        verb = "update " if current else "create "
        print(f"{verb} service {name}: {printable(body)}")
        if dry_run:
            continue
        if current:
            call(base, "PUT", f"/services/{current['id']}", body)
        else:
            call(base, "POST", "/services", body)


def main():
    parser = argparse.ArgumentParser(description="Import typed service YAML into the services API (idempotent by name). Link endpoints by setting policy.service_id. Set MISHMESH_API_AUTH_TOKEN when the API requires auth.")
    parser.add_argument("file", help="path to typed services YAML")
    parser.add_argument("api", nargs="?", default="http://127.0.0.1:8081")
    parser.add_argument("--dry-run", action="store_true")
    opts = parser.parse_args()
    if yaml is None:
        raise SystemExit("PyYAML is required to read services YAML; install it in your operator environment")
    with open(opts.file, encoding="utf-8") as fh:
        doc = yaml.safe_load(fh) or {}
    base = opts.api.rstrip("/") + "/api/v1"
    import_services(base, desired_services(doc), opts.dry_run)
    return 0


if __name__ == "__main__":
    sys.exit(main())
