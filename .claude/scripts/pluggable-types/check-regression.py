import argparse
import json
from pathlib import Path


def validate_migrated_snapshot(before, after):
    old_services = {row["id"]: row for row in before["services"]}
    new_services = {row["id"]: row for row in after["services"]}
    assert old_services.keys() == new_services.keys(), "service identity changed"
    for identity, old in old_services.items():
        new = new_services[identity]
        driver = old["driver"]
        spec = old.get("spec") or {}
        config = old.get("config", spec.get(driver, {}))
        expected_driver = "command" if driver == "exec" else driver
        assert new["driver"] == expected_driver, f"service {identity}: driver changed"
        assert new.get("config") == config, f"service {identity}: config changed"
        ready = old.get("ready") or {}
        if spec.get("ready_tcp"):
            ready = {"tcp": spec["ready_tcp"]}
        assert (new.get("ready") or {}) == ready, f"service {identity}: readiness changed"
        for field in ("name", "idle_timeout_seconds", "start_timeout_seconds"):
            assert new.get(field, 0) == old.get(field, 0), f"service {identity}: {field} changed"
        assert sorted(new.get("requires") or []) == sorted(old.get("requires") or []), f"service {identity}: Requires changed"
    old_endpoints = {row["id"]: row for row in before["endpoints"]}
    new_endpoints = {row["id"]: row for row in after["endpoints"]}
    assert old_endpoints.keys() == new_endpoints.keys(), "endpoint identity changed"
    connections = after.get("connections") or []
    for identity, old in old_endpoints.items():
        new = new_endpoints[identity]
        old_policy = dict(old.get("policy") or {})
        new_policy = dict(new.get("policy") or {})
        legacy = old_policy.pop("public_access", "")
        old_refs = old_policy.pop("access", [])
        new_refs = new_policy.pop("access", [])
        assert new_policy == old_policy, f"endpoint {identity}: unrelated policy changed"
        for ref in old_refs:
            assert ref in new_refs, f"endpoint {identity}: existing Access ref lost"
        if legacy == "cloudflare":
            candidates = [row for row in connections if row["type"] == "cloudflare" and row.get("org_id", after.get("org_id", "")) == old.get("org_id", before.get("org_id", ""))]
            assert len(candidates) == 1, f"endpoint {identity}: migrated connection ambiguous"
            legacy_connection = (before.get("legacy_cloudflare") or {}).get("connection")
            if legacy_connection:
                assert candidates[0]["name"] == legacy_connection["name"], "legacy connection name changed"
                for field in ("account_id", "tunnel_id", "access_app_id", "origin", "connector", "metrics_addr", "prune"):
                    value = candidates[0].get("config", {}).get(field, "")
                    if field == "origin" and not value:
                        value = (candidates[0].get("status") or {}).get("details", {}).get("origin", "")
                    assert value == legacy_connection.get(field, ""), f"legacy connection {field} changed"
            expected = {"connection_id": candidates[0]["id"], "hostname": old.get("domain", "")}
            assert expected in new_refs, f"endpoint {identity}: connection identity or hostname lost"
    assert sorted(before.get("unclaimed") or []) == sorted(after.get("unclaimed") or []), "unclaimed hostnames changed"


def main():
    parser = argparse.ArgumentParser(description="Compare credential-free migration API snapshots. Keep local snapshots outside published artifacts.")
    parser.add_argument("before")
    parser.add_argument("after")
    options = parser.parse_args()
    validate_migrated_snapshot(json.loads(Path(options.before).read_text(encoding="utf-8")), json.loads(Path(options.after).read_text(encoding="utf-8")))
    print("migration snapshot preserved identities, configurations, Access and unclaimed hostnames")


if __name__ == "__main__":
    main()
