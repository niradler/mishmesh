#!/usr/bin/env python3
import argparse
import json
import os
import re
import sys
import urllib.error
import urllib.request

try:
    import yaml
except ModuleNotFoundError:
    yaml = None

DURATION = re.compile(r"^(\d+)(s|m|h)?$")
UNIT_SECONDS = {None: 1, "s": 1, "m": 60, "h": 3600}


def seconds(value):
    if value is None:
        return 0
    if isinstance(value, int):
        return value
    match = DURATION.match(str(value).strip())
    if not match:
        raise ValueError(f"unsupported duration {value!r} (use 90s, 30m or 2h)")
    return int(match.group(1)) * UNIT_SECONDS[match.group(2)]


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
    if config is not None:
        return "command" if driver == "exec" else driver, config
    if driver == "docker":
        docker = raw.get("docker") or {}
        config = {k: docker[k] for k in ("containers", "label") if docker.get(k)}
    elif driver == "compose":
        compose = raw.get("compose") or {}
        config = {k: compose[k] for k in ("project", "file", "bin", "services") if compose.get(k)}
    elif driver in ("exec", "command"):
        command = raw.get(driver) or {}
        config = {k: command[k] for k in ("start", "stop", "status", "dir", "env") if command.get(k)}
        driver = "command"
    else:
        raise SystemExit(f"service {name}: unknown driver {driver!r}")
    return driver, config


def desired_services(doc):
    defaults = doc.get("defaults") or {}
    out = {}
    for name, raw in (doc.get("services") or {}).items():
        driver, config = config_for(name, raw)
        out[name] = {
            "name": name,
            "driver": driver,
            "config": config,
            "ready": raw.get("ready") or {},
            "requires": raw.get("requires") or [],
            "idle_timeout_seconds": seconds(raw.get("idle_timeout", defaults.get("idle_timeout"))),
            "start_timeout_seconds": seconds(raw.get("start_timeout", defaults.get("start_timeout"))),
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
    env = (body.get("config") or {}).get("env")
    if not env:
        return json.dumps(body)
    shown = json.loads(json.dumps(body))
    shown["config"]["env"] = {k: "<redacted>" for k in env}
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
    parser = argparse.ArgumentParser(description="Import a legacy MISHMESH_SERVICES_FILE into the services API (idempotent by name). Link endpoints by setting policy.service_id. Set MISHMESH_API_AUTH_TOKEN when the API requires auth.")
    parser.add_argument("file", help="path to the legacy services.yml")
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
