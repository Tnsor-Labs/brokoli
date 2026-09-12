import { expect, test, type Page } from "@playwright/test";

// The editor toolbar had 21 controls on one row and grew with every
// feature, so it broke twice in two days. The fix is structural: a
// schedule popover instead of an inline form, one contextual primary
// action, an overflow menu, and a command palette so a new feature can
// be reachable without widening the row (#555).
//
// These assert the property that kept regressing, not the appearance:
// the toolbar stays one row and nothing in it is clipped.

const pipeline = {
  id: "test",
  name: "a pipeline with a fairly long name",
  description: "",
  nodes: [
    {
      id: "n1",
      type: "source_file",
      name: "S",
      config: { path: "/tmp/x.csv" },
      position: { x: 120, y: 160 },
    },
  ],
  edges: [],
  schedule: "",
  schedule_timezone: "UTC",
  enabled: true,
  draft: false,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

async function openEditor(page: Page, overrides: Record<string, unknown> = {}) {
  await page.route("**/api/**", async (route) => {
    const p = new URL(route.request().url()).pathname;
    if (p === "/api/schedule/preview")
      return route.fulfill({
        json: {
          valid: true,
          cron: "0 9 * * 1-5",
          description: "Weekdays at 09:00",
          next: ["2026-09-14T09:00:00Z"],
        },
      });
    if (p === "/api/auth/setup") return route.fulfill({ json: { needs_setup: false } });
    if (p === "/api/auth/me")
      return route.fulfill({ json: { sub: "u", username: "admin", role: "admin" } });
    if (p === "/api/auth/me/permissions") return route.fulfill({ json: { permissions: [] } });
    if (p === "/api/connections") return route.fulfill({ json: [] });
    if (p === "/api/pipelines/test") return route.fulfill({ json: { ...pipeline, ...overrides } });
    await route.fulfill({ status: 404, json: { error: "not found" } });
  });
  await page.goto("/#/pipelines/test/edit");
  await expect(page.locator("svg.canvas")).toBeVisible();
}

// One row, and every control inside the viewport. Both halves matter:
// the row can stay short while a button hangs off the right edge, which
// is exactly what happened when Publish was added.
async function assertToolbarIntact(page: Page, width: number) {
  const toolbar = page.locator(".toolbar");
  const box = await toolbar.boundingBox();
  if (!box) throw new Error("the toolbar has no layout box");

  expect(box.height, `toolbar wrapped at ${width}px (height ${box.height})`).toBeLessThan(80);

  const controls = toolbar.locator("button:visible, a:visible, select:visible, input:visible");
  const n = await controls.count();
  expect(n, `no controls found at ${width}px`).toBeGreaterThan(0);

  for (let i = 0; i < n; i++) {
    const c = controls.nth(i);
    const cb = await c.boundingBox();
    if (!cb) continue;
    const label = (await c.getAttribute("title")) || (await c.innerText()) || `control ${i}`;
    expect(
      Math.round(cb.x + cb.width),
      `"${label.trim().slice(0, 30)}" is clipped at ${width}px: it ends at ${Math.round(cb.x + cb.width)}`,
    ).toBeLessThanOrEqual(width);
  }
}

for (const width of [1280, 1440, 1920]) {
  test(`the toolbar fits one row at ${width}px`, async ({ page }) => {
    await page.setViewportSize({ width, height: 900 });
    await openEditor(page);
    await assertToolbarIntact(page, width);
  });
}

// A draft carries Publish, which is the button that pushed Save off the
// screen. It must not cost a slot: it replaces Run rather than joining
// it.
test("a draft shows Publish instead of Run, not as well as", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 });
  await openEditor(page, { draft: true });

  await expect(page.getByRole("button", { name: "Publish" })).toBeVisible();
  await expect(page.getByRole("button", { name: /^Run$/ })).toHaveCount(0);
  await assertToolbarIntact(page, 1280);
});

test("a published pipeline shows Run and no Publish", async ({ page }) => {
  await page.setViewportSize({ width: 1280, height: 900 });
  await openEditor(page);

  await expect(page.getByRole("button", { name: /^Run$/ })).toBeVisible();
  await expect(page.getByRole("button", { name: "Publish" })).toHaveCount(0);
});

// The schedule is a form behind a button now. The button still states
// the schedule, because the point was to save space, not to hide it.
test("the schedule opens in a popover and does not move the toolbar", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await openEditor(page);

  const summary = page.locator(".schedule-summary");
  await expect(summary).toHaveText(/Manual/);

  const before = (await page.locator(".toolbar").boundingBox())?.height ?? 0;
  await summary.click();
  await expect(page.locator(".schedule-pop")).toBeVisible();
  const after = (await page.locator(".toolbar").boundingBox())?.height ?? 0;
  expect(after, "opening the schedule form resized the toolbar").toBe(before);

  await page.locator("#schedule-when").fill("every weekday at 9am");
  await expect(page.locator(".echo-cron")).toHaveText("0 9 * * 1-5");

  // Closed again, the button reports what was set.
  await page.keyboard.press("Escape");
  await expect(page.locator(".schedule-pop")).toHaveCount(0);
  await expect(summary).toHaveText(/Weekdays at 09:00/);
});

test("the long tail is in the overflow menu", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await openEditor(page);

  await page.getByRole("button", { name: "More actions" }).click();
  const menu = page.locator(".overflow-menu");
  for (const label of [
    "View runs",
    "Show YAML",
    "Validate nodes",
    "Version history",
    "Pipeline settings",
    "Duplicate pipeline",
  ]) {
    await expect(menu.getByText(label, { exact: true })).toBeVisible();
  }
});

// The app already had a palette: GlobalSearch, on Cmd+K. The editor
// registers its actions into that one rather than opening a second
// overlay on the same key, which is what a first attempt did.
test("the editor's actions appear in the one command palette", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await openEditor(page);

  await page.keyboard.press("ControlOrMeta+k");
  const modal = page.locator(".search-modal");
  await expect(modal).toHaveCount(1);

  // Actions the toolbar no longer shows are still reachable.
  await modal.locator("input").fill("yaml");
  await expect(modal.getByText("Show YAML", { exact: true })).toBeVisible();

  await modal.locator("input").fill("save");
  await expect(modal.getByText("Save", { exact: true })).toBeVisible();

  await page.keyboard.press("Escape");
  await expect(page.locator(".search-modal")).toHaveCount(0);
});

test("the palette runs an action instead of navigating", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await openEditor(page);

  await page.keyboard.press("ControlOrMeta+k");
  await page.locator(".search-modal input").fill("set schedule");
  await page.keyboard.press("Enter");

  await expect(page.locator(".search-modal")).toHaveCount(0);
  await expect(page.locator(".schedule-pop")).toBeVisible();
  // Still in the editor: an action is not a destination.
  expect(page.url()).toContain("/pipelines/test/edit");
});

// Every panel the editor can open must offer a way out. These were
// toolbar toggles, so a second click closed them; moving them into the
// overflow menu removed that second click and left the settings panel
// with no exit at all (#555).
test("every panel opened from the overflow can be closed again", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await openEditor(page);

  const panels = [
    { action: "Pipeline settings", panel: ".settings-panel" },
    { action: "Show YAML", panel: ".code-view" },
    { action: "Version history", panel: ".version-panel" },
  ];

  for (const { action, panel } of panels) {
    // Open from the overflow menu.
    await page.getByRole("button", { name: "More actions" }).click();
    await page.locator(".overflow-menu").getByText(action, { exact: true }).click();
    await expect(page.locator(panel), `${action} did not open`).toBeVisible();

    // A visible close control, not only a keyboard escape: someone who
    // opened this with the mouse should be able to shut it with one.
    await page.locator(panel).getByRole("button", { name: /close/i }).first().click();
    await expect(page.locator(panel), `${action} could not be closed by its button`).toHaveCount(0);
  }
});

test("Escape closes an open panel", async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await openEditor(page);

  await page.getByRole("button", { name: "More actions" }).click();
  await page.locator(".overflow-menu").getByText("Pipeline settings", { exact: true }).click();
  await expect(page.locator(".settings-panel")).toBeVisible();

  await page.keyboard.press("Escape");
  await expect(page.locator(".settings-panel")).toHaveCount(0);
});
