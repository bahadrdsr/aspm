import type { Locator, Page } from "@playwright/test";
import { requireProductionUI } from "./network";
import { policiesPath, policyDetail, policyPage, policyPath } from "./notification-policies-ui-data";
import type { PolicyControl } from "./notification-policies-ui-fixture";
import type { JiraControl } from "./jira-work-items-ui-fixture";
import { expect, test } from "./generic-webhook-ui-fixture";
import type { GenericWebhookUIAPI, WebhookControl } from "./generic-webhook-ui-fixture";
import {
  accepted, alpha, beta, connection, connectionDetail, connectionPage, connectionPath,
  connectionsPath, createInput, created, deliveryDetail, deliveryPage, deliveryPath,
  first, history, historyPath, preview, previewDetail,
  previewPath, profile, queued, replacementSecret, rotatedPath, rotatedURL, uncertain,
  webhookPolicy, webhookURL,
} from "./generic-webhook-ui-data";

test.use({ reducedMotion: "reduce", timezoneId: "UTC" });
test.beforeEach(async ({ webhooks }) => {
  requireProductionUI();
  expect(webhooks.requests).toEqual([]);
});

const section = (page: Page) => page.getByRole("region", { name: "Webhook connections", exact: true });
const toggle = (page: Page) => page.getByRole("button", { name: "Webhook connections", exact: true });
const table = (page: Page) => section(page).getByRole("table", { name: "Webhook connections", exact: true });
const row = (page: Page, name = connection.name) => table(page).getByRole("row").filter({ hasText: name });
const editor = (page: Page) => page.getByRole("dialog", { name: /(?:Add|Edit) webhook connection/i });
const form = (page: Page) => editor(page).getByRole("form", { name: /webhook connection/i });
const nameField = (scope: Locator) => scope.getByLabel(/^(?:Connection )?Name$/i);
const urlField = (scope: Locator) => scope.getByLabel(/^Webhook URL$/i);
const secretField = (scope: Locator) => scope.getByLabel(/^(?:New |Replacement )?(?:HMAC )?Secret$/i);
const enabled = (scope: Locator) => scope.getByRole("checkbox", { name: /Enabled/i })
  .or(scope.getByRole("switch", { name: /Enabled/i }));
const workTable = (page: Page) => page.getByRole("table", { name: "Findings", exact: true });
const findingRow = (page: Page) => workTable(page).getByRole("row").filter({ hasText: first.title });
const findingDialog = (page: Page) => page.getByRole("dialog", { name: first.title, exact: true });
const review = (page: Page) => page.getByRole("dialog", { name: /Review webhook/i });
const historyRegion = (page: Page) => page.getByRole("region", { name: /Webhook history/i });
const deliveryRegion = (page: Page) => page.getByRole("region", { name: /Webhook delivery details/i });
const policyRegion = (page: Page) => page.getByRole("region", { name: "Notification policies", exact: true });
const policyEditor = (page: Page) => page.getByRole("form", { name: "Edit notification policy", exact: true });

async function frames(page: Page) {
  await page.evaluate(() => new Promise<void>((resolve) =>
    requestAnimationFrame(() => requestAnimationFrame(() => resolve()))));
}
async function waitCalls(control: { calls: unknown[] }) {
  await expect.poll(() => control.calls.length, "The real App must reach the declared HTTP boundary.")
    .toBeGreaterThan(0);
}
async function settleWebhook(page: Page, api: GenericWebhookUIAPI, control: WebhookControl) {
  await waitCalls(control); control.release(); await frames(page);
  await expect.poll(() => control.calls.every((call) => call.finished || call.failure !== null)).toBe(true);
  api.finishWebhook(control);
}
async function settleJira(page: Page, api: GenericWebhookUIAPI, control: JiraControl) {
  await waitCalls(control); control.release(); await frames(page);
  await expect.poll(() => control.calls.every((call) => call.finished || call.failure !== null)).toBe(true);
  api.finish(control);
}
async function settlePolicy(page: Page, api: GenericWebhookUIAPI, control: PolicyControl) {
  await waitCalls(control); control.release(); await frames(page);
  await expect.poll(() => control.calls.every((call) => call.finished || call.failure !== null)).toBe(true);
  api.finishPolicy(control);
}
async function openIntegrations(page: Page, api: GenericWebhookUIAPI,
  policies = policyPage([])) {
  const navigation = api.navigationEntry();
  const policyRead = api.readPolicy(policiesPath, policies);
  await page.goto("/#/integrations");
  await expect(page.getByRole("heading", { name: "Integrations", exact: true })).toBeVisible();
  for (const control of navigation) await settleJira(page, api, control);
  await settlePolicy(page, api, policyRead);
  const catalog = page.getByRole("list", { name: "Native integrations", exact: true });
  await expect(catalog.getByRole("listitem")).toHaveCount(8);
  await expect(toggle(page), "Integrations must expose a separate non-native Webhook connections section.")
    .toBeVisible();
}
async function openConnections(page: Page, api: GenericWebhookUIAPI,
  items = [connection], workspace = alpha.id) {
  const read = api.listWebhooks(connectionsPath, connectionPage(items), { workspace, entry: true });
  await toggle(page).click();
  await expect(section(page)).toBeVisible();
  await settleWebhook(page, api, read);
}
async function bootWork(page: Page, api: GenericWebhookUIAPI) {
  const entry = api.entry();
  await page.goto("/#/work");
  await expect(workTable(page).locator("tbody tr")).toHaveCount(50);
  await expect.poll(() => entry.calls.length > 0 &&
    entry.calls.every((call) => call.finished || call.failure !== null)).toBe(true);
  api.finishEntry(entry);
}
async function openFinding(page: Page, api: GenericWebhookUIAPI) {
  await page.getByRole("textbox", { name: "Filter findings", exact: true }).fill(first.assetName);
  const detail = api.findingEntry();
  await findingRow(page).getByRole("button", { name: first.title, exact: true })
    .or(findingRow(page).getByRole("link", { name: first.title, exact: true })).click();
  await expect(findingDialog(page)).toBeVisible();
  await settleJira(page, api, detail);
}
async function setEnabled(scope: Locator, value: boolean) {
  const control = enabled(scope);
  if (await control.count()) {
    if (await control.evaluate((element) => element instanceof HTMLInputElement)) {
      await control.setChecked(value);
    } else if ((await control.getAttribute("aria-checked") === "true") !== value) {
      await control.click();
    }
  }
}

test("WH1 admin configures exact approved webhook metadata without a test send", async ({ page, webhooks }) => {
  await openIntegrations(page, webhooks);
  await openConnections(page, webhooks);
  await expect(row(page)).toContainText(connection.webhook.origin);
  await expect(row(page)).toContainText(connection.webhook.path);
  await expect(row(page)).toContainText(/not verified/i);
  await section(page).getByRole("button", { name: /Add webhook connection/i }).click();
  await expect(editor(page)).toBeVisible();
  await nameField(form(page)).fill(createInput.name);
  await urlField(form(page)).fill(createInput.webhookUrl);
  await secretField(form(page)).fill(createInput.secret);
  await expect(secretField(form(page))).toHaveAttribute("type", "password");
  await setEnabled(form(page), createInput.enabled);
  const create = webhooks.writeWebhook("POST", connectionsPath, createInput, connectionDetail(created), { status: 201 });
  await form(page).getByRole("button", { name: /(?:Add|Create|Save) webhook connection/i }).click();
  await settleWebhook(page, webhooks, create);
  await expect(editor(page)).toHaveCount(0);
  await webhooks.assertPrivate(page, true);
  await expect(row(page, created.name)).toContainText(created.webhook.origin);

  const updated = {
    ...structuredClone(created), name: "Synthetic rotated webhook",
    revision: 2, updatedAt: "2026-10-08T06:31:00.123456Z",
    webhook: { ...structuredClone(created.webhook), path: rotatedPath },
  };
  await row(page, created.name).getByRole("button", { name: /Edit webhook connection/i }).click();
  await expect(secretField(form(page))).toHaveValue("");
  await nameField(form(page)).fill(updated.name);
  await urlField(form(page)).fill(rotatedURL);
  const patch = webhooks.writeWebhook("PATCH", connectionPath(created.id), {
    name: updated.name, webhookUrl: rotatedURL,
  }, connectionDetail(updated));
  await form(page).getByRole("button", { name: /Save webhook connection/i }).click();
  await settleWebhook(page, webhooks, patch);
  await expect(editor(page)).toHaveCount(0);
  expect(webhooks.webhookCalls.filter((call) =>
    call.path.includes("/delivery-previews") || /\/deliveries$/.test(call.path))).toEqual([]);
  expect(webhooks.requests.some((call) => /test|verify|ping/i.test(call.path))).toBe(false);
  webhooks.mark("WH1 reached exact admin URL/secret configuration and sparse edit with no receiver/test-send semantics.");
});

test("WH2 viewer reads safe webhook metadata without configuration controls", async ({ page, webhooks }) => {
  webhooks.roles.set(alpha.id, "viewer"); webhooks.serverRoles.set(alpha.id, "viewer");
  await openIntegrations(page, webhooks, policyPage([webhookPolicy]));
  await openConnections(page, webhooks);
  await expect(row(page)).toContainText(connection.name);
  await expect(row(page)).toContainText(connection.webhook.origin);
  await expect(row(page)).toContainText(connection.webhook.path);
  await expect(section(page).getByRole("button", { name: /Add|Edit|Save|Test|Send/i })).toHaveCount(0);
  await expect(policyRegion(page).getByRole("button", { name: /Add|Edit|Save/i })).toHaveCount(0);
  await webhooks.assertPrivate(page);
  webhooks.mark("WH2 reached viewer-only safe webhook and policy metadata.");
});

test("WH3 finding review requires explicit queue consent and labels accepted versus uncertain honestly", async ({ page, webhooks }) => {
  await bootWork(page, webhooks);
  await openFinding(page, webhooks);
  const connectionRead = webhooks.listWebhooks(connectionsPath, connectionPage([connection]), { entry: true });
  await findingDialog(page).getByRole("button", { name: "Send webhook", exact: true }).click();
  await settleWebhook(page, webhooks, connectionRead);
  const chooser = page.getByRole("form", { name: /Send webhook/i });
  await chooser.getByLabel(/Webhook connection/i).selectOption(connection.id);
  const previewWrite = webhooks.writeWebhook("POST", previewPath(), {
    connectionId: connection.id,
  }, previewDetail(preview));
  await chooser.getByRole("button", { name: /Review webhook/i }).click();
  await settleWebhook(page, webhooks, previewWrite);
  await expect(review(page)).toBeVisible();
  for (const value of [
    preview.webhook.origin, preview.webhook.path, "hmac-sha256",
    preview.payload.title, preview.payload.body, "Explicit queue consent",
    "Operator-approved origin", "Receiver signature verification",
  ]) await expect(review(page)).toContainText(new RegExp(value, "i"));
  for (const value of [first.evidence.text, first.description, first.remediation]) {
    await expect(review(page)).not.toContainText(value);
  }
  let intent = "";
  const historyRead = webhooks.listWebhooks(historyPath(), deliveryPage(history), { entry: true });
  const enqueue = webhooks.writeWebhook("POST", historyPath(), (body) => {
    expect(Object.keys(body).sort()).toEqual([
      "confirm", "connectionId", "idempotencyKey", "previewDigest",
    ]);
    expect(body).toMatchObject({
      connectionId: connection.id, previewDigest: preview.bindingDigest, confirm: true,
    });
    expect(typeof body.idempotencyKey).toBe("string");
    intent = body.idempotencyKey as string;
    expect(intent.length).toBeGreaterThan(0);
  }, deliveryDetail(queued), { status: 202 });
  await review(page).getByRole("button", { name: "Queue webhook", exact: true }).click();
  await settleWebhook(page, webhooks, enqueue);
  const browserState = await page.evaluate(() => ({
    url: location.href,
    storage: [...Object.entries(localStorage), ...Object.entries(sessionStorage)],
  }));
  expect(JSON.stringify(browserState)).not.toContain(intent);
  await expect(page.getByText(/queued.*not sent|not sent.*queued/i)).toBeVisible();

  await frames(page);
  if (historyRead.calls.length === 0) {
    await findingDialog(page).getByRole("button", { name: /Webhook history/i }).click();
  }
  await settleWebhook(page, webhooks, historyRead);
  if (!await historyRegion(page).isVisible()) {
    await findingDialog(page).getByRole("button", { name: /Webhook history/i }).click();
  }
  await expect(historyRegion(page)).toBeVisible();
  const acceptedRead = webhooks.readWebhook(deliveryPath(accepted.id), deliveryDetail(accepted));
  await historyRegion(page).getByRole("row").filter({ hasText: accepted.id })
    .getByRole("button", { name: /View webhook delivery/i }).click();
  await settleWebhook(page, webhooks, acceptedRead);
  await expect(deliveryRegion(page)).toContainText(/endpoint accepted/i);
  await expect(deliveryRegion(page)).toContainText(/downstream processing not confirmed/i);
  await expect(deliveryRegion(page)).not.toContainText(/delivered successfully|receiver processed/i);

  const uncertainRead = webhooks.readWebhook(deliveryPath(uncertain.id), deliveryDetail(uncertain));
  await historyRegion(page).getByRole("row").filter({ hasText: uncertain.id })
    .getByRole("button", { name: /View webhook delivery/i }).click();
  await settleWebhook(page, webhooks, uncertainRead);
  await expect(deliveryRegion(page)).toContainText(/uncertain/i);
  await expect(deliveryRegion(page).getByRole("button", { name: /resend|retry|send again/i })).toHaveCount(0);
  await webhooks.assertPrivate(page);
  webhooks.mark("WH3 reached local signed review, explicit queue, manual history and honest accepted/uncertain labels.");
});

test("WH4 notification policy selects a generic webhook and scope loss clears held drafts and intent", async ({ page, webhooks }) => {
  await openIntegrations(page, webhooks, policyPage([webhookPolicy]));
  await openConnections(page, webhooks);
  const policyRow = policyRegion(page).getByRole("row").filter({ hasText: webhookPolicy.name });
  await expect(policyRow).toContainText(connection.name);
  await expect(policyRow).toContainText(/webhook/i);
  await policyRow.getByRole("button", { name: "Edit notification policy", exact: true }).click();
  await expect(policyEditor(page)).toBeVisible();
  const selected = policyEditor(page).getByLabel(/(?:Delivery|Webhook|Native) connection/i);
  await expect(selected).toHaveValue(connection.id);
  const saved = {
    ...structuredClone(webhookPolicy), minimumSeverity: "high" as const, revision: 3, epoch: 10,
    rationale: "Raise the signed webhook threshold to high severity.",
    updatedAt: "2026-10-08T06:32:00.123456Z",
  };
  await policyEditor(page).getByLabel("Minimum severity", { exact: true }).selectOption("high");
  await policyEditor(page).getByLabel("Rationale", { exact: true }).fill(saved.rationale);
  const savePolicy = webhooks.writePolicy("PATCH", policyPath(webhookPolicy.id), {
    minimumSeverity: "high", rationale: saved.rationale,
  }, policyDetail(saved));
  await policyEditor(page).getByRole("button", { name: "Save notification policy", exact: true }).click();
  await settlePolicy(page, webhooks, savePolicy);
  expect(webhooks.webhookCalls.filter((call) =>
    call.path.includes("/delivery-previews") || /\/deliveries$/.test(call.path))).toEqual([]);

  await section(page).getByRole("button", { name: /Add webhook connection/i }).click();
  await nameField(form(page)).fill("Held old-scope webhook draft");
  await urlField(form(page)).fill(webhookURL);
  await secretField(form(page)).fill(replacementSecret);
  await setEnabled(form(page), true);
  const held = webhooks.writeWebhook("POST", connectionsPath, {
    profile, name: "Held old-scope webhook draft", enabled: true,
    webhookUrl: webhookURL, secret: replacementSecret,
  }, connectionDetail(created), { status: 201, held: true });
  await form(page).getByRole("button", { name: /(?:Add|Create|Save) webhook connection/i }).click();
  await waitCalls(held);

  const betaNavigation = webhooks.navigationEntry(beta.id);
  const betaPolicies = webhooks.readPolicy(policiesPath, policyPage([]), { workspace: beta.id });
  const betaConnections = webhooks.listWebhooks(connectionsPath, connectionPage([]), {
    workspace: beta.id, entry: true,
  });
  await page.getByRole("combobox", { name: "Workspace", exact: true }).selectOption(beta.id);
  await expect(page.getByRole("combobox", { name: "Workspace", exact: true })).toHaveValue(beta.id);
  for (const control of betaNavigation) await settleJira(page, webhooks, control);
  await settlePolicy(page, webhooks, betaPolicies);
  await frames(page);
  if (betaConnections.calls.length) await settleWebhook(page, webhooks, betaConnections);
  else webhooks.omitWebhook(betaConnections);
  await expect(editor(page)).toHaveCount(0);
  webhooks.finishWebhookAborted(held);
  await webhooks.assertPrivate(page, true);
  await expect(page.locator("body")).not.toContainText("Held old-scope webhook draft");
  webhooks.mark("WH4 reached generic policy selection and aborted old-scope configuration intent with cleared URL/secret drafts.");
});
