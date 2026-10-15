async (page) => {
  const fixture = await page.evaluate(() => window.__mishmeshFixture);
  const runId = Date.now();
  const failures = [];
  page.on("pageerror", (error) => failures.push(error.message));
  function assert(condition, message) { if (!condition) throw new Error(message); }
  async function api(method, path, body) {
    return page.evaluate(async ({ method, path, body }) => {
      const response = await fetch("/api/v1" + path, { method, headers: { "Content-Type": "application/json" }, body: body === undefined ? undefined : JSON.stringify(body) });
      const text = await response.text();
      return { status: response.status, body: text ? JSON.parse(text) : null };
    }, { method, path, body });
  }
  await page.goto(fixture.api);
  const login = page.getByRole("button", { name: "Create an account", exact: true });
  await login.or(page.getByRole("link", { name: "Services", exact: true })).waitFor();
  if (await login.isVisible()) {
    await login.click();
    await page.getByLabel("Name", { exact: true }).fill("Browser Owner");
    await page.getByLabel("Email", { exact: true }).fill("browser-owner@example.test");
    await page.getByLabel("Password", { exact: true }).fill("SyntheticOnly#Browser19");
    await page.getByRole("button", { name: "Create account", exact: true }).click();
    await page.getByRole("link", { name: "Services", exact: true }).waitFor();
  }
  const descriptorResult = await api("GET", "/service-drivers");
  assert(descriptorResult.status === 200 && descriptorResult.body.map((row) => row.type).sort().join() === "command,compose,docker", "Shipping driver descriptors mismatch");
  const marker = fixture.directory.replace(/\\/g, "/") + `/command-state-${runId}.txt`;
  const version = fixture.directory.replace(/\\/g, "/") + `/command-version-${runId}.txt`;
  const startCode = `from pathlib import Path; Path(${JSON.stringify(marker)}).write_text('running')`;
  const stopCode = `from pathlib import Path; Path(${JSON.stringify(marker)}).write_text('stopped')`;
  const statusCode = `from pathlib import Path; p=Path(${JSON.stringify(marker)}); raise SystemExit(0 if p.exists() and p.read_text()=='running' else 1)`;
  await page.goto(fixture.api + "/services");
  await page.getByRole("button", { name: "New service", exact: true }).first().click();
  await page.getByRole("button", { name: "Command", exact: true }).click();
  await page.getByLabel("Name", { exact: true }).fill(`browser-command-${runId}`);
  await page.getByLabel("Start", { exact: true }).fill([fixture.python, "-c", startCode].join("\n"));
  await page.getByLabel("Stop", { exact: true }).fill([fixture.python, "-c", stopCode].join("\n"));
  await page.getByLabel("Status", { exact: true }).fill([fixture.python, "-c", statusCode].join("\n"));
  await page.getByLabel("Start timeout seconds", { exact: true }).fill("10");
  await page.getByRole("button", { name: "Add environment entry", exact: true }).click();
  await page.getByLabel("Environment key 1", { exact: true }).fill("SYNTHETIC_SECRET");
  await page.getByLabel("Environment value 1", { exact: true }).fill("synthetic-service-secret");
  const createdResponse = page.waitForResponse((response) => response.url().endsWith("/api/v1/services") && response.request().method() === "POST");
  await page.getByRole("button", { name: "Create service", exact: true }).click();
  const response = await createdResponse;
  const service = await response.json();
  assert(response.status() === 201 && service.driver === "command" && !service.spec, "Real typed service create failed");
  assert(service.config.env.SYNTHETIC_SECRET === "<redacted>" && !JSON.stringify(service).includes("synthetic-service-secret"), "Owner service response leaked environment secret");
  await page.waitForURL("**/services/" + service.id);
  await page.getByRole("button", { name: "Start", exact: true }).click();
  await page.getByRole("button", { name: "Stop", exact: true }).waitFor();
  assert((await api("GET", "/services/" + service.id)).body.status.state === "running", "Real command did not start");
  await page.getByRole("button", { name: "Edit", exact: true }).click();
  const changedStart = startCode + `; Path(${JSON.stringify(version)}).write_text('two')`;
  await page.getByLabel("Start", { exact: true }).fill([fixture.python, "-c", changedStart].join("\n"));
  const rejectedResponse = page.waitForResponse((result) => result.url().endsWith("/services/" + service.id) && result.request().method() === "PUT");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  assert((await rejectedResponse).status() === 409, "Normal save silently changed a running command");
  const restartedResponse = page.waitForResponse((result) => result.url().includes("/services/" + service.id + "?restart=true") && result.request().method() === "PUT");
  await page.getByRole("button", { name: "Save and restart", exact: true }).click();
  assert((await restartedResponse).status() === 200, "Explicit save/restart failed");
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  assert((await api("GET", "/services/" + service.id)).body.status.state === "running", "Saved command did not restart");
  await page.reload();
  await page.getByRole("button", { name: "Edit", exact: true }).click();
  assert(await page.getByLabel("Start", { exact: true }).inputValue() === [fixture.python, "-c", changedStart].join("\n"), "Saved runtime did not reopen");
  assert(await page.getByLabel("Environment value 1", { exact: true }).inputValue() === "", "Secret appeared in edit form");
  await page.getByLabel("Start", { exact: true }).fill([fixture.python, "-c", "raise SystemExit(17)"].join("\n"));
  const failedResponse = page.waitForResponse((result) => result.url().includes("?restart=true") && result.request().method() === "PUT");
  await page.getByRole("button", { name: "Save and restart", exact: true }).click();
  const failed = await failedResponse;
  const failedBody = await failed.json();
  assert(failed.status() === 502 && failedBody.saved === true && failedBody.phase === "start", "Real saved/start failure did not report phase");
  await page.getByText("The new configuration is saved.", { exact: false }).waitFor();
  await page.getByRole("button", { name: "Cancel", exact: true }).click();
  await page.reload();
  await page.getByRole("button", { name: "Edit", exact: true }).click();
  assert((await page.getByLabel("Start", { exact: true }).inputValue()).includes("raise SystemExit(17)"), "Failed-start saved configuration was lost");
  await page.getByLabel("Start", { exact: true }).fill([fixture.python, "-c", startCode].join("\n"));
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  assert((await api("GET", "/services/" + service.id)).body.config.env.SYNTHETIC_SECRET === "<redacted>", "Unchanged masked environment was not retained");
  assert(failures.length === 0, "Browser errors: " + failures.join("; "));
  await page.screenshot({ path: "output/playwright/real-service.png", fullPage: true });
  return { result: "PASS", evidence: "real Windows Command + SQLite APIs", explicitRestart: true, failedStartSaved: true, serviceId: service.id, marker, version };
}
