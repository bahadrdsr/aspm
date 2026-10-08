import type { Page } from "@playwright/test";
import { requireProductionUI } from "./network";
import { expect, test } from "./notification-policies-ui-fixture";
import type { NotificationPolicyUIAPI, PolicyControl } from "./notification-policies-ui-fixture";
import type { JiraControl } from "./jira-work-items-ui-fixture";
import {
  alpha, eventPage, events, policiesPath, policy, policyDetail, policyEventsPath, policyPage, policyPath,
  savedPolicy, stalePolicy,
} from "./notification-policies-ui-data";

test.use({ reducedMotion: "reduce", timezoneId: "UTC" });
test.beforeEach(async ({ policies }) => { requireProductionUI(); expect(policies.requests).toEqual([]); });

const section = (page: Page) => page.getByRole("region", { name: "Notification policies", exact: true });
const table = (page: Page) => section(page).getByRole("table", { name: "Notification policies", exact: true });
const row = (page: Page, name = policy.name) => table(page).getByRole("row").filter({ hasText: name });
const editor = (page: Page) => page.getByRole("form", { name: "Edit notification policy", exact: true });
const history = (page: Page) => page.getByRole("region", { name: "Notification policy events", exact: true });

async function frames(page: Page) {
  await page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))));
}
async function arrived(control: PolicyControl) {
  await expect.poll(() => control.calls.length, "The real App must reach the declared policy HTTP boundary.").toBeGreaterThan(0);
}
async function settled(page: Page, api: NotificationPolicyUIAPI, control: PolicyControl) {
  await arrived(control); control.release(); await frames(page);
  await expect.poll(() => control.calls.every((call) => call.finished || call.failure !== null)).toBe(true);
  api.finishPolicy(control);
}
async function settledNavigation(page: Page, api: NotificationPolicyUIAPI, control: JiraControl) {
  await expect.poll(() => control.calls.length).toBeGreaterThan(0);
  control.release(); await frames(page);
  await expect.poll(() => control.calls.every((call) => call.finished || call.failure !== null)).toBe(true);
  api.finish(control);
}
async function navigation(page: Page, api: NotificationPolicyUIAPI, policyRead: PolicyControl) {
  const controls = api.navigationEntry();
  await page.goto("/#/integrations");
  await expect(page.getByRole("heading", { name: "Integrations", exact: true })).toBeVisible();
  for (const control of controls) await settledNavigation(page, api, control);
  await expect(section(page), "Integrations must expose explicit notification-policy management.").toBeVisible();
  await settled(page, api, policyRead);
  await expect(page.getByRole("button", { name: "Jira connections", exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Teams connections", exact: true })).toBeVisible();
  await expect(page.getByRole("region", { name: "Connections", exact: true })).toBeVisible();
  await expect(page.getByRole("region", { name: "Sources", exact: true })).toBeVisible();
}

test("NP1 admin reviews the bounded native trigger and saves an explicit revision", async ({ page, policies }) => {
  const read = policies.readPolicy(policiesPath, policyPage([policy]));
  await navigation(page, policies, read);
  await expect(row(page)).toContainText(policy.connection.name);
  await expect(row(page)).toContainText("Jira");
  await expect(row(page)).toContainText(/new/i);
  await expect(row(page)).toContainText(/changed/i);
  await expect(row(page)).toContainText(/reopened/i);
  await expect(row(page)).toContainText(/medium/i);
  await expect(row(page)).toContainText(policy.rationale);
  await row(page).getByRole("button", { name: "Edit notification policy", exact: true }).click();
  await expect(editor(page)).toBeVisible();
  await expect(editor(page).getByLabel("Native connection", { exact: true })).toHaveValue(policy.connectionId);
  await expect(editor(page).getByRole("checkbox", { name: "New findings", exact: true })).toBeChecked();
  await expect(editor(page).getByRole("checkbox", { name: "Changed findings", exact: true })).toBeChecked();
  await expect(editor(page).getByRole("checkbox", { name: "Reopened findings", exact: true })).toBeChecked();
  await expect(editor(page).getByLabel("Minimum severity", { exact: true })).toHaveValue("medium");
  await expect(editor(page).getByLabel("Rationale", { exact: true })).toHaveValue(policy.rationale);
  await editor(page).getByLabel("Minimum severity", { exact: true }).selectOption("high");
  await editor(page).getByLabel("Rationale", { exact: true }).fill(savedPolicy.rationale);
  const save = policies.writePolicy("PATCH", policyPath(policy.id), {
    minimumSeverity: "high", rationale: savedPolicy.rationale,
  }, policyDetail(savedPolicy));
  await editor(page).getByRole("button", { name: "Save notification policy", exact: true }).click();
  await settled(page, policies, save);
  await expect(editor(page)).toHaveCount(0);
  await expect(row(page)).toContainText(/high/i);
  await expect(row(page)).toContainText(savedPolicy.rationale);
  await expect(section(page)).not.toContainText(/\b(?:sent successfully|delivered successfully|provider accepted)\b/i);
  expect(policies.policyCalls.filter((call) => call.path.includes("/deliveries") || call.path.includes("/delivery-previews"))).toEqual([]);
  policies.mark("NP1 reached explicit admin review/save with native connection, trigger, threshold and rationale, without provider semantics.");
});

test("NP2 viewer sees read-only stale and duplicate outcomes without a delivery claim", async ({ page, policies }) => {
  policies.roles.set(alpha.id, "viewer"); policies.serverRoles.set(alpha.id, "viewer");
  const read = policies.readPolicy(policiesPath, policyPage([stalePolicy]));
  await navigation(page, policies, read);
  await expect(row(page, stalePolicy.name)).toContainText(/connection stale/i);
  await expect(row(page, stalePolicy.name)).toContainText(/disabled/i);
  await expect(section(page).getByRole("button", { name: /add|edit|save/i })).toHaveCount(0);
  const eventRead = policies.readPolicy(policyEventsPath(stalePolicy.id), eventPage(events), { query: { limit: "100" } });
  await row(page, stalePolicy.name).getByRole("button", { name: "View notification policy events", exact: true }).click();
  await settled(page, policies, eventRead);
  const rows = history(page).getByRole("table", { name: "Notification policy events", exact: true }).getByRole("row");
  await expect(rows.filter({ hasText: events[0].id })).toContainText(/queued for delivery/i);
  await expect(rows.filter({ hasText: events[0].id })).toContainText(/not delivered/i);
  await expect(rows.filter({ hasText: events[1].id })).toContainText(/connection stale/i);
  await expect(rows.filter({ hasText: events[2].id })).toContainText(/duplicate ticket/i);
  await expect(rows.filter({ hasText: events[2].id })).toContainText(events[2].deliveryId!);
  await expect(history(page)).not.toContainText(/\b(?:sent|delivered successfully|provider accepted)\b/i);
  expect(policies.policyCalls.filter((call) => call.method !== "GET")).toEqual([]);
  policies.mark("NP2 reached viewer-only policy history with explicit queued, stale and duplicate-ticket outcomes.");
});
