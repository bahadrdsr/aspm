import type { Locator, Page } from "@playwright/test";
import { requireProductionUI } from "./network";
import { catalogResponse } from "./fixtures";
import { expectIneligibleOption } from "./reviews/ai-settings-ui-v1/native-option-A1";
import {
  alpha, beta, betaFinding, betaProfile, cancelPath, disabledProfile, draftMarker, finding, grantFor,
  historyPath, historySeries, jobFor, jobPath, otherContext, password, policy,
  policyPath, previewFor, previewPath, primaryProfile, reviewedContext, unreviewedProfile, user,
} from "./ai-assessments-data";
import type { AssessmentJob, AssessmentPreview } from "./ai-assessments-data";
import { expect, test } from "./ai-assessments-fixture";
import type { AssessmentCall, AssessmentControl, AssessmentHTTP } from "./ai-assessments-fixture";

test.use({ reducedMotion: "reduce", timezoneId: "UTC" });
test.beforeEach(async ({ assessments }) => { requireProductionUI(); expect(assessments.requests).toEqual([]); });

function dialog(page: Page, f = finding) { return page.getByRole("dialog", { name: f.title, exact: true, includeHidden: true }); }
function panel(page: Page) { return page.getByRole("region", { name: "AI assessments", exact: true, includeHidden: true }); }
function entry(page: Page, f = finding) { return dialog(page, f).getByRole("button", { name: "AI assessments", exact: true }); }
function history(page: Page) { return panel(page).getByRole("region", { name: "Assessment history", exact: true }); }
function table(page: Page) { return history(page).getByRole("table", { name: "Assessment history", exact: true }); }
function jobRow(page: Page, id: string) { return table(page).getByRole("row").filter({ hasText: id }); }
function details(page: Page) { return panel(page).getByRole("region", { name: "Assessment details", exact: true, includeHidden: true }); }
function form(page: Page) { return panel(page).getByRole("form", { name: "Prepare AI assessment", exact: true }); }
function contextField(page: Page) { return form(page).getByRole("textbox", { name: "Reviewed finding context", exact: true }); }
function reviewed(page: Page) { return form(page).getByRole("checkbox", { name: "I reviewed and redacted this derived context", exact: true }); }
function prepare(page: Page) { return form(page).getByRole("button", { name: "Prepare assessment preview", exact: true }); }
function preview(page: Page) { return panel(page).getByRole("region", { name: "Assessment preview", exact: true, includeHidden: true }); }
function consent(page: Page) { return panel(page).getByRole("checkbox", { name: "Approve this exact preview for queueing", exact: true }); }
function queue(page: Page) { return panel(page).getByRole("button", { name: "Queue assessment", exact: true }); }
function workspace(page: Page) { return page.getByRole("combobox", { name: "Workspace", exact: true }); }
function workRow(page: Page, f = finding) {
  return page.getByRole("table", { name: "Findings", exact: true, includeHidden: true }).getByRole("row", { includeHidden: true }).filter({ hasText: f.title });
}
function trigger(page: Page, f = finding) {
  return workRow(page, f).getByRole("button", { name: f.title, exact: true, includeHidden: true })
    .or(workRow(page, f).getByRole("link", { name: f.title, exact: true, includeHidden: true }));
}
function fact(scope: Locator, label: string) {
  const escaped = label.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
  return scope.getByRole("term").filter({ hasText: new RegExp(`^${escaped}$`, "i") }).locator("xpath=following-sibling::dd[1]");
}
async function frames(page: Page) {
  await page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))));
}
async function requested(control: AssessmentControl) {
  await expect.poll(() => control.call !== null, "Explicit user action must reach the declared assessment HTTP boundary.").toBe(true);
  return control.call!;
}
async function release(page: Page, control: AssessmentControl, aborted = false) {
  control.release(); await control.delivered; await frames(page);
  if (aborted) await expect.poll(() => control.call?.failure, "Closing/scope loss must abort the actual old request.").toMatch(/abort/i);
}
async function noEnabled(actions: Locator) { for (const action of await actions.all()) await expect(action).toBeDisabled(); }
async function clearedConsent(page: Page) {
  if (await consent(page).count()) await expect(consent(page)).not.toBeChecked();
  await noEnabled(queue(page));
}
async function instant(value: Locator, expected: string) {
  await expect(value).toBeVisible();
  await expect(value).toHaveAttribute("datetime", /\d{4}-\d\d-\d\dT/);
  expect(await value.evaluate((node) => node.tagName)).toBe("TIME");
  expect(Date.parse((await value.getAttribute("datetime"))!)).toBe(Date.parse(expected));
}
async function literalContext(scope: Locator, text: string) {
  const content = scope.getByLabel("Approved context", { exact: true });
  await expect(content).toBeVisible();
  expect(await content.evaluate((node) => node instanceof HTMLTextAreaElement ? node.value : node.textContent)).toBe(text);
  await expect(content.locator("a,img,iframe,script")).toHaveCount(0);
}
async function openFinding(page: Page, api: AssessmentHTTP, f = finding, navigate = true) {
  const before = api.aiCalls().length;
  if (navigate) await page.goto("/#/work");
  await expect(page.getByRole("navigation", { name: "Primary", exact: true }).getByRole("link")).toHaveCount(5);
  await expect(workRow(page, f)).toBeVisible();
  await page.getByRole("textbox", { name: "Filter findings", exact: true }).fill(f.assetName);
  await workRow(page, f).getByRole("checkbox", { name: `Select ${f.title}`, exact: true }).check();
  await trigger(page, f).click();
  await expect(dialog(page, f)).toBeVisible();
  await expect(dialog(page, f).getByRole("list", { name: "Observations", exact: true }).getByRole("listitem")).toHaveCount(f.observations.length);
  await expect(dialog(page, f).getByRole("list", { name: "Analyst notes", exact: true }).getByRole("listitem")).toHaveCount(f.notes.length);
  await expect(panel(page)).toHaveCount(0);
  await frames(page);
  expect(api.aiCalls(), "Finding open/filter/selection must not preload assessment/configuration calls.").toHaveLength(before);
  api.mark("closed finding AI: published dialog, observation/note histories, filter/selection, five nav, zero added AI reads");
}
async function openAI(page: Page, api: AssessmentHTTP, f = finding) {
  await expect(entry(page, f), "Missing AI assessments controls: add an explicit initially closed entry to the real finding dialog.").toBeVisible();
  await expect(entry(page, f)).toHaveAttribute("aria-expanded", "false");
  api.aiOpen = true;
  await entry(page, f).click();
  await expect(entry(page, f)).toHaveAttribute("aria-expanded", "true");
  await expect(panel(page)).toBeVisible();
  await expect(page.getByRole("dialog")).toHaveCount(1);
  await expect(panel(page).getByLabel(/API key|encryption key|ApprovalRef|actor ID|provider headers/i)).toHaveCount(0);
  await expect(panel(page).getByRole("button", { name: /probe|discover models|test connection|verify finding|close finding|execute tool/i })).toHaveCount(0);
  api.mark("AI assessments explicitly opened; no nested modal/provider/credential surface");
}
async function closeAI(page: Page, api: AssessmentHTTP, f = finding) {
  await panel(page).getByRole("button", { name: "Close AI assessments", exact: true }).click();
  api.aiOpen = false;
  await expect(panel(page)).toHaveCount(0);
  await expect(entry(page, f)).toHaveAttribute("aria-expanded", "false");
  await expect(entry(page, f)).toBeFocused();
  await api.assertPrivate(page, true);
}
async function closeFinding(page: Page, api: AssessmentHTTP, f = finding) {
  await dialog(page, f).getByRole("button", { name: "Close finding details", exact: true }).click();
  api.aiOpen = false;
  await expect(dialog(page, f)).toHaveCount(0);
  await expect(panel(page)).toHaveCount(0);
  await expect(trigger(page, f)).toBeFocused();
  await expect(page.getByRole("textbox", { name: "Filter findings", exact: true })).toHaveValue(f.assetName);
  await expect(workRow(page, f).getByRole("checkbox", { name: `Select ${f.title}`, exact: true })).toBeChecked();
  await api.assertPrivate(page, true);
}
async function newDraft(page: Page) {
  await panel(page).getByRole("button", { name: "New assessment", exact: true }).click();
  await expect(form(page)).toBeVisible();
  await expect(contextField(page)).toHaveValue("");
  await expect(form(page).getByRole("combobox", { name: "Observation", exact: true })).toHaveValue("");
  await expect(form(page).getByRole("combobox", { name: "AI profile", exact: true })).toHaveValue("");
  await expect(reviewed(page)).not.toBeChecked();
  await expect(prepare(page)).toBeDisabled();
}
async function selectFacts(page: Page, f = finding, p = primaryProfile, grantId = grantFor().id, observation = f.observations[0].id) {
  await form(page).getByRole("combobox", { name: "Observation", exact: true }).selectOption(observation);
  await form(page).getByRole("combobox", { name: "AI profile", exact: true }).selectOption(p.id);
  const grants = form(page).getByRole("combobox", { name: "AI grant", exact: true });
  if (grantId) await grants.selectOption(grantId);
  else if (await grants.count()) { await expect(grants).toBeDisabled(); await expect(grants).toHaveValue(""); }
}
async function previewFacts(page: Page, value: AssessmentPreview) {
  await expect(preview(page)).toBeVisible();
  for (const text of [value.id, value.observationId, value.profileId, value.profileRevision, value.policyRevision,
    value.destination, value.promptRevision, value.contextRef, value.task, value.dataClass]) await expect(preview(page)).toContainText(text);
  await expect(fact(preview(page), "Context digest")).toHaveText(value.contextDigest);
  await expect(fact(preview(page), "Source evidence digest")).toHaveText(value.sourceEvidenceDigest);
  await expect(fact(preview(page), "Model")).toHaveText(value.model);
  if (value.deployment) await expect(fact(preview(page), "Deployment")).toHaveText(value.deployment);
  await literalContext(preview(page), value.context);
  await instant(preview(page).getByLabel("Preview expires at", { exact: true }), value.expiresAt);
  await expect(preview(page)).toContainText(/derived|user.reviewed/i);
  await expect(preview(page)).toContainText(/not.*(?:original|scanner|proof)|(?:not|no).*cryptographic/i);
  await expect(preview(page)).toContainText(/advisory|not.*verification|not.*proof/i);
  await expect(consent(page)).not.toBeChecked();
  await expect(queue(page)).toBeDisabled();
}
async function preparePreview(page: Page, api: AssessmentHTTP, text = reviewedContext, f = finding,
  p = primaryProfile, grantId = grantFor().id, observation = f.observations[0].id) {
  await selectFacts(page, f, p, grantId, observation);
  await contextField(page).fill(text); api.payloadTexts.add(text);
  await reviewed(page).check();
  const held = api.expectWrite(previewPath(f.id), 201, true);
  await prepare(page).click();
  const call = await requested(held);
  expect(call.body).toEqual({ observationId: observation, profileId: p.id, grantId, context: text, reviewed: true });
  await expect(prepare(page)).toBeDisabled();
  await noEnabled(queue(page));
  await release(page, held);
  const value = call.response!.preview as AssessmentPreview;
  await previewFacts(page, value);
  return value;
}
function queueBody(call: AssessmentCall, value: AssessmentPreview) {
  expect(Object.keys(call.body).sort()).toEqual(["consent", "idempotencyKey", "previewId"]);
  expect(call.body).toMatchObject({ previewId: value.id, consent: true, idempotencyKey: expect.any(String) });
  const key = String(call.body.idempotencyKey);
  expect(key.trim()).not.toBe(""); expect(Buffer.byteLength(key)).toBeLessThanOrEqual(128);
}
async function viewJob(page: Page, job: AssessmentJob) {
  await jobRow(page, job.id).getByRole("button", { name: "View assessment", exact: true }).click();
  await expect(details(page)).toBeVisible();
  await expect(details(page)).toContainText(job.id);
}
async function unchangedFinding(page: Page, f = finding) {
  for (const text of [f.description, f.evidence.text, f.evidence.sourceLabel, f.notes[0].text, "No independently verified resolution is recorded."]) {
    await expect(dialog(page, f).getByText(text, { exact: true })).toBeVisible();
  }
  await expect(fact(dialog(page, f), "Owner")).toContainText(f.ownerName!);
  await expect(fact(dialog(page, f), "Human workflow")).toContainText(/in progress/i);
  await expect(fact(dialog(page, f), "Disposition")).toContainText(/accepted risk/i);
  await expect(dialog(page, f).getByRole("list", { name: "Observations", exact: true }).getByRole("listitem")).toHaveCount(f.observations.length);
}
async function noMotionOrOverflow(page: Page) {
  const value = await page.evaluate(async () => {
    const motion = new Set<string>();
    for (let sample = 0; sample < 6; sample++) {
      for (const animation of document.getAnimations()) {
        if (animation.playState !== "running" || !(animation.effect instanceof KeyframeEffect)) continue;
        if (animation.effect.getTiming().iterations === Infinity) motion.add("continuous");
        const frames = animation.effect.getKeyframes() as Array<Record<string, unknown>>;
        for (const property of ["transform", "translate", "scale", "rotate", "left", "top"]) {
          if (new Set(frames.map((frame) => frame[property]).filter((value) => value !== undefined).map(String)).size > 1) motion.add(property);
        }
      }
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
    }
    return { motion: [...motion], overflow: document.documentElement.scrollWidth - innerWidth, reduced: matchMedia("(prefers-reduced-motion: reduce)").matches };
  });
  expect(value.reduced).toBe(true); expect(value.motion).toEqual([]); expect(value.overflow).toBeLessThanOrEqual(1);
}

test("AAUI1 Lazy finding entry preserves published shell and readonly history with honest configuration failures", async ({ page, assessments: api }) => {
  api.roles.set(alpha.id, "viewer"); api.serverRoles.set(alpha.id, "viewer");
  const job = jobFor(); api.seedJobs([job]);
  await page.goto("/#/integrations");
  await expect(page.getByRole("navigation", { name: "Primary", exact: true }).getByRole("link")).toHaveCount(5);
  const catalog = page.getByRole("list", { name: "Native integrations", exact: true });
  await expect(catalog.getByRole("listitem")).toHaveCount(8);
  for (const family of catalogResponse.items) await expect(catalog.getByRole("heading", { name: family.name, exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "AI settings", exact: true })).toHaveAttribute("aria-expanded", "false");
  await expect(page.getByRole("region", { name: "AI settings", exact: true, includeHidden: true })).toHaveCount(0);
  expect(api.aiCalls()).toEqual([]);
  await openFinding(page, api); await openAI(page, api);
  await expect(jobRow(page, job.id)).toBeVisible();
  await noEnabled(panel(page).getByRole("button", { name: /^(New assessment|Prepare assessment preview|Queue assessment|Cancel assessment)$/ }));
  await viewJob(page, job);
  await expect(fact(details(page), "State")).toHaveText(/^queued$/i);
  expect(api.writes()).toEqual([]);
  await closeFinding(page, api);
  await workspace(page).selectOption(beta.id);
  api.queueRead(policyPath, 503, false, beta.id);
  await openFinding(page, api, betaFinding, false); await openAI(page, api, betaFinding);
  await newDraft(page);
  await expect(panel(page).getByRole("alert")).toBeVisible();
  await expect(panel(page).getByRole("status", { name: "Current assessment policy", exact: true })).toHaveCount(0);
  await expect(prepare(page)).toBeDisabled();
  api.policies.set(beta.id, { workspaceId: beta.id, mode: "disabled", revision: "0", updatedAt: null, updatedBy: null });
  await panel(page).getByRole("button", { name: "Retry assessment configuration", exact: true }).click();
  await expect(panel(page).getByRole("status", { name: "Current assessment policy", exact: true })).toContainText("disabled");
  await expect(prepare(page)).toBeDisabled();
  api.policies.set(beta.id, { ...policy, workspaceId: beta.id, mode: "local-only", revision: "beta-local/opaque" });
  await panel(page).getByRole("button", { name: "Refresh assessment configuration", exact: true }).click();
  await expect(panel(page).getByRole("status", { name: "Current assessment policy", exact: true })).toContainText("local-only");
  await preparePreview(page, api, otherContext, betaFinding, betaProfile, "");
  expect(api.calls("POST", historyPath(betaFinding.id))).toEqual([]);
  await closeAI(page, api, betaFinding);
  api.mark("AAUI1 readonly scoped history, actual config failure/default/local-only recovery and no inferred queue completed");
});

test("AAUI2 Explicit derived context preserves selected observation, exact UTF8 bytes and canonical preview linkage", async ({ page, assessments: api }) => {
  await openFinding(page, api); await openAI(page, api); await newDraft(page);
  const profiles = form(page).getByRole("combobox", { name: "AI profile", exact: true });
  for (const p of [disabledProfile, unreviewedProfile]) await expectIneligibleOption(profiles.getByRole("option").filter({ hasText: p.name }));
  await selectFacts(page, finding, primaryProfile, grantFor().id, finding.observations[1].id);
  for (const value of ["   \n  ", "bad\0context", "界".repeat(10923)]) {
    api.payloadTexts.add(value);
    await contextField(page).fill(value); await reviewed(page).check();
    const rejection = api.expectWrite(previewPath(), Buffer.byteLength(value) > 32768 ? 413 : 400);
    if (await prepare(page).isEnabled()) await prepare(page).click();
    await expect.poll(async () => !(await prepare(page).isEnabled()) ||
      await form(page).evaluate((node: HTMLFormElement) => !node.checkValidity()) ||
      await panel(page).getByRole("alert").isVisible(), "Invalid context must be rejected, not normalized into consent.").toBe(true);
    if (!rejection.call) api.discardUnsent(rejection);
    await expect(preview(page)).toHaveCount(0);
  }
  const maximum = "界".repeat(10922) + "xy";
  expect(Buffer.byteLength(maximum)).toBe(32768);
  const large = await preparePreview(page, api, maximum, finding, primaryProfile, grantFor().id, finding.observations[1].id);
  expect(large.observationId).toBe(finding.observations[1].id);
  await contextField(page).fill(reviewedContext);
  await clearedConsent(page);
  await reviewed(page).check();
  const next = api.expectWrite(previewPath(), 201, true);
  await prepare(page).click(); const call = await requested(next);
  expect(call.body).toEqual({ observationId: finding.observations[1].id, profileId: primaryProfile.id,
    grantId: grantFor().id, context: reviewedContext, reviewed: true });
  await release(page, next); await previewFacts(page, call.response!.preview as AssessmentPreview);
  await expect(preview(page).locator("img,iframe,script")).toHaveCount(0);
  expect(api.calls("POST", historyPath())).toEqual([]);
  await unchangedFinding(page); await api.assertPrivate(page);
  await closeAI(page, api);
  api.mark("AAUI2 exact selected observation, blank/NUL/UTF8 bounds, inert derived context, two distinct digests and second consent completed");
});

test("AAUI3 Queue consent handles lost ACK with same intent or history barrier and invalidates opaque or expired facts", async ({ page, assessments: api }) => {
  await openFinding(page, api); await openAI(page, api); await newDraft(page);
  const v = await preparePreview(page, api);
  await consent(page).check();
  const lost = api.expectWrite(historyPath(), 202, false, true);
  await queue(page).click(); const first = await requested(lost); queueBody(first, v);
  await lost.delivered; await expect(panel(page).getByRole("alert")).toBeVisible();
  const committed = first.response!.assessment as AssessmentJob;
  expect(api.jobs.size).toBe(1);
  await closeAI(page, api);
  api.queueRead(historyPath(), 503);
  await openAI(page, api);
  await expect(history(page).getByRole("alert")).toBeVisible();
  await frames(page); expect(api.calls("POST", historyPath())).toHaveLength(1);
  const retry = panel(page).getByRole("button", { name: "Retry pending queue", exact: true });
  if (await retry.isVisible() && await retry.isEnabled()) {
    const replay = api.expectWrite(historyPath(), 200, true);
    await retry.click(); const repeated = await requested(replay);
    expect(repeated.body).toEqual(first.body);
    await release(page, replay);
    await expect(jobRow(page, committed.id)).toBeVisible();
  } else {
    await noEnabled(panel(page).getByRole("button", { name: "New assessment", exact: true }));
    await history(page).getByRole("button", { name: "Retry assessment history", exact: true }).click();
    await expect(jobRow(page, committed.id)).toBeVisible();
  }
  expect(api.jobs.size).toBe(1);
  await newDraft(page);
  await preparePreview(page, api, otherContext);
  await consent(page).check();
  const currentProfile = { ...primaryProfile, revision: "profile-A/opaque.2" };
  const currentPolicy = { ...policy, revision: "policy-A/opaque.2" };
  expect(currentProfile.revision < primaryProfile.revision).toBe(true);
  api.profiles.set(currentProfile.id, currentProfile); api.policies.set(alpha.id, currentPolicy);
  const currentGrant = grantFor(currentProfile, currentPolicy, 20);
  api.grants.set(currentGrant.id, currentGrant);
  await panel(page).getByRole("button", { name: "Refresh assessment configuration", exact: true }).click();
  await clearedConsent(page);
  const previewsBefore = api.calls("POST", previewPath()).length;
  await frames(page); expect(api.calls("POST", previewPath())).toHaveLength(previewsBefore);
  const fresh = await preparePreview(page, api, otherContext, finding, currentProfile, currentGrant.id);
  await consent(page).check();
  api.serverOffsetMs = 360_000;
  const refused = api.expectWrite(historyPath(), 409);
  await queue(page).click(); const failed = await requested(refused); queueBody(failed, fresh);
  expect(failed.body.idempotencyKey).not.toBe(first.body.idempotencyKey);
  await expect(panel(page).getByRole("alert")).toContainText(/expired|changed|review|conflict/i);
  await clearedConsent(page);
  await frames(page);
  expect(api.calls("POST", previewPath())).toHaveLength(previewsBefore + 1);
  expect(api.jobs.size).toBe(1);
  await api.assertPrivate(page);
  api.mark("AAUI3 lost ACK never duplicated, opaque refresh invalidated consent, expired conflict made no automatic preview or queue");
});

test("AAUI4 Canonical states advisory outcomes unknown usage and cancellation never become verification or unsent claims", async ({ page, assessments: api }) => {
  const jobs = [
    jobFor(previewFor(), 1, "queued"), jobFor(previewFor(), 2, "dispatching"),
    jobFor(previewFor(), 3, "succeeded", "supported"), jobFor(previewFor(), 4, "succeeded", "contradicted"),
    { ...jobFor(previewFor(), 5, "succeeded", "inconclusive"), usage: { known: false, inputTokens: 0, outputTokens: 0, cachedInputTokens: 0, cacheWriteTokens: 0 } },
    jobFor(previewFor(), 6, "failed"), jobFor(previewFor(), 7, "cancelled"),
    jobFor(previewFor(), 8, "invalidated"), jobFor(previewFor(), 9, "uncertain"),
  ];
  api.seedJobs(jobs);
  await openFinding(page, api); await openAI(page, api);
  for (const job of jobs) {
    await viewJob(page, job);
    await expect(fact(details(page), "State")).toHaveText(new RegExp(`^${job.state}$`, "i"));
    await expect(fact(details(page), "Dispatch state")).toHaveText(new RegExp(`^${job.dispatchState.replaceAll("-", "[- ]")}$`, "i"));
    await expect(fact(details(page), "Attempts")).toHaveText(String(job.attempts));
    await expect(details(page)).toContainText(/advisory/i);
    await expect(details(page).getByText(/^(Verified|Reproduced|FalsePositive|False positive|Closed finding)$/i)).toHaveCount(0);
    await literalContext(details(page), job.context);
    await expect(fact(details(page), "Requested model")).toHaveText(job.requestedModel);
    if (job.deployment) await expect(fact(details(page), "Deployment")).toHaveText(job.deployment);
    if (job.requestId) await expect(fact(details(page), "Request ID")).toHaveText(job.requestId);
    if (job.returnedModel) await expect(fact(details(page), "Returned model")).toHaveText(job.returnedModel);
    if (job.stopReason) await expect(fact(details(page), "Stop reason")).toHaveText(job.stopReason);
    if (job.usage.known) {
      for (const [label, value] of [["Input tokens", job.usage.inputTokens], ["Output tokens", job.usage.outputTokens],
        ["Cached input tokens", job.usage.cachedInputTokens], ["Cache write tokens", job.usage.cacheWriteTokens]] as const) {
        await expect(fact(details(page), label)).toHaveText(String(value));
      }
    } else {
      await expect(fact(details(page), "Input tokens")).toHaveText(/unknown/i);
      await expect(fact(details(page), "Output tokens")).toHaveText(/unknown/i);
      await expect(details(page)).not.toContainText(/\$0(?:\.00)?|zero cost|no usage/i);
    }
    if (job.result) {
      const result = details(page).getByRole("region", { name: "Advisory result", exact: true });
      await expect(result).toContainText(job.result.conclusion);
      await expect(result).toContainText(job.result.uncertainty);
      await expect(result).toContainText(job.contextRef);
      await expect(result.locator("a,img,iframe,script")).toHaveCount(0);
    } else await expect(details(page).getByRole("region", { name: "Advisory result", exact: true })).toHaveCount(0);
    if (job.failure) await expect(details(page)).toContainText(job.failure.code);
    if (["uncertain", "cancelled"].includes(job.state)) {
      await expect(details(page).getByRole("button", { name: /resend|retry assessment|repeat assessment/i })).toHaveCount(0);
    }
  }
  const queued = jobs[0];
  await viewJob(page, queued);
  await details(page).getByRole("button", { name: "Cancel assessment", exact: true }).click();
  const confirmation = panel(page).getByRole("region", { name: "Cancel assessment confirmation", exact: true });
  await expect(page.getByRole("dialog")).toHaveCount(1);
  await confirmation.getByRole("button", { name: "Keep assessment", exact: true }).click();
  expect(api.calls("POST", cancelPath(queued.id))).toEqual([]);
  await details(page).getByRole("button", { name: "Cancel assessment", exact: true }).click();
  const held = api.expectWrite(cancelPath(queued.id), 200, true);
  await confirmation.getByRole("button", { name: "Confirm cancellation", exact: true }).click();
  expect((await requested(held)).body).toEqual({});
  await expect(fact(details(page), "State")).toHaveText(/^queued$/i);
  await release(page, held);
  await expect(fact(details(page), "State")).toHaveText(/^cancelled$/i);
  await expect(fact(details(page), "Dispatch state")).toHaveText(/^not[- ]started$/i);
  const dispatching = jobs[1];
  await viewJob(page, dispatching);
  await details(page).getByRole("button", { name: "Cancel assessment", exact: true }).click();
  const sent = api.expectWrite(cancelPath(dispatching.id), 200);
  await confirmation.getByRole("button", { name: "Confirm cancellation", exact: true }).click();
  expect((await requested(sent)).body).toEqual({});
  await expect(fact(details(page), "Dispatch state")).toHaveText(/^possibly[- ]sent$/i);
  await expect(details(page)).toContainText(/already.*sent|may.*sent|cannot.*undo|not.*rollback/i);
  await unchangedFinding(page);
  api.mark("AAUI4 all states/three advisory conclusions/native provenance/unknown usage and canonical cancel without mutation completed");
});

test("AAUI5 Manual native history continuation and denial latch retain confirmed pages without stale authority", async ({ page, assessments: api }) => {
  const jobs = historySeries();
  jobs[100] = jobFor(previewFor(finding, primaryProfile, policy, grantFor(), "SYNTHETIC DENIED JOB CONTEXT MUST STAY WITHHELD", 101), 101);
  api.seedJobs(jobs);
  await openFinding(page, api); await openAI(page, api);
  await expect(jobRow(page, jobs[0].id)).toBeVisible();
  const initial = api.pages.find((entry) => entry.call.path === historyPath())!;
  expect((initial.response.items as AssessmentJob[])).toHaveLength(100);
  const cursor = String(initial.response.nextCursor);
  expect(cursor).toBe(jobs[99].id);
  await frames(page); expect(api.calls("GET", historyPath()).filter((call) => call.query.cursor)).toEqual([]);
  const unavailable = api.queueRead(historyPath(), 503, true, alpha.id, cursor);
  await history(page).getByRole("button", { name: "Load more assessments", exact: true }).click();
  const first = await requested(unavailable);
  expect(first.query).toEqual({ ...initial.call.query, cursor });
  await expect(jobRow(page, jobs[0].id)).toBeVisible();
  await release(page, unavailable);
  await expect(history(page).getByRole("alert")).toBeVisible();
  await expect(jobRow(page, jobs[0].id)).toBeVisible();
  const recovered = api.queueRead(historyPath(), 200, false, alpha.id, cursor);
  await history(page).getByRole("button", { name: "Retry assessment history", exact: true }).click();
  expect((await requested(recovered)).query).toEqual(first.query);
  await expect(jobRow(page, jobs[100].id)).toBeVisible();
  const selected = jobs[100];
  await viewJob(page, selected);
  for (const status of [403, 503, 200, 404, 503, 200] as const) {
    const pending = api.queueRead(jobPath(selected.id), status, true);
    const refresh = details(page).getByRole("button", { name: /^(Refresh assessment|Retry assessment)$/ });
    await refresh.click(); await requested(pending); await release(page, pending);
    if (status === 200) await literalContext(details(page), selected.context);
    else {
      await expect(details(page).getByRole("alert")).toBeVisible();
      await expect(panel(page)).not.toContainText(selected.context);
      await expect(details(page)).not.toContainText(selected.contextDigest);
      await expect(details(page).getByRole("region", { name: "Advisory result", exact: true })).toHaveCount(0);
    }
  }
  api.serverRoles.set(alpha.id, "viewer");
  await details(page).getByRole("button", { name: "Cancel assessment", exact: true }).click();
  const denied = api.expectWrite(cancelPath(selected.id), 200);
  await panel(page).getByRole("region", { name: "Cancel assessment confirmation", exact: true })
    .getByRole("button", { name: "Confirm cancellation", exact: true }).click();
  expect((await requested(denied)).status).toBe(403);
  await expect(panel(page).getByRole("alert")).toContainText(/role|permission|permit|denied/i);
  expect(api.jobs.get(selected.id)!.state).toBe("queued");
  api.mark("AAUI5 manual 100+1 pages/same-cursor retry, 403/404 withholding through 503 and authoritative write denial completed");
});

test("AAUI6 Mobile focus and private context survive pane finding workspace 401 and logout epochs with held ACKs", async ({ page, assessments: api }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  api.seedJobs([jobFor(previewFor(), 10, "succeeded")]);
  await openFinding(page, api); await openAI(page, api); await newDraft(page);
  const draft = draftMarker + "MOBILE\n" + "W".repeat(1400);
  api.payloadTexts.add(draft);
  await selectFacts(page); await contextField(page).fill(draft); await reviewed(page).check();
  await noMotionOrOverflow(page);
  const oldPreview = api.expectWrite(previewPath(), 201, true);
  await prepare(page).click(); await requested(oldPreview);
  await expect(prepare(page)).toBeDisabled();
  await closeAI(page, api); await release(page, oldPreview, true);
  await api.assertPrivate(page, true);
  await openAI(page, api); await newDraft(page);
  const fresh = await preparePreview(page, api, draft);
  await consent(page).check();
  const heldQueue = api.expectWrite(historyPath(), 202, true);
  await queue(page).click(); const queued = await requested(heldQueue); queueBody(queued, fresh);
  const committed = queued.response!.assessment as AssessmentJob;
  await closeFinding(page, api);
  await workspace(page).selectOption(beta.id);
  await release(page, heldQueue, true);
  await expect(page.locator("body")).not.toContainText(finding.title);
  await expect(page.locator("body")).not.toContainText(committed.id);
  await api.assertPrivate(page, true);
  expect(api.jobs.get(committed.id)).toBeDefined();
  api.mark("AAUI6 390px/reduced motion, pane/finding focus, actual aborted preview/queue ACK and nonrollback workspace clear completed");

  await workspace(page).selectOption(alpha.id);
  await openFinding(page, api, finding, false); await openAI(page, api);
  const retained = [...api.jobs.values()].find((job) => job.id !== committed.id)!;
  const heldDetail = api.queueRead(jobPath(retained.id), 200, true);
  await jobRow(page, retained.id).getByRole("button", { name: "View assessment", exact: true }).click();
  await requested(heldDetail);
  const unauthorized = api.queueRead(historyPath(), 401, true);
  await history(page).getByRole("button", { name: "Refresh assessment history", exact: true }).click();
  await requested(unauthorized); await release(page, unauthorized);
  await expect(page.getByRole("form", { name: "Sign in", exact: true })).toBeVisible();
  await expect(panel(page)).toHaveCount(0); await expect(dialog(page)).toHaveCount(0);
  await release(page, heldDetail, true); await api.assertPrivate(page, true);

  const login = page.getByRole("form", { name: "Sign in", exact: true });
  await login.getByRole("textbox", { name: "Email", exact: true }).fill(user.email);
  await login.getByLabel("Password", { exact: true }).fill(password);
  await login.getByRole("button", { name: "Sign in", exact: true }).click();
  await openFinding(page, api, finding, false); await openAI(page, api); await newDraft(page);
  await contextField(page).fill(draft); await api.assertPrivate(page);
  const pending = api.queueRead(historyPath(), 200, true);
  await history(page).getByRole("button", { name: "Refresh assessment history", exact: true }).click(); await requested(pending);
  await closeFinding(page, api);
  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  await release(page, pending, true);
  await expect(page.getByRole("form", { name: "Sign in", exact: true })).toBeVisible();
  await api.assertPrivate(page, true); await noMotionOrOverflow(page);
  expect(api.calls("POST", historyPath())).toHaveLength(1);
  expect(api.calls("POST", "/api/v1/logout")).toHaveLength(1);
  api.mark("AAUI6 401/login/logout cleared all AI payloads and aborted late scoped reads without another queue");
});
