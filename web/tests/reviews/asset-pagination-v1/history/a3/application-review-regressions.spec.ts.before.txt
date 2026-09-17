import type { Locator, Page } from "@playwright/test";
import {
  alpha, createdAsset, expect, originalAsset, scope, sourceId, sourceScanAt, test, updatedAsset,
} from "./application-fixture";
import { requireProductionUI } from "./network";

test.use({ reducedMotion: "reduce" });
test.beforeEach(async ({ app }) => {
  requireProductionUI();
  expect(app.requests).toEqual([]);
});

const report = JSON.stringify({ version: "2.1.0", runs: [{
  tool: { driver: { name: "Synthetic workflow review" } },
  results: [{ ruleId: "synthetic-review", level: "warning", message: { text: "Synthetic observation only." } }],
}] }) + "\n";

async function submitReport(page: Page) {
  await page.getByRole("button", { name: "Import report", exact: true }).click();
  const form = page.getByRole("form", { name: "Import report", exact: true });
  await form.getByLabel("Asset", { exact: true }).selectOption(originalAsset.id);
  await form.getByLabel("Format", { exact: true }).selectOption("sarif");
  await form.getByLabel("Report file", { exact: true }).setInputFiles({
    name: "synthetic-review.sarif", mimeType: "application/json", buffer: Buffer.from(report, "utf8"),
  });
  for (const [label, value] of Object.entries({
    "Source ID": sourceId, "Scan ID": "synthetic-review-replay", "Scope ID": scope.id,
    "Scope revision": scope.revision, Branch: scope.branch, "Source scan time": sourceScanAt,
  })) await form.getByLabel(label, { exact: true }).fill(value);
  await form.getByLabel("Source status", { exact: true }).selectOption("succeeded");
  await form.getByLabel("Scan kind", { exact: true }).selectOption("full");
  await form.getByLabel("Completeness", { exact: true }).selectOption("complete");
  await form.getByRole("button", { name: "Import report", exact: true }).click();
  await expect(form).toHaveCount(0);
}

function receiptStatus(card: Locator) {
  return card.getByRole("status", { name: "Import status", exact: true, includeHidden: true })
    .or(card.getByRole("alert", { name: "Import status", exact: true, includeHidden: true }));
}

async function absentReceipt(card: Locator, phase: string) {
  expect.soft(await card.getByText("synthetic-import-1", { exact: true }).count(), `${phase}: old receipt ID must remain absent.`).toBe(0);
  expect.soft(await receiptStatus(card).count(), `${phase}: old receipt state must remain absent.`).toBe(0);
  expect.soft(await card.getByText(/^1 observations in this run\./).count(), `${phase}: old observation count must remain absent.`).toBe(0);
}

async function renderBoundary(page: Page) {
  await page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => resolve())));
}

for (const denied of [403, 404] as const) {
  test(`receipt ${denied} stays hidden through held retry and 503 until authorized recovery`, async ({ page, app }) => {
    await page.goto("/#/assets");
    await submitReport(page);
    const card = page.getByRole("region", { name: "Latest report import", exact: true });
    const refresh = card.getByRole("button", { name: "Refresh import status", exact: true });
    app.setImportState("synthetic-import-1", "succeeded");
    await refresh.click();
    await expect(receiptStatus(card)).toContainText("1 observations in this run.");
    const loss = app.queueImportStatus("synthetic-import-1", { status: denied });
    await refresh.click();
    await loss.delivered;
    await expect(card.getByRole("alert")).toContainText(denied === 403 ? "access denied" : "not found");
    await absentReceipt(card, `after ${denied}`);
    const retry = app.queueImportStatus("synthetic-import-1", { status: 503 }, true);
    await refresh.click();
    await retry.requested;
    await expect(refresh).toBeDisabled();
    await absentReceipt(card, "while retry response is held");
    retry.release();
    await retry.delivered;
    await expect(card.getByRole("alert")).toContainText("service unavailable");
    await absentReceipt(card, "after retry 503");
    const recovered = app.queueImportStatus("synthetic-import-1", { status: 200, state: "succeeded" });
    await refresh.click();
    await recovered.delivered;
    await expect(receiptStatus(card)).toContainText("1 observations in this run.");
    await expect(card.getByText("synthetic-import-1", { exact: true })).toBeVisible();
    expect(app.calls("GET", "/api/v1/imports/synthetic-import-1")).toHaveLength(4);
  });
}

test("same-ID replay acknowledgement supersedes the old receipt and held status response", async ({ page, app }) => {
  await page.goto("/#/assets");
  await submitReport(page);
  const card = page.getByRole("region", { name: "Latest report import", exact: true });
  await expect(receiptStatus(card)).toContainText("Queued");
  const older = app.queueImportStatus("synthetic-import-1", { status: 200, state: "queued" }, true);
  await card.getByRole("button", { name: "Refresh import status", exact: true }).click();
  await older.requested;
  app.queueReplayReceipt("synthetic-import-1", "succeeded");
  await submitReport(page);
  expect(app.calls("POST", "/api/v1/imports")).toHaveLength(2);
  expect(app.imports.size).toBe(1);
  await expect.soft(receiptStatus(card), "The newest same-ID acknowledgement must render before an older GET is released.")
    .toContainText("Succeeded", { timeout: 1000 });
  await expect.soft(receiptStatus(card)).toContainText("1 observations in this run.", { timeout: 1000 });
  older.release();
  await older.delivered;
  await renderBoundary(page);
  await expect.soft(receiptStatus(card), "An older status response must not revert the acknowledged replay.")
    .toContainText("Succeeded", { timeout: 1000 });
  await expect.soft(receiptStatus(card)).toContainText("1 observations in this run.", { timeout: 1000 });
  expect(app.calls("GET", "/api/v1/imports/synthetic-import-1")).toHaveLength(1);
});

async function tabTo(page: Page, target: Locator, key = "Tab") {
  for (let i = 0; i < 32; i += 1) {
    if (await target.evaluate((element) => element === document.activeElement)) return;
    await page.keyboard.press(key);
  }
  await expect(target, "The real keyboard must reach the chosen control.").toBeFocused();
}

async function meaningfulAssetFocus(inventory: Locator) {
  return inventory.evaluate((region) => {
    const focused = document.activeElement;
    if (!(focused instanceof HTMLElement) || focused === document.body || focused === document.documentElement ||
      focused.matches(":disabled, [aria-disabled='true']") || focused.closest("[inert]")) return false;
    const main = region.closest("main");
    const contextual = region.contains(focused) || focused === main ||
      (focused.getAttribute("role") === "status" && main?.contains(focused));
    const rect = focused.getBoundingClientRect(), style = getComputedStyle(focused);
    return Boolean(contextual && focused.isConnected && style.visibility !== "hidden" && style.display !== "none" &&
      rect.width > 0 && rect.height > 0 && rect.bottom > 0 && rect.top < innerHeight && rect.right > 0 && rect.left < innerWidth);
  });
}

async function scrollContext(page: Page, row: Locator) {
  const window = await page.evaluate(() => ({ x: scrollX, y: scrollY }));
  const top = await row.evaluate((element) => element.getBoundingClientRect().top);
  const inner = await page.getByRole("region", { name: "Asset results", exact: true })
    .evaluate((element) => ({ x: element.scrollLeft, y: element.scrollTop }));
  return { window, top, inner };
}

function sameScrollContext(before: Awaited<ReturnType<typeof scrollContext>>, after: Awaited<ReturnType<typeof scrollContext>>, label: string) {
  expect.soft(Math.abs(after.window.x - before.window.x), `${label}: window horizontal context`).toBeLessThanOrEqual(1);
  expect.soft(after.inner, `${label}: inventory scroll offsets`).toEqual(before.inner);
  // Retain either absolute offset or the edited-row anchor when feedback reflows.
  expect.soft(Math.abs(after.window.y - before.window.y) <= 1 || Math.abs(after.top - before.top) <= 1,
    `${label}: no scroll reset or loss of the edited-row anchor`).toBe(true);
}

for (const reducedMotion of ["no-preference", "reduce"] as const) {
  for (const moveFocus of [false, true]) {
    test(`keyboard asset Save retains context/focus (${reducedMotion}, user-moves-focus=${moveFocus})`, async ({ page, app }) => {
      app.assets.push(...Array.from({ length: 48 }, (_, index) => ({
        ...originalAsset, id: `synthetic-focus-asset-${index}`, name: `Synthetic inventory row ${index}`,
      })));
      app.assets.splice(25, 0, { ...createdAsset });
      await page.emulateMedia({ reducedMotion });
      await page.goto("/#/assets");
      const workspace = page.getByRole("combobox", { name: "Workspace", exact: true });
      const inventory = page.getByRole("region", { name: "Asset inventory", exact: true });
      const table = page.getByRole("table", { name: "Assets", exact: true });
      const original = table.getByRole("row").filter({ hasText: createdAsset.name });
      const edit = original.getByRole("button", { name: "Edit asset", exact: true });
      await edit.scrollIntoViewIfNeeded();
      await edit.focus();
      const before = await scrollContext(page, original);
      const initialAssetReads = app.calls("GET", "/api/v1/assets").length;
      expect(before.window.y + before.inner.y).toBeGreaterThan(100);
      await page.keyboard.press("Enter");
      const form = page.getByRole("form", { name: "Edit asset", exact: true });
      await form.getByLabel("Asset name", { exact: true }).fill(updatedAsset.name);
      await form.getByLabel("Environment", { exact: true }).fill("staging");
      const save = form.getByRole("button", { name: "Save asset", exact: true });
      await tabTo(page, save);
      const refresh = app.holdAssets(alpha.id);
      await page.keyboard.press("Enter");
      await refresh.requested;
      await expect(form).toHaveCount(0);
      await expect(page.getByRole("status").filter({ hasText: "Refreshing inventory." })).toBeVisible();
      expect.soft(await meaningfulAssetFocus(inventory), "Held refresh must not leave focus on BODY, disabled or offscreen content.").toBe(true);
      await expect(workspace).toHaveValue(alpha.id);
      await expect(page).toHaveURL(/#\/assets$/);
      sameScrollContext(before, await scrollContext(page, original), "held refresh");
      if (moveFocus) {
        await tabTo(page, workspace, "Shift+Tab");
        await expect(workspace).toBeInViewport();
      }
      const whileHeld = await scrollContext(page, original);
      refresh.release();
      await refresh.delivered;
      const updated = table.getByRole("row").filter({ hasText: updatedAsset.name });
      await expect(updated).toContainText("staging");
      await expect(page.getByRole("status").filter({ hasText: "Refreshing inventory." })).toHaveCount(0);
      if (moveFocus) await expect.soft(workspace, "Refresh completion must not steal deliberately moved keyboard focus.").toBeFocused();
      else expect.soft(await meaningfulAssetFocus(inventory), "Updated inventory must retain meaningful visible focus.").toBe(true);
      sameScrollContext(whileHeld, await scrollContext(page, updated), "completed refresh");
      await expect(workspace).toHaveValue(alpha.id);
      expect(app.calls("PATCH", `/api/v1/assets/${createdAsset.id}`)).toHaveLength(1);
      const assetReads = app.calls("GET", "/api/v1/assets");
      expect(assetReads.length).toBeGreaterThan(initialAssetReads);
      expect(assetReads.every((call) => call.workspace === alpha.id)).toBe(true);
    });
  }
}
