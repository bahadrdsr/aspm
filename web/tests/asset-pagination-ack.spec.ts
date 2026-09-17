import type { Page } from "@playwright/test";
import { requireProductionUI } from "./network";
import {
  apiVersion, assetsPath, createFields, createdID, editedName, firstAlpha, pagingAlpha, validAsset,
} from "./asset-pagination-data";
import type { AssetPage, PagingAsset } from "./asset-pagination-data";
import { expect, test } from "./asset-pagination-fixture";
import type { AssetPagingAPI, PagingCall, PagingControl } from "./asset-pagination-fixture";

test.use({ reducedMotion: "reduce", timezoneId: "UTC" });
test.beforeEach(async ({ paging }) => {
  requireProductionUI();
  expect(paging.requests).toEqual([]);
});

function inventory(page: Page) { return page.getByRole("region", { name: "Asset inventory", exact: true, includeHidden: true }); }
function table(page: Page) { return inventory(page).getByRole("table", { name: "Assets", exact: true, includeHidden: true }); }
function rows(page: Page) { return table(page).getByRole("row", { includeHidden: true }).filter({ has: page.getByRole("cell", { includeHidden: true }) }); }
function row(page: Page, name: string) { return rows(page).filter({ hasText: name }); }
function more(page: Page) { return inventory(page).getByRole("button", { name: /^(?:Load more assets|Next assets|Next(?: page)?)$/i }); }
function importAction(page: Page) { return page.getByRole("main").getByRole("button", { name: "Import report", exact: true }); }
function importForm(page: Page) { return page.getByRole("form", { name: "Import report", exact: true }); }
async function requested(control: PagingControl) {
  await expect.poll(() => control.call !== null, "The real UI must request the specifically held HTTP operation.").toBe(true);
  return control.call!;
}
function pageFrom(call: PagingCall): AssetPage {
  expect(call.status).toBe(200);
  const value = call.response as unknown as AssetPage;
  expect(value.apiVersion).toBe(apiVersion);
  expect(Array.isArray(value.items)).toBe(true);
  return value;
}
async function exactRows(page: Page, assets: PagingAsset[]) {
  await expect.poll(async () => {
    const text = await rows(page).allTextContents();
    return text.length === assets.length &&
      assets.every((asset) => text.filter((row) => row.includes(asset.name)).length === 1);
  }, "Every confirmed initial/later row must remain, with the acknowledged record merged by ID exactly once.").toBe(true);
}
async function exactOptions(page: Page, assets: PagingAsset[]) {
  const selector = importForm(page).getByRole("combobox", { name: "Asset", exact: true });
  const expected = assets.map((asset) => asset.id).sort();
  await expect.poll(() => selector.locator("option").evaluateAll((items) =>
    items.map((item) => (item as HTMLOptionElement).value).filter(Boolean).sort()),
  "The native Import selector must use the same complete confirmed loaded set, with no stale/missing/duplicate IDs.").toEqual(expected);
  return selector;
}
async function initialAndLater(page: Page, paging: AssetPagingAPI) {
  await page.goto("/#/assets");
  await expect(row(page, firstAlpha.name)).toHaveCount(1);
  const first = paging.pages.filter((entry) => !entry.call.query.cursor).at(-1)!.page;
  expect(first.total).toBeGreaterThan(500);
  expect(first.nextCursor).not.toBeNull();
  const continuation = paging.queuePage(first.nextCursor!, 200, true);
  await more(page).click();
  const call = await requested(continuation);
  expect(call).toMatchObject({ workspace: pagingAlpha.id, query: { cursor: first.nextCursor } });
  continuation.release(); await continuation.delivered;
  const later = pageFrom(call);
  const loaded = [...new Map([...first.items, ...later.items].map((asset) => [asset.id, asset])).values()];
  await exactRows(page, loaded);
  expect(later.nextCursor).not.toBeNull();
  return { first, later, loaded };
}

for (const variant of [
  { kind: "edit", refreshStatus: 200 },
  { kind: "create", refreshStatus: 503 },
] as const) {
  test(`AP6 ${variant.kind}: canonical ACK is visible before its NEW post-save GET settles (${variant.refreshStatus})`, async ({ page, paging }, testInfo) => {
    const { later, loaded } = await initialAndLater(page, paging);
    const initialReads = paging.calls("GET", assetsPath).length;
    let mutation: PagingControl;
    let method: "PATCH" | "POST";
    let mutationPath: string;
    let expectedBody: Record<string, unknown>;
    let expectedAsset: PagingAsset;

    if (variant.kind === "edit") {
      await row(page, firstAlpha.name).getByRole("button", { name: "Edit asset", exact: true }).click();
      const editor = page.getByRole("form", { name: "Edit asset", exact: true });
      await editor.getByLabel("Asset name", { exact: true }).fill(editedName);
      mutation = paging.queuePatch(firstAlpha.id, true);
      method = "PATCH"; mutationPath = `${assetsPath}/${firstAlpha.id}`;
      expectedBody = { name: editedName };
      expectedAsset = { ...firstAlpha, name: editedName };
      await editor.getByRole("button", { name: "Save asset", exact: true }).click();
    } else {
      await page.getByRole("main").getByRole("button", { name: "Create asset", exact: true }).click();
      const editor = page.getByRole("form", { name: "Create asset", exact: true });
      await editor.getByLabel("Asset name", { exact: true }).fill(createFields.name);
      await editor.getByLabel("Environment", { exact: true }).fill(createFields.environment);
      await editor.getByLabel("Criticality", { exact: true }).selectOption(createFields.criticality);
      await editor.getByLabel("Tags", { exact: true }).fill(createFields.tags.join(", "));
      mutation = paging.queueCreate(true);
      method = "POST"; mutationPath = assetsPath; expectedBody = createFields;
      expectedAsset = { id: createdID, workspaceId: pagingAlpha.id, ...createFields };
      await editor.getByRole("button", { name: "Create asset", exact: true }).click();
    }

    const write = await requested(mutation);
    expect(write).toMatchObject({ method, path: mutationPath, workspace: pagingAlpha.id, query: {} });
    expect(write.body).toEqual(expectedBody);
    expect(write.status).toBe(variant.kind === "edit" ? 200 : 201);
    expect(write.response).toEqual({ apiVersion, asset: expectedAsset });
    expect(validAsset(expectedAsset)).toBe(true);
    await exactRows(page, loaded);
    await expect(row(page, expectedAsset.name), "An unacknowledged mutation must not paint optimistic inventory data.").toHaveCount(0);
    expect(paging.calls("GET", assetsPath)).toHaveLength(initialReads);

    // Install the read gate only after the mutation is held; it cannot be the older GET covered by AP4.
    const postSaveRead = paging.queuePage("", variant.refreshStatus, true);
    let postSaveSettled = false;
    const observedRead = postSaveRead.delivered.then(() => { postSaveSettled = true; });
    expect(postSaveRead.call).toBeNull();
    mutation.release(); await mutation.delivered;
    const postSave = await requested(postSaveRead);
    expect(postSave).toMatchObject({ method: "GET", path: assetsPath, workspace: pagingAlpha.id, query: {} });
    expect(paging.requests.indexOf(postSave)).toBeGreaterThan(paging.requests.indexOf(write));
    expect(paging.calls("GET", assetsPath)).toHaveLength(initialReads + 1);
    await expect(page.getByRole("dialog", { name: /^(?:Create|Edit) asset$/, exact: true })).toHaveCount(0);
    await expect(page.getByRole("status").filter({ hasText: `${expectedAsset.name} was saved by the service.` })).toBeVisible();
    await expect(inventory(page).getByRole("status", { includeHidden: true }).filter({ hasText: /Refreshing inventory/i })).toHaveCount(1);
    expect(postSaveSettled, "The NEW post-save GET remains held while checking the validated mutation ACK.").toBe(false);

    const acknowledged = new Map(loaded.map((asset) => [asset.id, asset]));
    acknowledged.set(expectedAsset.id, expectedAsset);
    const canonical = [...acknowledged.values()];
    await testInfo.attach("ack-before-new-get-release", {
      body: JSON.stringify({
        phase: "validated mutation ACK delivered; NEW post-save first-page GET requested but not released",
        kind: variant.kind, plannedReadStatus: variant.refreshStatus, postSaveSettled,
        mutation: { method: write.method, path: write.path, status: write.status, body: write.body, response: write.response },
        nextRead: { path: postSave.path, query: postSave.query, status: postSave.status },
        expectedCanonicalAsset: expectedAsset, expectedLoadedRows: canonical.length,
        observedCanonicalRows: await row(page, expectedAsset.name).count(), observedLoadedRows: await rows(page).count(),
        observedOldEditedRows: variant.kind === "edit" ? await row(page, firstAlpha.name).count() : null,
      }, null, 2), contentType: "application/json",
    });
    await testInfo.attach("inventory-with-new-get-held", { body: await page.screenshot(), contentType: "image/png" });
    await expect(row(page, expectedAsset.name),
      "The server-confirmed canonical row must be visible immediately after ACK, BEFORE releasing the NEW post-save GET.").toHaveCount(1);
    await exactRows(page, canonical);
    if (variant.kind === "edit") await expect(row(page, firstAlpha.name)).toHaveCount(0);
    expect(postSaveSettled).toBe(false);

    await expect(importAction(page)).toBeEnabled();
    await importAction(page).click();
    const selector = await exactOptions(page, canonical);
    await expect(selector.locator(`option[value="${expectedAsset.id}"]`)).toHaveText(expectedAsset.name);
    await selector.selectOption(expectedAsset.id);
    await expect(selector).toHaveValue(expectedAsset.id);
    expect(postSaveSettled).toBe(false);
    expect(paging.calls("GET", assetsPath)).toHaveLength(initialReads + 1);

    postSaveRead.release(); await observedRead;
    if (variant.refreshStatus === 503) {
      await expect(importForm(page).getByRole("alert")).toContainText(/could not|unavailable|unable/i);
    }
    await exactRows(page, canonical);
    await exactOptions(page, canonical);
    await expect(selector).toHaveValue(expectedAsset.id);
    await expect(selector.locator(`option[value="${expectedAsset.id}"]`)).toHaveText(expectedAsset.name);
    await page.keyboard.press("Escape");
    await expect(importForm(page)).toHaveCount(0);
    await expect(importAction(page)).toBeFocused();

    const nextPage = paging.queuePage(later.nextCursor!, 200, true);
    await more(page).click();
    const nextRead = await requested(nextPage);
    expect(nextRead.query.cursor, "A post-save first-page refresh must not rewind the already reached continuation frontier.").toBe(later.nextCursor);
    nextPage.release(); await nextPage.delivered;
    const expectedAfterNext = new Map(canonical.map((asset) => [asset.id, asset]));
    for (const asset of pageFrom(nextRead).items) expectedAfterNext.set(asset.id, asset);
    await exactRows(page, [...expectedAfterNext.values()]);
    await expect(row(page, expectedAsset.name)).toHaveCount(1);
    expect(paging.calls(method, mutationPath)).toHaveLength(1);
    expect(paging.requests.filter((call) => call.method !== "GET")).toHaveLength(1);
    await expect(page.getByRole("combobox", { name: "Workspace", exact: true })).toHaveValue(pagingAlpha.id);
    await expect(page).toHaveURL(/#\/assets$/);
    await paging.assertPrivate(page, true);
  });
}
