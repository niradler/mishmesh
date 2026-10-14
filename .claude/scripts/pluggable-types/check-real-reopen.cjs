async (page) => {
  const fixture = await page.evaluate(() => window.__mishmeshFixture);
  function assert(condition, message) { if (!condition) throw new Error(message); }
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  await page.goto(fixture.api);
  await page.getByLabel("Email", { exact: true }).fill("browser-owner@example.test");
  await page.getByLabel("Password", { exact: true }).fill("SyntheticOnly#Browser19");
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await page.getByRole("link", { name: "Services", exact: true }).waitFor();
  async function get(path) {
    return page.evaluate(async (path) => { const response = await fetch("/api/v1" + path); return { status: response.status, body: await response.json() }; }, path);
  }
  const service = (await get("/services/" + fixture.evidence.services.serviceId)).body;
  assert(service.driver === "command" && service.config.env.SYNTHETIC_SECRET === "<redacted>" && service.config.start[2].includes("write_text('running')"), "Service config or secret did not survive daemon/database reopen");
  await page.goto(fixture.api + "/services/" + service.id);
  await page.getByRole("button", { name: "Edit", exact: true }).click();
  assert((await page.getByLabel("Start", { exact: true }).inputValue()).includes("write_text('running')"), "Actual reopened form lost argv");
  assert(await page.getByLabel("Environment value 1", { exact: true }).inputValue() === "", "Reopened form exposed secret");
  await page.getByRole("button", { name: "Cancel", exact: true }).click();
  const access = fixture.evidence.access;
  const endpoint = (await get("/endpoints/" + access.endpointId)).body;
  assert(endpoint.policy.access.length === 2 && endpoint.policy.connect.name === "browser-notes" && endpoint.policy.connect.allow_agents[0] === "*", "Access/Connect did not survive daemon/database reopen");
  assert((await get("/connections/" + access.publicConnectionId)).body.has_tunnel_token, "Connection secret did not reopen with original key");
  assert(!(await get("/connections/" + access.privateConnectionId)).body.has_oauth_client_secret, "Cleared secret reappeared on reopen");
  const udp = (await get("/endpoints/" + fixture.evidence["shipping-forms"].udpEndpointId)).body;
  assert(udp.kind === "udp" && udp.port === 39960, "UDP persisted identity/port changed");
  const required = (await get("/services/" + fixture.evidence.guards.requiredId)).body;
  const dependent = (await get("/services/" + fixture.evidence.guards.dependentId)).body;
  assert(required.idle_timeout_seconds === -1 && dependent.requires.includes(required.id) && required.required_by.includes(dependent.id), "Requires graph did not survive reopen");
  await page.goto(fixture.api + "/endpoints/" + endpoint.id);
  await page.getByLabel("Hostname 2", { exact: true }).waitFor();
  assert(await page.getByLabel("Hostname 2", { exact: true }).inputValue() === "browser-notes", "Reopened Access editor lost private hostname");
  assert(errors.length === 0, "Browser errors: " + errors.join("; "));
  await page.screenshot({ path: "output/playwright/real-reopen.png", fullPage: true });
  return { result: "PASS", evidence: "browser closed/reopened after native server termination and SQLite/key reopen", serviceId: service.id, endpointId: endpoint.id, udpEndpointId: udp.id, secretKeyRetained: true, requiresRetained: true };
}
