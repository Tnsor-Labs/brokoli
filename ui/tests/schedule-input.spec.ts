import { expect, test, type Page } from "@playwright/test";

// The schedule field takes plain language and compiles it to cron. The
// echo is the feature: natural language without feedback is worse than
// cron, because a misreading is invisible until something runs at the
// wrong time or not at all (#552).
//
// These stub the preview endpoint rather than reaching a server: what is
// under test here is that the editor asks the right question and shows
// the answer. The grammar itself is covered by Go tests, which is where
// it lives.

const pipeline = {
  id: "test",
  name: "schedule test",
  description: "",
  nodes: [
    {
      id: "n1",
      type: "source_api",
      name: "Fetch",
      config: { url: "/x", method: "GET" },
      position: { x: 120, y: 160 },
    },
  ],
  edges: [],
  schedule: "",
  schedule_timezone: "Africa/Maputo",
  enabled: true,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

type PreviewCall = { input: string; timezone: string };

async function openEditor(page: Page, calls: PreviewCall[]) {
  await page.route("**/api/**", async (route) => {
    const p = new URL(route.request().url()).pathname;

    if (p === "/api/schedule/preview") {
      const body = route.request().postDataJSON();
      calls.push({ input: body.input, timezone: body.timezone });

      if (body.input === "every weekday at 9am") {
        await route.fulfill({
          json: {
            valid: true,
            cron: "0 9 * * 1-5",
            description: "Weekdays at 09:00",
            next: ["2026-09-14T09:00:00+02:00", "2026-09-15T09:00:00+02:00"],
          },
        });
        return;
      }
      await route.fulfill({
        json: {
          valid: false,
          error: "every 90 minutes cannot be expressed as a cron schedule",
          suggestion: 'the nearest options are "every hour" and "every 2 hours"',
        },
      });
      return;
    }

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
}

test("a phrase shows what it compiled to and when it will run", async ({ page }) => {
  const calls: PreviewCall[] = [];
  await openEditor(page, calls);

  await page.locator(".schedule-field").fill("every weekday at 9am");

  const echo = page.locator(".schedule-echo");
  await expect(echo).toBeVisible();

  // What was understood, the cron it became, and when it fires. All
  // three, or the echo is not doing its job.
  await expect(echo.locator(".echo-desc")).toContainText("Weekdays at 09:00");
  await expect(echo.locator(".echo-cron")).toHaveText("0 9 * * 1-5");
  await expect(echo.locator(".echo-next")).toContainText("Next:");

  // The zone is always named. "09:00" without a zone is the ambiguity
  // this feature makes worse if left unsaid.
  await expect(echo.locator(".echo-tz")).toHaveText("Africa/Maputo");

  // And the pipeline's timezone is what was actually asked about.
  expect(calls.at(-1)?.timezone).toBe("Africa/Maputo");
});

test("a refusal explains itself and offers a way forward", async ({ page }) => {
  const calls: PreviewCall[] = [];
  await openEditor(page, calls);

  await page.locator(".schedule-field").fill("every 90 minutes");

  const echo = page.locator(".schedule-echo");
  await expect(echo).toHaveClass(/invalid/);
  await expect(echo.locator(".echo-error")).toContainText("cannot be expressed as a cron schedule");
  await expect(echo.locator(".echo-suggestion")).toContainText("every hour");

  // A refused schedule shows no cron and no occurrences: there is
  // nothing to run, and implying otherwise would be the rounding this
  // grammar exists to avoid.
  await expect(echo.locator(".echo-cron")).toHaveCount(0);
  await expect(echo.locator(".echo-next")).toHaveCount(0);
});

test("typing is debounced rather than asking on every keystroke", async ({ page }) => {
  const calls: PreviewCall[] = [];
  await openEditor(page, calls);

  await page.locator(".schedule-field").pressSequentially("every weekday at 9am", { delay: 15 });
  await expect(page.locator(".schedule-echo")).toBeVisible();

  // Twenty characters typed. Without debouncing this would be roughly
  // twenty requests.
  expect(calls.length, `made ${calls.length} preview requests while typing`).toBeLessThan(6);
  expect(calls.at(-1)?.input).toBe("every weekday at 9am");
});
