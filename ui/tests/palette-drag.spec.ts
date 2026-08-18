import { expect, test, type Page } from "@playwright/test";

const pipeline = {
  id: "test",
  name: "Pointer drag test",
  description: "",
  nodes: [],
  edges: [],
  schedule: "",
  enabled: true,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

async function openEditor(page: Page) {
  await page.route("**/api/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path === "/api/auth/setup") {
      await route.fulfill({ json: { needs_setup: false } });
    } else if (path === "/api/auth/me") {
      await route.fulfill({ json: { sub: "test-user", username: "test", role: "admin" } });
    } else if (path === "/api/auth/me/permissions") {
      await route.fulfill({ json: { permissions: [] } });
    } else if (path === "/api/pipelines/test") {
      await route.fulfill({ json: pipeline });
    } else {
      await route.fulfill({ status: 404, json: { error: "not found" } });
    }
  });

  await page.goto("/#/pipelines/test/edit");
  await expect(page.getByRole("button", { name: "File Source" })).toBeVisible();
  await expect(page.locator("svg.canvas")).toBeVisible();
}

async function dragToCanvas(page: Page, modifier?: "Control" | "Alt") {
  const source = page.getByRole("button", { name: "File Source" });
  const canvas = page.locator("svg.canvas");
  const sourceBox = await source.boundingBox();
  const canvasBox = await canvas.boundingBox();
  if (!sourceBox || !canvasBox) throw new Error("drag elements have no layout box");

  await page.mouse.move(sourceBox.x + sourceBox.width / 2, sourceBox.y + sourceBox.height / 2);
  await page.mouse.down();
  if (modifier) await page.keyboard.down(modifier);
  await page.mouse.move(canvasBox.x + canvasBox.width / 2, canvasBox.y + canvasBox.height / 2, {
    steps: 8,
  });
  await page.mouse.up();
  if (modifier) await page.keyboard.up(modifier);
}

test.beforeEach(async ({ page }) => {
  await openEditor(page);
});

test("adds exactly one node when dragged onto the SVG canvas", async ({ page }) => {
  await dragToCanvas(page);

  await expect(page.locator(".node-card")).toHaveCount(1);
  await expect(page.locator(".node-card")).toContainText("File Source");
});

test("modifier keys do not duplicate a dropped node", async ({ page }) => {
  await dragToCanvas(page, "Control");

  await expect(page.locator(".node-card")).toHaveCount(1);
});

test("cancelling a drag does not add a node", async ({ page }) => {
  const source = page.getByRole("button", { name: "File Source" });
  const canvas = page.locator("svg.canvas");
  const sourceBox = await source.boundingBox();
  const canvasBox = await canvas.boundingBox();
  if (!sourceBox || !canvasBox) throw new Error("drag elements have no layout box");

  await page.mouse.move(sourceBox.x + sourceBox.width / 2, sourceBox.y + sourceBox.height / 2);
  await page.mouse.down();
  await page.mouse.move(canvasBox.x + canvasBox.width / 2, canvasBox.y + canvasBox.height / 2, {
    steps: 8,
  });
  await page.keyboard.press("Escape");
  await page.mouse.up();

  await expect(page.locator(".node-card")).toHaveCount(0);
});

test("releasing outside the canvas does not add a node", async ({ page }) => {
  const source = page.getByRole("button", { name: "File Source" });
  const sourceBox = await source.boundingBox();
  if (!sourceBox) throw new Error("drag source has no layout box");

  await page.mouse.move(sourceBox.x + sourceBox.width / 2, sourceBox.y + sourceBox.height / 2);
  await page.mouse.down();
  await page.mouse.move(sourceBox.x + sourceBox.width / 2, 5, { steps: 8 });
  await page.mouse.up();

  await expect(page.locator(".node-card")).toHaveCount(0);
});
