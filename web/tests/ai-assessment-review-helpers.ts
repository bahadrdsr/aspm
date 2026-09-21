import type { Page } from "@playwright/test";
import { expect } from "./ai-assessments-fixture";
import type { AssessmentControl, AssessmentHTTP } from "./ai-assessments-fixture";
import { finding, grantFor, previewPath, primaryProfile, reviewedContext } from "./ai-assessments-data";
import type { AssessmentPreview } from "./ai-assessments-data";
import { requireProductionUI } from "./network";

export const pane = (page: Page) => page.getByRole("region", { name: "AI assessments", exact: true });
export const history = (page: Page) => pane(page).getByRole("region", { name: "Assessment history", exact: true });
export const rows = (page: Page) => history(page).getByRole("table", { name: "Assessment history", exact: true }).locator("tbody tr");
export const details = (page: Page) => pane(page).getByRole("region", { name: "Assessment details", exact: true });
export const draft = (page: Page) => pane(page).getByRole("form", { name: "Prepare AI assessment", exact: true });
export const context = (page: Page) => draft(page).getByRole("textbox", { name: "Reviewed finding context", exact: true });
export const reviewed = (page: Page) => draft(page).getByRole("checkbox", { name: "I reviewed and redacted this derived context", exact: true });
export const prepare = (page: Page) => draft(page).getByRole("button", { name: "Prepare assessment preview", exact: true });
export const consent = (page: Page) => pane(page).getByRole("checkbox", { name: "Approve this exact preview for queueing", exact: true });
export const queue = (page: Page) => pane(page).getByRole("button", { name: "Queue assessment", exact: true });
export const newAssessment = (page: Page) => pane(page).getByRole("button", { name: "New assessment", exact: true });
const findingDialog = (page: Page) => page.getByRole("dialog", { name: finding.title, exact: true });
const entry = (page: Page) => findingDialog(page).getByRole("button", { name: "AI assessments", exact: true });
const findingTrigger = (page: Page) => page.getByRole("table", { name: "Findings", exact: true })
  .getByRole("button", { name: finding.title, exact: true });

export async function frames(page: Page) {
  await page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))));
}
export async function arrived(control: AssessmentControl) {
  await expect.poll(() => control.call !== null, "Declared UI action reached the original guarded HTTP ledger").toBe(true);
  return control.call!;
}
export async function deliver(control: AssessmentControl) {
  control.release();
  await control.delivered;
}
export async function openFinding(page: Page, api: AssessmentHTTP, navigate = true) {
  requireProductionUI();
  const before = api.aiCalls().length;
  if (navigate) await page.goto("/#/work");
  await expect(findingTrigger(page)).toBeVisible();
  await findingTrigger(page).click();
  await expect(findingDialog(page)).toBeVisible();
  await expect(entry(page)).toHaveAttribute("aria-expanded", "false");
  expect(api.aiCalls()).toHaveLength(before);
}
export async function openPane(page: Page, api: AssessmentHTTP) {
  api.aiOpen = true;
  await entry(page).click();
  await expect(pane(page)).toBeVisible();
  await expect(page.getByRole("dialog")).toHaveCount(1);
}
export async function closePane(page: Page, api: AssessmentHTTP) {
  await pane(page).getByRole("button", { name: "Close AI assessments", exact: true }).click();
  api.aiOpen = false;
  await expect(pane(page)).toHaveCount(0);
  await expect(entry(page)).toBeFocused();
  await api.assertPrivate(page, true);
}
export async function closeFinding(page: Page, api: AssessmentHTTP) {
  await findingDialog(page).getByRole("button", { name: "Close finding details", exact: true }).click();
  api.aiOpen = false;
  await expect(findingDialog(page)).toHaveCount(0);
  await expect(findingTrigger(page)).toBeFocused();
  await api.assertPrivate(page, true);
}
export async function startDraft(page: Page) {
  await newAssessment(page).click();
  await expect(context(page)).toHaveValue("");
  await expect(reviewed(page)).not.toBeChecked();
}
export async function preparePreview(page: Page, api: AssessmentHTTP, text = reviewedContext): Promise<AssessmentPreview> {
  await draft(page).getByRole("combobox", { name: "Observation", exact: true }).selectOption(finding.observations[0].id);
  await draft(page).getByRole("combobox", { name: "AI profile", exact: true }).selectOption(primaryProfile.id);
  await draft(page).getByRole("combobox", { name: "AI grant", exact: true }).selectOption(grantFor().id);
  await context(page).fill(text);
  api.payloadTexts.add(text);
  await reviewed(page).check();
  const response = api.expectWrite(previewPath(), 201, true);
  await prepare(page).click();
  const call = await arrived(response);
  expect(call.body).toEqual({ observationId: finding.observations[0].id, profileId: primaryProfile.id,
    grantId: grantFor().id, context: text, reviewed: true });
  await deliver(response);
  const value = call.response!.preview as AssessmentPreview;
  const preview = pane(page).getByRole("region", { name: "Assessment preview", exact: true });
  await expect(preview).toContainText(value.id);
  const time = preview.getByLabel("Preview expires at", { exact: true });
  expect(Date.parse((await time.getAttribute("datetime"))!)).toBe(Date.parse(value.expiresAt));
  await expect(consent(page)).not.toBeChecked();
  await expect(queue(page)).toBeDisabled();
  return value;
}
