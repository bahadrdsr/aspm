import type { Locator, Page } from "@playwright/test";
import { requireProductionUI } from "./network";
import { expect, test } from "./saved-work-views-fixture";
import type { SavedViewsAPI, WorkControl, WorkEntry } from "./saved-work-views-fixture";
import {
  alpha, alphaTotal, beta, betaTotal, currentOwner, cursor200, findingAt, first, password, pathFor,
  queries, searchTotal, user, workPath,
} from "./work-search-data";
import {
  apiVersion, currentView, emptyView, firstViews, maxViewName, maxViewQuery, primaryView, tailViews,
  viewAt, viewCursor100, viewDetail, viewPage, viewPath, viewSorts,
} from "./saved-work-views-data";
import type { SavedView } from "./saved-work-views-data";

test.use({ reducedMotion: "reduce", timezoneId: "UTC" });
test.beforeEach(async ({ viewsAPI }) => { requireProductionUI(); expect(viewsAPI.requests).toEqual([]); });
const main = (page: Page) => page.getByRole("main", { includeHidden: true });
const queue = (page: Page) => main(page).getByRole("region", { name: "Finding work queue", exact: true, includeHidden: true });
const table = (page: Page) => queue(page).getByRole("table", { name: "Findings", exact: true, includeHidden: true });
const rows = (page: Page) => table(page).locator("tbody tr");
const findingRow = (page: Page, finding = first) => rows(page).filter({ hasText: finding.title });
const findingButton = (page: Page, finding = first) => findingRow(page, finding).getByRole("button", { name: finding.title, exact: true });
const selectFinding = (page: Page, finding = first) => findingRow(page, finding).getByRole("checkbox", { name: `Select ${finding.title}`, exact: true });
const filter = (page: Page) => queue(page).getByRole("textbox", { name: "Filter findings", exact: true });
const search = (page: Page) => queue(page).getByRole("button", { name: "Search all findings", exact: true });
const clear = (page: Page) => queue(page).getByRole("button", { name: "Clear search", exact: true });
const moreFindings = (page: Page) => queue(page).getByRole("button", { name: "Load more findings", exact: true });
const searchStatus = (page: Page) => queue(page).getByRole("status", { name: "Workspace search", exact: true, includeHidden: true });
const pagination = (page: Page) => queue(page).getByRole("status", { name: "Finding pagination", exact: true, includeHidden: true });
const selection = (page: Page) => queue(page).getByRole("status", { includeHidden: true }).filter({ hasText: /selected.*on this page/i });
const navigation = (page: Page) => page.getByRole("navigation", { name: "Primary", exact: true });
const toggle = (page: Page) => main(page).getByRole("button", { name: "Saved views", exact: true });
const panel = (page: Page) => main(page).getByRole("region", { name: "Saved views", exact: true, includeHidden: true });
const viewList = (page: Page) => panel(page).getByRole("list", { name: "Personal saved views", exact: true, includeHidden: true });
const viewRows = (page: Page) => viewList(page).getByRole("listitem", { includeHidden: true });
const viewRow = (page: Page, view = primaryView) => viewRows(page).filter({ has: page.getByText(view.name, { exact: true }) });
const viewAction = (page: Page, action: "Apply" | "Edit" | "Delete", view = primaryView) =>
  viewRow(page, view).getByRole("button", { name: action, exact: true });
const refreshViews = (page: Page) => panel(page).getByRole("button", { name: "Refresh saved views", exact: true });
const moreViews = (page: Page) => panel(page).getByRole("button", { name: "Load more saved views", exact: true });
const retryViews = (page: Page) => panel(page).getByRole("button", { name: "Retry saved views", exact: true });
const retryView = (page: Page) => panel(page).getByRole("button", { name: "Retry saved view", exact: true });
const reloadView = (page: Page) => panel(page).getByRole("button", { name: "Reload saved view", exact: true });
const saveCurrent = (page: Page) => panel(page).getByRole("button", { name: "Save current view", exact: true });
const saveForm = (page: Page) => panel(page).getByRole("form", { name: "Save current view", exact: true, includeHidden: true });
const editForm = (page: Page) => panel(page).getByRole("form", { name: "Edit saved view", exact: true, includeHidden: true });
const deleteForm = (page: Page) => panel(page).getByRole("form", { name: "Delete saved view", exact: true, includeHidden: true });
const nameInput = (form: Locator) => form.getByRole("textbox", { name: "View name", exact: true });
const queryInput = (form: Locator) => form.getByRole("textbox", { name: "View query", exact: true });
const sortInput = (form: Locator) => form.getByRole("combobox", { name: "Loaded sort", exact: true });
const saveChanges = (page: Page) => editForm(page).getByRole("button", { name: "Save changes", exact: true });
const saveView = (page: Page) => saveForm(page).getByRole("button", { name: "Save view", exact: true });
const emptyViews = (page: Page) => panel(page).getByRole("heading", { name: "No saved views", exact: true, includeHidden: true });

async function frames(page: Page) {
  await page.evaluate(() => new Promise<void>((done) => requestAnimationFrame(() => requestAnimationFrame(() => done()))));
}
async function arrived(control: WorkControl) {
  await expect.poll(() => control.call !== null, "The declared intent must reach actual HTTP.").toBe(true);
  return control.call!;
}
async function release(page: Page, control: WorkControl) {
  control.release(); await control.delivered; await frames(page);
}
async function settled(control: WorkControl) {
  const call = await arrived(control);
  await expect.poll(() => call.finished || call.failure !== null).toBe(true);
  if (call.failure !== null) expect(call.failure).toMatch(/abort/i);
  else expect(call).toMatchObject({ responseStatus: call.status, responseBeforeFailure: true, finished: true });
}
async function noIO(page: Page, api: SavedViewsAPI, action: () => Promise<unknown>) {
  const before = api.requests.length;
  await action(); await frames(page);
  expect(api.requests, "Local interaction cannot fetch metadata or issue another intent.").toHaveLength(before);
}
async function draft(page: Page, api: SavedViewsAPI, value: string) {
  api.remember(value);
  await noIO(page, api, () => filter(page).fill(value));
}
async function active(page: Page, query: string) {
  if (query === "") await expect(searchStatus(page)).toContainText("No server search");
  else {
    await expect(searchStatus(page)).toContainText(query);
    await expect(searchStatus(page)).toContainText(/confirmed.*workspace.*server|confirmed.*server.*workspace/i);
  }
}
async function counts(page: Page, loaded: number, total: number) {
  await expect(pagination(page)).toContainText(`${loaded} loaded findings`);
  await expect(pagination(page)).toContainText(`${total} total`);
}
async function finishWorkEntry(page: Page, api: SavedViewsAPI, entry: WorkEntry) {
  await expect(rows(page)).toHaveCount(50); await frames(page);
  await expect.poll(() => entry.calls.length > 0 && entry.calls.every((call) => call.finished || call.failure !== null)).toBe(true);
  expect(api.finishEntry(entry, entry.workspace).query).toEqual({});
}
async function boot(page: Page, api: SavedViewsAPI) {
  const entry = api.entry();
  await page.goto("/#/work"); await finishWorkEntry(page, api, entry);
  await counts(page, 100, alphaTotal); await active(page, "");
  await expect(filter(page)).toHaveValue(""); await expect(navigation(page).getByRole("link")).toHaveCount(5);
  expect(api.viewCalls).toHaveLength(0); expect(api.calls()).toHaveLength(entry.calls.length);
  api.mark("Reached real App: authorized bounded native Work entry, 100 loaded/207 reported, 50 display rows, five nav links, no saved-view HTTP.");
  await expect(toggle(page), "Published Work is missing the closed-by-default Saved views toggle.").toBeVisible();
  await expect(toggle(page)).toHaveCount(1); await expect(toggle(page)).toHaveAttribute("aria-expanded", "false");
  await expect(panel(page)).toHaveCount(0);
  api.mark("Saved views toggle reached. Remaining saved-view narrative is now executable.");
}
async function openViews(page: Page, api: SavedViewsAPI, items: SavedView[] = [primaryView, emptyView],
  options: { total?: number; cursor?: string | null; workspace?: string; keyboard?: boolean } = {}) {
  const workBefore = api.calls().length, viewsBefore = api.viewCalls.length;
  const entry = api.viewsEntry(viewPage(items, options.total ?? items.length, options.cursor ?? null), options.workspace ?? alpha.id);
  if (options.keyboard) { await toggle(page).focus(); await toggle(page).press("Space"); }
  else await toggle(page).click();
  await expect(panel(page)).toBeVisible(); await expect(toggle(page)).toHaveAttribute("aria-expanded", "true");
  await expect(toggle(page)).toHaveAttribute("aria-controls", await panel(page).getAttribute("id") ?? "missing-region-id");
  await expect.poll(() => entry.calls.length > 0).toBe(true);
  await expect(panel(page).getByRole("status").filter({ hasText: /loading/i })).toBeVisible();
  await expect(viewRows(page)).toHaveCount(0); await expect(emptyViews(page)).toHaveCount(0);
  for (const response of entry.responses) response.release();
  await expect(viewRows(page)).toHaveCount(items.length); await frames(page);
  await expect.poll(() => entry.calls.every((call) => call.finished || call.failure !== null)).toBe(true);
  api.finishViewsEntry(entry);
  expect(api.calls()).toHaveLength(workBefore);
  expect(api.viewCalls.slice(viewsBefore), "Opening may read the list only, never individual views.").toEqual(entry.calls);
  await expect(panel(page)).toContainText(/personal/i);
  await expect(panel(page)).toContainText(/query snapshot/i);
  await expect(panel(page).getByRole("button", { name: /share|team|admin override|download|export/i })).toHaveCount(0);
  await expect(main(page).getByRole("button", { name: /download.*csv|export.*csv/i })).toHaveCount(0);
  return entry;
}
async function manualSearch(page: Page, api: SavedViewsAPI, query: string, status: 200 | 403 | 503 = 200) {
  const response = query === "" ? api.page("", status) : api.search(query, "", status);
  await draft(page, api, query); await search(page).click();
  expect((await arrived(response)).query).toEqual(query === "" ? {} : { q: query });
  return response;
}
async function confirmWork(page: Page, response: WorkControl, query: string) {
  await release(page, response); await active(page, query);
  await expect.poll(() => response.call?.finished).toBe(true);
  expect(response.call).toMatchObject({ responseStatus: 200, responseBeforeFailure: true, failure: null });
}
async function applyView(page: Page, api: SavedViewsAPI, listed: SavedView, canonical: SavedView,
  status: 200 | 503 = 200) {
  const detail = api.detailView(listed.id, viewDetail(canonical));
  const work = canonical.query === "" ? api.page("", status) : api.search(canonical.query, "", status);
  const before = api.calls().length;
  await viewAction(page, "Apply", listed).click();
  expect(await arrived(detail)).toMatchObject({ method: "GET", path: viewPath(listed.id), query: {}, body: {} });
  expect(work.call).toBeNull(); expect(api.calls()).toHaveLength(before);
  await release(page, detail);
  const call = await arrived(work);
  expect(call).toMatchObject({ method: "GET", path: workPath, workspace: alpha.id, body: {} });
  expect(call.query).toEqual(canonical.query === "" ? {} : { q: canonical.query });
  expect(api.calls()).toHaveLength(before + 1);
  return work;
}
async function startSave(page: Page, api: SavedViewsAPI, name: string, query: string, sort: SavedView["sort"]) {
  api.remember(name, query);
  await noIO(page, api, () => saveCurrent(page).click());
  await expect(saveForm(page)).toBeVisible(); await expect(nameInput(saveForm(page))).toHaveValue("");
  await expect(saveForm(page).getByRole("textbox")).toHaveCount(1);
  const snapshot = saveForm(page).getByRole("region", { name: "View snapshot", exact: true });
  await expect(snapshot.getByText(query === "" ? "All findings (empty query)" : query, { exact: true })).toBeVisible();
  await expect(snapshot).toContainText(sort);
  await noIO(page, api, () => nameInput(saveForm(page)).fill(name));
}
async function editView(page: Page, api: SavedViewsAPI, listed: SavedView, canonical = listed) {
  const detail = api.detailView(listed.id, viewDetail(canonical));
  await viewAction(page, "Edit", listed).click(); await arrived(detail);
  await expect(editForm(page)).toHaveCount(0);
  await release(page, detail); await expect(editForm(page)).toBeVisible();
  await expect(nameInput(editForm(page))).toHaveValue(canonical.name);
  await expect(queryInput(editForm(page))).toHaveValue(canonical.query);
  await expect(sortInput(editForm(page))).toHaveValue(canonical.sort);
}
async function deleteView(page: Page, api: SavedViewsAPI, view: SavedView) {
  const before = api.viewCalls.filter((call) => call.method === "DELETE").length;
  const detail = api.detailView(view.id, viewDetail(view));
  await viewAction(page, "Delete", view).click(); await arrived(detail); await release(page, detail);
  await expect(deleteForm(page)).toBeVisible(); await expect(deleteForm(page)).toContainText(view.name);
  expect(api.viewCalls.filter((call) => call.method === "DELETE")).toHaveLength(before);
}
async function cancel(page: Page, api: SavedViewsAPI, form: Locator) {
  await noIO(page, api, () => form.getByRole("button", { name: "Cancel", exact: true }).click());
  await expect(form).toHaveCount(0);
}
async function listRead(page: Page, api: SavedViewsAPI, payload: unknown, status: 200 | 403 | 503 = 200, retry = false) {
  const response = api.listViews(payload, { status });
  await (retry ? retryViews(page) : refreshViews(page)).click();
  expect((await arrived(response)).query).toEqual({});
  return response;
}
async function withheldViews(page: Page) {
  await expect(viewRows(page)).toHaveCount(0); await expect(emptyViews(page)).toHaveCount(0);
  await expect(editForm(page)).toHaveCount(0); await expect(deleteForm(page)).toHaveCount(0);
}
async function noPrivateDOM(page: Page, ...values: string[]) {
  const visibleAndHidden = await page.evaluate(() => document.body.textContent + JSON.stringify(
    [...document.querySelectorAll("input, textarea, select")].map((field) => (field as HTMLInputElement).value)));
  for (const value of values) expect(visibleAndHidden).not.toContain(value);
}

test("SV1 Closed lazy personal list uses native manual pages and saves confirmed query before snapshot apply", async ({ page, viewsAPI: api }) => {
  await boot(page, api);
  await draft(page, api, "initial");
  await noIO(page, api, () => table(page).getByRole("button", { name: "Severity", exact: true }).click());
  await navigation(page).getByRole("link", { name: "Settings", exact: true }).click();
  await expect(main(page).getByRole("heading", { name: "Settings", exact: true })).toBeVisible();
  expect(api.viewCalls).toHaveLength(0);
  const reentry = api.entry();
  await navigation(page).getByRole("link", { name: "Work", exact: true }).click();
  await finishWorkEntry(page, api, reentry); expect(api.viewCalls).toHaveLength(0);
  await expect(toggle(page)).toHaveAttribute("aria-expanded", "false"); await expect(filter(page)).toHaveValue("initial");
  await confirmWork(page, await manualSearch(page, api, queries.initial), queries.initial);
  await draft(page, api, "repository");
  const listedViews = firstViews.map((view, index) => index === 3 ? { ...view, name: firstViews[2].name } : view);
  await openViews(page, api, listedViews, { total: 103, cursor: viewCursor100 });
  await expect(viewRow(page, listedViews[2])).toHaveCount(2);
  await expect(filter(page)).toHaveValue("repository");
  await expect(table(page).getByRole("columnheader", { name: "Severity", exact: true })).toHaveAttribute("aria-sort", "ascending");
  await active(page, queries.initial);
  api.mark("Closed startup/navigation/local filtering issued no view HTTP; opening loaded list only and preserved Work query/draft/sort.");

  const failure = await listRead(page, api, undefined, 503);
  await release(page, failure); await expect(panel(page).getByRole("alert")).toBeVisible();
  await expect(viewRows(page)).toHaveCount(100); await expect(emptyViews(page)).toHaveCount(0);
  const recovered = await listRead(page, api, viewPage(listedViews, 103, viewCursor100), 200, true);
  await release(page, recovered); await expect(viewRows(page)).toHaveCount(100);
  const failedTail = api.listViews(undefined, { cursor: viewCursor100, status: 503 });
  await moreViews(page).click(); await arrived(failedTail); await expect(moreViews(page)).toBeDisabled();
  await release(page, failedTail); await expect(panel(page).getByRole("alert")).toBeVisible();
  for (const badPage of [
    viewPage([tailViews[0], tailViews[0]], 103),
    viewPage([tailViews[0]], 103, viewCursor100),
    viewPage([firstViews[99], tailViews[0]], 103),
  ]) {
    const invalid = api.listViews(badPage, { cursor: viewCursor100 });
    await retryViews(page).click();
    expect((await arrived(invalid)).query).toEqual({ limit: "100", cursor: viewCursor100 });
    await release(page, invalid); await expect(panel(page).getByRole("alert")).toBeVisible();
    await expect(viewRows(page)).toHaveCount(100); await expect(viewRow(page, tailViews[0])).toHaveCount(0);
  }
  const tail = api.listViews(viewPage(tailViews, 103), { cursor: viewCursor100 });
  await retryViews(page).click(); expect((await arrived(tail)).query).toEqual({ limit: "100", cursor: viewCursor100 });
  await release(page, tail); await expect(viewRows(page)).toHaveCount(103);
  await expect(viewRow(page, tailViews[2])).toHaveCount(1);
  for (const button of await moreViews(page).all()) await expect(button).toBeDisabled();
  const afterTail = api.requests.length; await frames(page); expect(api.requests).toHaveLength(afterTail);
  api.mark("Manual 100+3 native list, visible transient failure, exact cursor retries, duplicate/nonadvancing rejection and no auto-drain reached.");

  await startSave(page, api, "Confirmed query only", queries.initial, "severity");
  await expect(saveForm(page).getByRole("region", { name: "View snapshot" })).not.toContainText("repository");
  const created: SavedView = { ...viewAt(105), name: "Confirmed query only", query: queries.initial, sort: "severity" };
  const saved = api.createView({ name: created.name, query: queries.initial, sort: "severity" }, viewDetail(created));
  const workBeforeSave = api.calls().length;
  await saveView(page).click(); await arrived(saved); await expect(viewRow(page, created)).toHaveCount(0);
  await expect(saveView(page)).toBeDisabled();
  await release(page, saved); await expect(viewRow(page, created)).toHaveCount(1); await expect(saveForm(page)).toHaveCount(0);
  expect(api.calls()).toHaveLength(workBeforeSave); await expect(filter(page)).toHaveValue("repository");

  await queue(page).getByRole("button", { name: "Next", exact: true }).click();
  await queue(page).getByRole("checkbox", { name: "Select all findings on this page", exact: true }).check();
  const applied = await applyView(page, api, primaryView, currentView);
  await active(page, queries.initial); await expect(selection(page)).toBeVisible();
  await confirmWork(page, applied, queries.asset);
  await expect(filter(page)).toHaveValue(queries.asset); await expect(selection(page)).toHaveCount(0);
  await expect(queue(page)).toContainText("Page 1 of 2"); await counts(page, 100, searchTotal);
  await expect(rows(page).first()).toContainText(findingAt(200).title);
  await expect(table(page).getByRole("columnheader", { name: /^Finding$/ })).toHaveAttribute("aria-sort", "ascending");
  await expect(queue(page)).toContainText(/sort[^.]*loaded|loaded[^.]*sort/i);
  await expect(pagination(page)).toContainText(/not.*atomic.*snapshot/i);
  const findingsTail = api.search(queries.asset, cursor200);
  await moreFindings(page).click();
  expect((await arrived(findingsTail)).query).toEqual({ q: queries.asset, limit: "100", cursor: cursor200 });
  await release(page, findingsTail); await counts(page, 107, searchTotal);
  await expect(rows(page).first()).toContainText(findingAt(207).title);
  const empty = await applyView(page, api, emptyView, emptyView);
  await confirmWork(page, empty, ""); await expect(filter(page)).toHaveValue(""); await counts(page, 100, alphaTotal);
  await expect(table(page).getByRole("columnheader", { name: "Severity", exact: true })).toHaveAttribute("aria-sort", "ascending");
  const duplicateName = api.detailView(listedViews[3].id, viewDetail(listedViews[3])), duplicateSearch = api.search(queries.initial);
  await viewRow(page, listedViews[2]).nth(1).getByRole("button", { name: "Apply", exact: true }).click();
  expect((await arrived(duplicateName)).path).toBe(viewPath(listedViews[3].id));
  await release(page, duplicateName); await arrived(duplicateSearch); await confirmWork(page, duplicateSearch, queries.initial);
  api.mark("Only canonical 201 added a personal ID; Save used confirmed query not draft. Current detail, not old list, drove one q-only apply; empty detail drove bare GET.");
  api.mark("Two identical names remained separate native IDs; the second listed identity, not a name match, drove its own detail and query.");
});

test("SV2 Canonical sparse revisions conflict explicitly and template edits never rewrite an applied snapshot", async ({ page, viewsAPI: api }) => {
  await boot(page, api); await openViews(page, api, [primaryView]);
  await confirmWork(page, await applyView(page, api, primaryView, primaryView), queries.initial);
  await editView(page, api, primaryView, currentView);
  api.remember("Cancelled personal draft", "unconfirmed conflicting query");
  await nameInput(editForm(page)).fill("Cancelled personal draft");
  await cancel(page, api, editForm(page)); expect(api.viewCalls.filter((call) => call.method !== "GET")).toHaveLength(0);
  await editView(page, api, primaryView, currentView);
  const renamed: SavedView = { ...currentView, name: "Renamed personal template", revision: "3", updatedAt: "2026-09-30T12:02:00Z" };
  await nameInput(editForm(page)).fill(renamed.name);
  const wrongPatch = api.patchView(primaryView.id, { revision: "2", name: renamed.name }, viewDetail(renamed), { status: 201 });
  await saveChanges(page).click(); await arrived(wrongPatch); await release(page, wrongPatch);
  await expect(editForm(page).getByRole("alert")).toBeVisible(); await expect(viewRow(page, renamed)).toHaveCount(0);
  await cancel(page, api, editForm(page)); await editView(page, api, primaryView, currentView);
  await nameInput(editForm(page)).fill(renamed.name);
  const rename = api.patchView(primaryView.id, { revision: "2", name: renamed.name }, viewDetail(renamed));
  await saveChanges(page).click(); await arrived(rename);
  await expect(viewRow(page, renamed)).toHaveCount(0); await expect(saveChanges(page)).toBeDisabled();
  await release(page, rename); await expect(viewRow(page, renamed)).toHaveCount(1);
  await active(page, queries.initial); await expect(filter(page)).toHaveValue(queries.initial);
  await expect(table(page).getByRole("columnheader", { name: /^Finding$/ })).toHaveAttribute("aria-sort", "none");

  await editView(page, api, renamed);
  await queryInput(editForm(page)).fill("unconfirmed conflicting query");
  const conflict = api.patchView(renamed.id, { revision: "3", query: "unconfirmed conflicting query" }, undefined, { status: 409 });
  await saveChanges(page).click(); await arrived(conflict); await release(page, conflict);
  await expect(editForm(page).getByRole("alert")).toContainText(/conflict|changed|stale|revision/i);
  const afterConflict = api.requests.length;
  if (await saveChanges(page).isEnabled()) await saveChanges(page).click();
  await frames(page); expect(api.requests).toHaveLength(afterConflict);
  const reloaded: SavedView = { ...renamed, query: queries.owner, sort: "source-order", revision: "4", updatedAt: "2026-09-30T12:03:00Z" };
  const reload = api.detailView(renamed.id, viewDetail(reloaded));
  await reloadView(page).click(); await arrived(reload); await release(page, reload);
  await expect(queryInput(editForm(page))).toHaveValue(queries.owner); await expect(sortInput(editForm(page))).toHaveValue("source-order");
  expect(api.requests).toHaveLength(afterConflict + 1);
  await queryInput(editForm(page)).fill(""); await sortInput(editForm(page)).selectOption("severity");
  const changed: SavedView = { ...reloaded, query: "", sort: "severity", revision: "5", updatedAt: "2026-09-30T12:04:00Z" };
  const patch = api.patchView(changed.id, { revision: "4", query: "", sort: "severity" }, viewDetail(changed));
  await saveChanges(page).click(); await arrived(patch); await release(page, patch);
  await expect(editForm(page)).toHaveCount(0); await active(page, queries.initial); await counts(page, 100, 100);
  await editView(page, api, changed);
  if (await saveChanges(page).isEnabled()) {
    await noIO(page, api, () => saveChanges(page).click()); await expect(editForm(page)).toHaveCount(0);
  } else await cancel(page, api, editForm(page));
  api.mark("Edit used current detail and sparse fields; 409 could not silently retry/rebase, new canonical GET preceded new revision; no-op sent nothing.");

  await deleteView(page, api, changed); await cancel(page, api, deleteForm(page));
  expect(api.viewCalls.filter((call) => call.method === "DELETE")).toHaveLength(0);
  await deleteView(page, api, changed);
  const wrongDelete = api.deleteView(changed.id, "5", { status: 200 });
  await deleteForm(page).getByRole("button", { name: "Confirm delete", exact: true }).click();
  await arrived(wrongDelete); await release(page, wrongDelete);
  await expect(deleteForm(page).getByRole("alert")).toBeVisible(); await expect(viewRow(page, changed)).toHaveCount(1);
  await cancel(page, api, deleteForm(page)); await deleteView(page, api, changed);
  const deleted = api.deleteView(changed.id, "5");
  await deleteForm(page).getByRole("button", { name: "Confirm delete", exact: true }).click();
  expect((await arrived(deleted)).body).toEqual({ revision: "5" }); await expect(viewRow(page, changed)).toHaveCount(1);
  await release(page, deleted); await expect(viewRow(page, changed)).toHaveCount(0);
  await active(page, queries.initial); await expect(filter(page)).toHaveValue(queries.initial); await counts(page, 100, 100);
  api.mark("Inline delete cancelled without write and removed only after 204; edit/rename/delete left the already applied query/loaded sort snapshot unchanged.");

  const uncertain: SavedView = { ...viewAt(106), name: "Uncertain personal create", query: queries.initial };
  await startSave(page, api, uncertain.name, queries.initial, "source-order");
  const lost = api.createView({ name: uncertain.name, query: queries.initial, sort: "source-order" }, undefined, { status: "network" });
  await saveView(page).click(); await arrived(lost); await release(page, lost);
  await expect(panel(page).getByRole("alert")).toContainText(/uncertain|may have (?:reached|been|saved)|could not.*confirm|not.*confirm/i);
  await expect(viewRow(page, uncertain)).toHaveCount(0);
  const postCount = api.viewCalls.filter((call) => call.method === "POST").length;
  await frames(page); expect(api.viewCalls.filter((call) => call.method === "POST")).toHaveLength(postCount);
  await expect(panel(page)).not.toContainText(/created successfully|guaranteed exactly.once|idempotent creation|idempotency guaranteed/i);
  await cancel(page, api, saveForm(page));
  const inspected = await listRead(page, api, viewPage([uncertain]));
  await release(page, inspected); await expect(viewRow(page, uncertain)).toHaveCount(1);
  const another: SavedView = { ...viewAt(107), name: "Another explicit personal intent", query: queries.initial };
  await startSave(page, api, another.name, queries.initial, "source-order");
  const nextIntent = api.createView({ name: another.name, query: queries.initial, sort: "source-order" }, viewDetail(another));
  await saveView(page).click(); await arrived(nextIntent); await release(page, nextIntent);
  await expect(viewRow(page, another)).toHaveCount(1);
  expect(api.viewCalls.filter((call) => call.method === "POST")).toHaveLength(postCount + 1);
  api.mark("Lost POST ACK remained explicitly uncertain with no repeat or invented ID; manual Refresh/inspection preceded a distinct explicit Save.");
});

test("SV3 Apply generations share Work search supersession pagination drafts and canonical finding ACK fences", async ({ page, viewsAPI: api }) => {
  await boot(page, api); await openViews(page, api);
  const obsoleteDetail = api.detailView(primaryView.id, viewDetail(currentView));
  await viewAction(page, "Apply").click(); await arrived(obsoleteDetail);
  await confirmWork(page, await manualSearch(page, api, queries.initial), queries.initial);
  const afterManual = api.calls().length;
  await release(page, obsoleteDetail); await settled(obsoleteDetail);
  expect(api.calls()).toHaveLength(afterManual); await active(page, queries.initial);
  await expect(filter(page)).toHaveValue(queries.initial);
  await queue(page).getByRole("button", { name: "Next", exact: true }).click(); await selectFinding(page, findingAt(51)).check();
  const applied = await applyView(page, api, primaryView, currentView);
  await active(page, queries.initial); await expect(selection(page)).toBeVisible();
  await draft(page, api, "0001");
  await confirmWork(page, applied, queries.asset);
  await expect(filter(page)).toHaveValue("0001"); await expect(rows(page)).toHaveCount(0);
  await expect(selection(page)).toHaveCount(0); await counts(page, 100, searchTotal);
  await draft(page, api, "");
  await expect(queue(page)).toContainText("Page 1 of 2"); await expect(rows(page).first()).toContainText(findingAt(200).title);
  api.mark("Manual search superseded a held view detail without an old query; successful apply preserved a draft typed after activation and reset selection/page.");

  const abandonedApply = await applyView(page, api, emptyView, emptyView);
  await active(page, queries.asset);
  await confirmWork(page, await manualSearch(page, api, queries.asset), queries.asset);
  await draft(page, api, "repository");
  const afterNewSearch = api.calls().length;
  await release(page, abandonedApply); await settled(abandonedApply);
  expect(api.calls()).toHaveLength(afterNewSearch); await active(page, queries.asset); await expect(filter(page)).toHaveValue("repository");
  await expect(table(page).getByRole("columnheader", { name: /^Finding$/ })).toHaveAttribute("aria-sort", "ascending");
  api.mark("Manual search also superseded an already-started apply Work GET; its late bare result and saved severity did not replace the newer query/draft/title sort.");

  const oldTail = api.search(queries.asset, cursor200);
  await moreFindings(page).click(); expect((await arrived(oldTail)).query).toEqual({ q: queries.asset, limit: "100", cursor: cursor200 });
  const beforeClearDetail = api.detailView(emptyView.id, viewDetail(emptyView));
  await viewAction(page, "Apply", emptyView).click(); await arrived(beforeClearDetail);
  const cleared = api.page();
  await clear(page).click(); expect((await arrived(cleared)).query).toEqual({});
  await confirmWork(page, cleared, ""); await counts(page, 100, alphaTotal);
  const afterClear = api.calls().length;
  await release(page, beforeClearDetail); await settled(beforeClearDetail);
  await release(page, oldTail); await settled(oldTail);
  expect(api.calls()).toHaveLength(afterClear); await active(page, ""); await counts(page, 100, alphaTotal);
  await expect(filter(page)).toHaveValue("");
  const older = api.detailView(primaryView.id, viewDetail(currentView));
  await viewAction(page, "Apply").click(); await arrived(older);
  const newer = await applyView(page, api, emptyView, emptyView);
  await confirmWork(page, newer, "");
  const afterNewer = api.calls().length;
  await release(page, older); await settled(older); expect(api.calls()).toHaveLength(afterNewer);
  await expect(table(page).getByRole("columnheader", { name: "Severity", exact: true })).toHaveAttribute("aria-sort", "ascending");
  api.mark("Clear superseded pending detail and query continuation; another Apply won over older detail, without mixed q/viewId or old rows/frontiers.");

  const ownerView: SavedView = { ...currentView, query: queries.owner, sort: "source-order", revision: "3", updatedAt: "2026-09-30T12:02:00Z" };
  const staleRead = await applyView(page, api, primaryView, ownerView);
  await findingButton(page).click();
  const detail = page.getByRole("dialog", { name: first.title, exact: true });
  await expect(detail).toContainText(first.evidence.text);
  const owned = { ...api.findings.get(first.id)!, ownerId: user.id, ownerName: user.name };
  const ack = api.patch({ ownerId: user.id }, owned, true);
  await detail.getByRole("button", { name: "Assign to me", exact: true }).click();
  expect(await arrived(ack)).toMatchObject({ method: "PATCH", path: pathFor(first.id), body: { ownerId: user.id } });
  await release(page, ack); await expect(detail).toContainText(user.name);
  await detail.getByRole("button", { name: "Close finding details", exact: true }).click();
  await confirmWork(page, staleRead, queries.owner); await expect(rows(page)).toHaveCount(0);
  await draft(page, api, ""); await expect(rows(page)).toHaveCount(1); await counts(page, 1, 1);
  await expect(findingRow(page)).toContainText(user.name); await expect(findingRow(page)).not.toContainText(currentOwner.name);
  await expect(searchStatus(page)).toContainText(/membership.*refresh|refresh.*membership/i);
  api.assertData();
  api.mark("Apply reused existing Work ACK fencing: old owner-query row could not overwrite canonical owner; observed membership stayed labeled needs refresh.");
});

test("SV4 Personal viewer CRUD and metadata denials retain independent Work authority without stale private restoration", async ({ page, viewsAPI: api }) => {
  api.roles.set(alpha.id, "viewer"); api.serverRoles.set(alpha.id, "viewer");
  await boot(page, api); await openViews(page, api);
  await expect(queue(page)).toContainText("Read only");
  await findingButton(page).click();
  const finding = page.getByRole("dialog", { name: first.title, exact: true });
  await expect(finding).toContainText(first.evidence.text);
  for (const name of ["Assign to me", "Unassign", "Save workflow", "Add note", "Accept risk"]) {
    for (const button of await finding.getByRole("button", { name, exact: true }).all()) await expect(button).toBeDisabled();
  }
  await finding.getByRole("button", { name: "Close finding details", exact: true }).click();
  const personal: SavedView = { ...viewAt(105), name: "Viewer own preference", query: "" };
  await startSave(page, api, personal.name, "", "source-order");
  const created = api.createView({ name: personal.name, query: "", sort: "source-order" }, viewDetail(personal));
  await saveView(page).click(); await arrived(created); await release(page, created); await expect(viewRow(page, personal)).toHaveCount(1);
  await editView(page, api, personal);
  const renamed: SavedView = { ...personal, name: "Viewer renamed preference", revision: "2", updatedAt: "2026-09-30T12:01:00Z" };
  await nameInput(editForm(page)).fill(renamed.name);
  const patched = api.patchView(personal.id, { revision: "1", name: renamed.name }, viewDetail(renamed));
  await saveChanges(page).click(); await arrived(patched); await release(page, patched);
  await deleteView(page, api, renamed);
  const deleted = api.deleteView(renamed.id, "2");
  await deleteForm(page).getByRole("button", { name: "Confirm delete", exact: true }).click();
  await arrived(deleted); await release(page, deleted); await expect(viewRow(page, renamed)).toHaveCount(0);
  expect(api.requests.filter((call) => call.method !== "GET" && call.path.startsWith("/api/v1/findings"))).toHaveLength(0);
  api.mark("Viewer created, edited and deleted only their scoped personal preferences; finding mutations remained unavailable.");

  for (const status of [403, 404] as const) {
    const denied = api.detailView(primaryView.id, undefined, { status });
    const before = api.calls().length;
    await viewAction(page, "Apply").click(); await arrived(denied); await release(page, denied);
    await expect(panel(page).getByRole("alert")).toBeVisible(); await expect(viewRow(page)).toHaveCount(0);
    await expect(viewRow(page, emptyView)).toHaveCount(1); await active(page, ""); await counts(page, 100, alphaTotal);
    const unavailable = api.detailView(primaryView.id, undefined, { status: 503 });
    await retryView(page).click(); await arrived(unavailable); await release(page, unavailable);
    await expect(viewRow(page)).toHaveCount(0); await expect(editForm(page)).toHaveCount(0);
    expect(api.calls()).toHaveLength(before);
    if (status === 403) {
      const authorized = await listRead(page, api, viewPage([primaryView, emptyView]));
      await release(page, authorized); await expect(viewRow(page)).toHaveCount(1);
    }
  }
  const detailRecovery = api.detailView(primaryView.id, viewDetail(primaryView));
  const searchRecovery = api.search(queries.initial);
  await retryView(page).click(); await arrived(detailRecovery); expect(searchRecovery.call).toBeNull();
  await release(page, detailRecovery); await arrived(searchRecovery); await confirmWork(page, searchRecovery, queries.initial);
  const listRecovery = await listRead(page, api, viewPage([primaryView, emptyView]));
  await release(page, listRecovery); await expect(viewRow(page)).toHaveCount(1);
  api.mark("403/404 detail withheld private fields and never searched; 503 did not restore them; explicit authorized detail recovered apply.");

  const workDenied = await manualSearch(page, api, queries.asset, 403);
  await release(page, workDenied); await expect(rows(page)).toHaveCount(0); await expect(viewRow(page)).toHaveCount(1);
  const failedApply = await applyView(page, api, primaryView, currentView, 503);
  await release(page, failedApply); await expect(rows(page)).toHaveCount(0); await expect(pagination(page)).toHaveCount(0);
  await expect(queue(page).getByRole("alert")).toBeVisible();
  const workRecovery = api.search(queries.asset), viewsBeforeRetry = api.viewCalls.length;
  await queue(page).getByRole("button", { name: "Retry search", exact: true }).click();
  expect((await arrived(workRecovery)).query).toEqual({ q: queries.asset });
  await confirmWork(page, workRecovery, queries.asset); await counts(page, 100, searchTotal);
  expect(api.viewCalls).toHaveLength(viewsBeforeRetry);
  await confirmWork(page, await manualSearch(page, api, queries.initial), queries.initial); await counts(page, 100, 100);

  await editView(page, api, primaryView, currentView);
  const late: SavedView = { ...currentView, name: "Withheld late personal rename", revision: "3", updatedAt: "2026-09-30T12:02:00Z" };
  await nameInput(editForm(page)).fill(late.name);
  const oldReceipt = api.patchView(primaryView.id, { revision: "2", name: late.name }, viewDetail(late));
  await saveChanges(page).click(); await arrived(oldReceipt);
  const listDenied = await listRead(page, api, undefined, 403);
  await release(page, listDenied); await withheldViews(page);
  await release(page, oldReceipt); await settled(oldReceipt); await withheldViews(page);
  await noPrivateDOM(page, late.name, primaryView.name, emptyView.name);
  const listUnavailable = await listRead(page, api, undefined, 503, true);
  await release(page, listUnavailable); await withheldViews(page); await active(page, queries.initial); await counts(page, 100, 100);
  const listAuthorized = await listRead(page, api, viewPage([late, emptyView]), 200, true);
  await release(page, listAuthorized); await expect(viewRow(page, late)).toHaveCount(1);
  api.mark("Metadata did not grant denied Work rows; Work 200 alone recovered Work. List 403 latched through late PATCH 200 and 503 until authorized list 200.");
});

test("SV5 Close reload workspace global 401 and logout discard late private view receipts and drafts", async ({ page, viewsAPI: api }) => {
  await boot(page, api); await openViews(page, api, [primaryView]);
  const closed: SavedView = { ...viewAt(105), name: "Closed panel private create", query: "" };
  await startSave(page, api, closed.name, "", "source-order");
  const closeReceipt = api.createView({ name: closed.name, query: "", sort: "source-order" }, viewDetail(closed));
  await saveView(page).click(); await arrived(closeReceipt);
  await toggle(page).click(); await expect(panel(page)).toHaveCount(0); await expect(toggle(page)).toBeFocused();
  await release(page, closeReceipt); await settled(closeReceipt); await expect(panel(page)).toHaveCount(0);
  await noPrivateDOM(page, closed.name); await api.assertPrivate(page);
  await openViews(page, api, [closed]);
  await startSave(page, api, "Discard this on true reload", "", "source-order");
  const reloadEntry = api.entry(), beforeReload = api.viewCalls.length;
  await page.reload(); await finishWorkEntry(page, api, reloadEntry);
  await expect(toggle(page)).toHaveAttribute("aria-expanded", "false"); await expect(panel(page)).toHaveCount(0);
  expect(api.viewCalls).toHaveLength(beforeReload); await noPrivateDOM(page, closed.name, "Discard this on true reload");
  api.mark("Closing a pending create cleared drafts and late receipt; reopening used authorized list. Actual page.reload cleared panel/drafts with no metadata prefetch.");

  await openViews(page, api, [closed]);
  const alphaDetail = api.detailView(closed.id, viewDetail({ ...closed, query: queries.asset, revision: "2", updatedAt: "2026-09-30T12:01:00Z" }));
  await viewAction(page, "Apply", closed).click(); await arrived(alphaDetail);
  const betaEntry = api.entry(beta.id);
  await page.getByRole("combobox", { name: "Workspace", exact: true }).selectOption(beta.id);
  await finishWorkEntry(page, api, betaEntry); await counts(page, 100, betaTotal);
  const afterSwitch = api.calls().length;
  await expect(panel(page)).toHaveCount(0); await expect(filter(page)).toHaveValue(""); await active(page, "");
  await release(page, alphaDetail); await settled(alphaDetail); expect(api.calls()).toHaveLength(afterSwitch);
  await noPrivateDOM(page, closed.name);
  const betaView = viewAt(1, beta.id);
  await openViews(page, api, [betaView], { workspace: beta.id });
  const betaDetail = api.detailView(betaView.id, viewDetail(betaView), { workspace: beta.id });
  await viewAction(page, "Edit", betaView).click(); await arrived(betaDetail); await release(page, betaDetail);
  const betaChanged: SavedView = { ...betaView, name: "Beta private pending rename", revision: "2", updatedAt: "2026-09-30T12:01:00Z" };
  api.remember(betaChanged.name); await nameInput(editForm(page)).fill(betaChanged.name);
  const betaReceipt = api.patchView(betaView.id, { revision: "1", name: betaChanged.name }, viewDetail(betaChanged), { workspace: beta.id });
  await saveChanges(page).click(); await arrived(betaReceipt);
  const betaFinding = findingAt(1, beta.id), denial = api.denyDetailEntry(betaFinding.id, beta.id);
  await findingButton(page, betaFinding).click();
  await expect.poll(() => denial.calls.some((call) => call.failure === null)).toBe(true); await frames(page);
  expect(denial.calls.length).toBeLessThanOrEqual(2);
  for (const call of denial.calls) expect(call).toMatchObject({ status: 401, responseStatus: null, finished: false });
  const login = page.getByRole("form", { name: "Sign in", exact: true });
  await expect(login).toHaveCount(0);
  for (const response of denial.responses) response.release();
  await Promise.all(denial.responses.filter((response) => response.call !== null).map((response) => response.delivered));
  await expect.poll(() => denial.calls.some((call) => call.responseStatus === 401 && call.responseBeforeFailure)).toBe(true);
  await expect(login).toBeVisible();
  await expect.poll(() => denial.calls.every((call) => call.finished || call.failure !== null)).toBe(true);
  api.finishDetailDenial(denial); expect(api.calls("GET", denial.path)).toEqual(denial.calls);
  await release(page, betaReceipt); await settled(betaReceipt);
  await expect(panel(page)).toHaveCount(0); await expect(table(page)).toHaveCount(0);
  await noPrivateDOM(page, betaView.name, betaChanged.name, closed.name);
  await expect(page).not.toHaveURL(/finding=|[?&](?:q|viewId)=/);
  api.mark("Workspace discarded pending apply. Actual scoped detail 401 response event preceded failure and real Sign in; late personal PATCH could not restore private UI.");

  const fresh = api.entry();
  await login.getByRole("textbox", { name: "Email", exact: true }).fill(user.email);
  await login.getByLabel("Password", { exact: true }).fill(password);
  await login.getByRole("button", { name: "Sign in", exact: true }).click();
  await finishWorkEntry(page, api, fresh); await active(page, ""); await expect(panel(page)).toHaveCount(0);
  await openViews(page, api, [primaryView]);
  const loggedOut: SavedView = { ...viewAt(106), name: "Logout private pending create", query: "" };
  await startSave(page, api, loggedOut.name, "", "source-order");
  const logoutReceipt = api.createView({ name: loggedOut.name, query: "", sort: "source-order" }, viewDetail(loggedOut));
  await saveView(page).click(); await arrived(logoutReceipt);
  await page.getByRole("button", { name: "Sign out", exact: true }).click(); await expect(login).toBeVisible();
  await release(page, logoutReceipt); await settled(logoutReceipt);
  await expect(login).toBeVisible(); await expect(panel(page)).toHaveCount(0); await expect(table(page)).toHaveCount(0);
  await noPrivateDOM(page, loggedOut.name, primaryView.name); await api.assertPrivate(page);
  expect(api.calls("POST", "/api/v1/logout")).toHaveLength(1);
  api.mark("Fresh login began closed/bare; logout plus late POST left no private view metadata, form values, Work context or browser persistence.");
});

test("SV6 Mobile keyboard personal forms enforce UTF8 strict metadata and canonical receipt boundaries", async ({ page, viewsAPI: api }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await boot(page, api); await openViews(page, api, [primaryView], { keyboard: true });
  await startSave(page, api, "draft", "", "source-order");
  for (const invalid of ["   ", "a".repeat(257), "\u00e9".repeat(129), "private\0name"]) {
    api.remember(invalid); await nameInput(saveForm(page)).fill(invalid);
    const before = api.requests.length;
    await nameInput(saveForm(page)).press("Enter");
    await expect(nameInput(saveForm(page))).toHaveValue(invalid);
    await expect(nameInput(saveForm(page))).toHaveAttribute("aria-invalid", "true");
    await expect(saveForm(page).getByRole("alert")).toBeVisible(); expect(api.requests).toHaveLength(before);
  }
  await nameInput(saveForm(page)).fill(`  ${maxViewName}  `);
  const maxNameView: SavedView = { ...viewAt(105), name: maxViewName, query: "" };
  const created = api.createView({ name: maxViewName, query: "", sort: "source-order" }, viewDetail(maxNameView));
  await nameInput(saveForm(page)).press("Enter"); await arrived(created); await release(page, created);
  await expect(viewRow(page, maxNameView)).toHaveCount(1); await expect(saveCurrent(page)).toBeFocused();
  expect(await page.evaluate(() => document.documentElement.scrollWidth - innerWidth)).toBeLessThanOrEqual(1);
  await editView(page, api, primaryView);
  expect(await sortInput(editForm(page)).locator("option").evaluateAll((options) =>
    options.map((option) => (option as HTMLOptionElement).value))).toEqual([...viewSorts]);
  for (const invalid of ["q".repeat(513), "\u00e9".repeat(257), "private\0query"]) {
    api.remember(invalid); await queryInput(editForm(page)).fill(invalid);
    const before = api.requests.length;
    await saveChanges(page).focus(); await saveChanges(page).press("Enter");
    await expect(queryInput(editForm(page))).toHaveValue(invalid);
    await expect(queryInput(editForm(page))).toHaveAttribute("aria-invalid", "true");
    await expect(editForm(page).getByRole("alert")).toBeVisible(); expect(api.requests).toHaveLength(before);
  }
  await queryInput(editForm(page)).fill(`  ${maxViewQuery}  `);
  const edited: SavedView = { ...primaryView, query: maxViewQuery, revision: "2", updatedAt: "2026-09-30T12:01:00Z" };
  const patched = api.patchView(primaryView.id, { revision: "1", query: maxViewQuery }, viewDetail(edited));
  await saveChanges(page).focus(); await saveChanges(page).press("Enter"); await arrived(patched); await release(page, patched);
  await expect(editForm(page)).toHaveCount(0); await active(page, "");
  api.mark("390px keyboard forms preserved invalid drafts with accessible errors/no HTTP; trimmed 256-byte name and 512-byte query passed; sort options were the exact enum.");

  const invalidDetails: Array<{ label: string; payload: unknown }> = [
    { label: "version", payload: { apiVersion: "aspm/unknown", view: edited } },
    { label: "wrong ID", payload: viewDetail({ ...edited, id: emptyView.id }) },
    { label: "zero revision", payload: viewDetail({ ...edited, revision: "0" }) },
    { label: "numeric revision", payload: { apiVersion, view: { ...edited, revision: 2 } } },
    { label: "noncanonical revision", payload: viewDetail({ ...edited, revision: "02" }) },
    { label: "blank name", payload: viewDetail({ ...edited, name: "   " }) },
    { label: "oversized name", payload: viewDetail({ ...edited, name: "\u00e9".repeat(129) }) },
    { label: "sort", payload: { apiVersion, view: { ...edited, sort: "global-severity" } } },
    { label: "NUL query", payload: viewDetail({ ...edited, query: "bad\0query" }) },
    { label: "timestamp", payload: viewDetail({ ...edited, createdAt: "not-a-time" }) },
    { label: "stale known revision", payload: viewDetail(primaryView) },
  ];
  for (const [index, invalid] of invalidDetails.entries()) {
    const response = api.detailView(primaryView.id, invalid.payload);
    const before = api.calls().length;
    await (index === 0 ? viewAction(page, "Apply") : retryView(page)).click();
    await arrived(response); await release(page, response);
    await expect(panel(page).getByRole("alert"), invalid.label).toBeVisible();
    expect(api.calls(), `Invalid ${invalid.label} must not start Work query.`).toHaveLength(before);
    await active(page, ""); await counts(page, 100, alphaTotal);
  }
  const canonical: SavedView = { ...currentView, sort: "severity", revision: "3", updatedAt: "2026-09-30T12:02:00Z" };
  const canonicalDetail = api.detailView(primaryView.id, viewDetail(canonical)), searched = api.search(queries.asset);
  await retryView(page).click(); await arrived(canonicalDetail); expect(searched.call).toBeNull();
  await release(page, canonicalDetail); await arrived(searched); await confirmWork(page, searched, queries.asset);
  api.mark("Invalid envelope/ID/revision type or value/sort/NUL/timestamp and older-known detail never searched; explicit canonical detail then q-only Work 200 recovered.");

  for (const [index, status] of ([200, 201] as const).entries()) {
    const refused: SavedView = { ...viewAt(106 + index), name: `Unconfirmed canonical receipt ${status}`, query: queries.asset, sort: "severity" };
    await startSave(page, api, refused.name, queries.asset, "severity");
    const payload = status === 200 ? viewDetail(refused) : { apiVersion, view: { ...refused, id: "invented" } };
    const response = api.createView({ name: refused.name, query: queries.asset, sort: "severity" }, payload, { status });
    await saveView(page).click(); await arrived(response); await release(page, response);
    await expect(saveForm(page).getByRole("alert")).toBeVisible(); await expect(viewRow(page, refused)).toHaveCount(0);
    await cancel(page, api, saveForm(page));
  }
  const invalidList = await listRead(page, api, viewPage([canonical], 0));
  await release(page, invalidList); await expect(panel(page).getByRole("alert")).toBeVisible(); await expect(emptyViews(page)).toHaveCount(0);
  const empty = await listRead(page, api, viewPage([]), 200, true);
  await release(page, empty); await expect(emptyViews(page)).toBeVisible(); await expect(viewRows(page)).toHaveCount(0);
  const geometry = await page.evaluate(async () => {
    const motion = new Set<string>();
    for (let index = 0; index < 6; index++) {
      for (const animation of document.getAnimations()) {
        if (!(animation.effect instanceof KeyframeEffect) || animation.playState !== "running") continue;
        const keyframes = animation.effect.getKeyframes() as Array<Record<string, unknown>>;
        for (const name of ["transform", "translate", "scale", "rotate", "left", "top"]) {
          if (new Set(keyframes.map((frame) => frame[name]).filter((value) => value !== undefined).map(String)).size > 1) motion.add(name);
        }
      }
      await new Promise<void>((done) => requestAnimationFrame(() => done()));
    }
    return { reduced: matchMedia("(prefers-reduced-motion: reduce)").matches,
      overflow: document.documentElement.scrollWidth - innerWidth, motion: [...motion] };
  });
  expect(geometry).toMatchObject({ reduced: true, motion: [] }); expect(geometry.overflow).toBeLessThanOrEqual(1);
  await toggle(page).focus(); await noIO(page, api, () => toggle(page).press("Space"));
  await expect(panel(page)).toHaveCount(0); await expect(toggle(page)).toBeFocused();
  await api.assertPrivate(page);
  api.mark("Wrong-status/malformed creation receipts added no rows; invalid list count was not empty, authorized empty was distinct; reduced-motion keyboard panel stayed within 390px.");
});
