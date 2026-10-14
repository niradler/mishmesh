import argparse
import json
from pathlib import Path


def validate_snapshot(before, after):
    old_services = {row["id"]: row for row in before["services"]}
    new_services = {row["id"]: row for row in after["services"]}
    assert old_services.keys() == new_services.keys(), "service identity changed"
    for identity, old in old_services.items():
        new = new_services[identity]
        assert new["driver"] == old["driver"], f"service {identity}: driver changed"
        assert new["config"] == old["config"], f"service {identity}: config changed"
        assert (new.get("ready") or {}) == (old.get("ready") or {}), f"service {identity}: readiness changed"
        for field in ("name", "idle_timeout_seconds", "start_timeout_seconds"):
            assert new.get(field, 0) == old.get(field, 0), f"service {identity}: {field} changed"
        assert sorted(new.get("requires") or []) == sorted(old.get("requires") or []), f"service {identity}: Requires changed"
    old_endpoints = {row["id"]: row for row in before["endpoints"]}
    new_endpoints = {row["id"]: row for row in after["endpoints"]}
    assert old_endpoints.keys() == new_endpoints.keys(), "endpoint identity changed"
    for identity, old in old_endpoints.items():
        new = new_endpoints[identity]
        assert new.get("policy") == old.get("policy"), f"endpoint {identity}: policy or Access changed"
        for field in ("kind", "domain", "subdomain", "port", "method", "agent_id", "org_id"):
            assert new.get(field) == old.get(field), f"endpoint {identity}: {field} changed"
    old_connections = {row["id"]: row for row in before.get("connections") or []}
    new_connections = {row["id"]: row for row in after.get("connections") or []}
    assert old_connections.keys() == new_connections.keys(), "connection identity changed"
    for identity, old in old_connections.items():
        for field in ("name", "type", "config", "org_id"):
            assert new_connections[identity].get(field) == old.get(field), f"connection {identity}: {field} changed"
    assert sorted(before.get("unclaimed") or []) == sorted(after.get("unclaimed") or []), "unclaimed hostnames changed"


def main():
    parser = argparse.ArgumentParser(description="Compare credential-free typed API snapshots. Keep local snapshots outside published artifacts.")
    parser.add_argument("before")
    parser.add_argument("after")
    options = parser.parse_args()
    validate_snapshot(json.loads(Path(options.before).read_text(encoding="utf-8")), json.loads(Path(options.after).read_text(encoding="utf-8")))
    print("typed snapshot preserved identities, configurations, Access and unclaimed hostnames")


if __name__ == "__main__":
    main()
