import type { Locator, Page } from "@playwright/test";
import { password } from "./application-fixture";
import { requireProductionUI } from "./network";
import {
  alphaOverview, betaOverview, coverageAssetsPath, coverageStorageCanaries, coverageVerificationReason,
  overviewPath, refreshedOverview, reportAlpha, reportBeta, reportUser, savedReport, savedSnapshots,
  snapshotsPath, syntheticCoverageDrilldown, syntheticCoverageResponse,
} from "./reports-data";
import type { CoverageState, SyntheticCoverageDrilldown, SyntheticCoverageItem } from "./reports-data";
import { expect, test } from "./reports-fixture";
import type { ReportResponseControl, ReportsAPI } from "./reports-fixture";

test.use({ reducedMotion: "reduce" });
test.beforeEach(async ({ reports }) => {
  requireProductionUI();
  expect(reports.requests).toEqual([]);
});

const coverageLabels: Record<CoverageState, string> = {
  scanned: "Scanned assets",
  unscanned: "Unscanned assets",
  stale: "Stale assets",
  "unknown-freshness": "Unknown freshness",
};

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

function freshness(page: Page) {
  return live(page).getByRole("spinbutton", { name: "Freshness days", exact: true });
}

function refreshReport(page: Page) {
  return live(page).getByRole("button", { name: "Refresh report", exact: true });
}

function metricActivation(page: Page, state: CoverageState, region = live(page)) {
  const label = literal(coverageLabels[state]);
  return region.getByRole("button", {
    name: new RegExp(`^(?:View )?${label}(?:\\s+[\\d,]+)?$`, "i"),
  });
}

function coveragePanel(page: Page) {
  return page.getByRole("region", { name: "Current live asset membership", exact: true, includeHidden: true });
}

function refreshCoverage(page: Page) {
  return coveragePanel(page).getByRole("button", {
    name: /refresh (?:coverage assets|asset membership)/i,
  });
}

function loadMoreCoverage(page: Page) {
  return coveragePanel(page).getByRole("button", { name: /load more (?:coverage )?assets/i });
}

function coverageTable(page: Page) {
  return coveragePanel(page).getByRole("table", { name: /coverage assets/i });
}

function coverageRows(page: Page) {
  return coverageTable(page).getByRole("row").filter({ has: page.getByRole("cell") });
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

async function openCoverage(page: Page, reports: ReportsAPI, state: CoverageState) {
  const before = reports.calls("GET", coverageAssetsPath).length;
  const activation = metricActivation(page, state);
  await expect(activation, `${coverageLabels[state]} needs one accessible Live overview activation.`).toHaveCount(1);
  await activation.click();
  await expect(coveragePanel(page)).toBeVisible();
  await expect.poll(() => reports.calls("GET", coverageAssetsPath).length).toBe(before + 1);
  return coveragePanel(page);
}

function membership(item: SyntheticCoverageItem, state: CoverageState) {
  return state === "scanned" ? item.coverage.scanned :
    state === "unscanned" ? !item.coverage.scanned :
      state === "stale" ? item.coverage.stale : item.coverage.unknownFreshness;
}

async function exactCoverage(page: Page, expected: SyntheticCoverageDrilldown, actionsPending = false) {
  const panel = coveragePanel(page);
  await expect(panel).toBeVisible();
  await expect(panel).toContainText(/current live asset membership/i);
  await expect(panel).toContainText(coverageLabels[expected.state]);
  await expect(panel.locator(`time[datetime="${expected.freshnessWindow.from}"]`).first(),
    "Coverage freshness start must remain the exact server timestamp.").toBeVisible();
  await expect(panel.locator(`time[datetime="${expected.freshnessWindow.to}"]`).first(),
    "Coverage freshness end must remain the exact server timestamp.").toBeVisible();
  await expect(panel).toContainText(new RegExp(`\\b${expected.freshnessWindow.days} days?\\b`, "i"));
  await expect(panel).toContainText(coverageVerificationReason);
  await expect(panel).toContainText(new RegExp(`\\b${expected.items.length}\\s+of\\s+${expected.total}\\b`, "i"));
  if (actionsPending && expected.nextCursor !== null) {
    await expect(loadMoreCoverage(page)).toBeDisabled();
  }

  const table = coverageTable(page);
  await expect(table).toBeVisible();
  const headers = (await table.getByRole("columnheader").allTextContents()).map((value) => value.trim().toLowerCase());
  const indexes = {
    asset: headers.findIndex((value) => value === "asset"),
    scanned: headers.findIndex((value) => value === "scanned"),
    stale: headers.findIndex((value) => value === "stale"),
    unknown: headers.findIndex((value) => value === "unknown freshness"),
    latest: headers.findIndex((value) => /^latest (?:known )?source scan(?: time)?$/.test(value)),
  };
  for (const [name, index] of Object.entries(indexes)) {
    expect(index, `Coverage table needs an accessible ${name} column.`).toBeGreaterThanOrEqual(0);
  }
  await expect(coverageRows(page)).toHaveCount(expected.items.length);
  const rendered = await coverageRows(page).evaluateAll((rows, columns) => rows.map((row) => {
    const cells = [...row.querySelectorAll("td")];
    const latest = cells[columns.latest], time = latest?.querySelector("time");
    return {
      asset: cells[columns.asset]?.textContent ?? "",
      scanned: cells[columns.scanned]?.textContent ?? "",
      stale: cells[columns.stale]?.textContent ?? "",
      unknown: cells[columns.unknown]?.textContent ?? "",
      latest: latest?.textContent ?? "",
      latestDateTime: time?.getAttribute("datetime") ?? null,
    };
  }), indexes);
  for (let index = 0; index < expected.items.length; index += 1) {
    const item = expected.items[index], row = rendered[index];
    expect(membership(item, expected.state), "Synthetic expected item must satisfy its state.").toBe(true);
    expect(row.asset).toContain(item.asset.name);
    expect(row.asset).toContain(item.asset.id);
    expect(row.scanned.trim()).toMatch(new RegExp(`^${item.coverage.scanned ? "Yes" : "No"}$`, "i"));
    expect(row.stale.trim()).toMatch(new RegExp(`^${item.coverage.stale ? "Yes" : "No"}$`, "i"));
    expect(row.unknown.trim()).toMatch(new RegExp(`^${item.coverage.unknownFreshness ? "Yes" : "No"}$`, "i"));
    if (item.coverage.latestSourceScanAt === null) {
      expect(row.latest).toMatch(/unknown|no known/i);
      expect(row.latestDateTime).toBeNull();
    } else {
      expect(row.latestDateTime).toBe(item.coverage.latestSourceScanAt);
    }
  }
  if (expected.nextCursor === null) {
    if (await loadMoreCoverage(page).count()) await expect(loadMoreCoverage(page)).toBeDisabled();
  } else {
    await expect(loadMoreCoverage(page)).toBeVisible();
    if (!actionsPending) await expect(loadMoreCoverage(page)).toBeEnabled();
  }
}

async function release(control: ReportResponseControl, requireAbort = false) {
  control.release();
  await control.delivered;
  if (requireAbort) {
    await expect.poll(() => control.call?.failure,
      "Workspace or session invalidation must abort the old coverage request.").toMatch(/abort/i);
  }
}

async function hiddenCoverage(page: Page, data: SyntheticCoverageDrilldown) {
  for (const item of data.items.slice(0, 4)) {
    await expect(page.getByText(item.asset.name, { exact: true })).toHaveCount(0);
    if (item.coverage.latestSourceScanAt) {
      await expect(page.locator(`time[datetime="${item.coverage.latestSourceScanAt}"]`)).toHaveCount(0);
    }
  }
  if (await coverageTable(page).count()) await expect(coverageRows(page)).toHaveCount(0);
}

async function signIn(page: Page) {
  const form = page.getByRole("form", { name: "Sign in", exact: true });
  await form.getByLabel("Email", { exact: true }).fill(reportUser.email);
  await form.getByLabel("Password", { exact: true }).fill(password);
  await form.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(form).toHaveCount(0);
}

async function protectedCoverageGone(page: Page, data: SyntheticCoverageDrilldown) {
  await expect(coveragePanel(page)).toHaveCount(0);
  for (const item of data.items.slice(0, 4)) {
    await expect(page.locator("body")).not.toContainText(item.asset.name);
    if (item.coverage.latestSourceScanAt) {
      await expect(page.locator(`time[datetime="${item.coverage.latestSourceScanAt}"]`)).toHaveCount(0);
    }
  }
  const stored = await page.evaluate(() => JSON.stringify([...Object.entries(localStorage), ...Object.entries(sessionStorage)]));
  for (const canary of coverageStorageCanaries) expect(stored).not.toContain(canary);
}

test("Only Live coverage metrics open exact current membership with the applied overview freshness", async ({ page, reports }) => {
  test.setTimeout(90_000);
  reports.roles.set(reportAlpha.id, "viewer");
  reports.serverRoles.set(reportAlpha.id, "viewer");
  await page.clock.install();
  await reportsPage(page, reports);
  await selectSavedSnapshot(page);
  for (const state of Object.keys(coverageLabels) as CoverageState[]) {
    await expect(metricActivation(page, state)).toHaveCount(1);
    await expect(metricActivation(page, state, selected(page)),
      "Saved snapshot coverage counts cannot claim current membership.").toHaveCount(0);
  }
  expect(reports.calls("GET", coverageAssetsPath)).toEqual([]);

  const overviewReads = reports.calls("GET", overviewPath).length;
  await freshness(page).fill("30");
  await renderBoundary(page);
  expect(reports.calls("GET", overviewPath)).toHaveLength(overviewReads);
  const boundary = reports.requests.length;
  await openCoverage(page, reports, "scanned");
  expect(reports.requests.slice(boundary)).toEqual([
    expect.objectContaining({
      method: "GET", path: coverageAssetsPath, workspace: reportAlpha.id,
      query: { state: "scanned", freshnessDays: "7", limit: "100" }, body: {},
    }),
  ]);
  await exactCoverage(page, syntheticCoverageDrilldown(alphaOverview, "scanned", 7));

  const coverageReads = reports.calls("GET", coverageAssetsPath).length;
  await page.clock.fastForward(300_000);
  await renderBoundary(page);
  expect(reports.calls("GET", coverageAssetsPath), "Coverage drill-down must not poll.").toHaveLength(coverageReads);

  await refreshReport(page).click();
  await expect.poll(() => reports.calls("GET", overviewPath).length).toBe(overviewReads + 1);
  expect(reports.calls("GET", coverageAssetsPath)).toHaveLength(coverageReads);
  await openCoverage(page, reports, "stale");
  expect(reports.calls("GET", coverageAssetsPath).at(-1)).toMatchObject({
    workspace: reportAlpha.id, query: { state: "stale", freshnessDays: "30", limit: "100" }, body: {},
  });
  await exactCoverage(page, syntheticCoverageDrilldown(alphaOverview, "stale", 30));
  expect(reports.requests.filter((call) => call.method !== "GET")).toEqual([]);
});

test("Coverage paging uses the native cursor while refresh and metric switches replace pages", async ({ page, reports }) => {
  test.setTimeout(90_000);
  await reportsPage(page, reports);
  await page.getByRole("combobox", { name: "Workspace", exact: true }).selectOption(reportBeta.id);
  await expect.poll(() => reports.calls("GET", overviewPath)
    .filter((call) => call.workspace === reportBeta.id).length).toBe(1);
  const first = syntheticCoverageDrilldown(betaOverview, "scanned", 7);
  await openCoverage(page, reports, "scanned");
  await exactCoverage(page, first);
  expect(first.items).toHaveLength(100);
  expect(first.nextCursor).toBe(first.items.at(-1)!.asset.id);

  const unavailable = reports.queueCoverage(reportBeta.id, "scanned", { status: 503 }, true);
  await loadMoreCoverage(page).click();
  await unavailable.requested;
  expect(unavailable.call!.query).toEqual({
    state: "scanned", freshnessDays: "7", limit: "100", cursor: first.nextCursor,
  });
  await expect(loadMoreCoverage(page)).toBeDisabled();
  await expect(coverageRows(page)).toHaveCount(first.items.length);
  await expect(coverageRows(page).first()).toContainText(first.items[0].asset.name);
  await expect(coverageRows(page).last()).toContainText(first.items.at(-1)!.asset.name);
  await release(unavailable);
  await expect(coveragePanel(page).getByRole("alert")).toContainText(/unavailable/i);
  await exactCoverage(page, first);

  const callsBeforeRetry = reports.calls("GET", coverageAssetsPath).length;
  await loadMoreCoverage(page).click();
  await expect.poll(() => reports.calls("GET", coverageAssetsPath).length).toBe(callsBeforeRetry + 1);
  expect(reports.calls("GET", coverageAssetsPath).at(-1)!.query).toEqual(unavailable.call!.query);
  const second = syntheticCoverageDrilldown(betaOverview, "scanned", 7, 100, first.nextCursor!);
  await exactCoverage(page, {
    ...second, items: [...first.items, ...second.items],
  });

  const refresh = reports.queueCoverage(reportBeta.id, "scanned",
    { status: 200, value: first }, true);
  await refreshCoverage(page).click();
  await refresh.requested;
  expect(refresh.call!.query).toEqual({ state: "scanned", freshnessDays: "7", limit: "100" });
  await release(refresh);
  await exactCoverage(page, first);

  const unscannedPage = syntheticCoverageDrilldown(betaOverview, "unscanned", 7);
  const switched = reports.queueCoverage(reportBeta.id, "unscanned",
    { status: 200, value: unscannedPage }, true);
  await metricActivation(page, "unscanned").click();
  await switched.requested;
  expect(switched.call!.query).toEqual({ state: "unscanned", freshnessDays: "7", limit: "100" });
  await hiddenCoverage(page, first);
  await release(switched);
  await exactCoverage(page, unscannedPage);
  expect(reports.requests.filter((call) => call.method !== "GET")).toEqual([]);
});

test("Coverage authority preserves transient rows, latches denials and clears workspace or session data", async ({ page, reports }) => {
  test.setTimeout(90_000);
  await reportsPage(page, reports);
  const alpha = syntheticCoverageDrilldown(alphaOverview, "stale", 7);
  await openCoverage(page, reports, "stale");
  await exactCoverage(page, alpha);

  const unavailable = reports.queueCoverage(reportAlpha.id, "stale", { status: 503 }, true);
  await refreshCoverage(page).click();
  await unavailable.requested;
  expect(unavailable.call!.query).toEqual({ state: "stale", freshnessDays: "7", limit: "100" });
  await exactCoverage(page, alpha);
  await release(unavailable);
  await expect(coveragePanel(page).getByRole("alert")).toContainText(/unavailable/i);
  await exactCoverage(page, alpha);

  for (const status of [403, 404] as const) {
    reports.queueCoverage(reportAlpha.id, "stale", { status });
    await refreshCoverage(page).click();
    await expect(coveragePanel(page).getByRole("alert"))
      .toContainText(status === 403 ? /access denied/i : /not found/i);
    await hiddenCoverage(page, alpha);

    const retry = reports.queueCoverage(reportAlpha.id, "stale", { status: 503 }, true);
    await refreshCoverage(page).click();
    await retry.requested;
    await hiddenCoverage(page, alpha);
    await release(retry);
    await expect(coveragePanel(page).getByRole("alert")).toContainText(/unavailable/i);
    await hiddenCoverage(page, alpha);

    reports.queueCoverage(reportAlpha.id, "stale", { status: 200, value: alpha });
    await refreshCoverage(page).click();
    await exactCoverage(page, alpha);
    await expect(coveragePanel(page).getByRole("alert")).toHaveCount(0);
  }

  const oldAlpha = reports.queueCoverage(reportAlpha.id, "stale",
    { status: 200, value: alpha }, true);
  await refreshCoverage(page).click();
  await oldAlpha.requested;
  await page.getByRole("combobox", { name: "Workspace", exact: true }).selectOption(reportBeta.id);
  await hiddenCoverage(page, alpha);
  await release(oldAlpha, true);
  await hiddenCoverage(page, alpha);
  const beta = syntheticCoverageDrilldown(betaOverview, "scanned", 7);
  await openCoverage(page, reports, "scanned");
  await exactCoverage(page, beta);

  const oldBeta = reports.queueCoverage(reportBeta.id, "scanned",
    { status: 200, value: beta }, true);
  await refreshCoverage(page).click();
  await oldBeta.requested;
  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  await expect(page.getByRole("form", { name: "Sign in", exact: true })).toBeVisible();
  await protectedCoverageGone(page, beta);
  await release(oldBeta, true);
  await protectedCoverageGone(page, beta);

  await signIn(page);
  const recovered = syntheticCoverageDrilldown(alphaOverview, "unknown-freshness", 7);
  await openCoverage(page, reports, "unknown-freshness");
  await exactCoverage(page, recovered);
  reports.queueCoverage(reportAlpha.id, "unknown-freshness", { status: 401 });
  await refreshCoverage(page).click();
  await expect(page.getByRole("form", { name: "Sign in", exact: true })).toBeVisible();
  await protectedCoverageGone(page, recovered);
});

test("Coverage client rejects wrong authority, window, membership, ordering, cursors, keys and timestamps", async ({ page, reports }) => {
  test.setTimeout(90_000);
  await reportsPage(page, reports);
  const state: CoverageState = "unknown-freshness";
  const baseline = syntheticCoverageDrilldown(alphaOverview, state, 7);
  await openCoverage(page, reports, state);
  await exactCoverage(page, baseline);
  const scenarios: Array<[string, (body: any) => void]> = [
    ["unknown envelope key", (body) => { body.forecast = false; }],
    ["wrong workspace", (body) => { body.drilldown.workspaceId = reportBeta.id; }],
    ["wrong state", (body) => { body.drilldown.state = "stale"; }],
    ["wrong window start", (body) => {
      body.drilldown.freshnessWindow.from =
        new Date(Date.parse(body.drilldown.freshnessWindow.from) + 1_000).toISOString();
    }],
    ["wrong window end", (body) => {
      body.drilldown.freshnessWindow.to =
        new Date(Date.parse(body.drilldown.freshnessWindow.to) - 1_000).toISOString();
    }],
    ["wrong window days", (body) => { body.drilldown.freshnessWindow.days = 8; }],
    ["wrong item workspace", (body) => { body.drilldown.items[0].asset.workspaceId = reportBeta.id; }],
    ["descending item order", (body) => { body.drilldown.items.reverse(); }],
    ["duplicate asset ID", (body) => {
      body.drilldown.items[1].asset.id = body.drilldown.items[0].asset.id;
    }],
    ["upper-case asset ID", (body) => { body.drilldown.items[0].asset.id = "A".repeat(32); }],
    ["wrong whole-filter total", (body) => { body.drilldown.total += 1; }],
    ["unexpected next cursor", (body) => {
      body.drilldown.nextCursor = body.drilldown.items.at(-1).asset.id;
    }],
    ["truncated page without cursor", (body) => { body.drilldown.items.pop(); }],
    ["requested membership mismatch", (body) => {
      body.drilldown.items[0].coverage.unknownFreshness = false;
    }],
    ["impossible coverage flags", (body) => {
      body.drilldown.items[0].coverage.scanned = false;
      body.drilldown.items[0].coverage.stale = true;
    }],
    ["unknown drill-down key", (body) => { body.drilldown.rate = 0.5; }],
    ["unknown asset key", (body) => { body.drilldown.items[0].asset.forecast = "none"; }],
    ["unknown coverage key", (body) => { body.drilldown.items[0].coverage.verified = true; }],
    ["nonboolean coverage flag", (body) => { body.drilldown.items[0].coverage.stale = "false"; }],
    ["invalid latest source scan time", (body) => {
      body.drilldown.items[0].coverage.latestSourceScanAt = "not-a-timestamp";
    }],
    ["future latest source scan time", (body) => {
      body.drilldown.items[0].coverage.latestSourceScanAt =
        new Date(Date.parse(body.drilldown.freshnessWindow.to) + 1_000).toISOString();
    }],
    ["changed verification state", (body) => { body.drilldown.verification.state = "verified"; }],
    ["changed verification wording", (body) => { body.drilldown.verification.reason += " Changed."; }],
    ["wrong API version", (body) => { body.apiVersion = "aspm/v2"; }],
    ["wrong data origin", (body) => { body.dataOrigin = "synthetic"; }],
  ];

  for (const [name, mutate] of scenarios) {
    await test.step(name, async () => {
      const malformed = structuredClone(syntheticCoverageResponse(alphaOverview, state, 7)) as any;
      mutate(malformed);
      const reply = reports.queueCoverageRaw(reportAlpha.id, state, malformed, true);
      const before = reports.calls("GET", coverageAssetsPath).length;
      await refreshCoverage(page).click();
      await reply.requested;
      await exactCoverage(page, baseline);
      await release(reply);
      await expect(coveragePanel(page).getByRole("alert")).toContainText(/invalid|replacement data|could not/i);
      await exactCoverage(page, baseline);
      expect(reports.calls("GET", coverageAssetsPath)).toHaveLength(before + 1);
    });
  }
  expect(reports.requests.filter((call) => call.method !== "GET")).toEqual([]);
});

async function tabTo(page: Page, target: Locator, key = "Tab") {
  for (let index = 0; index < 50; index += 1) {
    if (await target.evaluate((element) => element === document.activeElement)) {
      await expect(target).toBeInViewport({ ratio: 0.99 });
      return;
    }
    await page.keyboard.press(key);
  }
  await expect(target, "The chosen coverage control must be reachable by keyboard.").toBeFocused();
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

test("Coverage controls and table stay keyboard reachable and preserve moved focus at 390px", async ({ page, reports }) => {
  test.setTimeout(90_000);
  reports.overviews.set(reportAlpha.id, refreshedOverview);
  await page.setViewportSize({ width: 390, height: 844 });
  await reportsPage(page, reports);
  const activation = metricActivation(page, "scanned");
  await activation.scrollIntoViewIfNeeded();
  await activation.focus();
  await page.keyboard.press("Enter");
  await expect.poll(() => reports.calls("GET", coverageAssetsPath).length).toBe(1);
  const first = syntheticCoverageDrilldown(refreshedOverview, "scanned", 7);
  await exactCoverage(page, first);

  const results = coveragePanel(page).getByRole("region", { name: /coverage asset results/i });
  await expect(results).toBeVisible();
  await tabTo(page, refreshCoverage(page));
  await tabTo(page, results);
  await tabTo(page, loadMoreCoverage(page));

  const held = reports.queueCoverage(reportAlpha.id, "scanned",
    { status: 200, value: first }, true);
  await refreshCoverage(page).scrollIntoViewIfNeeded();
  await refreshCoverage(page).focus();
  await page.keyboard.press("Enter");
  await held.requested;
  await expect(refreshCoverage(page)).toBeDisabled();
  const workspace = page.getByRole("combobox", { name: "Workspace", exact: true });
  await tabTo(page, workspace, "Shift+Tab");
  const scroll = await page.evaluate(() => ({ x: scrollX, y: scrollY }));
  await release(held);
  await expect(workspace, "Completing a coverage refresh must not steal deliberately moved focus.").toBeFocused();
  expect(await page.evaluate(() => ({ x: scrollX, y: scrollY }))).toEqual(scroll);
  await exactCoverage(page, first);
  await reducedMovement(page);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1),
    "390px Reports must contain coverage controls and the scrollable table without page-level overflow.").toBe(true);
  const stored = await page.evaluate(() => JSON.stringify([...Object.entries(localStorage), ...Object.entries(sessionStorage)]));
  for (const canary of coverageStorageCanaries) expect(stored).not.toContain(canary);
  expect(reports.requests.filter((call) => call.method !== "GET")).toEqual([]);
});
