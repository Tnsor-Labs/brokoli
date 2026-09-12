import { expect, test, type Page } from "@playwright/test";

// Test Connection only ever read config.uri. Selecting a connection
// clears uri, so the one configuration that carries working credentials
// was the one the button refused to test: it answered "Enter a URI
// first" over a node that had everything it needed.

const connections = [{ conn_id: "pg-dev", type: "postgres", description: "local" }];

function pipelineWith(config: Record<string, unknown>) {
  return {
    id: "test",
    name: "conn test",
    description: "",
    nodes: [
      { id: "n1", type: "source_db", name: "Customers", config, position: { x: 160, y: 200 } },
    ],
    edges: [],
    schedule: "",
    enabled: true,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  };
}

type Tested = { path: string; method: string; body: unknown };

async function openNode(page: Page, config: Record<string, unknown>, tested: Tested[]) {
  await page.route("**/api/**", async (route) => {
    const url = new URL(route.request().url());
    const p = url.pathname;

    if (p.startsWith("/api/connections/") && p.endsWith("/test")) {
      tested.push({ path: p, method: route.request().method(), body: null });
      return route.fulfill({ json: { success: true, driver: "pgx" } });
    }
    if (p === "/api/test-connection") {
      tested.push({
        path: p,
        method: route.request().method(),
        body: route.request().postDataJSON(),
      });
      return route.fulfill({ json: { success: true, driver: "pgx" } });
    }
    if (p === "/api/connections") return route.fulfill({ json: connections });
    if (p === "/api/auth/setup") return route.fulfill({ json: { needs_setup: false } });
    if (p === "/api/auth/me")
      return route.fulfill({ json: { sub: "u", username: "admin", role: "admin" } });
    if (p === "/api/auth/me/permissions") return route.fulfill({ json: { permissions: [] } });
    if (p === "/api/pipelines/test") return route.fulfill({ json: pipelineWith(config) });
    await route.fulfill({ status: 404, json: { error: "not found" } });
  });

  await page.goto("/#/pipelines/test/edit");
  await expect(page.locator("svg.canvas")).toBeVisible();
  await page.locator(".node-card").first().click();
  await expect(page.locator(".config-panel")).toBeVisible();
}

test("a node using a connection is tested by that connection", async ({ page }) => {
  const tested: Tested[] = [];
  await openNode(page, { conn_id: "pg-dev", query: "select 1" }, tested);

  await page.getByRole("button", { name: /Test Connection/i }).click();

  await expect.poll(() => tested.length, { timeout: 5000 }).toBe(1);
  expect(tested[0].path, "the stored connection should be tested by id").toBe(
    "/api/connections/pg-dev/test",
  );

  // The credentials live on the server, encrypted. Rebuilding a URI in
  // the browser would test something other than what the pipeline uses.
  expect(tested[0].body).toBeNull();
});

test("a node using a manual URI still tests that URI", async ({ page }) => {
  const tested: Tested[] = [];
  await openNode(page, { uri: "postgres://user:pw@host/db", query: "select 1" }, tested);

  await page.getByRole("button", { name: /Test Connection/i }).click();

  await expect.poll(() => tested.length, { timeout: 5000 }).toBe(1);
  expect(tested[0].path).toBe("/api/test-connection");
  expect((tested[0].body as { uri: string }).uri).toBe("postgres://user:pw@host/db");
});

test("a node with neither is told what it needs, and nothing is called", async ({ page }) => {
  const tested: Tested[] = [];
  await openNode(page, { query: "select 1" }, tested);

  await page.getByRole("button", { name: /Test Connection/i }).click();

  // The warning names both ways out, because either one is valid.
  await expect(page.getByText(/Choose a connection, or enter a URI/i)).toBeVisible();
  expect(tested, "nothing should be tested when nothing is configured").toEqual([]);
});
