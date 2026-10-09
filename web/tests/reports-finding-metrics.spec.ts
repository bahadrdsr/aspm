import type { Locator, Page } from "@playwright/test";
import { password } from "./application-fixture";
import { requireProductionUI } from "./network";
import {
  alphaOverview, betaOverview, findingMetricsPath, findingMetricStorageCanaries,
  findingMetricTotal, findingMetricVerificationReason, overviewPath, refreshedOverview,
  reportAlpha, reportBeta, reportUser, savedReport, savedSnapshots, snapshotsPath,
  syntheticFindingMetricDrilldown, syntheticFindingMetricResponse,
} from "./reports-data";
import type {
  FindingMetric, SyntheticFindingMetricDrilldown, SyntheticFindingMetricItem,
} from "./reports-data";
import { expect, test } from "./reports-fixture";
import type { ReportResponseControl, ReportsAPI } from "./reports-fixture";

test.use({ reducedMotion: "reduce" });
test.beforeEach(async ({ reports }) => {
  requireProductionUI();
  expect(reports.requests).toEqual([]);
});

const metricLabels: Record<FindingMetric, string> = {
  findings: "Findings",
  "open-findings": "Open findings",
  "accepted-risk": "Accepted risk",
  "expired-accepted-risk": "Expired accepted risk",
  suppressed: "Suppressed",
  "expired-suppression": "Expired suppression",
  "false-positive": "False positive",
  "inferred-resolved": "Inferred resolved",
  critical: "Critical",
  high: "High",
  medium: "Medium",
  low: "Low",
  info: "Info",
};

const metrics = Object.keys(metricLabels) as FindingMetric[];

function literal(value: string) {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

function live(page: Page) {
  return page.getByRole("region", { name: "Live overview", exact: true, includeHidden: true });
}

function selected(page: Page) {
  return page.getByRole("region", { name: "Selected snapshot", exact: true, includeHidden: true });
}

function history(page: Page) {
  return page.getByRole("region", { name: "Saved snapshots", exact: true, includeHidden: true });
}

function refreshReport(page: Page) {
  return live(page).getByRole("button", { name: "Refresh report", exact: true });
}

function metricActivation(page: Page, metric: FindingMetric, region = live(page)) {
  const label = literal(metricLabels[metric]);
  return region.getByRole("button", {
    name: new RegExp(`^(?:View )?${label}(?:\\s+[\\d,]+)?$`, "i"),
  });
}

function metricInteractive(page: Page, label: string, region = live(page)) {
  const name = new RegExp(`^(?:View )?${literal(label)}(?:\\s+[\\d,]+)?$`, "i");
  return region.getByRole("button", { name }).or(region.getByRole("link", { name }));
}

function findingPanel(page: Page) {
  return page.getByRole("region", {
    name: "Current live finding membership", exact: true, includeHidden: true,
  });
}

function refreshFindings(page: Page) {
  return findingPanel(page).getByRole("button", {
    name: /refresh (?:current )?(?:finding )?(?:metric|membership|findings)/i,
  });
}

function loadMoreFindings(page: Page) {
  return findingPanel(page).getByRole("button", {
    name: /load more (?:current )?(?:finding )?(?:membership|findings)/i,
  });
}

function findingTable(page: Page) {
  return findingPanel(page).getByRole("table", { name: /current.*finding|finding.*membership/i });
}

function findingRows(page: Page) {
  return findingTable(page).getByRole("row").filter({ has: page.getByRole("cell") });
}

async function reportsPage(page: Page, reports: ReportsAPI) {
  await page.goto("/#/reports");
  await expect(page.getByRole("main").getByRole("heading", { name: "Reports", exact: true })).toBeVisible();
  await expect.poll(() => reports.calls("GET", overviewPath).length).toBe(1);
  await expect.poll(() => reports.calls("GET", snapshotsPath).length).toBe(1);
}

async function renderBoundary(page: Page) {
  await page.evaluate(() => new Promise<void>((resolve) =>
    requestAnimationFrame(() => requestAnimationFrame(() => resolve()))));
}

async function selectSavedSnapshot(page: Page) {
  const snapshot = savedSnapshots(reportAlpha.id)[0];
  const row = history(page).getByRole("table", { name: "Snapshots", exact: true })
    .getByRole("row").filter({ hasText: snapshot.name });
  await row.getByRole("button", { name: "Open snapshot", exact: true })
    .or(row.getByRole("link", { name: "Open snapshot", exact: true })).click();
  await expect(selected(page).getByText(snapshot.name, { exact: true })).toBeVisible();
  await expect(selected(page).locator(`time[datetime="${savedReport.asOf}"]`).first()).toBeVisible();
}

async function openMetric(page: Page, reports: ReportsAPI, metric: FindingMetric) {
  const before = reports.calls("GET", findingMetricsPath).length;
  const activation = metricActivation(page, metric);
  await expect(activation, `${metricLabels[metric]} needs one accessible Live overview activation.`).toHaveCount(1);
  await activation.click();
  await expect(findingPanel(page)).toBeVisible();
  await expect.poll(() => reports.calls("GET", findingMetricsPath).length).toBe(before + 1);
}

function itemMembership(item: SyntheticFindingMetricItem, metric: FindingMetric, asOf: string) {
  switch (metric) {
    case "findings": return true;
    case "open-findings": return item.workflowState !== "resolved";
    case "accepted-risk": return item.disposition === "accepted-risk";
    case "expired-accepted-risk":
      return item.disposition === "accepted-risk" && item.acceptedRiskExpiresAt !== null &&
        Date.parse(item.acceptedRiskExpiresAt) <= Date.parse(asOf);
    case "suppressed":
    case "expired-suppression":
      return item.disposition === "suppressed";
    case "false-positive": return item.disposition === "false-positive";
    case "inferred-resolved": return item.sourceState === "inferred-resolved";
    case "critical":
    case "high":
    case "medium":
    case "low":
    case "info":
      return item.severity === metric;
  }
}

async function exactFindingMetric(page: Page, expected: SyntheticFindingMetricDrilldown,
  actionsPending = false) {
  const panel = findingPanel(page);
  await expect(panel).toBeVisible();
  await expect(panel).toContainText(/current live finding membership/i);
  await expect(panel).toContainText(metricLabels[expected.metric]);
  await expect(panel.locator(`time[datetime="${expected.asOf}"]`).first(),
    "Finding membership as-of must remain the exact displayed Live overview timestamp.").toBeVisible();
  await expect(panel).toContainText(findingMetricVerificationReason);
  await expect(panel).toContainText(new RegExp(`\\b${expected.items.length}\\s+of\\s+${expected.total}\\b`, "i"));
  if (actionsPending && expected.nextCursor !== null) await expect(loadMoreFindings(page)).toBeDisabled();

  const table = findingTable(page);
  await expect(table).toBeVisible();
  const headers = (await table.getByRole("columnheader").allTextContents())
    .map((value) => value.trim().toLowerCase());
  const indexes = {
    finding: headers.findIndex((value) => value === "finding" || value.includes("finding")),
    asset: headers.findIndex((value) => value === "asset" || value.includes("asset")),
    severity: headers.findIndex((value) => value === "severity"),
    owner: headers.findIndex((value) => value === "owner"),
    workflow: headers.findIndex((value) => value.includes("workflow")),
    disposition: headers.findIndex((value) => value === "disposition"),
    acceptedExpiry: headers.findIndex((value) => /accepted risk.*expir/.test(value)),
    riskExpired: headers.findIndex((value) => /risk acceptance.*expired|accepted risk.*expired/.test(value)),
    sourceState: headers.findIndex((value) => value.includes("source state")),
    sourceFreshness: headers.findIndex((value) => /source.*fresh/.test(value)),
  };
  for (const [name, index] of Object.entries(indexes)) {
    expect(index, `Finding membership table needs an accessible ${name} column.`).toBeGreaterThanOrEqual(0);
  }
  await expect(findingRows(page)).toHaveCount(expected.items.length);
  const rendered = await findingRows(page).evaluateAll((rows, columns) => rows.map((row) => {
    const cells = [...row.querySelectorAll("td")];
    const value = (key: keyof typeof columns) => cells[columns[key]];
    return {
      finding: value("finding")?.textContent ?? "",
      asset: value("asset")?.textContent ?? "",
      severity: value("severity")?.textContent ?? "",
      owner: value("owner")?.textContent ?? "",
      workflow: value("workflow")?.textContent ?? "",
      disposition: value("disposition")?.textContent ?? "",
      acceptedExpiry: value("acceptedExpiry")?.textContent ?? "",
      acceptedExpiryTime: value("acceptedExpiry")?.querySelector("time")?.getAttribute("datetime") ?? null,
      riskExpired: value("riskExpired")?.textContent ?? "",
      sourceState: value("sourceState")?.textContent ?? "",
      sourceFreshness: value("sourceFreshness")?.textContent ?? "",
      sourceFreshnessTime: value("sourceFreshness")?.querySelector("time")?.getAttribute("datetime") ?? null,
    };
  }), indexes);
  for (let index = 0; index < expected.items.length; index += 1) {
    const item = expected.items[index], row = rendered[index];
    expect(itemMembership(item, expected.metric, expected.asOf),
      "Synthetic expected item must satisfy the requested metric.").toBe(true);
    expect(row.finding).toContain(item.title);
    expect(row.finding).toContain(item.findingId);
    expect(row.asset).toContain(item.assetName);
    expect(row.asset).toContain(item.assetId);
    expect(row.severity).toMatch(new RegExp(item.severity, "i"));
    if (item.ownerId === null) {
      expect(item.ownerName).toBeNull();
      expect(row.owner).toMatch(/unassigned|no owner|none/i);
    } else {
      expect(item.ownerName).not.toBeNull();
      expect(row.owner).toContain(item.ownerId);
      expect(row.owner).toContain(item.ownerName!);
    }
    expect(row.workflow).toMatch(new RegExp(item.workflowState.replace("-", "[ -]"), "i"));
    expect(row.disposition).toMatch(new RegExp(item.disposition.replace("-", "[ -]"), "i"));
    if (item.acceptedRiskExpiresAt === null) {
      expect(row.acceptedExpiry).toMatch(/no expiry|not applicable|none/i);
      expect(row.acceptedExpiryTime).toBeNull();
    } else {
      expect(row.acceptedExpiryTime).toBe(item.acceptedRiskExpiresAt);
    }
    expect(row.riskExpired.trim()).toMatch(new RegExp(`^${item.riskAcceptanceExpired ? "Yes" : "No"}$`, "i"));
    expect(row.sourceState).toMatch(new RegExp(item.sourceState.replace("-", "[ -]"), "i"));
    if (item.sourceFreshnessAt === null) {
      expect(row.sourceFreshness).toMatch(/unknown|no known/i);
      expect(row.sourceFreshnessTime).toBeNull();
    } else {
      expect(row.sourceFreshnessTime).toBe(item.sourceFreshnessAt);
    }
    const expired = item.disposition === "accepted-risk" && item.acceptedRiskExpiresAt !== null &&
      Date.parse(item.acceptedRiskExpiresAt) <= Date.parse(expected.asOf);
    expect(item.riskAcceptanceExpired).toBe(expired);
  }
  if (expected.nextCursor === null) {
    if (await loadMoreFindings(page).count()) await expect(loadMoreFindings(page)).toBeDisabled();
  } else {
    await expect(loadMoreFindings(page)).toBeVisible();
    if (!actionsPending) await expect(loadMoreFindings(page)).toBeEnabled();
  }
}

async function release(control: ReportResponseControl, requireAbort = false) {
  control.release();
  await control.delivered;
  if (requireAbort) {
    await expect.poll(() => control.call?.failure,
      "Workspace or session invalidation must abort the old finding membership request.").toMatch(/abort/i);
  }
}

async function hiddenFindingMetric(page: Page, data: SyntheticFindingMetricDrilldown) {
  const panel = findingPanel(page);
  for (const item of data.items.slice(0, 4)) {
    await expect(panel.getByText(item.title, { exact: true })).toHaveCount(0);
    if (item.acceptedRiskExpiresAt) {
      await expect(panel.locator(`time[datetime="${item.acceptedRiskExpiresAt}"]`)).toHaveCount(0);
    }
    if (item.sourceFreshnessAt) {
      await expect(panel.locator(`time[datetime="${item.sourceFreshnessAt}"]`)).toHaveCount(0);
    }
  }
  if (await findingTable(page).count()) await expect(findingRows(page)).toHaveCount(0);
}

async function signIn(page: Page) {
  const form = page.getByRole("form", { name: "Sign in", exact: true });
  await form.getByLabel("Email", { exact: true }).fill(reportUser.email);
  await form.getByLabel("Password", { exact: true }).fill(password);
  await form.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(form).toHaveCount(0);
}

async function protectedFindingMetricGone(page: Page, data: SyntheticFindingMetricDrilldown) {
  await expect(findingPanel(page)).toHaveCount(0);
  for (const item of data.items.slice(0, 4)) {
    await expect(page.locator("body")).not.toContainText(item.title);
  }
}

test("Only Live finding metrics open one exact as-of-bound current membership read and never poll", async ({ page, reports }) => {
  test.setTimeout(90_000);
  reports.roles.set(reportAlpha.id, "viewer");
  reports.serverRoles.set(reportAlpha.id, "viewer");
  await page.clock.install();
  await reportsPage(page, reports);
  await selectSavedSnapshot(page);
  for (const metric of metrics) {
    await expect(metricActivation(page, metric)).toHaveCount(1);
    await expect(metricInteractive(page, metricLabels[metric], selected(page)),
      "Saved snapshot aggregates cannot claim current finding membership.").toHaveCount(0);
  }
  await expect(metricInteractive(page, "Assets")).toHaveCount(0);
  await expect(metricInteractive(page, "Verified resolved")).toHaveCount(0);
  expect(reports.calls("GET", findingMetricsPath)).toEqual([]);

  const boundary = reports.requests.length;
  await openMetric(page, reports, "findings");
  expect(reports.requests.slice(boundary)).toEqual([
    expect.objectContaining({
      method: "GET", path: findingMetricsPath, workspace: reportAlpha.id,
      query: { metric: "findings", limit: "100" }, body: {},
      reportAsOf: alphaOverview.asOf,
    }),
  ]);
  const first = syntheticFindingMetricDrilldown(alphaOverview, "findings");
  await exactFindingMetric(page, first);
  expect(first.total).toBe(alphaOverview.totals.findings);

  const reads = reports.calls("GET", findingMetricsPath).length;
  await page.clock.fastForward(300_000);
  await renderBoundary(page);
  expect(reports.calls("GET", findingMetricsPath), "Current finding membership must not poll.").toHaveLength(reads);
  expect(reports.requests.filter((call) => call.method !== "GET")).toEqual([]);
});

test("Finding metric paging preserves retry context while refresh, metric replacement and Live refresh stay explicit", async ({ page, reports }) => {
  test.setTimeout(120_000);
  await reportsPage(page, reports);
  await openMetric(page, reports, "findings");
  const first = syntheticFindingMetricDrilldown(alphaOverview, "findings");
  await exactFindingMetric(page, first);
  expect(first.items).toHaveLength(100);
  expect(first.nextCursor).toBe(first.items.at(-1)!.findingId);

  const unavailable = reports.queueFindingMetric(reportAlpha.id, "findings", { status: 503 }, true);
  await loadMoreFindings(page).click();
  await unavailable.requested;
  expect(unavailable.call).toMatchObject({
    workspace: reportAlpha.id, reportAsOf: alphaOverview.asOf,
    query: { metric: "findings", limit: "100", cursor: first.nextCursor },
  });
  await exactFindingMetric(page, first, true);
  await release(unavailable);
  await expect(findingPanel(page).getByRole("alert")).toContainText(/unavailable/i);
  await exactFindingMetric(page, first);

  const beforeRetry = reports.calls("GET", findingMetricsPath).length;
  await loadMoreFindings(page).click();
  await expect.poll(() => reports.calls("GET", findingMetricsPath).length).toBe(beforeRetry + 1);
  expect(reports.calls("GET", findingMetricsPath).at(-1)!.query).toEqual(unavailable.call!.query);
  const second = syntheticFindingMetricDrilldown(alphaOverview, "findings", 100, first.nextCursor!);
  await exactFindingMetric(page, { ...second, items: [...first.items, ...second.items] });

  const refreshed = reports.queueFindingMetric(reportAlpha.id, "findings",
    { status: 200, value: first }, true);
  await refreshFindings(page).click();
  await refreshed.requested;
  expect(refreshed.call).toMatchObject({
    reportAsOf: alphaOverview.asOf, query: { metric: "findings", limit: "100" },
  });
  await release(refreshed);
  await exactFindingMetric(page, first);

  const high = syntheticFindingMetricDrilldown(alphaOverview, "high");
  const switched = reports.queueFindingMetric(reportAlpha.id, "high",
    { status: 200, value: high }, true);
  await metricActivation(page, "high").click();
  await switched.requested;
  expect(switched.call).toMatchObject({
    reportAsOf: alphaOverview.asOf, query: { metric: "high", limit: "100" },
  });
  await hiddenFindingMetric(page, first);
  await release(switched);
  await exactFindingMetric(page, high);

  const changed = structuredClone(syntheticFindingMetricResponse(alphaOverview, "high")) as any;
  changed.drilldown.total += 1;
  const invalid = reports.queueFindingMetricRaw(reportAlpha.id, "high", changed, true);
  await refreshFindings(page).click();
  await invalid.requested;
  await exactFindingMetric(page, high);
  await release(invalid);
  await expect(findingPanel(page).getByRole("alert"))
    .toContainText(/refresh.*live overview|live overview.*refresh/i);
  await exactFindingMetric(page, high);

  reports.overviews.set(reportAlpha.id, refreshedOverview);
  await refreshReport(page).click();
  await expect(live(page).locator(`time[datetime="${refreshedOverview.asOf}"]`).first()).toBeVisible();
  const current = syntheticFindingMetricDrilldown(refreshedOverview, "high");
  await metricActivation(page, "high").click();
  await expect.poll(() => reports.calls("GET", findingMetricsPath)
    .filter((call) => call.reportAsOf === refreshedOverview.asOf).length).toBe(1);
  await exactFindingMetric(page, current);
  expect(reports.calls("GET", findingMetricsPath).at(-1)).toMatchObject({
    reportAsOf: refreshedOverview.asOf, query: { metric: "high", limit: "100" },
  });
  expect(reports.requests.filter((call) => call.method !== "GET")).toEqual([]);
});

test("Finding metric authority preserves transient rows, latches denials and clears workspace or session data", async ({ page, reports }) => {
  test.setTimeout(150_000);
  await reportsPage(page, reports);
  const alpha = syntheticFindingMetricDrilldown(alphaOverview, "critical");
  await openMetric(page, reports, "critical");
  await exactFindingMetric(page, alpha);

  const unavailable = reports.queueFindingMetric(reportAlpha.id, "critical", { status: 503 }, true);
  await refreshFindings(page).click();
  await unavailable.requested;
  await exactFindingMetric(page, alpha);
  await release(unavailable);
  await expect(findingPanel(page).getByRole("alert")).toContainText(/unavailable/i);
  await exactFindingMetric(page, alpha);

  for (const status of [403, 404] as const) {
    reports.queueFindingMetric(reportAlpha.id, "critical", { status });
    await refreshFindings(page).click();
    await expect(findingPanel(page).getByRole("alert"))
      .toContainText(status === 403 ? /access denied/i : /not found/i);
    await hiddenFindingMetric(page, alpha);

    const retry = reports.queueFindingMetric(reportAlpha.id, "critical", { status: 503 }, true);
    await refreshFindings(page).click();
    await retry.requested;
    await hiddenFindingMetric(page, alpha);
    await release(retry);
    await expect(findingPanel(page).getByRole("alert")).toContainText(/unavailable/i);
    await hiddenFindingMetric(page, alpha);

    reports.queueFindingMetric(reportAlpha.id, "critical", { status: 200, value: alpha });
    await refreshFindings(page).click();
    await exactFindingMetric(page, alpha);
  }

  const oldAlpha = reports.queueFindingMetric(reportAlpha.id, "critical",
    { status: 200, value: alpha }, true);
  await refreshFindings(page).click();
  await oldAlpha.requested;
  await page.getByRole("combobox", { name: "Workspace", exact: true }).selectOption(reportBeta.id);
  await hiddenFindingMetric(page, alpha);
  await release(oldAlpha, true);
  await hiddenFindingMetric(page, alpha);
  const beta = syntheticFindingMetricDrilldown(betaOverview, "info");
  await openMetric(page, reports, "info");
  await exactFindingMetric(page, beta);

  const oldBeta = reports.queueFindingMetric(reportBeta.id, "info",
    { status: 200, value: beta }, true);
  await refreshFindings(page).click();
  await oldBeta.requested;
  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  await expect(page.getByRole("form", { name: "Sign in", exact: true })).toBeVisible();
  await protectedFindingMetricGone(page, beta);
  await release(oldBeta, true);
  await protectedFindingMetricGone(page, beta);

  await signIn(page);
  const recovered = syntheticFindingMetricDrilldown(alphaOverview, "false-positive");
  await openMetric(page, reports, "false-positive");
  await exactFindingMetric(page, recovered);
  reports.queueFindingMetric(reportAlpha.id, "false-positive", { status: 401 });
  await refreshFindings(page).click();
  await expect(page.getByRole("form", { name: "Sign in", exact: true })).toBeVisible();
  await protectedFindingMetricGone(page, recovered);
});

test("Finding metric client rejects wrong envelopes, authority, enums, membership, expiry, totals and cursors", async ({ page, reports }) => {
  test.setTimeout(180_000);
  await reportsPage(page, reports);
  await openMetric(page, reports, "high");
  const baseline = syntheticFindingMetricDrilldown(alphaOverview, "high");
  await exactFindingMetric(page, baseline);

  const scenarios: Array<[string, (body: any) => void, boolean?]> = [
    ["unknown envelope key", (body) => { body.forecast = false; }],
    ["unknown drill-down key", (body) => { body.drilldown.rate = 0.5; }],
    ["unknown item key", (body) => { body.drilldown.items[0].verified = true; }],
    ["wrong workspace", (body) => { body.drilldown.workspaceId = reportBeta.id; }],
    ["wrong metric", (body) => { body.drilldown.metric = "critical"; }],
    ["wrong as-of", (body) => {
      body.drilldown.asOf = new Date(Date.parse(body.drilldown.asOf) + 1_000).toISOString();
    }],
    ["invalid as-of", (body) => { body.drilldown.asOf = "not-a-timestamp"; }],
    ["upper-case finding ID", (body) => { body.drilldown.items[0].findingId = "A".repeat(32); }],
    ["invalid asset ID", (body) => { body.drilldown.items[0].assetId = "asset"; }],
    ["unknown severity", (body) => { body.drilldown.items[0].severity = "urgent"; }],
    ["unknown workflow", (body) => { body.drilldown.items[0].workflowState = "paused"; }],
    ["unknown disposition", (body) => { body.drilldown.items[0].disposition = "waived"; }],
    ["unknown source state", (body) => { body.drilldown.items[0].sourceState = "verified"; }],
    ["owner pair mismatch", (body) => { body.drilldown.items[0].ownerName = null; }],
    ["invalid accepted-risk expiry", (body) => { body.drilldown.items[0].acceptedRiskExpiresAt = "invalid"; }],
    ["wrong accepted-risk expiry arithmetic", (body) => {
      body.drilldown.items[0].riskAcceptanceExpired = !body.drilldown.items[0].riskAcceptanceExpired;
    }],
    ["non-accepted item retains risk expiry", (body) => {
      const item = body.drilldown.items.find((candidate: any) => candidate.disposition !== "accepted-risk");
      item.acceptedRiskExpiresAt = body.drilldown.asOf;
    }],
    ["descending finding order", (body) => { body.drilldown.items.reverse(); }],
    ["duplicate finding ID", (body) => {
      body.drilldown.items[1].findingId = body.drilldown.items[0].findingId;
    }],
    ["wrong whole-filter total", (body) => { body.drilldown.total += 1; }, true],
    ["wrong cursor", (body) => { body.drilldown.nextCursor = body.drilldown.items[0].findingId; }],
    ["truncated first page", (body) => { body.drilldown.items.pop(); }],
    ["changed verification state", (body) => { body.drilldown.verification.state = "verified"; }],
    ["changed verification wording", (body) => { body.drilldown.verification.reason += " Changed."; }],
    ["wrong API version", (body) => { body.apiVersion = "aspm/v2"; }],
    ["wrong data origin", (body) => { body.dataOrigin = "synthetic"; }],
  ];
  for (const [name, mutate, needsLiveRefresh] of scenarios) {
    await test.step(name, async () => {
      const malformed = structuredClone(syntheticFindingMetricResponse(alphaOverview, "high")) as any;
      mutate(malformed);
      const reply = reports.queueFindingMetricRaw(reportAlpha.id, "high", malformed, true);
      await refreshFindings(page).click();
      await reply.requested;
      await exactFindingMetric(page, baseline);
      await release(reply);
      await expect(findingPanel(page).getByRole("alert")).toContainText(
        needsLiveRefresh ? /refresh.*live overview|live overview.*refresh/i : /invalid|replacement data|could not/i);
      await exactFindingMetric(page, baseline);
    });
  }

  const membershipScenarios: Array<[FindingMetric, (body: any) => void]> = [
    ["open-findings", (body) => { body.drilldown.items[0].workflowState = "resolved"; }],
    ["accepted-risk", (body) => {
      body.drilldown.items[0].disposition = "none";
      body.drilldown.items[0].acceptedRiskExpiresAt = null;
      body.drilldown.items[0].riskAcceptanceExpired = false;
    }],
    ["expired-accepted-risk", (body) => {
      body.drilldown.items[0].acceptedRiskExpiresAt =
        new Date(Date.parse(body.drilldown.asOf) + 1_000).toISOString();
      body.drilldown.items[0].riskAcceptanceExpired = false;
    }],
    ["suppressed", (body) => { body.drilldown.items[0].disposition = "none"; }],
    ["expired-suppression", (body) => { body.drilldown.items[0].disposition = "none"; }],
    ["false-positive", (body) => { body.drilldown.items[0].disposition = "none"; }],
    ["inferred-resolved", (body) => { body.drilldown.items[0].sourceState = "observed"; }],
    ["critical", (body) => { body.drilldown.items[0].severity = "high"; }],
  ];
  for (const [metric, mutate] of membershipScenarios) {
    await test.step(`${metric} membership`, async () => {
      await metricActivation(page, metric).click();
      await expect.poll(() => reports.calls("GET", findingMetricsPath)
        .filter((call) => call.query.metric === metric).length).toBeGreaterThan(0);
      const valid = syntheticFindingMetricDrilldown(alphaOverview, metric);
      await exactFindingMetric(page, valid);
      const malformed = structuredClone(syntheticFindingMetricResponse(alphaOverview, metric)) as any;
      mutate(malformed);
      const reply = reports.queueFindingMetricRaw(reportAlpha.id, metric, malformed, true);
      await refreshFindings(page).click();
      await reply.requested;
      await exactFindingMetric(page, valid, true);
      await release(reply);
      await expect(findingPanel(page).getByRole("alert")).toContainText(/invalid|replacement data|could not/i);
      await exactFindingMetric(page, valid);
    });
  }
  expect(reports.requests.filter((call) => call.method !== "GET")).toEqual([]);
});

async function tabTo(page: Page, target: Locator, key = "Tab") {
  for (let index = 0; index < 80; index += 1) {
    if (await target.evaluate((element) => element === document.activeElement)) {
      await expect(target).toBeInViewport({ ratio: 0.99 });
      return;
    }
    await page.keyboard.press(key);
  }
  await expect(target, "The chosen finding membership control must be keyboard reachable.").toBeFocused();
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
          if (new Set(frames.map((frame) => frame[key]).filter((value) => value !== undefined).map(String)).size > 1) {
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

test("Finding metric controls and exact table stay keyboard reachable without overflow or focus theft at 390px", async ({ page, reports }) => {
  test.setTimeout(120_000);
  await page.setViewportSize({ width: 390, height: 844 });
  await reportsPage(page, reports);
  for (const metric of metrics) {
    const activation = metricActivation(page, metric);
    await expect(activation, `${metricLabels[metric]} must be keyboard reachable at 390px.`).toHaveCount(1);
    await activation.scrollIntoViewIfNeeded();
    await tabTo(page, activation);
  }
  await metricActivation(page, "findings").focus();
  await page.keyboard.press("Enter");
  await expect.poll(() => reports.calls("GET", findingMetricsPath).length).toBe(1);
  const first = syntheticFindingMetricDrilldown(alphaOverview, "findings");
  await exactFindingMetric(page, first);

  const results = findingPanel(page).getByRole("region", { name: /finding membership results/i });
  await expect(results).toBeVisible();
  await tabTo(page, refreshFindings(page));
  await tabTo(page, results);
  await tabTo(page, loadMoreFindings(page));

  const held = reports.queueFindingMetric(reportAlpha.id, "findings",
    { status: 200, value: first }, true);
  await refreshFindings(page).scrollIntoViewIfNeeded();
  await refreshFindings(page).focus();
  await page.keyboard.press("Enter");
  await held.requested;
  await expect(refreshFindings(page)).toBeDisabled();
  const workspace = page.getByRole("combobox", { name: "Workspace", exact: true });
  await tabTo(page, workspace, "Shift+Tab");
  const scroll = await page.evaluate(() => ({ x: scrollX, y: scrollY }));
  await release(held);
  await expect(workspace, "Completing a finding membership refresh must not steal deliberately moved focus.").toBeFocused();
  expect(await page.evaluate(() => ({ x: scrollX, y: scrollY }))).toEqual(scroll);
  await exactFindingMetric(page, first);
  await reducedMovement(page);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1),
    "390px Reports must contain finding metric controls and the scrollable table without page-level overflow.").toBe(true);
  const stored = await page.evaluate(() =>
    JSON.stringify([...Object.entries(localStorage), ...Object.entries(sessionStorage)]));
  for (const canary of findingMetricStorageCanaries) expect(stored).not.toContain(canary);
  for (const metric of metrics) {
    expect(findingMetricTotal(alphaOverview, metric)).toBeGreaterThanOrEqual(0);
  }
  expect(reports.requests.filter((call) => call.method !== "GET")).toEqual([]);
});
