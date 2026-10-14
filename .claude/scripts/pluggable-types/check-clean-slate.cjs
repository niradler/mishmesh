async (page) => {
  const fixture = await page.evaluate(() => window.__mishmeshFixture);
  const result = await page.evaluate(async () => {
    const response = await fetch("/api/v1/cloudflare");
    return response.status;
  });
  if (result !== 404) throw new Error("Retired singleton API remains available");
  await page.goto(fixture.api + "/cloudflare");
  await page.getByText("This page does not exist.", { exact: true }).waitFor();
  if (new URL(page.url()).pathname !== "/cloudflare") throw new Error("Retired page redirected");
  await page.goto(fixture.api + "/connections");
  await page.getByRole("heading", { name: "Connections", exact: true }).waitFor();
  return { result: "PASS", typedConnections: true, singletonApiAbsent: true, oldPageNotRedirected: true };
}
