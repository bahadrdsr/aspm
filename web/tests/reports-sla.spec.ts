import type { Locator, Page } from "@playwright/test";
import { password } from "./application-fixture";
import { requireProductionUI } from "./network";
import {
  alphaSLAPolicy, overviewPath, reportAlpha, reportBeta, reportUser,
  slaFindingsPath, slaPath, slaPolicyPath, slaStorageCanaries, slaVerificationReason,
  snapshotsPath, syntheticSLAFindingPage, syntheticSLAPolicyResponse, syntheticSLAResponse,
  syntheticSLASummary,
} from "./reports-data";
import type {
  SLAStatus, SyntheticSLAFindingPage, SyntheticSLAPolicy, SyntheticSLASummary,
} from "./reports-data";
import { expect, test } from "./reports-fixture";
import type { ReportResponseControl, ReportsAPI } from "./reports-fixture";

test.use({ reducedMotion: "reduce" });
test.beforeEach(async ({ reports }) => {
  requireProductionUI();
  expect(reports.requests).toEqual([]);
});

function live(page: Page) {
  return page.getByRole("region", { name: "Live overview", exact: true, includeHidden: true });
}

function sla(page: Page) {
  return page.getByRole("region", { name: "Remediation SLA", exact: true, includeHidden: true });
}

function slaToggle(page: Page) {
  return page.getByRole("button", { name: "Remediation SLA", exact: true })
    .or(page.getByRole("button", { name: "Show remediation SLA", exact: true }));
}

function refreshSLA(page: Page) {
  return sla(page).getByRole("button", { name: /refresh (?:remediation )?sla$/i });
}

function statusActivation(page: Page, status: SLAStatus) {
  const label = status === "breached" ? "Breached" : "Within target";
  return sla(page).getByRole("button", {
    name: new RegExp(`^(?:View )?${label}(?: findings)?(?:\\s+[\\d,]+)?$`, "i"),
  });
}

function findings(page: Page) {
  return page.getByRole("region", { name: "Remediation SLA findings", exact: true, includeHidden: true });
}

function refreshFindings(page: Page) {
  return findings(page).getByRole("button", { name: /refresh (?:sla )?findings/i });
}

function loadMoreFindings(page: Page) {
  return findings(page).getByRole("button", { name: /load more (?:sla )?findings/i });
}

function findingsTable(page: Page) {
  return findings(page).getByRole("table", { name: /remediation sla findings/i });
}

function findingRows(page: Page) {
  return findingsTable(page).getByRole("row").filter({ has: page.getByRole("cell") });
}

function editSLA(page: Page) {
  return sla(page).getByRole("button", { name: "Edit SLA targets", exact: true });
}

function editForm(page: Page) {
  return page.getByRole("form", { name: "Edit SLA targets", exact: true });
}

function saveSLA(page: Page) {
  return editForm(page).getByRole("button", { name: "Save", exact: true });
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

async function openSLA(page: Page, reports: ReportsAPI) {
  const before = reports.calls("GET", slaPath).length;
  if (await sla(page).isVisible()) {
    await refreshSLA(page).click();
  } else {
    await expect(slaToggle(page)).toBeVisible();
    await slaToggle(page).click();
  }
  await expect(sla(page)).toBeVisible();
  await expect.poll(() => reports.calls("GET", slaPath).length).toBe(before + 1);
}

async function exactSummary(page: Page, expected: SyntheticSLASummary) {
  const region = sla(page);
  await expect(region.locator(`time[datetime="${expected.asOf}"]`).first()).toBeVisible();
  await expect(region).toContainText(slaVerificationReason);
  await expect(region).toContainText(/accepted risk.*(?:does not|doesn't).*stop|accepted risk.*clock/i);
  await expect(region).toContainText(/suppression.*(?:does not|doesn't).*stop|suppression.*clock/i);
  await expect(region).toContainText(/source (?:inference|state).*(?:does not|doesn't).*stop|source.*clock/i);
  await expect(region).toContainText(new RegExp(`\\brevision\\s+${expected.policy.revision}\\b`, "i"));
  for (const [label, value] of [
    ["Critical target", expected.policy.criticalDays],
    ["High target", expected.policy.highDays],
    ["Medium target", expected.policy.mediumDays],
    ["Low target", expected.policy.lowDays],
    ["Info target", expected.policy.infoDays],
  ] as const) {
    await expect(definition(region, label)).toContainText(new RegExp(`\\b${value}\\s+days?\\b`, "i"));
  }
  await expect(definition(region, "Tracked"))
    .toHaveText(new RegExp(`^\\s*${expected.totals.tracked.toLocaleString("en-US")}\\s*$`));
  await expect(statusActivation(page, "within-target"))
    .toContainText(expected.totals.withinTarget.toLocaleString("en-US"));
  await expect(statusActivation(page, "breached"))
    .toContainText(expected.totals.breached.toLocaleString("en-US"));
  for (const severity of ["critical", "high", "medium", "low", "info"] as const) {
    const counts = expected.bySeverity[severity];
    await expect(region).toContainText(new RegExp(
      `${severity}[^\\n]*(?:${counts.tracked}[^\\n]*${counts.breached}|${counts.breached}[^\\n]*${counts.tracked})`, "i"));
  }
}

async function openStatus(page: Page, reports: ReportsAPI, status: SLAStatus) {
  const before = reports.calls("GET", slaFindingsPath).length;
  await statusActivation(page, status).click();
  await expect(findings(page)).toBeVisible();
  await expect.poll(() => reports.calls("GET", slaFindingsPath).length).toBe(before + 1);
}

async function exactFindingPage(page: Page, expected: SyntheticSLAFindingPage, combinedItems = expected.items) {
  const region = findings(page);
  await expect(region).toContainText(new RegExp(`\\b${combinedItems.length}\\s+of\\s+${expected.total}\\b`, "i"));
  const table = findingsTable(page);
  await expect(table).toBeVisible();
  const headers = (await table.getByRole("columnheader").allTextContents()).map((value) => value.trim().toLowerCase());
  for (const name of ["finding", "asset", "severity", "owner", "workflow", "disposition", "source state",
    "first observed", "due", "target days", "status", "overdue"]) {
    expect(headers.some((header) => header === name || header.includes(name)),
      `SLA table needs an accessible ${name} column.`).toBe(true);
  }
  await expect(findingRows(page)).toHaveCount(combinedItems.length);
  const rendered = await findingRows(page).evaluateAll((rows, count) => rows.slice(0, count).map((row) => ({
    text: row.textContent ?? "",
    times: [...row.querySelectorAll("time")].map((time) => time.getAttribute("datetime")),
  })), Math.min(combinedItems.length, 6));
  for (let index = 0; index < Math.min(combinedItems.length, 6); index += 1) {
    const item = combinedItems[index], row = rendered[index];
    expect(row.text).toContain(item.title);
    expect(row.text).toContain(item.findingId);
    expect(row.text).toContain(item.assetName);
    expect(row.text).toContain(item.assetId);
    expect(row.text).toMatch(new RegExp(item.severity, "i"));
    expect(row.text).toMatch(new RegExp(item.workflowState.replace("-", "[ -]"), "i"));
    expect(row.text).toMatch(new RegExp(item.disposition.replace("-", "[ -]"), "i"));
    expect(row.text).toMatch(new RegExp(item.sourceState.replace("-", "[ -]"), "i"));
    expect(row.times).toContain(item.firstObservedAt);
    expect(row.times).toContain(item.dueAt);
    expect(row.text).toMatch(new RegExp(`\\b${item.targetDays}\\s+days?\\b`, "i"));
    expect(row.text).toMatch(new RegExp(item.status === "breached" ? "breached" : "within target", "i"));
    expect(row.text).toMatch(new RegExp(`\\b${item.overdueSeconds.toLocaleString("en-US")}\\b`));
  }
  if (expected.nextCursor === null) {
    if (await loadMoreFindings(page).count()) await expect(loadMoreFindings(page)).toBeDisabled();
  } else {
    await expect(loadMoreFindings(page)).toBeVisible();
  }
}

async function release(control: ReportResponseControl, requireAbort = false) {
  control.release();
  await control.delivered;
  if (requireAbort) {
    await expect.poll(() => control.call?.failure,
      "Workspace or session invalidation must abort the old SLA request.").toMatch(/abort/i);
  }
}

async function signIn(page: Page) {
  const form = page.getByRole("form", { name: "Sign in", exact: true });
  await form.getByLabel("Email", { exact: true }).fill(reportUser.email);
  await form.getByLabel("Password", { exact: true }).fill(password);
  await form.getByRole("button", { name: "Sign in", exact: true }).click();
  await expect(form).toHaveCount(0);
}

test("Remediation SLA is closed by default, reads once, shows exact current policy and never polls", async ({ page, reports }) => {
  await page.clock.install();
  reports.roles.set(reportAlpha.id, "viewer");
  reports.serverRoles.set(reportAlpha.id, "viewer");
  await reportsPage(page, reports);
  expect(reports.calls("GET", slaPath)).toEqual([]);
  expect(reports.calls("GET", slaFindingsPath)).toEqual([]);
  const boundary = reports.requests.length;
  await openSLA(page, reports);
  expect(reports.requests.slice(boundary)).toEqual([
    expect.objectContaining({ method: "GET", path: slaPath, workspace: reportAlpha.id, query: {}, body: {} }),
  ]);
  await exactSummary(page, syntheticSLASummary(reportAlpha.id));
  await expect(editSLA(page)).toHaveCount(0);
  const reads = reports.calls("GET", slaPath).length;
  await page.clock.fastForward(300_000);
  await renderBoundary(page);
  expect(reports.calls("GET", slaPath), "Remediation SLA must not poll.").toHaveLength(reads);
  expect(reports.calls("GET", slaFindingsPath)).toEqual([]);
  expect(reports.requests.filter((call) => call.method !== "GET")).toEqual([]);
});

test("SLA status activations use native pages, exact cursors, refresh and immediate state replacement", async ({ page, reports }) => {
  test.setTimeout(90_000);
  await reportsPage(page, reports);
  await openSLA(page, reports);
  const summary = syntheticSLASummary(reportAlpha.id);
  await exactSummary(page, summary);
  await openStatus(page, reports, "breached");
  const first = syntheticSLAFindingPage(reportAlpha.id, "breached");
  expect(reports.calls("GET", slaFindingsPath).at(-1)).toMatchObject({
    workspace: reportAlpha.id, query: { status: "breached", limit: "100" }, body: {},
  });
  await exactFindingPage(page, first);
  expect(first.items).toHaveLength(100);
  expect(first.total).toBe(summary.totals.breached);

  const unavailable = reports.queueSLAFinding(reportAlpha.id, "breached", { status: 503 }, true);
  await loadMoreFindings(page).click();
  await unavailable.requested;
  expect(unavailable.call!.query).toEqual({
    status: "breached", limit: "100", cursor: first.nextCursor,
  });
  await expect(loadMoreFindings(page)).toBeDisabled();
  await exactFindingPage(page, first);
  await release(unavailable);
  await expect(findings(page).getByRole("alert")).toContainText(/unavailable/i);
  await exactFindingPage(page, first);

  const beforeRetry = reports.calls("GET", slaFindingsPath).length;
  await loadMoreFindings(page).click();
  await expect.poll(() => reports.calls("GET", slaFindingsPath).length).toBe(beforeRetry + 1);
  const second = syntheticSLAFindingPage(reportAlpha.id, "breached", 100, first.nextCursor!);
  await exactFindingPage(page, second, [...first.items, ...second.items]);

  const refreshed = reports.queueSLAFinding(reportAlpha.id, "breached", { status: 200, value: first }, true);
  await refreshFindings(page).click();
  await refreshed.requested;
  expect(refreshed.call!.query).toEqual({ status: "breached", limit: "100" });
  await release(refreshed);
  await exactFindingPage(page, first);

  const within = syntheticSLAFindingPage(reportAlpha.id, "within-target");
  const switched = reports.queueSLAFinding(reportAlpha.id, "within-target",
    { status: 200, value: within }, true);
  await statusActivation(page, "within-target").click();
  await switched.requested;
  for (const item of first.items.slice(0, 3)) await expect(page.getByText(item.title, { exact: true })).toHaveCount(0);
  await release(switched);
  await exactFindingPage(page, within);
  expect(within.total).toBe(summary.totals.withinTarget);
  expect(reports.requests.filter((call) => call.method !== "GET")).toEqual([]);
});

test("SLA authority preserves transient data, latches denials and clears old workspace or session state", async ({ page, reports }) => {
  test.setTimeout(150_000);
  await reportsPage(page, reports);
  await openSLA(page, reports);
  const alpha = syntheticSLASummary(reportAlpha.id);
  await exactSummary(page, alpha);
  await openStatus(page, reports, "within-target");
  const alphaPage = syntheticSLAFindingPage(reportAlpha.id, "within-target");
  await exactFindingPage(page, alphaPage);

  const summary503 = reports.queueSLA(reportAlpha.id, { status: 503 }, true);
  await refreshSLA(page).click();
  await summary503.requested;
  await exactSummary(page, alpha);
  await release(summary503);
  await expect(sla(page).getByRole("alert")).toContainText(/unavailable/i);
  await exactSummary(page, alpha);

  const page503 = reports.queueSLAFinding(reportAlpha.id, "within-target", { status: 503 }, true);
  await refreshFindings(page).click();
  await page503.requested;
  await exactFindingPage(page, alphaPage);
  await release(page503);
  await expect(findings(page).getByRole("alert")).toContainText(/unavailable/i);
  await exactFindingPage(page, alphaPage);

  for (const status of [403, 404] as const) {
    reports.queueSLA(reportAlpha.id, { status });
    await refreshSLA(page).click();
    await expect(sla(page).getByRole("alert")).toContainText(status === 403 ? /access denied/i : /not found/i);
    await expect(sla(page).locator(`time[datetime="${alpha.asOf}"]`)).toHaveCount(0);
    const retry = reports.queueSLA(reportAlpha.id, { status: 503 }, true);
    await refreshSLA(page).click();
    await retry.requested;
    await expect(sla(page).locator(`time[datetime="${alpha.asOf}"]`)).toHaveCount(0);
    await release(retry);
    await expect(sla(page).locator(`time[datetime="${alpha.asOf}"]`)).toHaveCount(0);
    reports.queueSLA(reportAlpha.id, { status: 200, value: alpha });
    await refreshSLA(page).click();
    await exactSummary(page, alpha);
  }

  reports.queueSLAFinding(reportAlpha.id, "within-target", { status: 403 });
  await refreshFindings(page).click();
  for (const item of alphaPage.items) await expect(page.getByText(item.title, { exact: true })).toHaveCount(0);
  const deniedRetry = reports.queueSLAFinding(reportAlpha.id, "within-target", { status: 503 }, true);
  await refreshFindings(page).click();
  await deniedRetry.requested;
  for (const item of alphaPage.items) await expect(page.getByText(item.title, { exact: true })).toHaveCount(0);
  await release(deniedRetry);
  for (const item of alphaPage.items) await expect(page.getByText(item.title, { exact: true })).toHaveCount(0);

  const old = reports.queueSLA(reportAlpha.id, { status: 200, value: alpha }, true);
  await refreshSLA(page).click();
  await old.requested;
  await page.getByRole("combobox", { name: "Workspace", exact: true }).selectOption(reportBeta.id);
  await expect(page.locator(`time[datetime="${alpha.asOf}"]`)).toHaveCount(0);
  await release(old, true);
  await expect(page.locator(`time[datetime="${alpha.asOf}"]`)).toHaveCount(0);
  await openSLA(page, reports);
  await exactSummary(page, syntheticSLASummary(reportBeta.id));

  const beta = syntheticSLASummary(reportBeta.id);
  const oldBeta = reports.queueSLA(reportBeta.id, { status: 200, value: beta }, true);
  await refreshSLA(page).click();
  await oldBeta.requested;
  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  await expect(page.getByRole("form", { name: "Sign in", exact: true })).toBeVisible();
  await expect(sla(page)).toHaveCount(0);
  await release(oldBeta, true);
  await expect(sla(page)).toHaveCount(0);

  await signIn(page);
  await openSLA(page, reports);
  reports.queueSLA(reportAlpha.id, { status: 401 });
  await refreshSLA(page).click();
  await expect(page.getByRole("form", { name: "Sign in", exact: true })).toBeVisible();
  await expect(sla(page)).toHaveCount(0);
});

test("Admin SLA editing is explicit, nonoptimistic, conflict-safe and refreshes authorized reads", async ({ page, reports }) => {
  test.setTimeout(90_000);
  await reportsPage(page, reports);
  await openSLA(page, reports);
  await openStatus(page, reports, "breached");
  const before = syntheticSLASummary(reportAlpha.id);
  await exactSummary(page, before);
  await editSLA(page).click();
  const form = editForm(page);
  await expect(form).toBeVisible();
  await expect(form).toContainText(new RegExp(`\\brevision\\s+${alphaSLAPolicy.revision}\\b`, "i"));
  const fields = [
    ["Critical days", "5"], ["High days", "20"], ["Medium days", "60"],
    ["Low days", "120"], ["Info days", "240"],
  ] as const;
  for (const [label, value] of fields) await form.getByLabel(label, { exact: true }).fill(value);
  const rationale = "Approve the bounded synthetic SLA target change after explicit review.";
  await form.getByLabel("Rationale", { exact: true }).fill(rationale);
  const expected: SyntheticSLAPolicy = {
    ...structuredClone(alphaSLAPolicy),
    criticalDays: 5, highDays: 20, mediumDays: 60, lowDays: 120, infoDays: 240,
    revision: alphaSLAPolicy.revision + 1, rationale, updatedAt: "2026-10-08T17:02:51.000Z",
  };
  const held = reports.queueSLAPolicyUpdate(reportAlpha.id, { status: 200, value: expected }, true);
  const boundary = reports.requests.length;
  await saveSLA(page).click();
  await held.requested;
  expect(held.call).toMatchObject({
    method: "PATCH", path: slaPolicyPath, workspace: reportAlpha.id, query: {},
    body: {
      revision: alphaSLAPolicy.revision,
      criticalDays: 5, highDays: 20, mediumDays: 60, lowDays: 120, infoDays: 240, rationale,
    },
  });
  await exactSummary(page, before);
  await expect(saveSLA(page)).toBeDisabled();
  await release(held);
  await expect.poll(() => reports.calls("GET", slaPath).length).toBe(2);
  await expect.poll(() => reports.calls("GET", slaFindingsPath).length).toBe(2);
  await exactSummary(page, syntheticSLASummary(reportAlpha.id, expected));
  const postSaveCalls = reports.requests.slice(boundary).map((call) => `${call.method} ${call.path}`).sort();
  expect(postSaveCalls).toEqual([
    `GET ${slaFindingsPath}`, `GET ${slaPath}`, `PATCH ${slaPolicyPath}`,
  ].sort());

  await editSLA(page).click();
  await editForm(page).getByLabel("Critical days", { exact: true }).fill("4");
  await editForm(page).getByLabel("Rationale", { exact: true }).fill("Keep this draft after a stale revision conflict.");
  const conflict = reports.queueSLAPolicyUpdate(reportAlpha.id, { status: 409 }, true);
  await saveSLA(page).click();
  await conflict.requested;
  await release(conflict);
  await expect(editForm(page).getByLabel("Critical days", { exact: true })).toHaveValue("4");
  await expect(editForm(page).getByLabel("Rationale", { exact: true }))
    .toHaveValue("Keep this draft after a stale revision conflict.");
  await expect(editForm(page).getByRole("alert")).toContainText(/conflict|reload|changed/i);

  const forbidden = reports.queueSLAPolicyUpdate(reportAlpha.id, { status: 403 }, true);
  await saveSLA(page).click();
  await forbidden.requested;
  await release(forbidden);
  await expect(editForm(page).getByLabel("Critical days", { exact: true })).toHaveValue("4");
  await expect(editForm(page).getByRole("alert")).toContainText(/access denied|forbidden/i);

  const unauthorized = reports.queueSLAPolicyUpdate(reportAlpha.id, { status: 401 }, true);
  await saveSLA(page).click();
  await unauthorized.requested;
  await release(unauthorized);
  await expect(page.getByRole("form", { name: "Sign in", exact: true })).toBeVisible();
  await expect(editForm(page)).toHaveCount(0);
  await signIn(page);

  await page.getByRole("combobox", { name: "Workspace", exact: true }).selectOption(reportBeta.id);
  await expect(editForm(page)).toHaveCount(0);
  await openSLA(page, reports);
  await expect(editSLA(page)).toHaveCount(0);
});

test("SLA client rejects wrong keys, authority, arithmetic, timestamps, membership and acknowledgements", async ({ page, reports }) => {
  test.setTimeout(120_000);
  await reportsPage(page, reports);
  await openSLA(page, reports);
  const baseline = syntheticSLASummary(reportAlpha.id);
  await exactSummary(page, baseline);
  const summaryScenarios: Array<[string, (body: any) => void]> = [
    ["unknown envelope key", (body) => { body.forecast = false; }],
    ["wrong workspace", (body) => { body.sla.workspaceId = reportBeta.id; }],
    ["wrong policy workspace", (body) => { body.sla.policy.workspaceId = reportBeta.id; }],
    ["unordered policy", (body) => { body.sla.policy.criticalDays = body.sla.policy.highDays + 1; }],
    ["invalid policy revision", (body) => { body.sla.policy.revision = 0; }],
    ["actor pair mismatch", (body) => { body.sla.policy.approvedByName = null; }],
    ["invalid policy timestamp", (body) => { body.sla.policy.updatedAt = "not-a-timestamp"; }],
    ["wrong tracked arithmetic", (body) => { body.sla.totals.tracked += 1; }],
    ["wrong severity arithmetic", (body) => { body.sla.bySeverity.high.breached += 1; }],
    ["unknown totals key", (body) => { body.sla.totals.paused = 1; }],
    ["changed verification", (body) => { body.sla.verification.reason += " Changed."; }],
    ["wrong API version", (body) => { body.apiVersion = "aspm/v2"; }],
    ["wrong data origin", (body) => { body.dataOrigin = "synthetic"; }],
  ];
  for (const [name, mutate] of summaryScenarios) {
    await test.step(name, async () => {
      const malformed = structuredClone(syntheticSLAResponse(reportAlpha.id)) as any;
      mutate(malformed);
      const reply = reports.queueSLARaw(reportAlpha.id, malformed, true);
      await refreshSLA(page).click();
      await reply.requested;
      await exactSummary(page, baseline);
      await release(reply);
      await expect(sla(page).getByRole("alert")).toContainText(/invalid|replacement data|could not/i);
      await exactSummary(page, baseline);
    });
  }

  await openStatus(page, reports, "breached");
  const first = syntheticSLAFindingPage(reportAlpha.id, "breached");
  await exactFindingPage(page, first);
  const pageScenarios: Array<[string, (body: any) => void]> = [
    ["unknown page key", (body) => { body.offset = 0; }],
    ["descending finding order", (body) => { body.items.reverse(); }],
    ["duplicate finding ID", (body) => { body.items[1].findingId = body.items[0].findingId; }],
    ["wrong whole total", (body) => { body.total += 1; }],
    ["missing required cursor", (body) => { body.nextCursor = null; }],
    ["wrong cursor", (body) => { body.nextCursor = body.items[0].findingId; }],
    ["wrong requested status", (body) => { body.items[0].status = "within-target"; }],
    ["wrong target arithmetic", (body) => { body.items[0].targetDays += 1; }],
    ["wrong due arithmetic", (body) => {
      body.items[0].dueAt = new Date(Date.parse(body.items[0].dueAt) + 1_000).toISOString();
    }],
    ["wrong overdue arithmetic", (body) => { body.items[0].overdueSeconds += 1; }],
    ["invalid first observed", (body) => { body.items[0].firstObservedAt = "not-a-timestamp"; }],
    ["owner pair mismatch", (body) => { body.items[0].ownerName = null; }],
    ["unknown item key", (body) => { body.items[0].verified = true; }],
    ["wrong API version", (body) => { body.apiVersion = "aspm/v2"; }],
    ["wrong data origin", (body) => { body.dataOrigin = "synthetic"; }],
  ];
  for (const [name, mutate] of pageScenarios) {
    await test.step(name, async () => {
      const malformed = structuredClone(first) as any;
      mutate(malformed);
      const reply = reports.queueSLAFindingRaw(reportAlpha.id, "breached", malformed, true);
      await refreshFindings(page).click();
      await reply.requested;
      await exactFindingPage(page, first);
      await release(reply);
      await expect(findings(page).getByRole("alert")).toContainText(/invalid|replacement data|could not/i);
      await exactFindingPage(page, first);
    });
  }

  await editSLA(page).click();
  await editForm(page).getByLabel("Critical days", { exact: true }).fill("5");
  await editForm(page).getByLabel("Rationale", { exact: true }).fill("Strict acknowledgement validation.");
  const malformedAck = structuredClone(syntheticSLAPolicyResponse({
    ...alphaSLAPolicy, criticalDays: 5, revision: 5, rationale: "Strict acknowledgement validation.",
    updatedAt: "2026-10-08T17:02:51.000Z",
  })) as any;
  malformedAck.policy.forecast = false;
  const ack = reports.queueSLAPolicyUpdateRaw(reportAlpha.id, malformedAck, true);
  await saveSLA(page).click();
  await ack.requested;
  await release(ack);
  await expect(editForm(page).getByRole("alert")).toContainText(/invalid|acknowledgement|replacement/i);
  await expect(editForm(page).getByLabel("Critical days", { exact: true })).toHaveValue("5");
});

async function tabTo(page: Page, target: Locator, key = "Tab") {
  for (let index = 0; index < 60; index += 1) {
    if (await target.evaluate((element) => element === document.activeElement)) {
      await expect(target).toBeInViewport({ ratio: 0.99 });
      return;
    }
    await page.keyboard.press(key);
  }
  await expect(target, "The chosen SLA control must be keyboard reachable.").toBeFocused();
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

test("SLA summary, table and edit form remain reachable without overflow or focus theft at 390px", async ({ page, reports }) => {
  test.setTimeout(90_000);
  await page.setViewportSize({ width: 390, height: 844 });
  await reportsPage(page, reports);
  await openSLA(page, reports);
  await openStatus(page, reports, "breached");
  await exactFindingPage(page, syntheticSLAFindingPage(reportAlpha.id, "breached"));
  const results = findings(page).getByRole("region", { name: /sla finding results/i });
  await tabTo(page, refreshSLA(page));
  await tabTo(page, statusActivation(page, "within-target"));
  await tabTo(page, refreshFindings(page));
  await tabTo(page, results);
  await tabTo(page, loadMoreFindings(page));

  const held = reports.queueSLA(reportAlpha.id, { status: 200, value: syntheticSLASummary(reportAlpha.id) }, true);
  await refreshSLA(page).focus();
  await page.keyboard.press("Enter");
  await held.requested;
  const workspace = page.getByRole("combobox", { name: "Workspace", exact: true });
  await tabTo(page, workspace, "Shift+Tab");
  const scroll = await page.evaluate(() => ({ x: scrollX, y: scrollY }));
  await release(held);
  await expect(workspace, "Completing an SLA refresh must not steal deliberately moved focus.").toBeFocused();
  expect(await page.evaluate(() => ({ x: scrollX, y: scrollY }))).toEqual(scroll);
  await tabTo(page, editSLA(page));
  await editSLA(page).click();
  await tabTo(page, editForm(page).getByLabel("Critical days", { exact: true }));
  await tabTo(page, editForm(page).getByLabel("Rationale", { exact: true }));
  await tabTo(page, saveSLA(page));
  await expect(live(page)).toBeVisible();
  await reducedMovement(page);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= document.documentElement.clientWidth + 1),
    "390px Reports must contain SLA controls, forms and scrollable tables without page-level overflow.").toBe(true);
  const stored = await page.evaluate(() => JSON.stringify([...Object.entries(localStorage), ...Object.entries(sessionStorage)]));
  for (const canary of slaStorageCanaries) expect(stored).not.toContain(canary);
});
