async (page) => {
  const fixture = await page.evaluate(() => window.__mishmeshFixture);
  const runId = Date.now();
  function assert(condition, message) { if (!condition) throw new Error(message); }
  const services = [];
  for (const driver of ["Docker", "Compose"]) {
    await page.goto(fixture.api + "/services");
    await page.getByRole("button", { name: "New service", exact: true }).first().click();
    await page.getByRole("button", { name: driver, exact: true }).click();
    await page.getByLabel("Name", { exact: true }).fill(`browser-${driver.toLowerCase()}-${runId}`);
    if (driver === "Docker") await page.getByLabel("Label", { exact: true }).fill("mishmesh.synthetic=absent");
    else {
      await page.getByLabel("Project", { exact: true }).fill(`synthetic-${runId}`);
      await page.getByLabel("Compose file", { exact: true }).fill(fixture.directory + "/not-started-compose.yml");
      await page.getByLabel("Ready when", { exact: true }).selectOption("http");
      await page.getByLabel("HTTP URL", { exact: true }).fill(fixture.api + "/healthz");
      await page.getByLabel("Expected HTTP status", { exact: true }).fill("200");
      await page.getByLabel("Idle behavior", { exact: true }).selectOption("-1");
    }
    const saved = page.waitForResponse((response) => new URL(response.url()).pathname.endsWith("/services") && response.request().method() === "POST");
    await page.getByRole("button", { name: "Create service", exact: true }).click();
    const response = await saved;
    const body = await response.json();
    assert(response.status() === 201 && body.driver === driver.toLowerCase() && !body.spec, "Real driver form contract failed");
    await page.waitForURL("**/services/" + body.id);
    await page.reload();
    await page.getByRole("button", { name: "Edit", exact: true }).click();
    if (driver === "Compose") {
      assert(await page.getByLabel("HTTP URL", { exact: true }).inputValue() === fixture.api + "/healthz", "HTTP readiness did not reopen");
      assert(await page.getByLabel("Idle timeout seconds", { exact: true }).inputValue() === "-1", "Requires idle sentinel did not reopen");
    } else assert(await page.getByLabel("Label", { exact: true }).inputValue() === "mishmesh.synthetic=absent", "Docker config did not reopen");
    await page.getByRole("button", { name: "Cancel", exact: true }).click();
    services.push(body.id);
  }
  await page.goto(fixture.api + "/endpoints");
  await page.getByRole("button", { name: "Reserve endpoint", exact: true }).first().click();
  const dialog = page.getByRole("dialog");
  await page.getByLabel("Custom domain", { exact: true }).fill(`ui-${runId}.example.test`);
  await dialog.getByPlaceholder("127.0.0.1:8080 or https://127.0.0.1:9443").fill("127.0.0.1:8081");
  const saved = page.waitForResponse((response) => new URL(response.url()).pathname.endsWith("/endpoints") && response.request().method() === "POST");
  await dialog.getByRole("button", { name: "Reserve", exact: true }).click();
  const response = await saved;
  const endpoint = await response.json();
  assert(response.status() === 201 && endpoint.lifecycle === "reserved", "Real endpoint create UI still violates API contract");
  const requestBody = response.request().postDataJSON();
  assert(!("lifecycle" in requestBody), "Client sent server-owned lifecycle field");
  await page.waitForURL("**/endpoints/" + endpoint.id);
  const agent = await page.evaluate(async (name) => {
    const response = await fetch("/api/v1/agents", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ name }) });
    return { status: response.status, body: await response.json() };
  }, `browser-agent-${runId}`);
  assert(agent.status === 201, "UDP fixture agent create failed");
  await page.goto(fixture.api + "/endpoints");
  await page.getByRole("button", { name: "Reserve endpoint", exact: true }).first().click();
  await page.getByRole("button", { name: "Agent", exact: true }).click();
  await dialog.getByRole("combobox").nth(0).click();
  await page.getByRole("option", { name: `browser-agent-${runId} (offline)`, exact: true }).click();
  await dialog.getByRole("combobox").nth(1).click();
  await page.getByRole("option", { name: "UDP", exact: true }).click();
  await page.getByLabel("Port", { exact: true }).fill("39960");
  const udpSaved = page.waitForResponse((result) => new URL(result.url()).pathname.endsWith("/endpoints") && result.request().method() === "POST");
  await dialog.getByRole("button", { name: "Reserve", exact: true }).click();
  const udpResponse = await udpSaved;
  const udp = await udpResponse.json();
  assert(udpResponse.status() === 201 && udp.kind === "udp" && udp.port >= 39960 && udp.port <= 39963, "UDP endpoint form regression");
  await page.waitForURL("**/endpoints/" + udp.id);
  await page.reload();
  return { result: "PASS", evidence: "real descriptor forms and reserved HTTP/UDP endpoints", driverServiceIds: services, endpointId: endpoint.id, udpEndpointId: udp.id, udpPort: udp.port, httpReadiness: true, idleMinusOne: true };
}
