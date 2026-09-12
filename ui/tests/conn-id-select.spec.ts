import { expect, test, type Page } from "@playwright/test";

// Selecting a connection on a database node used to lose the conn_id.
//
// The picker called updateConfig twice: once to set conn_id, then once
// to clear uri. Each call built its payload from `node`, which is a
// prop, so the second call spread a config that did not yet contain
// conn_id and the parent applied that payload last. The node was saved
// with neither, and the server refused it with "'uri' or 'conn_id' is
// required for source_db".
//
// This test drives the real picker and inspects what actually goes over
// the wire on save, because that is where the key went missing.

const pipeline = {
  id: "test",
  name: "conn id test",
  description: "",
  nodes: [
    {
      id: "src",
      type: "source_db",
      name: "Source DB",
      config: { query: "select 1" },
      position: { x: 200, y: 160 },
    },
  ],
  edges: [],
  schedule: "",
  enabled: true,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

const connections = [{ conn_id: "pg-main", type: "postgres", description: "primary warehouse" }];

async function openEditor(page: Page, onSave: (body: unknown) => void) {
  await page.route("**/api/**", async (route) => {
    const url = new URL(route.request().url());
    const path = url.pathname;
    if (route.request().method() === "PUT" && path === "/api/pipelines/test") {
      onSave(route.request().postDataJSON());
      await route.fulfill({ json: { ...pipeline } });
      return;
    }
    if (path === "/api/auth/setup") {
      await route.fulfill({ json: { needs_setup: false } });
    } else if (path === "/api/auth/me") {
      await route.fulfill({ json: { sub: "u", username: "test", role: "admin" } });
    } else if (path === "/api/auth/me/permissions") {
      await route.fulfill({ json: { permissions: [] } });
    } else if (path === "/api/connections") {
      await route.fulfill({ json: connections });
    } else if (path === "/api/pipelines/test") {
      await route.fulfill({ json: pipeline });
    } else {
      await route.fulfill({ status: 404, json: { error: "not found" } });
    }
  });

  await page.goto("/#/pipelines/test/edit");
  await expect(page.locator("svg.canvas")).toBeVisible();
}

test("selecting a connection keeps conn_id on the saved node", async ({ page }) => {
  let saved: any = null;
  await openEditor(page, (body) => {
    saved = body;
  });

  // Open the node's config panel.
  await page.locator(".node-card").first().click();

  // Scoped to the panel, not `select.first()` on the page. That was
  // unambiguous when this was written and stopped being so the moment
  // the toolbar gained a timezone picker (#552), which sits earlier in
  // the DOM: both PRs were green alone and red together, and main went
  // red on the merge. A locator that names where it is looking cannot
  // be captured by an unrelated control appearing above it.
  const picker = page.locator(".config-panel select").first();
  await expect(picker).toBeVisible();

  await picker.selectOption("pg-main");

  // The panel should now show it is using the connection rather than a
  // manual URI. Against the broken version this already fails: the
  // second dispatch lands without conn_id, so `usingConnection` reads
  // false from the refreshed prop and the panel snaps back to the manual
  // URI field. That is the symptom a user sees.
  await expect(page.getByText("Using connection:")).toBeVisible();

  await page.getByRole("button", { name: /^Save/ }).click();
  await expect.poll(() => saved !== null, { timeout: 5000 }).toBe(true);

  const node = saved.nodes.find((n: any) => n.id === "src");
  expect(node, "the source_db node is missing from the saved pipeline").toBeTruthy();
  expect(
    node.config.conn_id,
    "conn_id was dropped between selecting the connection and saving; " +
      "the server will refuse this pipeline with \"'uri' or 'conn_id' is required\"",
  ).toBe("pg-main");
  expect(node.config.uri ?? "").toBe("");
});
