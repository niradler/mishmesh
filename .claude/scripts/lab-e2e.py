#!/usr/bin/env python3
import argparse
import http.client
import json
import os
import ssl
import sys
import time
import urllib.error
import urllib.parse
import urllib.request

ORPHANS = {"2fauth.niradler.com", "docker.niradler.com", "vaultwarden.niradler.com"}
CLOUDFLARE_FAILURES = {502, 503, 504, 521, 522, 523, 524, 525, 526, 530}

failures = []


def check(ok, label, detail=""):
    print(f"{'PASS' if ok else 'FAIL'}  {label}{(' - ' + detail) if detail else ''}")
    if not ok:
        failures.append(label)
    return ok


def auth_headers():
    headers = {"Content-Type": "application/json"}
    token = os.environ.get("MISHMESH_API_AUTH_TOKEN", "").strip()
    if token:
        headers["Authorization"] = "Bearer " + token
    return headers


class Client:
    def __init__(self, api):
        self.root = api.rstrip("/")
        self.base = self.root + "/api/v1"

    def call(self, method, path, body=None):
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(self.base + path, data=data, method=method, headers=auth_headers())
        try:
            with urllib.request.urlopen(req, timeout=60) as resp:
                raw = resp.read()
                return resp.status, json.loads(raw) if raw else None
        except urllib.error.HTTPError as err:
            raw = err.read()
            try:
                return err.code, json.loads(raw) if raw else None
            except json.JSONDecodeError:
                return err.code, raw.decode(errors="replace")

    def raw(self, path):
        with urllib.request.urlopen(self.root + path, timeout=30) as resp:
            return resp.status, resp.read().decode(errors="replace")


def status_no_redirect(url, host=None, timeout=30):
    parsed = urllib.parse.urlsplit(url)
    if parsed.scheme == "https":
        conn = http.client.HTTPSConnection(parsed.hostname, parsed.port or 443, timeout=timeout, context=ssl.create_default_context())
    else:
        conn = http.client.HTTPConnection(parsed.hostname, parsed.port or 80, timeout=timeout)
    headers = {"User-Agent": "mishmesh-lab-e2e"}
    if host:
        headers["Host"] = host
    try:
        conn.request("GET", parsed.path or "/", headers=headers)
        resp = conn.getresponse()
        return resp.status, resp.getheader("Location") or ""
    finally:
        conn.close()


def check_ui(c):
    code, html = c.raw("/")
    check(code == 200 and "<div id=\"root\"" in html, "web ui index served")
    asset = next((p.split('"')[0] for p in html.split('src="')[1:] if p.startswith("/assets/")), "")
    if check(bool(asset), "web ui references a bundled script"):
        code, _ = c.raw(asset)
        check(code == 200, "web ui asset served", asset)
    for deep in ("/network", "/services", "/cloudflare"):
        code, body = c.raw(deep)
        check(code == 200 and "<div id=\"root\"" in body, f"spa deep link {deep}")


def check_services(c):
    code, svcs = c.call("GET", "/services")
    check(code == 200 and isinstance(svcs, list) and len(svcs) > 0, "services listed", f"{len(svcs or [])} services")
    for s in svcs or []:
        state = (s.get("status") or {}).get("state", "")
        check(state in {"running", "sleeping", "stopped", "starting"}, f"service {s['name']} state", state or "missing")
    return {s["id"]: s for s in svcs or []}


def check_endpoints(c, svcs):
    code, eps = c.call("GET", "/endpoints")
    check(code == 200 and isinstance(eps, list), "endpoints listed", f"{len(eps or [])} endpoints")
    for ep in eps or []:
        policy = ep.get("policy") or {}
        health = ep.get("health") or {}
        name = ep.get("domain") or ep["id"]
        check(bool(policy.get("proxy_targets")), f"{name} has proxy_targets")
        check(policy.get("public_access") == "cloudflare", f"{name} public access on")
        sid = policy.get("service_id")
        if sid:
            linked = sid in svcs and ep["id"] in (svcs[sid].get("endpoints") or [])
            check(linked, f"{name} linked to service {svcs.get(sid, {}).get('name', sid)}")
        check(health.get("state") in {"up", "sleeping"}, f"{name} health", f"{health.get('state')} {health.get('detail', '')}".strip())
    return eps or []


def check_cloudflare(c, eps):
    code, st = c.call("POST", "/cloudflare/sync")
    check(code == 200, "cloudflare sync", str(code))
    code, cf = c.call("GET", "/cloudflare")
    status = (cf or {}).get("status") or {}
    tunnel = status.get("tunnel") or {}
    check(cf.get("level") == "imported", "cloudflare level imported", str(cf.get("level")))
    check(tunnel.get("status") == "healthy", "tunnel healthy", f"{tunnel.get('status')} connections={tunnel.get('connections')}")
    check(not status.get("last_error"), "no cloudflare error", str(status.get("last_error") or ""))
    hosts = {h["host"]: h for h in status.get("hostnames") or []}
    for ep in eps:
        h = hosts.get(ep.get("domain"))
        check(h is not None and h["state"] == "published" and h.get("endpoint_id") == ep["id"], f"{ep.get('domain')} published", (h or {}).get("state", "absent"))
    unclaimed = {h for h, v in hosts.items() if v["state"] == "unclaimed"}
    check(unclaimed == ORPHANS, "only the known orphans are unclaimed", ", ".join(sorted(unclaimed)))
    return status


def check_topology(c, eps, svcs):
    code, topo = c.call("GET", "/topology")
    check(code == 200, "topology served")
    nodes = {n["id"]: n for n in topo.get("nodes", [])}
    edges = {(e["from"], e["to"], e["kind"]) for e in topo.get("edges", [])}
    check("tunnel:cloudflare" in nodes, "topology has the tunnel node")
    ep_ids = {ep["id"] for ep in eps}
    for ep in eps:
        host = "hostname:" + ep["domain"]
        epn = "endpoint:" + ep["id"]
        chain = ("tunnel:cloudflare", host, "routes") in edges and (host, epn, "serves") in edges
        check(chain, f"map tunnel -> {ep['domain']} -> endpoint")
        sid = (ep.get("policy") or {}).get("service_id")
        if sid:
            check((epn, "service:" + sid, "wakes") in edges, f"map {ep['domain']} wakes {svcs.get(sid, {}).get('name', sid)}")
    for n in nodes.values():
        ref = n.get("ref") or {}
        if ref.get("kind") == "endpoint":
            check(ref.get("id") in ep_ids, f"map node {n['id']} links to an existing endpoint")
        if ref.get("kind") == "service":
            check(ref.get("id") in svcs, f"map node {n['id']} links to an existing service")
    dangling = [e for e in edges if e[0] not in nodes or e[1] not in nodes]
    check(not dangling, "map has no dangling edges", str(dangling[:3]))


def check_traffic(eps, ingress, public):
    for ep in eps:
        domain = ep["domain"]
        try:
            code, loc = status_no_redirect(ingress, host=domain)
            check(code < 500, f"ingress {domain}", f"{code} {loc[:60]}".strip())
        except OSError as err:
            check(False, f"ingress {domain}", str(err))
        if not public:
            continue
        try:
            code, loc = status_no_redirect("https://" + domain + "/")
            check(code not in CLOUDFLARE_FAILURES and code < 500, f"public {domain}", f"{code} {loc[:60]}".strip())
        except OSError as err:
            check(False, f"public {domain}", str(err))


def check_service_crud(c, svcs):
    name = "e2e-probe-" + str(int(time.time()))
    body = {"name": name, "driver": "docker", "spec": {"docker": {"containers": ["mishmesh-e2e-absent"]}}, "idle_timeout_seconds": 0}
    code, created = c.call("POST", "/services", body)
    if not check(code == 201, "service create", str(code)):
        return
    sid = created["id"]
    code, got = c.call("GET", f"/services/{sid}")
    check(code == 200 and got["name"] == name, "service read")
    body["name"] = name + "-renamed"
    code, upd = c.call("PUT", f"/services/{sid}", body)
    check(code == 200 and upd["name"] == body["name"], "service update", str(code))
    code, _ = c.call("POST", "/services", dict(body))
    check(code == 409, "duplicate service name rejected", str(code))
    code, _ = c.call("DELETE", f"/services/{sid}")
    check(code in (200, 204), "service delete", str(code))
    code, _ = c.call("GET", f"/services/{sid}")
    check(code == 404, "deleted service is gone", str(code))
    linked = next((s for s in svcs.values() if s.get("endpoints")), None)
    if linked:
        code, _ = c.call("DELETE", f"/services/{linked['id']}")
        check(code == 409, f"delete of linked service {linked['name']} refused", str(code))


def check_wake(c, svcs, eps, ingress, service_name):
    svc = next((s for s in svcs.values() if s["name"] == service_name), None)
    if not check(svc is not None and svc.get("endpoints"), f"wake target {service_name} exists and is linked"):
        return
    ep = next(e for e in eps if e["id"] == svc["endpoints"][0])
    code, _ = c.call("POST", f"/services/{svc['id']}/stop")
    check(code in (200, 202, 204), f"stop {service_name}", str(code))
    state = ""
    for _ in range(60):
        _, cur = c.call("GET", f"/services/{svc['id']}")
        state = (cur.get("status") or {}).get("state", "")
        if state in ("sleeping", "stopped"):
            break
        time.sleep(1)
    check(state in ("sleeping", "stopped"), f"{service_name} asleep", state)
    started = time.monotonic()
    try:
        code, loc = status_no_redirect(ingress, host=ep["domain"], timeout=200)
        check(code < 500, f"request wakes {service_name}", f"{code} in {time.monotonic() - started:.1f}s")
    except OSError as err:
        check(False, f"request wakes {service_name}", str(err))
    _, cur = c.call("GET", f"/services/{svc['id']}")
    state = (cur.get("status") or {}).get("state", "")
    check(state == "running", f"{service_name} running after wake", state)


def main():
    parser = argparse.ArgumentParser(description="End-to-end check of a live lab mishmesh: UI, services, endpoints, Cloudflare import, topology, ingress and public traffic, service CRUD, and optionally scale-to-zero wake. Set MISHMESH_API_AUTH_TOKEN when the API requires auth.")
    parser.add_argument("api", nargs="?", default="http://127.0.0.1:8091")
    parser.add_argument("--ingress", default="http://127.0.0.1:8090")
    parser.add_argument("--no-public", action="store_true", help="skip requests through the public Cloudflare hostnames")
    parser.add_argument("--wake", default="", help="service name to stop and wake through ingress")
    opts = parser.parse_args()
    c = Client(opts.api)
    code, body = c.raw("/healthz")
    check(code == 200 and "ok" in body, "server healthz")
    check_ui(c)
    svcs = check_services(c)
    eps = check_endpoints(c, svcs)
    check_cloudflare(c, eps)
    check_topology(c, eps, svcs)
    check_traffic(eps, opts.ingress, not opts.no_public)
    check_service_crud(c, svcs)
    if opts.wake:
        check_wake(c, svcs, eps, opts.ingress, opts.wake)
    print(f"\n{len(failures)} failed" if failures else "\nall checks passed")
    for f in failures:
        print("  - " + f)
    return 1 if failures else 0


if __name__ == "__main__":
    sys.exit(main())
