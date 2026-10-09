import type { Locator, Page } from "@playwright/test";
import {
  actionAlpha, actionBeta, betaFinding, companionFinding, primaryFinding, serverNow, workItem,
} from "./finding-actions-data";
import type { ActionFinding, ActionWorkItem, ActionWorkResponse } from "./finding-actions-data";
import { expect, test } from "./finding-actions-fixture";
import type { FindingActionControl } from "./finding-actions-fixture";
import { requireProductionUI } from "./network";

test.use({ reducedMotion: "reduce", timezoneId: "UTC" });
test.beforeEach(async ({ actions }) => {
  requireProductionUI();
  expect(actions.requests).toEqual([]);
});

const bulkPath = "/api/v1/findings";
const futureExpiry = "2026-10-10T12:00:00Z";
const riskRationale = "Accept the selected synthetic findings after a bounded current-revision review.";

function queue(page: Page) {
  return page.getByRole("table", { name: "Findings", exact: true, includeHidden: true });
}

function row(page: Page, finding: ActionFinding) {
  return queue(page).getByRole("row", { includeHidden: true }).filter({ hasText: finding.title });
}

function selection(page: Page, finding: ActionFinding) {
  return row(page, finding).getByRole("checkbox", { name: `Select ${finding.title}`, exact: true, includeHidden: true });
}

function bulkForm(page: Page) {
  return page.getByRole("form", { name: "Bulk triage selected findings", exact: true });
}

function bulkAction(form: Locator) {
  return form.getByRole("combobox", { name: "Action", exact: true });
}

function bulkRationale(form: Locator) {
  return form.getByRole("textbox", { name: "Rationale", exact: true });
}

function riskExpiry(form: Locator) {
  return form.getByLabel(/^(?:Accepted risk|Risk acceptance) expir(?:y|es at)(?: \(.*\))?$/i);
}

function apply(form: Locator) {
  return form.getByRole("button", { name: "Apply", exact: true });
}

async function selectRisk(form: Locator) {
  const select = bulkAction(form);
  const option = select.locator("option").filter({ hasText: /^Accept risk$/i });
  await expect(option, "The selected-findings toolbar must add exactly one Accept risk action.").toHaveCount(1);
  const value = await option.getAttribute("value");
  if (!value) throw new Error("Accept risk needs a native option value.");
  await select.selectOption(value);
  await expect(form).toContainText(/one[\s\S]{0,80}finding-scoped[\s\S]{0,80}immutable approval[\s\S]{0,80}(?:per|for each) finding/i);
  await expect(form).toContainText(/does not verify safety|not (?:a )?verification of safety|does not prove safety/i);
  await expect(riskExpiry(form)).toBeVisible();
}

async function fillExpiry(field: Locator, value: string) {
  const type = await field.getAttribute("type");
  await field.fill(type === "datetime-local" ? value.replace(/:00Z$/, "") : value);
}

async function selectPair(page: Page) {
  for (const finding of [companionFinding, primaryFinding]) await selection(page, finding).check();
}

async function requested(control: FindingActionControl) {
  await expect.poll(() => control.call !== null).toBe(true);
  if (!control.call) throw new Error("Expected a captured bulk request.");
  return control.call;
}

async function release(control: FindingActionControl) {
  control.release();
  await control.delivered;
}

function expectedRiskBody(expiry: string | null, rationale = riskRationale) {
  return {
    findingIds: [companionFinding.id, primaryFinding.id],
    decisionRevisions: {
      [companionFinding.id]: companionFinding.decisionRevision,
      [primaryFinding.id]: primaryFinding.decisionRevision,
    },
    disposition: "accepted-risk",
    acceptedRiskExpiresAt: expiry,
    rationale,
  };
}

async function prepareRisk(page: Page, expiry = futureExpiry, rationale = riskRationale) {
  await page.goto("/#/work");
  await expect(queue(page)).toBeVisible();
  await selectPair(page);
  const form = bulkForm(page);
  await selectRisk(form);
  if (expiry !== "") await fillExpiry(riskExpiry(form), expiry);
  await bulkRationale(form).fill(rationale);
  return form;
}

async function expectDraftAndSelection(page: Page, expiry: string, rationale: string) {
  const form = bulkForm(page);
  await expect(selection(page, companionFinding)).toBeChecked();
  await expect(selection(page, primaryFinding)).toBeChecked();
  await expect(bulkRationale(form)).toHaveValue(rationale);
  const type = await riskExpiry(form).getAttribute("type");
  await expect(riskExpiry(form)).toHaveValue(type === "datetime-local" ? expiry.replace(/:00Z$/, "") : expiry);
  for (const finding of [companionFinding, primaryFinding]) {
    await expect(row(page, finding)).not.toContainText(/Accepted risk/i);
  }
}

async function expectProtectedGone(page: Page) {
  await expect(queue(page)).toHaveCount(0);
  await expect(page.locator("body")).not.toContainText(primaryFinding.title);
  await expect(page.locator("body")).not.toContainText(riskRationale);
}

function acceptedItem(finding: ActionFinding, expiry: string | null): ActionWorkItem {
  return workItem({
    ...finding,
    decisionRevision: finding.decisionRevision + 1,
    disposition: "accepted-risk",
    acceptedRiskExpiresAt: expiry,
    riskAcceptanceExpired: false,
  });
}

test("V25W1 Accept risk submits exact revisions, updates canonical Work, and keeps owner/workflow bodies unchanged", async ({ page, actions }) => {
  await page.goto("/#/work");
  const filter = page.getByRole("textbox", { name: "Filter findings", exact: true });
  await filter.fill("triage-alpha");
  await selectPair(page);
  let form = bulkForm(page);
  await selectRisk(form);
  await fillExpiry(riskExpiry(form), futureExpiry);
  await bulkRationale(form).fill(riskRationale);
  await apply(form).click();

  await expect.poll(() => actions.calls("PATCH", bulkPath).at(-1)?.status).toBe(200);
  expect(actions.calls("PATCH", bulkPath).at(-1)?.body).toEqual(expectedRiskBody(futureExpiry));
  await expect(page.getByRole("status").filter({ hasText: /2 selected findings updated/i })).toBeVisible();
  for (const finding of [companionFinding, primaryFinding]) {
    await expect(row(page, finding)).toContainText(/Accepted risk/i);
    await expect(selection(page, finding)).not.toBeChecked();
  }
  await expect(page.getByText(/(?:Accepted risk(?: findings)?\s*:?\s*2|2\s+(?:loaded\s+)?findings?\s+(?:are\s+)?accepted risk)/i).first()).toBeVisible();
  await expect(filter).toHaveValue("triage-alpha");
  await expect(bulkForm(page)).toHaveCount(0);
  await expect.poll(() => page.evaluate(() => {
    const active = document.activeElement, main = document.querySelector("main");
    return active !== null && active !== document.body && main?.contains(active) === true;
  }), "Completed bulk work must leave focus in the current Work context.").toBe(true);

  await selectPair(page);
  form = bulkForm(page);
  await bulkAction(form).selectOption("pending-retest");
  const workflowRationale = "Keep the exact established owner/workflow bulk request body.";
  await bulkRationale(form).fill(workflowRationale);
  await apply(form).click();
  await expect.poll(() => actions.calls("PATCH", bulkPath).length).toBe(2);
  expect(actions.calls("PATCH", bulkPath).at(-1)?.body).toEqual({
    findingIds: [companionFinding.id, primaryFinding.id],
    workflowState: "pending-retest",
    rationale: workflowRationale,
  });
  for (const key of ["decisionRevisions", "disposition", "acceptedRiskExpiresAt"]) {
    expect(actions.calls("PATCH", bulkPath).at(-1)?.body).not.toHaveProperty(key);
  }
  for (const finding of [companionFinding, primaryFinding]) {
    await expect(row(page, finding)).toContainText(/Accepted risk/i);
  }

  actions.seedFinding(primaryFinding);
  actions.seedFinding(companionFinding);
  await page.reload();
  await selection(page, primaryFinding).check();
  form = bulkForm(page);
  await selectRisk(form);
  const nullRationale = "Accept one selected finding with an explicit no-expiry decision.";
  await bulkRationale(form).fill(nullRationale);
  await apply(form).click();
  await expect.poll(() => actions.calls("PATCH", bulkPath).length).toBe(3);
  expect(actions.calls("PATCH", bulkPath).at(-1)?.body).toEqual({
    findingIds: [primaryFinding.id],
    decisionRevisions: { [primaryFinding.id]: primaryFinding.decisionRevision },
    disposition: "accepted-risk",
    acceptedRiskExpiresAt: null,
    rationale: nullRationale,
  });
});

test("V25W2 Invalid drafts send no HTTP, viewers have no action, and 101 selected stays disabled at 390px", async ({ page, actions }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await page.emulateMedia({ reducedMotion: "reduce" });
  await page.goto("/#/work");
  await expect(bulkForm(page)).toHaveCount(0);
  expect(actions.calls("PATCH", bulkPath)).toHaveLength(0);
  await selection(page, primaryFinding).check();
  let form = bulkForm(page);
  await selectRisk(form);
  const patchCount = actions.calls("PATCH", bulkPath).length;

  await bulkRationale(form).fill(" \n\t ");
  await expect(apply(form)).toBeDisabled();
  await expect.poll(() => actions.calls("PATCH", bulkPath).length).toBe(patchCount);

  await bulkRationale(form).fill("é".repeat(4097));
  await expect(apply(form)).toBeDisabled();
  await expect.poll(() => actions.calls("PATCH", bulkPath).length).toBe(patchCount);

  await bulkRationale(form).fill("Reject a nonfuture expiry without sending HTTP.");
  await fillExpiry(riskExpiry(form), serverNow);
  if (await apply(form).isEnabled()) await apply(form).click();
  await expect.poll(() => actions.calls("PATCH", bulkPath).length).toBe(patchCount);

  actions.roles.set(actionAlpha.id, "viewer");
  actions.serverRoles.set(actionAlpha.id, "viewer");
  await page.reload();
  await selection(page, primaryFinding).check();
  await expect(bulkForm(page)).toHaveCount(0);
  await expect(page.getByRole("option", { name: "Accept risk", exact: true })).toHaveCount(0);
  await expect(page.getByText(/Read-only selection/i)).toBeVisible();

  actions.roles.set(actionAlpha.id, "analyst");
  actions.serverRoles.set(actionAlpha.id, "analyst");
  actions.seedQueueRows(99);
  await page.reload();
  await page.getByRole("button", { name: "Load more findings", exact: true }).click();
  await expect(page.getByLabel("Finding pagination", { exact: true })).toContainText("101 loaded findings");
  for (let index = 0; index < 3; index++) {
    await page.getByRole("checkbox", { name: "Select all findings on this page", exact: true }).check();
    if (index < 2) await page.getByRole("button", { name: "Next", exact: true }).click();
  }
  await expect(page.getByText(/^101$/).first()).toBeVisible();
  form = bulkForm(page);
  await selectRisk(form);
  await bulkRationale(form).fill("The over-limit selection must not submit.");
  await expect(apply(form)).toBeDisabled();
  await expect(form).toContainText(/limited to 100 selected findings/i);
  await expect.poll(() => actions.calls("PATCH", bulkPath).length).toBe(patchCount);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1),
    "The V25 selected-findings control must not overflow at 390px.").toBe(true);
});

test("V25W3 Stale 409, current-role 403 and 503 retain the draft, while 401 clears protected state", async ({ page, actions }) => {
  let form = await prepareRisk(page);
  actions.seedFinding({ ...primaryFinding, decisionRevision: primaryFinding.decisionRevision + 1 });
  await apply(form).click();
  await expect.poll(() => actions.calls("PATCH", bulkPath).at(-1)?.status).toBe(409);
  expect(actions.calls("PATCH", bulkPath).at(-1)?.body).toEqual(expectedRiskBody(futureExpiry));
  await expect(form.getByRole("alert")).toContainText(/changed|current|conflict|review/i);
  await expectDraftAndSelection(page, futureExpiry, riskRationale);
  expect(actions.calls("PATCH", bulkPath)).toHaveLength(1);

  actions.serverRoles.set(actionAlpha.id, "viewer");
  await apply(form).click();
  await expect.poll(() => actions.calls("PATCH", bulkPath).at(-1)?.status).toBe(403);
  await expect(form.getByRole("alert")).toContainText(/denied|permission|forbidden/i);
  await expectDraftAndSelection(page, futureExpiry, riskRationale);
  expect(actions.calls("PATCH", bulkPath)).toHaveLength(2);

  actions.serverRoles.set(actionAlpha.id, "analyst");
  const unavailable = actions.queueBulk(503);
  await apply(form).click();
  expect((await requested(unavailable)).body).toEqual(expectedRiskBody(futureExpiry));
  await unavailable.delivered;
  await expect(form.getByRole("alert")).toContainText(/could not|unavailable|try again/i);
  await expectDraftAndSelection(page, futureExpiry, riskRationale);
  expect(actions.calls("PATCH", bulkPath)).toHaveLength(3);

  const expired = actions.queueBulk(401);
  await apply(form).click();
  expect((await requested(expired)).body).toEqual(expectedRiskBody(futureExpiry));
  await expect(page.getByRole("form", { name: "Sign in", exact: true })).toBeVisible();
  await expectProtectedGone(page);
  await page.reload();
  await expect(page.getByRole("form", { name: "Sign in", exact: true })).toBeVisible();
  await expectProtectedGone(page);
});

test("V25W4 Workspace change and logout abort held bulk risk work and discard late acknowledgements", async ({ page, actions }) => {
  for (const boundary of ["workspace", "logout"] as const) await test.step(boundary, async () => {
    actions.seedFinding(primaryFinding);
    actions.seedFinding(companionFinding);
    if (!actions.authenticated) throw new Error("The logout boundary is last and must not be reused as a hidden login shortcut.");
    await page.goto("/#/work");
    const workspace = page.getByRole("combobox", { name: "Workspace", exact: true });
    await workspace.selectOption(actionAlpha.id);
    const form = await prepareRisk(page);
    const held = actions.queueBulk(200, true);
    await apply(form).click();
    expect((await requested(held)).body).toEqual(expectedRiskBody(futureExpiry));

    if (boundary === "workspace") {
      const beta = actions.holdWork(actionBeta.id);
      await workspace.selectOption(actionBeta.id);
      await requested(beta);
      await expect(page.locator("body")).not.toContainText(primaryFinding.title);
      await expect(page.locator("body")).not.toContainText(riskRationale);
      await release(held);
      await expect.poll(() => held.call?.failure).toMatch(/abort/i);
      await expect(page.getByText(/Accepted risk findings:\s*0/i)).toBeVisible();
      await expect(row(page, betaFinding)).not.toContainText(/Accepted risk/i);
      await release(beta);
      await expect(row(page, betaFinding)).toBeVisible();
      await expect(page.locator("body")).not.toContainText(primaryFinding.title);
    } else {
      await page.getByRole("button", { name: "Sign out", exact: true }).click();
      await expect(page.getByRole("form", { name: "Sign in", exact: true })).toBeVisible();
      await expectProtectedGone(page);
      await release(held);
      await expect.poll(() => held.call?.failure).toMatch(/abort/i);
      await expectProtectedGone(page);
    }
  });
});

test("V25W5 Strict bulk response parsing rejects membership, order, duplicate, cursor, and decision metadata faults", async ({ page, actions }) => {
  const acceptedCompanion = acceptedItem(companionFinding, futureExpiry);
  const acceptedPrimary = acceptedItem(primaryFinding, futureExpiry);
  const cases: { name: string; response: Record<string, unknown> | ActionWorkResponse }[] = [
    {
      name: "missing member",
      response: { apiVersion: "aspm/v1alpha1", dataOrigin: "synthetic", items: [acceptedCompanion], total: 1, nextCursor: null },
    },
    {
      name: "extra member",
      response: {
        apiVersion: "aspm/v1alpha1", dataOrigin: "synthetic",
        items: [acceptedCompanion, acceptedPrimary, workItem(betaFinding)], total: 3, nextCursor: null,
      },
    },
    {
      name: "duplicate member",
      response: {
        apiVersion: "aspm/v1alpha1", dataOrigin: "synthetic",
        items: [acceptedCompanion, acceptedCompanion], total: 2, nextCursor: null,
      },
    },
    {
      name: "out of order",
      response: {
        apiVersion: "aspm/v1alpha1", dataOrigin: "synthetic",
        items: [acceptedPrimary, acceptedCompanion], total: 2, nextCursor: null,
      },
    },
    {
      name: "nonnull cursor",
      response: {
        apiVersion: "aspm/v1alpha1", dataOrigin: "synthetic",
        items: [acceptedCompanion, acceptedPrimary], total: 2, nextCursor: companionFinding.id,
      },
    },
    {
      name: "invalid decision metadata",
      response: {
        apiVersion: "aspm/v1alpha1", dataOrigin: "synthetic",
        items: [{ ...acceptedCompanion, decisionRevision: 0 }, acceptedPrimary], total: 2, nextCursor: null,
      },
    },
  ];

  for (const scenario of cases) await test.step(scenario.name, async () => {
    actions.seedFinding(primaryFinding);
    actions.seedFinding(companionFinding);
    await page.goto("/#/work");
    const form = await prepareRisk(page);
    const malformed = actions.queueBulk(200, false, scenario.response);
    await apply(form).click();
    expect((await requested(malformed)).body).toEqual(expectedRiskBody(futureExpiry));
    await malformed.delivered;
    await expect(form.getByRole("alert")).toContainText(/invalid|response|unexpected|could not/i);
    await expectDraftAndSelection(page, futureExpiry, riskRationale);
    await expect(page.getByRole("status").filter({ hasText: /selected findings updated/i })).toHaveCount(0);
    await expect(page.locator("body")).not.toContainText(/d100000000000000[0-9a-f]{16}/i);
  });
});
