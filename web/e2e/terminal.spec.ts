import { test, expect } from "@playwright/test";
test("authenticated terminal, real order, cancel, operations, account and responsive layout", async ({
  page,
}) => {
  const errors: string[] = [];
  page.on("pageerror", (e) => errors.push(e.message));
  await page.goto("/");
  await page
    .getByLabel("Local API key")
    .fill(process.env.DEMO_API_KEY ?? "local-alice-key-00000001");
  await page.getByRole("button", { name: "Open terminal" }).click();
  await expect(page.getByText("● Streaming", { exact: true })).toBeVisible();
  await expect(
    page.getByRole("heading", { name: "Trading terminal" }),
  ).toBeVisible();
  await page.getByLabel("Price per coin", { exact: true }).fill("10");
  await page.getByLabel("Quantity", { exact: true }).fill("0.001");
  const placed = page.waitForResponse(
    (r) => r.url().endsWith("/api/orders") && r.request().method() === "POST",
  );
  await page.getByRole("button", { name: "Buy BTC" }).click();
  const outcome = await (await placed).json();
  expect(outcome.order.status).toBe("NEW");
  await expect(page.getByRole("status")).toContainText("NEW");
  await page
    .getByRole("row")
    .filter({ hasText: outcome.order.client_id.slice(0, 12) })
    .getByRole("button", { name: "Cancel", exact: true })
    .click();
  await expect(page.getByRole("status")).toContainText(
    "Cancellation committed",
  );
  await page.evaluate(() => window.scrollTo(0, 0));
  await page.screenshot({
    path: "../docs/screenshots/terminal.png",
    fullPage: true,
  });
  await page.getByRole("button", { name: "Operations" }).click();
  await expect(
    page.getByRole("heading", { name: "System overview" }),
  ).toBeVisible();
  await expect(page.getByText("Recovered · accepting").first()).toBeVisible();
  await page.evaluate(() => window.scrollTo(0, 0));
  await page.screenshot({
    path: "../docs/screenshots/operations.png",
    fullPage: true,
  });
  await page.getByRole("button", { name: "Account", exact: true }).click();
  await expect(
    page.getByRole("heading", { name: "Account overview" }),
  ).toBeVisible();
  await expect(
    page.getByRole("heading", { name: "Simulated asset balances" }),
  ).toBeVisible();
  await page.evaluate(() => window.scrollTo(0, 0));
  await page.screenshot({
    path: "../docs/screenshots/account.png",
    fullPage: true,
  });
  await page.setViewportSize({ width: 390, height: 844 });
  await page.getByRole("button", { name: "Terminal" }).click();
  await expect(
    page.getByRole("heading", { name: "Trading terminal" }),
  ).toBeVisible();
  await expect(page.getByRole("button", { name: "Buy BTC" })).toBeVisible();
  await page.evaluate(() => window.scrollTo(0, 0));
  await page.screenshot({
    path: "../docs/screenshots/mobile.png",
    fullPage: true,
  });
  expect(errors).toEqual([]);
});
test("Grafana dashboard provisions engine metrics", async ({ page }) => {
  const base = process.env.GRAFANA_URL ?? "http://localhost:3000";
  await page.goto(base + "/login");
  await page.getByRole("textbox", { name: "Email or username" }).fill("admin");
  await page
    .getByLabel("Password", { exact: true })
    .fill(process.env.GRAFANA_PASSWORD ?? "matchcore-local-only");
  const login = page.waitForResponse(
    (r) => r.url() === base + "/login" && r.request().method() === "POST",
  );
  await page.getByRole("button", { name: "Log in", exact: true }).click();
  expect((await login).ok()).toBeTruthy();
  await page.waitForURL((url) => !url.pathname.includes("login"));
  await page.goto(base + "/d/matchcore");
  await expect(
    page.getByText("Command throughput", { exact: true }),
  ).toBeVisible();
  await page.evaluate(() => window.scrollTo(0, 0));
  await page.screenshot({
    path: "../docs/screenshots/grafana.png",
    fullPage: true,
  });
});
