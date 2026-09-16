import type { Locator, Page } from "@playwright/test";
import { alpha, beta, detailedFinding, expect, test } from "./application-fixture";
import { workItems } from "./fixtures";
import { requireProductionUI } from "./network";

type InputMode = "pointer" | "keyboard";

test.use({ reducedMotion: "reduce" });
test.beforeEach(async ({ app }) => {
  requireProductionUI();
  expect(app.requests).toEqual([]);
});

async function inViewport(page: Page, candidates: Locator): Promise<Locator | null> {
  const viewport = page.viewportSize();
  if (!viewport) throw new Error("Responsive session checks require an explicit viewport.");
  for (let index = 0; index < await candidates.count(); index += 1) {
    const candidate = candidates.nth(index);
    if (!await candidate.isVisible()) continue;
    const box = await candidate.boundingBox();
    if (box && box.x >= 0 && box.y >= 0 && box.x + box.width <= viewport.width && box.y + box.height <= viewport.height) {
      return candidate;
    }
  }
  return null;
}

async function keyboardReach(page: Page, target: Locator) {
  for (let step = 0; step < 32; step += 1) {
    if (await target.count() && await target.evaluate((element) => element === document.activeElement)) {
      await expect(target).toBeVisible();
      await expect(target).toBeEnabled();
      await expect(target, "A focused session control must not remain offscreen.").toBeInViewport({ ratio: 0.99 });
      return;
    }
    const focused = page.locator(":focus");
    const inMenu = await focused.count() > 0 && await focused.first().evaluate((element) =>
      element.matches('[role="menuitem"], [role="menuitemcheckbox"], [role="menuitemradio"]') &&
      element.closest('[role="menu"], [role="menubar"]') !== null);
    await page.keyboard.press(inMenu ? "ArrowDown" : "Tab");
    const next = page.locator(":focus");
    if (await next.count()) {
      await expect(next.first(), "Keyboard navigation must not enter a hidden focus trap.").toBeVisible();
      await expect(next.first(), "Keyboard navigation must bring focused controls onscreen.").toBeInViewport({ ratio: 0.99 });
    }
  }
  await expect(target, "Session controls must be reachable through actual keyboard navigation.").toBeFocused();
}

async function sessionControl(page: Page, name: "Workspace" | "Sign out", mode: InputMode) {
  const candidates = name === "Workspace"
    ? page.getByRole("combobox", { name, exact: true })
    : page.getByRole("button", { name, exact: true }).or(page.getByRole("menuitem", { name, exact: true }));
  let target = await inViewport(page, candidates);
  if (!target) {
    const opener = await inViewport(page, page.getByRole("button", {
      name: /menu|navigation|account|profile|session|workspace|sidebar|controls/i,
    }));
    expect(opener, `${name} must be reachable directly or through a visible, accessibly named session/navigation menu.`).not.toBeNull();
    if (!opener) throw new Error(`No reachable ${name} control or accessible menu opener.`);
    await expect(opener).toBeEnabled();
    if (mode === "keyboard") {
      await keyboardReach(page, opener);
      await page.keyboard.press("Enter");
    } else {
      await opener.click();
    }
    await expect.poll(async () => await inViewport(page, candidates) !== null,
      { message: `Opening the menu must expose an onscreen ${name} control.` }).toBe(true);
    target = await inViewport(page, candidates);
  }
  if (!target) throw new Error(`The ${name} control is not reachable.`);
  await expect(target).toBeVisible();
  await expect(target).toBeEnabled();
  await expect(target).toBeInViewport({ ratio: 0.99 });
  if (mode === "keyboard") await keyboardReach(page, target);
  else await target.click({ trial: true });
  return target;
}

for (const viewport of [
  { width: 390, height: 844 }, { width: 683, height: 900 },
  { width: 850, height: 900 }, { width: 851, height: 900 },
]) {
  for (const mode of ["pointer", "keyboard"] as const) {
    test(`authenticated session controls remain reachable at ${viewport.width}px by ${mode}`, async ({ page, app }) => {
      await page.setViewportSize(viewport);
      await page.goto("/#/work");
      await expect(page.getByRole("table", { name: "Findings", exact: true })).toBeVisible();
      await expect(page.getByText(detailedFinding.title, { exact: true })).toBeVisible();
      const workspace = await sessionControl(page, "Workspace", mode);
      await expect(workspace).toHaveValue(alpha.id);

      const switchBoundary = app.requests.length;
      app.holdWork(beta.id);
      if (mode === "pointer") await workspace.selectOption(beta.id);
      else {
        await page.keyboard.press("End");
        await page.keyboard.press("Enter");
        await page.keyboard.press("Escape");
      }
      await expect.poll(() => app.requests.slice(switchBoundary).some((call) =>
        call.method === "GET" && call.path === "/api/v1/work" && call.workspace === beta.id)).toBe(true);
      for (const title of [detailedFinding.title, workItems[1].title]) {
        await expect(page.getByText(title, { exact: true }), "Alpha data must leave the DOM before the held Beta response.").toHaveCount(0);
      }
      app.releaseWork();
      // A modal session menu may still cover the queue; its scoped Beta result
      // must nevertheless replace Alpha before the user signs out.
      await expect(page.getByText(workItems[2].title, { exact: true }).first()).toBeAttached();

      const signOut = await sessionControl(page, "Sign out", mode);
      if (mode === "keyboard") await page.keyboard.press("Enter");
      else await signOut.click();
      await expect.poll(() => app.calls("POST", "/api/v1/logout").length).toBe(1);
      await expect(page.getByRole("form", { name: "Sign in", exact: true })).toBeVisible();
      await expect(page.getByRole("table", { name: /^(Findings|Assets)$/, includeHidden: true })).toHaveCount(0);
      await expect(page.getByRole("combobox", { name: "Workspace", exact: true, includeHidden: true })).toHaveCount(0);
      for (const title of [detailedFinding.title, workItems[1].title, workItems[2].title]) {
        await expect(page.getByText(title, { exact: true })).toHaveCount(0);
      }
      const scopedReads = app.requests.slice(switchBoundary).filter((call) => call.method === "GET" && call.path !== "/api/v1/session");
      expect(scopedReads.every((call) => call.workspace === beta.id), "All data reads after switching must carry Beta's server scope.").toBe(true);
      expect(app.calls("POST", "/api/v1/login")).toEqual([]);
      expect((await page.context().cookies()).filter((cookie) => cookie.name === "aspm_session")).toEqual([]);
    });
  }
}
