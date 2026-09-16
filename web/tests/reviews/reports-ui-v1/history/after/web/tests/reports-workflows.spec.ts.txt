import type { Locator, Page } from "@playwright/test";
import { password } from "./application-fixture";
import { workItems } from "./fixtures";
import { requireProductionUI } from "./network";
import {
  alphaOverview, betaOverview, emptyReport, overviewPath, refreshedOverview, reportAlpha, reportBeta,
  reportUser, savedReport, savedSnapshots, snapshotsPath, verificationReason, withFreshness,
} from "./reports-data";
import type { SyntheticPostureReport, SyntheticSnapshot } from "./reports-data";
import { expect, test } from "./reports-fixture";
import type { ReportResponseControl, ReportsAPI } from "./reports-fixture";

test.use({ reducedMotion: "reduce" });
test.beforeEach(async ({ reports }) => {
  requireProductionUI();
  expect(reports.requests).toEqual([]);
});

const totalLabels = {
  assets: "Assets", findings: "Findings", openFindings: "Open findings", acceptedRisk: "Accepted risk",
  expiredAcceptedRisk: "Expired accepted risk", inferredResolved: "Inferred resolved", verifiedResolved: "Verified resolved",
} as const;
const severityLabels = { critical: "Critical", high: "High", medium: "Medium", low: "Low", info: "Info" } as const;
const coverageLabels = {
  scannedAssets: "Scanned assets", unscannedAssets: "Unscanned assets",
  staleAssets: "Stale assets", unknownFreshnessAssets: "Unknown freshness",
} as const;
const allMetricLabels = [...Object.values(totalLabels), ...Object.values(severityLabels), ...Object.values(coverageLabels)];
const firstSaved = savedSnapshots(reportAlpha.id)[0];
const firstBetaSaved = savedSnapshots(reportBeta.id)[0];

function live(page: Page) { return page.getByRole("region", { name: "Live overview", exact: true, includeHidden: true }); }
function selected(page: Page) { return page.getByRole("region", { name: "Selected snapshot", exact: true, includeHidden: true }); }
function history(page: Page) { return page.getByRole("region", { name: "Saved snapshots", exact: true, includeHidden: true }); }
function historyTable(page: Page) { return history(page).getByRole("table", { name: "Snapshots", exact: true, includeHidden: true }); }
function historyRows(page: Page) { return historyTable(page).getByRole("row").filter({ has: page.getByRole("cell") }); }
function createAction(page: Page) { return page.getByRole("main").getByRole("button", { name: "Create snapshot", exact: true }); }
function freshness(page: Page) { return live(page).getByRole("spinbutton", { name: "Freshness days", exact: true }); }
function refreshReport(page: Page) { return live(page).getByRole("button", { name: "Refresh report", exact: true }); }
function refreshSnapshot(page: Page) { return selected(page).getByRole("button", { name: "Refresh snapshot", exact: true }); }
function loadMore(page: Page) { return history(page).getByRole("button", { name: "Load more snapshots", exact: true }); }
function snapshotStatus(page: Page) {
  return selected(page).getByRole("status", { name: "Snapshot status", exact: true })
    .or(selected(page).getByRole("alert", { name: "Snapshot status", exact: true }));
}
function literal(value: string) { return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&"); }

function metricValue(region: Locator, label: string) {
  return region.getByRole("term", { includeHidden: true })
    .filter({ hasText: new RegExp(`^${literal(label)}$`, "i") }).locator("xpath=following-sibling::dd[1]");
}

async function exactMetric(region: Locator, label: string, value: number) {
  const definition = metricValue(region, label);
  await expect(definition, `${label} must have one associated, accessible definition.`).toHaveCount(1);
  await expect(definition).toBeVisible();
  await expect(definition, `${label} must map the server count, not a derived or fabricated replacement.`)
    .toHaveText(new RegExp(`^\\s*(?:${value}|${literal(value.toLocaleString("en-US"))})\\s*$`));
}

async function noMetricNumbers(region: Locator) {
  for (const label of allMetricLabels) {
    await expect(metricValue(region, label).filter({ hasText: /\d/ }),
      `Unavailable ${label} must not retain old counts, hidden counts or manufactured zeroes.`).toHaveCount(0);
  }
}

async function exactReport(region: Locator, report: SyntheticPostureReport) {
  await expect(region).toBeVisible();
  for (const key of Object.keys(totalLabels) as Array<keyof typeof totalLabels>) await exactMetric(region, totalLabels[key], report.totals[key]);
  for (const key of Object.keys(severityLabels) as Array<keyof typeof severityLabels>) await exactMetric(region, severityLabels[key], report.bySeverity[key]);
  for (const key of Object.keys(coverageLabels) as Array<keyof typeof coverageLabels>) await exactMetric(region, coverageLabels[key], report.coverage[key]);
  await expect(region).toContainText(/as of/i);
  for (const stamp of [report.asOf, report.freshnessWindow.from, report.freshnessWindow.to]) {
    await expect(region.locator(`time[datetime="${stamp}"]`).first(), "Server as-of and freshness bounds must survive, not become browser 'now'.").toBeVisible();
  }
  await expect(region).toContainText(new RegExp(`\\b${report.freshnessWindow.days} days?\\b`, "i"));
  await expect(region).toContainText(verificationReason);
  await expect(region).toContainText(/verification (?:not run|not-run)|not independently verified/i);
  await expect(region.getByText(/^(?:verified|resolved and verified|all findings verified)$/i)).toHaveCount(0);
}

async function reportsPage(page: Page) {
  await page.goto("/#/reports");
  await expect(page.getByRole("main").getByRole("heading", { name: "Reports", exact: true })).toBeVisible();
}

async function renderBoundary(page: Page) {
  await page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))));
}

async function applyFreshness(page: Page, reports: ReportsAPI, days: number) {
  const before = reports.calls("GET", overviewPath).length;
  await freshness(page).fill(String(days));
  await renderBoundary(page);
  expect(reports.calls("GET", overviewPath), "Editing a window is not an implicit report refresh.").toHaveLength(before);
  await refreshReport(page).click();
  await expect.poll(() => reports.calls("GET", overviewPath).length).toBe(before + 1);
  expect(reports.calls("GET", overviewPath).at(-1)).toMatchObject({ workspace: reportAlpha.id, query: { freshnessDays: String(days) } });
}

async function openCreation(page: Page) {
  await expect(createAction(page)).toBeVisible();
  await createAction(page).click();
  const dialog = page.getByRole("dialog", { name: "Create snapshot", exact: true });
  await expect(dialog).toBeVisible();
  return dialog.getByRole("form", { name: "Create snapshot", exact: true });
}

async function createSnapshot(page: Page, reports: ReportsAPI, name: string, days: number) {
  const form = await openCreation(page);
  await form.getByLabel("Snapshot name", { exact: true }).fill(name);
  await expect(form.getByRole("spinbutton", { name: "Freshness days", exact: true })).toHaveValue(String(days));
  await form.getByRole("button", { name: "Create snapshot", exact: true }).click();
  await expect(form).toHaveCount(0);
  const snapshot = [...reports.snapshots.values()].find((item) => item.name === name);
  expect(snapshot, "Use the returned 202 receipt, not a browser-generated ID or success state.").toBeDefined();
  if (!snapshot) throw new Error("No acknowledged synthetic snapshot.");
  expect(reports.calls("POST", snapshotsPath).at(-1)).toMatchObject({ workspace: reportAlpha.id, body: { name, freshnessDays: days } });
  expect(Object.keys(reports.calls("POST", snapshotsPath).at(-1)!.body).sort()).toEqual(["freshnessDays", "name"]);
  await expect(selected(page).getByText(snapshot.id, { exact: true })).toBeVisible();
  return snapshot.id;
}

async function selectSaved(page: Page, snapshot = firstSaved) {
  const row = historyTable(page).getByRole("row").filter({ hasText: snapshot.name });
  const action = row.getByRole("button", { name: "Open snapshot", exact: true })
    .or(row.getByRole("link", { name: "Open snapshot", exact: true }));
  await expect(action).toBeVisible();
  await action.click();
  await expect(selected(page).getByText(snapshot.name, { exact: true })).toBeVisible();
  await expect(selected(page).getByText(snapshot.id, { exact: true })).toBeVisible();
}

async function historyMatches(page: Page, names: string[]) {
  await expect(historyRows(page)).toHaveCount(names.length);
  const rows = await historyRows(page).allTextContents();
  for (const name of names) expect(rows.filter((row) => row.includes(name)), "Each returned snapshot has exactly one history row.").toHaveLength(1);
}

function lastHistory(reports: ReportsAPI, workspace = reportAlpha.id) {
  const value = reports.pages.filter((entry) => entry.call.workspace === workspace).at(-1);
  if (!value) throw new Error("Expected an actual completed synthetic history request.");
  return value;
}

async function releaseAndObserve(page: Page, controls: ReportResponseControl[], requireAbort = false) {
  for (const control of controls) control.release();
  await Promise.all(controls.map((control) => control.delivered));
  await renderBoundary(page);
  if (requireAbort) for (const control of controls) {
    await expect.poll(() => control.call?.failure, "Scope/session invalidation must abort the old protected request, not merely cover its contents.").toMatch(/abort/i);
  }
}

async function oldSnapshotHidden(page: Page, snapshot: SyntheticSnapshot) {
  await noMetricNumbers(selected(page));
  for (const value of [snapshot.id, snapshot.name]) await expect(selected(page).getByText(value, { exact: true })).toHaveCount(0);
  await expect(selected(page).getByText(/^(?:succeeded|completed|queued|processing)$/i)).toHaveCount(0);
  if (snapshot.report) await expect(selected(page).locator(`time[datetime="${snapshot.report.asOf}"]`)).toHaveCount(0);
}

async function protectedReportsGone(page: Page) {
  for (const region of [live(page), selected(page), history(page)]) await expect(region).toHaveCount(0);
  await expect(page.getByRole("combobox", { name: "Workspace", exact: true, includeHidden: true })).toHaveCount(0);
  await expect(page.getByRole("dialog", { name: "Create snapshot", includeHidden: true })).toHaveCount(0);
  for (const value of [firstSaved.name, firstSaved.id, firstBetaSaved.name, reportAlpha.name, reportBeta.name]) {
    await expect(page.locator("body")).not.toContainText(value);
  }
  for (const stamp of [alphaOverview.asOf, betaOverview.asOf, refreshedOverview.asOf, savedReport.asOf]) {
    await expect(page.locator(`time[datetime="${stamp}"]`)).toHaveCount(0);
  }
  const stored = await page.evaluate(() => JSON.stringify([...Object.entries(localStorage), ...Object.entries(sessionStorage)]));
  for (const value of [firstSaved.name, firstSaved.id, alphaOverview.asOf, savedReport.asOf]) expect(stored).not.toContain(value);
}

async function signIn(page: Page) {
  const form = page.getByRole("form", { name: "Sign in", exact: true });
  await form.getByLabel("Email", { exact: true }).fill(reportUser.email);
  await form.getByLabel("Password", { exact: true }).fill(password);
  await form.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(form).toHaveCount(0);
}

test("Reports maps exact live totals, severity, coverage and as-of with explicit bounded freshness refresh", async ({ page, reports }) => {
  await reportsPage(page);
  await expect.poll(() => reports.calls("GET", overviewPath).length, "Reports must request the existing overview API, not retain the foundation placeholder.").toBe(1);
  expect(reports.requests[0].path).toBe("/api/v1/session");
  const initial = reports.calls("GET", overviewPath)[0];
  expect(initial.workspace).toBe(reportAlpha.id);
  expect(Number(initial.query.freshnessDays ?? "7")).toBe(7);
  await expect(freshness(page)).toHaveValue("7");
  await expect(freshness(page)).toHaveAttribute("min", "1");
  await expect(freshness(page)).toHaveAttribute("max", "365");
  await exactReport(live(page), alphaOverview);
  await expect(live(page)).toContainText(/synthetic/i);

  for (const days of [1, 365]) {
    await test.step(`Refresh a valid ${days}-day window`, async () => {
      await applyFreshness(page, reports, days);
      await exactReport(live(page), withFreshness(alphaOverview, days));
    });
  }
  const reads = reports.calls("GET", overviewPath).length;
  for (const invalid of ["0", "366", "1.5", ""]) {
    await freshness(page).fill(invalid);
    if (await refreshReport(page).isEnabled()) await refreshReport(page).click();
    await renderBoundary(page);
    expect(reports.calls("GET", overviewPath), `Invalid freshness '${invalid}' must not reach the API or silently clamp.`).toHaveLength(reads);
    expect(await freshness(page).evaluate((element: HTMLInputElement) => element.checkValidity()),
      "The labelled freshness field must expose native invalidity, not accept a malformed window.").toBe(false);
  }
  reports.overviews.set(reportAlpha.id, refreshedOverview);
  await applyFreshness(page, reports, 7);
  await exactReport(live(page), refreshedOverview);
  await expect(live(page).locator(`time[datetime="${alphaOverview.asOf}"]`)).toHaveCount(0);
  expect(reports.requests.filter((call) => call.method !== "GET")).toEqual([]);
});

test("Reports separates held loading, unavailable, denied and authorized empty data without fabricated zeroes", async ({ page, reports }) => {
  reports.seedHistory(reportAlpha.id, 0);
  reports.overviews.set(reportAlpha.id, emptyReport());
  const first = reports.queueOverview(reportAlpha.id, { status: 503 }, true);
  const saved = reports.holdHistory(reportAlpha.id);
  await reportsPage(page);
  await expect(live(page).getByRole("status")).toContainText(/loading.*report|loading.*overview/i);
  await first.requested;
  await saved.requested;
  await noMetricNumbers(live(page));
  await expect(history(page).getByRole("status")).toContainText(/loading.*snapshot/i);
  await expect(history(page).getByText(/no saved snapshots/i)).toHaveCount(0);
  await releaseAndObserve(page, [saved]);
  await expect(history(page)).toContainText(/no saved snapshots/i);
  await releaseAndObserve(page, [first]);
  await expect(live(page).getByRole("alert")).toContainText("Synthetic report service unavailable.");
  await noMetricNumbers(live(page));
  await expect(live(page).getByText(/no findings|no assets/i)).toHaveCount(0);

  const denied = reports.queueOverview(reportAlpha.id, { status: 403 });
  await refreshReport(page).click();
  await denied.delivered;
  await expect(live(page).getByRole("alert")).toContainText("Synthetic report access denied.");
  await expect(live(page)).toContainText(/access restricted|permission|administrator/i);
  await noMetricNumbers(live(page));
  const authorized = reports.queueOverview(reportAlpha.id, { status: 200, value: emptyReport() }, true);
  await refreshReport(page).click();
  await authorized.requested;
  await noMetricNumbers(live(page));
  await releaseAndObserve(page, [authorized]);
  await exactReport(live(page), emptyReport());
  await expect(live(page)).toContainText(/no findings|no assets|no report data/i);
  await expect(live(page).getByRole("alert")).toHaveCount(0);

  reports.queueHistory(reportAlpha.id, { status: 503 });
  await page.reload();
  await exactReport(live(page), emptyReport());
  await expect(history(page).getByRole("alert")).toContainText("Synthetic report service unavailable.");
  await expect(history(page).getByText(/no saved snapshots/i)).toHaveCount(0);
});

test("Snapshot creation acknowledges queued work and renders only actual worker states, diagnostics and completion", async ({ page, reports }) => {
  await reportsPage(page);
  await expect(createAction(page)).toBeVisible();
  await exactReport(live(page), alphaOverview);
  await applyFreshness(page, reports, 30);
  const id = await createSnapshot(page, reports, "Synthetic worker-failure snapshot", 30);
  await expect(snapshotStatus(page)).toContainText(/queued/i);
  await expect(snapshotStatus(page)).not.toContainText(/succeeded|completed|generated successfully/i);
  await noMetricNumbers(selected(page));
  await expect(selected(page).locator(`time[datetime="${savedReport.asOf}"]`)).toHaveCount(0);

  for (const state of ["processing", "queued", "failed"] as const) {
    await test.step(`Server reports ${state}`, async () => {
      reports.setSnapshotState(id, state);
      await refreshSnapshot(page).click();
      await expect(snapshotStatus(page)).toContainText(new RegExp(state, "i"));
      await noMetricNumbers(selected(page));
      if (state !== "processing") {
        await expect(selected(page)).toContainText("Synthetic snapshot generation could not be committed.");
        await expect(selected(page)).toContainText("report-generation-failed");
      }
      if (state === "queued") await expect(selected(page)).toContainText(/retry|queued/i);
      await expect(selected(page).getByText(/^(?:succeeded|completed)$/i)).toHaveCount(0);
    });
  }
  const completedId = await createSnapshot(page, reports, "Synthetic completed worker snapshot", 30);
  await expect(snapshotStatus(page)).toContainText(/queued/i);
  reports.setSnapshotState(completedId, "succeeded");
  const completed = structuredClone(reports.snapshots.get(completedId)!);
  await refreshSnapshot(page).click();
  await expect(snapshotStatus(page)).toContainText(/succeeded|completed/i);
  expect(completed.report).not.toBeNull();
  await exactReport(selected(page), completed.report!);
  await expect(selected(page).locator(`time[datetime="${completed.completedAt}"]`).first()).toBeVisible();
  await expect(selected(page)).toContainText(/saved|immutable/i);
  await expect(selected(page)).not.toContainText("Synthetic snapshot generation could not be committed.");
  reports.overviews.set(reportAlpha.id, refreshedOverview);
  await refreshReport(page).click();
  await exactReport(live(page), withFreshness(refreshedOverview, 30));
  await exactReport(selected(page), completed.report!);
  expect(reports.snapshots.get(completedId), "The synthetic worker, like the API contract, cannot update a completed snapshot.").toEqual(completed);
  expect(reports.calls("POST", snapshotsPath)).toHaveLength(2);
  expect(reports.calls("GET", `${snapshotsPath}/${id}`).length).toBeGreaterThanOrEqual(3);
});

test("Saved snapshots use real cursor Load more, preserve rows on errors and reset pagination on scope change", async ({ page, reports }) => {
  reports.seedHistory(reportAlpha.id, 501);
  await reportsPage(page);
  await expect(historyTable(page)).toBeVisible();
  const first = lastHistory(reports);
  const limit = Number(first.call.query.limit ?? "100");
  expect(first.call.query.cursor ?? "").toBe("");
  expect(limit).toBeGreaterThanOrEqual(1);
  expect(limit).toBeLessThanOrEqual(500);
  expect(first.response.items).toHaveLength(limit);
  expect(first.response.total).toBe(501);
  expect(first.response.nextCursor).toBe(first.response.items.at(-1)!.id);
  await historyMatches(page, first.response.items.map((item) => item.name));
  await expect(history(page)).toContainText(/\b501\b/);

  const unavailable = reports.queueHistory(reportAlpha.id, { status: 503 }, true);
  await loadMore(page).click();
  await unavailable.requested;
  expect(unavailable.call!.query.cursor).toBe(first.response.nextCursor);
  await expect(loadMore(page)).toBeDisabled();
  await historyMatches(page, first.response.items.map((item) => item.name));
  await releaseAndObserve(page, [unavailable]);
  await expect(history(page).getByRole("alert")).toContainText("Synthetic report service unavailable.");
  await historyMatches(page, first.response.items.map((item) => item.name));
  await loadMore(page).click();
  await expect.poll(() => reports.pages.filter((entry) => entry.call.workspace === reportAlpha.id).length).toBe(2);
  const second = lastHistory(reports);
  expect(second.call.query.cursor).toBe(first.response.nextCursor);
  expect(Number(second.call.query.limit ?? "100")).toBe(limit);
  await historyMatches(page, [...first.response.items, ...second.response.items].map((item) => item.name));
  expect(new Set([...first.response.items, ...second.response.items].map((item) => item.id)).size)
    .toBe(first.response.items.length + second.response.items.length);
  await expect(history(page).getByRole("alert")).toHaveCount(0);

  const beta = reports.holdHistory(reportBeta.id);
  await page.getByRole("combobox", { name: "Workspace", exact: true }).selectOption(reportBeta.id);
  await beta.requested;
  expect(beta.call!.query.cursor ?? "").toBe("");
  await expect(page.getByText(firstSaved.name, { exact: true })).toHaveCount(0);
  await releaseAndObserve(page, [beta]);
  const betaPage = lastHistory(reports, reportBeta.id);
  await historyMatches(page, betaPage.response.items.map((item) => item.name));
  if (betaPage.response.nextCursor === null) {
    if (await loadMore(page).count()) await expect(loadMore(page)).toBeDisabled();
    else await expect(loadMore(page)).toHaveCount(0);
  }
  const alpha = reports.holdHistory(reportAlpha.id);
  await page.getByRole("combobox", { name: "Workspace", exact: true }).selectOption(reportAlpha.id);
  await alpha.requested;
  expect(alpha.call!.query.cursor ?? "").toBe("");
  await expect(page.getByText(firstBetaSaved.name, { exact: true })).toHaveCount(0);
  await releaseAndObserve(page, [alpha]);
  await historyMatches(page, lastHistory(reports).response.items.map((item) => item.name));
});

test("Viewer reports are read-only and stale writer controls cannot override a server creation denial", async ({ page, reports }) => {
  reports.roles.set(reportAlpha.id, "viewer");
  reports.serverRoles.set(reportAlpha.id, "viewer");
  await reportsPage(page);
  await exactReport(live(page), alphaOverview);
  await selectSaved(page);
  await exactReport(selected(page), savedReport);
  const viewerCreate = page.getByRole("button", { name: "Create snapshot", exact: true, includeHidden: true });
  if (await viewerCreate.count()) await expect(viewerCreate).toBeDisabled();
  else await expect(viewerCreate).toHaveCount(0);
  await expect(page.getByRole("form", { name: "Create snapshot", includeHidden: true })).toHaveCount(0);
  expect(reports.calls("POST", snapshotsPath)).toEqual([]);

  reports.roles.set(reportAlpha.id, "analyst");
  await page.reload();
  const form = await openCreation(page);
  const name = "Synthetic server-authority snapshot";
  await form.getByLabel("Snapshot name", { exact: true }).fill(name);
  await form.getByRole("button", { name: "Create snapshot", exact: true }).click();
  await expect(page.getByRole("dialog", { name: "Create snapshot", exact: true }).getByRole("alert"))
    .toContainText("Synthetic report access denied.");
  await expect(form).toBeVisible();
  await expect(form.getByLabel("Snapshot name", { exact: true })).toHaveValue(name);
  expect(reports.calls("POST", snapshotsPath)).toHaveLength(1);
  expect(reports.calls("POST", snapshotsPath)[0].body).toEqual({ name, freshnessDays: 7 });
  expect([...reports.snapshots.values()].filter((item) => item.name === name)).toHaveLength(0);
  await expect(snapshotStatus(page)).toHaveCount(0);
  reports.serverRoles.set(reportAlpha.id, "analyst");
  await form.getByRole("button", { name: "Create snapshot", exact: true }).click();
  await expect(form).toHaveCount(0);
  await expect(snapshotStatus(page)).toContainText(/queued/i);
  expect(reports.calls("POST", snapshotsPath)).toHaveLength(2);
  await noMetricNumbers(selected(page));
});

test("Workspace switch aborts held current, history and selected reads before any old report can reappear", async ({ page, reports }) => {
  reports.seedHistory(reportAlpha.id, 501);
  await reportsPage(page);
  await exactReport(live(page), alphaOverview);
  await selectSaved(page);
  await exactReport(selected(page), savedReport);
  const oldOverview = reports.queueOverview(reportAlpha.id, { status: 200, value: refreshedOverview }, true);
  const oldHistory = reports.holdHistory(reportAlpha.id);
  const oldSelected = reports.queueSnapshot(firstSaved.id, { status: 200, value: firstSaved }, true);
  await refreshReport(page).click();
  await oldOverview.requested;
  await loadMore(page).click();
  await oldHistory.requested;
  await refreshSnapshot(page).click();
  await oldSelected.requested;
  const nextOverview = reports.queueOverview(reportBeta.id, { status: 200, value: betaOverview }, true);
  const nextHistory = reports.holdHistory(reportBeta.id);
  const boundary = reports.requests.length;
  await page.getByRole("combobox", { name: "Workspace", exact: true }).selectOption(reportBeta.id);
  await nextOverview.requested;
  await nextHistory.requested;
  await noMetricNumbers(live(page));
  await expect(page.getByText(firstSaved.name, { exact: true })).toHaveCount(0);
  await expect(page.locator(`time[datetime="${alphaOverview.asOf}"], time[datetime="${savedReport.asOf}"]`)).toHaveCount(0);
  await oldSnapshotHidden(page, firstSaved);
  await releaseAndObserve(page, [oldOverview, oldHistory, oldSelected], true);
  await noMetricNumbers(live(page));
  await oldSnapshotHidden(page, firstSaved);
  await expect(page.getByText(firstSaved.name, { exact: true })).toHaveCount(0);
  await releaseAndObserve(page, [nextOverview, nextHistory]);
  await exactReport(live(page), betaOverview);
  await expect(historyTable(page).getByText(firstBetaSaved.name, { exact: true })).toBeVisible();
  await oldSnapshotHidden(page, firstSaved);
  expect(nextHistory.call!.query.cursor ?? "").toBe("");
  expect(reports.requests.slice(boundary).every((call) => call.workspace === reportBeta.id)).toBe(true);
  expect(reports.requests.filter((call) => call.method !== "GET")).toEqual([]);
});

test("Logout and protected 401 clear live, saved and selected report context and reject late old responses", async ({ page, reports }) => {
  await reportsPage(page);
  for (const reason of ["logout", "protected 401"] as const) {
    await test.step(reason, async () => {
      reports.seedHistory(reportAlpha.id, 501);
      reports.overviews.set(reportAlpha.id, alphaOverview);
      if (reason === "logout") await page.reload();
      else await signIn(page);
      await exactReport(live(page), alphaOverview);
      await selectSaved(page);
      await exactReport(selected(page), savedReport);
      const oldOverview = reports.queueOverview(reportAlpha.id, { status: 200, value: refreshedOverview }, true);
      const oldHistory = reports.holdHistory(reportAlpha.id);
      await refreshReport(page).click();
      await oldOverview.requested;
      await loadMore(page).click();
      await oldHistory.requested;
      const held = [oldOverview, oldHistory];
      if (reason === "logout") {
        const oldSelected = reports.queueSnapshot(firstSaved.id, { status: 200, value: firstSaved }, true);
        held.push(oldSelected);
        await refreshSnapshot(page).click();
        await oldSelected.requested;
        await page.getByRole("button", { name: "Sign out", exact: true }).click();
      } else {
        reports.queueSnapshot(firstSaved.id, { status: 401 });
        await refreshSnapshot(page).click();
      }
      await expect(page.getByRole("form", { name: "Sign in", exact: true })).toBeVisible();
      await protectedReportsGone(page);
      await releaseAndObserve(page, held, true);
      await protectedReportsGone(page);
      await page.reload();
      await expect(page.getByRole("form", { name: "Sign in", exact: true })).toBeVisible();
      await protectedReportsGone(page);
    });
  }
  reports.seedHistory(reportAlpha.id, 0);
  reports.overviews.set(reportAlpha.id, emptyReport());
  await signIn(page);
  await exactReport(live(page), emptyReport());
  await expect(history(page)).toContainText(/no saved snapshots/i);
  await oldSnapshotHidden(page, firstSaved);
  await expect(freshness(page)).toHaveValue("7");
  expect(reports.calls("POST", "/api/v1/logout")).toHaveLength(1);
});

test("Selected snapshot 403 and 404 remain latched through held retries and 503 until authorized detail succeeds", async ({ page, reports }) => {
  await reportsPage(page);
  await selectSaved(page);
  await exactReport(selected(page), savedReport);
  for (const status of [403, 404] as const) {
    await test.step(`Selected detail loses access with ${status}`, async () => {
      const denied = reports.queueSnapshot(firstSaved.id, { status });
      await refreshSnapshot(page).click();
      await denied.delivered;
      await expect(selected(page).getByRole("alert")).toContainText(status === 403 ? "Synthetic report access denied." : "Synthetic snapshot not found.");
      await oldSnapshotHidden(page, firstSaved);
      const retry = reports.queueSnapshot(firstSaved.id, { status: 503 }, true);
      await refreshSnapshot(page).click();
      await retry.requested;
      await expect(refreshSnapshot(page)).toBeDisabled();
      await oldSnapshotHidden(page, firstSaved);
      await releaseAndObserve(page, [retry]);
      await expect(selected(page).getByRole("alert")).toContainText("Synthetic report service unavailable.");
      await oldSnapshotHidden(page, firstSaved);
      const recovery = reports.queueSnapshot(firstSaved.id, { status: 200, value: firstSaved }, true);
      await refreshSnapshot(page).click();
      await recovery.requested;
      await oldSnapshotHidden(page, firstSaved);
      await releaseAndObserve(page, [recovery]);
      await exactReport(selected(page), savedReport);
      await expect(selected(page).getByText(firstSaved.id, { exact: true })).toBeVisible();
      await expect(selected(page).getByRole("alert")).toHaveCount(0);
      await exactMetric(live(page), "Assets", alphaOverview.totals.assets);
      await expect(historyTable(page).getByText(firstSaved.name, { exact: true })).toBeVisible();
    });
  }
  expect(reports.calls("GET", `${snapshotsPath}/${firstSaved.id}`).length).toBeGreaterThanOrEqual(7);
  expect(reports.calls("POST", snapshotsPath)).toEqual([]);
});

async function tabTo(page: Page, target: Locator, key = "Tab") {
  for (let index = 0; index < 40; index += 1) {
    if (await target.evaluate((element) => element === document.activeElement)) {
      await expect(target).toBeInViewport({ ratio: 0.99 });
      return;
    }
    await page.keyboard.press(key);
  }
  await expect(target, "The chosen Reports control must be reachable by the real keyboard.").toBeFocused();
}

async function dialogFocus(dialog: Locator) {
  expect(await dialog.evaluate((element) => {
    const active = document.activeElement;
    if (!(active instanceof HTMLElement) || !element.contains(active) || active.matches(":disabled, [inert]")) return false;
    const box = active.getBoundingClientRect();
    return box.width > 0 && box.height > 0 && box.left >= 0 && box.top >= 0 && box.right <= innerWidth && box.bottom <= innerHeight;
  }), "Dialog focus must stay inside a visible, onscreen enabled control.").toBe(true);
}

async function reducedMovement(page: Page) {
  const violations = await page.evaluate(async () => {
    const failures = new Set<string>();
    for (let index = 0; index < 6; index += 1) {
      for (const animation of document.getAnimations()) {
        if (animation.playState !== "running" || !(animation.effect instanceof KeyframeEffect)) continue;
        if (animation.effect.getTiming().iterations === Infinity) failures.add("continuous animation");
        const frames = animation.effect.getKeyframes() as Array<Record<string, unknown>>;
        for (const key of ["transform", "translate", "rotate", "scale", "left", "top"]) {
          if (new Set(frames.map((frame) => frame[key]).filter((value) => value !== undefined).map(String)).size > 1) failures.add(key);
        }
      }
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
    }
    return [...failures];
  });
  expect(violations, "Reduced motion must affect actual Reports animation, not only a CSS preference marker.").toEqual([]);
}

test("Reports dialogs and refresh preserve keyboard context without focus stealing at 390px with reduced motion", async ({ page, reports }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.goto("/#/work");
  const filter = page.getByRole("textbox", { name: "Filter findings", exact: true });
  await filter.fill(workItems[0].title);
  await page.getByRole("checkbox", { name: `Select ${workItems[0].title}`, exact: true }).check();
  await page.getByRole("navigation", { name: "Primary" }).getByRole("link", { name: "Reports", exact: true }).click();
  await expect(createAction(page)).toBeVisible();
  await exactReport(live(page), alphaOverview);
  await applyFreshness(page, reports, 30);
  await selectSaved(page);
  await exactReport(selected(page), savedReport);
  await createAction(page).scrollIntoViewIfNeeded();
  await createAction(page).focus();
  const scrollBefore = await page.evaluate(() => ({ x: scrollX, y: scrollY }));
  await page.keyboard.press("Enter");
  const dialog = page.getByRole("dialog", { name: "Create snapshot", exact: true });
  const form = dialog.getByRole("form", { name: "Create snapshot", exact: true });
  await expect(dialog).toBeVisible();
  await dialogFocus(dialog);
  await reducedMovement(page);
  await expect(form.getByRole("spinbutton", { name: "Freshness days", exact: true })).toHaveValue("30");
  for (let index = 0; index < 8; index += 1) { await page.keyboard.press("Tab"); await dialogFocus(dialog); }
  const submit = form.getByRole("button", { name: "Create snapshot", exact: true });
  await form.getByLabel("Snapshot name", { exact: true }).fill("");
  if (await submit.isEnabled()) { await tabTo(page, submit); await page.keyboard.press("Enter"); }
  await expect(form).toBeVisible();
  expect(reports.calls("POST", snapshotsPath)).toEqual([]);
  await form.getByLabel("Snapshot name", { exact: true }).fill("Synthetic cancelled keyboard draft");
  await page.keyboard.press("Escape");
  await expect(dialog).toHaveCount(0);
  await expect(createAction(page)).toBeFocused();
  expect(await page.evaluate(() => ({ x: scrollX, y: scrollY }))).toEqual(scrollBefore);
  await expect(selected(page).getByText(firstSaved.name, { exact: true })).toBeVisible();
  await expect(freshness(page)).toHaveValue("30");

  await page.keyboard.press("Enter");
  await expect(dialog).toBeVisible();
  const name = `Synthetic narrow keyboard snapshot ${"x".repeat(72)}`;
  await form.getByLabel("Snapshot name", { exact: true }).fill(name);
  await tabTo(page, submit);
  const creation = reports.holdCreation();
  await page.keyboard.press("Enter");
  await creation.requested;
  await expect(form).toBeVisible();
  await expect(submit).toBeDisabled();
  expect(creation.call!.body).toEqual({ name, freshnessDays: 30 });
  await releaseAndObserve(page, [creation]);
  await expect(dialog).toHaveCount(0);
  await expect(createAction(page)).toBeFocused();
  await expect(snapshotStatus(page)).toContainText(/queued/i);
  await expect(selected(page).getByText(name, { exact: true })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1),
    "390px Reports must wrap long saved names and controls without page-level horizontal overflow.").toBe(true);

  const held = reports.queueOverview(reportAlpha.id, { status: 200, value: refreshedOverview }, true);
  await refreshReport(page).scrollIntoViewIfNeeded();
  await refreshReport(page).focus();
  await page.keyboard.press("Enter");
  await held.requested;
  const workspace = page.getByRole("combobox", { name: "Workspace", exact: true });
  await tabTo(page, workspace, "Shift+Tab");
  const moved = await page.evaluate(() => ({ x: scrollX, y: scrollY }));
  await releaseAndObserve(page, [held]);
  await expect(workspace, "Finishing the held overview must not steal deliberately moved focus.").toBeFocused();
  expect(await page.evaluate(() => ({ x: scrollX, y: scrollY }))).toEqual(moved);
  await exactMetric(live(page), "Assets", refreshedOverview.totals.assets);
  await expect(freshness(page)).toHaveValue("30");
  await expect(selected(page).getByText(name, { exact: true })).toBeVisible();
  await reducedMovement(page);
  await page.getByRole("navigation", { name: "Primary" }).getByRole("link", { name: "Work", exact: true }).click();
  await expect(filter).toHaveValue(workItems[0].title);
  await expect(page.getByRole("checkbox", { name: `Select ${workItems[0].title}`, exact: true })).toBeChecked();
  await expect(workspace).toHaveValue(reportAlpha.id);
  expect(reports.calls("POST", snapshotsPath)).toHaveLength(1);
});
