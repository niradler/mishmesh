async (page) => {
  const errors = [];
  page.on("pageerror", (error) => errors.push(error.message));
  const nodes = [{ id: "tunnel", kind: "tunnel", label: "Lab tunnel", state: "connected", ref: { kind: "cloudflare" } }];
  const edges = [];
  for (let index = 0; index < 20; index++) {
    nodes.push(
      { id: `hostname-${index}`, kind: "hostname", label: `service-${index}.example.test`, state: "published" },
      { id: `endpoint-${index}`, kind: "endpoint", label: `Endpoint ${index}`, state: "online", ref: { kind: "endpoint", id: `endpoint-${index}` } },
      { id: `target-${index}`, kind: "target", label: `Target ${index}`, state: "healthy", detail: "http://127.0.0.1:8080" },
    );
    edges.push(
      { from: "tunnel", to: `hostname-${index}`, kind: "routes" },
      { from: `hostname-${index}`, to: `endpoint-${index}`, kind: "serves" },
      { from: `endpoint-${index}`, to: `target-${index}`, kind: "proxies" },
    );
  }
  await page.route("**/api/v1/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path.startsWith("/api/v1/endpoints/")) {
      await route.fulfill({ status: 404, json: { error: "Endpoint not found" } });
      return;
    }
    const body = path.endsWith("/auth/config")
      ? { auth_enabled: false, password_enabled: false, password_signup: false, google_enabled: false, signup_mode: "org" }
      : path.endsWith("/topology") ? { nodes, edges } : [];
    await route.fulfill({ json: body });
  });
  const results = [];
  for (const [width, height] of [[1920, 1080], [1440, 900], [1024, 768], [390, 844], [844, 390]]) {
    await page.setViewportSize({ width, height });
    await page.goto("http://127.0.0.1:5179/network");
    const map = page.getByRole("region", { name: "Network topology" });
    await map.waitFor();
    await page.waitForFunction(() => document.querySelectorAll('svg path[marker-end]').length === 60);
    const dimensions = await map.evaluate((element) => {
      const bounds = element.getBoundingClientRect();
      const main = document.querySelector("main");
      const mainBounds = main.getBoundingClientRect();
      const wrapper = main.firstElementChild.getBoundingClientRect();
      return {
        viewportWidth: innerWidth, viewportHeight: innerHeight,
        documentWidth: document.documentElement.scrollWidth, documentHeight: document.documentElement.scrollHeight,
        mainWidth: main.clientWidth, mainScrollWidth: main.scrollWidth,
        mainHeight: main.clientHeight, mainScrollHeight: main.scrollHeight,
        wrapperWidth: wrapper.width, mainOverflow: getComputedStyle(main).overflow,
        mapHeight: bounds.height, mapBottom: bounds.bottom, mainBottom: mainBounds.bottom,
        mapOverflow: getComputedStyle(element).overflow,
        mapScrollHeight: element.scrollHeight, mapClientHeight: element.clientHeight,
      };
    });
    if (dimensions.documentWidth > width || dimensions.documentHeight > height
      || dimensions.mainScrollWidth > dimensions.mainWidth || dimensions.mainScrollHeight > dimensions.mainHeight
      || dimensions.wrapperWidth !== dimensions.mainWidth || dimensions.mainOverflow !== "hidden"
      || dimensions.mapHeight <= 0 || dimensions.mapBottom > dimensions.mainBottom
      || dimensions.mapOverflow !== "auto" || dimensions.mapScrollHeight <= dimensions.mapClientHeight) {
      throw new Error(`Layout failure: ${JSON.stringify(dimensions)}`);
    }
    await map.evaluate((element) => { element.scrollTop = element.scrollHeight; element.scrollLeft = element.scrollWidth; });
    await page.getByRole("link", { name: "Open endpoint: Endpoint 9", exact: true }).focus();
    await page.keyboard.press("Enter");
    await page.waitForURL("**/endpoints/endpoint-9");
    await page.getByText("Endpoint not found", { exact: true }).waitFor();
    await page.waitForFunction(() => {
      const main = document.querySelector("main");
      return main && getComputedStyle(main).overflowY === "auto";
    });
    const normalLayout = await page.locator("main").evaluate((element) => ({
      overflow: getComputedStyle(element).overflowY,
      maxWidth: getComputedStyle(element.firstElementChild).maxWidth,
    }));
    if (normalLayout.overflow !== "auto" || normalLayout.maxWidth !== "1152px") {
      throw new Error(`Other page layout changed: ${JSON.stringify(normalLayout)}`);
    }
    results.push({ width, height, dimensions, normalLayout });
  }
  if (errors.length) throw new Error(`Browser errors: ${JSON.stringify(errors)}`);
  await page.setViewportSize({ width: 1920, height: 1080 });
  await page.goto("http://127.0.0.1:5179/network");
  await page.getByRole("region", { name: "Network topology" }).waitFor();
  await page.screenshot({ path: "output/playwright/network-desktop.png" });
  console.log(JSON.stringify(results, null, 2));
}
