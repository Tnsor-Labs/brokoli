import { expect, test, type Page } from "@playwright/test";

// Deleting a node left the editor broken: the deleted node stayed in the
// config panel, an edge to nowhere stayed on the canvas, and no other
// node could be selected or deleted afterwards.
//
// The cause was `bind:node` on the canvas's `{#each nodes as node}`.
// Binding to an each-block item writes back by position, so when the
// array shrank the binding left a NodeCard holding an undefined node.
// Its reactive `node.type` threw, which aborted the Svelte update
// mid-flight: the canvas was half re-rendered and the editor's own state
// never committed, which is why the panel kept showing a node that no
// longer existed.

const pipeline = {
  id: "test",
  name: "delete test",
  description: "",
  nodes: [
    {
      id: "n1",
      type: "source_api",
      name: "Fetch Employees",
      config: { url: "/api/samples/data/employees.json", method: "GET" },
      position: { x: 120, y: 160 },
    },
    { id: "n2", type: "transform", name: "Add Column", config: {}, position: { x: 380, y: 160 } },
    {
      id: "n3",
      type: "sink_file",
      name: "Save Result",
      config: { path: "/tmp/out.json", format: "json" },
      position: { x: 640, y: 160 },
    },
  ],
  edges: [
    { from: "n1", to: "n2" },
    { from: "n2", to: "n3" },
  ],
  schedule: "",
  enabled: true,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

async function openEditor(page: Page): Promise<string[]> {
  const errors: string[] = [];
  page.on("pageerror", (err) => errors.push(err.message));

  await page.route("**/api/**", async (route) => {
    const p = new URL(route.request().url()).pathname;
    if (p === "/api/auth/setup") await route.fulfill({ json: { needs_setup: false } });
    else if (p === "/api/auth/me")
      await route.fulfill({ json: { sub: "u", username: "t", role: "admin" } });
    else if (p === "/api/auth/me/permissions") await route.fulfill({ json: { permissions: [] } });
    else if (p === "/api/connections") await route.fulfill({ json: [] });
    else if (p === "/api/pipelines/test") await route.fulfill({ json: pipeline });
    else await route.fulfill({ status: 404, json: { error: "not found" } });
  });

  await page.goto("/#/pipelines/test/edit");
  await expect(page.locator("svg.canvas")).toBeVisible();
  return errors;
}

test("deleting a node leaves the editor usable", async ({ page }) => {
  const errors = await openEditor(page);
  const panelTitle = page.locator(".panel-title");

  await page.locator(".node-card", { hasText: "Fetch Employees" }).click();
  await expect(panelTitle).toHaveText("API Source");

  await page.getByRole("button", { name: "Delete" }).click();

  // The node goes, and so does the panel: nothing is selected now.
  await expect(page.locator(".node-card")).toHaveCount(2);
  await expect(panelTitle).toHaveCount(0);

  // Nothing threw. This is the assertion that actually catches the bug:
  // every symptom above followed from an exception aborting the update.
  expect(errors, `page threw during delete: ${errors.join(" | ")}`).toEqual([]);

  // And the editor still works: another node can be selected, its
  // properties shown, and it can be deleted in turn.
  await page.locator(".node-card", { hasText: "Add Column" }).click();
  await expect(panelTitle).toHaveText("Transform");

  await page.getByRole("button", { name: "Delete" }).click();
  await expect(page.locator(".node-card")).toHaveCount(1);
  await expect(panelTitle).toHaveCount(0);

  await page.locator(".node-card", { hasText: "Save Result" }).click();
  await expect(panelTitle).toHaveText("File Output");

  expect(errors, `page threw while working after a delete: ${errors.join(" | ")}`).toEqual([]);
});

// Dragging is what the binding existed for, so it has to keep working
// now that the card reports its position instead of writing it.
test("a node can still be dragged", async ({ page }) => {
  const errors = await openEditor(page);

  const card = page.locator(".node-card", { hasText: "Add Column" });
  const before = await card.boundingBox();
  if (!before) throw new Error("the node has no layout box");

  await page.mouse.move(before.x + before.width / 2, before.y + before.height / 2);
  await page.mouse.down();
  await page.mouse.move(before.x + before.width / 2 + 120, before.y + before.height / 2 + 60, {
    steps: 10,
  });
  await page.mouse.up();

  const after = await card.boundingBox();
  if (!after) throw new Error("the node vanished while dragging");

  // Asserting movement, not a distance. The node travels less than the
  // cursor does because the canvas is scaled, and that ratio is
  // unchanged by this fix: the original bind:node code moves it exactly
  // as far. Pinning the number here would be pinning the zoom factor.
  expect(Math.round(after.x - before.x), "the node did not move horizontally").toBeGreaterThan(20);
  expect(Math.round(after.y - before.y), "the node did not move vertically").toBeGreaterThan(10);
  expect(errors, `page threw while dragging: ${errors.join(" | ")}`).toEqual([]);
});
