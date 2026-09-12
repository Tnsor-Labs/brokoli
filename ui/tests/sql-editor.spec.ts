import { expect, test, type Page } from "@playwright/test";

// A query is code. It was being written in a four-line textarea with no
// line numbers and no highlighting, which scrolls out of sight by the
// third join. It now opens the same window the code node uses, with the
// SQL grammar instead of Python.

const pipeline = {
  id: "test",
  name: "sql",
  description: "",
  nodes: [
    {
      id: "n1",
      type: "source_db",
      name: "Customers",
      config: { conn_id: "pg-dev", query: "select id, name from customers where active = true" },
      position: { x: 160, y: 200 },
    },
  ],
  edges: [],
  schedule: "",
  enabled: true,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

async function openNode(page: Page, saved: string[]) {
  await page.route("**/api/**", async (route) => {
    const p = new URL(route.request().url()).pathname;
    if (p === "/api/pipelines/test" && route.request().method() === "PUT") {
      const body = route.request().postDataJSON();
      saved.push(body.nodes?.[0]?.config?.query ?? "");
      return route.fulfill({ json: pipeline });
    }
    if (p === "/api/connections")
      return route.fulfill({ json: [{ conn_id: "pg-dev", type: "postgres", description: "" }] });
    if (p === "/api/auth/setup") return route.fulfill({ json: { needs_setup: false } });
    if (p === "/api/auth/me")
      return route.fulfill({ json: { sub: "u", username: "admin", role: "admin" } });
    if (p === "/api/auth/me/permissions") return route.fulfill({ json: { permissions: [] } });
    if (p === "/api/pipelines/test") return route.fulfill({ json: pipeline });
    await route.fulfill({ status: 404, json: { error: "not found" } });
  });
  await page.goto("/#/pipelines/test/edit");
  await expect(page.locator("svg.canvas")).toBeVisible();
  await page.locator(".node-card").first().click();
  await expect(page.locator(".config-panel")).toBeVisible();
}

test("a SQL query opens in the editor, highlighted", async ({ page }) => {
  await openNode(page, []);

  await page.getByRole("button", { name: "Expand" }).first().click();

  const modal = page.locator(".modal-title");
  await expect(modal).toHaveText("SQL Query");

  // Highlighting is the must-have: Prism marks up the SQL rather than
  // dumping plain text, so the keywords carry token classes.
  const highlight = page.locator(".highlight-layer");
  await expect(highlight).toBeVisible();
  await expect(highlight.locator(".token.keyword").first()).toBeVisible();

  // And it is SQL grammar, not Python: "select" is a keyword here and
  // would be an ordinary identifier under the Python rules.
  await expect(highlight.locator(".token.keyword", { hasText: /select/i }).first()).toBeVisible();
});

test("editing in the window writes back to the node", async ({ page }) => {
  const saved: string[] = [];
  await openNode(page, saved);

  await page.getByRole("button", { name: "Expand" }).first().click();
  const area = page.locator("textarea.code-textarea");
  await area.fill("select count(*) from customers");

  await page.locator(".modal-overlay button.btn-save").click();
  await expect(page.locator(".modal-title")).toHaveCount(0);

  // The panel's own field reflects it, without needing a round trip.
  await expect(page.locator("#sql-query")).toHaveValue("select count(*) from customers");
});
