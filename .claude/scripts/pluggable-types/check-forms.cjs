async (page) => {
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  const drivers = [
    { type: "docker", label: "Docker", help: "Docker containers", fields: [{ key: "containers", label: "Containers", kind: "list", required: false }] },
    { type: "compose", label: "Compose", help: "Compose project", fields: [{ key: "project", label: "Project", kind: "text", required: true }, { key: "file", label: "Compose file", kind: "text", required: false, host_command: true }] },
    { type: "command", label: "Command", help: "Explicit argv", fields: [{ key: "start", label: "Start", kind: "list", required: true, host_command: true }, { key: "stop", label: "Stop", kind: "list", required: true, host_command: true }, { key: "env", label: "Environment", kind: "keyvalue", required: false, sensitive: true }] },
    { type: "widget", label: "Widget", help: "Synthetic renderer extension", fields: [
      { key: "alias", label: "Alias", kind: "text", required: true },
      { key: "arguments", label: "Arguments", kind: "list", required: false },
      { key: "metadata", label: "Metadata", kind: "keyvalue", required: false },
      { key: "password", label: "Password", kind: "secret", required: false },
      { key: "mode", label: "Mode", kind: "select", options: ["one", "two"], default: "one", required: true },
      { key: "count", label: "Count", kind: "number", default: 2, required: true },
      { key: "enabled", label: "Enabled", kind: "bool", default: false, required: false },
      { key: "runtime", label: "Runtime connection", kind: "connection", conn_type: "runtime", required: false },
    ] },
  ];
  const types = [
    { type: "cloudflare", label: "Cloudflare Tunnel", role: "access", scope: "public", levels: ["imported", "managed"], protocols: ["http"], detect: true, detect_fields: [{ key: "metrics_addr", label: "Metrics address", kind: "text", required: false }], fields: [{ key: "metrics_addr", label: "Metrics address", kind: "text", default: "127.0.0.1:20241", required: false }, { key: "tunnel_token", label: "Tunnel token", kind: "secret", required: true }] },
    { type: "tailscale", label: "Tailscale Services", role: "access", scope: "private", levels: ["imported", "managed"], protocols: ["http"], detect: true, detect_fields: [{ key: "socket", label: "Socket", kind: "text", required: false }], fields: [{ key: "level", label: "Level", kind: "select", options: ["imported", "managed"], default: "imported", required: true }, { key: "socket", label: "Socket", kind: "text", required: false }, { key: "tailnet", label: "Tailnet", kind: "text", required: true }, { key: "oauth_client_secret", label: "OAuth client secret", kind: "secret", required: false }] },
    { type: "runtime", label: "Synthetic Runtime", role: "runtime", scope: "", levels: ["imported"], protocols: [], detect: false, fields: [] },
  ];
  const serviceRows = [{ id: "svc-base", name: "base", driver: "compose", config: { project: "base" }, ready: {}, requires: [], required_by: [], idle_timeout_seconds: 0, start_timeout_seconds: 120, status: { state: "stopped", active: 0 }, endpoints: [] }];
  const connectionRows = [{ id: "conn-runtime", type: "runtime", name: "runtime-one", config: {}, status: { level: "imported", health: "up", hostnames: [], details: {} } }, { id: "conn-public", type: "cloudflare", name: "public-one", config: { metrics_addr: "127.0.0.1:20241" }, has_tunnel_token: true, status: { level: "imported", health: "up", hostnames: [], details: {} } }];
  const endpointRows = [{ id: "ep-notes", org_id: "org-fixture", agent_id: "", kind: "http", method: "proxy", lifecycle: "reserved", domain: "notes.example.test", public_url: "https://notes.example.test", online: true, policy: { proxy_targets: ["http://127.0.0.1:8080"], connect: { name: "notes", allow_agents: ["*"] }, access: [{ connection_id: "conn-public", hostname: "notes.example.test" }] } }];
  const requests = [];
  let sequence = 0;
  await page.route("**/api/v1/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname.replace("/api/v1", "");
    const body = request.postDataJSON();
    if (request.method() !== "GET") requests.push({ path, method: request.method(), body });
    let payload = [];
    let status = 200;
    if (path === "/auth/config") payload = { auth_enabled: false };
    else if (path === "/capabilities") payload = { host_commands: true };
    else if (path === "/service-drivers") payload = drivers;
    else if (path === "/connection-types") payload = types;
    else if (path === "/status") payload = { agents: { total: 0, connected: 0 }, endpoints: { total: 1, online: 1, by_kind: { http: 1, tcp: 0, udp: 0, tls: 0 } }, usage_bytes: 0 };
    else if (path === "/connections/detect") payload = { socket: "/synthetic/socket", tailnet: "fixture.ts.net" };
    else if (path === "/services" && request.method() === "POST") { payload = { ...body, id: `svc-${++sequence}`, required_by: [], status: { state: "stopped", active: 0 }, endpoints: [] }; serviceRows.push(payload); status = 201; }
    else if (path === "/services") payload = serviceRows;
    else if (path.startsWith("/services/")) payload = serviceRows.find((row) => row.id === path.split("/")[2]);
    else if (path === "/connections" && request.method() === "POST") {
      payload = { id: `conn-${++sequence}`, name: body.name, type: body.type, config: body.config, status: { level: body.config.level || "imported", health: "down", hostnames: [], details: {} } };
      for (const [key, value] of Object.entries(body.secrets ?? {})) payload[`has_${key}`] = Boolean(value);
      connectionRows.push(payload); status = 201;
    } else if (path === "/connections") payload = connectionRows;
    else if (path.startsWith("/connections/")) {
      const row = connectionRows.find((entry) => entry.id === path.split("/")[2]);
      if (request.method() === "PUT") { row.name = body.name; row.config = body.config; for (const [key, value] of Object.entries(body.secrets ?? {})) row[`has_${key}`] = Boolean(value); }
      payload = row;
    } else if (path === "/endpoints") payload = endpointRows;
    else if (path.startsWith("/endpoints/")) {
      const row = endpointRows.find((entry) => entry.id === path.split("/")[2]);
      if (request.method() === "PUT" || request.method() === "PATCH") Object.assign(row, body);
      payload = row;
    } else if (path === "/topology") payload = { nodes: [], edges: [] };
    await route.fulfill({ status, contentType: "application/json", body: JSON.stringify(payload ?? {}) });
  });
  function assert(condition, message) { if (!condition) throw new Error(message); }
  await page.goto("http://127.0.0.1:5188/services");
  await page.getByRole("button", { name: "New service", exact: true }).click();
  await page.getByRole("button", { name: "Compose", exact: true }).click();
  await page.getByLabel("Name", { exact: true }).fill("compose-test");
  await page.getByLabel("Project", { exact: true }).fill("lab");
  await page.getByLabel("Compose file", { exact: true }).fill("/srv/lab/compose.yml");
  await page.getByRole("button", { name: "Create service", exact: true }).click();
  await page.getByRole("heading", { name: "compose-test", exact: true }).waitFor();
  const compose = requests.find((entry) => entry.path === "/services");
  assert(compose.body.driver === "compose" && compose.body.config.project === "lab" && !compose.body.spec, "Service payload did not use typed config");
  await page.getByRole("button", { name: "Edit", exact: true }).click();
  assert(await page.getByLabel("Compose file", { exact: true }).inputValue() === "/srv/lab/compose.yml", "Compose config lost after reopen");
  await page.getByRole("button", { name: "Cancel", exact: true }).click();
  await page.goto("http://127.0.0.1:5188/services");
  await page.getByRole("button", { name: "New service", exact: true }).click();
  await page.getByRole("button", { name: "Widget", exact: true }).click();
  await page.getByLabel("Name", { exact: true }).fill("widget-test");
  await page.getByLabel("Alias", { exact: true }).fill("extension");
  await page.getByLabel("Arguments", { exact: true }).fill("first\n second with spaces ");
  await page.getByLabel("Mode", { exact: true }).selectOption("two");
  await page.getByLabel("Count", { exact: true }).fill("3");
  await page.getByLabel("Enabled", { exact: true }).check();
  await page.getByLabel("Runtime connection", { exact: true }).selectOption("conn-runtime");
  assert(await page.getByLabel("Runtime connection", { exact: true }).locator("option").count() === 2, "Wrong-type runtime connection offered");
  await page.getByRole("button", { name: "Add metadata entry", exact: true }).click();
  await page.getByLabel("Metadata key 1", { exact: true }).fill("LABEL");
  await page.getByLabel("Metadata value 1", { exact: true }).fill("value");
  await page.getByLabel("Password", { exact: true }).fill("synthetic-only-secret");
  await page.getByLabel("Idle timeout seconds", { exact: true }).fill("-1");
  await page.getByLabel("base", { exact: true }).check();
  await page.getByRole("button", { name: "Create service", exact: true }).click();
  await page.getByRole("heading", { name: "widget-test", exact: true }).waitFor();
  const widget = requests.find((entry) => entry.body?.name === "widget-test");
  assert(widget.body.config.arguments[1] === " second with spaces " && widget.body.config.count === 3 && widget.body.config.enabled === true && widget.body.config.metadata.LABEL === "value" && widget.body.requires[0] === "svc-base", "Descriptor field coercion or argv preservation failed");
  assert(!await page.locator("body").innerText().then((text) => text.includes("synthetic-only-secret")), "Sensitive details exposed values");
  await page.goto("http://127.0.0.1:5188/connections");
  await page.getByRole("button", { name: "Add connection", exact: true }).click();
  await page.getByRole("button", { name: "Tailscale Services", exact: true }).click();
  await page.getByLabel("Name", { exact: true }).fill("private-test");
  await page.getByRole("button", { name: "Detect", exact: true }).click();
  await page.getByLabel("Tailnet", { exact: true }).fill("fixture.ts.net");
  await page.getByLabel("OAuth client secret", { exact: true }).fill("synthetic-only-secret");
  await page.getByRole("button", { name: "Create connection", exact: true }).click();
  await page.getByRole("heading", { name: "private-test", exact: true }).waitFor();
  const privateRow = connectionRows.find((row) => row.name === "private-test");
  assert(!JSON.stringify(privateRow).includes("synthetic-only-secret"), "Connection response exposed secret");
  await page.getByRole("button", { name: "Edit connection", exact: true }).click();
  assert(await page.getByLabel("OAuth client secret", { exact: true }).inputValue() === "", "Fetched secret populated edit form");
  await page.getByRole("button", { name: "Save connection", exact: true }).click();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  assert(privateRow.has_oauth_client_secret, "Unchanged connection secret was cleared");
  await page.goto("http://127.0.0.1:5188/endpoints/ep-notes");
  await page.getByRole("button", { name: "Add access", exact: true }).click();
  await page.getByLabel("Hostname 2", { exact: true }).fill("notes");
  await Promise.all([page.waitForResponse((response) => response.url().includes("/endpoints/ep-notes") && response.request().method() !== "GET"), page.getByRole("button", { name: "Save access", exact: true }).click()]);
  await page.reload();
  await page.getByLabel("Hostname 2", { exact: true }).waitFor();
  assert(endpointRows[0].policy.access.length === 2 && endpointRows[0].policy.connect.name === "notes" && endpointRows[0].policy.connect.allow_agents[0] === "*", "Access save lost another ref or Connect fields");
  assert(errors.length === 0, `Browser errors: ${errors.join("; ")}`);
  await page.screenshot({ path: "output/playwright/descriptor-forms.png", fullPage: true });
  console.log(JSON.stringify({ result: "PASS", evidence: "synthetic descriptor renderer only", drivers: drivers.length, fieldKinds: 8, servicePayload: "config/ready/requires", connectionSecret: "retained without GET value", accessRefs: endpointRows[0].policy.access.length, connectPreserved: true }));
}
