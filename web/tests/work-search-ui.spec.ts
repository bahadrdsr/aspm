import type { Locator, Page } from "@playwright/test";
import { requireProductionUI } from "./network";
import { expect, test } from "./work-search-fixture";
import type { WorkControl, WorkEntry, WorkSearchAPI } from "./work-search-fixture";
import {
  alpha, alphaTotal, beta, betaTotal, currentOwner, cursor200, findingAt, first, later, maxByteQuery,
  password, pathFor, queries, searchFirst, searchTotal, user, workItem, workPath,
} from "./work-search-data";
import type { ActionFinding } from "./work-search-data";

test.use({ reducedMotion: "reduce", timezoneId: "UTC" });
test.beforeEach(async ({ searchAPI }) => { requireProductionUI(); expect(searchAPI.requests).toEqual([]); });
const main = (page: Page) => page.getByRole("main", { includeHidden: true });
const queue = (page: Page) => main(page).getByRole("region", { name: "Finding work queue", exact: true, includeHidden: true });
const table = (page: Page) => queue(page).getByRole("table", { name: "Findings", exact: true, includeHidden: true });
const rows = (page: Page) => table(page).locator("tbody tr");
const row = (page: Page, finding = first) => rows(page).filter({ hasText: finding.title });
const trigger = (page: Page, finding = first) => row(page, finding).getByRole("button", { name: finding.title, exact: true, includeHidden: true });
const selected = (page: Page, finding = first) => row(page, finding).getByRole("checkbox", { name: `Select ${finding.title}`, exact: true, includeHidden: true });
const filter = (page: Page) => main(page).getByRole("textbox", { name: "Filter findings", exact: true, includeHidden: true });
const search = (page: Page) => queue(page).getByRole("button", { name: "Search all findings", exact: true });
const clear = (page: Page) => queue(page).getByRole("button", { name: "Clear search", exact: true });
const more = (page: Page) => queue(page).getByRole("button", { name: "Load more findings", exact: true });
const retrySearch = (page: Page) => queue(page).getByRole("button", { name: "Retry search", exact: true });
const retryMore = (page: Page) => queue(page).getByRole("button", { name: "Retry more findings", exact: true });
const refresh = (page: Page) => main(page).getByRole("button", { name: "Refresh", exact: true });
const searchStatus = (page: Page) => queue(page).getByRole("status", { name: "Workspace search", exact: true, includeHidden: true });
const pagination = (page: Page) => queue(page).getByRole("status", { name: "Finding pagination", exact: true, includeHidden: true });
const selection = (page: Page) => queue(page).getByRole("status", { includeHidden: true }).filter({ hasText: /selected.*on this page/i });
const next = (page: Page) => queue(page).getByRole("button", { name: "Next", exact: true });
const dialog = (page: Page, finding = first) => page.getByRole("dialog", { name: finding.title, exact: true });
const navigation = (page: Page) => page.getByRole("navigation", { name: "Primary", exact: true });

async function frames(page: Page) {
  await page.evaluate(() => new Promise<void>((done) => requestAnimationFrame(() => requestAnimationFrame(() => done()))));
}
async function edit(page: Page, api: WorkSearchAPI, value: string) {
  const before = api.requests.length;
  await filter(page).fill(value); await frames(page);
  expect(api.requests, "Editing the shared local filter must issue zero HTTP.").toHaveLength(before);
}
async function arrived(control: WorkControl) {
  await expect.poll(() => control.call !== null, "The explicit request must reach the real transport ledger.").toBe(true);
  return control.call!;
}
async function release(page: Page, control: WorkControl) {
  control.release(); await control.delivered; await frames(page);
}
async function settledOld(control: WorkControl) {
  const call = await arrived(control);
  await expect.poll(() => call.finished || call.failure !== null).toBe(true);
  if (call.failure !== null) expect(call.failure).toMatch(/abort/i);
  else expect(call).toMatchObject({ responseStatus: 200, responseBeforeFailure: true, finished: true });
}
async function active(page: Page, q: string) {
  await expect(searchStatus(page)).toContainText(q);
  await expect(searchStatus(page)).toContainText(/confirmed/i);
  await expect(searchStatus(page)).toContainText(/workspace/i);
  await expect(searchStatus(page)).toContainText(/server/i);
}
async function bare(page: Page) {
  await expect(searchStatus(page)).toContainText(/No server search/i);
}
async function counts(page: Page, loaded: number, total: number) {
  await expect(pagination(page)).toContainText(new RegExp(`(?:\\b${loaded}\\b[^.;]*loaded|loaded[^.;]*\\b${loaded}\\b)`, "i"));
  await expect(pagination(page)).toContainText(new RegExp(`(?:\\b${total}\\b[^.;]*(?:total|reported)|(?:total|reported)[^.;]*\\b${total}\\b)`, "i"));
  await expect(pagination(page)).toContainText(/last|reported/i);
}
async function titles(page: Page, findings: ActionFinding[]) {
  await expect(rows(page).getByRole("button", { includeHidden: true })).toHaveText(findings.map((finding) => finding.title));
  await expect(rows(page)).toHaveCount(findings.length);
}
async function exhausted(page: Page) {
  expect(await more(page).count()).toBeLessThanOrEqual(1);
  for (const button of await more(page).all()) await expect(button).toBeDisabled();
}
async function finishBareEntry(page: Page, api: WorkSearchAPI, entry: WorkEntry, workspace = alpha.id) {
  await expect(rows(page)).toHaveCount(50); await frames(page);
  await expect.poll(() => entry.calls.length > 0 && entry.calls.every((call) => call.finished || call.failure !== null)).toBe(true);
  const call = api.finishEntry(entry, workspace);
  expect(call.query).toEqual({});
  return call;
}
async function boot(page: Page, api: WorkSearchAPI) {
  const entry = api.entry();
  await page.goto("/#/work");
  const call = await finishBareEntry(page, api, entry);
  expect((call.response as { items: unknown[] }).items).toHaveLength(100);
  await counts(page, 100, alphaTotal); await expect(filter(page)).toHaveValue("");
  await expect(navigation(page).getByRole("link")).toHaveCount(5);
  await frames(page); expect(api.calls()).toHaveLength(entry.calls.length);
  api.mark("Real App reached: authorized bare native first page, 100 loaded/207 reported, 50 display rows, bounded entry, no auto-drain.");
  await expect(search(page), "Published producer is missing explicit workspace server search: Search all findings.").toBeVisible();
  api.mark("Search all findings control reached; remaining search assertions are now executable.");
  await bare(page);
}
async function submit(page: Page, api: WorkSearchAPI, q: string, options: {
  draft?: string; enter?: boolean; status?: Parameters<WorkSearchAPI["search"]>[2]; workspace?: string;
} = {}) {
  const response = api.search(q, "", options.status ?? 200, true, options.workspace ?? alpha.id);
  await edit(page, api, options.draft ?? q);
  if (options.enter) await filter(page).press("Enter"); else await search(page).click();
  const call = await arrived(response);
  expect(call).toMatchObject({ method: "GET", path: workPath, workspace: options.workspace ?? alpha.id, body: {} });
  expect(call.query).toEqual({ q });
  return response;
}
async function confirm(page: Page, response: WorkControl, q: string) {
  await release(page, response); await active(page, q);
  await expect.poll(() => response.call?.finished).toBe(true);
  expect(response.call).toMatchObject({ responseStatus: 200, responseBeforeFailure: true, failure: null });
}
async function openFinding(page: Page, finding = first) {
  await trigger(page, finding).click(); await expect(dialog(page, finding)).toBeVisible();
  await expect(dialog(page, finding)).toContainText(finding.evidence.text);
}
async function closeFinding(page: Page, finding = first) {
  await dialog(page, finding).getByRole("button", { name: "Close finding details", exact: true }).click();
  await expect(dialog(page, finding)).toHaveCount(0);
}
async function ack(page: Page, api: WorkSearchAPI, finding: ActionFinding, fields: Record<string, unknown>,
  canonical: ActionFinding, action: Locator) {
  const response = api.patch(fields, canonical, true);
  await action.click();
  expect(await arrived(response)).toMatchObject({ method: "PATCH", path: pathFor(finding.id), body: fields });
  await release(page, response);
  await expect(dialog(page, finding).getByRole("status").filter({ hasText: /updated from the service|Human workflow saved/i })).toBeVisible();
}
async function withheld(page: Page) {
  await expect(rows(page)).toHaveCount(0); await expect(selection(page)).toHaveCount(0);
  await expect(pagination(page)).toHaveCount(0);
  await expect(queue(page).getByRole("heading", { name: "No findings", exact: true, includeHidden: true })).toHaveCount(0);
}

test("WS1 Explicit workspace query reaches unloaded findings with query-bound native continuation and refresh", async ({ page, searchAPI: api }) => {
  await boot(page, api);
  await edit(page, api, "  later  ");
  await expect(rows(page)).toHaveCount(0); await counts(page, 100, alphaTotal); await bare(page);
  const searched = await submit(page, api, queries.asset, { draft: "  later  " });
  await bare(page); await counts(page, 100, alphaTotal);
  await confirm(page, searched, queries.asset);
  await titles(page, Array.from({ length: 50 }, (_, i) => findingAt(i + 101)));
  await counts(page, 100, searchTotal); await expect(trigger(page)).toHaveCount(0);
  api.mark("Asset q alone found records outside the bare initial page; the draft still filters only loaded results.");

  await edit(page, api, "0001"); await expect(rows(page)).toHaveCount(0);
  const tail = api.search(queries.asset, cursor200);
  await more(page).click();
  expect((await arrived(tail)).query).toEqual({ q: queries.asset, limit: "100", cursor: cursor200 });
  await expect(more(page)).toBeDisabled(); await counts(page, 100, searchTotal);
  await filter(page).focus(); await release(page, tail);
  await counts(page, 107, searchTotal); await expect(trigger(page, later)).toBeVisible();
  await expect(filter(page)).toBeFocused(); await active(page, queries.asset); await exhausted(page);
  await expect(pagination(page)).toContainText(/not.*(?:atomic|single)|(?:pages|counts).*snapshot/i);
  await expect(queue(page)).toContainText(/sort[^.]*loaded|loaded[^.]*sort/i);
  const beforeSort = api.requests.length;
  await edit(page, api, queries.asset);
  await table(page).getByRole("button", { name: "Finding", exact: true }).click();
  await expect(rows(page).first()).toContainText(later.title);
  await table(page).getByRole("button", { name: "Severity", exact: true }).click();
  await expect(table(page).getByRole("columnheader", { name: "Severity", exact: true })).toHaveAttribute("aria-sort", "ascending");
  await frames(page); expect(api.requests).toHaveLength(beforeSort);

  await edit(page, api, "0001");
  const refreshed = api.search(queries.asset);
  await refresh(page).click(); expect((await arrived(refreshed)).query).toEqual({ q: queries.asset });
  await confirm(page, refreshed, queries.asset); await counts(page, 100, searchTotal);
  await expect(filter(page)).toHaveValue("0001"); await expect(rows(page)).toHaveCount(0); await expect(more(page)).toBeEnabled();
  const titleResult = await submit(page, api, queries.title, { enter: true });
  await confirm(page, titleResult, queries.title); await titles(page, [later]); await counts(page, 1, 1);
  const ownerResult = await submit(page, api, queries.owner);
  await confirm(page, ownerResult, queries.owner); await titles(page, [first]); await counts(page, 1, 1);
  await expect(row(page)).toContainText(currentOwner.name);
  api.mark("Continuation and refresh used confirmed asset q despite an edited draft; title and owner searches used the same native first-page endpoint.");

  await edit(page, api, "current");
  await navigation(page).getByRole("link", { name: "Settings", exact: true }).click();
  await expect(main(page).getByRole("heading", { name: "Settings", exact: true })).toBeVisible();
  const reentry = api.queryEntry(queries.owner);
  await navigation(page).getByRole("link", { name: "Work", exact: true }).click();
  await active(page, queries.owner); await expect(filter(page)).toHaveValue("current"); await titles(page, [first]);
  await expect.poll(() => reentry.calls.length > 0 && reentry.calls.every((call) => call.finished || call.failure !== null)).toBe(true);
  api.finishQueryEntry(reentry); await api.assertPrivate(page);
  api.mark("Settings/Work navigation retained only in-memory confirmed query and separate draft, with a bounded q-only reentry.");
});

test("WS2 Superseded queries never mix rows and retries ignore unsaved drafts before a bare clear", async ({ page, searchAPI: api }) => {
  await boot(page, api);
  await confirm(page, await submit(page, api, queries.asset), queries.asset);
  await next(page).click(); await expect(queue(page)).toContainText("Page 2 of 2");
  await selected(page, findingAt(151)).check();
  const oldTail = api.search(queries.asset, cursor200);
  await more(page).click(); await arrived(oldTail);
  const replacement = await submit(page, api, queries.initial);
  await active(page, queries.asset); await counts(page, 100, searchTotal);
  await expect(searchStatus(page)).not.toContainText(queries.initial);
  await edit(page, api, queries.asset); await expect(selection(page)).toContainText("1");
  await confirm(page, replacement, queries.initial);
  await expect(filter(page)).toHaveValue(queries.asset); await expect(rows(page)).toHaveCount(0);
  await expect(selection(page)).toHaveCount(0);
  await edit(page, api, queries.initial); await expect(queue(page)).toContainText("Page 1 of 2");
  await titles(page, Array.from({ length: 50 }, (_, i) => findingAt(i + 1))); await counts(page, 100, 100);
  await release(page, oldTail); await settledOld(oldTail); await counts(page, 100, 100); await exhausted(page);
  await expect(trigger(page, searchFirst)).toHaveCount(0);

  const oldFirst = await submit(page, api, queries.asset);
  const newer = await submit(page, api, queries.title, { enter: true });
  await active(page, queries.initial);
  await confirm(page, newer, queries.title); await titles(page, [later]);
  await release(page, oldFirst); await settledOld(oldFirst);
  await active(page, queries.title); await titles(page, [later]); await counts(page, 1, 1);
  api.mark("Different successful query reset hidden selected IDs, display page and frontier; older first page/continuation could not overwrite it.");

  const failed = await submit(page, api, queries.initial, { status: 503 });
  await edit(page, api, queries.title); await release(page, failed);
  await expect(queue(page).getByRole("alert")).toBeVisible();
  await active(page, queries.title); await titles(page, [later]); await counts(page, 1, 1);
  await expect(searchStatus(page)).not.toContainText(queries.initial);
  await edit(page, api, "unsaved search draft");
  const retried = api.search(queries.initial);
  await retrySearch(page).click(); expect((await arrived(retried)).query).toEqual({ q: queries.initial });
  await confirm(page, retried, queries.initial); await expect(filter(page)).toHaveValue("unsaved search draft");
  await counts(page, 100, 100); await expect(selection(page)).toHaveCount(0);

  await confirm(page, await submit(page, api, queries.asset), queries.asset);
  const failedTail = api.search(queries.asset, cursor200, 503);
  await more(page).click(); await arrived(failedTail); await release(page, failedTail);
  await expect(queue(page).getByRole("alert")).toBeVisible(); await counts(page, 100, searchTotal);
  await edit(page, api, "0001");
  const retryTail = api.search(queries.asset, cursor200);
  await retryMore(page).click();
  expect((await arrived(retryTail)).query).toEqual({ q: queries.asset, limit: "100", cursor: cursor200 });
  await release(page, retryTail); await counts(page, 107, searchTotal); await titles(page, [later]);
  api.mark("503 search and continuation retries replayed their exact failed q/cursor, not unsaved text; last confirmed rows stayed explicitly labeled.");

  const beforeClear = await submit(page, api, queries.initial);
  const cleared = api.page("");
  await clear(page).click(); expect((await arrived(cleared)).query).toEqual({});
  await release(page, cleared); await bare(page); await counts(page, 100, alphaTotal);
  await expect(filter(page)).toHaveValue(""); await expect(filter(page)).toBeFocused();
  await expect(selection(page)).toHaveCount(0); await expect(queue(page)).toContainText("Page 1 of 2");
  await release(page, beforeClear); await settledOld(beforeClear);
  await bare(page); await counts(page, 100, alphaTotal); await expect(filter(page)).toHaveValue("");
  await titles(page, Array.from({ length: 50 }, (_, i) => findingAt(i + 1)));
  api.mark("Clear search superseded a held query with a truly bare native first page; late completion did not restore old query state.");
});

test("WS3 Mobile keyboard search validates UTF-8 and NUL without HTTP or losing focus and selection boundaries", async ({ page, searchAPI: api }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await boot(page, api);
  await selected(page).check(); await next(page).click(); await selected(page, findingAt(51)).check();
  const entered = await submit(page, api, queries.asset, { draft: "  later  ", enter: true });
  await expect(filter(page)).toBeFocused();
  await confirm(page, entered, queries.asset);
  await expect(filter(page)).toBeFocused(); await expect(selection(page)).toHaveCount(0);
  await expect(queue(page)).toContainText("Page 1 of 2"); await counts(page, 100, searchTotal);
  for (const invalid of ["a".repeat(513), "\u00e9".repeat(257), "later\0not-allowed"]) {
    await edit(page, api, invalid); const before = api.requests.length;
    await filter(page).press("Enter");
    await expect(filter(page)).toHaveValue(invalid);
    await expect(filter(page)).toHaveAttribute("aria-invalid", "true");
    await expect(queue(page).getByRole("alert")).toContainText(invalid.includes("\0") ? /NUL|null|zero.*character/i : /512.*byte|byte.*512/i);
    await frames(page); expect(api.requests).toHaveLength(before);
    await active(page, queries.asset); await counts(page, 100, searchTotal);
  }
  api.mark("ASCII 513 bytes, multibyte 514 bytes and a literal NUL were rejected locally; confirmed query/counts were not an empty success.");

  expect(Buffer.byteLength(maxByteQuery, "utf8")).toBe(512);
  const boundary = await submit(page, api, maxByteQuery, { draft: `  ${maxByteQuery}  `, enter: true });
  await confirm(page, boundary, maxByteQuery); await counts(page, 0, 0);
  await expect(queue(page).getByRole("heading", { name: "No findings", exact: true })).toBeVisible();
  await expect(filter(page)).not.toHaveAttribute("aria-invalid", "true");
  await expect(queue(page).getByRole("alert")).toHaveCount(0); await exhausted(page);
  const geometry = await page.evaluate(async () => {
    const motion = new Set<string>();
    for (let sample = 0; sample < 6; sample++) {
      for (const animation of document.getAnimations()) {
        if (animation.playState !== "running" || !(animation.effect instanceof KeyframeEffect)) continue;
        if (animation.effect.getTiming().iterations === Infinity) motion.add("continuous");
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
  const cleared = api.page("");
  await edit(page, api, "   "); await filter(page).press("Enter");
  expect((await arrived(cleared)).query).toEqual({});
  await release(page, cleared); await bare(page); await counts(page, 100, alphaTotal);
  await expect(filter(page)).toHaveValue(""); await expect(filter(page)).toBeFocused();
  await expect(selection(page)).toHaveCount(0);
  const beforeFocus = api.requests.length;
  await page.keyboard.press("Control+k"); await expect(filter(page)).toBeFocused(); await frames(page);
  expect(api.requests).toHaveLength(beforeFocus);
  api.mark("Trimmed 512 UTF-8 bytes reached q-only HTTP; 390px reduced-motion controls did not overflow, and empty Enter restored bare results and input focus.");
});

test("WS4 Search reads preserve canonical ACKs and Work denial needs Work authority to recover", async ({ page, searchAPI: api }) => {
  await boot(page, api);
  await confirm(page, await submit(page, api, queries.owner), queries.owner);
  await titles(page, [first]); await selected(page).check();
  const staleOwner = api.search(queries.owner);
  await refresh(page).click(); expect((await arrived(staleOwner)).query).toEqual({ q: queries.owner });
  expect((staleOwner.call!.response as { items: unknown[] }).items).toEqual([workItem(first)]);
  await openFinding(page);
  await expect(dialog(page).getByText("Unknown source time", { exact: true })).toBeVisible();
  await expect(dialog(page).locator(`time[datetime="${first.importedAt}"]`)).toHaveCount(1);
  const owned = { ...api.findings.get(first.id)!, ownerId: user.id, ownerName: user.name };
  await ack(page, api, first, { ownerId: user.id }, owned,
    dialog(page).getByRole("button", { name: "Assign to me", exact: true }));
  await expect(dialog(page).getByRole("status").filter({ hasText: /no longer matches/i })).toBeVisible();
  await closeFinding(page); await expect(page.getByRole("heading", { name: "Work", exact: true })).toBeFocused();
  await release(page, staleOwner); await expect(filter(page)).toHaveValue(queries.owner); await expect(rows(page)).toHaveCount(0);
  await edit(page, api, ""); await titles(page, [owned]); await counts(page, 1, 1);
  await expect(row(page)).toContainText(user.name); await expect(row(page)).not.toContainText(currentOwner.name);
  await active(page, queries.owner);
  await expect(searchStatus(page)).toContainText(/membership.*(?:refresh|not.*confirm|may.*chang)|refresh.*(?:membership|match)/i);
  api.mark("Pre-ACK owner-search read could not roll back canonical owner or source fields; local-match exit/focus and unconfirmed server membership stayed honest.");

  for (const denial of [403, 404] as const) {
    await confirm(page, await submit(page, api, queries.asset), queries.asset);
    await counts(page, 100, searchTotal); await expect(trigger(page)).toHaveCount(0);
    await selected(page, searchFirst).check();
    const oldPage = api.search(queries.asset, cursor200);
    await more(page).click(); await arrived(oldPage);
    const denied = await submit(page, api, queries.initial, { status: denial });
    await edit(page, api, queries.asset); await openFinding(page, searchFirst);
    await release(page, denied); await withheld(page);
    const workflow = denial === 403 ? "resolved" as const : "in-progress" as const;
    const canonical = { ...api.findings.get(searchFirst.id)!, workflowState: workflow };
    await dialog(page, searchFirst).getByRole("combobox", { name: "Workflow", exact: true }).selectOption(workflow);
    await ack(page, api, searchFirst, { workflowState: workflow }, canonical,
      dialog(page, searchFirst).getByRole("button", { name: "Save workflow", exact: true }));
    await withheld(page); await closeFinding(page, searchFirst);
    await release(page, oldPage); await settledOld(oldPage); await withheld(page);
    const failed = await submit(page, api, queries.title, { status: 503 });
    await withheld(page); await release(page, failed);
    await expect(queue(page).getByRole("alert")).toBeVisible(); await withheld(page);
    const recovery = await submit(page, api, queries.initial);
    await withheld(page); await confirm(page, recovery, queries.initial);
    await counts(page, 100, 100); await expect(rows(page)).toHaveCount(50);
    await expect(selection(page)).toHaveCount(0); await expect(row(page)).toContainText(user.name);
    api.mark(`${denial} cleared Work rows/selection; detail ACK, old page and failed query could not recover them; authorized Work 200 alone recovered.`);
  }
  api.assertData();
});

test("WS5 Workspace session and logout epochs clear confirmed search and ignore released private results", async ({ page, searchAPI: api }) => {
  await boot(page, api);
  await confirm(page, await submit(page, api, queries.asset), queries.asset);
  await selected(page, searchFirst).check();
  const oldAlpha = api.search(queries.asset, cursor200);
  await more(page).click(); await arrived(oldAlpha);
  const betaEntry = api.entry(beta.id);
  await page.getByRole("combobox", { name: "Workspace", exact: true }).selectOption(beta.id);
  await finishBareEntry(page, api, betaEntry, beta.id);
  await bare(page); await counts(page, 100, betaTotal);
  await expect(filter(page)).toHaveValue(""); await expect(selection(page)).toHaveCount(0);
  await release(page, oldAlpha); await settledOld(oldAlpha);
  await expect(trigger(page, searchFirst)).toHaveCount(0); await bare(page); await counts(page, 100, betaTotal);

  const betaLater = findingAt(101, beta.id);
  await confirm(page, await submit(page, api, queries.asset, { workspace: beta.id }), queries.asset);
  await counts(page, 3, 3); await selected(page, betaLater).check();
  const oldBeta = await submit(page, api, queries.initial, { workspace: beta.id });
  await edit(page, api, queries.asset);
  const denied = api.denyDetailEntry(betaLater.id, beta.id);
  await trigger(page, betaLater).click();
  await expect.poll(() => denied.calls.some((call) => call.failure === null)).toBe(true); await frames(page);
  expect(denied.calls.length).toBeLessThanOrEqual(2);
  for (const call of denied.calls) expect(call).toMatchObject({ status: 401, responseStatus: null, finished: false });
  const login = page.getByRole("form", { name: "Sign in", exact: true });
  await expect(login).toHaveCount(0);
  for (const response of denied.responses) response.release();
  await Promise.all(denied.responses.filter((response) => response.call !== null).map((response) => response.delivered));
  await expect.poll(() => denied.calls.some((call) => call.responseStatus === 401 && call.responseBeforeFailure)).toBe(true);
  await expect(login).toBeVisible();
  await expect.poll(() => denied.calls.every((call) => call.finished || call.failure !== null)).toBe(true);
  api.finishDetailDenial(denied);
  expect(api.calls("GET", denied.path)).toEqual(denied.calls);
  await release(page, oldBeta); await settledOld(oldBeta);
  await expect(login).toBeVisible(); await expect(table(page)).toHaveCount(0);
  await expect(page.getByRole("dialog", { includeHidden: true })).toHaveCount(0);
  await expect(page).not.toHaveURL(/finding=|[?&]q=/); await api.assertPrivate(page);
  api.mark("Workspace change cleared query/selection; actual scoped detail 401 arrived before failure and triggered Sign in with no default-200 fallthrough or stale restoration.");

  const freshEntry = api.entry();
  await login.getByRole("textbox", { name: "Email", exact: true }).fill(user.email);
  await login.getByLabel("Password", { exact: true }).fill(password);
  await login.getByRole("button", { name: "Sign in", exact: true }).click();
  await finishBareEntry(page, api, freshEntry); await bare(page); await counts(page, 100, alphaTotal);
  await expect(filter(page)).toHaveValue(""); await expect(selection(page)).toHaveCount(0);
  const beforeLogout = await submit(page, api, queries.asset);
  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  await expect(login).toBeVisible(); await release(page, beforeLogout); await settledOld(beforeLogout);
  await expect(login).toBeVisible(); await expect(table(page)).toHaveCount(0);
  await expect(page.getByRole("dialog", { includeHidden: true })).toHaveCount(0);
  await expect(page).not.toHaveURL(/finding=|[?&]q=/);
  expect(api.calls("POST", "/api/v1/logout")).toHaveLength(1);
  await api.assertPrivate(page);
  api.mark("Fresh login used bare native Work with cleared private context; logout plus released search could not restore it or persist search text.");
});
