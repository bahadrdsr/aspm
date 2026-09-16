import type { Locator, Page } from "@playwright/test";
import { apiVersion } from "./api-contract";
import {
  alpha, beta, bootstrapToken, createdAsset, detailedFinding, expect, observations, originalAsset,
  password, scope, sessionCookie, sourceFreshnessAt, sourceId, sourceScanAt, test, updatedAsset, wrongPassword,
} from "./application-fixture";
import { syntheticSession, workItems } from "./fixtures";
import { requireProductionUI } from "./network";

test.use({ reducedMotion: "reduce" });
test.beforeEach(async ({ app }) => {
  requireProductionUI();
  expect(app.requests).toEqual([]);
});

function findingAction(page: Page): Locator {
  const row = page.getByRole("table", { name: "Findings" }).getByRole("row").filter({ hasText: detailedFinding.title });
  return row.getByRole("button", { name: detailedFinding.title, exact: true })
    .or(row.getByRole("link", { name: detailedFinding.title, exact: true }));
}

async function noProtectedData(page: Page) {
  await expect(page.getByRole("table", { name: "Findings", includeHidden: true })).toHaveCount(0);
  await expect(page.getByRole("table", { name: "Assets", includeHidden: true })).toHaveCount(0);
  await expect(page.getByRole("combobox", { name: "Workspace", exact: true, includeHidden: true })).toHaveCount(0);
  await expect(page.getByRole("dialog", { name: detailedFinding.title, includeHidden: true })).toHaveCount(0);
  for (const item of workItems) await expect(page.getByText(item.title, { exact: true })).toHaveCount(0);
}

async function fillSignIn(page: Page, value: string) {
  const form = page.getByRole("form", { name: "Sign in", exact: true });
  await expect(form).toBeVisible();
  await form.getByLabel("Email", { exact: true }).fill(syntheticSession().user.email);
  await expect(form.getByLabel("Password", { exact: true })).toHaveAttribute("type", "password");
  await form.getByLabel("Password", { exact: true }).fill(value);
  await form.getByRole("button", { name: "Sign in", exact: true }).click();
}

test("M04 authenticated startup uses the session endpoint and logout removes protected context", async ({ page, app }) => {
  await page.goto("/#/work");
  await expect.poll(() => app.calls("GET", "/api/v1/session").length, "The real App must request its session on startup.").toBeGreaterThan(0);
  expect(app.requests[0].path).toBe("/api/v1/session");
  expect(app.calls("POST", "/api/v1/login")).toHaveLength(0);
  await expect(page.getByRole("combobox", { name: "Workspace", exact: true })).toHaveValue(alpha.id);
  await findingAction(page).click();
  await expect(page.getByRole("dialog", { name: detailedFinding.title })).toBeVisible();
  await page.keyboard.press("Escape");
  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  await expect(page.getByRole("form", { name: "Sign in", exact: true })).toBeVisible();
  expect(app.calls("POST", "/api/v1/logout")).toHaveLength(1);
  await noProtectedData(page);
  expect((await page.context().cookies()).filter((cookie) => cookie.name === "aspm_session")).toEqual([]);
  await page.reload();
  await expect(page.getByRole("form", { name: "Sign in", exact: true })).toBeVisible();
  await noProtectedData(page);
});

test("M04 invalid login is safe and an expired session cannot leave cached findings visible", async ({ page, app }) => {
  app.authenticated = false;
  await page.context().clearCookies({ name: "aspm_session" });
  await page.goto("/#/work");
  await expect(page.getByRole("form", { name: "Sign in", exact: true })).toBeVisible();
  await noProtectedData(page);
  expect(app.calls("GET", "/api/v1/work")).toHaveLength(0);
  await fillSignIn(page, wrongPassword);
  await expect(page.getByRole("alert")).toContainText(/invalid email or password/i);
  await expect(page.locator("body")).not.toContainText(wrongPassword);
  expect(page.url()).not.toContain(encodeURIComponent(wrongPassword));
  await noProtectedData(page);
  await fillSignIn(page, password);
  await expect(page.getByRole("table", { name: "Findings" })).toBeVisible();
  expect(app.calls("POST", "/api/v1/login")).toHaveLength(2);
  expect(app.calls("POST", "/api/v1/login")[1].body).toMatchObject({ email: syntheticSession().user.email, password });
  const cookie = (await page.context().cookies()).find((item) => item.name === "aspm_session");
  expect(cookie?.httpOnly).toBe(true);
  expect(await page.evaluate(() => document.cookie)).not.toContain(sessionCookie);
  await page.getByRole("checkbox", { name: `Select ${detailedFinding.title}`, exact: true }).check();
  app.authenticated = false;
  await page.getByRole("button", { name: "Refresh", exact: true }).click();
  await expect(page.getByRole("form", { name: "Sign in", exact: true })).toBeVisible();
  await noProtectedData(page);
  await page.reload();
  await expect(page.getByRole("form", { name: "Sign in", exact: true })).toBeVisible();
  await noProtectedData(page);
});

test("M04 first-time setup sends the out-of-band token only to bootstrap and then requires sign-in", async ({ page, app }) => {
  app.authenticated = false;
  app.bootstrapAvailable = true;
  await page.context().clearCookies({ name: "aspm_session" });
  await page.goto("/");
  const setup = page.getByRole("button", { name: "Set up instance", exact: true });
  await expect(setup).toBeVisible();
  await setup.click();
  const form = page.getByRole("form", { name: "Set up instance", exact: true });
  await expect(form).toBeVisible();
  await form.getByLabel("Workspace name", { exact: true }).fill("Synthetic first workspace");
  await form.getByLabel("Name", { exact: true }).fill("Synthetic instance admin");
  await form.getByLabel("Email", { exact: true }).fill(syntheticSession().user.email);
  await expect(form.getByLabel("Password", { exact: true })).toHaveAttribute("type", "password");
  await form.getByLabel("Password", { exact: true }).fill(password);
  await expect(form.getByLabel("Bootstrap token", { exact: true })).toHaveAttribute("type", "password");
  await form.getByLabel("Bootstrap token", { exact: true }).fill(bootstrapToken);
  await form.getByRole("button", { name: "Set up instance", exact: true }).click();
  await expect(page.getByRole("form", { name: "Sign in", exact: true })).toBeVisible();
  await expect(page.getByRole("status")).toContainText(/set up|created|sign in/i);
  const writes = app.calls("POST", "/api/v1/bootstrap");
  expect(writes).toHaveLength(1);
  expect(writes[0].bootstrap).toBe(bootstrapToken);
  expect(writes[0].body).toMatchObject({ workspaceName: "Synthetic first workspace", name: "Synthetic instance admin", email: syntheticSession().user.email, password });
  expect(JSON.stringify(writes[0].body)).not.toContain(bootstrapToken);
  expect(app.calls("POST", "/api/v1/login")).toHaveLength(0);
  await noProtectedData(page);
});

test("M04 changing workspace clears filters, selection, details and old rows before the new response", async ({ page, app }) => {
  await page.goto("/#/work");
  const filter = page.getByRole("textbox", { name: "Filter findings" });
  await filter.fill("alpha");
  await page.getByRole("checkbox", { name: `Select ${detailedFinding.title}`, exact: true }).check();
  await findingAction(page).click();
  await expect(page.getByRole("dialog", { name: detailedFinding.title })).toBeVisible();
  await page.keyboard.press("Escape");
  const workspace = page.getByRole("combobox", { name: "Workspace", exact: true });
  await expect(workspace).toBeVisible();
  app.holdWork(beta.id);
  await workspace.selectOption(beta.id);
  await expect.poll(() => app.calls("GET", "/api/v1/work").some((call) => call.workspace === beta.id)).toBe(true);
  await expect(filter).toHaveValue("");
  await expect(page.getByRole("dialog", { includeHidden: true })).toHaveCount(0);
  await expect(page.getByText(detailedFinding.title, { exact: true })).toHaveCount(0);
  await expect(page.getByRole("checkbox", { checked: true })).toHaveCount(0);
  app.releaseWork();
  await expect(page.getByRole("table", { name: "Findings" }).getByText(workItems[2].title, { exact: true })).toBeVisible();
  expect(app.calls("GET", `/api/v1/findings/${detailedFinding.id}`)[0].workspace).toBe(alpha.id);
  await expect(workspace).toHaveValue(beta.id);
});

test("M04 assets can be created and edited without leaving their selected workspace", async ({ page, app }) => {
  await page.goto("/#/assets");
  const create = page.getByRole("button", { name: "Create asset", exact: true });
  await expect(create).toBeVisible();
  await create.click();
  const form = page.getByRole("form", { name: "Create asset", exact: true });
  await form.getByLabel("Asset name", { exact: true }).fill(createdAsset.name);
  await form.getByLabel("Kind", { exact: true }).selectOption("repository");
  await form.getByLabel("Environment", { exact: true }).fill("test");
  await form.getByLabel("Criticality", { exact: true }).selectOption("medium");
  await form.getByLabel("Tags", { exact: true }).fill("ui-synthetic, owned");
  await form.getByRole("button", { name: "Create asset", exact: true }).click();
  const table = page.getByRole("table", { name: "Assets", exact: true });
  const row = table.getByRole("row").filter({ hasText: createdAsset.name });
  await expect(row).toBeVisible();
  expect(app.calls("POST", "/api/v1/assets")).toHaveLength(1);
  expect(app.calls("POST", "/api/v1/assets")[0]).toMatchObject({ workspace: alpha.id, body: {
    name: createdAsset.name, kind: "repository", environment: "test", criticality: "medium", tags: createdAsset.tags, ownerId: null,
  } });
  await row.getByRole("button", { name: "Edit asset", exact: true }).click();
  const edit = page.getByRole("form", { name: "Edit asset", exact: true });
  await expect(edit.getByLabel("Asset name", { exact: true })).toHaveValue(createdAsset.name);
  await edit.getByLabel("Asset name", { exact: true }).fill(updatedAsset.name);
  await edit.getByLabel("Environment", { exact: true }).fill("staging");
  await edit.getByLabel("Criticality", { exact: true }).selectOption("high");
  await edit.getByRole("button", { name: "Save asset", exact: true }).click();
  await expect(table.getByRole("row").filter({ hasText: updatedAsset.name })).toContainText("staging");
  expect(app.calls("PATCH", `/api/v1/assets/${createdAsset.id}`)).toHaveLength(1);
  expect(app.calls("PATCH", `/api/v1/assets/${createdAsset.id}`)[0]).toMatchObject({ workspace: alpha.id, body: {
    name: updatedAsset.name, environment: "staging", criticality: "high",
  } });
  expect(app.calls("POST", "/api/v1/assets")).toHaveLength(1);
  await expect(page.getByRole("combobox", { name: "Workspace", exact: true })).toHaveValue(alpha.id);
  await expect(page).toHaveURL(/#\/assets$/);
});

test("M05-M07 local SARIF and JSON files send exact import metadata and show server-controlled progress/failure", async ({ page, app }) => {
  const mapping = { sourceFindingId: "id", title: "title", sourceSeverity: "severity", sourceLocation: "path", sourceLine: "line", impact: "impact", remediation: "remediation", description: "description" };
  const sarif = { version: "2.1.0", runs: [{
    tool: { driver: { name: "synthetic-scanner", rules: [{ id: "synthetic-only", shortDescription: { text: "Synthetic upload observation" }, help: { text: "Review synthetic configuration." } }] } },
    results: [{ ruleId: "synthetic-only", guid: "dddddddd-dddd-4ddd-8ddd-dddddddddddd", level: "warning",
      message: { text: "Synthetic browser upload." },
      locations: [{ physicalLocation: { artifactLocation: { uri: "src/synthetic.ts" }, region: { startLine: 12 } } }],
      properties: { impact: "Synthetic only.", vendorNote: "Preserve this synthetic browser report." } }],
  }] };
  const reports = [
    { format: "sarif", name: "synthetic.sarif", report: JSON.stringify(sarif, null, 2).replaceAll("\n", "\r\n") + "\r\n", state: "succeeded" as const },
    { format: "generic-json", name: "synthetic.json", report: JSON.stringify([{ id: "synthetic-json", title: "Synthetic JSON observation", severity: "HIGH", path: "src/synthetic.ts", line: 12, impact: "Synthetic only.", remediation: "Review fixture.", description: "Synthetic local file." }]) + "\n", state: "failed" as const },
  ];
  for (const [index, input] of reports.entries()) {
    await test.step(input.format, async () => {
      await page.goto("/#/assets");
      const open = page.getByRole("button", { name: "Import report", exact: true });
      await expect(open).toBeVisible();
      await open.click();
      const form = page.getByRole("form", { name: "Import report", exact: true });
      await form.getByLabel("Asset", { exact: true }).selectOption(originalAsset.id);
      await form.getByLabel("Format", { exact: true }).selectOption(input.format);
      const started = Date.now();
      await form.getByLabel("Report file", { exact: true }).setInputFiles({ name: input.name, mimeType: "application/json", buffer: Buffer.from(input.report, "utf8") });
      const scanTime = input.format === "sarif" ? sourceScanAt : null;
      for (const [label, value] of Object.entries({ "Source ID": sourceId, "Scan ID": `synthetic-upload-${index + 1}`, "Scope ID": scope.id, "Scope revision": scope.revision, Branch: scope.branch, "Source scan time": scanTime ?? "" })) {
        await form.getByLabel(label, { exact: true }).fill(value);
      }
      await form.getByLabel("Source status", { exact: true }).selectOption("succeeded");
      await form.getByLabel("Scan kind", { exact: true }).selectOption("full");
      await form.getByLabel("Completeness", { exact: true }).selectOption("complete");
      await form.getByRole("button", { name: "Import report", exact: true }).click();
      const status = page.getByRole("status", { name: "Import status", exact: true }).or(page.getByRole("alert", { name: "Import status", exact: true }));
      await expect(status).toContainText(/queued|pending/i);
      expect(app.calls("POST", "/api/v1/imports")).toHaveLength(index + 1);
      const upload = app.calls("POST", "/api/v1/imports")[index];
      expect(upload.workspace).toBe(alpha.id);
      expect(upload.body).toMatchObject({ apiVersion, assetId: originalAsset.id, format: input.format, report: input.report,
        sourceId, scanId: `synthetic-upload-${index + 1}`, scope, sourceScanAt: scanTime, sourceStatus: "succeeded", scanKind: "full", completeness: "complete" });
      expect(typeof upload.body.collectedAt).toBe("string");
      expect(String(upload.body.collectedAt)).toMatch(/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/);
      const collected = Date.parse(String(upload.body.collectedAt));
      expect(collected).toBeGreaterThanOrEqual(Math.floor(started / 1000) * 1000);
      expect(collected).toBeLessThanOrEqual(Date.now());
      if (input.format === "generic-json") expect(upload.body.mapping).toEqual(mapping);
      const id = `synthetic-import-${index + 1}`;
      if (input.state === "succeeded") {
        app.setImportState(id, "processing");
        await page.getByRole("button", { name: "Refresh import status", exact: true }).click();
        await expect(status).toContainText(/processing/i);
      }
      app.setImportState(id, input.state);
      await page.getByRole("button", { name: "Refresh import status", exact: true }).click();
      await expect(status).toContainText(input.state === "succeeded" ? /succeeded/i : /failed/i);
      expect(app.calls("GET", `/api/v1/imports/${id}`).length).toBeGreaterThan(0);
      if (input.state === "failed") await expect(page.getByRole("alert")).toContainText("Synthetic parser rejected this report.");
      await expect(page.getByText(/^verified resolution$|^verified$/i)).toHaveCount(0);
    });
  }
});

test("M06 work/detail preserve both observations and distinct freshness without verified-resolution claims", async ({ page }) => {
  await page.goto("/#/work");
  const row = page.getByRole("table", { name: "Findings" }).getByRole("row").filter({ hasText: detailedFinding.title });
  await expect(row.locator(`time[datetime="${sourceScanAt}"]`)).toBeVisible();
  await findingAction(page).click();
  const panel = page.getByRole("dialog", { name: detailedFinding.title });
  await expect(panel).toBeVisible();
  const list = panel.getByRole("list", { name: "Observations", exact: true });
  await expect(list.getByRole("listitem")).toHaveCount(2);
  for (const observation of observations) {
    const item = list.getByRole("listitem").filter({ hasText: observation.scanId });
    await expect(item).toContainText(observation.sourceId);
    await expect(item).toContainText(`${observation.sourceLocation.uri}:${observation.sourceLocation.line}`);
    await expect(item).toContainText(/warning/i);
    await expect(item).toContainText(observation.unmapped.vendorNote);
  }
  const freshness = panel.locator("section").filter({ has: page.getByRole("heading", { name: "Freshness & provenance", exact: true }) });
  for (const stamp of [sourceScanAt, sourceFreshnessAt, detailedFinding.collectedAt, detailedFinding.importedAt]) {
    await expect(freshness.locator(`time[datetime="${stamp}"]`)).toBeVisible();
  }
  await expect(panel.getByText("Synthetic analyst note retained.", { exact: true })).toBeVisible();
  await expect(panel).toContainText(/inferred.?resolved/i);
  await expect(panel).toContainText(/accepted.?risk/i);
  await expect(panel.getByText(/^not verified$|^verification not run$/i)).toBeVisible();
  await expect(panel.getByText(/^verified resolution$|^resolved and verified$|^verified$/i)).toHaveCount(0);
});
