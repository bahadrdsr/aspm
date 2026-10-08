import type { Locator, Page } from "@playwright/test";
import { password } from "./application-fixture";
import { requireProductionUI } from "./network";
import {
  alphaOverview, overviewPath, reportAlpha, reportBeta, reportUser, savedReport, savedSnapshots, snapshotsPath,
  syntheticTrend, syntheticTrendResponse, trendsPath, trendStorageCanaries, trendVerificationReason,
} from "./reports-data";
import type { SyntheticTrend, SyntheticTrendDelta } from "./reports-data";
import { expect, test } from "./reports-fixture";
import type { ReportResponseControl, ReportsAPI } from "./reports-fixture";

test.use({ reducedMotion: "reduce" });
test.beforeEach(async ({ reports }) => {
  requireProductionUI();
  expect(reports.requests).toEqual([]);
});

const deltaLabels: Record<keyof SyntheticTrendDelta, string> = {
  findings: "Findings delta",
  openFindings: "Open findings delta",
  acceptedRisk: "Accepted risk delta",
  suppressed: "Suppressed delta",
  falsePositive: "False positive delta",
  critical: "Critical delta",
  high: "High delta",
  medium: "Medium delta",
  low: "Low delta",
  info: "Info delta",
  scannedAssets: "Scanned assets delta",
  unscannedAssets: "Unscanned assets delta",
  staleAssets: "Stale assets delta",
  unknownFreshnessAssets: "Unknown freshness delta",
};

function live(page: Page) {
  return page.getByRole("region", { name: "Live overview", exact: true, includeHidden: true });
}

function selected(page: Page) {
  return page.getByRole("region", { name: "Selected snapshot", exact: true, includeHidden: true });
}

function history(page: Page) {
  return page.getByRole("region", { name: "Saved snapshots", exact: true, includeHidden: true });
}

function trends(page: Page) {
  return page.getByRole("region", { name: "Historical trends", exact: true, includeHidden: true });
}

function trendToggle(page: Page) {
  return page.getByRole("button", { name: "Historical trends", exact: true })
    .or(page.getByRole("button", { name: "Show historical trends", exact: true }));
}

function trendInput(page: Page) {
  return trends(page).getByRole("spinbutton", { name: "Trend days", exact: true });
}

function refreshTrends(page: Page) {
  return trends(page).getByRole("button", { name: "Refresh trends", exact: true });
}

function trendTable(page: Page) {
  return trends(page).getByRole("table", { name: "Historical trend snapshots", exact: true });
}

function trendRows(page: Page) {
  return trendTable(page).getByRole("row").filter({ has: page.getByRole("cell") });
}

function deltaRegion(page: Page) {
  return trends(page).getByRole("region", { name: "First-to-last deltas", exact: true, includeHidden: true });
}

function literal(value: string) {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

function definition(region: Locator, label: string) {
  return region.getByRole("term", { includeHidden: true })
    .filter({ hasText: new RegExp(`^${literal(label)}$`, "i") }).locator("xpath=following-sibling::dd[1]");
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

async function openHistoricalTrends(page: Page, reports: ReportsAPI) {
  const region = trends(page);
  const before = reports.calls("GET", trendsPath).length;
  if (await region.isVisible()) {
    await refreshTrends(page).click();
  } else {
    await expect(trendToggle(page)).toBeVisible();
    await trendToggle(page).click();
  }
  await expect(region).toBeVisible();
  await expect.poll(() => reports.calls("GET", trendsPath).length).toBe(before + 1);
  return region;
}

async function exactSignedDelta(page: Page, key: keyof SyntheticTrendDelta, value: number) {
  const target = definition(deltaRegion(page), deltaLabels[key]);
  await expect(target).toHaveCount(1);
  const expected = value > 0 ? `+${value}` : String(value);
  await expect(target, `${deltaLabels[key]} must be a signed first-to-last integer.`)
    .toHaveText(value === 0 ? /^\s*\+?0\s*$/ : new RegExp(`^\\s*${literal(expected)}\\s*$`));
}

async function exactTrend(page: Page, trend: SyntheticTrend) {
  const region = trends(page);
  await expect(region).toBeVisible();
  await expect(region.locator(`time[datetime="${trend.from}"]`).first(), "Trend from must remain the server timestamp.").toBeVisible();
  await expect(region.locator(`time[datetime="${trend.to}"]`).first(), "Trend to must remain the server timestamp.").toBeVisible();
  await expect(region).toContainText(new RegExp(`\\b${trend.points.length} (?:point|points)\\b`, "i"));
  await expect(region).toContainText(trendVerificationReason);
  if (trend.points.length > 1) {
    await expect(region).toContainText(/points are (?:saved )?snapshots|saved snapshot points/i);
    await expect(region).toContainText(/missing (?:days|periods|time).*not filled|do not fill missing/i);
  }

  const table = trendTable(page);
  if (trend.points.length === 0) {
    if (await table.count()) {
      await expect(table).toBeVisible();
      await expect(trendRows(page)).toHaveCount(0);
    }
  } else {
    await expect(table).toBeVisible();
    const headers = (await table.getByRole("columnheader").allTextContents()).map((value) => value.trim().toLowerCase());
    const expectedColumns = ["snapshot", "completed", "findings", "open findings", "critical", "high", "scanned assets"];
    const indexes = new Map(expectedColumns.map((name) => [name, headers.indexOf(name)]));
    for (const [name, index] of indexes) {
      expect(index, `Historical trend table needs an accessible ${name} column.`).toBeGreaterThanOrEqual(0);
    }
    await expect(trendRows(page)).toHaveCount(trend.points.length);
    for (let index = 0; index < trend.points.length; index += 1) {
      const point = trend.points[index], row = trendRows(page).nth(index), cells = row.getByRole("cell");
      await expect(cells.nth(indexes.get("snapshot")!)).toContainText(point.name);
      await expect(cells.nth(indexes.get("completed")!).locator(`time[datetime="${point.completedAt}"]`)).toBeVisible();
      await expect(cells.nth(indexes.get("findings")!)).toHaveText(new RegExp(`^\\s*${point.totals.findings}\\s*$`));
      await expect(cells.nth(indexes.get("open findings")!)).toHaveText(new RegExp(`^\\s*${point.totals.openFindings}\\s*$`));
      await expect(cells.nth(indexes.get("critical")!)).toHaveText(new RegExp(`^\\s*${point.bySeverity.critical}\\s*$`));
      await expect(cells.nth(indexes.get("high")!)).toHaveText(new RegExp(`^\\s*${point.bySeverity.high}\\s*$`));
      await expect(cells.nth(indexes.get("scanned assets")!)).toHaveText(new RegExp(`^\\s*${point.coverage.scannedAssets}\\s*$`));
    }
  }

  if (trend.delta === null) {
    await expect(region).toContainText(trend.points.length === 0 ? /no historical|no saved snapshot/i : /insufficient history|need.*two/i);
    await expect(deltaRegion(page).getByRole("definition").filter({ hasText: /[-+]?\d/ })).toHaveCount(0);
  } else {
    await expect(deltaRegion(page)).toBeVisible();
    for (const key of Object.keys(deltaLabels) as Array<keyof SyntheticTrendDelta>) {
      await exactSignedDelta(page, key, trend.delta[key]);
    }
  }
}

async function release(control: ReportResponseControl, requireAbort = false) {
  control.release();
  await control.delivered;
  if (requireAbort) {
    await expect.poll(() => control.call?.failure,
      "Scope or session invalidation must abort the old historical trend request.").toMatch(/abort/i);
  }
}

async function hiddenTrend(page: Page, trend: SyntheticTrend) {
  for (const point of trend.points) {
    await expect(trends(page).getByText(point.name, { exact: true })).toHaveCount(0);
    await expect(trends(page).locator(`time[datetime="${point.completedAt}"]`)).toHaveCount(0);
  }
  if (await trendTable(page).count()) await expect(trendRows(page)).toHaveCount(0);
}

async function signIn(page: Page) {
  const form = page.getByRole("form", { name: "Sign in", exact: true });
  await form.getByLabel("Email", { exact: true }).fill(reportUser.email);
  await form.getByLabel("Password", { exact: true }).fill(password);
  await form.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(form).toHaveCount(0);
}

async function protectedTrendsGone(page: Page, trend: SyntheticTrend) {
  await expect(trends(page)).toHaveCount(0);
  for (const point of trend.points) {
    await expect(page.locator("body")).not.toContainText(point.name);
    await expect(page.locator(`time[datetime="${point.completedAt}"]`)).toHaveCount(0);
  }
  const stored = await page.evaluate(() => JSON.stringify([...Object.entries(localStorage), ...Object.entries(sessionStorage)]));
  for (const canary of trendStorageCanaries) expect(stored).not.toContain(canary);
}

test("Historical trends use explicit bounded refresh, exact snapshot points and no polling or writes", async ({ page, reports }) => {
  await page.clock.install();
  await reportsPage(page, reports);
  expect(reports.calls("GET", trendsPath)).toEqual([]);
  await renderBoundary(page);
  const boundary = reports.requests.length;
  const expected = syntheticTrend(reportAlpha.id, 30);
  await openHistoricalTrends(page, reports);
  expect(reports.requests.slice(boundary)).toEqual([
    expect.objectContaining({ method: "GET", path: trendsPath, workspace: reportAlpha.id, query: { days: "30" } }),
  ]);
  await expect(trendInput(page)).toHaveValue("30");
  await expect(trendInput(page)).toHaveAttribute("min", "1");
  await expect(trendInput(page)).toHaveAttribute("max", "365");
  await expect(trendInput(page)).toHaveAttribute("step", "1");
  await exactTrend(page, expected);

  const beforeEdit = reports.calls("GET", trendsPath).length;
  await trendInput(page).fill("7");
  await renderBoundary(page);
  expect(reports.calls("GET", trendsPath)).toHaveLength(beforeEdit);
  await refreshTrends(page).click();
  await expect.poll(() => reports.calls("GET", trendsPath).length).toBe(beforeEdit + 1);
  expect(reports.calls("GET", trendsPath).at(-1)).toMatchObject({
    workspace: reportAlpha.id, query: { days: "7" }, body: {},
  });
  await exactTrend(page, syntheticTrend(reportAlpha.id, 7));

  const validReads = reports.calls("GET", trendsPath).length;
  for (const invalid of ["0", "366", "1.5", ""]) {
    await trendInput(page).fill(invalid);
    if (await refreshTrends(page).isEnabled()) await refreshTrends(page).click();
    await renderBoundary(page);
    expect(reports.calls("GET", trendsPath), `Invalid trend days '${invalid}' must not reach the API.`).toHaveLength(validReads);
    expect(await trendInput(page).evaluate((element: HTMLInputElement) => element.checkValidity())).toBe(false);
  }
  await trendInput(page).fill("7");
  const trendText = await trends(page).innerText();
  const overviewReads = reports.calls("GET", overviewPath).length;
  await live(page).getByRole("button", { name: "Refresh report", exact: true }).click();
  await expect.poll(() => reports.calls("GET", overviewPath).length).toBe(overviewReads + 1);
  expect(reports.calls("GET", trendsPath)).toHaveLength(validReads);
  expect(await trends(page).innerText()).toBe(trendText);

  await page.clock.fastForward(300_000);
  await renderBoundary(page);
  expect(reports.calls("GET", trendsPath), "Historical trends must not poll.").toHaveLength(validReads);
  expect(reports.calls("POST", snapshotsPath), "Opening or refreshing trends must not create snapshots.").toEqual([]);
  expect(reports.requests.filter((call) => call.method !== "GET")).toEqual([]);
});

test("Viewer trends distinguish authorized empty, one-point history, gaps and the whole-window cap", async ({ page, reports }) => {
  reports.roles.set(reportAlpha.id, "viewer");
  reports.serverRoles.set(reportAlpha.id, "viewer");
  reports.queueTrend(reportAlpha.id, { status: 200, value: syntheticTrend(reportAlpha.id, 30, 0) });
  await reportsPage(page, reports);
  await openHistoricalTrends(page, reports);
  await exactTrend(page, syntheticTrend(reportAlpha.id, 30, 0));
  await expect(trends(page).getByRole("status")).toContainText(/no historical|no saved snapshot/i);
  const create = page.getByRole("button", { name: "Create snapshot", exact: true, includeHidden: true });
  if (await create.count()) await expect(create).toBeDisabled();
  else await expect(create).toHaveCount(0);

  const one = syntheticTrend(reportAlpha.id, 30, 1);
  reports.queueTrend(reportAlpha.id, { status: 200, value: one });
  await refreshTrends(page).click();
  await exactTrend(page, one);
  await expect(trends(page)).toContainText(/insufficient history|need.*two/i);

  const gaps = syntheticTrend(reportAlpha.id, 30, 3);
  reports.queueTrend(reportAlpha.id, { status: 200, value: gaps });
  await refreshTrends(page).click();
  await exactTrend(page, gaps);
  await expect(trends(page)).toContainText(/missing (?:days|periods|time).*not filled|do not fill missing/i);

  const tooLarge = reports.queueTrend(reportAlpha.id, { status: 413 }, true);
  await refreshTrends(page).click();
  await tooLarge.requested;
  await exactTrend(page, gaps);
  await release(tooLarge);
  await expect(trends(page).getByRole("alert")).toContainText(/too large|100|window/i);
  const visibleNames = await Promise.all(gaps.points.map((point) =>
    trends(page).getByText(point.name, { exact: true }).count()));
  const visibleCount = visibleNames.reduce((sum, count) => sum + count, 0);
  expect([0, gaps.points.length], "A 413 may preserve or withhold prior authority, but never truncate it.").toContain(visibleCount);
  if (visibleCount === gaps.points.length) await exactTrend(page, gaps);
  else await hiddenTrend(page, gaps);
  expect(reports.calls("POST", snapshotsPath)).toEqual([]);
});

test("Trend authority withholds denials, preserves transient failures and clears scope or session data", async ({ page, reports }) => {
  await reportsPage(page, reports);
  const alpha = syntheticTrend(reportAlpha.id, 30);
  await openHistoricalTrends(page, reports);
  await exactTrend(page, alpha);

  const unavailable = reports.queueTrend(reportAlpha.id, { status: 503 }, true);
  await refreshTrends(page).click();
  await unavailable.requested;
  await exactTrend(page, alpha);
  await expect(refreshTrends(page)).toBeDisabled();
  await release(unavailable);
  await expect(trends(page).getByRole("alert")).toContainText("Synthetic report service unavailable.");
  await exactTrend(page, alpha);

  for (const status of [403, 404] as const) {
    reports.queueTrend(reportAlpha.id, { status });
    await refreshTrends(page).click();
    await expect(trends(page).getByRole("alert")).toContainText(status === 403 ? /access denied/i : /not found/i);
    await hiddenTrend(page, alpha);

    const retry = reports.queueTrend(reportAlpha.id, { status: 503 }, true);
    await refreshTrends(page).click();
    await retry.requested;
    await hiddenTrend(page, alpha);
    await release(retry);
    await expect(trends(page).getByRole("alert")).toContainText(/unavailable/i);
    await hiddenTrend(page, alpha);

    reports.queueTrend(reportAlpha.id, { status: 200, value: alpha });
    await refreshTrends(page).click();
    await exactTrend(page, alpha);
    await expect(trends(page).getByRole("alert")).toHaveCount(0);
  }

  const oldAlpha = reports.queueTrend(reportAlpha.id, { status: 200, value: alpha }, true);
  await refreshTrends(page).click();
  await oldAlpha.requested;
  await page.getByRole("combobox", { name: "Workspace", exact: true }).selectOption(reportBeta.id);
  await hiddenTrend(page, alpha);
  await release(oldAlpha, true);
  await hiddenTrend(page, alpha);
  const beta = syntheticTrend(reportBeta.id, 30);
  reports.queueTrend(reportBeta.id, { status: 200, value: beta });
  if (!(await trends(page).isVisible())) {
    await openHistoricalTrends(page, reports);
  } else {
    await refreshTrends(page).click();
  }
  await exactTrend(page, beta);
  for (const point of alpha.points) await expect(page.getByText(point.name, { exact: true })).toHaveCount(0);

  const oldBeta = reports.queueTrend(reportBeta.id, { status: 200, value: beta }, true);
  await refreshTrends(page).click();
  await oldBeta.requested;
  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  await expect(page.getByRole("form", { name: "Sign in", exact: true })).toBeVisible();
  await protectedTrendsGone(page, beta);
  await release(oldBeta, true);
  await protectedTrendsGone(page, beta);

  await signIn(page);
  const recovered = syntheticTrend(reportAlpha.id, 30);
  reports.queueTrend(reportAlpha.id, { status: 200, value: recovered });
  await openHistoricalTrends(page, reports);
  await exactTrend(page, recovered);
  reports.queueTrend(reportAlpha.id, { status: 401 });
  await refreshTrends(page).click();
  await expect(page.getByRole("form", { name: "Sign in", exact: true })).toBeVisible();
  await protectedTrendsGone(page, recovered);
});

test("Trend client rejects wrong authority, windows, ordering, metrics, deltas and verification", async ({ page, reports }) => {
  test.setTimeout(60_000);
  await reportsPage(page, reports);
  const baseline = syntheticTrend(reportAlpha.id, 30);
  await openHistoricalTrends(page, reports);
  await exactTrend(page, baseline);
  const zeroDelta: SyntheticTrendDelta = {
    findings: 0, openFindings: 0, acceptedRisk: 0, suppressed: 0, falsePositive: 0,
    critical: 0, high: 0, medium: 0, low: 0, info: 0,
    scannedAssets: 0, unscannedAssets: 0, staleAssets: 0, unknownFreshnessAssets: 0,
  };
  const scenarios: Array<[string, (body: any) => void]> = [
    ["wrong workspace", (body) => { body.trend.workspaceId = reportBeta.id; }],
    ["wrong from", (body) => { body.trend.from = new Date(Date.parse(body.trend.from) + 1_000).toISOString(); }],
    ["wrong to", (body) => { body.trend.to = new Date(Date.parse(body.trend.to) - 1_000).toISOString(); }],
    ["wrong days", (body) => { body.trend.days = 29; }],
    ["descending point order", (body) => { body.trend.points.reverse(); }],
    ["duplicate snapshot ID", (body) => { body.trend.points[1].snapshotId = body.trend.points[0].snapshotId; }],
    ["point outside window", (body) => {
      body.trend.points[0].completedAt = new Date(Date.parse(body.trend.from) - 1_000).toISOString();
      body.trend.points[0].asOf = body.trend.points[0].completedAt;
    }],
    ["point asOf mismatch", (body) => {
      body.trend.points[1].asOf = new Date(Date.parse(body.trend.points[1].completedAt) + 1_000).toISOString();
    }],
    ["unknown total metric", (body) => { body.trend.points[0].totals.forecast = 4; }],
    ["inconsistent severity total", (body) => { body.trend.points[0].bySeverity.critical += 1; }],
    ["inconsistent coverage total", (body) => { body.trend.points[0].coverage.scannedAssets += 1; }],
    ["wrong delta arithmetic", (body) => { body.trend.delta.findings += 1; }],
    ["unknown delta metric", (body) => { body.trend.delta.rate = 0.5; }],
    ["nonnull one-point delta", (body) => { body.trend.points = [body.trend.points[0]]; body.trend.delta = zeroDelta; }],
    ["null multi-point delta", (body) => { body.trend.delta = null; }],
    ["changed verification state", (body) => { body.trend.verification.state = "verified"; }],
    ["changed verification wording", (body) => { body.trend.verification.reason += " Changed."; }],
    ["wrong API version", (body) => { body.apiVersion = "aspm/v2"; }],
    ["wrong data origin", (body) => { body.dataOrigin = "synthetic"; }],
    ["more than 100 points", (body) => {
      const start = Date.parse(body.trend.from) + 1_000;
      body.trend.points = Array.from({ length: 101 }, (_, index) => {
        const point = structuredClone(body.trend.points[0]);
        point.snapshotId = (index + 1).toString(16).padStart(32, "0");
        point.name = `Synthetic over-cap historical point ${index + 1}`;
        point.completedAt = new Date(start + index * 1_000).toISOString();
        point.asOf = point.completedAt;
        return point;
      });
      body.trend.delta = zeroDelta;
    }],
  ];

  for (const [name, mutate] of scenarios) {
    await test.step(name, async () => {
      const malformed = structuredClone(syntheticTrendResponse(reportAlpha.id, 30)) as any;
      mutate(malformed);
      const reply = reports.queueTrendRaw(reportAlpha.id, malformed, true);
      const before = reports.calls("GET", trendsPath).length;
      await refreshTrends(page).click();
      await reply.requested;
      await exactTrend(page, baseline);
      await release(reply);
      await expect(trends(page).getByRole("alert")).toContainText(/invalid|replacement data|could not/i);
      await exactTrend(page, baseline);
      expect(reports.calls("GET", trendsPath)).toHaveLength(before + 1);
    });
  }
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
  await expect(target).toBeFocused();
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
  expect(violations).toEqual([]);
}

test("Historical trend controls remain reachable and preserve focus and Reports context at 390px", async ({ page, reports }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await reportsPage(page, reports);
  const firstSaved = savedSnapshots(reportAlpha.id)[0];
  const row = history(page).getByRole("table", { name: "Snapshots", exact: true })
    .getByRole("row").filter({ hasText: firstSaved.name });
  await row.getByRole("button", { name: "Open snapshot", exact: true })
    .or(row.getByRole("link", { name: "Open snapshot", exact: true })).click();
  await expect(selected(page).getByText(firstSaved.name, { exact: true })).toBeVisible();
  await expect(selected(page).locator(`time[datetime="${savedReport.asOf}"]`).first()).toBeVisible();

  await openHistoricalTrends(page, reports);
  await exactTrend(page, syntheticTrend(reportAlpha.id, 30));
  await trendInput(page).scrollIntoViewIfNeeded();
  await tabTo(page, trendInput(page));
  await tabTo(page, refreshTrends(page));
  const held = reports.queueTrend(reportAlpha.id, { status: 200, value: syntheticTrend(reportAlpha.id, 7) }, true);
  await trendInput(page).fill("7");
  await refreshTrends(page).focus();
  await page.keyboard.press("Enter");
  await held.requested;
  await expect(refreshTrends(page)).toBeDisabled();
  await tabTo(page, trendInput(page), "Shift+Tab");
  const scroll = await page.evaluate(() => ({ x: scrollX, y: scrollY }));
  await release(held);
  await expect(trendInput(page), "Completing a trend refresh must not steal deliberately moved focus.").toBeFocused();
  expect(await page.evaluate(() => ({ x: scrollX, y: scrollY }))).toEqual(scroll);
  await exactTrend(page, syntheticTrend(reportAlpha.id, 7));
  await expect(selected(page).getByText(firstSaved.name, { exact: true })).toBeVisible();
  await expect(live(page).locator(`time[datetime="${alphaOverview.asOf}"]`)).toBeVisible();
  await reducedMovement(page);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1),
    "390px Reports must contain the trend table and controls without page-level horizontal overflow.").toBe(true);
  const stored = await page.evaluate(() => JSON.stringify([...Object.entries(localStorage), ...Object.entries(sessionStorage)]));
  for (const canary of trendStorageCanaries) expect(stored).not.toContain(canary);
  expect(reports.calls("POST", snapshotsPath)).toEqual([]);
});
