async (page) => {
  const fixture = await page.evaluate(() => window.__mishmeshFixture);
  await page.goto(fixture.api + "/connections/" + fixture.evidence.access.publicConnectionId);
  await page.getByText("Zone · Zone · Read", { exact: true }).waitFor();
  return { result: "PASS", zoneReadGuidance: true };
}
