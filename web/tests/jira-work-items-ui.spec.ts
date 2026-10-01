import type { Locator, Page } from "@playwright/test";
import { requireProductionUI } from "./network";
import { expect, test } from "./jira-work-items-ui-fixture";
import type { JiraControl, JiraUIAPI } from "./jira-work-items-ui-fixture";
import type { WorkControl } from "./saved-work-views-fixture";
import { currentOwner, password, user } from "./work-search-data";
import { primaryView, viewDetail, viewPage } from "./saved-work-views-data";
import {
  alpha, alternate, apiVersion, attempted, beta, blocked, confirmed, connection, connectionDetail, connectionPages,
  connectionPath, connectionsPath, created, createFields, createInput, deliveryDetail, deliveryPath, draftToken,
  findingPath, first, historyPath, historyRows, limited, localPreview, missingFields, nativeAuth, nativePage,
  preview, previewDetail, previewPath, queued, replacementToken, sources, target, uncertain,
} from "./jira-work-items-ui-data";
import type { JiraConnection, JiraDelivery, JiraPreview, JiraTarget } from "./jira-work-items-ui-data";

test.use({ reducedMotion: "reduce", timezoneId: "UTC" });
test.beforeEach(async ({ jira }) => { requireProductionUI(); expect(jira.requests).toEqual([]); });
const navigation = (page: Page) => page.getByRole("navigation", { name: "Primary", exact: true });
const toggle = (page: Page) => page.getByRole("button", { name: "Jira connections", exact: true });
const panel = (page: Page) => page.getByRole("region", { name: "Jira connections", exact: true });
const connectionRows = (page: Page) => panel(page).getByRole("table", { name: "Jira connections", exact: true }).locator("tbody tr");
const connectionRow = (page: Page, name = connection.name) => connectionRows(page).filter({ hasText: name });
const editor = (page: Page) => page.getByRole("form", { name: /^(?:Add|Edit) Jira connection$/ });
const save = (page: Page) => editor(page).getByRole("button", { name: /^(?:Save|Add) Jira connection$/ });
const token = (page: Page) => editor(page).getByLabel("OAuth bearer token", { exact: true });
const mappings = (page: Page) => editor(page).getByRole("group", { name: "Custom field mapping", exact: true });
const refreshConnections = (page: Page) => page.getByRole("button", { name: "Refresh Jira connections", exact: true });
const retryConnections = (page: Page) => page.getByRole("button", { name: "Retry Jira connections", exact: true });
const workTable = (page: Page) => page.getByRole("table", { name: "Findings", exact: true, includeHidden: true });
const workRow = (page: Page) => workTable(page).getByRole("row", { includeHidden: true }).filter({ hasText: first.title });
const findingTrigger = (page: Page) => workRow(page).getByRole("button", { name: first.title, exact: true, includeHidden: true });
const selection = (page: Page) => workRow(page).getByRole("checkbox", { name: `Select ${first.title}`, exact: true, includeHidden: true });
const filter = (page: Page) => page.getByRole("textbox", { name: "Filter findings", exact: true, includeHidden: true });
const details = (page: Page) => page.getByRole("dialog", { name: first.title, exact: true });
const createEntry = (page: Page) => details(page).getByRole("button", { name: "Create Jira work item", exact: true });
const review = (page: Page) => details(page).getByRole("region", { name: "Jira work item review", exact: true });
const queue = (page: Page) => review(page).getByRole("button", { name: "Queue Jira work item", exact: true });
const sameIntent = (page: Page) => review(page).getByRole("button", { name: "Confirm same Jira work item", exact: true });
const closeReview = (page: Page) => review(page).getByRole("button", { name: "Close Jira review", exact: true });
const previewAgain = (page: Page) => review(page).getByRole("button", { name: /^(?:Refresh|Retry) Jira preview$/ });
const choose = (page: Page) => review(page).getByRole("combobox", { name: "Jira connection", exact: true });
const historyEntry = (page: Page) => details(page).getByRole("button", { name: "Jira work item history", exact: true });
const history = (page: Page) => details(page).getByRole("region", { name: "Jira work item history", exact: true });
const historyTable = (page: Page) => history(page).getByRole("table", { name: "Jira work items", exact: true });
const historyRow = (page: Page, id: string) => historyTable(page).getByRole("row").filter({ hasText: id });
const delivery = (page: Page) => details(page).getByRole("region", { name: "Jira work item details", exact: true });
const deliveryStatus = (page: Page) => delivery(page).getByRole("status", { name: "Jira work item status", exact: true });
const refreshDelivery = (page: Page) => delivery(page).getByRole("button", { name: "Refresh Jira work item", exact: true });
const retryDelivery = (page: Page) => delivery(page).getByRole("button", { name: "Retry Jira work item", exact: true });
const workspace = (page: Page) => page.getByRole("combobox", { name: "Workspace", exact: true });

async function frames(page: Page) {
  await page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))));
}
async function arrived(control: JiraControl) {
  await expect.poll(() => control.calls.length, "The real App must reach this declared HTTP boundary.").toBeGreaterThan(0);
  return control.calls[0];
}
async function settled(page: Page, api: JiraUIAPI, control: JiraControl) {
  await arrived(control); control.release(); await frames(page);
  await expect.poll(() => control.calls.every((call) => call.finished || call.failure !== null ||
    control.status === 401 && call.responseStatus === 401)).toBe(true);
  api.finish(control);
}
async function inherited(control: WorkControl) {
  await expect.poll(() => control.call !== null).toBe(true); control.release();
  await control.delivered;
  await expect.poll(() => control.call?.finished || control.call?.failure !== null).toBe(true);
}
async function noIO(page: Page, api: JiraUIAPI, action: () => Promise<unknown>) {
  const before = api.requests.length;
  await action(); await frames(page);
  expect(api.requests, "Local input or close must not cross an HTTP boundary.").toHaveLength(before);
}
async function optionalRead(page: Page, api: JiraUIAPI, control: JiraControl) {
  await frames(page);
  if (control.calls.length) await settled(page, api, control);
  else api.omitUnrequested(control);
}
async function quiet(page: Page, api: JiraUIAPI) {
  const before = api.requests.length;
  await frames(page); await page.waitForTimeout(120);
  expect(api.requests, "No polling, hidden preview, automatic queue or resend during this bounded observation.").toHaveLength(before);
}
async function noEnabled(action: Locator) {
  for (const control of await action.all()) await expect(control).toBeDisabled();
}
async function finishNavigation(page: Page, api: JiraUIAPI, controls: JiraControl[]) {
  await expect(page.getByRole("heading", { name: "Integrations", exact: true })).toBeVisible();
  await expect(page.getByRole("region", { name: "Connections", exact: true })).toBeVisible();
  await expect(page.getByRole("region", { name: "Sources", exact: true })).toBeVisible();
  for (const control of controls) await settled(page, api, control);
}
async function integrations(page: Page, api: JiraUIAPI) {
  const controls = api.navigationEntry();
  await page.goto("/#/integrations"); await finishNavigation(page, api, controls);
  await expect(navigation(page).getByRole("link")).toHaveCount(5);
  const catalog = page.getByRole("list", { name: "Native integrations", exact: true });
  await expect(catalog.getByRole("listitem")).toHaveCount(8);
  await expect(page.getByRole("button", { name: "AI settings", exact: true })).toHaveAttribute("aria-expanded", "false");
  expect(api.jiraCalls).toHaveLength(0);
  expect(api.requests.filter((call) => call.path === connectionsPath).every((call) => !("profile" in call.query))).toBe(true);
  api.mark("Reached published Integrations: five nav links, eight families, original Slack/Sources queries, lazy AI, zero Jira HTTP.");
  await expect(toggle(page), "Published Integrations is missing the collapsed Jira connections entry.").toBeVisible();
  await expect(toggle(page)).toHaveAttribute("aria-expanded", "false"); await expect(panel(page)).toHaveCount(0);
  api.mark("Jira connections control reached; setup/edit tails executable.");
}
async function openConnections(page: Page, api: JiraUIAPI, values = [connection]) {
  const read = api.list(connectionsPath, nativePage(values), { entry: true });
  await toggle(page).click(); await expect(panel(page)).toBeVisible(); await settled(page, api, read);
  await expect(toggle(page)).toHaveAttribute("aria-expanded", "true");
  await expect(connectionRows(page)).toHaveCount(values.length);
  await expect(panel(page)).toContainText(/not[- ]verified|not (?:live )?verified/i);
}
async function bootWork(page: Page, api: JiraUIAPI, reload = false) {
  const before = api.jiraCalls.length;
  const entry = api.entry();
  if (reload) await page.reload(); else await page.goto("/#/work");
  await expect(workTable(page).locator("tbody tr")).toHaveCount(50); await frames(page);
  await expect.poll(() => entry.calls.length > 0 && entry.calls.every((call) => call.finished || call.failure !== null)).toBe(true);
  api.finishEntry(entry);
  await expect(navigation(page).getByRole("link")).toHaveCount(5);
  expect(api.jiraCalls).toHaveLength(before);
}
async function openFinding(page: Page, api: JiraUIAPI, mode: "create" | "history" = "create") {
  await noIO(page, api, () => filter(page).fill(first.assetName));
  await selection(page).check();
  const read = api.findingEntry();
  const before = api.jiraCalls.length;
  await findingTrigger(page).click(); await expect(details(page)).toBeVisible(); await settled(page, api, read);
  expect(api.jiraCalls).toHaveLength(before);
  await expect(details(page).getByRole("button", { name: "Delivery history", exact: true })).toBeVisible();
  api.mark("Reached real finding detail with original Work selection/filter and Slack control; finding open alone made no Jira HTTP.");
  await expect(mode === "create" ? createEntry(page) : historyEntry(page),
    `Published finding detail is missing its explicit Jira ${mode} entry.`).toBeVisible();
  api.mark(`Jira ${mode} entry reached; local review/history tails executable.`);
}
async function showPreview(page: Page, api: JiraUIAPI, value = preview) {
  await expect(review(page)).toBeVisible();
  await expect(page.getByRole("dialog")).toHaveCount(1);
  await expect(review(page)).toContainText(/native validation(?:\s*:)? not run/i);
  await expect(review(page)).toContainText(/required fields.*worker|worker.*required fields/i);
  await expect(review(page)).toContainText(/permissions.*worker|worker.*permissions/i);
  for (const text of [value.payload.title, value.payload.body, value.jira.project, value.jira.issueType, value.jira.siteOrigin,
    ...Object.keys(value.payload.fields), ...Object.values(value.payload.fields)]) await expect(review(page)).toContainText(text);
  const findingLink = review(page).getByRole("link", { name: /^(?:View|Open) finding$/ });
  await expect(findingLink).toHaveAttribute("href", value.payload.deepLink);
  for (const link of await review(page).getByRole("link").all()) {
    await expect(link).toHaveAttribute("rel", /noopener/); await expect(link).toHaveAttribute("rel", /noreferrer/);
  }
  for (const text of [first.evidence.text, first.description, first.remediation]) await expect(review(page)).not.toContainText(text);
  await expect(queue(page).or(sameIntent(page))).toHaveCount(1);
  await expect(queue(page).or(sameIntent(page))).toBeEnabled(); await api.assertPrivate(page);
}
async function enterReview(page: Page, api: JiraUIAPI, selected = connection, value = preview, keyboard = false) {
  const before = api.jiraCalls.filter((call) => call.method === "POST" && call.path === historyPath()).length;
  const metadata = api.list(connectionsPath, nativePage([selected]), { entry: true });
  const local = api.write("POST", previewPath(), { connectionId: selected.id }, previewDetail(value), { held: true });
  if (keyboard) { await createEntry(page).focus(); await createEntry(page).press("Space"); }
  else await createEntry(page).click();
  await settled(page, api, metadata); await arrived(local);
  await noEnabled(queue(page).or(sameIntent(page)));
  expect(api.jiraCalls.filter((call) => call.method === "POST" && call.path === historyPath())).toHaveLength(before);
  await settled(page, api, local); await showPreview(page, api, value);
}
function queueBody(value: JiraPreview, capture: (key: string) => void, original?: string) {
  return (body: Record<string, unknown>) => {
    expect(Object.keys(body).sort()).toEqual(["confirm", "connectionId", "idempotencyKey", "previewDigest"]);
    expect(body).toMatchObject({ connectionId: value.connectionId, previewDigest: value.bindingDigest, confirm: true });
    expect(typeof body.idempotencyKey).toBe("string");
    const key = body.idempotencyKey as string;
    expect(Buffer.byteLength(key, "utf8")).toBeLessThanOrEqual(256);
    expect(key).toMatch(/^\S+$/); expect(key).not.toMatch(/[\u0000-\u001f\u007f]/);
    if (original !== undefined) expect(key, "Lost ACK must retain the original explicit intent key.").toBe(original);
    capture(key);
  };
}
async function add(page: Page, api: JiraUIAPI) {
  await noIO(page, api, () => panel(page).getByRole("button", { name: "Add Jira connection", exact: true }).click());
  await expect(editor(page)).toBeVisible(); await expect(token(page)).toHaveAttribute("type", "password");
  await expect(token(page)).toHaveValue("");
  await expect(editor(page)).toContainText(/Cloud v3/i); await expect(editor(page)).toContainText(/OAuth.*bearer/i);
  await expect(editor(page).getByRole("textbox", { name: /email|API token|Basic|JSON|template|script/i })).toHaveCount(0);
}
async function mapping(page: Page, api: JiraUIAPI, id: string, source: string) {
  await noIO(page, api, () => editor(page).getByRole("button", { name: "Add field mapping", exact: true }).click());
  const row = mappings(page).last();
  await noIO(page, api, () => row.getByLabel("Custom field ID", { exact: true }).fill(id));
  const select = row.getByRole("combobox", { name: "String source", exact: true });
  const values = await select.locator("option").evaluateAll((options) => options.map((option) => (option as HTMLOptionElement).value).filter(Boolean));
  expect(values.sort()).toEqual([...sources].sort());
  await noIO(page, api, () => select.selectOption(source));
}
async function fillTarget(page: Page, api: JiraUIAPI, value: JiraTarget, alternateGateway = false) {
  for (const [label, text] of [["Cloud ID", value.cloudId], ["Site origin", value.siteOrigin],
    ["Project", value.project], ["Issue type", value.issueType]]) {
    await noIO(page, api, () => editor(page).getByLabel(label, { exact: true }).fill(text));
  }
  const field = editor(page).getByLabel("API base", { exact: true });
  await expect(field, "The exact API credential destination must be visible and reviewable.").toBeVisible();
  if (alternateGateway) {
    const advanced = editor(page).getByRole("button", { name: "Advanced gateway", exact: true });
    if (await advanced.count()) await noIO(page, api, () => advanced.click());
    await noIO(page, api, () => field.fill(value.apiBase));
  }
  await expect(field).toHaveValue(value.apiBase);
}
async function newDraft(page: Page, api: JiraUIAPI) {
  await add(page, api);
  await noIO(page, api, () => editor(page).getByLabel("Name", { exact: true }).fill(createInput.name));
  await fillTarget(page, api, target);
  await noIO(page, api, () => token(page).fill(draftToken));
  await noIO(page, api, () => editor(page).getByRole("checkbox", { name: "Enabled", exact: true }).setChecked(false));
  for (const [id, source] of Object.entries(target.fieldMappings)) await mapping(page, api, id, source);
}
async function edit(page: Page, api: JiraUIAPI, value: JiraConnection) {
  const read = api.read(connectionPath(value.id), connectionDetail(value), { entry: true });
  await connectionRow(page, value.name).getByRole("button", { name: "Edit Jira connection", exact: true }).click();
  await settled(page, api, read); await expect(editor(page)).toBeVisible(); await expect(token(page)).toHaveValue("");
}
async function closedEditor(page: Page, api: JiraUIAPI) {
  await expect(editor(page)).toHaveCount(0); await api.assertPrivate(page, true);
}
async function unchanged(page: Page) {
  await expect(details(page)).toContainText(first.evidence.text);
  await expect(details(page)).toContainText(first.description);
  await expect(details(page)).toContainText(first.remediation);
  await expect(details(page)).toContainText("No independently verified resolution is recorded.");
  await expect(workRow(page)).toContainText(currentOwner.name);
  await expect(workRow(page).getByRole("cell", { includeHidden: true }).filter({ hasText: /^Open$/ })).toHaveCount(1);
  await expect(selection(page)).toBeChecked(); await expect(filter(page)).toHaveValue(first.assetName);
}

test("JU1 lazy setup, native paging and denied metadata, strict explicit creation without probes", async ({ page, jira }) => {
  await integrations(page, jira);
  const firstPage = jira.list(connectionsPath, nativePage(connectionPages.slice(0, 100), 101, connectionPages[99].id), { entry: true });
  await toggle(page).click(); await settled(page, jira, firstPage);
  await expect(connectionRows(page)).toHaveCount(100); await expect(panel(page)).toContainText(/101/);
  const tail = jira.list(connectionsPath, nativePage(connectionPages.slice(100), 101), { cursor: connectionPages[99].id });
  await panel(page).getByRole("button", { name: "Next Jira connections", exact: true }).click(); await settled(page, jira, tail);
  await expect(connectionRows(page)).toHaveCount(1); await expect(panel(page)).toContainText(/101/);
  for (const status of [403, 503] as const) {
    const read = jira.list(connectionsPath, null, { status });
    await (status === 403 ? refreshConnections(page) : retryConnections(page)).click(); await settled(page, jira, read);
    await expect(connectionRows(page)).toHaveCount(0); await expect(panel(page).getByRole("alert")).toBeVisible();
    await noEnabled(panel(page).getByRole("button", { name: "Add Jira connection", exact: true }));
  }
  const recovered = jira.list(connectionsPath, nativePage([connection]));
  await retryConnections(page).click(); await settled(page, jira, recovered);
  await newDraft(page, jira);
  for (const [label, invalid, valid] of [
    ["Name", "\u00e9".repeat(129), createInput.name],
    ["Cloud ID", target.cloudId.toUpperCase(), target.cloudId],
    ["Site origin", "http://jira.synthetic.invalid", target.siteOrigin],
    ["Site origin", `${target.siteOrigin}/path?token=no`, target.siteOrigin],
    ["Project", "lowercase", target.project], ["Project", "A".repeat(256), target.project],
    ["Issue type", "0", target.issueType], ["Issue type", "1".repeat(21), target.issueType],
    ["OAuth bearer token", "Bearer synthetic", draftToken],
  ]) {
    const field = editor(page).getByLabel(label, { exact: true });
    await noIO(page, jira, async () => { await field.fill(invalid); await save(page).click(); });
    await expect(field, "Invalid input must not be truncated or coerced.").toHaveValue(invalid);
    await expect(editor(page).getByRole("alert")).toBeVisible(); await field.fill(valid);
  }
  const secondID = mappings(page).nth(1).getByLabel("Custom field ID", { exact: true });
  for (const invalid of ["summary", "customfield_10010", `customfield_${"1".repeat(117)}`]) {
    await noIO(page, jira, async () => { await secondID.fill(invalid); await save(page).click(); });
    await expect(secondID).toHaveValue(invalid); await expect(editor(page).getByRole("alert")).toBeVisible();
  }
  await secondID.fill("customfield_10011");
  for (let index = 2; index < 16; index++) await mapping(page, jira, `customfield_${10010 + index}`, sources[index % sources.length]);
  await expect(mappings(page)).toHaveCount(16);
  await expect(editor(page).getByRole("button", { name: "Add field mapping", exact: true })).toBeDisabled();
  await expect(editor(page)).toContainText(/16/);
  for (let index = 16; index > 2; index--) await noIO(page, jira,
    () => mappings(page).last().getByRole("button", { name: "Remove field mapping", exact: true }).click());
  const write = jira.write("POST", connectionsPath, createInput, connectionDetail(created), { status: 201, held: true });
  const refresh = jira.list(connectionsPath, nativePage([connection, created]));
  await save(page).click(); await arrived(write); await expect(save(page)).toBeDisabled();
  expect(write.calls).toHaveLength(1); await jira.assertPrivate(page);
  await settled(page, jira, write); await closedEditor(page, jira);
  await optionalRead(page, jira, refresh);
  await expect(connectionRow(page, created.name)).toContainText(/not[- ]verified|not (?:live )?verified/i);
  await expect(page.getByRole("list", { name: "Native integrations" }).getByRole("listitem").filter({
    has: page.getByRole("heading", { name: "Jira", exact: true }),
  })).toContainText(/not verified/i);
  expect(jira.jiraCalls.filter((call) => call.method === "POST")).toHaveLength(1);
  await quiet(page, jira);
  jira.mark("JU1 completed: explicit 201 metadata only, exact target and mapping choices, no browser native probe or permission success.");
});

test("JU2 sparse edit, blank credential, whole-target replacement, malformed ACK and current roles", async ({ page, jira }) => {
  await integrations(page, jira); await openConnections(page, jira); await edit(page, jira, connection);
  const named = { ...connection, name: "Synthetic renamed Jira connection", revision: 5 };
  await noIO(page, jira, () => editor(page).getByLabel("Name", { exact: true }).fill(named.name));
  let ack = jira.write("PATCH", connectionPath(connection.id), { name: named.name }, connectionDetail(named));
  let refresh = jira.list(connectionsPath, nativePage([named]));
  await save(page).click(); await settled(page, jira, ack); await closedEditor(page, jira);
  await optionalRead(page, jira, refresh);
  await edit(page, jira, named);
  await expect(save(page), "Unchanged edit must not send an empty or credential-clearing PATCH.").toBeDisabled();
  await noIO(page, jira, () => token(page).fill(replacementToken));
  const rotated = { ...named, revision: 6 };
  ack = jira.write("PATCH", connectionPath(connection.id), { token: replacementToken }, connectionDetail(rotated));
  refresh = jira.list(connectionsPath, nativePage([rotated]));
  await save(page).click(); await settled(page, jira, ack); await closedEditor(page, jira);
  await optionalRead(page, jira, refresh);
  await edit(page, jira, rotated);
  const nextTarget = { ...target, apiBase: `https://approved-gateway.synthetic.invalid/ex/jira/${target.cloudId}`,
    siteOrigin: "https://next-jira.synthetic.invalid", project: "NEXT" };
  await fillTarget(page, jira, nextTarget, true);
  for (const invalid of [
    `${nextTarget.apiBase}?permission=approved`, `${nextTarget.apiBase}/`,
    `https://user:password@approved-gateway.synthetic.invalid/ex/jira/${target.cloudId}`,
    `https://approved-gateway.synthetic.invalid/ex/jira/${alternate.jira.cloudId}`,
  ]) {
    const field = editor(page).getByLabel("API base", { exact: true });
    await noIO(page, jira, async () => { await field.fill(invalid); await save(page).click(); });
    await expect(field).toHaveValue(invalid); await expect(editor(page).getByRole("alert")).toBeVisible();
    await noIO(page, jira, () => field.fill(nextTarget.apiBase));
  }
  await noIO(page, jira, () => editor(page).getByRole("checkbox", { name: "Enabled", exact: true }).uncheck());
  const next = { ...rotated, enabled: false, jira: nextTarget, revision: 7 };
  const body = { enabled: false, jira: nextTarget };
  const invalid = [
    { ...next, profile: "slack-workspace-bot" }, { ...next, workspaceId: beta.id },
    { ...next, id: alternate.id }, { ...next, revision: "7" }, { ...next, revision: 0 },
    { ...next, jira: { ...nextTarget, apiBase: `${nextTarget.apiBase}/` } },
    { ...next, permissionState: "verified" },
  ];
  for (const wrong of invalid) {
    const rejected = jira.write("PATCH", connectionPath(connection.id), body, { apiVersion, connection: wrong });
    await save(page).click(); await settled(page, jira, rejected);
    await expect(editor(page).getByRole("alert")).toContainText(/invalid|not.*confirm|unrecognized/i);
    await expect(editor(page).getByLabel("Project", { exact: true })).toHaveValue("NEXT");
    await expect(token(page)).toHaveValue("");
  }
  const wrongStatus = jira.write("PATCH", connectionPath(connection.id), body, connectionDetail(next), { status: 201 });
  await save(page).click(); await settled(page, jira, wrongStatus); await expect(editor(page)).toBeVisible();
  ack = jira.write("PATCH", connectionPath(connection.id), body, connectionDetail(next));
  refresh = jira.list(connectionsPath, nativePage([next]));
  await save(page).click(); await settled(page, jira, ack); await closedEditor(page, jira);
  await optionalRead(page, jira, refresh);
  await expect(connectionRow(page, next.name)).toContainText(/disabled/i);
  await edit(page, jira, next);
  await editor(page).getByLabel("Name", { exact: true }).fill("Forbidden cached admin edit");
  jira.serverRoles.set(alpha.id, "analyst");
  const forbidden = jira.write("PATCH", connectionPath(connection.id), { name: "Forbidden cached admin edit" }, null, {
    status: 403, errorMessage: `<img src="https://untrusted.synthetic.invalid/${draftToken}">`,
  });
  await save(page).click(); await settled(page, jira, forbidden);
  await expect(page.getByRole("alert")).toContainText(/permission|denied|not permitted/i);
  await noEnabled(save(page)); await jira.assertPrivate(page); await quiet(page, jira);
  for (const role of ["analyst", "viewer"] as const) {
    jira.roles.set(alpha.id, role); jira.serverRoles.set(alpha.id, role);
    const nav = jira.navigationEntry(); await page.reload(); await finishNavigation(page, jira, nav);
    await openConnections(page, jira, [next]);
    await expect(panel(page).getByRole("button", { name: /^(?:Add|Edit) Jira connection$/ })).toHaveCount(0);
    await expect(panel(page)).toContainText(/read.only|administrator|admin/i);
    await expect(panel(page)).toContainText(/not[- ]verified|not (?:live )?verified/i);
  }
  jira.mark("JU2 completed: sparse exact PATCHes, no blank token write, strict receipt parsing and current-role denial.");
});

test("JU3 canonical local preview and explicit bound queue preserve personal Work and require a target choice", async ({ page, jira }) => {
  jira.roles.set(alpha.id, "analyst"); jira.serverRoles.set(alpha.id, "analyst");
  await bootWork(page, jira);
  const personal = { ...primaryView, name: "Synthetic Jira personal focus", query: first.assetName, sort: "title" as const };
  const views = jira.viewsEntry(viewPage([personal]), alpha.id, false);
  await page.getByRole("button", { name: "Saved views", exact: true }).click();
  const personalRow = page.getByRole("list", { name: "Personal saved views", exact: true }).getByRole("listitem");
  await expect(personalRow).toContainText(personal.name); await frames(page);
  await expect.poll(() => views.calls.length > 0 && views.calls.every((call) => call.finished || call.failure !== null)).toBe(true);
  jira.finishViewsEntry(views);
  const view = jira.detailView(personal.id, viewDetail(personal), { held: false });
  const work = jira.search(personal.query, "", 200, false);
  await personalRow.getByRole("button", { name: "Apply", exact: true }).click();
  await inherited(view); await inherited(work); await expect(filter(page)).toHaveValue(first.assetName);
  await expect(page.getByRole("status", { name: "Workspace search", exact: true })).toContainText(first.assetName);
  jira.mark("Reached existing personal saved-view list, canonical apply/detail and authorized Work search before the Jira entry.");
  await openFinding(page, jira); await enterReview(page, jira);
  const acknowledged = queued();
  let key = "";
  const enqueue = jira.write("POST", historyPath(), queueBody(preview, (value) => { key = value; }),
    deliveryDetail(acknowledged), { status: 202, held: true });
  await queue(page).click(); await arrived(enqueue); await noEnabled(queue(page));
  expect(enqueue.calls).toHaveLength(1);
  await expect(workRow(page)).toContainText(currentOwner.name);
  await expect(workRow(page).getByRole("cell", { includeHidden: true }).filter({ hasText: /^Open$/ })).toHaveCount(1);
  await settled(page, jira, enqueue);
  expect(key).not.toBe("");
  await expect(deliveryStatus(page)).toContainText(/queued/i);
  await expect(deliveryStatus(page)).toContainText(/not (?:yet )?created|waiting.*worker|awaiting.*worker/i);
  await expect(delivery(page)).toContainText(acknowledged.id);
  await expect(delivery(page).getByText(/^(?:Created in Jira|Confirmed|Issue created)$/)).toHaveCount(0);
  await quiet(page, jira);
  await noIO(page, jira, () => closeReview(page).click()); await expect(createEntry(page)).toBeFocused();
  await unchanged(page);
  await details(page).getByRole("button", { name: "Close finding details", exact: true }).click();
  await expect(findingTrigger(page)).toBeFocused(); await expect(selection(page)).toBeChecked();
  await expect(filter(page)).toHaveValue(first.assetName); await expect(personalRow).toContainText(personal.name);
  await expect(workTable(page).getByRole("columnheader").filter({ hasText: /^Finding$/ })).toHaveAttribute("aria-sort", "ascending");
  await openFinding(page, jira);
  const multiple = jira.list(connectionsPath, nativePage([connection, alternate]), { entry: true });
  const previewCount = jira.jiraCalls.filter((call) => call.path === previewPath()).length;
  await createEntry(page).click(); await settled(page, jira, multiple);
  await expect(choose(page)).toHaveValue(""); await noEnabled(queue(page)); await quiet(page, jira);
  expect(jira.jiraCalls.filter((call) => call.path === previewPath())).toHaveLength(previewCount);
  const selected = localPreview(alternate, first, "b");
  const explicit = jira.write("POST", previewPath(), { connectionId: alternate.id }, previewDetail(selected));
  await choose(page).selectOption(alternate.id); await settled(page, jira, explicit); await showPreview(page, jira, selected);
  for (const wrong of [
    { ...selected, workspaceId: beta.id }, { ...selected, findingId: "f".repeat(32) },
    { ...selected, requestedBy: "f".repeat(32) }, { ...selected, connectionRevision: selected.connectionRevision + 1 },
    { ...selected, bindingDigest: `sha256:${"B".repeat(64)}` },
    { ...selected, jira: target },
    { ...selected, payload: { ...selected.payload, fields: { customfield_10010: 42 } } },
  ]) {
    const malformed = jira.write("POST", previewPath(), { connectionId: alternate.id }, { apiVersion, preview: wrong });
    await previewAgain(page).click(); await settled(page, jira, malformed);
    await expect(review(page).getByRole("alert")).toContainText(/invalid|not.*confirm|changed|scope/i);
    await noEnabled(queue(page));
  }
  expect(jira.jiraCalls.filter((call) => call.path === historyPath() && call.method === "POST")).toHaveLength(1);
  await noIO(page, jira, () => closeReview(page).click()); await unchanged(page);
  jira.mark("JU3 completed: true local-preview HTTP, one explicit queue with bound digest, ambiguous-target selection and malformed preview refusal.");
});

async function readHistory(page: Page, api: JiraUIAPI, values: JiraDelivery[], total = values.length, cursor: string | null = null) {
  const read = api.list(historyPath(), nativePage(values, total, cursor), { entry: true });
  await historyEntry(page).click(); await settled(page, api, read); await expect(history(page)).toBeVisible();
  await expect(historyTable(page).locator("tbody tr")).toHaveCount(values.length);
}
async function openDelivery(page: Page, api: JiraUIAPI, value: JiraDelivery) {
  const read = api.read(deliveryPath(value.id), deliveryDetail(value), { entry: true });
  await historyRow(page, value.id).getByRole("button", { name: "Open Jira work item", exact: true }).click();
  await settled(page, api, read);
  await expect(delivery(page)).toContainText(value.id);
}
async function nativeState(page: Page, value: JiraDelivery) {
  await expect(deliveryStatus(page)).toContainText(new RegExp(value.state.replace("-", "[- ]"), "i"));
  for (const text of [value.payload.title, value.payload.body, value.jira.project, value.jira.siteOrigin]) {
    await expect(delivery(page)).toContainText(text);
  }
  if (value.createAttemptedAt !== null) await expect(delivery(page).locator(`time[datetime="${value.createAttemptedAt}"]`)).toBeVisible();
  if (value.failure) {
    for (const text of [value.failure.code, ...value.failure.missingFields ?? []]) await expect(delivery(page)).toContainText(text);
    if (value.failure.nativeCode) await expect(delivery(page)).toContainText(value.failure.nativeCode);
    await expect(delivery(page)).toContainText(new RegExp(`HTTP(?: status)?\\s*:?\\s*${value.failure.httpStatus}\\b`, "i"));
    await expect(delivery(page)).toContainText(new RegExp(`stage\\s*:?\\s*${value.failure.stage}`, "i"));
    await expect(delivery(page)).toContainText(/no automatic retr(?:y|ies)|retryable\s*:\s*false/i);
  }
}

test("JU4 explicit native history, terminal meaning, canonical receipt URLs and detail denials", async ({ page, jira }) => {
  jira.roles.set(alpha.id, "viewer"); jira.serverRoles.set(alpha.id, "viewer");
  await bootWork(page, jira); await openFinding(page, jira, "history");
  await expect(createEntry(page)).toHaveCount(0);
  await readHistory(page, jira, historyRows.slice(0, 100), 101, historyRows[99].id);
  await expect(history(page)).toContainText(/101/);
  const tail = jira.list(historyPath(), nativePage(historyRows.slice(100), 101), { cursor: historyRows[99].id });
  await history(page).getByRole("button", { name: "Next Jira work items", exact: true }).click(); await settled(page, jira, tail);
  await expect(historyTable(page).locator("tbody tr")).toHaveCount(1); await expect(history(page)).toContainText(/101/);
  const reload = jira.list(historyPath(), nativePage(historyRows.slice(0, 100), 101, historyRows[99].id));
  await history(page).getByRole("button", { name: "Refresh Jira history", exact: true }).click(); await settled(page, jira, reload);
  for (const value of [historyRows[0], historyRows[1], confirmed, missingFields, limited, uncertain, blocked, nativeAuth, createFields]) {
    await openDelivery(page, jira, value); await nativeState(page, value);
    if (value === confirmed) {
      const link = delivery(page).getByRole("link", { name: /^(?:View|Open) Jira (?:issue|work item)$/ });
      await expect(link).toHaveAttribute("href", confirmed.receipt!.remoteUrl);
      await expect(link).toHaveAttribute("rel", /noopener/); await expect(link).toHaveAttribute("rel", /noreferrer/);
      await expect(delivery(page)).toContainText("SYN-42");
      await expect(delivery(page)).toContainText(/does not (?:resolve|verify)|not.*(?:account|permission|finding).*verif/i);
    }
    if (value === missingFields) {
      await expect(delivery(page)).toContainText(/no (?:native |Jira )?(?:create |issue )?POST|not (?:attempted|sent|created)/i);
      await expect(delivery(page).locator(`time[datetime="${attempted}"]`)).toHaveCount(0);
    }
    if (value === limited) await expect(delivery(page)).toContainText(/Retry.After\s*:?\s*7\s*(?:s\b|seconds)/i);
    if (value === uncertain) {
      await expect(deliveryStatus(page)).toContainText(/may.*(?:sent|created)|possibly.*(?:sent|created)/i);
      await expect(deliveryStatus(page)).toContainText(/do not (?:retry|resend)|must not (?:retry|resend)/i);
      await expect(delivery(page).getByText(/safe to retry|safe to resend|creation failed/i)).toHaveCount(0);
    }
    await noEnabled(delivery(page).getByRole("button", { name: /queue|send|resend|retry create|create issue/i }));
  }
  await quiet(page, jira);
  await openDelivery(page, jira, confirmed);
  for (const receipt of [
    { remoteId: "ALT-42", remoteUrl: `${target.siteOrigin}/browse/ALT-42` },
    { remoteId: "SYN-42", remoteUrl: "https://untrusted.synthetic.invalid/browse/SYN-42" },
    { remoteId: "SYN-42", remoteUrl: `${target.siteOrigin}/browse/SYN-43` },
    { remoteId: "SYN-42", remoteUrl: "javascript:alert('not-a-link')" },
  ]) {
    const bad = jira.read(deliveryPath(confirmed.id), deliveryDetail({ ...confirmed, receipt }));
    await (await refreshDelivery(page).isVisible() ? refreshDelivery(page) : retryDelivery(page)).click();
    await settled(page, jira, bad);
    await expect(delivery(page).getByRole("alert")).toContainText(/invalid|not.*confirm|untrusted/i);
    await expect(delivery(page).getByRole("link", { name: /Jira (?:issue|work item)/ })).toHaveCount(0);
    await expect(deliveryStatus(page)).toHaveCount(0);
  }
  for (const status of [404, 503] as const) {
    const denied = jira.read(deliveryPath(confirmed.id), null, { status });
    await retryDelivery(page).click(); await settled(page, jira, denied);
    await expect(delivery(page).getByRole("link", { name: /Jira (?:issue|work item)/ })).toHaveCount(0);
    await expect(delivery(page)).not.toContainText("SYN-42"); await expect(delivery(page)).not.toContainText(first.assetName);
    await expect(deliveryStatus(page)).toHaveCount(0);
    await expect(historyTable(page).locator("tbody tr")).toHaveCount(100);
  }
  expect(jira.jiraCalls.filter((call) => call.method !== "GET")).toHaveLength(0);
  await unchanged(page);
  jira.mark("JU4 completed: manual native cursor/history only, metadata no-POST vs create uncertainty, 429 data, approved-site links, detail denial through 503.");
});

async function aborted(page: Page, api: JiraUIAPI, control: JiraControl) {
  await expect.poll(() => control.calls.length > 0 && control.calls.every((call) => /abort/i.test(call.failure ?? "")),
    "Old requests must be aborted before their held late response is released.").toBe(true);
  api.finishAborted(control); await frames(page);
}

test("JU5 lost ACK keeps the original key, explicit replay only, stale selection and revision revoke consent", async ({ page, jira }) => {
  await bootWork(page, jira); await openFinding(page, jira); await enterReview(page, jira);
  let original = "";
  const lost = jira.write("POST", historyPath(), queueBody(preview, (key) => { original = key; }), deliveryDetail(queued()),
    { status: "lost-ack" });
  await queue(page).click(); await settled(page, jira, lost);
  await expect(review(page).getByRole("alert")).toContainText(/may.*queued|acknowledgement.*not.*confirm|outcome.*unknown/i);
  await expect(review(page)).toContainText(/same.*(?:key|intent)|original.*(?:key|intent)/i);
  await quiet(page, jira);
  await noIO(page, jira, () => closeReview(page).click());
  await enterReview(page, jira);
  await expect(sameIntent(page)).toBeEnabled(); await expect(queue(page)).toHaveCount(0);
  expect(jira.jiraCalls.filter((call) => call.method === "POST" && call.path === historyPath())).toHaveLength(1);
  for (const wrong of [
    { ...queued(), workspaceId: beta.id },
    { ...queued(), connectionRevision: connection.revision + 1, jira: alternate.jira },
  ]) {
    const replay = jira.write("POST", historyPath(), queueBody(preview, () => {}, original), { apiVersion, delivery: wrong }, { status: 200 });
    await sameIntent(page).click(); await settled(page, jira, replay);
    await expect(review(page).getByRole("alert")).toContainText(/invalid|not.*confirm|unknown/i);
    await expect(deliveryStatus(page)).toHaveCount(0); await quiet(page, jira);
  }
  const replay = jira.write("POST", historyPath(), queueBody(preview, () => {}, original), deliveryDetail(queued()), { status: 200 });
  await sameIntent(page).click(); await settled(page, jira, replay);
  await expect(deliveryStatus(page)).toContainText(/queued/i);
  expect(jira.jiraCalls.filter((call) => call.path === historyPath() && call.method === "POST")
    .map((call) => call.body.idempotencyKey)).toEqual([original, original, original, original]);
  await noIO(page, jira, () => closeReview(page).click());
  const choices = jira.list(connectionsPath, nativePage([connection, alternate]), { entry: true });
  await createEntry(page).click(); await settled(page, jira, choices);
  const old = jira.write("POST", previewPath(), { connectionId: connection.id }, previewDetail(preview), { held: true });
  await choose(page).selectOption(connection.id); await arrived(old); await noEnabled(queue(page));
  const selected = localPreview(alternate, first, "b");
  const replacement = jira.write("POST", previewPath(), { connectionId: alternate.id }, previewDetail(selected));
  await choose(page).selectOption(alternate.id); await settled(page, jira, replacement);
  await aborted(page, jira, old); await showPreview(page, jira, selected);
  await expect(review(page)).not.toContainText(target.siteOrigin);
  const disabled = { ...alternate, enabled: false, revision: 5, jira: { ...alternate.jira, project: "NEXT" } };
  const changed = jira.list(connectionsPath, nativePage([disabled]));
  await review(page).getByRole("button", { name: "Refresh Jira connections", exact: true }).click();
  await settled(page, jira, changed); await noEnabled(queue(page).or(sameIntent(page)));
  await expect(review(page)).toContainText(/disabled|changed|new preview|review again/i); await quiet(page, jira);
  const enabled = { ...disabled, enabled: true, revision: 6 };
  const authorized = jira.list(connectionsPath, nativePage([enabled]));
  await review(page).getByRole("button", { name: "Refresh Jira connections", exact: true }).click();
  await settled(page, jira, authorized); await noEnabled(queue(page).or(sameIntent(page)));
  const current = localPreview(enabled, first, "c");
  const fresh = jira.write("POST", previewPath(), { connectionId: enabled.id }, previewDetail(current));
  await previewAgain(page).click(); await settled(page, jira, fresh); await showPreview(page, jira, current);
  const conflict = jira.write("POST", historyPath(), queueBody(current, (key) => {
    expect(key, "A separately confirmed, acknowledged prior intent is not the new target's key.").not.toBe(original);
  }), null, { status: 409 });
  await queue(page).click(); await settled(page, jira, conflict);
  await expect(review(page).getByRole("alert")).toContainText(/changed|conflict|new preview|review again/i);
  await noEnabled(queue(page).or(sameIntent(page))); await quiet(page, jira);
  const posts = jira.jiraCalls.filter((call) => call.path === historyPath() && call.method === "POST").length;
  await noIO(page, jira, () => closeReview(page).click());
  await details(page).getByRole("button", { name: "Close finding details", exact: true }).click();
  const canonical = { ...first, severity: "critical" as const };
  const detail = jira.read(findingPath(), { apiVersion, dataOrigin: "synthetic", finding: canonical }, { entry: true });
  await findingTrigger(page).click(); await settled(page, jira, detail);
  await expect(review(page)).toHaveCount(0); await quiet(page, jira);
  expect(jira.jiraCalls.filter((call) => call.path === historyPath() && call.method === "POST")).toHaveLength(posts);
  jira.mark("JU5 completed: lost ACK never replaced its key, wrong ACK was not success, old selection/target/revision consent cannot post, 409 requires review.");
});

async function noPrivateDOM(page: Page, values: string[]) {
  const state = await page.evaluate(() => document.body.textContent + JSON.stringify(
    [...document.querySelectorAll("input,textarea,select")].map((field) => (field as HTMLInputElement).value)));
  for (const value of values) expect(state).not.toContain(value);
}
async function signIn(page: Page) {
  const form = page.getByRole("form", { name: "Sign in", exact: true });
  await expect(form).toBeVisible();
  await form.getByLabel("Email", { exact: true }).fill(user.email);
  await form.getByLabel("Password", { exact: true }).fill(password);
  await form.getByRole("button", { name: "Sign in", exact: true }).click();
}

test("JU6 workspace, logout and actual-response 401 clear secrets and abort late metadata or ACKs", async ({ page, jira }) => {
  await integrations(page, jira); await openConnections(page, jira);
  await newDraft(page, jira);
  await noIO(page, jira, () => editor(page).getByRole("button", { name: "Cancel", exact: true }).click());
  await closedEditor(page, jira); await newDraft(page, jira);
  const heldCreate = jira.write("POST", connectionsPath, createInput, connectionDetail(created), { status: 201, held: true });
  await save(page).click(); await arrived(heldCreate);
  const betaNav = jira.navigationEntry(beta.id);
  await workspace(page).selectOption(beta.id); await finishNavigation(page, jira, betaNav);
  await expect(workspace(page)).toHaveValue(beta.id); await expect(toggle(page)).toHaveAttribute("aria-expanded", "false");
  await aborted(page, jira, heldCreate); await closedEditor(page, jira);
  await noPrivateDOM(page, [created.name, connection.name, draftToken, first.title]);
  const betaConnection = { ...connection, workspaceId: beta.id, name: "Synthetic Jira Beta private metadata" };
  const late = jira.list(connectionsPath, nativePage([betaConnection]), { workspace: beta.id, entry: true, held: true });
  await toggle(page).click(); await arrived(late);
  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  await expect(page.getByRole("form", { name: "Sign in", exact: true })).toBeVisible();
  await aborted(page, jira, late); await jira.assertPrivate(page, true);
  await noPrivateDOM(page, [connection.name, created.name, betaConnection.name, user.name, alpha.name, beta.name]);
  await expect(workspace(page)).toHaveCount(0); await expect(panel(page)).toHaveCount(0);
  const nav = jira.navigationEntry();
  await signIn(page); await finishNavigation(page, jira, nav);
  await expect(toggle(page)).toHaveAttribute("aria-expanded", "false");
  const expired = jira.list(connectionsPath, null, { status: 401, entry: true, held: true });
  await toggle(page).click(); await arrived(expired);
  await settled(page, jira, expired);
  expect(expired.calls.some((call) => call.responseStatus === 401 && call.responseBeforeFailure)).toBe(true);
  await expect(page.getByRole("form", { name: "Sign in", exact: true })).toBeVisible();
  await expect(panel(page)).toHaveCount(0); await expect(editor(page)).toHaveCount(0);
  await noPrivateDOM(page, [connection.name, created.name, draftToken, betaConnection.name, user.name, alpha.name, beta.name]);
  await jira.assertPrivate(page, true); await quiet(page, jira);
  jira.mark("JU6 completed: closed token drafts, workspace/logout aborts and real 401 response before auth cleanup; no aborted 401 success fallback.");
});

test("JU7 390px keyboard review, distinct metadata/detail authority and read-only history preserve focus", async ({ page, jira }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await bootWork(page, jira); await openFinding(page, jira); await enterReview(page, jira, connection, preview, true);
  await expect(page.getByRole("dialog")).toHaveCount(1);
  await expect.poll(() => review(page).evaluate((element) => element.contains(document.activeElement))).toBe(true);
  for (let index = 0; index < 6; index++) {
    await page.keyboard.press("Tab");
    expect(await review(page).evaluate((element) => element.contains(document.activeElement))).toBe(true);
  }
  await page.keyboard.press("Shift+Tab");
  expect(await review(page).evaluate((element) => element.contains(document.activeElement))).toBe(true);
  expect(await page.evaluate(() => matchMedia("(prefers-reduced-motion: reduce)").matches)).toBe(true);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
  await noIO(page, jira, () => page.keyboard.press("Escape"));
  await expect(review(page)).toHaveCount(0); await expect(details(page)).toBeVisible(); await expect(createEntry(page)).toBeFocused();
  await unchanged(page);
  await enterReview(page, jira);
  for (const status of [403, 503] as const) {
    const denied = jira.list(connectionsPath, null, { status });
    const action = review(page).getByRole("button", { name: status === 403 ? "Refresh Jira connections" : "Retry Jira connections", exact: true });
    await action.click(); await settled(page, jira, denied);
    await noEnabled(queue(page).or(sameIntent(page))); await expect(review(page).getByRole("alert")).toBeVisible();
    await expect(review(page)).not.toContainText(target.siteOrigin);
  }
  await noIO(page, jira, () => closeReview(page).click()); await unchanged(page);
  await details(page).getByRole("button", { name: "Close finding details", exact: true }).click();
  await expect(findingTrigger(page)).toBeFocused(); await expect(selection(page)).toBeChecked();
  const forbidden = jira.findingEntry(first.id, { status: 403 });
  await findingTrigger(page).click(); await settled(page, jira, forbidden);
  await expect(details(page).getByRole("alert")).toBeVisible();
  await expect(createEntry(page)).toHaveCount(0); await expect(historyEntry(page)).toHaveCount(0);
  await expect(details(page)).not.toContainText(first.evidence.text);
  const unavailable = jira.read(findingPath(), null, { status: 503 });
  await details(page).getByRole("button", { name: /^Retry(?: finding(?: details| history)?)?$/ }).click();
  await settled(page, jira, unavailable);
  await expect(details(page)).not.toContainText(first.evidence.text); await expect(createEntry(page)).toHaveCount(0);
  await expect(workRow(page)).toContainText(currentOwner.name);
  await details(page).getByRole("button", { name: "Close finding details", exact: true }).click();
  jira.roles.set(alpha.id, "viewer"); jira.serverRoles.set(alpha.id, "viewer");
  await bootWork(page, jira, true); await openFinding(page, jira, "history");
  await expect(createEntry(page)).toHaveCount(0); await readHistory(page, jira, [queued()]);
  await openDelivery(page, jira, queued()); await expect(deliveryStatus(page)).toContainText(/queued/i);
  await noEnabled(details(page).getByRole("button", { name: /queue Jira|confirm same Jira|create Jira/i }));
  await details(page).getByRole("button", { name: "Close finding details", exact: true }).click();
  await expect(selection(page)).toBeChecked(); await expect(filter(page)).toHaveValue(first.assetName);
  await expect(findingTrigger(page)).toBeFocused();
  expect(jira.jiraCalls.filter((call) => call.path === historyPath() && call.method === "POST")).toHaveLength(0);
  await quiet(page, jira);
  jira.mark("JU7 completed: narrow keyboard/reduced-motion in-detail flow, metadata denial is not Work authority, denied detail stays withheld, viewer history only.");
});
