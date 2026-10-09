import type { Locator, Page } from "@playwright/test";
import { requireProductionUI } from "./network";
import {
  approvalsPath, canonicalFixture, evidencePath, initialApproval, initialJob, jobsPath,
  verificationAlpha, verificationBeta, verificationEnvironment, verificationExpiresAt,
  verificationFinding, verificationMethod, verificationRationale,
  verificationRevocationRationale, verificationSchema, verificationScopeRevision,
} from "./deterministic-verification-data";
import type { VerificationJob } from "./deterministic-verification-data";
import { expect, test } from "./deterministic-verification-fixture";
import type {
  DeterministicVerificationAPI, VerificationControl,
} from "./deterministic-verification-fixture";

test.use({ reducedMotion: "reduce", timezoneId: "UTC" });
test.beforeEach(async ({ verificationAPI }) => {
  requireProductionUI();
  expect(verificationAPI.requests).toEqual([]);
});

function findingDialog(page: Page) {
  return page.getByRole("dialog", { name: verificationFinding.title, exact: true, includeHidden: true });
}
function findingsTable(page: Page) {
  return page.getByRole("table", { name: "Findings", exact: true, includeHidden: true });
}
function findingRow(page: Page) {
  return findingsTable(page).getByRole("row", { includeHidden: true }).filter({ hasText: verificationFinding.title });
}
function findingTrigger(page: Page) {
  return findingRow(page).getByRole("button", { name: verificationFinding.title, exact: true, includeHidden: true })
    .or(findingRow(page).getByRole("link", { name: verificationFinding.title, exact: true, includeHidden: true }));
}
function toggle(page: Page) {
  return findingDialog(page).getByRole("button", { name: "Show deterministic verification", exact: true })
    .or(findingDialog(page).getByRole("button", { name: "Deterministic verification", exact: true }));
}
function verificationRegion(page: Page) {
  return findingDialog(page).getByRole("region", { name: "Deterministic verification", exact: true, includeHidden: true });
}
function evidenceForm(page: Page) {
  return verificationRegion(page).getByRole("form", { name: "Submit synthetic fixture", exact: true });
}
function approvalForm(page: Page) {
  return verificationRegion(page).getByRole("form", { name: "Approve synthetic fixture", exact: true });
}
function queueForm(page: Page) {
  return verificationRegion(page).getByRole("form", { name: "Queue deterministic verification", exact: true });
}
function historyTable(page: Page) {
  return verificationRegion(page).getByRole("table", { name: "Verification history", exact: true, includeHidden: true });
}
function historyRows(page: Page) {
  return historyTable(page).getByRole("row").filter({ has: page.getByRole("cell") });
}
function refreshHistory(page: Page) {
  return verificationRegion(page).getByRole("button", { name: "Refresh verification history", exact: true });
}
function loadMore(page: Page) {
  return verificationRegion(page).getByRole("button", { name: "Load more verification history", exact: true });
}
function detail(page: Page) {
  return verificationRegion(page).getByRole("region", { name: "Selected verification", exact: true, includeHidden: true });
}
function refreshDetail(page: Page) {
  return detail(page).getByRole("button", { name: "Refresh verification", exact: true });
}
function openJobAction(page: Page, job: VerificationJob) {
  const row = historyTable(page).getByRole("row").filter({ hasText: job.id });
  return row.getByRole("button", { name: "Open verification", exact: true })
    .or(row.getByRole("link", { name: "Open verification", exact: true }));
}

async function frame(page: Page) {
  await page.evaluate(() => new Promise<void>((resolve) =>
    requestAnimationFrame(() => requestAnimationFrame(() => resolve()))));
}

async function requested(control: VerificationControl) {
  await expect.poll(() => control.call !== null,
    "The production verification UI must issue the explicitly requested operation.").toBe(true);
  return control.call!;
}

async function release(page: Page, control: VerificationControl, aborted = false) {
  control.release();
  await control.delivered;
  await frame(page);
  if (aborted) {
    await expect.poll(() => control.call?.failure,
      "Scope loss or a synthetic lost acknowledgement must abort the old request.").toMatch(/abort|failed/i);
  }
}

async function openFinding(page: Page) {
  await page.goto("/#/work");
  const workspace = page.getByRole("combobox", { name: "Workspace", exact: true });
  await workspace.selectOption(verificationAlpha.id);
  await expect(workspace).toHaveValue(verificationAlpha.id);
  await expect(findingsTable(page)).toBeVisible();
  await findingTrigger(page).click();
  await expect(findingDialog(page)).toBeVisible();
  await expect(findingDialog(page)).toContainText(verificationFinding.description);
}

async function openVerification(page: Page, api: DeterministicVerificationAPI) {
  const before = new Map([
    [evidencePath, api.calls("GET", evidencePath).length],
    [approvalsPath, api.calls("GET", approvalsPath).length],
    [jobsPath, api.calls("GET", jobsPath).length],
  ]);
  await expect(toggle(page)).toBeVisible();
  await toggle(page).click();
  await expect(verificationRegion(page)).toBeVisible();
  await expect.poll(() => api.calls("GET", evidencePath).length).toBe(before.get(evidencePath)! + 1);
  await expect.poll(() => api.calls("GET", approvalsPath).length).toBe(before.get(approvalsPath)! + 1);
  await expect.poll(() => api.calls("GET", jobsPath).length).toBe(before.get(jobsPath)! + 1);
  for (const path of [evidencePath, approvalsPath, jobsPath]) {
    expect(api.calls("GET", path).at(-1)!.query).toEqual({ limit: "100" });
  }
  await expect(verificationRegion(page)).toContainText(
    "Synthetic fixture reproduction is not proof of a real vulnerability and does not close or classify the finding.");
}

async function submitFixture(page: Page, api: DeterministicVerificationAPI, condition = true) {
  const before = api.calls("POST", evidencePath).length;
  await evidenceForm(page).getByLabel("Environment", { exact: true }).fill(verificationEnvironment);
  await evidenceForm(page).getByLabel("Scope revision", { exact: true }).fill(verificationScopeRevision);
  const conditionControl = evidenceForm(page).getByLabel("Fixture condition", { exact: true });
  await conditionControl.selectOption(condition ? "true" : "false");
  await evidenceForm(page).getByRole("button", { name: "Submit fixture", exact: true }).click();
  await expect.poll(() => api.calls("POST", evidencePath).length).toBe(before + 1);
  const call = api.calls("POST", evidencePath).at(-1)!;
  expect(call.body).toEqual({
    method: verificationMethod,
    environmentId: verificationEnvironment,
    scopeRevision: verificationScopeRevision,
    fixture: {
      schema: verificationSchema,
      environmentId: verificationEnvironment,
      condition,
    },
  });
  const item = [...api.evidence.values()].at(-1)!;
  await expect(verificationRegion(page).getByText(item.id, { exact: true })).toBeVisible();
  return item;
}

async function approveFixture(page: Page, api: DeterministicVerificationAPI, evidenceId: string) {
  const before = api.calls("POST", approvalsPath).length;
  await approvalForm(page).getByLabel("Verification evidence", { exact: true }).selectOption(evidenceId);
  await approvalForm(page).getByLabel("Approval expires at", { exact: true }).fill("2026-10-09T12:00");
  await approvalForm(page).getByLabel("Approval rationale", { exact: true }).fill(verificationRationale);
  await approvalForm(page).getByRole("button", { name: "Approve fixture", exact: true }).click();
  await expect.poll(() => api.calls("POST", approvalsPath).length).toBe(before + 1);
  expect(api.calls("POST", approvalsPath).at(-1)!.body).toEqual({
    evidenceId, rationale: verificationRationale, expiresAt: verificationExpiresAt,
  });
  const item = [...api.approvals.values()].at(-1)!;
  await expect(verificationRegion(page).getByText(item.id, { exact: true })).toBeVisible();
  return item;
}

async function queueFixture(page: Page, api: DeterministicVerificationAPI, approvalId: string) {
  const before = api.calls("POST", jobsPath).length;
  await queueForm(page).getByLabel("Current approval", { exact: true }).selectOption(approvalId);
  await queueForm(page).getByRole("button", { name: "Queue verification", exact: true }).click();
  await expect.poll(() => api.calls("POST", jobsPath).length).toBe(before + 1);
  const call = api.calls("POST", jobsPath).at(-1)!;
  expect(Object.keys(call.body).sort()).toEqual(["approvalId", "idempotencyKey"]);
  expect(call.body).toMatchObject({ approvalId, idempotencyKey: expect.any(String) });
  const key = String(call.body.idempotencyKey);
  expect(key.trim()).not.toBe("");
  expect(key).not.toContain("\0");
  expect(Buffer.byteLength(key, "utf8")).toBeLessThanOrEqual(256);
  return call;
}

async function expectFindingUnchanged(page: Page) {
  await expect(findingDialog(page)).toContainText(/Open/);
  await expect(findingDialog(page)).toContainText(/No disposition|None/i);
  await expect(findingDialog(page).getByText("Not verified", { exact: true }).first()).toBeVisible();
  await expect(findingDialog(page)).toContainText(verificationFinding.description);
}

test("DV1 Admin submits exact fixture bytes, explicitly approves and queues, then manually refreshes a safe result", async ({ page, verificationAPI }) => {
  await page.clock.install();
  await openFinding(page);
  await expect(verificationRegion(page)).toHaveCount(0);
  const quietBefore = verificationAPI.requests.length;
  await page.clock.fastForward(10_000);
  await frame(page);
  expect(verificationAPI.requests).toHaveLength(quietBefore);
  await openVerification(page, verificationAPI);

  const evidence = await submitFixture(page, verificationAPI, true);
  expect(evidence.sizeBytes).toBe(Buffer.byteLength(canonicalFixture, "utf8"));
  const approval = await approveFixture(page, verificationAPI, evidence.id);
  const queued = await queueFixture(page, verificationAPI, approval.id);
  const job = [...verificationAPI.jobs.values()].at(-1)!;
  await expect(detail(page)).toContainText(/queued/i);
  await expect(detail(page)).not.toContainText(/reproduced|not-reproduced/i);
  await expectFindingUnchanged(page);

  const callsAfterQueue = verificationAPI.requests.length;
  await page.clock.fastForward(30_000);
  await frame(page);
  expect(verificationAPI.requests, "Queued verification must not poll or auto-run.").toHaveLength(callsAfterQueue);
  expect(verificationAPI.calls("POST", jobsPath)).toHaveLength(1);
  expect(queued.body.idempotencyKey).toBeTruthy();

  verificationAPI.setJobState(job.id, "processing");
  await refreshDetail(page).click();
  await expect(detail(page)).toContainText(/processing/i);
  verificationAPI.setJobState(job.id, "succeeded");
  await refreshDetail(page).click();
  await expect(detail(page)).toContainText("reproduced");
  await expect(detail(page)).toContainText(job.evidenceDigest);
  await expect(detail(page)).toContainText(job.environmentId);
  await expect(detail(page)).toContainText(job.scopeRevision);
  await expect(detail(page)).toContainText(/does not close|not closure/i);
  await expect(detail(page)).not.toContainText(/close finding: true|false positive: true/i);
  await expectFindingUnchanged(page);

  await approvalForm(page).getByLabel("Verification evidence", { exact: true }).selectOption(evidence.id);
  await expect(verificationRegion(page).getByRole("button", { name: "Revoke approval", exact: true })).toBeVisible();
  await verificationRegion(page).getByLabel("Revocation rationale", { exact: true }).fill(verificationRevocationRationale);
  await verificationRegion(page).getByRole("button", { name: "Revoke approval", exact: true }).click();
  const revokePath = `${approvalsPath}/${approval.id}/revoke`;
  await expect.poll(() => verificationAPI.calls("POST", revokePath).length).toBe(1);
  expect(verificationAPI.calls("POST", revokePath)[0].body).toEqual({ rationale: verificationRevocationRationale });
  await expect(verificationRegion(page)).toContainText(/revoked|not current/i);
  await expectFindingUnchanged(page);
});

test("DV2 Analyst can submit and queue but cannot approve or revoke; viewer reads manual native history only", async ({ page, verificationAPI }) => {
  verificationAPI.roles.set(verificationAlpha.id, "analyst");
  verificationAPI.serverRoles.set(verificationAlpha.id, "analyst");
  await openFinding(page);
  await openVerification(page, verificationAPI);
  await expect(evidenceForm(page)).toBeVisible();
  await expect(queueForm(page)).toBeVisible();
  await expect(approvalForm(page)).toHaveCount(0);
  await expect(verificationRegion(page).getByRole("button", { name: "Revoke approval", exact: true })).toHaveCount(0);
  await submitFixture(page, verificationAPI, false);
  await queueFixture(page, verificationAPI, initialApproval.id);
  expect(verificationAPI.calls("POST", approvalsPath)).toEqual([]);

  verificationAPI.roles.set(verificationAlpha.id, "viewer");
  verificationAPI.serverRoles.set(verificationAlpha.id, "viewer");
  await page.reload();
  await openVerification(page, verificationAPI);
  await expect(evidenceForm(page)).toHaveCount(0);
  await expect(approvalForm(page)).toHaveCount(0);
  await expect(queueForm(page)).toHaveCount(0);
  await expect(historyTable(page)).toBeVisible();
  await openJobAction(page, initialJob).click();
  await expect(detail(page)).toContainText(initialJob.id);
  await expect(detail(page)).toContainText(/queued/i);
});

test("DV3 History pages only on manual actions and a lost queue acknowledgement retains one key until explicit reconciliation", async ({ page, verificationAPI }) => {
  verificationAPI.seedJobs(201);
  await openFinding(page);
  await openVerification(page, verificationAPI);
  expect(verificationAPI.jobPages[0].response.items).toHaveLength(100);
  await expect(historyRows(page)).toHaveCount(100);
  const firstCursor = verificationAPI.jobPages[0].response.nextCursor!;
  await loadMore(page).click();
  await expect.poll(() => verificationAPI.jobPages.length).toBe(2);
  expect(verificationAPI.jobPages[1].call.query).toEqual({ limit: "100", cursor: firstCursor });
  await expect(historyRows(page)).toHaveCount(200);
  const callsBeforeWait = verificationAPI.calls("GET", jobsPath).length;
  await page.waitForTimeout(150);
  expect(verificationAPI.calls("GET", jobsPath)).toHaveLength(callsBeforeWait);
  await loadMore(page).click();
  await expect.poll(() => verificationAPI.jobPages.length).toBe(3);
  await expect(historyRows(page)).toHaveCount(201);
  await expect(loadMore(page)).toBeDisabled();

  verificationAPI.seedJobs(0);
  await refreshHistory(page).click();
  await expect(historyRows(page)).toHaveCount(0);
  const lost = verificationAPI.loseNextQueueAcknowledgement(true);
  await queueForm(page).getByLabel("Current approval", { exact: true }).selectOption(initialApproval.id);
  await queueForm(page).getByRole("button", { name: "Queue verification", exact: true }).click();
  const lostCall = await requested(lost);
  expect(lostCall.body).toMatchObject({ approvalId: initialApproval.id, idempotencyKey: expect.any(String) });
  const originalKey = String(lostCall.body.idempotencyKey);
  await release(page, lost, true);
  await expect(verificationRegion(page).getByRole("alert")).toContainText(/unresolved|not confirmed|acknowledg/i);
  const callsAfterLoss = verificationAPI.requests.length;
  await page.waitForTimeout(150);
  expect(verificationAPI.requests).toHaveLength(callsAfterLoss);
  expect(verificationAPI.calls("POST", jobsPath)).toHaveLength(1);
  await refreshHistory(page).click();
  await expect(historyRows(page)).toHaveCount(1);
  await expect(verificationRegion(page).getByRole("alert")).toHaveCount(0);
  expect(verificationAPI.calls("POST", jobsPath)).toHaveLength(1);
  expect(originalKey).toBeTruthy();
});

async function expectWithheld(scope: Locator) {
  await expect(scope.getByText(initialJob.id, { exact: true })).toHaveCount(0);
  await expect(scope).not.toContainText(initialJob.evidenceDigest);
  await expect(scope).not.toContainText(initialJob.environmentId);
  await expect(scope).not.toContainText(initialJob.scopeRevision);
}

test("DV4 Denials latch, malformed success is rejected, and workspace or session changes abort and clear old scope", async ({ page, verificationAPI }) => {
  await openFinding(page);
  await openVerification(page, verificationAPI);
  await openJobAction(page, initialJob).click();
  await expect(detail(page)).toContainText(initialJob.id);

  const denied = verificationAPI.queueJobDetail(initialJob.id, 403);
  await refreshDetail(page).click();
  await denied.delivered;
  await expect(detail(page).getByRole("alert")).toContainText(/denied|permission|forbidden/i);
  await expectWithheld(detail(page));
  const unavailable = verificationAPI.queueJobDetail(initialJob.id, 503);
  await refreshDetail(page).click();
  await unavailable.delivered;
  await expectWithheld(detail(page));
  const restored = verificationAPI.queueJobDetail(initialJob.id, 200, false, {
    apiVersion: "aspm/v1alpha1", dataOrigin: "synthetic", verification: structuredClone(initialJob),
  });
  await refreshDetail(page).click();
  await restored.delivered;
  await expect(detail(page)).toContainText(initialJob.id);

  const rowsBefore = await historyRows(page).allTextContents();
  const malformed = verificationAPI.queueJobHistory(200, false, {
    apiVersion: "aspm/v1alpha1", dataOrigin: "synthetic",
    items: [{ ...structuredClone(initialJob), result: {
      method: verificationMethod, environmentId: initialJob.environmentId,
      scopeRevision: initialJob.scopeRevision, evidenceId: initialJob.evidenceId,
      evidenceDigest: initialJob.evidenceDigest, outcome: "reproduced",
      closeFinding: true, falsePositive: false,
    } }],
    total: 1, nextCursor: null,
  });
  await refreshHistory(page).click();
  await malformed.delivered;
  await expect(verificationRegion(page).getByRole("alert")).toContainText(/invalid|could not|unexpected/i);
  expect(await historyRows(page).allTextContents()).toEqual(rowsBefore);

  const held = verificationAPI.queueJobHistory(200, true);
  await refreshHistory(page).click();
  await requested(held);
  await page.getByRole("combobox", { name: "Workspace", exact: true }).selectOption(verificationBeta.id);
  await release(page, held, true);
  await expect(findingDialog(page)).toHaveCount(0);
  await expect(page.locator("body")).not.toContainText(verificationFinding.title);
  await expect(page.locator("body")).not.toContainText(initialJob.id);

  await page.getByRole("combobox", { name: "Workspace", exact: true }).selectOption(verificationAlpha.id);
  await findingTrigger(page).click();
  await toggle(page).click();
  const expired = verificationAPI.queueJobHistory(401);
  await refreshHistory(page).click();
  await expired.delivered;
  await expect(page.getByRole("form", { name: "Sign in", exact: true })).toBeVisible();
  await expect(page.locator("body")).not.toContainText(initialJob.id);
  await expect(page.locator("body")).not.toContainText(verificationFinding.evidence.text);
});

async function reducedMovement(page: Page) {
  const failures = await page.evaluate(async () => {
    const values = new Set<string>();
    for (let index = 0; index < 5; index += 1) {
      for (const animation of document.getAnimations()) {
        if (animation.playState !== "running" || !(animation.effect instanceof KeyframeEffect)) continue;
        if (animation.effect.getTiming().iterations === Infinity) values.add("continuous");
        const frames = animation.effect.getKeyframes() as Array<Record<string, unknown>>;
        for (const key of ["transform", "translate", "rotate", "scale", "left", "top"]) {
          if (new Set(frames.map((frame) => frame[key]).filter((value) => value !== undefined).map(String)).size > 1) {
            values.add(key);
          }
        }
      }
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
    }
    return [...values];
  });
  expect(failures).toEqual([]);
}

test("DV5 The 390px reduced-motion finding detail keeps manual controls usable and returns focus on close", async ({ page, verificationAPI }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await openFinding(page);
  await openVerification(page, verificationAPI);
  await expect(verificationRegion(page)).toBeVisible();
  await expect(historyTable(page)).toBeVisible();
  const overflow = await page.evaluate(() => ({
    body: document.body.scrollWidth - document.body.clientWidth,
    root: document.documentElement.scrollWidth - document.documentElement.clientWidth,
  }));
  expect(overflow.body).toBeLessThanOrEqual(1);
  expect(overflow.root).toBeLessThanOrEqual(1);
  await reducedMovement(page);
  await queueForm(page).getByRole("button", { name: "Queue verification", exact: true }).focus();
  await expect(queueForm(page).getByRole("button", { name: "Queue verification", exact: true })).toBeFocused();
  await page.keyboard.press("Escape");
  await expect(findingDialog(page)).toHaveCount(0);
  await expect(findingTrigger(page)).toBeFocused();
  expect(verificationAPI.externalAttempts).toBe(0);
});
