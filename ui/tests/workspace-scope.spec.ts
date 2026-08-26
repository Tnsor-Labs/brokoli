import { expect, test, type Page } from "@playwright/test";

// #231: the browser held a workspace id that the server it was talking to had
// never heard of, so every scoped list came back an empty 200 and the app
// reported "no pipelines" while the run panel -- scoped by org rather than
// workspace -- listed runs of the pipelines it would not show.
//
// localStorage is keyed by origin, not by instance: http://localhost:8088
// serves whichever build was last run there, so this is the ordinary case for
// anyone who runs more than one Brokoli, not an exotic one.
//
// What these tests pin is the rule that fixes it: a stored workspace id is a
// hint until a workspace list confirms it, and an unconfirmed hint is never
// put on the wire. They assert on the headers of the request the page
// actually makes, because that is the thing that was wrong.

const STALE = "ws-from-another-instance";

const pipeline = {
  id: "p1",
  name: "Nightly load",
  description: "",
  nodes: [],
  edges: [],
  schedule: "",
  enabled: true,
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
};

interface Options {
  /** What GET /api/workspaces answers. 404 models a build without one. */
  workspaces?: Array<{ id: string; name: string; slug: string; description: string }>;
  user?: string;
}

/** Records the X-Workspace-ID of every scoped request the page makes. */
async function loadPipelines(page: Page, opts: Options = {}) {
  const scopedHeaders: Array<string | undefined> = [];

  await page.route("**/api/**", async (route) => {
    const req = route.request();
    const path = new URL(req.url()).pathname;

    if (path === "/api/auth/setup") {
      await route.fulfill({ json: { needs_setup: false } });
    } else if (path === "/api/auth/me") {
      await route.fulfill({
        json: { sub: opts.user ?? "user-1", username: "test", role: "admin" },
      });
    } else if (path === "/api/auth/me/permissions") {
      await route.fulfill({ json: { permissions: [] } });
    } else if (path === "/api/workspaces") {
      if (!opts.workspaces) {
        await route.fulfill({ status: 404, json: { error: "not found" } });
      } else {
        await route.fulfill({ json: opts.workspaces });
      }
    } else if (path === "/api/pipelines/summary") {
      const scope = req.headers()["x-workspace-id"];
      scopedHeaders.push(scope);
      // Filter the way the server does, so a stale scope reproduces the
      // bug rather than being merely recorded: an empty 200, never an
      // error the page could show.
      const known = new Set([...(opts.workspaces ?? []).map((w) => w.id), "default"]);
      await route.fulfill({ json: scope && !known.has(scope) ? [] : [pipeline] });
    } else {
      await route.fulfill({ status: 404, json: { error: "not found" } });
    }
  });

  await page.goto("/#/pipelines");
  await expect(page.getByText("Nightly load")).toBeVisible();
  return scopedHeaders;
}

/** Seeds the stale value the way a previous instance would have left it. */
async function seedStaleWorkspace(page: Page, owner?: string) {
  await page.addInitScript(
    ([ws, user]) => {
      localStorage.setItem("brokoli-workspace", ws as string);
      if (user) localStorage.setItem("brokoli-workspace-user", user as string);
    },
    [STALE, owner ?? ""],
  );
}

test("a build with no workspace list never sends a stored id", async ({ page }) => {
  await seedStaleWorkspace(page);

  const headers = await loadPipelines(page); // /api/workspaces 404s

  expect(headers.length).toBeGreaterThan(0);
  for (const h of headers) {
    expect(h, "an unconfirmed workspace id must not reach the server").toBeUndefined();
  }
});

test("a stored id absent from the workspace list is replaced, not sent", async ({ page }) => {
  await seedStaleWorkspace(page);

  const headers = await loadPipelines(page, {
    workspaces: [{ id: "real-one", name: "Real", slug: "real", description: "" }],
  });

  expect(headers.length).toBeGreaterThan(0);
  for (const h of headers) {
    expect(h).toBe("real-one");
    expect(h).not.toBe(STALE);
  }
});

test("a stored id the list confirms is used as-is", async ({ page }) => {
  await page.addInitScript(() => {
    localStorage.setItem("brokoli-workspace", "mine");
  });

  const headers = await loadPipelines(page, {
    workspaces: [
      { id: "default", name: "Default", slug: "default", description: "" },
      { id: "mine", name: "Mine", slug: "mine", description: "" },
    ],
  });

  expect(headers.length).toBeGreaterThan(0);
  for (const h of headers) {
    expect(h, "a confirmed choice must survive").toBe("mine");
  }
});

test("a workspace stored for another user is not carried over", async ({ page }) => {
  // user-1 left "mine" behind; user-2 signs in on the same browser profile.
  await page.addInitScript(() => {
    localStorage.setItem("brokoli-workspace", "mine");
    localStorage.setItem("brokoli-workspace-user", "user-1");
  });

  const headers = await loadPipelines(page, {
    user: "user-2",
    workspaces: [
      { id: "default", name: "Default", slug: "default", description: "" },
      { id: "mine", name: "Mine", slug: "mine", description: "" },
    ],
  });

  expect(headers.length).toBeGreaterThan(0);
  for (const h of headers) {
    expect(h, "user-2 must not inherit user-1's workspace").not.toBe("mine");
  }
});

test("the page shows pipelines it would previously have hidden", async ({ page }) => {
  // The symptom, stated as the user saw it: a seeded stale id used to make
  // this page report zero while the data was there all along.
  await seedStaleWorkspace(page);
  await loadPipelines(page);

  await expect(page.getByText("Nightly load")).toBeVisible();
  await expect(page.getByText("Build your first pipeline")).toHaveCount(0);
});
