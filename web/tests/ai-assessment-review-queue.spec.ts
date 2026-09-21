import type { Page } from "@playwright/test";
import { expect, test } from "./ai-assessments-fixture";
import type { AssessmentCall, AssessmentControl, AssessmentHTTP } from "./ai-assessments-fixture";
import { apiVersion, historyPath, otherContext, previewPath } from "./ai-assessments-data";
import type { AssessmentJob } from "./ai-assessments-data";
import { arrived, closeFinding, closePane, consent, context, deliver, draft, frames, history, newAssessment,
  openFinding, openPane, pane, prepare, preparePreview, queue, reviewed, rows, startDraft } from "./ai-assessment-review-helpers";

test.use({ reducedMotion: "reduce", timezoneId: "UTC" });
test.beforeEach(async ({ assessments }) => { expect(assessments.requests).toEqual([]); });

function ambiguous(status: 500 | 502 | 503, code: "conflict" | "invalid-input") {
  return { status, body: { apiVersion, error: { code, message: "Synthetic ambiguous transport failure after queue persistence.",
    retryable: false, requestId: "synthetic-reviewed-queue-response" } } };
}
function receipt(api: AssessmentHTTP, call: AssessmentCall) {
  const matching = [...api.jobs.values()].filter((job) =>
    job.previewId === call.body.previewId && job.idempotencyKey === call.body.idempotencyKey);
  expect(matching).toHaveLength(1);
  return matching[0];
}
function queueIdentity(call: AssessmentCall, previewId: string) {
  expect(Object.keys(call.body).sort()).toEqual(["consent", "idempotencyKey", "previewId"]);
  expect(call.body).toMatchObject({ consent: true, previewId, idempotencyKey: expect.any(String) });
  expect(String(call.body.idempotencyKey).trim()).not.toBe("");
  expect(Buffer.byteLength(String(call.body.idempotencyKey))).toBeLessThanOrEqual(128);
}
async function failedHistory(page: Page, api: AssessmentHTTP, refresh = false) {
  const response = api.queueRead(historyPath(), 503, true);
  await history(page).getByRole("button", { name: refresh ? "Refresh assessment history" : "Retry assessment history", exact: true }).click();
  await arrived(response);
  await expect(newAssessment(page)).toBeDisabled();
  await deliver(response);
  await expect(history(page).getByRole("alert")).toContainText(/unavailable/i);
}
async function reopenedFailure(page: Page, api: AssessmentHTTP, wholeFinding: boolean) {
  const response = api.queueRead(historyPath(), 503, true);
  if (wholeFinding) await openFinding(page, api, false);
  await openPane(page, api);
  await arrived(response);
  await expect(newAssessment(page)).toBeDisabled();
  await deliver(response);
  await expect(history(page).getByRole("alert")).toContainText(/unavailable/i);
  await expect(newAssessment(page)).toBeDisabled();
  await expect(draft(page)).toHaveCount(0);
}
async function replayIfOffered(page: Page, api: AssessmentHTTP, first: AssessmentCall) {
  const replay = pane(page).getByRole("button", { name: "Retry pending queue", exact: true });
  if (!await replay.isVisible() || !await replay.isEnabled()) return null;
  const response = api.expectWrite(historyPath(), 200, true);
  await replay.click();
  const repeated = await arrived(response);
  expect.soft(repeated.body, "An explicit pending replay must keep BOTH original preview and key, including across close.").toEqual(first.body);
  expect.soft(api.jobs.size, "A pending replay cannot create another job.").toBe(1);
  return response;
}

test.describe("AAR1 Ambiguous queue HTTP response never retires unresolved intent", () => {
  for (const [status, code] of [[502, "conflict"], [500, "invalid-input"], [503, "conflict"]] as const) {
    test(`${status}/${code}: committed first job, failed history, close/reopen and authorized recovery`, async ({ page, assessments: api }, info) => {
      const phases: string[] = [];
      const mark = (value: string) => { phases.push(value); api.mark(value); };
      await openFinding(page, api); await openPane(page, api); await startDraft(page);
      const preview = await preparePreview(page, api);
      const response = api.expectWrite(historyPath(), 202, true, false, ambiguous(status, code));
      await consent(page).check(); await queue(page).click();
      const first = await arrived(response);
      queueIdentity(first, preview.id);
      const committed = receipt(api, first);
      expect(api.jobs.size).toBe(1);
      expect(first.status).toBe(status);
      expect(first.response).toEqual(ambiguous(status, code).body);
      await expect(rows(page)).toHaveCount(0);
      await expect(queue(page)).toBeDisabled();
      mark("One original guarded queue committed before its misleading 5xx response; no optimistic row.");
      await deliver(response);
      await expect(draft(page).getByRole("alert")).toBeVisible();
      await failedHistory(page, api, true);
      await frames(page);
      expect(api.calls("POST", historyPath())).toHaveLength(1);
      expect(api.calls("POST", previewPath())).toHaveLength(1);
      expect(api.pages).toHaveLength(1);
      expect(api.pages[0].response.items).toEqual([]);
      mark("503 history supplied no matching receipt and no automatic preview/queue occurred.");

      let pendingResponse: AssessmentControl | null = await replayIfOffered(page, api, first);
      let attempted: AssessmentCall | null = null;
      if (pendingResponse) {
        attempted = pendingResponse.call;
        mark("Only an explicitly clicked pending replay was offered; its ACK stays held through close.");
      } else {
        if (!await draft(page).count() && await newAssessment(page).isEnabled()) await startDraft(page);
        if (await context(page).count() && await context(page).isEnabled()) {
          await context(page).fill(otherContext);
          api.payloadTexts.add(otherContext);
          if (await reviewed(page).isEnabled()) await reviewed(page).check();
          if (await prepare(page).isEnabled()) {
            const prepared = api.expectWrite(previewPath(), 201, true);
            await prepare(page).click();
            const call = await arrived(prepared);
            expect(call.body).toEqual({ ...api.calls("POST", previewPath())[0].body, context: otherContext });
            const deliveredPreview = page.waitForResponse((value) => new URL(value.url()).pathname === previewPath() && value.status() === 201);
            await deliver(prepared);
            await (await deliveredPreview).finished();
            await frames(page);
          }
          if (await consent(page).count() && await consent(page).isEnabled()) await consent(page).check();
          if (await queue(page).count() && await queue(page).isEnabled()) {
            pendingResponse = api.expectWrite(historyPath(), 202, true);
            await queue(page).click();
            attempted = await arrived(pendingResponse);
            expect.soft(attempted.body, "History is still failed: another explicit action cannot create a fresh preview/key queue.")
              .toEqual(first.body);
            mark("A second explicit queue reached the guarded HTTP boundary before any authorized reconciliation.");
          }
        }
      }
      expect.soft(api.jobs.size, "An unresolved committed queue cannot become a second fixture job.").toBe(1);
      const beforeClose = api.calls("POST", historyPath()).length;
      await closePane(page, api);
      if (pendingResponse) {
        await deliver(pendingResponse);
        await expect.poll(() => pendingResponse!.call!.failure).toMatch(/abort/i);
      }
      await reopenedFailure(page, api, false);
      await frames(page);
      expect(api.calls("POST", historyPath())).toHaveLength(beforeClose);
      mark("Pane close/reopen cleared payloads, preserved focus, made no automatic POST and failed history still blocked new work.");
      const paneReplay = await replayIfOffered(page, api, first);
      const beforeFindingClose = api.calls("POST", historyPath()).length;
      await closeFinding(page, api);
      if (paneReplay) {
        await deliver(paneReplay);
        await expect.poll(() => paneReplay.call!.failure).toMatch(/abort/i);
      }
      await reopenedFailure(page, api, true);
      await frames(page);
      expect(api.calls("POST", historyPath())).toHaveLength(beforeFindingClose);
      expect(api.pages).toHaveLength(1);
      mark("Finding close/reopen remained blocked through a further 503 without an authorized matching receipt.");
      const findingReplay = await replayIfOffered(page, api, first);

      const authorized = api.queueRead(historyPath(), 200, true);
      await history(page).getByRole("button", { name: "Retry assessment history", exact: true }).click();
      const recovery = await arrived(authorized);
      expect(recovery.response!.items as AssessmentJob[]).toEqual(expect.arrayContaining([
        expect.objectContaining({ id: committed.id, previewId: first.body.previewId, idempotencyKey: first.body.idempotencyKey }),
      ]));
      await expect(newAssessment(page)).toBeDisabled();
      await deliver(authorized);
      await expect(rows(page).filter({ hasText: committed.id })).toBeVisible();
      if (findingReplay) await deliver(findingReplay);
      await startDraft(page);
      const genuinelyNew = await preparePreview(page, api, otherContext);
      const allowed = api.expectWrite(historyPath(), 202, true);
      await consent(page).check(); await queue(page).click();
      const later = await arrived(allowed);
      queueIdentity(later, genuinelyNew.id);
      expect(later.body.previewId).not.toBe(first.body.previewId);
      expect(later.body.idempotencyKey).not.toBe(first.body.idempotencyKey);
      expect.soft(api.jobs.size, "Only the first reconciled job and one genuinely new consented job may exist.").toBe(2);
      await deliver(allowed);
      await expect(rows(page).filter({ hasText: receipt(api, later).id })).toBeVisible();
      mark("Authorized matching history permitted a genuinely new separately reviewed preview/key and canonical queue receipt.");
      await api.assertPrivate(page);
      await closeFinding(page, api);
      await info.attach("reviewed-queue-evidence", { contentType: "application/json", body: JSON.stringify({
        scope: "Real application UI and typed client over original guarded synthetic HTTP",
        status, code, first: first.body, attemptedBeforeReconciliation: attempted?.body ?? null, authorizedLater: later.body,
        jobs: [...api.jobs.values()].map(({ id, previewId, idempotencyKey }) => ({ id, previewId, idempotencyKey })),
        phases, downstreamBlocked: [],
      }, null, 2) });
    });
  }

  test("Definitive initial 409 persists no job and allows a new explicit reviewed intent", async ({ page, assessments: api }) => {
    await openFinding(page, api); await openPane(page, api); await startDraft(page);
    const preview = await preparePreview(page, api);
    api.serverOffsetMs = 360_000;
    const rejected = api.expectWrite(historyPath(), 202, true, false, ambiguous(500, "invalid-input"));
    await consent(page).check(); await queue(page).click();
    const first = await arrived(rejected);
    queueIdentity(first, preview.id);
    expect(first.status, "A transport override cannot hide an actual pre-commit fixture validation rejection.").toBe(409);
    expect(api.jobs.size).toBe(0);
    await deliver(rejected);
    await expect(draft(page).getByRole("alert")).toBeVisible();
    await expect(consent(page)).toHaveCount(0);
    await frames(page);
    expect(api.calls("POST", historyPath())).toHaveLength(1);
    expect(api.calls("POST", previewPath())).toHaveLength(1);
    api.serverOffsetMs = 0;
    const fresh = await preparePreview(page, api, otherContext);
    const allowed = api.expectWrite(historyPath(), 202, true);
    await consent(page).check(); await queue(page).click();
    const next = await arrived(allowed);
    queueIdentity(next, fresh.id);
    expect(next.body.previewId).not.toBe(first.body.previewId);
    expect(next.body.idempotencyKey).not.toBe(first.body.idempotencyKey);
    expect(api.jobs.size).toBe(1);
    expect(api.calls("GET", historyPath())).toHaveLength(1);
    await deliver(allowed);
    await expect(rows(page).filter({ hasText: receipt(api, next).id })).toBeVisible();
    api.mark("Actual initial expiry 409, not a misleading 5xx: no job, no permanent ambiguity latch, no automatic POST, fresh explicit consent succeeded.");
    await closeFinding(page, api);
  });
});
