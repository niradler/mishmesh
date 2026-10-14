import contextlib
import copy
import importlib.util
import io
import json
from pathlib import Path
import threading
import unittest
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer


def load_script(name, path):
    spec = importlib.util.spec_from_file_location(name, path)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


ROOT = Path(__file__).resolve().parents[1]
IMPORT = load_script("import_services", ROOT / "import-services.py")
CF = load_script("cf_import", ROOT / "cf-import.py")
REGRESSION = load_script("check_regression", ROOT / "pluggable-types" / "check-regression.py")
SNAPSHOT = load_script("snapshot_api", ROOT / "pluggable-types" / "snapshot-api.py")


class ScriptTests(unittest.TestCase):
    def test_import_rejects_obsolete_formats(self):
        for raw in (
            {"driver": "exec", "config": {"start": ["run"], "stop": ["stop"]}},
            {"driver": "docker", "docker": {"containers": ["web"]}},
            {"driver": "command", "config": {}, "spec": {}},
            {"driver": "command", "config": {}, "idle_timeout": "30m"},
            {"driver": "command", "config": {}, "idle_timeout_seconds": "30m"},
        ):
            with self.assertRaises(ValueError):
                IMPORT.desired_services({"services": {"app": raw}})
        with self.assertRaises(ValueError):
            IMPORT.desired_services({"defaults": {"idle_timeout": "30m"}})

    def test_read_only_snapshot_masks_environment(self):
        requests = []
        documents = {"/api/v1/services": [{"id": "svc", "driver": "command", "config": {"env": {"TOKEN": "private-snapshot"}}}], "/api/v1/endpoints": [{"id": "ep", "org_id": "org-one"}], "/api/v1/cloudflare": {"connection": {"name": "public"}, "status": {"hostnames": [{"host": "orphan.example.test", "state": "unclaimed"}]}}, "/api/v1/connections": [{"id": "conn", "type": "cloudflare", "status": {"hostnames": []}}]}

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def do_GET(self):
                requests.append((self.command, self.path))
                data = json.dumps(documents[self.path.split("?", 1)[0]]).encode()
                self.send_response(200)
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            base = "http://127.0.0.1:" + str(server.server_port)
            typed = SNAPSHOT.snapshot(base, "org-one")
            self.assertNotIn("private-snapshot", json.dumps(typed))
            self.assertEqual(typed["connections"][0]["org_id"], "org-one")
            self.assertTrue(all(method == "GET" and path.endswith("?org_id=org-one") for method, path in requests))
            self.assertEqual(documents["/api/v1/services"][0]["config"]["env"]["TOKEN"], "private-snapshot")
        finally:
            server.shutdown()
            server.server_close()
            thread.join(timeout=5)

    def test_private_endpoint_script_preserves_other_access(self):
        path = ROOT.parent / "local" / "lab-endpoints.py"
        if not path.exists():
            self.skipTest("private operator script is optional")
        script = load_script("local_endpoints", path)
        script.LAB = {"app": ("127.0.0.1:9000", "")}
        script.PINS = {}
        original = {"access": [{"connection_id": "conn-private", "hostname": "app.private.test"}], "connect": {"name": "app", "allow_agents": ["agent-one"]}}
        updated = script.desired_policy(original, "app", {}, "conn-public", "app.example.test")
        self.assertEqual(updated["access"][0], original["access"][0])
        self.assertEqual(updated["connect"], original["connect"])
        self.assertEqual(len(updated["access"]), 2)
        self.assertEqual(script.desired_policy(updated, "app", {}, "conn-public", "app.example.test"), updated)
        self.assertEqual(len(original["access"]), 1)

    def test_snapshot_checks_every_configuration_and_reference(self):
        before = {"org_id": "org-one", "services": [{"id": f"svc-{i}", "name": f"service-{i}", "driver": "command", "config": {"start": ["run", str(i)]}, "ready": {"tcp": "127.0.0.1:9000"}, "idle_timeout_seconds": -1} for i in range(8)], "endpoints": [{"id": f"ep-{i}", "domain": f"app-{i}.example.test", "policy": {"access": [{"connection_id": "conn-cf", "hostname": f"app-{i}.example.test"}], "connect": {"name": f"app-{i}"}}} for i in range(12)], "connections": [{"id": "conn-cf", "type": "cloudflare", "name": "public", "config": {"tunnel_id": "tunnel"}}], "unclaimed": ["orphan.example.test"]}
        after = copy.deepcopy(before)
        REGRESSION.validate_snapshot(before, after)
        damaged_connection = copy.deepcopy(after)
        damaged_connection["connections"][0]["config"]["tunnel_id"] = "wrong-tunnel"
        with self.assertRaises(AssertionError):
            REGRESSION.validate_snapshot(before, damaged_connection)
        for index in range(12):
            damaged = copy.deepcopy(after)
            damaged["endpoints"][index]["policy"]["access"][0]["connection_id"] = "wrong"
            with self.assertRaises(AssertionError):
                REGRESSION.validate_snapshot(before, damaged)
        for index in range(8):
            damaged = copy.deepcopy(after)
            damaged["services"][index]["config"]["start"] = ["wrong"]
            with self.assertRaises(AssertionError):
                REGRESSION.validate_snapshot(before, damaged)

    def test_flattened_service_configuration(self):
        raw = {"services": {"worker": {"driver": "command", "config": {"start": ["run", " a b "], "stop": ["stop"], "env": {"TOKEN": "private"}}, "ready": {"http": {"url": "http://127.0.0.1:9000", "status": 204}}, "requires": ["database"], "idle_timeout_seconds": -1}}}
        body = IMPORT.desired_services(raw)["worker"]
        self.assertEqual(body["driver"], "command")
        self.assertNotIn("spec", body)
        self.assertEqual(body["config"]["start"], ["run", " a b "])
        self.assertEqual(body["ready"], raw["services"]["worker"]["ready"])
        self.assertEqual(body["requires"], ["database"])
        self.assertEqual(body["idle_timeout_seconds"], -1)
        self.assertNotIn("private", IMPORT.printable(body))

    def test_cloudflare_named_import_is_idempotent(self):
        requests = []
        rows = []

        class Handler(BaseHTTPRequestHandler):
            def log_message(self, *args):
                pass

            def handle_request(self):
                body = json.loads(self.rfile.read(int(self.headers.get("Content-Length", "0"))) or b"null")
                requests.append((self.command, self.path, body))
                if self.command == "GET":
                    response = rows
                else:
                    response = {**body, "id": "conn-test", "status": {"health": "up", "hostnames": []}}
                    rows[:] = [response]
                data = json.dumps(response).encode()
                self.send_response(200)
                self.send_header("Content-Length", str(len(data)))
                self.end_headers()
                self.wfile.write(data)

            do_GET = handle_request
            do_POST = handle_request
            do_PUT = handle_request

        server = ThreadingHTTPServer(("127.0.0.1", 0), Handler)
        thread = threading.Thread(target=server.serve_forever, daemon=True)
        thread.start()
        try:
            base = "http://127.0.0.1:" + str(server.server_port) + "/api/v1"
            with contextlib.redirect_stdout(io.StringIO()) as output:
                CF.import_connection(base, "named", "private-token", "127.0.0.1:20241", "")
                CF.import_connection(base, "named", "private-token", "127.0.0.1:20241", "")
            self.assertNotIn("private-token", output.getvalue())
            writes = [r for r in requests if r[0] != "GET"]
            self.assertEqual([r[:2] for r in writes], [("POST", "/api/v1/connections"), ("PUT", "/api/v1/connections/conn-test")])
            self.assertEqual(writes[0][2]["type"], "cloudflare")
            self.assertEqual(writes[0][2]["secrets"], {"tunnel_token": "private-token"})
            self.assertNotIn("tunnel_token", writes[0][2]["config"])
            desired = IMPORT.desired_services({"services": {"worker": {"driver": "command", "config": {"start": ["run", " a b "], "stop": ["stop"], "env": {"TOKEN": "private-service"}}, "ready": {"tcp": "127.0.0.1:9000"}, "requires": ["database"]}}})
            with contextlib.redirect_stdout(io.StringIO()) as output:
                IMPORT.import_services(base, desired, False)
                IMPORT.import_services(base, desired, False)
            self.assertNotIn("private-service", output.getvalue())
            service_writes = [request for request in requests if request[0] != "GET" and request[1].endswith("/services")]
            self.assertEqual(len(service_writes), 1)
            payload = service_writes[0][2]
            self.assertEqual(payload["driver"], "command")
            self.assertEqual(payload["config"]["start"], ["run", " a b "])
            self.assertEqual(payload["ready"], {"tcp": "127.0.0.1:9000"})
            self.assertEqual(payload["requires"], ["database"])
            self.assertNotIn("spec", payload)
        finally:
            server.shutdown()
            server.server_close()
            thread.join(timeout=5)


if __name__ == "__main__":
    unittest.main()
