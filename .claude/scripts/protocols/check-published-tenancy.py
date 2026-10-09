import argparse
import json
import pathlib
import subprocess


API = "http://mm-beta-api:8081"
OWNER_COOKIE = "/tmp/mm-tenant-owner-cookie"
MEMBER_COOKIE = "/tmp/mm-tenant-member-cookie"


def request(method, path, cookie, payload=None, expected=200):
    arguments = ["kubectl", "--context", "k3d-mmhelm", "--request-timeout=30s", "exec", "-n", "mm-beta-proof", "kit", "--", "curl", "--max-time", "20", "-sS", "-X", method, "-b", cookie, "-c", cookie, "-o", "/tmp/mm-tenant-response", "-w", "%{http_code}", "-H", "Content-Type: application/json", "-H", "Origin: " + API]
    if payload is not None:
        arguments.extend(["--data-binary", json.dumps(payload)])
    arguments.append(API + path)
    result = subprocess.run(arguments, capture_output=True, text=True, check=True, timeout=40)
    status = int(result.stdout.strip())
    if status != expected:
        raise RuntimeError(f"{method} {path} returned {status}, expected {expected}")
    body = subprocess.run(["kubectl", "--context", "k3d-mmhelm", "--request-timeout=20s", "exec", "-n", "mm-beta-proof", "kit", "--", "cat", "/tmp/mm-tenant-response"], capture_output=True, text=True, check=True, timeout=30).stdout
    return json.loads(body) if body.strip() else None


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--output", type=pathlib.Path, required=True)
    args = parser.parse_args()
    context = subprocess.run(["kubectl", "config", "current-context"], capture_output=True, text=True, check=True, timeout=15).stdout.strip()
    if context != "k3d-mmhelm":
        raise RuntimeError("Requires isolated k3d-mmhelm cluster")
    owner = request("POST", "/api/v1/auth/login", OWNER_COOKIE, {"email": "beta-proof@example.test", "password": "beta-proof-test-only-password"})
    request("POST", "/api/v1/auth/switch-org", OWNER_COOKIE, {"org_id": "org_default"})
    endpoint = request("GET", "/api/v1/endpoints", OWNER_COOKIE)[0]
    agent = request("GET", "/api/v1/agents", OWNER_COOKIE)[0]
    orgs = request("GET", "/api/v1/orgs", OWNER_COOKIE)
    other = next((org for org in orgs if org["name"] == "customer-isolation-proof"), None)
    if other is None:
        other = request("POST", "/api/v1/orgs", OWNER_COOKIE, {"name": "customer-isolation-proof"}, expected=201)
    request("POST", "/api/v1/auth/switch-org", OWNER_COOKIE, {"org_id": other["id"]})
    members = request("GET", "/api/v1/members", OWNER_COOKIE)
    email = "customer-isolation@example.test"
    if not any(member["user"]["email"] == email for member in members):
        invite = request("POST", "/api/v1/members", OWNER_COOKIE, {"email": email, "role": "member"}, expected=201)
        request("POST", "/api/v1/auth/register", MEMBER_COOKIE, {"email": email, "name": "Isolation Proof", "password": "beta-proof-test-only-password", "invite_token": invite["invite_token"]}, expected=201)
    member = request("POST", "/api/v1/auth/login", MEMBER_COOKIE, {"email": email, "password": "beta-proof-test-only-password"})
    request("POST", "/api/v1/auth/switch-org", MEMBER_COOKIE, {"org_id": other["id"]})
    if member["role"] != "member":
        raise RuntimeError("Invited member role mismatch")
    if request("GET", "/api/v1/endpoints?org_id=org_default", MEMBER_COOKIE) != []:
        raise RuntimeError("Cross-org endpoint list leaked")
    request("GET", "/api/v1/endpoints/" + endpoint["id"], MEMBER_COOKIE, expected=404)
    request("GET", "/api/v1/agents/" + agent["id"], MEMBER_COOKIE, expected=404)
    request("POST", "/api/v1/auth/switch-org", MEMBER_COOKIE, {"org_id": "org_default"}, expected=404)
    request("POST", "/api/v1/agents", MEMBER_COOKIE, {"name": "must-not-create"}, expected=403)
    request("PATCH", "/api/v1/members/" + owner["id"], MEMBER_COOKIE, {"role": "member"}, expected=403)
    request("PATCH", "/api/v1/members/" + owner["id"], OWNER_COOKIE, {"role": "member"}, expected=409)
    summary = {"passed": True, "invitation_signup": True, "member_read_only": True, "cross_org_reads_and_switch_rejected": True, "query_org_override_does_not_leak": True, "owner_demotion_rejected": True, "last_owner_protected": True}
    args.output.write_text(json.dumps(summary, indent=2) + "\n", encoding="utf-8")
    print(json.dumps(summary), flush=True)


if __name__ == "__main__":
    main()
