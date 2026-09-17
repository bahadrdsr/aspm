import type { Page } from "@playwright/test";
import { requireProductionUI } from "./network";
import {
  apiVersion, backendID, collectionPath, collectionResult, collectionsPath, githubSource,
  queuedCollection, sourceAlpha, sourceSeries, sourcesPath, sourceUpdatedAt, sourceUser, validText,
} from "./source-ui-data";
import type { UISource } from "./source-ui-data";
import { expect, test } from "./source-ui-fixture";
import type { SourceCall, SourceControl, SourcesUIAPI } from "./source-ui-fixture";

test.use({ reducedMotion: "reduce", timezoneId: "UTC" });
test.beforeEach(async ({ sources }) => { requireProductionUI(); expect(sources.requests).toEqual([]); });

const refreshedSource: UISource = {
  ...githubSource, name: "Synthetic refreshed selected source",
  repository: "synthetic-owner/authorized-new-target", revision: githubSource.revision + 1, updatedAt: sourceUpdatedAt,
};
const laterSource: UISource = {
  ...refreshedSource, name: "Synthetic later authorized selected source",
  repository: "synthetic-owner/authorized-later-target", revision: refreshedSource.revision + 1,
  updatedAt: "2026-09-17T12:15:16.654321Z",
};
const sourceCollectionPath = collectionsPath(githubSource.id);
function inventory(page: Page) { return page.getByRole("region", { name: /^(?:Workspace )?Sources$/i, includeHidden: true }); }
function sourceRow(page: Page, source: UISource) {
  return inventory(page).getByRole("table", { name: /^(?:Workspace )?Sources$/i })
    .getByRole("row").filter({ has: page.getByText(source.name, { exact: true }) });
}
function history(page: Page) { return page.getByRole("region", { name: /^Source collections$|^Collection history$|^Collections$/i }); }
function historyRow(page: Page, id: string) {
  return history(page).getByRole("table", { name: /collections/i }).getByRole("row").filter({ hasText: id });
}
function collect(page: Page) { return history(page).getByRole("button", { name: /^Collect source$/i }); }
function consent(page: Page) { return page.getByRole("dialog", { name: /^Collect(?: GitHub)? source$|^Confirm collection$/i }); }
function confirm(page: Page) {
  return consent(page).getByRole("button", { name: /^Collect source$|^Queue collection$|^Confirm (?:same )?collection$/i });
}
function refresh(page: Page) { return inventory(page).getByRole("button", { name: /^Refresh sources$/i }); }
function more(page: Page) { return inventory(page).getByRole("button", { name: /^Load more sources$|^Next sources$/i }); }
async function rendered(page: Page) {
  await page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))));
}
async function requested(control: SourceControl) {
  await expect.poll(() => control.call !== null, "The actual selected-workspace HTTP request must reach its held response.").toBe(true);
  return control.call!;
}
async function release(page: Page, control: SourceControl) {
  control.release(); await control.delivered; await rendered(page);
}
async function openHistory(page: Page) {
  await page.goto("/#/integrations");
  await expect(sourceRow(page, githubSource)).toBeVisible();
  await sourceRow(page, githubSource).getByRole("button", { name: /^Collections$|^Collection history$|^View source$|^Open source$/i }).click();
  await expect(history(page).getByText(githubSource.repository, { exact: true })).toBeVisible();
}
async function selectedFacts(page: Page, source: UISource) {
  await expect(history(page).getByText(source.repository, { exact: true }),
    "The selected source summary must use the same-ID repository from the newly authorized GET, not the old selection copy.").toBeVisible();
  await expect(history(page).getByText(source.name, { exact: true })).toBeVisible();
  if (source.repository !== githubSource.repository) await expect(history(page).getByText(githubSource.repository, { exact: true })).toHaveCount(0);
}
async function consentFacts(page: Page, source: UISource) {
  await expect(consent(page).getByText(source.repository, { exact: true }),
    "Enabled collection consent must describe the current authorized repository before any explicit POST.").toBeVisible();
  await expect(consent(page).getByText(source.name, { exact: true })).toBeVisible();
  await expect(consent(page)).toContainText(new RegExp(`(?:source )?revision\\s*:?\\s*${source.revision}\\b`, "i"));
  if (source.repository !== githubSource.repository) await expect(consent(page).getByText(githubSource.repository, { exact: true })).toHaveCount(0);
}
async function openConsent(page: Page, source: UISource) {
  await collect(page).click(); await expect(consent(page)).toBeVisible();
  await consentFacts(page, source);
}
async function refreshedRow(page: Page, source: UISource) {
  const row = sourceRow(page, source);
  await expect(row).toHaveCount(1);
  await expect(row.getByText(source.repository, { exact: true })).toBeVisible();
  await expect(row).toContainText(new RegExp(`revision\\s*:?\\s*${source.revision}\\b`, "i"));
}
function pageCursor(call: SourceCall) {
  const cursor = call.response?.nextCursor;
  if (!backendID(cursor)) throw new Error("This bounded source page must return its actual native nextCursor.");
  return cursor;
}
async function startRefresh(page: Page, sources: SourcesUIAPI, value: UISource) {
  sources.seedSource(value);
  const held = sources.queueRead(sourcesPath, 200, true, sourceAlpha.id, "");
  await refresh(page).click();
  const call = await requested(held);
  expect(call).toMatchObject({ method: "GET", path: sourcesPath, workspace: sourceAlpha.id, query: {}, body: {}, status: 200 });
  expect(call.response).toMatchObject({ apiVersion, items: expect.arrayContaining([value]) });
  return held;
}
function keyOnly(call: SourceCall) {
  expect(call).toMatchObject({ method: "POST", path: sourceCollectionPath, workspace: sourceAlpha.id, query: {} });
  expect(Object.keys(call.body)).toEqual(["idempotencyKey"]);
  expect(validText(call.body.idempotencyKey, 256)).toBe(true);
}
async function noEnabledCollection(page: Page) {
  for (const action of await collect(page).all()) await expect(action).toBeDisabled();
  for (const action of await confirm(page).all()) await expect(action).toBeDisabled();
}
async function pageAway(page: Page, sources: SourcesUIAPI, cursor: string) {
  const held = sources.queueRead(sourcesPath, 200, true, sourceAlpha.id, cursor);
  await more(page).click();
  const call = await requested(held);
  expect(call.query).toEqual({ cursor });
  const tail = sourceSeries(102).slice(100);
  expect(call.response).toEqual({ apiVersion, items: tail, total: 102, nextCursor: null });
  expect(tail.every((source) => source.id !== githubSource.id)).toBe(true);
  await release(page, held);
  for (const source of tail) await expect(sourceRow(page, source)).toHaveCount(1);
}

test("ST1 Authorized source refresh updates the selected target before consent; later-page absence and disable do not revive old facts", async ({ page, sources }, testInfo) => {
  sources.seedSources(sourceAlpha.id, sourceSeries(102));
  const historical = collectionResult(queuedCollection(githubSource, 31), "succeeded");
  sources.seedCollection(historical);
  await openHistory(page);
  await expect(historyRow(page, historical.id)).toHaveCount(1);
  const updated = await startRefresh(page, sources, refreshedSource);
  await expect(history(page).getByText(githubSource.repository, { exact: true })).toBeVisible();
  expect(sources.calls("POST", sourceCollectionPath)).toHaveLength(0);
  await release(page, updated);
  await refreshedRow(page, refreshedSource);
  await testInfo.attach("authorized-table-and-selected-target", {
    body: JSON.stringify({
      phase: "Authorized list GET completed; table shows revision 5; collection selection must now use those same facts",
      sourceId: githubSource.id, oldMetadata: githubSource, authorizedMetadata: refreshedSource,
      response: updated.call!.response, selectedSummary: await history(page).allTextContents(),
      enqueues: sources.calls("POST", sourceCollectionPath).length,
    }, null, 2), contentType: "application/json",
  });
  await selectedFacts(page, refreshedSource);
  await openConsent(page, refreshedSource);
  expect(sources.calls("POST", sourceCollectionPath)).toHaveLength(0);
  await page.keyboard.press("Escape");

  await pageAway(page, sources, pageCursor(updated.call!));
  await selectedFacts(page, refreshedSource);
  await expect(historyRow(page, historical.id)).toHaveCount(1);
  await openConsent(page, refreshedSource);
  const enqueue = sources.queueWrite("POST", sourceCollectionPath, 202, true);
  await confirm(page).click();
  const call = await requested(enqueue); keyOnly(call);
  expect(call.response).toMatchObject({ apiVersion, collection: {
    sourceId: refreshedSource.id, connectionRevision: refreshedSource.revision, repository: refreshedSource.repository,
    profile: refreshedSource.profile, requestedBy: sourceUser.id, state: "queued", complete: false,
  } });
  await release(page, enqueue);
  await expect(consent(page)).toHaveCount(0);

  const disabled: UISource = { ...refreshedSource, enabled: false, revision: refreshedSource.revision + 1, updatedAt: laterSource.updatedAt };
  const disableRead = await startRefresh(page, sources, disabled);
  await release(page, disableRead); await refreshedRow(page, disabled); await selectedFacts(page, disabled);
  await expect(history(page)).toContainText(/disabled for collection|collection.*disabled|source.*disabled/i);
  await noEnabledCollection(page);
  await historyRow(page, historical.id).getByRole("button", { name: /^Open collection(?: .*)?$|^View collection(?: .*)?$/i }).click();
  const detail = page.getByRole("region", { name: /^Selected collection$|^Collection details$/i });
  await expect(detail).toContainText(historical.id);
  await expect(detail.getByText(historical.repository, { exact: true })).toBeVisible();
  const historicalRead = sources.calls("GET", collectionPath(historical.id)).at(-1);
  expect(historicalRead?.response).toEqual({ apiVersion, collection: historical });
  await pageAway(page, sources, pageCursor(disableRead.call!));
  await selectedFacts(page, disabled); await noEnabledCollection(page);
  await expect(historyRow(page, historical.id)).toHaveCount(1);
  expect(sources.collections.get(historical.id)).toEqual(historical);
  expect(sources.requests.filter((request) => request.method !== "GET")).toEqual([call]);
  await sources.assertPrivate(page, true);
});

test("ST2 A held authorized refresh updates or invalidates open consent without posting or forgetting a lost-ACK intent", async ({ page, sources }, testInfo) => {
  await openHistory(page);
  const updated = await startRefresh(page, sources, refreshedSource);
  const openedDuringRead = await collect(page).isEnabled();
  if (openedDuringRead) await openConsent(page, githubSource);
  else await expect(collect(page)).toBeDisabled();
  expect(sources.calls("POST", sourceCollectionPath)).toHaveLength(0);
  await release(page, updated);
  // A modal legitimately makes the underlying Sources table inert to role queries.
  await expect(inventory(page).getByText(refreshedSource.repository, { exact: true })).toBeVisible();
  await testInfo.attach("authorized-refresh-while-consent-open", {
    body: JSON.stringify({
      phase: "Same-ID revision 5 GET completed while revision 4 consent was open or collection was deliberately blocked",
      openedDuringRead, sourceId: githubSource.id, authorizedMetadata: refreshedSource,
      openConsent: await consent(page).allTextContents(),
      selectedSummary: await page.getByRole("region", { name: /^Source collections$/i, includeHidden: true }).allTextContents(),
      enqueues: sources.calls("POST", sourceCollectionPath).length,
    }, null, 2), contentType: "application/json",
  });
  expect(sources.calls("POST", sourceCollectionPath)).toHaveLength(0);
  if (await consent(page).isVisible()) {
    if (await confirm(page).isEnabled()) await consentFacts(page, refreshedSource);
    else {
      await expect(confirm(page)).toBeDisabled();
      await page.keyboard.press("Escape");
    }
  }
  if (!await consent(page).isVisible()) {
    await selectedFacts(page, refreshedSource);
    await openConsent(page, refreshedSource);
  }

  // Keep reconciliation reads held so a metadata refresh alone cannot resolve the lost intent.
  const reconciliation = sources.queueRead(sourceCollectionPath, 200, true);
  const dropped = sources.queueWrite("POST", sourceCollectionPath, "drop-ack");
  await confirm(page).click();
  const lost = await requested(dropped); keyOnly(lost); await dropped.delivered;
  await expect.poll(() => lost.failure).toMatch(/failed/i);
  expect(lost.response).toMatchObject({ apiVersion, collection: {
    sourceId: refreshedSource.id, connectionRevision: refreshedSource.revision, repository: refreshedSource.repository, state: "queued",
  } });
  await expect(consent(page).getByRole("alert")
    .or(page.getByRole("region", { name: /^Source collections$/i, includeHidden: true }).getByRole("alert")).first()).toBeVisible();
  if (await consent(page).isVisible()) await page.keyboard.press("Escape");
  const persisted = structuredClone([...sources.collections.values()]);
  expect(persisted).toHaveLength(1);
  const newer = await startRefresh(page, sources, laterSource);
  await release(page, newer); await refreshedRow(page, laterSource); await selectedFacts(page, laterSource);
  expect(sources.calls("POST", sourceCollectionPath)).toHaveLength(1);
  if (await collect(page).count() && await collect(page).isEnabled()) {
    await openConsent(page, laterSource);
    if (await confirm(page).isEnabled()) {
      const replay = sources.queueWrite("POST", sourceCollectionPath, 202, true);
      await confirm(page).click();
      const repeated = await requested(replay); keyOnly(repeated);
      expect(repeated.body, "An unresolved intent must not acquire a fresh idempotency key just because authorized metadata changed.").toEqual(lost.body);
      await release(page, replay);
      expect(repeated.status).toBe(409);
      await expect(consent(page).getByRole("alert")).toContainText(/conflict|changed|reconcil/i);
    } else await expect(confirm(page)).toBeDisabled();
    await page.keyboard.press("Escape");
  } else await noEnabledCollection(page);
  expect([...sources.collections.values()]).toEqual(persisted);
  expect(sources.calls("POST", sourceCollectionPath).every((request) => request.body.idempotencyKey === lost.body.idempotencyKey)).toBe(true);
  const posts = sources.calls("POST", sourceCollectionPath).length;
  reconciliation.release();
  if (reconciliation.call) await reconciliation.delivered;
  await rendered(page);
  expect(sources.calls("POST", sourceCollectionPath)).toHaveLength(posts);
  expect(sources.requests.filter((request) => request.method !== "GET").every((request) =>
    request.method === "POST" && request.path === sourceCollectionPath)).toBe(true);
  await sources.assertPrivate(page, true);
});
