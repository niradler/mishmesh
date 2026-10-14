async (page) => {
  const types = ["alpha", "beta"].map((type) => ({ type, label: type, role: "access", scope: "private", protocols: ["http"], levels: ["imported"], detect: true, fields: [{ key: "location", label: "Location", kind: "text" }], detect_fields: [{ key: "location", label: "Location", kind: "text" }] }));
  const waiting = [];
  await page.route("**/api/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path.endsWith("/connections/detect")) {
      return new Promise((resolve) => waiting.push(async (status, body) => { await route.fulfill({ status, json: body }); resolve(); }));
    }
    const json = path.endsWith("/auth/config") ? { auth_enabled: false } : path.endsWith("/capabilities") ? { host_commands: true } : path.endsWith("/connection-types") ? types : [];
    await route.fulfill({ json });
  });
  function assert(condition, message) { if (!condition) throw new Error(message); }
  await page.goto("http://127.0.0.1:8081/connections");
  await page.getByRole("button", { name: "Add connection", exact: true }).click();
  await page.getByRole("button", { name: "alpha", exact: true }).click();
  await page.getByLabel("Location", { exact: true }).fill("original");
  await page.getByRole("button", { name: "Detect", exact: true }).click();
  await page.getByRole("button", { name: "Detecting…", exact: true }).waitFor();
  await page.getByRole("button", { name: "beta", exact: true }).click();
  await page.getByRole("button", { name: "alpha", exact: true }).click();
  await page.getByLabel("Location", { exact: true }).fill("manual-after-type-cycle");
  const oldResponse = page.waitForResponse((response) => response.url().includes("/connections/detect"));
  await waiting.shift()(200, { location: "stale-original" });
  await oldResponse;
  assert(await page.getByLabel("Location", { exact: true }).inputValue() === "manual-after-type-cycle", "Old detect result survived type ABA cycle");
  await page.getByRole("button", { name: "Detect", exact: true }).click();
  await page.getByRole("button", { name: "Detecting…", exact: true }).waitFor();
  await page.getByLabel("Location", { exact: true }).fill("manual-during-detect");
  const editedResponse = page.waitForResponse((response) => response.url().includes("/connections/detect"));
  await waiting.shift()(200, { location: "stale-edit" });
  await editedResponse;
  await page.getByRole("button", { name: "Detect", exact: true }).waitFor();
  assert(await page.getByLabel("Location", { exact: true }).inputValue() === "manual-during-detect", "Detect clobbered newer field input");
  await page.getByRole("button", { name: "Detect", exact: true }).click();
  await page.getByRole("button", { name: "Detecting…", exact: true }).waitFor();
  const freshResponse = page.waitForResponse((response) => response.url().includes("/connections/detect"));
  await waiting.shift()(200, { location: "accepted" });
  await freshResponse;
  await page.getByText("Detected available connection fields. Review them before saving.", { exact: true }).waitFor();
  assert(await page.getByLabel("Location", { exact: true }).inputValue() === "accepted", "Current detect result was not applied");
  return { result: "PASS", evidence: "synthetic asynchronous descriptor Detect", typeCycle: true, newerInputPreserved: true, freshResultApplied: true };
}
