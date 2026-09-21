import type { Page } from "@playwright/test";
import { expect, test } from "./ai-assessments-fixture";
import type { AssessmentHTTP } from "./ai-assessments-fixture";
import { alpha, cancelPath, finding, historyPath, jobFor, previewFor, user } from "./ai-assessments-data";
import type { AssessmentJob } from "./ai-assessments-data";
import { unitCancelId, unitFindingId, unitPreviewId, unitWorkspaceId } from "./harness/ai-assessment-review/inputs";
import { arrived, closeFinding, deliver, details, history, newAssessment, openFinding, openPane, pane, rows } from "./ai-assessment-review-helpers";

test.use({ reducedMotion: "reduce", timezoneId: "UTC" });
test.beforeEach(async ({ assessments }) => { expect(assessments.requests).toEqual([]); });

const unitHistory = (page: Page) => page.getByRole("region", { name: "Assessment history", exact: true });
const unitRows = (page: Page) => unitHistory(page).getByRole("table", { name: "Assessment history", exact: true }).locator("tbody tr");
const unitValue = (page: Page, name: string) => page.getByLabel(`Unit ${name}`, { exact: true });
async function startUnit(page: Page, api: AssessmentHTTP) {
  const preview = previewFor(), original = jobFor(preview, 2);
  expect([unitWorkspaceId, unitFindingId, unitPreviewId, unitCancelId]).toEqual([alpha.id, finding.id, preview.id, original.id]);
  api.previews.set(preview.id, preview);
  api.seedJobs([original]);
  api.aiOpen = true;
  await page.goto("/tests/harness/ai-assessment-review/index.html");
  await expect(page.getByRole("heading", { name: "Production-hook invariant only, not the application", exact: true })).toBeVisible();
  await expect(unitRows(page)).toHaveCount(1);
  await expect(unitValue(page, "history authorized")).toHaveText("true");
  return original;
}
async function startUnitWrite(page: Page, api: AssessmentHTTP, operation: "queue" | "cancellation", original: AssessmentJob) {
  const path = operation === "queue" ? historyPath() : cancelPath(original.id);
  const response = api.expectWrite(path, operation === "queue" ? 202 : 200, true);
  await page.getByRole("button", { name: `Unit start ${operation}`, exact: true }).click();
  const call = await arrived(response);
  const canonical = call.response!.assessment as AssessmentJob;
  if (operation === "queue") {
    expect(call.body).toEqual({ previewId: unitPreviewId, consent: true, idempotencyKey: canonical.idempotencyKey });
    expect(JSON.parse((await unitValue(page, "unresolved identity").textContent())!)).toEqual({
      workspaceId: alpha.id, findingId: finding.id, requestedBy: user.id,
      previewId: call.body.previewId, idempotencyKey: call.body.idempotencyKey,
    });
  } else expect(call.body).toEqual({});
  return { response, call, canonical };
}
async function unitSnapshot(page: Page) {
  return { rows: await unitRows(page).count(), authorized: await unitValue(page, "history authorized").textContent() };
}

test.describe("AAR2 History denial cannot be undone by an old write receipt", () => {
  for (const denial of [403, 404] as const) {
    test(`Browser boundary: ${denial}, old cancel 200, 503, then authorized history 200`, async ({ page, assessments: api }, info) => {
      const job = jobFor(); api.seedJobs([job]);
      await openFinding(page, api); await openPane(page, api);
      await rows(page).filter({ hasText: job.id }).getByRole("button", { name: "View assessment", exact: true }).click();
      await details(page).getByRole("button", { name: "Cancel assessment", exact: true }).click();
      const cancel = api.expectWrite(cancelPath(job.id), 200, true);
      await pane(page).getByRole("button", { name: "Confirm cancellation", exact: true }).click();
      expect((await arrived(cancel)).body).toEqual({});
      expect(api.jobs.get(job.id)!.state).toBe("cancelled");
      const denied = api.queueRead(historyPath(), denial, true);
      await history(page).getByRole("button", { name: "Refresh assessment history", exact: true }).click();
      await arrived(denied);
      const responseOrder: number[] = [];
      page.on("response", (value) => {
        if ([historyPath(), cancelPath(job.id)].includes(new URL(value.url()).pathname)) responseOrder.push(value.status());
      });
      const deniedHTTP = page.waitForResponse((value) => new URL(value.url()).pathname === historyPath() && value.status() === denial);
      await deliver(denied);
      await deniedHTTP;
      await deliver(cancel);
      await expect(history(page).getByRole("alert")).toBeVisible();
      await expect(rows(page)).toHaveCount(0);
      await expect(details(page)).toHaveCount(0);
      await expect(newAssessment(page)).toBeDisabled();
      const unavailable = api.queueRead(historyPath(), 503, true);
      await history(page).getByRole("button", { name: "Retry assessment history", exact: true }).click();
      await arrived(unavailable);
      await expect(rows(page)).toHaveCount(0);
      await expect(newAssessment(page)).toBeDisabled();
      await deliver(unavailable);
      await expect(history(page).getByRole("alert")).toContainText(/unavailable/i);
      await expect(rows(page)).toHaveCount(0);
      await expect(newAssessment(page)).toBeDisabled();
      api.mark("Actual app remained empty and unwritable after denial, old cancel ACK and 503. This does not prove the smaller pre-cleanup race.");
      const recovered = api.queueRead(historyPath(), 200, true);
      await history(page).getByRole("button", { name: "Retry assessment history", exact: true }).click();
      await arrived(recovered);
      await expect(rows(page)).toHaveCount(0);
      await deliver(recovered);
      await expect(rows(page)).toHaveCount(1);
      await expect(rows(page).filter({ hasText: job.id })).toContainText("cancelled");
      await expect(newAssessment(page)).toBeEnabled();
      expect(api.calls("POST", cancelPath(job.id))).toHaveLength(1);
      expect(api.calls("POST", historyPath())).toHaveLength(0);
      expect(responseOrder[0]).toBe(denial);
      await closeFinding(page, api);
      await info.attach("reviewed-history-browser-boundary", { contentType: "application/json", body: JSON.stringify({
        scope: "Actual-app ordered response boundary; NOT a deterministic pre-layout-cleanup race reproduction",
        denial, responseOrder, oldWriteFailure: cancel.call!.failure, persistedCancellation: api.jobs.get(job.id)!.state,
        afterDenialAndOldACKRows: 0, after503Rows: 0, afterAuthorizedRows: 1,
      }, null, 2) });
    });
  }

  for (const [denial, operation] of [[403, "queue"], [404, "cancellation"]] as const) {
    test(`PROPOSED production-hook invariant: ${denial} then old ${operation} ACK stays withheld through 503`, async ({ page, assessments: api }, info) => {
      const original = await startUnit(page, api);
      const { response, call, canonical } = await startUnitWrite(page, api, operation, original);
      const denied = api.queueRead(historyPath(), denial, true);
      await unitHistory(page).getByRole("button", { name: "Refresh assessment history", exact: true }).click();
      await arrived(denied); await deliver(denied);
      await expect(unitHistory(page).getByRole("alert")).toBeVisible();
      expect(await unitSnapshot(page)).toEqual({ rows: 0, authorized: "false" });
      api.mark("Real mounted production history hook cleared its visible rows on actual typed HTTP denial.");
      await deliver(response);
      await expect(unitValue(page, "write pending")).toHaveText("false");
      expect(JSON.parse((await unitValue(page, "receipt identity").textContent())!)).toEqual({
        id: canonical.id, previewId: canonical.previewId, idempotencyKey: canonical.idempotencyKey,
      });
      const afterACK = await unitSnapshot(page);
      expect.soft(afterACK, "An old typed write receipt is not a new authorized history read.").toEqual({ rows: 0, authorized: "false" });
      const unresolved = JSON.parse((await unitValue(page, "unresolved identity").textContent())!);
      if (unresolved !== null) expect(unresolved).toMatchObject({
        previewId: call.body.previewId, idempotencyKey: call.body.idempotencyKey,
        workspaceId: alpha.id, requestedBy: user.id, findingId: finding.id,
      });
      await expect(page.getByRole("button", { name: "Unit start queue", exact: true })).toBeDisabled();
      api.mark("Old canonical ACK completed in the isolated real lifecycle; minimal receipt identity was observed separately from history authority.");
      const unavailable = api.queueRead(historyPath(), 503, true);
      await unitHistory(page).getByRole("button", { name: "Retry assessment history", exact: true }).click();
      await arrived(unavailable);
      await expect(unitHistory(page).getByRole("status")).toBeVisible();
      const during503 = await unitSnapshot(page);
      expect.soft(during503, "The loading gap after denial must not regain permission from a stale receipt.").toEqual({ rows: 0, authorized: "false" });
      await expect(page.getByRole("button", { name: "Unit start queue", exact: true })).toBeDisabled();
      await deliver(unavailable);
      await expect(unitHistory(page).getByRole("alert")).toContainText(/unavailable/i);
      const after503 = await unitSnapshot(page);
      expect.soft(after503, "503 is not authorized recovery; protected receipt summaries stay withheld.").toEqual({ rows: 0, authorized: "false" });
      api.mark("Held loading gap and delivered 503 were both observed, with no automatic post or authorized recovery.");
      const recovered = api.queueRead(historyPath(), 200, true);
      await unitHistory(page).getByRole("button", { name: "Retry assessment history", exact: true }).click();
      await arrived(recovered);
      await deliver(recovered);
      await expect(unitValue(page, "history authorized")).toHaveText("true");
      await expect(unitRows(page)).toHaveCount(operation === "queue" ? 2 : 1);
      await expect(unitRows(page).filter({ hasText: canonical.id })).toContainText(canonical.state);
      await expect(unitValue(page, "unresolved identity")).toHaveText("null");
      await expect(page.getByRole("button", { name: "Unit start queue", exact: true })).toBeEnabled();
      expect(api.writes()).toHaveLength(1);
      expect(api.calls("GET", historyPath())).toHaveLength(4);
      api.mark("Only a later authorized 200 restored current rows/permission; matching minimal uncertainty reconciled.");
      await info.attach("reviewed-history-hook-invariant", { contentType: "application/json", body: JSON.stringify({
        scope: "PROPOSED existing-export production-hook unit binding; parent approval before freeze. NOT full-app race proof.",
        denial, operation, canonicalIdentity: { id: canonical.id, previewId: canonical.previewId, idempotencyKey: canonical.idempotencyKey },
        afterDenial: { rows: 0, authorized: "false" }, afterACK, during503, after503, after200: await unitSnapshot(page),
        downstreamBlocked: [],
      }, null, 2) });
    });
  }

  for (const operation of ["queue", "cancellation"] as const) {
    test(`PROPOSED production-hook positive control: immediate ${operation} ACK without history denial`, async ({ page, assessments: api }) => {
      const original = await startUnit(page, api);
      const { response, canonical } = await startUnitWrite(page, api, operation, original);
      await expect(unitRows(page)).toHaveCount(1);
      await expect(unitRows(page).filter({ hasText: original.id })).toContainText("queued");
      await deliver(response);
      await expect(unitRows(page)).toHaveCount(operation === "queue" ? 2 : 1);
      await expect(unitRows(page).filter({ hasText: canonical.id })).toContainText(canonical.state);
      await expect(unitValue(page, "history authorized")).toHaveText("true");
      await expect(unitValue(page, "unresolved identity")).toHaveText("null");
      expect(api.calls("GET", historyPath())).toHaveLength(1);
      expect(api.writes()).toHaveLength(1);
      api.mark(`Real history hook immediately accepted canonical ${operation} ACK without an extra history read when no denial occurred.`);
    });
  }
});
