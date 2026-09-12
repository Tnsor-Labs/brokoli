import { expect, test, type Page } from "@playwright/test";

// A query is code. It was being written in a four-line textarea with no
// line numbers and no highlighting, which scrolls out of sight by the
// third join. It now opens the same window the code node uses, with the
// SQL grammar instead of Python.
//
// The first version of this reached that window through a small "Expand"
// link beside the label while the code node had a full-width "Open Full
// Editor" button with a preview: two affordances for the same job. These
// tests pin the shared one, on both nodes, so they cannot drift apart
// again.

function pipelineWith(node: Record<string, unknown>) {
  return {
    id: "test",
    name: "sql",
    description: "",
    nodes: [{ position: { x: 160, y: 200 }, ...node }],
    edges: [],
    schedule: "",
    enabled: true,
    created_at: "2026-01-01T00:00:00Z",
    updated_at: "2026-01-01T00:00:00Z",
  };
}

const dbNode = {
  id: "n1",
  type: "source_db",
  name: "Customers",
  config: { conn_id: "pg-dev", query: "select id, name from customers where active = true" },
};

async function openNode(page: Page, node: Record<string, unknown>, saved: string[] = []) {
  const pipeline = pipelineWith(node);
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
  await openNode(page, dbNode);

  await page.getByRole("button", { name: "Open Full Editor" }).click();

  await expect(page.locator(".modal-title")).toHaveText("SQL Query");

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
  await openNode(page, dbNode);

  await page.getByRole("button", { name: "Open Full Editor" }).click();
  await page.locator("textarea.code-textarea").fill("select count(*) from customers");

  await page.locator(".modal-overlay button.btn-save").click();
  await expect(page.locator(".modal-title")).toHaveCount(0);

  // The panel shows what is stored without needing a round trip, which
  // is what the preview is for.
  await expect(page.locator(".code-preview")).toHaveText("select count(*) from customers");
});

// The complaint that started this: the code node had one control for
// this and the database node had another. One component now, so the
// button, the preview and the empty state are identical on both.
for (const { label, node, title } of [
  { label: "a database source", node: dbNode, title: "SQL Query" },
  {
    label: "a code node",
    node: { id: "n1", type: "code", name: "Reshape", config: { script: "print(1)" } },
    title: "Script",
  },
]) {
  test(`${label} reaches its editor through the same control`, async ({ page }) => {
    await openNode(page, node);

    const group = page.locator(".field-group", { hasText: title });
    await expect(group.getByRole("button", { name: "Open Full Editor" })).toBeVisible();
    await expect(group.locator(".code-preview")).toBeVisible();

    // No second way in: the small Expand link is gone, not merely hidden.
    await expect(page.getByRole("button", { name: "Expand" })).toHaveCount(0);
    await expect(page.locator("textarea.code-input")).toHaveCount(0);
  });
}

// The node runs Python today, but the product now runs JVM and
// TypeScript too, so the runtime does not belong in the node's name. The
// one Python-specific field keeps saying Python, because it is a path to
// a Python interpreter.
test("the node is called Code, not Python Code", async ({ page }) => {
  await openNode(page, { id: "n1", type: "code", name: "Reshape", config: { script: "x = 1" } });

  const panel = page.locator(".config-panel");
  await expect(panel).toContainText("Code");
  await expect(panel).not.toContainText("Python Code");
  await expect(panel.locator(".field-group", { hasText: "Script" })).toBeVisible();
  await expect(panel).toContainText("Python Path");
});

test("an empty query says so where the preview would be", async ({ page }) => {
  await openNode(page, { ...dbNode, config: { conn_id: "pg-dev" } });

  await expect(page.locator(".code-empty")).toHaveText("No query written yet");
  await expect(page.locator(".code-preview")).toHaveCount(0);
});

// The window was built for the code node, and its footer documented the
// Python contract. Opening a query in it showed `output_data` and
// `print(..., file=sys.stderr)` over a SQL editor.
test("the editor's footer describes the language it is editing", async ({ page }) => {
  await openNode(page, dbNode);

  await page.getByRole("button", { name: "Open Full Editor" }).click();
  const footer = page.locator(".modal-footer");
  await expect(footer).toContainText("the connection selected on this node");
  await expect(footer).not.toContainText("output_data");
  await expect(footer).not.toContainText("sys.stderr");
});

test("the code node's footer still describes the Python contract", async ({ page }) => {
  await openNode(page, { id: "n1", type: "code", name: "Reshape", config: { script: "x = 1" } });

  await page.getByRole("button", { name: "Open Full Editor" }).click();
  const footer = page.locator(".modal-footer");
  await expect(footer).toContainText("output_data");
});
