import type { Page } from "@playwright/test";
import { password } from "./application-fixture";
import { requireProductionUI } from "./network";
import {
  exportsPath, reportAlpha, reportBeta, reportExportArtifact, reportUser,
  savedReportExports, savedSnapshots, snapshotsPath,
} from "./reports-data";
import type {
  ReportExportFormat, ReportExportPage, SyntheticReportExport,
} from "./reports-data";
import { expect, test } from "./reports-fixture";
import type { ReportExportContentReply, ReportResponseControl, ReportsAPI } from "./reports-fixture";

test.use({ reducedMotion: "reduce", timezoneId: "UTC" });
test.beforeEach(async ({ reports }) => {
  requireProductionUI();
  expect(reports.requests).toEqual([]);
});

const firstSnapshot = savedSnapshots(reportAlpha.id, 2)[0];
const firstExport = savedReportExports(reportAlpha.id, 4)[0];

function section(page: Page) {
  return page.getByRole("region", { name: "Report exports", exact: true, includeHidden: true });
}
function table(page: Page) {
  return section(page).getByRole("table", { name: "Report exports", exact: true, includeHidden: true });
}
function rows(page: Page) {
  return table(page).getByRole("row").filter({ has: page.getByRole("cell") });
}
function refreshHistory(page: Page) {
  return section(page).getByRole("button", { name: "Refresh exports", exact: true });
}
function loadMore(page: Page) {
  return section(page).getByRole("button", { name: "Load more exports", exact: true });
}
function form(page: Page) {
  return section(page).getByRole("form", { name: "Create report export", exact: true });
}
function detail(page: Page) {
  return page.getByRole("region", { name: "Selected report export", exact: true, includeHidden: true });
}
function refreshDetail(page: Page) {
  return detail(page).getByRole("button", { name: "Refresh export", exact: true });
}
function status(page: Page) {
  return detail(page).getByRole("status", { name: "Export status", exact: true })
    .or(detail(page).getByRole("alert", { name: "Export status", exact: true }));
}
function download(page: Page) {
  return detail(page).getByRole("button", { name: "Download", exact: true })
    .or(detail(page).getByRole("link", { name: "Download", exact: true }));
}

async function frame(page: Page) {
  await page.evaluate(() => new Promise<void>((resolve) =>
    requestAnimationFrame(() => requestAnimationFrame(() => resolve()))));
}

async function reportsPage(page: Page) {
  await page.goto("/#/reports");
  await expect(page.getByRole("main").getByRole("heading", { name: "Reports", exact: true })).toBeVisible();
}

async function openExports(page: Page, reports: ReportsAPI) {
  const before = reports.calls("GET", exportsPath).length;
  const toggle = page.getByRole("button", { name: "Show report exports", exact: true })
    .or(page.getByRole("button", { name: "Report exports", exact: true }));
  await expect(toggle).toBeVisible();
  await toggle.click();
  await expect(section(page)).toBeVisible();
  await expect.poll(() => reports.calls("GET", exportsPath).length).toBe(before + 1);
}

async function release(controls: ReportResponseControl[], abort = false) {
  for (const control of controls) control.release();
  await Promise.all(controls.map((control) => control.delivered));
  if (abort) for (const control of controls) {
    await expect.poll(() => control.call?.failure).toMatch(/abort|failed/i);
  }
}

async function submitExport(page: Page, reports: ReportsAPI, snapshotId: string, format: ReportExportFormat) {
  const before = reports.calls("POST", exportsPath).length;
  await form(page).getByLabel("Saved snapshot", { exact: true }).selectOption(snapshotId);
  await form(page).getByLabel("Export format", { exact: true }).selectOption(format);
  await form(page).getByRole("button", { name: "Create export", exact: true }).click();
  await expect.poll(() => reports.calls("POST", exportsPath).length).toBe(before + 1);
  const call = reports.calls("POST", exportsPath).at(-1)!;
  expect(Object.keys(call.body).sort()).toEqual(["format", "idempotencyKey", "snapshotId"]);
  expect(call.body).toMatchObject({ snapshotId, format, idempotencyKey: expect.any(String) });
  const key = String(call.body.idempotencyKey);
  expect(key.trim()).not.toBe("");
  expect(key).not.toContain("\0");
  expect(Buffer.byteLength(key, "utf8")).toBeLessThanOrEqual(256);
  return call;
}

async function openExport(page: Page, item: SyntheticReportExport) {
  const row = table(page).getByRole("row").filter({ hasText: item.id });
  const action = row.getByRole("button", { name: "Open export", exact: true })
    .or(row.getByRole("link", { name: "Open export", exact: true }));
  await expect(action).toBeVisible();
  await action.click();
  await expect(detail(page).getByText(item.id, { exact: true })).toBeVisible();
}

function latestExportPage(reports: ReportsAPI, workspace = reportAlpha.id) {
  const value = reports.exportPages.filter((entry) => entry.call.workspace === workspace).at(-1);
  if (!value) throw new Error("Expected one completed native report export page.");
  return value;
}

async function expectRows(page: Page, items: SyntheticReportExport[]) {
  await expect(rows(page)).toHaveCount(items.length);
  const text = await rows(page).allTextContents();
  for (const item of items) {
    expect(text.filter((row) => row.includes(item.id) && row.includes(item.snapshotName))).toHaveLength(1);
  }
}

async function downloadBytes(page: Page) {
  const pending = page.waitForEvent("download");
  await download(page).click();
  const value = await pending;
  const stream = await value.createReadStream();
  if (!stream) throw new Error("Report export download had no exact-byte stream.");
  const chunks: Buffer[] = [];
  for await (const chunk of stream) chunks.push(Buffer.from(chunk));
  return { value, body: Buffer.concat(chunks) };
}

async function reducedMovement(page: Page) {
  const violations = await page.evaluate(async () => {
    const failures = new Set<string>();
    for (let index = 0; index < 4; index += 1) {
      for (const animation of document.getAnimations()) {
        if (animation.playState !== "running" || !(animation.effect instanceof KeyframeEffect)) continue;
        if (animation.effect.getTiming().iterations === Infinity) failures.add("continuous animation");
        const frames = animation.effect.getKeyframes() as Array<Record<string, unknown>>;
        for (const key of ["transform", "translate", "rotate", "scale", "left", "top"]) {
          if (new Set(frames.map((item) => item[key]).filter((value) => value !== undefined).map(String)).size > 1) {
            failures.add(key);
          }
        }
      }
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
    }
    return [...failures];
  });
  expect(violations).toEqual([]);
}

async function signIn(page: Page) {
  const login = page.getByRole("form", { name: "Sign in", exact: true });
  await login.getByLabel("Email", { exact: true }).fill(reportUser.email);
  await login.getByLabel("Password", { exact: true }).fill(password);
  await login.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(login).toHaveCount(0);
}

test("Report exports create exact durable intents, refresh explicit states, and download exact JSON or CSV bytes", async ({ page, reports }) => {
  await page.addInitScript(() => {
    const state = { created: [] as string[], revoked: [] as string[] };
    const create = URL.createObjectURL.bind(URL);
    const revoke = URL.revokeObjectURL.bind(URL);
    Object.assign(globalThis, { __reportExportObjectURLs: state });
    URL.createObjectURL = (value: Blob | MediaSource) => {
      const result = create(value);
      state.created.push(result);
      return result;
    };
    URL.revokeObjectURL = (value: string) => {
      state.revoked.push(value);
      revoke(value);
    };
  });
  await page.clock.install();
  reports.seedExportHistory(reportAlpha.id, 0);
  await reportsPage(page);
  expect(reports.calls("GET", exportsPath)).toEqual([]);
  await expect.poll(() => reports.calls("GET", snapshotsPath).length).toBe(1);
  const snapshotReads = reports.calls("GET", snapshotsPath).length;
  await openExports(page, reports);
  await expect(form(page)).toBeVisible();
  await expect(form(page).getByLabel("Saved snapshot", { exact: true })).toHaveValue(firstSnapshot.id);

  const jsonCall = await submitExport(page, reports, firstSnapshot.id, "json");
  const jsonItem = [...reports.exports.values()].find((item) =>
    item.workspaceId === reportAlpha.id && item.snapshotId === firstSnapshot.id && item.format === "json");
  expect(jsonItem).toBeDefined();
  if (!jsonItem) throw new Error("No acknowledged synthetic JSON export.");
  await expect(status(page)).toContainText(/queued/i);
  await expect(download(page)).toHaveCount(0);
  const quietCalls = reports.requests.length;
  await page.clock.fastForward(10_000);
  await frame(page);
  expect(reports.requests, "Queued report exports must not poll or automatically start new work.").toHaveLength(quietCalls);

  reports.setExportState(jsonItem.id, "processing");
  await refreshDetail(page).click();
  await expect(status(page)).toContainText(/processing/i);
  reports.setExportState(jsonItem.id, "failed");
  await refreshDetail(page).click();
  await expect(status(page)).toContainText(/failed/i);
  await expect(detail(page)).toContainText("report-export-generation-failed");
  await expect(detail(page)).toContainText("Synthetic report export generation could not be committed.");
  await expect(download(page)).toHaveCount(0);

  const csvCall = await submitExport(page, reports, firstSnapshot.id, "csv");
  expect(csvCall.body.idempotencyKey).not.toBe(jsonCall.body.idempotencyKey);
  const csvItem = [...reports.exports.values()].find((item) =>
    item.workspaceId === reportAlpha.id && item.snapshotId === firstSnapshot.id && item.format === "csv" &&
    item.id !== jsonItem.id);
  expect(csvItem).toBeDefined();
  if (!csvItem) throw new Error("No acknowledged synthetic CSV export.");
  reports.setExportState(csvItem.id, "succeeded");
  await refreshDetail(page).click();
  const succeeded = reports.exports.get(csvItem.id)!;
  await expect(status(page)).toContainText(/succeeded|completed/i);
  await expect(detail(page)).toContainText(succeeded.digest!);
  await expect(detail(page)).toContainText(`${succeeded.sizeBytes}`);
  const artifact = reports.exportArtifact(succeeded.id);
  const downloaded = await downloadBytes(page);
  expect(downloaded.value.suggestedFilename()).toBe(artifact.filename);
  expect(downloaded.body.equals(artifact.body)).toBe(true);
  await page.clock.fastForward(1);
  await expect.poll(() => page.evaluate(() => {
    const state = (globalThis as typeof globalThis & {
      __reportExportObjectURLs: { created: string[]; revoked: string[] };
    }).__reportExportObjectURLs;
    return { created: state.created.length, revoked: state.revoked.length,
      same: state.created.length === 1 && state.created[0] === state.revoked[0],
      inDOM: state.created.length === 1 && document.documentElement.outerHTML.includes(state.created[0]) };
  })).toEqual({ created: 1, revoked: 1, same: true, inDOM: false });
  expect(reports.calls("GET", `${exportsPath}/${succeeded.id}/content`)).toHaveLength(1);
  expect(reports.calls("GET", snapshotsPath),
    "Report export creation must use already loaded succeeded snapshots without draining history.").toHaveLength(snapshotReads);

  reports.roles.set(reportAlpha.id, "viewer");
  reports.serverRoles.set(reportAlpha.id, "viewer");
  await page.reload();
  await openExports(page, reports);
  await expect(table(page)).toBeVisible();
  await expect(form(page)).toHaveCount(0);
  await openExport(page, succeeded);
  await expect(download(page)).toBeVisible();
  expect(reports.calls("POST", exportsPath)).toHaveLength(2);
});

test("Report export history uses native cursor paging, retains rows on retry, and never auto-drains", async ({ page, reports }) => {
  reports.seedExportHistory(reportAlpha.id, 201);
  await reportsPage(page);
  await openExports(page, reports);
  const first = latestExportPage(reports);
  expect(first.call.query).toEqual({ limit: "100" });
  expect(first.response.total).toBe(201);
  expect(first.response.items).toHaveLength(100);
  expect(first.response.nextCursor).toBe(first.response.items.at(-1)!.id);
  await expectRows(page, first.response.items);

  const unavailable = reports.queueExportHistory(reportAlpha.id, { status: 503 }, true);
  await loadMore(page).click();
  await unavailable.requested;
  expect(unavailable.call!.query).toEqual({ limit: "100", cursor: first.response.nextCursor! });
  await expect(loadMore(page)).toBeDisabled();
  await expectRows(page, first.response.items);
  await release([unavailable]);
  await expect(section(page).getByRole("alert")).toContainText("Synthetic report service unavailable.");
  await expectRows(page, first.response.items);

  await loadMore(page).click();
  await expect.poll(() => reports.exportPages.length).toBe(2);
  const second = latestExportPage(reports);
  expect(second.call.query.cursor).toBe(first.response.nextCursor);
  await expectRows(page, [...first.response.items, ...second.response.items]);
  const before = reports.calls("GET", exportsPath).length;
  await page.waitForTimeout(150);
  expect(reports.calls("GET", exportsPath), "Report export history must not auto-drain its third page.").toHaveLength(before);
  await loadMore(page).click();
  await expect.poll(() => reports.exportPages.length).toBe(3);
  const third = latestExportPage(reports);
  expect(third.response.items).toHaveLength(1);
  expect(third.response.nextCursor).toBeNull();
  await expectRows(page, [...first.response.items, ...second.response.items, ...third.response.items]);
});

test("A lost report export acknowledgement keeps one unresolved key until explicit history reconciliation", async ({ page, reports }) => {
  await page.clock.install();
  reports.seedExportHistory(reportAlpha.id, 0);
  await reportsPage(page);
  await openExports(page, reports);
  const lost = reports.loseNextExportAcknowledgement(reportAlpha.id, true);
  const submit = submitExport(page, reports, firstSnapshot.id, "json");
  await lost.requested;
  const first = lost.call!;
  expect(first.body).toMatchObject({
    snapshotId: firstSnapshot.id, format: "json", idempotencyKey: expect.any(String),
  });
  const alphaExports = () => [...reports.exports.values()].filter((item) => item.workspaceId === reportAlpha.id);
  expect(alphaExports()).toHaveLength(1);
  await release([lost], true);
  await submit;
  await expect(section(page).getByRole("alert")).toContainText(/unresolved|not confirmed|acknowledg/i);
  const before = reports.requests.length;
  await page.clock.fastForward(10_000);
  await frame(page);
  expect(reports.requests, "An ambiguous export acknowledgement must not generate a blind new key or automatic replay.").toHaveLength(before);
  expect(reports.calls("POST", exportsPath)).toHaveLength(1);

  await refreshHistory(page).click();
  await expect(rows(page)).toHaveCount(1);
  const committed = alphaExports()[0];
  await expect(table(page).getByText(committed.id, { exact: true })).toBeVisible();
  expect(reports.calls("POST", exportsPath)).toHaveLength(1);
  await expect(section(page).getByRole("alert")).toHaveCount(0);

  const second = await submitExport(page, reports, firstSnapshot.id, "csv");
  expect(second.body.idempotencyKey).not.toBe(first.body.idempotencyKey);
  expect(reports.calls("POST", exportsPath)).toHaveLength(2);
});

test("Report export authority, scope and late responses fail closed while transient reads retain authorized rows", async ({ page, reports }) => {
  await reportsPage(page);
  await openExports(page, reports);
  const alphaPage = latestExportPage(reports);
  await expectRows(page, alphaPage.response.items);
  await openExport(page, firstExport);

  for (const denial of [403, 404] as const) {
    const denied = reports.queueExport(firstExport.id, { status: denial });
    await refreshDetail(page).click();
    await denied.delivered;
    await expect(detail(page).getByRole("alert")).toContainText(
      denial === 403 ? "Synthetic report access denied." : /not found/i);
    await expect(detail(page).getByText(firstExport.id, { exact: true })).toHaveCount(0);
    const transient = reports.queueExport(firstExport.id, { status: 503 });
    await refreshDetail(page).click();
    await transient.delivered;
    await expect(detail(page).getByText(firstExport.id, { exact: true })).toHaveCount(0);
    const recovery = reports.queueExport(firstExport.id, { status: 200, value: firstExport });
    await refreshDetail(page).click();
    await recovery.delivered;
    await expect(detail(page).getByText(firstExport.id, { exact: true })).toBeVisible();
  }

  const unavailable = reports.queueExportHistory(reportAlpha.id, { status: 503 });
  await refreshHistory(page).click();
  await unavailable.delivered;
  await expect(section(page).getByRole("alert")).toContainText("Synthetic report service unavailable.");
  await expectRows(page, alphaPage.response.items);

  const oldHistory = reports.queueExportHistory(reportAlpha.id,
    { status: 200, value: reports.exportPage(reportAlpha.id) }, true);
  const oldDetail = reports.queueExport(firstExport.id, { status: 200, value: firstExport }, true);
  await refreshHistory(page).click();
  await oldHistory.requested;
  await refreshDetail(page).click();
  await oldDetail.requested;
  const betaHistory = reports.queueExportHistory(reportBeta.id,
    { status: 200, value: reports.exportPage(reportBeta.id) }, true);
  await page.getByRole("combobox", { name: "Workspace", exact: true }).selectOption(reportBeta.id);
  await betaHistory.requested;
  await expect(page.locator("body")).not.toContainText(firstExport.id);
  await release([oldHistory, oldDetail], true);
  await expect(page.locator("body")).not.toContainText(firstExport.id);
  await release([betaHistory]);
  const beta = reports.exportPage(reportBeta.id);
  await expectRows(page, beta.items);

  reports.authenticated = false;
  await refreshHistory(page).click();
  await expect(page.getByRole("form", { name: "Sign in", exact: true })).toBeVisible();
  await expect(section(page)).toHaveCount(0);
  for (const item of [...alphaPage.response.items, ...beta.items]) {
    await expect(page.locator("body")).not.toContainText(item.id);
  }
  await signIn(page);
  await openExports(page, reports);
  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  await expect(page.getByRole("form", { name: "Sign in", exact: true })).toBeVisible();
  await expect(section(page)).toHaveCount(0);
});

test("Strict export envelopes and content integrity reject malformed replacements at 390px without storage or focus loss", async ({ page, reports }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  reports.seedExportHistory(reportAlpha.id, 101);
  await reportsPage(page);
  await openExports(page, reports);
  const original = latestExportPage(reports);
  await expectRows(page, original.response.items);

  const malformedPage: ReportExportPage & { extra: boolean } = {
    ...reports.exportPage(reportAlpha.id), extra: true,
  };
  const invalidHistory = reports.queueExportHistoryRaw(reportAlpha.id, malformedPage, true);
  await refreshHistory(page).click();
  await invalidHistory.requested;
  await expectRows(page, original.response.items);
  await release([invalidHistory]);
  await expect(section(page).getByRole("alert")).toContainText(/invalid|unexpected|malformed/i);
  await expectRows(page, original.response.items);

  await openExport(page, firstExport);
  const malformedDetail = reports.queueExportRaw(firstExport.id, {
    apiVersion: "aspm/v1alpha1", dataOrigin: "live",
    export: { ...firstExport, state: "succeeded", digest: null },
  }, true);
  await refreshDetail(page).click();
  await malformedDetail.requested;
  await release([malformedDetail]);
  await expect(detail(page).getByRole("alert")).toContainText(/invalid|digest|metadata/i);
  await expect(detail(page).getByText(firstExport.id, { exact: true })).toBeVisible();

  const artifact = reportExportArtifact(savedSnapshots(reportAlpha.id, 2)[0], firstExport.format);
  const successful = await downloadBytes(page);
  expect(successful.body.equals(artifact.body)).toBe(true);
  let downloads = 0;
  page.on("download", () => { downloads += 1; });
  const faults: ReportExportContentReply[] = [
    { headers: { "Content-Type": "application/octet-stream" } },
    { headers: { "X-ASPM-Content-Digest": "sha256:" + "0".repeat(64) } },
    { headers: { "X-ASPM-Content-Length": String(artifact.sizeBytes + 1) } },
    { headers: { "Content-Disposition": `attachment; filename="../unsafe.${firstExport.format}"` } },
    { body: Buffer.from("tampered synthetic export bytes", "utf8") },
  ];
  for (const fault of faults) {
    const control = reports.queueExportContent(firstExport.id, fault, true);
    await download(page).click();
    await control.requested;
    await release([control]);
    await expect(detail(page).getByRole("alert")).toContainText(/invalid|integrity|header|content|download/i);
    expect(downloads, "Malformed report export bytes or headers must not create a browser download.").toBe(0);
  }

  const malformedAck = reports.queueExportCreateRaw(reportAlpha.id, 202, {
    apiVersion: "aspm/v1alpha1", dataOrigin: "live",
    export: { ...firstExport, id: "c".repeat(32), state: "queued" },
  }, true);
  const malformedSubmission = submitExport(page, reports, firstSnapshot.id, "json");
  await malformedAck.requested;
  await release([malformedAck]);
  await malformedSubmission;
  await expect(section(page).getByRole("alert")).toContainText(/invalid|metadata|acknowledg/i);
  await expect(table(page).getByText("c".repeat(32), { exact: true })).toHaveCount(0);

  const held = reports.queueExportHistory(reportAlpha.id,
    { status: 200, value: reports.exportPage(reportAlpha.id) }, true);
  await refreshHistory(page).scrollIntoViewIfNeeded();
  await refreshHistory(page).focus();
  await refreshHistory(page).click();
  await held.requested;
  const workspace = page.getByRole("combobox", { name: "Workspace", exact: true });
  await workspace.focus();
  await release([held]);
  await expect(workspace).toBeFocused();
  await loadMore(page).scrollIntoViewIfNeeded();
  await loadMore(page).focus();
  await expect(loadMore(page)).toBeFocused();
  await expect(loadMore(page)).toBeInViewport();
  await reducedMovement(page);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1),
    "Report export controls must not create page-level overflow at 390px.").toBe(true);
  const stored = await page.evaluate(async () => JSON.stringify({
    local: Object.fromEntries(Object.entries(localStorage)),
    session: Object.fromEntries(Object.entries(sessionStorage)),
    cookie: document.cookie,
    indexedDB: (await indexedDB.databases()).map((entry) => entry.name ?? ""),
  }));
  for (const value of [
    firstExport.id, firstExport.digest!, artifact.body.toString("utf8"),
    ...reports.exportIdempotencyKeys,
  ]) expect(stored).not.toContain(value);
});
