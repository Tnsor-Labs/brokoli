import { expect, test, type Page } from "@playwright/test";

// The reported case: "when I want to create a new pipeline without using
// a template, I don't find a way."
//
// There was no way. #106 made persistence fail-closed, so an empty
// pipeline could not be saved, so the seeded "Blank" template was
// filtered out of the list rather than allowed to produce a 400. The
// modal then insisted on a template that did not exist (#107).

const templates = [
  {
    id: "hello-world",
    name: "Hello World",
    description: "Minimal: fetch, transform, save",
    icon: "file",
    nodes: [{ id: "s1", type: "source_file", name: "S", config: {}, position: { x: 0, y: 0 } }],
    edges: [],
  },
];

type CreateCall = { name: string; draft?: boolean; nodes: unknown[] };

async function openList(page: Page, calls: CreateCall[]) {
  await page.route("**/api/**", async (route) => {
    const p = new URL(route.request().url()).pathname;
    const method = route.request().method();

    if (p === "/api/pipelines" && method === "POST") {
      const body = route.request().postDataJSON();
      calls.push({ name: body.name, draft: body.draft, nodes: body.nodes ?? [] });
      await route.fulfill({
        json: { ...body, id: "new-pipe", created_at: "2026-01-01T00:00:00Z" },
      });
      return;
    }
    if (p === "/api/templates") return route.fulfill({ json: templates });
    if (p === "/api/pipelines" || p === "/api/pipelines/summary")
      return route.fulfill({ json: [] });
    if (p === "/api/workspaces") return route.fulfill({ json: [] });
    if (p === "/api/system/info") return route.fulfill({ json: { version: "dev" } });
    if (p === "/api/scheduler/status") return route.fulfill({ json: [] });

    if (p === "/api/auth/setup") return route.fulfill({ json: { needs_setup: false } });
    if (p === "/api/auth/me")
      return route.fulfill({ json: { sub: "u", username: "admin", role: "admin" } });
    if (p === "/api/auth/me/permissions") return route.fulfill({ json: { permissions: [] } });
    await route.fulfill({ status: 404, json: { error: "not found" } });
  });

  await page.goto("/#/pipelines");
}

test("a pipeline can be started from scratch", async ({ page }) => {
  const calls: CreateCall[] = [];
  await openList(page, calls);

  await page
    .getByRole("button", { name: /New Pipeline|Create/i })
    .first()
    .click();

  // The option exists at all, which is the whole complaint.
  const scratch = page.getByRole("button", { name: /Start from scratch/i });
  await expect(scratch).toBeVisible();
  await scratch.click();

  await page.getByPlaceholder("e.g. daily-customer-sync").fill("from scratch");
  await page.locator("button.btn-primary", { hasText: /Create/i }).click();

  await expect.poll(() => calls.length, { timeout: 5000 }).toBe(1);
  expect(calls[0].draft, "starting from scratch must create a draft").toBe(true);
  expect(calls[0].nodes, "a scratch pipeline starts empty").toEqual([]);

  // And it opens the editor, because an empty pipeline left on the list
  // page is the moment someone most needs the canvas.
  await expect.poll(() => page.url()).toContain("/pipelines/new-pipe/edit");
});

test("a draft is marked in the list and cannot be run from it", async ({ page }) => {
  await page.route("**/api/**", async (route) => {
    const p = new URL(route.request().url()).pathname;
    if (p === "/api/pipelines" || p === "/api/pipelines/summary")
      return route.fulfill({
        json: [
          {
            id: "d1",
            name: "half built",
            draft: true,
            enabled: true,
            nodes: [],
            edges: [],
            created_at: "2026-01-01T00:00:00Z",
            updated_at: "2026-01-01T00:00:00Z",
          },
        ],
      });
    if (p === "/api/templates") return route.fulfill({ json: templates });
    if (p === "/api/workspaces") return route.fulfill({ json: [] });
    if (p === "/api/system/info") return route.fulfill({ json: { version: "dev" } });
    if (p === "/api/scheduler/status") return route.fulfill({ json: [] });
    if (p === "/api/auth/setup") return route.fulfill({ json: { needs_setup: false } });
    if (p === "/api/auth/me")
      return route.fulfill({ json: { sub: "u", username: "admin", role: "admin" } });
    if (p === "/api/auth/me/permissions") return route.fulfill({ json: { permissions: [] } });
    await route.fulfill({ status: 404, json: { error: "not found" } });
  });
  await page.goto("/#/pipelines");

  await expect(page.locator(".draft-badge")).toHaveText("Draft");

  // Disabled with a reason, not hidden: an absent button reads as a bug,
  // a disabled one with a tooltip teaches the rule.
  const run = page.getByRole("button", { name: /Run half built/i });
  await expect(run).toBeDisabled();
  await expect(run).toHaveAttribute("title", /draft/i);
});
