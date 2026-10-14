async (page) => {
  const fixture = await page.evaluate(() => window.__mishmeshFixture);
  const runId = Date.now();
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  function assert(condition, message) { if (!condition) throw new Error(message); }
  async function api(target, method, path, body) {
    return target.evaluate(async ({ method, path, body }) => {
      const response = await fetch("/api/v1" + path, { method, headers: { "Content-Type": "application/json" }, body: body === undefined ? undefined : JSON.stringify(body) });
      const text = await response.text();
      return { status: response.status, body: text ? JSON.parse(text) : null };
    }, { method, path, body });
  }
  await page.goto(fixture.api + "/services");
  await page.getByRole("button", { name: "New service", exact: true }).first().waitFor();
  const owner = (await api(page, "GET", "/auth/me")).body;
  const marker = fixture.directory.replace(/\\/g, "/") + `/required-${runId}.txt`;
  const config = { start: [fixture.python, "-c", `from pathlib import Path; Path(${JSON.stringify(marker)}).write_text('running')`], stop: [fixture.python, "-c", `from pathlib import Path; Path(${JSON.stringify(marker)}).write_text('stopped')`], status: [fixture.python, "-c", `from pathlib import Path; p=Path(${JSON.stringify(marker)}); raise SystemExit(0 if p.exists() and p.read_text()=='running' else 1)`], env: { SYNTHETIC_SECRET: "synthetic-requirement-secret" } };
  const required = await api(page, "POST", "/services", { name: `required-${runId}`, driver: "command", config, ready: { http: { url: fixture.api + "/healthz", status: 200 } }, idle_timeout_seconds: -1, start_timeout_seconds: 10 });
  assert(required.status === 201, "Required command create failed");
  const dependentConfig = { start: config.start.map((arg) => arg.replace(marker, marker + ".dependent")), stop: config.stop.map((arg) => arg.replace(marker, marker + ".dependent")), status: config.status.map((arg) => arg.replace(marker, marker + ".dependent")) };
  const dependent = await api(page, "POST", "/services", { name: `dependent-${runId}`, driver: "command", config: dependentConfig, requires: [required.body.id], idle_timeout_seconds: 0, start_timeout_seconds: 10 });
  assert(dependent.status === 201, "Dependent create failed");
  await page.goto(fixture.api + "/services/" + dependent.body.id);
  await page.getByRole("button", { name: "Start", exact: true }).click();
  await page.getByRole("button", { name: "Stop", exact: true }).waitFor();
  assert((await api(page, "GET", "/services/" + required.body.id)).body.status.state === "running", "Requires did not start requirement first");
  await page.goto(fixture.api + "/services/" + required.body.id);
  const rejected = page.waitForResponse((response) => response.url().endsWith("/stop") && response.request().method() === "POST");
  await page.getByRole("button", { name: "Stop", exact: true }).click();
  assert((await rejected).status() === 409, "Running requirement stop guard lost");
  await page.getByRole("button", { name: "Stop dependents too", exact: true }).click();
  await page.getByRole("button", { name: "Stop dependents and service", exact: true }).click();
  await page.getByRole("dialog").waitFor({ state: "hidden" });
  assert((await api(page, "GET", "/services/" + dependent.body.id)).body.status.state === "sleeping" && (await api(page, "GET", "/services/" + required.body.id)).body.status.state === "sleeping", "Explicit dependent stop did not release requirement");
  const linkedDelete = await api(page, "DELETE", "/services/" + required.body.id);
  assert(linkedDelete.status === 409, "Requires delete guard lost");
  const topology = (await api(page, "GET", "/topology")).body;
  assert(topology.edges.some((edge) => edge.kind === "requires" && edge.from === "service:" + dependent.body.id && edge.to === "service:" + required.body.id), "Real Requires topology missing");
  const contexts = [];
  const roles = [];
  for (const role of ["admin", "member"]) {
    const email = `${role}-${runId}@example.test`;
    const invite = await api(page, "POST", "/members", { email, role });
    assert(invite.status === 201, "Fixture role invite failed");
    const context = await page.context().browser().newContext();
    contexts.push(context);
    const target = await context.newPage();
    await target.goto(fixture.api);
    const registered = await api(target, "POST", "/auth/register", { email, name: role, password: "SyntheticOnly#Role19", invite_token: invite.body.invite_token });
    assert(registered.status === 201, "Invited fixture account registration failed: " + JSON.stringify(registered));
    await target.goto(fixture.api + "/services");
    await target.getByRole("heading", { name: "Services", exact: true }).waitFor();
    assert((await api(target, "GET", "/service-drivers")).status === 200, "Role could not read descriptors");
    const start = await api(target, "POST", "/services/" + dependent.body.id + "/start");
    assert(start.status === 403, "Non-owner bypassed transitive host execution gate");
    const secrets = (await api(target, "GET", "/services/" + required.body.id)).body;
    assert(secrets.config.env.SYNTHETIC_SECRET === "<redacted>" && !JSON.stringify(secrets).includes("synthetic-requirement-secret"), "Role read leaked sensitive config");
    if (role === "member") {
      assert(await target.getByRole("button", { name: "New service", exact: true }).count() === 0, "Member saw service mutation controls");
      assert((await api(target, "POST", "/services", { name: "forbidden", driver: "docker", config: { label: "fixture=absent" } })).status === 403, "Member service write permitted");
    } else {
      await target.getByRole("button", { name: "New service", exact: true }).first().click();
      assert(await target.getByRole("button", { name: "Command", exact: true }).count() === 0, "Admin saw prohibited Command picker");
      await target.getByRole("button", { name: "Cancel", exact: true }).click();
      assert((await api(target, "POST", "/services", { name: `admin-docker-${runId}`, driver: "docker", config: { label: "fixture=absent" } })).status === 201, "Admin allowed non-host driver write failed");
    }
    roles.push(role);
  }
  const foreignContext = await page.context().browser().newContext();
  contexts.push(foreignContext);
  const foreignPage = await foreignContext.newPage();
  await foreignPage.goto(fixture.api);
  const foreign = await api(foreignPage, "POST", "/auth/register", { email: `foreign-${runId}@example.test`, name: "Foreign", password: "SyntheticOnly#Foreign19" });
  assert(foreign.status === 201, "Second org registration failed");
  assert((await api(foreignPage, "GET", "/services/" + required.body.id)).status === 404, "Foreign org service disclosure");
  assert((await api(foreignPage, "POST", "/services", { name: "foreign-dependent", driver: "docker", config: { label: "fixture=absent" }, requires: [required.body.id] })).status === 400, "Foreign Requires edge permitted");
  const foreignTopology = (await api(foreignPage, "GET", "/topology")).body;
  assert(!JSON.stringify(foreignTopology).includes(required.body.id), "Foreign topology leaked requirement ID");
  for (const context of contexts) await context.close();
  assert(errors.length === 0, "Browser errors: " + errors.join("; "));
  return { result: "PASS", evidence: "real API owner/admin/member and two organizations", roles, ownerOrgId: owner.active_org_id, requiredId: required.body.id, dependentId: dependent.body.id, requires409AndExplicitStop: true, transitiveHost403: true, foreignOrg404: true };
}
