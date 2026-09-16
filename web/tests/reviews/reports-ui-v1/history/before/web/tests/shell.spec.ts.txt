import type { Locator, Page } from "@playwright/test";
import { catalogResponse, findingResponse, workItems } from "./fixtures";
import { expect, requireProductionUI, test } from "./network";

test.beforeEach(async ({ api }) => {
  expect(api.requests).toEqual([]);
  requireProductionUI();
});

function findingAction(page: Page): Locator {
  const row = page.getByRole("row").filter({ hasText: workItems[0].title });
  return row.getByRole("button", { name: workItems[0].title, exact: true })
    .or(row.getByRole("link", { name: workItems[0].title, exact: true }));
}

async function tabTo(page: Page, target: Locator): Promise<void> {
  for (let step = 0; step < 30; step += 1) {
    if (await target.evaluate((element) => element === document.activeElement)) return;
    await page.keyboard.press("Tab");
  }
  await expect(target, "Visible queue actions must be reachable with the keyboard.").toBeFocused();
}

async function pagePalette(page: Page): Promise<string> {
  return page.getByRole("main").evaluate((element) => {
    const colors: string[] = [];
    for (let current: Element | null = element; current; current = current.parentElement) {
      const style = getComputedStyle(current);
      colors.push(style.color, style.backgroundColor, style.backgroundImage);
    }
    return JSON.stringify(colors);
  });
}

async function expectNoDecorativeMovement(page: Page): Promise<void> {
  const violations = await page.evaluate(async () => {
    const failures = new Set<string>();
    const movementKeys = ["transform", "translate", "rotate", "scale", "left", "right", "top", "bottom", "marginLeft", "marginTop"];
    for (let frame = 0; frame < 6; frame += 1) {
      for (const animation of document.getAnimations()) {
        if (animation.playState !== "running" || !(animation.effect instanceof KeyframeEffect)) continue;
        const timing = animation.effect.getTiming();
        if (timing.iterations === Infinity) failures.add("continuous animation");
        const frames = animation.effect.getKeyframes() as Array<Record<string, unknown>>;
        for (const key of movementKeys) {
          const values = frames.map((item) => item[key]).filter((value) => value !== undefined);
          if (new Set(values.map(String)).size > 1) failures.add(key);
        }
      }
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
    }
    return [...failures];
  });
  expect(violations, "Reduced motion must affect composed CSS and Web Animations, not just a preference marker.").toEqual([]);
}

test("primary navigation exposes the five focused destinations", async ({ page }) => {
  await page.goto("/");
  const navigation = page.getByRole("navigation", { name: "Primary" });
  const destinations = ["Work", "Assets", "Integrations", "Reports", "Settings"];
  await expect(navigation.getByRole("link")).toHaveCount(destinations.length);
  for (const destination of destinations) {
    const link = navigation.getByRole("link", { name: destination, exact: true });
    await link.click();
    await expect(page.getByRole("main").getByRole("heading", { name: destination, exact: true })).toBeVisible();
    await expect(link).toHaveAttribute("aria-current", "page");
  }
});

test("theme follows the system and preserves an explicit light/dark choice", async ({ page }) => {
  await page.emulateMedia({ colorScheme: "dark" });
  await page.goto("/");
  await expect(page.locator("html")).toHaveCSS("color-scheme", /dark/);
  const darkPalette = await pagePalette(page);
  await page.getByRole("button", { name: "Color theme" }).click();
  await page.getByRole("menuitem", { name: "Light", exact: true }).click();
  await expect(page.locator("html")).toHaveCSS("color-scheme", /light/);
  await expect.poll(() => pagePalette(page)).not.toBe(darkPalette);
  await page.reload();
  await expect(page.locator("html")).toHaveCSS("color-scheme", /light/);
  await page.getByRole("button", { name: "Color theme" }).click();
  await page.getByRole("menuitem", { name: "Dark", exact: true }).click();
  await expect(page.locator("html")).toHaveCSS("color-scheme", /dark/);
});

for (const reducedMotion of ["no-preference", "reduce"] as const) {
  test(`finding details retain filter, selection and keyboard focus (${reducedMotion})`, async ({ page, api }) => {
    await page.emulateMedia({ reducedMotion });
    await page.goto("/");
    const table = page.getByRole("table", { name: "Findings" });
    await expect(table).toBeVisible();
    expect(await table.getByRole("columnheader").count(), "The default queue must not become a wall of columns.").toBeLessThanOrEqual(8);
    const filter = page.getByRole("textbox", { name: "Filter findings" });
    await filter.fill("alpha");
    await expect(table.getByRole("row").filter({ has: page.getByRole("cell") })).toHaveCount(2);
    const row = table.getByRole("row").filter({ hasText: workItems[0].title });
    const checkbox = row.getByRole("checkbox");
    await tabTo(page, checkbox);
    await page.keyboard.press("Space");
    await expect(checkbox).toBeChecked();
    expect(await checkbox.evaluate((element) => element === document.activeElement),
      "Selection feedback must not steal keyboard focus.").toBe(true);
    const action = findingAction(page);
    await expect(action).toHaveCount(1);
    await tabTo(page, action);
    await page.keyboard.press("Enter");
    const panel = page.getByRole("dialog", { name: workItems[0].title });
    await expect(panel).toBeVisible();
    await expect(panel.getByText(findingResponse.finding.evidence.text, { exact: true })).toBeVisible();
    await expect(panel.locator("em")).toHaveCount(0);
    expect(await panel.evaluate((element) => element.contains(document.activeElement))).toBe(true);
    if (reducedMotion === "reduce") await expectNoDecorativeMovement(page);
    await page.keyboard.press("Escape");
    expect(await action.evaluate((element) => element === document.activeElement),
      "Focus restoration must not wait for a decorative exit animation.").toBe(true);
    await expect(panel).not.toBeVisible();
    await expect(filter).toHaveValue("alpha");
    await expect(row.getByRole("checkbox")).toBeChecked();
    expect(api.requests.every((request) => request.method === "GET")).toBe(true);
  });
}

test("loading, failure, retry, empty and permission denial are distinct with no sample fallback", async ({ page, api }) => {
  api.holdWork();
  await page.goto("/");
  await expect(page.getByRole("status").filter({ hasText: /loading findings/i })).toBeVisible();
  await expect(page.getByRole("navigation", { name: "Primary" }).getByRole("link", { name: "Settings" })).toBeEnabled();
  api.workState = 503;
  api.releaseWork();
  await expect(page.getByRole("alert")).toContainText(/unavailable|unable to load|could not load/i);
  await expect(page.getByRole("row").filter({ has: page.getByRole("cell") })).toHaveCount(0);
  const retry = page.getByRole("button", { name: "Retry", exact: true });
  await expect(retry).toBeVisible();
  api.workState = "empty";
  await retry.click();
  await expect(page.getByText("No findings", { exact: true })).toBeVisible();
  await expect(page.getByRole("alert")).toHaveCount(0);
  api.workState = 403;
  await page.reload();
  await expect(page.getByRole("alert")).toContainText(/permission|access|forbidden/i);
  await expect(page.getByRole("row").filter({ has: page.getByRole("cell") })).toHaveCount(0);
  await expect(page.getByText("No findings", { exact: true })).toHaveCount(0);
});

test("the eight native families remain explicitly unverified with synthetic catalog data", async ({ page }) => {
  await page.goto("/");
  await page.getByRole("navigation", { name: "Primary" }).getByRole("link", { name: "Integrations", exact: true }).click();
  const catalog = page.getByRole("list", { name: "Native integrations" });
  await expect(catalog.getByRole("listitem")).toHaveCount(8);
  for (const family of catalogResponse.items) {
    const card = catalog.getByRole("listitem").filter({ has: page.getByRole("heading", { name: family.name, exact: true }) });
    await expect(card).toHaveCount(1);
    await expect(card.getByText(/not verified|unverified|not run/i)).toBeVisible();
    await expect(card.getByText(/^live verified$|^verified$|^ready to connect$/i)).toHaveCount(0);
  }
});

test("the gallery labels synthetic states instead of presenting them as operational evidence", async ({ page }, testInfo) => {
  await page.goto("/");
  await page.getByRole("navigation", { name: "Primary" }).getByRole("link", { name: "Settings", exact: true }).click();
  await page.getByRole("link", { name: "Component gallery", exact: true }).click();
  await expect(page.getByRole("heading", { name: "Synthetic component gallery", exact: true })).toBeVisible();
  const notice = page.getByRole("note", { name: "Synthetic data notice" });
  await expect(notice).toContainText(/synthetic/i);
  await expect(notice).toContainText(/not live|not.*verification/i);
  const samples = page.getByRole("region", { name: "State samples" });
  for (const state of ["Empty", "Loading", "Error", "Permission denied", "Partial data", "Success"]) {
    await expect(samples.getByRole("heading", { name: state, exact: true })).toBeVisible();
  }
  await testInfo.attach("synthetic-gallery-light", { body: await page.screenshot(), contentType: "image/png" });
});
