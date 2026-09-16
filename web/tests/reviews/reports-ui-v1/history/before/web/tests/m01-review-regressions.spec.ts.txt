import type { Locator, Page } from "@playwright/test";
import type { CatalogResponse, WorkItem } from "./api-contract";
import { catalogResponse, findingResponse, workItems, workResponse } from "./fixtures";
import { expect, requireProductionUI, test } from "./network";
import type { MockAPI } from "./network";

test.beforeEach(async ({ api }) => {
  expect(api.requests).toEqual([]);
  requireProductionUI();
});

async function reply(page: Page, api: MockAPI, baseURL: string | undefined, path: string, body: (url: URL) => unknown) {
  if (!baseURL) throw new Error("Missing local M01 test origin.");
  const origin = new URL(baseURL).origin;
  await page.route((url) => url.origin === origin && url.pathname === path, async (route) => {
    const request = route.request();
    if (request.method() !== "GET" || request.headers().authorization) {
      await route.fallback();
      return;
    }
    api.requests.push({ method: request.method(), path });
    await route.fulfill({ json: body(new URL(request.url())) });
  });
}

async function settleInput(page: Page) {
  await page.evaluate(async () => {
    for (let frame = 0; frame < 8; frame += 1) {
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
    }
  });
}

async function contextPosition(trigger: Locator) {
  return trigger.evaluate((element) => {
    const rect = element.getBoundingClientRect();
    let innerX = 0, innerY = 0;
    for (let parent = element.parentElement; parent && parent !== document.body; parent = parent.parentElement) {
      innerX += parent.scrollLeft;
      innerY += parent.scrollTop;
    }
    return {
      x: rect.x, y: rect.y, pageX: scrollX, pageY: scrollY, innerX, innerY,
      inViewport: rect.bottom > 0 && rect.top < innerHeight && rect.right > 0 && rect.left < innerWidth,
    };
  });
}

async function scrollInput(page: Page, point: { x: number; y: number }, distance: number) {
  await page.mouse.move(point.x, point.y);
  await page.mouse.wheel(0, distance);
  await settleInput(page);
}

const largeWork: WorkItem[] = Array.from({ length: 90 }, (_, index) => ({
  ...workItems[0], id: `synthetic-scroll-${index}`, title: `Synthetic scroll observation ${index}`,
  assetName: index % 2 === 0 ? "kept-context-repository" : "unrelated-repository",
}));
largeWork[50] = { ...workItems[0], assetName: "kept-context-repository" };

for (const mobile of [false, true]) {
  test.describe(mobile ? "M01 mobile-viewport dialog" : "M01 desktop dialog", () => {
    const viewport = mobile ? { width: 390, height: 844 } : { width: 1366, height: 768 };
    test.use({ viewport, isMobile: mobile, hasTouch: mobile });
    for (const reducedMotion of ["no-preference", "reduce"] as const) {
      test(`scroll lock and all dismissal paths preserve queue context (${reducedMotion})`, async ({ page, api, baseURL }) => {
        await page.emulateMedia({ reducedMotion });
        await reply(page, api, baseURL, "/api/v1/work", (url) => workResponse(url.searchParams.get("q") ?? "", largeWork));
        await reply(page, api, baseURL, `/api/v1/findings/${workItems[0].id}`, () => ({
          ...findingResponse, finding: {
            ...findingResponse.finding, ...largeWork[50],
            evidence: {
              ...findingResponse.finding.evidence,
              text: Array.from({ length: 40 }, (_, index) => `Synthetic scroll-only evidence line ${index + 1}.`).join("\n"),
            },
          },
        }));
        await page.goto("/#/work");
        const filter = page.getByRole("textbox", { name: "Filter findings" });
        await filter.fill("kept-context");
        const table = page.getByRole("table", { name: "Findings", includeHidden: true });
        await expect(table.getByRole("row").filter({ has: page.getByRole("cell") })).toHaveCount(45);
        const row = table.getByRole("row", { includeHidden: true }).filter({ hasText: workItems[0].title });
        const trigger = row.getByRole("button", { name: workItems[0].title, exact: true, includeHidden: true });
        await row.getByRole("checkbox").check();
        for (const dismissal of ["Escape", "close button", "hash Back"] as const) {
          await test.step(dismissal, async () => {
            await trigger.scrollIntoViewIfNeeded();
            await trigger.focus();
            await settleInput(page);
            const before = await contextPosition(trigger);
            expect(before.pageY + before.innerY, "The fixture must exercise a genuinely scrolled queue.").toBeGreaterThan(200);
            const url = page.url();
            await trigger.press("Enter");
            const dialog = page.getByRole("dialog", { name: workItems[0].title });
            await expect(dialog).toBeVisible();
            await expect(dialog.getByText("Synthetic scroll-only evidence line 40.", { exact: false })).toBeVisible();
            if (mobile) {
              const scrollable = await dialog.evaluate((element) => {
                let count = 0;
                for (const node of [element, ...element.querySelectorAll<HTMLElement>("*")]) {
                  if (node.scrollHeight > node.clientHeight && /auto|scroll/.test(getComputedStyle(node).overflowY)) {
                    node.scrollTop = node.scrollHeight;
                    count += 1;
                  }
                }
                return count;
              });
              expect(scrollable, "The mobile dialog must have real content to overscroll.").toBeGreaterThan(0);
              await expect(dialog.getByRole("button", { name: "Back to work" })).toBeInViewport();
              await scrollInput(page, { x: viewport.width / 2, y: viewport.height * 0.75 }, 600);
            } else {
              const box = await dialog.boundingBox();
              if (!box || box.x < 32) throw new Error("Desktop fixture needs an exposed modal backdrop.");
              await scrollInput(page, { x: box.x - 24, y: viewport.height / 2 }, 700);
            }
            const during = await contextPosition(trigger);
            // Check visual queue position so body-fixed and overflow-based locks are both valid.
            expect.soft(Math.abs(during.y - before.y), "Modal scrolling must not move the background queue.").toBeLessThanOrEqual(1);
            if (dismissal === "Escape") await page.keyboard.press("Escape");
            else if (dismissal === "close button") await dialog.getByRole("button", { name: "Close finding details" }).click();
            else await page.goBack();
            await expect(dialog).not.toBeVisible();
            await settleInput(page);
            const after = await contextPosition(trigger);
            for (const key of ["x", "y", "pageX", "pageY", "innerX", "innerY"] as const) {
              expect.soft(Math.abs(after[key] - before[key]), `${dismissal} must preserve queue ${key}.`).toBeLessThanOrEqual(1);
            }
            expect.soft(after.inViewport, "Restored focus must not leave the trigger offscreen.").toBe(true);
            expect.soft(await trigger.evaluate((element) => element === document.activeElement), "Dismissal must restore trigger focus.").toBe(true);
            await expect.soft(page).toHaveURL(url);
            await expect.soft(filter).toHaveValue("kept-context");
            await expect.soft(row.getByRole("checkbox")).toBeChecked();
          });
        }
        await trigger.scrollIntoViewIfNeeded();
        const unlocked = await contextPosition(trigger);
        await scrollInput(page, { x: viewport.width * 0.6, y: viewport.height * 0.75 }, -180);
        expect((await contextPosition(trigger)).y, "Queue scrolling must work again after dismissal.").toBeGreaterThan(unlocked.y + 20);
      });
    }
  });
}

test("M01 skip link focuses main without changing any destination or hash", async ({ page }) => {
  for (const [destination, heading] of [
    ["work", "Work"], ["assets", "Assets"], ["integrations", "Integrations"],
    ["reports", "Reports"], ["settings", "Settings"], ["gallery", "Synthetic component gallery"],
  ]) {
    await test.step(destination, async () => {
      await page.goto(`/#/${destination}`);
      const main = page.getByRole("main");
      await expect(main.getByRole("heading", { name: heading, exact: true })).toBeVisible();
      const url = page.url();
      await page.getByRole("link", { name: "Skip to content" }).focus();
      await page.keyboard.press("Enter");
      await settleInput(page);
      expect.soft(page.url(), `${destination}: skip navigation must not rewrite the router hash.`).toBe(url);
      expect.soft(await main.evaluate((element) => element === document.activeElement), `${destination}: focus belongs on main.`).toBe(true);
      await expect.soft(main.getByRole("heading", { name: heading, exact: true })).toBeVisible({ timeout: 500 });
    });
  }
});

test("M01 catalog verification is independent of readiness and never trusts synthetic or planned claims", async ({ page, api, baseURL }) => {
  let response: CatalogResponse = structuredClone(catalogResponse);
  await reply(page, api, baseURL, "/api/v1/integrations/catalog", () => response);
  await page.goto("/#/integrations");
  await expect(page.getByRole("heading", { name: "GitHub", exact: true })).toBeVisible();
  for (const scenario of [
    { origin: "live", maturity: "supported", ready: false, state: "passed", verified: true },
    { origin: "live", maturity: "supported", ready: true, state: "passed", verified: true },
    { origin: "synthetic", maturity: "supported", ready: true, state: "passed", verified: false },
    { origin: "live", maturity: "planned", ready: true, state: "passed", verified: false },
    { origin: "synthetic", maturity: "planned", ready: true, state: "passed", verified: false },
    { origin: "live", maturity: "supported", ready: true, state: "not-run", verified: false },
  ] as const) {
    await test.step(`${scenario.origin}/${scenario.maturity}/${scenario.state}/ready=${scenario.ready}`, async () => {
      response = structuredClone(catalogResponse);
      response.dataOrigin = scenario.origin;
      const reason = `Controlled ${scenario.origin}/${scenario.maturity}/${scenario.state} receipt; ready=${scenario.ready}.`;
      Object.assign(response.items[0], {
        supportMaturity: scenario.maturity, readyToConnect: scenario.ready,
        liveVerification: { state: scenario.state, reason },
      });
      await page.reload();
      const card = page.getByRole("list", { name: "Native integrations" }).getByRole("listitem")
        .filter({ has: page.getByRole("heading", { name: "GitHub", exact: true }) });
      await expect(card).toBeVisible();
      await expect(card.getByText(reason, { exact: true })).toBeVisible();
      const verified = card.getByText(/^(?:(?:api|source) reports )?(?:live )?(?:verification passed|verified)$/i);
      const unverified = card.getByText(/^(?:not verified|unverified|not run)$/i);
      await expect.soft(verified).toHaveCount(scenario.verified ? 1 : 0, { timeout: 500 });
      await expect.soft(unverified).toHaveCount(scenario.verified ? 0 : 1, { timeout: 500 });
    });
  }
});
