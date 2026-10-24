#!/usr/bin/env python3
import argparse
import http.cookiejar
import json
import os
import secrets
import subprocess
import sys
import tempfile
import time
import urllib.error
import urllib.request

API_ADDR = "127.0.0.1:18181"
INGRESS_ADDR = "127.0.0.1:18180"
PROXY_ADDR = "127.0.0.1:18190"
UPSTREAM = "httpbin.org"

failures = []


def check(ok, label, detail=""):
    print(f"{'PASS' if ok else 'FAIL'}  {label}{(' - ' + detail) if detail else ''}", flush=True)
    if not ok:
        failures.append(label)
    return ok


class API:
    def __init__(self):
        self.base = f"http://{API_ADDR}"
        self.opener = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(http.cookiejar.CookieJar()))

    def call(self, method, path, body=None):
        data = json.dumps(body).encode() if body is not None else None
        req = urllib.request.Request(self.base + path, data=data, method=method, headers={"Content-Type": "application/json"})
        try:
            with self.opener.open(req, timeout=30) as resp:
                raw = resp.read()
                return resp.status, json.loads(raw) if raw else None
        except urllib.error.HTTPError as err:
            return err.code, err.read().decode(errors="replace")
        except OSError as err:
            return 0, str(err)

    def must(self, method, path, body=None, want=(200,)):
        status, out = self.call(method, path, body)
        if status not in want:
            raise SystemExit(f"{method} {path}: status {status}: {out}")
        return out


def wait_for(label, fn, timeout=20):
    deadline = time.time() + timeout
    while time.time() < deadline:
        if fn():
            return True
        time.sleep(0.3)
    raise SystemExit(f"timed out waiting for {label}")


def curl(curl_bin, ca_file, version_flag, path, extra=()):
    cmd = [curl_bin, "-sS", "--max-time", "30", "-x", f"http://{PROXY_ADDR}", "--cacert", ca_file, version_flag,
           "-o", "-", "-w", "\n%{http_code} %{http_version}", *extra, f"https://{UPSTREAM}{path}"]
    proc = subprocess.run(cmd, capture_output=True, text=True, timeout=60)
    body, _, tail = proc.stdout.rpartition("\n")
    parts = tail.split()
    code = parts[0] if parts else "000"
    version = parts[1] if len(parts) > 1 else ""
    return proc.returncode, code, version, body, proc.stderr.strip()


def policy(base, extra):
    return {"cedar_src": base + "\n" + extra}


def main():
    ap = argparse.ArgumentParser(description="local 127.0.0.1 smoke test for agent-side egress tls decryption")
    ap.add_argument("--bin", required=True, help="directory holding mishmesh-server and mishmesh-agent")
    ap.add_argument("--curl", required=True, help="curl binary with HTTP/2 support")
    args = ap.parse_args()

    ext = ".exe" if os.name == "nt" else ""
    server_bin = os.path.join(args.bin, "mishmesh-server" + ext)
    agent_bin = os.path.join(args.bin, "mishmesh-agent" + ext)
    work = tempfile.mkdtemp(prefix="mishmesh-decrypt-smoke-")
    procs = []
    try:
        env = dict(os.environ,
                   MISHMESH_API_ADDR=API_ADDR, MISHMESH_INGRESS_ADDR=INGRESS_ADDR,
                   MISHMESH_DATA_DSN=os.path.join(work, "smoke.db"), MISHMESH_AUTH_ENABLED="true",
                   MISHMESH_TCP_ENABLED="false", MISHMESH_LOG_LEVEL="info",
                   MISHMESH_API_AUTH_TOKEN=secrets.token_urlsafe(32))
        server_log = open(os.path.join(work, "server.log"), "w")
        procs.append(subprocess.Popen([server_bin, "serve"], env=env, cwd=work, stdout=server_log, stderr=subprocess.STDOUT))
        api = API()
        wait_for("server", lambda: api.call("GET", "/healthz")[0] == 200 or api.call("GET", "/api/v1/auth/me")[0] in (200, 401))

        api.must("POST", "/api/v1/auth/register", {"email": "smoke@decrypt.local", "password": "smoke-password-123", "name": "smoke"}, want=(201,))
        created = api.must("POST", "/api/v1/agents", {"name": "decrypting"}, want=(201,))
        agent_id, agent_token = created["agent"]["id"], created["token"]
        base_policy = api.must("GET", "/api/v1/policy")["cedar_src"]
        strict = f"""
permit (principal, action == Action::"egress:connect", resource);
permit (principal, action == Action::"egress:request", resource) when {{ resource.host == "{UPSTREAM}" && context.decrypted && context.method == "GET" && context.path like "/get*" }};
@credential("api-key")
permit (principal, action == Action::"egress:request", resource) when {{ resource.host == "{UPSTREAM}" && context.decrypted && context.path like "/headers*" }};
"""
        api.must("PUT", "/api/v1/policy", policy(base_policy, strict))
        api.must("PUT", "/api/v1/secrets/api-secret", {"value": "smoke-secret-value"}, want=(200, 201, 204))
        api.must("PUT", "/api/v1/egress/credentials/api-key", {"header": "Authorization", "prefix": "Bearer ", "secret": "api-secret", "agent_ids": [agent_id], "destinations": [UPSTREAM]})
        api.must("PUT", "/api/v1/egress/decrypt", {"allow": [UPSTREAM], "bypass": []})

        agent_cfg = os.path.join(work, "agent.yml")
        with open(agent_cfg, "w") as f:
            f.write(f"gateway: ws://{API_ADDR}\ntoken: {agent_token}\negress:\n  listen: {PROXY_ADDR}\n")
        agent_log = open(os.path.join(work, "agent.log"), "w")
        procs.append(subprocess.Popen([agent_bin, "start", "--config", agent_cfg], cwd=work, stdout=agent_log, stderr=subprocess.STDOUT))

        ca_file = os.path.join(work, "org-ca.pem")
        def fetch_ca():
            proc = subprocess.run([agent_bin, "ca", "--config", agent_cfg], capture_output=True, text=True, timeout=30)
            if proc.returncode != 0 or "BEGIN CERTIFICATE" not in proc.stdout:
                return False
            with open(ca_file, "w") as f:
                f.write(proc.stdout)
            return True
        wait_for("mishmesh-agent ca", fetch_ca)
        api_ca = api.must("GET", "/api/v1/egress/ca")
        with open(ca_file) as f:
            check(api_ca["pem"].strip() in f.read(), "agent ca command matches GET /api/v1/egress/ca")

        wait_for("agent proxy", lambda: curl(args.curl, ca_file, "--http1.1", "/get")[1] == "200", timeout=30)

        for flag, want_version in (("--http1.1", "1.1"), ("--http2", "2")):
            rc, code, version, body, err = curl(args.curl, ca_file, flag, "/get?probe=1")
            check(rc == 0 and code == "200" and version == want_version, f"{flag} GET /get decrypted through org CA", f"{code} http/{version} {err}")
            rc, code, version, body, err = curl(args.curl, ca_file, flag, "/anything/denied")
            check(code == "403", f"{flag} GET /anything denied per request", f"{code} {body[:80]!r}")
            rc, code, version, body, err = curl(args.curl, ca_file, flag, "/headers")
            injected = ""
            try:
                injected = json.loads(body)["headers"].get("Authorization", "")
            except (ValueError, KeyError):
                pass
            check(code == "200" and injected == "Bearer smoke-secret-value", f"{flag} credential injected on /headers", f"{code} auth={injected!r}")
            rc, code, version, body, err = curl(args.curl, ca_file, flag, "/get", ("-H", "Authorization: Bearer client-supplied"))
            check(code == "200" and "client-supplied" in body, f"{flag} no injection outside the @credential rule", code)

        open_policy = strict + f"""
permit (principal, action == Action::"egress:request", resource) when {{ resource.host == "{UPSTREAM}" && context.decrypted && context.path like "/anything*" }};
"""
        api.must("PUT", "/api/v1/policy", policy(base_policy, open_policy))
        started = time.time()
        flushed = False
        while time.time() - started < 10:
            if curl(args.curl, ca_file, "--http2", "/anything/allowed")[1] == "200":
                flushed = True
                break
            time.sleep(0.25)
        check(flushed, "policy change flushes the agent's cached decisions", f"{time.time() - started:.2f}s")

        api.must("PUT", "/api/v1/egress/decrypt", {"allow": []})
        started = time.time()
        passthrough = False
        while time.time() - started < 10:
            rc, code, _, _, err = curl(args.curl, ca_file, "--http2", "/get")
            if rc != 0 and code == "000":
                passthrough = True
                break
            time.sleep(0.25)
        check(passthrough, "disabling decryption flushes the agent to passthrough (org CA no longer presented)", f"{time.time() - started:.2f}s")
        rc, code, version, _, err = curl(args.curl, ca_file, "--http2", "/get", ("--ca-native",))
        check(rc == 0 and code == "200", "passthrough reaches the real upstream certificate", f"{code} http/{version} {err}")
    finally:
        for p in reversed(procs):
            p.terminate()
            try:
                p.wait(timeout=10)
            except subprocess.TimeoutExpired:
                p.kill()
        print(f"logs: {work}")

    if failures:
        print(f"{len(failures)} failure(s)")
        sys.exit(1)
    print("all checks passed")


if __name__ == "__main__":
    main()
