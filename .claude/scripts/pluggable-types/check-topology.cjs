async (page) => {
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  const nodes = [{ id: "connection:public", kind: "connection", label: "Public connection", state: "up", ref: { kind: "connection", id: "public" } }, { id: "connection:private", kind: "connection", label: "Private connection", state: "down", ref: { kind: "connection", id: "private" } }];
  const edges = [];
  for (let index = 0; index < 20; index++) {
    nodes.push({ id: `hostname:public:${index}`, kind: "hostname", label: `service-${index}.example.test`, state: "up" }, { id: `endpoint:${index}`, kind: "endpoint", label: `Endpoint ${index}`, state: "up", ref: { kind: "endpoint", id: String(index) } }, { id: `service:${index}`, kind: "service", label: `Service ${index}`, state: "sleeping", detail: `Service ${index} requires another service. ` + "Bounded tooltip details. ".repeat(12), ref: { kind: "service", id: String(index) } });
    edges.push({ from: "connection:public", to: `hostname:public:${index}`, kind: "routes" }, { from: `hostname:public:${index}`, to: `endpoint:${index}`, kind: "serves" }, { from: `endpoint:${index}`, to: `service:${index}`, kind: "wakes" });
    if (index) edges.push({ from: `service:${index}`, to: `service:${index - 1}`, kind: "requires" });
  }
  nodes.push({ id: "hostname:private:0", kind: "hostname", label: "service-0", state: "unknown" });
  edges.push({ from: "connection:private", to: "hostname:private:0", kind: "routes" }, { from: "hostname:private:0", to: "endpoint:0", kind: "serves" });
  await page.route("**/api/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path.startsWith("/api/v1/connections/")) return route.fulfill({ status: 404, json: { error: "Synthetic connection not found" } });
    const body = path.endsWith("/auth/config") ? { auth_enabled: false } : path.endsWith("/topology") ? { nodes, edges } : [];
    await route.fulfill({ json: body });
  });
  const results = [];
  for (const [width, height] of [[1920, 1080], [1024, 768], [390, 844], [844, 390]]) {
    await page.setViewportSize({ width, height });
    await page.goto("http://127.0.0.1:5188/network");
    const map = page.getByRole("region", { name: "Network topology" });
    await map.waitFor();
    await page.waitForFunction((count) => document.querySelectorAll('svg path[marker-end]').length === count, edges.length);
    const dimensions = await map.evaluate((element) => {
      const box = element.getBoundingClientRect();
      const main = document.querySelector("main");
      return { width: innerWidth, height: innerHeight, documentWidth: document.documentElement.scrollWidth, documentHeight: document.documentElement.scrollHeight, mainWidth: main.clientWidth, mainScrollWidth: main.scrollWidth, mainHeight: main.clientHeight, mainScrollHeight: main.scrollHeight, mapHeight: box.height, mapBottom: box.bottom, mainBottom: main.getBoundingClientRect().bottom, scrollHeight: element.scrollHeight, clientHeight: element.clientHeight };
    });
    if (dimensions.documentWidth > width || dimensions.documentHeight > height || dimensions.mainScrollWidth > dimensions.mainWidth || dimensions.mainScrollHeight > dimensions.mainHeight || dimensions.mapHeight <= 0 || dimensions.mapBottom > dimensions.mainBottom + 1 || dimensions.scrollHeight <= dimensions.clientHeight) throw new Error(`Network bounds: ${JSON.stringify(dimensions)}`);
    const curves = await map.locator('path[stroke-dasharray="10 3 2 3"]').evaluateAll((paths) => paths.map((path) => ({ right: path.getBBox().x + path.getBBox().width, width: path.ownerSVGElement.getBoundingClientRect().width })));
    if (curves.length !== 19 || curves.some((curve) => curve.right > curve.width)) throw new Error("Requires curves escape the canvas");
    await page.getByRole("button", { name: "Expand Network", exact: true }).click();
    await page.getByRole("button", { name: "Exit full screen", exact: true }).waitFor();
    const expanded = await map.boundingBox();
    if (!expanded || expanded.height <= dimensions.mapHeight) throw new Error("Expanded Network did not gain bounded space");
    await page.keyboard.press("Escape");
    await page.getByRole("button", { name: "Expand Network", exact: true }).waitFor();
    await page.getByRole("link", { name: "Open connection: Private connection", exact: true }).click();
    await page.waitForURL("**/connections/private");
    await page.getByText("Synthetic connection not found", { exact: true }).waitFor();
    results.push({ width, height, mapHeight: dimensions.mapHeight, requiresCurves: curves.length, fullScreen: true, connectionLink: true });
  }
  if (errors.length) throw new Error(`Browser errors: ${errors.join("; ")}`);
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto("http://127.0.0.1:5188/network");
  await page.getByRole("region", { name: "Network topology" }).waitFor();
  await page.screenshot({ path: "output/playwright/topology-desktop.png" });
  return { result: "PASS", evidence: "synthetic dense topology", results };
}
