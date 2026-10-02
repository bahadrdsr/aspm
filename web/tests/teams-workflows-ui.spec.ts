import type { Locator, Page } from "@playwright/test";
import { requireProductionUI } from "./network";
import { expect, test } from "./teams-workflows-ui-fixture";
import type { TeamsControl, TeamsUIAPI } from "./teams-workflows-ui-fixture";
import type { WorkControl } from "./saved-work-views-fixture";
import { currentOwner, password } from "./work-search-data";
import { primaryView, viewDetail, viewPage } from "./saved-work-views-data";
import { connection as jiraConnection } from "./jira-work-items-ui-data";
import {
  accepted, alpha, alternate, apiVersion, attempted, beta, blocked, connection, connectionDetail, connectionPages,
  connectionPath, connectionsPath, created, createInput, deliveryDetail, deliveryPath, failed, findingPath,
  findingWithHistory, first, historyPath, historyRows, invalidWorkflowURLs, limited, localPreview, nativePage,
  noteDraft, notePage, noteTail, preview, previewDetail, previewPath, queued, rotatedOrigin, rotatedURL, uncertain,
  user, workflowOrigin, workflowURL,
} from "./teams-workflows-ui-data";
import type { TeamsConnection, TeamsDelivery, TeamsPreview } from "./teams-workflows-ui-data";

test.use({ reducedMotion: "reduce", timezoneId: "UTC" });
test.beforeEach(async ({ teams }) => { requireProductionUI(); expect(teams.requests).toEqual([]); });
const button = (root: Page | Locator, name: string | RegExp) => root.getByRole("button", { name, exact: typeof name === "string" });
const toggle = (page: Page) => button(page, "Teams connections");
const panel = (page: Page) => page.getByRole("region", { name: "Teams connections", exact: true });
const rows = (page: Page) => panel(page).getByRole("table", { name: "Teams connections", exact: true }).locator("tbody tr");
const row = (page: Page, name = connection.name) => rows(page).filter({ hasText: name });
const editor = (page: Page) => page.getByRole("form", { name: /^(?:Add|Edit) Teams connection$/ });
const save = (page: Page) => button(editor(page), "Save Teams connection");
const secret = (page: Page) => editor(page).getByLabel("Workflow URL", { exact: true });
const ownership = (page: Page) => editor(page).getByRole("checkbox", { name: "I acknowledge Workflow ownership", exact: true });
const workTable = (page: Page) => page.getByRole("table", { name: "Findings", exact: true, includeHidden: true });
const workRow = (page: Page) => workTable(page).getByRole("row", { includeHidden: true }).filter({ hasText: first.title });
const trigger = (page: Page) => workRow(page).getByRole("button", { name: first.title, exact: true, includeHidden: true });
const selection = (page: Page) => workRow(page).getByRole("checkbox", { name: `Select ${first.title}`, exact: true, includeHidden: true });
const filter = (page: Page) => page.getByRole("textbox", { name: "Filter findings", exact: true, includeHidden: true });
const finding = (page: Page) => page.getByRole("dialog", { name: first.title, exact: true });
const notify = (page: Page) => button(finding(page), "Notify Teams");
const review = (page: Page) => finding(page).getByRole("region", { name: "Teams notification review", exact: true });
const queue = (page: Page) => button(review(page), "Queue Teams notification");
const replay = (page: Page) => button(review(page), "Confirm same Teams notification");
const previewAgain = (page: Page) => button(review(page), /^(?:Refresh|Retry) Teams preview$/);
const choose = (page: Page) => review(page).getByRole("combobox", { name: "Teams connection", exact: true });
const history = (page: Page) => finding(page).getByRole("region", { name: "Teams notification history", exact: true });
const historyTable = (page: Page) => history(page).getByRole("table", { name: "Teams notifications", exact: true });
const detail = (page: Page) => finding(page).getByRole("region", { name: "Teams notification details", exact: true });
const outcome = (page: Page) => detail(page).getByRole("status", { name: "Teams notification status", exact: true });
const workspace = (page: Page) => page.getByRole("combobox", { name: "Workspace", exact: true });

async function frames(page: Page) {
  await page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))));
}
async function arrived(control: TeamsControl) { await expect.poll(() => control.calls.length).toBeGreaterThan(0); }
async function settled(page: Page, api: TeamsUIAPI, control: TeamsControl) {
  await arrived(control); control.release(); await frames(page);
  await expect.poll(() => control.calls.every((call) => call.finished || call.failure !== null ||
    control.status === 401 && call.responseStatus === 401)).toBe(true);
  api.finish(control);
}
async function inherited(control: WorkControl) {
  await expect.poll(() => control.call !== null).toBe(true); control.release(); await control.delivered;
  await expect.poll(() => control.call !== null && (control.call.finished || control.call.failure !== null)).toBe(true);
}
async function noIO(page: Page, api: TeamsUIAPI, action: () => Promise<unknown>) {
  const before = api.requests.length; await action(); await frames(page);
  expect(api.requests, "Local edits/close must not generate HTTP.").toHaveLength(before);
}
async function quiet(page: Page, api: TeamsUIAPI, milliseconds = 120) {
  const before = api.requests.length; await frames(page); await page.waitForTimeout(milliseconds);
  expect(api.requests, "No background paging, probes, polling or resend in this observation window.").toHaveLength(before);
}
async function disabled(controls: Locator) { for (const control of await controls.all()) await expect(control).toBeDisabled(); }
const queuePosts = (api: TeamsUIAPI) => api.teamsCalls.filter((call) => call.method === "POST" && call.path === historyPath());
async function finishNavigation(page: Page, api: TeamsUIAPI, controls: TeamsControl[]) {
  await expect(page.getByRole("heading", { name: "Integrations", exact: true })).toBeVisible();
  await expect(page.getByRole("region", { name: "Connections", exact: true })).toBeVisible();
  await expect(page.getByRole("region", { name: "Sources", exact: true })).toBeVisible();
  for (const control of controls) await settled(page, api, control);
}
async function integrations(page: Page, api: TeamsUIAPI) {
  const reads = api.integrationsEntry();
  await page.goto("/#/integrations"); await finishNavigation(page, api, reads);
  await expect(page.getByRole("navigation", { name: "Primary", exact: true }).getByRole("link")).toHaveCount(5);
  await expect(page.getByRole("list", { name: "Native integrations", exact: true }).getByRole("listitem")).toHaveCount(8);
  expect(api.teamsCalls).toHaveLength(0); expect(api.jiraCalls).toHaveLength(0);
  expect(api.requests.filter((call) => call.path === connectionsPath).every((call) =>
    JSON.stringify(call.query) === JSON.stringify({ limit: "100" }))).toBe(true);
  await expect(button(page, "AI settings")).toHaveAttribute("aria-expanded", "false");
  api.mark("Reached published Integrations, five destinations/eight families, original Slack/Sources and zero Teams/Jira metadata.");
  await expect(toggle(page), "Published UI must expose a collapsed Teams connections entry.").toBeVisible();
  await expect(toggle(page)).toHaveAttribute("aria-expanded", "false"); await expect(panel(page)).toHaveCount(0);
}
async function openConnections(page: Page, api: TeamsUIAPI, values: TeamsConnection[] = [connection], scope = alpha.id) {
  const read = api.listTeams(connectionsPath, nativePage(values), { entry: true, workspace: scope });
  await toggle(page).click(); await settled(page, api, read);
  await expect(rows(page)).toHaveCount(values.length);
  await expect(panel(page)).toContainText(/not[- ]verified|not (?:live )?verified/i);
}
async function addDraft(page: Page, api: TeamsUIAPI, acknowledge = true) {
  await noIO(page, api, () => button(panel(page), "Add Teams connection").click());
  await expect(secret(page)).toHaveAttribute("type", "password"); await expect(secret(page)).toHaveValue("");
  await expect(ownership(page)).not.toBeChecked();
  await expect(editor(page)).toContainText(/standard/i); await expect(editor(page)).toContainText(/Anyone/);
  await expect(editor(page)).toContainText(/owner.*co.owner|co.owner.*owner/i);
  await expect(editor(page)).toContainText(/not verified/i);
  await expect(editor(page).getByRole("combobox")).toHaveCount(0);
  await expect(editor(page).getByRole("textbox", { name: /token|channel|tenant|team id|user id|Jira|override/i })).toHaveCount(0);
  await expect(button(editor(page), /show.*URL|copy|export|test.*workflow|probe|delete/i)).toHaveCount(0);
  await noIO(page, api, async () => {
    await editor(page).getByLabel("Name", { exact: true }).fill(createInput.name);
    await secret(page).fill(workflowURL);
    await editor(page).getByRole("checkbox", { name: "Enabled", exact: true }).uncheck();
  });
  await expect(save(page)).toBeDisabled();
  if (acknowledge) await noIO(page, api, () => ownership(page).check());
}
async function edit(page: Page, api: TeamsUIAPI, value: TeamsConnection) {
  const read = api.readTeams(connectionPath(value.id), connectionDetail(value), { entry: true });
  await button(row(page, value.name), "Edit Teams connection").click(); await settled(page, api, read);
  await expect(secret(page)).toHaveValue(""); await expect(ownership(page)).toBeChecked();
}
async function closedEditor(page: Page, api: TeamsUIAPI) {
  await expect(editor(page)).toHaveCount(0); await api.assertPrivate(page, true);
}
async function bootWork(page: Page, api: TeamsUIAPI) {
  const read = api.entry(); await page.goto("/#/work");
  await expect(workTable(page).locator("tbody tr")).toHaveCount(50); await frames(page);
  await expect.poll(() => read.calls.length > 0 && read.calls.every((call) => call.finished || call.failure !== null)).toBe(true);
  api.finishEntry(read); expect(api.teamsCalls).toHaveLength(0);
}
async function openFinding(page: Page, api: TeamsUIAPI, value = first, readOnly = false) {
  await noIO(page, api, async () => { await filter(page).fill(first.assetName); await selection(page).check(); });
  const before = api.teamsCalls.length;
  const read = api.read(findingPath(), { apiVersion, dataOrigin: "synthetic", finding: value }, { entry: true });
  await trigger(page).click(); await settled(page, api, read); await expect(finding(page)).toBeVisible();
  expect(api.teamsCalls).toHaveLength(before);
  await expect(button(finding(page), "Delivery history")).toBeVisible();
  api.mark("Reached real Work/finding with selection/filter and Slack history intact, zero implicit Teams requests.");
  await expect(readOnly ? button(finding(page), "Teams notification history") : notify(page),
    "Published finding UI must expose its explicit Teams entry.").toBeVisible();
}
async function showPreview(page: Page, api: TeamsUIAPI, value = preview) {
  await expect(review(page)).toBeVisible(); await expect(page.getByRole("dialog")).toHaveCount(1);
  for (const text of [value.payload.title, value.payload.body, value.destination.name, value.destination.workflowOrigin, "standard"]) {
    await expect(review(page)).toContainText(text);
  }
  await expect(review(page)).toContainText(/native validation(?:\s*:)? not run/i);
  await expect(review(page)).toContainText(/owner.*co.owner|co.owner.*owner/i);
  await expect(review(page)).toContainText(/not (?:yet )?verified/i);
  const link = review(page).getByRole("link", { name: /^(?:View|Open) finding$/ });
  await expect(link).toHaveAttribute("href", value.payload.deepLink);
  await expect(link).toHaveAttribute("rel", /noopener/); await expect(link).toHaveAttribute("rel", /noreferrer/);
  await expect(review(page).getByRole("link")).toHaveCount(1);
  for (const text of [first.evidence.text, first.description, first.remediation, noteDraft, noteTail.text]) {
    await expect(review(page)).not.toContainText(text);
  }
  await expect(queue(page).or(replay(page))).toHaveCount(1); await expect(queue(page).or(replay(page))).toBeEnabled();
  await api.assertPrivate(page);
}
async function enterReview(page: Page, api: TeamsUIAPI, keyboard = false) {
  const metadata = api.listTeams(connectionsPath, nativePage([connection]), { entry: true });
  const local = api.writeTeams("POST", previewPath(), { connectionId: connection.id }, previewDetail(preview), { held: true });
  const before = queuePosts(api).length;
  if (keyboard) { await notify(page).focus(); await notify(page).press("Space"); } else await notify(page).click();
  await settled(page, api, metadata); await arrived(local); await disabled(queue(page).or(replay(page)));
  expect(queuePosts(api)).toHaveLength(before);
  await settled(page, api, local); await showPreview(page, api);
}
function queueBody(value: TeamsPreview, capture: (key: string) => void, original?: string) {
  return (body: Record<string, unknown>) => {
    expect(Object.keys(body).sort()).toEqual(["confirm", "connectionId", "idempotencyKey", "previewDigest"]);
    expect(body).toMatchObject({ connectionId: value.connectionId, previewDigest: value.bindingDigest, confirm: true });
    expect(typeof body.idempotencyKey).toBe("string");
    const key = body.idempotencyKey as string;
    expect(key).toMatch(/^\S+$/); expect(key).not.toMatch(/[\u0000-\u001f\u007f]/);
    expect(Buffer.byteLength(key, "utf8")).toBeLessThanOrEqual(256);
    if (original !== undefined) expect(key, "Replay must retain the original uncertain intent key.").toBe(original);
    capture(key);
  };
}
async function unchanged(page: Page) {
  for (const text of [first.evidence.text, first.description, first.remediation]) await expect(finding(page)).toContainText(text);
  await expect(finding(page)).toContainText("No independently verified resolution is recorded.");
  await expect(workRow(page)).toContainText(currentOwner.name);
  await expect(workRow(page).getByRole("cell", { includeHidden: true }).filter({ hasText: /^Open$/ })).toHaveCount(1);
  await expect(selection(page)).toBeChecked(); await expect(filter(page)).toHaveValue(first.assetName);
}

test("TU1 lazy profile entry, native cursor retry and strict secret-safe creation", async ({ page, teams }) => {
  await integrations(page, teams);
  const jira = teams.list(connectionsPath, nativePage([jiraConnection]), { entry: true });
  await button(page, "Jira connections").click(); await settled(page, teams, jira);
  await expect(page.getByRole("region", { name: "Jira connections", exact: true })).toContainText(jiraConnection.name);
  await noIO(page, teams, () => button(page, "Jira connections").click());
  const mixed = teams.listTeams(connectionsPath, { apiVersion, items: [jiraConnection, connection], total: 2, nextCursor: null }, { entry: true });
  await toggle(page).click(); await settled(page, teams, mixed);
  await expect(panel(page).getByRole("alert")).toBeVisible(); await expect(rows(page)).toHaveCount(0);
  const initial = teams.listTeams(connectionsPath, nativePage(connectionPages.slice(0, 100), 101, connectionPages[99].id));
  await button(panel(page), "Retry Teams connections").click(); await settled(page, teams, initial);
  await expect(rows(page)).toHaveCount(100); await expect(panel(page)).toContainText(/101/); await quiet(page, teams);
  for (const [index, status] of ([503, 403, 503, 200] as const).entries()) {
    const tail = teams.listTeams(connectionsPath, status === 200 ? nativePage(connectionPages.slice(100), 101) : null,
      { status, cursor: connectionPages[99].id });
    await button(panel(page), index === 0 ? "Next Teams connections" : "Retry Teams connections").click();
    await settled(page, teams, tail);
    if (status === 403 || status === 503 && teams.teamsCalls.some((call) => call.responseStatus === 403)) {
      await expect(rows(page)).toHaveCount(0); await disabled(button(panel(page), "Add Teams connection"));
    }
  }
  await expect(rows(page)).toHaveCount(1); await expect(panel(page)).toContainText(/101/);
  await addDraft(page, teams, false); await expect(editor(page)).not.toContainText(workflowOrigin);
  await noIO(page, teams, () => ownership(page).check());
  const name = editor(page).getByLabel("Name", { exact: true }), tooLong = "\u00e9".repeat(129);
  await noIO(page, teams, async () => { await name.fill(tooLong); await save(page).click(); });
  await expect(name).toHaveValue(tooLong); await expect(editor(page).getByRole("alert")).toBeVisible(); await name.fill(createInput.name);
  for (const invalid of invalidWorkflowURLs) {
    teams.watchSecret(invalid);
    await noIO(page, teams, async () => { await secret(page).fill(invalid); await save(page).click(); });
    await expect(secret(page), "Invalid credentials cannot be truncated, coerced or echoed.").toHaveValue(invalid);
    await expect(editor(page).getByRole("alert")).toBeVisible();
  }
  await secret(page).fill(workflowURL); await teams.assertPrivate(page);
  const create = teams.writeTeams("POST", connectionsPath, createInput, connectionDetail(created), { status: 201 });
  await save(page).click(); await settled(page, teams, create); await closedEditor(page, teams);
  expect(create.calls[0].body.workflowUrl).toBe(workflowURL);
  await expect(row(page, created.name)).toContainText(workflowOrigin);
  await expect(row(page, created.name)).toContainText(/not[- ]verified/i);
  await expect(panel(page).getByRole("link")).toHaveCount(0);
  const family = page.getByRole("list", { name: "Native integrations", exact: true }).getByRole("listitem")
    .filter({ has: page.getByRole("heading", { name: "Microsoft Teams", exact: true }) });
  await expect(family).toContainText("Not verified");
  await expect(family).not.toContainText("API reports verification passed"); await quiet(page, teams);
  teams.mark("TU1 completed: strict Teams/Jira selectors, cursor replay through denial, unmodified raw URL and unverified canonical metadata.");
});

test("TU2 sparse edits, unchanged declaration, strict acknowledgements and current-role authority", async ({ page, teams }) => {
  await integrations(page, teams); await openConnections(page, teams); await edit(page, teams, connection);
  await expect(save(page)).toBeDisabled();
  await noIO(page, teams, async () => { await ownership(page).uncheck(); await ownership(page).check(); });
  await expect(save(page)).toBeDisabled();
  const named = { ...connection, name: "Synthetic Teams renamed", revision: 5 };
  await editor(page).getByLabel("Name", { exact: true }).fill(named.name);
  const malformed = teams.writeTeams("PATCH", connectionPath(connection.id), { name: named.name },
    { apiVersion, connection: { ...named, workflowUrl: workflowURL } });
  await save(page).click(); await settled(page, teams, malformed);
  await expect(editor(page).getByRole("alert")).toContainText(/invalid|not.*confirm|rejected/i);
  await expect(editor(page).getByLabel("Name", { exact: true })).toHaveValue(named.name);
  await expect(secret(page)).toHaveValue(""); await teams.assertPrivate(page);
  const rename = teams.writeTeams("PATCH", connectionPath(connection.id), { name: named.name }, connectionDetail(named));
  await save(page).click(); await settled(page, teams, rename); await closedEditor(page, teams);
  await edit(page, teams, named); await expect(save(page)).toBeDisabled();
  await secret(page).fill(rotatedURL); await editor(page).getByRole("checkbox", { name: "Enabled", exact: true }).uncheck();
  const rotated = { ...named, revision: 6, enabled: false, teams: { ...named.teams, workflowOrigin: rotatedOrigin } };
  const input = { enabled: false, workflowUrl: rotatedURL };
  const wrongStatus = teams.writeTeams("PATCH", connectionPath(connection.id), input, connectionDetail(rotated), { status: 201 });
  await save(page).click(); await settled(page, teams, wrongStatus);
  await expect(editor(page).getByRole("alert")).toBeVisible(); await expect(secret(page)).toHaveValue(rotatedURL);
  await teams.assertPrivate(page);
  const rotation = teams.writeTeams("PATCH", connectionPath(connection.id), input, connectionDetail(rotated));
  await save(page).click(); await settled(page, teams, rotation); await closedEditor(page, teams);
  await expect(row(page, rotated.name)).toContainText(/disabled/i); await expect(row(page, rotated.name)).toContainText(rotatedOrigin);
  await edit(page, teams, rotated);
  await editor(page).getByLabel("Name", { exact: true }).fill("Denied cached admin edit");
  teams.serverRoles.set(alpha.id, "analyst");
  const denied = teams.writeTeams("PATCH", connectionPath(connection.id), { name: "Denied cached admin edit" }, null,
    { status: 403, errorMessage: `Native echo ${workflowURL} SYNTHETIC-SIG/Upper+Value= SYNTHETIC-RAW/TAIL` });
  await save(page).click(); await settled(page, teams, denied);
  await expect(editor(page).getByRole("alert")).toContainText(/permission|denied|not permitted/i);
  await disabled(save(page)); await teams.assertPrivate(page, true);
  for (const role of ["analyst", "viewer"] as const) {
    teams.roles.set(alpha.id, role); teams.serverRoles.set(alpha.id, role);
    const navigation = teams.integrationsEntry(); await page.reload(); await finishNavigation(page, teams, navigation);
    await openConnections(page, teams, [rotated]);
    await expect(button(panel(page), /^(?:Add|Edit) Teams connection$/)).toHaveCount(0);
    await expect(panel(page)).toContainText(/read.only|administrator|admin/i);
    for (const text of [rotatedOrigin, "standard"]) await expect(panel(page)).toContainText(text);
    await expect(panel(page)).toContainText(/credential.*configured|configured.*credential/i);
    await expect(panel(page)).toContainText(/revision\s*:?\s*6/i);
  }
  await quiet(page, teams);
  teams.mark("TU2 completed: sparse exact PATCHes, blank URL omission, one atomic rotation, no-op and actual new-document read-only roles.");
});

test("TU3 focused canonical review, explicit queue and personal Work/note focus without export", async ({ page, teams }) => {
  teams.roles.set(alpha.id, "analyst"); teams.serverRoles.set(alpha.id, "analyst");
  await page.setViewportSize({ width: 390, height: 844 }); await bootWork(page, teams);
  const personal = { ...primaryView, name: "Synthetic Teams personal focus", query: first.assetName, sort: "title" as const };
  const views = teams.viewsEntry(viewPage([personal]), alpha.id, false);
  await button(page, "Saved views").click();
  const personalRow = page.getByRole("list", { name: "Personal saved views", exact: true }).getByRole("listitem");
  await expect(personalRow).toContainText(personal.name); await frames(page);
  await expect.poll(() => views.calls.length > 0 && views.calls.every((call) => call.finished || call.failure !== null)).toBe(true);
  teams.finishViewsEntry(views);
  const canonical = teams.detailView(personal.id, viewDetail(personal), { held: false });
  const search = teams.search(personal.query, "", 200, false);
  await button(personalRow, "Apply").click(); await inherited(canonical); await inherited(search);
  await openFinding(page, teams, findingWithHistory); await enterReview(page, teams, true);
  expect(await page.evaluate(() => matchMedia("(prefers-reduced-motion: reduce)").matches)).toBe(true);
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
  for (const key of ["Tab", "Tab", "Tab", "Shift+Tab"]) {
    await page.keyboard.press(key);
    expect(await review(page).evaluate((element) => element.contains(document.activeElement))).toBe(true);
  }
  let key = "";
  const enqueue = teams.writeTeams("POST", historyPath(), queueBody(preview, (value) => { key = value; teams.remember(value); }),
    deliveryDetail(queued()), { status: 202, held: true });
  await queue(page).click(); await arrived(enqueue); await disabled(queue(page).or(replay(page)));
  expect(enqueue.calls).toHaveLength(1); await unchanged(page);
  await settled(page, teams, enqueue); expect(key).not.toBe("");
  await expect(outcome(page)).toContainText(/queued/i);
  await expect(outcome(page)).toContainText(/waiting.*worker|not (?:yet )?(?:sent|accepted)|awaiting.*worker/i);
  await quiet(page, teams);
  await review(page).focus(); await noIO(page, teams, () => page.keyboard.press("Escape"));
  await expect(review(page)).toHaveCount(0); await expect(notify(page)).toBeFocused();
  const notes = teams.read(findingPath(), { apiVersion, dataOrigin: "synthetic",
    finding: { ...first, notes: [noteTail], notesNextCursor: null } },
    { held: true, query: { notesCursor: notePage[99].id } });
  await button(finding(page), "Load more notes").click(); await arrived(notes);
  const draft = finding(page).getByRole("textbox", { name: "New note", exact: true });
  teams.remember(noteDraft);
  await draft.fill(noteDraft); await settled(page, teams, notes);
  await expect(finding(page)).toContainText(noteTail.text);
  await expect(draft, "Discarding Teams UI on a finding update must not steal an ordinary note draft's focus.").toBeFocused();
  await expect(draft).toHaveValue(noteDraft); await draft.press("End"); await draft.pressSequentially(" still editing");
  await expect(draft).toHaveValue(noteDraft + " still editing");
  const partial = [connection, alternate, ...connectionPages.slice(2, 100)];
  const choices = teams.listTeams(connectionsPath, nativePage(partial, 101, partial[99].id), { entry: true });
  const previewCount = teams.teamsCalls.filter((call) => call.path === previewPath()).length;
  await notify(page).click(); await settled(page, teams, choices);
  await expect(choose(page)).toHaveValue(""); await disabled(queue(page)); await quiet(page, teams);
  expect(teams.teamsCalls.filter((call) => call.path === previewPath())).toHaveLength(previewCount);
  const selected = localPreview(alternate, "b");
  const explicit = teams.writeTeams("POST", previewPath(), { connectionId: alternate.id }, previewDetail(selected));
  await choose(page).selectOption(alternate.id); await settled(page, teams, explicit); await showPreview(page, teams, selected);
  for (const wrong of [
    { ...selected, workspaceId: beta.id }, { ...selected, requestedBy: "f".repeat(32) },
    { ...selected, connectionRevision: selected.connectionRevision + 1 },
    { ...selected, payload: { ...selected.payload, body: first.evidence.text } },
  ]) {
    const rejected = teams.writeTeams("POST", previewPath(), { connectionId: alternate.id }, { apiVersion, preview: wrong });
    await previewAgain(page).click(); await settled(page, teams, rejected);
    await expect(review(page).getByRole("alert")).toContainText(/invalid|not.*confirm|changed|scope/i);
    await disabled(queue(page).or(replay(page)));
  }
  expect(queuePosts(teams)).toHaveLength(1);
  await noIO(page, teams, () => button(review(page), "Close Teams review").click()); await unchanged(page);
  await expect(draft).toHaveValue(noteDraft + " still editing");
  await button(finding(page), "Close finding details").click();
  await expect(trigger(page)).toBeFocused(); await expect(selection(page)).toBeChecked();
  await expect(personalRow).toContainText(personal.name);
  await expect(workTable(page).getByRole("columnheader").filter({ hasText: /^Finding$/ })).toHaveAttribute("aria-sort", "ascending");
  expect(teams.requests.filter((call) => call.method === "POST" && call.path.endsWith("/notes"))).toHaveLength(0);
  teams.mark("TU3 completed: two-activation single-target queue, canonical review, partial/ambiguous selection, 390px keyboard and held-read note focus.");
});

async function openDelivery(page: Page, api: TeamsUIAPI, value: TeamsDelivery) {
  const read = api.readTeams(deliveryPath(value.id), deliveryDetail(value), { entry: true });
  await button(historyTable(page).getByRole("row").filter({ hasText: value.id }), "Open Teams notification").click();
  await settled(page, api, read); await expect(detail(page)).toContainText(value.id);
}
async function nativeState(page: Page, value: TeamsDelivery) {
  await expect(outcome(page)).toContainText(new RegExp(value.state.replace("-", "[- ]"), "i"));
  for (const text of [value.payload.title, value.payload.body, value.destination.name, value.destination.workflowOrigin]) {
    await expect(detail(page)).toContainText(text);
  }
  await expect(detail(page).getByRole("link", { name: /Teams message|receipt/i })).toHaveCount(0);
  if (value.outboundAttemptedAt !== null) await expect(detail(page).locator(`time[datetime="${value.outboundAttemptedAt}"]`)).toBeVisible();
  if (value.failure) {
    await expect(detail(page)).toContainText(value.failure.code);
    await expect(detail(page)).toContainText(new RegExp(`HTTP(?: status)?\\s*:?\\s*${value.failure.httpStatus}\\b`, "i"));
    await expect(detail(page)).toContainText(/no automatic retr(?:y|ies)|retryable\s*:\s*false/i);
    await expect(detail(page).getByText(/^(?:Stage|Missing fields)\s*:/i)).toHaveCount(0);
  }
}

test("TU4 manual history separates Workflow acceptance, failed delivery and uncertainty", async ({ page, teams }) => {
  teams.roles.set(alpha.id, "viewer"); teams.serverRoles.set(alpha.id, "viewer");
  await bootWork(page, teams); await openFinding(page, teams, first, true); await expect(notify(page)).toHaveCount(0);
  const initial = teams.listTeams(historyPath(), nativePage(historyRows.slice(0, 100), 101, historyRows[99].id), { entry: true });
  await button(finding(page), "Teams notification history").click(); await settled(page, teams, initial);
  await expect(historyTable(page).locator("tbody tr")).toHaveCount(100); await quiet(page, teams);
  const unavailable = teams.listTeams(historyPath(), null, { status: 503, cursor: historyRows[99].id });
  await button(history(page), "Next Teams notifications").click(); await settled(page, teams, unavailable);
  const tail = teams.listTeams(historyPath(), nativePage(historyRows.slice(100), 101), { cursor: historyRows[99].id });
  await button(history(page), "Retry Teams history").click(); await settled(page, teams, tail);
  await expect(historyTable(page).locator("tbody tr")).toHaveCount(1); await expect(history(page)).toContainText(/101/);
  const refresh = teams.listTeams(historyPath(), nativePage(historyRows.slice(0, 100), 101, historyRows[99].id));
  await button(history(page), "Refresh Teams history").click(); await settled(page, teams, refresh);
  for (const value of [accepted, limited, uncertain, blocked, failed]) {
    await openDelivery(page, teams, value); await nativeState(page, value);
    if (value === accepted) {
      await expect(outcome(page)).toContainText("Workflow accepted; channel delivery not confirmed");
      await expect(detail(page).getByText(/^Confirmed$/i)).toHaveCount(0);
    }
    if (value === limited) {
      await expect(detail(page)).toContainText(/Retry.After\s*:?\s*1\s*(?:s\b|seconds)/i);
      await expect(detail(page).getByRole("timer")).toHaveCount(0); await quiet(page, teams, 1100);
    }
    if (value === uncertain) {
      await expect(outcome(page)).toContainText(/may.*sent|possibly.*sent/i);
      await expect(outcome(page)).toContainText(/do not (?:retry|resend)|no blind resend/i);
    }
    if (value === blocked) {
      await expect(detail(page)).toContainText(/no (?:native |outbound )?(?:POST|attempt)|not (?:attempted|sent)/i);
      await expect(detail(page).locator(`time[datetime="${attempted}"]`)).toHaveCount(0);
    }
    await disabled(button(detail(page), /queue|send|resend|retry (?:send|delivery)/i));
  }
  await openDelivery(page, teams, accepted);
  for (const wrong of [
    { ...accepted, state: "confirmed" },
    { ...accepted, receipt: { remoteId: "SYNTHETIC-FAKE-CHANNEL-RECEIPT", remoteUrl: workflowURL } },
  ]) {
    const malformed = teams.readTeams(deliveryPath(accepted.id), { apiVersion, delivery: wrong });
    await button(detail(page), /^(?:Refresh|Retry) Teams notification$/).click(); await settled(page, teams, malformed);
    await expect(detail(page).getByRole("alert")).toContainText(/invalid|not.*confirm|untrusted/i);
    await expect(outcome(page)).toHaveCount(0); await expect(detail(page).getByRole("link")).toHaveCount(0);
    await teams.assertPrivate(page);
  }
  for (const status of [404, 503] as const) {
    const denied = teams.readTeams(deliveryPath(accepted.id), null, { status });
    await button(detail(page), "Retry Teams notification").click(); await settled(page, teams, denied);
    await expect(detail(page)).not.toContainText(first.assetName); await expect(detail(page)).not.toContainText(workflowOrigin);
    await expect(outcome(page)).toHaveCount(0); await expect(historyTable(page).locator("tbody tr")).toHaveCount(100);
  }
  await unchanged(page); expect(teams.teamsCalls.filter((call) => call.method !== "GET")).toHaveLength(0);
  teams.mark("TU4 completed: native cursor retry, receipt-null acceptance, no retry timer, pre-send blocking and withheld denied detail.");
});

test("TU5 uncertain original-key replay cannot turn stale consent into a replacement send", async ({ page, teams }) => {
  await bootWork(page, teams); await openFinding(page, teams); await enterReview(page, teams);
  let original = "";
  const lost = teams.writeTeams("POST", historyPath(), queueBody(preview, (key) => { original = key; teams.remember(key); }),
    deliveryDetail(queued()), { status: "lost-ack" });
  await queue(page).click(); await settled(page, teams, lost);
  await expect(review(page).getByRole("alert")).toContainText(/may.*queued|acknowledgement.*not.*confirm|outcome.*unknown/i);
  await quiet(page, teams);
  await noIO(page, teams, () => button(review(page), "Close Teams review").click());
  await enterReview(page, teams); await expect(replay(page)).toBeEnabled(); await expect(queue(page)).toHaveCount(0);
  expect(queuePosts(teams)).toHaveLength(1);
  const { outboundAttemptedAt: _marker, ...missingMarker } = queued();
  for (const wrong of [{ ...queued(), workspaceId: beta.id }, missingMarker]) {
    const bad = teams.writeTeams("POST", historyPath(), queueBody(preview, () => {}, original),
      { apiVersion, delivery: wrong }, { status: 200 });
    await replay(page).click(); await settled(page, teams, bad);
    await expect(review(page).getByRole("alert")).toContainText(/invalid|not.*confirm|unknown/i);
    await expect(outcome(page)).toHaveCount(0);
    await expect(review(page)).toContainText(/original.*(?:key|intent)|same.*(?:key|intent)/i); await quiet(page, teams);
  }
  const conflict = teams.writeTeams("POST", historyPath(), queueBody(preview, () => {}, original), null, { status: 409 });
  await replay(page).click(); await settled(page, teams, conflict);
  await disabled(queue(page).or(replay(page)));
  await expect(review(page)).toContainText(/unresolved|retained/i);
  const changed = { ...connection, revision: 5, enabled: false, teams: alternate.teams };
  const disabledRead = teams.listTeams(connectionsPath, nativePage([changed, alternate]));
  await button(review(page), "Refresh Teams connections").click(); await settled(page, teams, disabledRead);
  await disabled(queue(page).or(replay(page)));
  const enabled = { ...changed, enabled: true, revision: 6 };
  const current = teams.listTeams(connectionsPath, nativePage([enabled, alternate]));
  await button(review(page), "Refresh Teams connections").click(); await settled(page, teams, current);
  await disabled(queue(page).or(replay(page))); await quiet(page, teams);
  const fresh = localPreview(enabled, "c");
  const freshRead = teams.writeTeams("POST", previewPath(), { connectionId: enabled.id }, previewDetail(fresh));
  await previewAgain(page).click(); await settled(page, teams, freshRead);
  await disabled(queue(page).or(replay(page)));
  await expect(review(page)).toContainText(/unresolved|retained/i);
  const other = teams.writeTeams("POST", previewPath(), { connectionId: alternate.id }, previewDetail(localPreview(alternate, "b")));
  await choose(page).selectOption(alternate.id); await settled(page, teams, other);
  await disabled(queue(page).or(replay(page))); await expect(review(page)).toContainText(/unresolved|retained/i);
  await noIO(page, teams, () => button(review(page), "Close Teams review").click());
  const historyRead = teams.listTeams(historyPath(), nativePage([uncertain]), { entry: true });
  await button(finding(page), "Teams notification history").click(); await settled(page, teams, historyRead);
  await quiet(page, teams);
  expect(queuePosts(teams).map((call) => call.body.idempotencyKey)).toEqual([original, original, original, original]);
  await unchanged(page);
  teams.mark("TU5 completed: explicit same-key uncertain replays only; 409/disablement/revision/target changes retain warning, never replace consent.");
});

async function absentPrivate(page: Page, values: string[]) {
  const state = await page.evaluate(() => document.body.textContent + JSON.stringify(
    [...document.querySelectorAll("input,textarea,select")].map((field) => (field as HTMLInputElement).value)));
  for (const value of values) expect(state).not.toContain(value);
}
async function aborted(page: Page, api: TeamsUIAPI, control: TeamsControl) {
  await expect.poll(() => control.calls.length > 0 && control.calls.every((call) => /abort/i.test(call.failure ?? ""))).toBe(true);
  api.finishAborted(control); await frames(page);
}

test("TU6 scope/logout/actual HTTP401 discard private drafts and held old-scope acknowledgements", async ({ page, teams }) => {
  teams.roles.set(beta.id, "admin"); teams.serverRoles.set(beta.id, "admin");
  await integrations(page, teams); await openConnections(page, teams);
  await addDraft(page, teams);
  await noIO(page, teams, () => button(editor(page), "Cancel").click()); await closedEditor(page, teams);
  await addDraft(page, teams); await secret(page).focus();
  await noIO(page, teams, () => page.keyboard.press("Escape")); await closedEditor(page, teams);
  await addDraft(page, teams);
  const held = teams.writeTeams("POST", connectionsPath, createInput, connectionDetail(created), { status: 201, held: true });
  await save(page).click(); await arrived(held); await disabled(save(page));
  const betaNavigation = teams.integrationsEntry(beta.id);
  await workspace(page).selectOption(beta.id); await finishNavigation(page, teams, betaNavigation);
  await aborted(page, teams, held); await closedEditor(page, teams);
  await expect(toggle(page)).toHaveAttribute("aria-expanded", "false");
  await absentPrivate(page, [connection.name, created.name, workflowURL]);
  const betaConnection = { ...connection, id: connection.id.replace(/^cc/, "ce"), workspaceId: beta.id, name: "Synthetic Teams Beta private" };
  await openConnections(page, teams, [betaConnection], beta.id);
  const late = teams.listTeams(connectionsPath, nativePage([betaConnection]), { workspace: beta.id, held: true });
  await button(panel(page), "Refresh Teams connections").click(); await arrived(late);
  await button(page, "Sign out").click();
  await expect(page.getByRole("form", { name: "Sign in", exact: true })).toBeVisible();
  await aborted(page, teams, late);
  await closedEditor(page, teams); await expect(workspace(page)).toHaveCount(0);
  await absentPrivate(page, [connection.name, created.name, betaConnection.name, workflowURL, user.name, alpha.name, beta.name]);
  const navigation = teams.integrationsEntry();
  const signIn = page.getByRole("form", { name: "Sign in", exact: true });
  await signIn.getByLabel("Email", { exact: true }).fill(user.email);
  await signIn.getByLabel("Password", { exact: true }).fill(password); await button(signIn, "Sign in").click();
  await finishNavigation(page, teams, navigation);
  await expect(toggle(page)).toHaveAttribute("aria-expanded", "false");
  await openConnections(page, teams); await addDraft(page, teams);
  const expired = teams.writeTeams("POST", connectionsPath, createInput, null, { status: 401, held: true });
  await save(page).click(); await settled(page, teams, expired);
  const received = expired.calls.find((call) => call.responseStatus === 401 && call.responseBeforeFailure);
  expect(received, "A fulfilled/aborted fixture without actual HTTP401 is not session-loss evidence.").toBeDefined();
  expect(teams.observedHeaders.get(received!)).toMatchObject({
    "content-type": "application/json; charset=utf-8", "cache-control": "no-store", "x-content-type-options": "nosniff",
  });
  await expect(signIn).toBeVisible(); await expect(panel(page)).toHaveCount(0); await closedEditor(page, teams);
  await absentPrivate(page, [connection.name, created.name, betaConnection.name, workflowURL, user.name, alpha.name, beta.name]);
  await expect(workspace(page)).toHaveCount(0); await quiet(page, teams);
  teams.mark("TU6 completed: cancel/close/scope/logout cleanup, actual late ACK abort and observed401 headers before auth-induced cleanup.");
});
