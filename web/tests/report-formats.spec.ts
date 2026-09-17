import type { Page } from "@playwright/test";
import { requireProductionUI } from "./network";
import {
  assetsPath, betaAsset, csvFile, csvWithExtra, digest, encodedOverflowCSV, gitleaksFile,
  importsPath, intakeAlpha, intakeBeta, invalidUTF8File, literalMapping, manualFile, manualProseFile, metadata,
  nearLimitCSV, profiles, selectedAsset, trivyFile, uploadLimit, validExpiry, zapFile,
} from "./report-formats-data";
import type { IntakeMetadata, IntakeReceipt, ReportFile, ReportProfile } from "./report-formats-data";
import { expect, test } from "./report-formats-fixture";
import type { IntakeCall, IntakeControl, ReportFormatsAPI } from "./report-formats-fixture";

test.use({ reducedMotion: "reduce", timezoneId: "UTC" });
test.beforeEach(async ({ formats }) => {
  requireProductionUI();
  expect(formats.requests).toEqual([]);
});

function dialog(page: Page) { return page.getByRole("dialog", { name: "Import report", exact: true }); }
function form(page: Page) { return dialog(page).getByRole("form", { name: "Import report", exact: true }); }
function launch(page: Page) { return page.getByRole("main").getByRole("button", { name: "Import report", exact: true }); }
function inventory(page: Page) { return page.getByRole("table", { name: "Assets", exact: true, includeHidden: true }); }
function latest(page: Page) { return page.getByRole("region", { name: "Latest report import", exact: true, includeHidden: true }); }
function status(page: Page) {
  return latest(page).getByRole("status", { name: "Import status", exact: true })
    .or(latest(page).getByRole("alert", { name: "Import status", exact: true }));
}
function refresh(page: Page) { return latest(page).getByRole("button", { name: "Refresh import status", exact: true }); }
function submit(page: Page) { return form(page).getByRole("button", { name: "Import report", exact: true }); }
async function frames(page: Page, count = 2) {
  await page.evaluate(async (count) => {
    for (let index = 0; index < count; index++) await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
  }, count);
}
async function visit(page: Page) {
  await page.goto("/#/assets");
  await expect(inventory(page)).toBeVisible();
  await expect(page.getByRole("combobox", { name: "Workspace", exact: true })).toHaveValue(intakeAlpha.id);
}
async function open(page: Page) {
  await launch(page).click();
  await expect(form(page)).toBeVisible();
  await form(page).getByLabel("Asset", { exact: true }).selectOption(selectedAsset.id);
  return form(page);
}
async function choose(page: Page, profile: ReportProfile) {
  const select = form(page).getByRole("combobox", { name: "Format", exact: true });
  await expect(select.locator(`option[value="${profile}"]`),
    `The existing report form must expose backend-supported ${profile}, without inventing an intake API.`).toHaveCount(1);
  await select.selectOption(profile);
  await expect(select).toHaveValue(profile);
  await expect(form(page).getByLabel("Asset", { exact: true }), "Choosing a report format must not reset the selected asset.").toHaveValue(selectedAsset.id);
}
async function prepare(page: Page, file: ReportFile, input: IntakeMetadata) {
  await choose(page, file.format);
  await form(page).getByLabel("Report file", { exact: true }).setInputFiles({ name: file.name, mimeType: file.mimeType, buffer: file.buffer });
  await expect(form(page).getByLabel("Format", { exact: true }), "Filename/MIME/content must not auto-select a different parser.").toHaveValue(file.format);
  for (const [label, value] of Object.entries({
    "Source ID": input.sourceId, "Scan ID": input.scanId, "Scope ID": input.scope.id,
    "Scope revision": input.scope.revision, Branch: input.scope.branch, "Source scan time": input.sourceScanAt ?? "",
  })) await form(page).getByLabel(label, { exact: true }).fill(value);
  await form(page).getByLabel("Source status", { exact: true }).selectOption(input.sourceStatus);
  await form(page).getByLabel("Scan kind", { exact: true }).selectOption(input.scanKind);
  await form(page).getByLabel("Completeness", { exact: true }).selectOption(input.completeness);
}
async function requested(control: IntakeControl) {
  await expect.poll(() => control.call !== null, "The real form must reach the existing import HTTP boundary.").toBe(true);
  return control.call!;
}
async function release(page: Page, control: IntakeControl) {
  control.release();
  await control.delivered;
  await frames(page);
}
function exactUpload(call: IntakeCall, file: ReportFile, input: IntakeMetadata, started: number) {
  const mapped = ["generic-json", "generic-csv"].includes(file.format);
  expect(call).toMatchObject({ method: "POST", path: importsPath, workspace: intakeAlpha.id, query: "" });
  expect(Object.keys(call.body).sort()).toEqual([
    "apiVersion", "assetId", "format", "report", "sourceId", "scanId", "scope", "sourceScanAt",
    "collectedAt", "sourceStatus", "scanKind", "completeness", ...(mapped ? ["mapping"] : []),
  ].sort());
  expect(call.body).toMatchObject({ apiVersion: "aspm/v1alpha1", ...input, format: file.format });
  expect(typeof call.body.report).toBe("string");
  expect(Buffer.from(String(call.body.report)).equals(file.buffer),
    "The wrapper's decoded report string must retain the original file's exact UTF-8 bytes, CRLFs and trailing newlines.").toBe(true);
  expect(digest(String(call.body.report))).toBe(digest(file.buffer));
  expect(validExpiry(call.body.collectedAt)).toBe(true);
  expect(Date.parse(String(call.body.collectedAt))).toBeGreaterThanOrEqual(Math.floor(started / 1000) * 1000);
  expect(Date.parse(String(call.body.collectedAt))).toBeLessThanOrEqual(Date.now());
  expect(call.body.sourceScanAt).toBe(input.sourceScanAt);
  expect(call.bodyBytes).toBeLessThanOrEqual(uploadLimit);
  if (mapped) expect(call.body.mapping).toEqual(literalMapping);
  else expect(call.body).not.toHaveProperty("mapping");
}
function receipt(call: IntakeCall): IntakeReceipt {
  expect(call.status).toBe(202);
  const value = call.response?.import as IntakeReceipt | undefined;
  if (!value) throw new Error("No server-provided queued import DTO.");
  expect(value).toMatchObject({ state: "queued", observationCount: 0, failure: null });
  return value;
}
async function acknowledged(page: Page, value: IntakeReceipt) {
  await expect(form(page)).toHaveCount(0);
  await expect(latest(page)).toContainText(value.id);
  await expect(status(page)).toContainText(/\bqueued\b/i);
  await expect(status(page)).toContainText(/not completed|queued|pending/i);
  await expect(latest(page).locator(`time[datetime="${value.importedAt}"]`)).toBeVisible();
  await expect(page.getByText(/^verified resolution$|^verified$|^scan complete and verified$/i)).toHaveCount(0);
}
async function provenance(page: Page, value: IntakeReceipt) {
  const summary = latest(page).getByText("Report identity and provenance", { exact: true });
  if (!await latest(page).getByText(value.assetId, { exact: true }).isVisible()) await summary.click();
  for (const text of [value.assetId, value.runId, value.format, value.scope.id, value.scope.revision, value.scope.branch, value.reportDigest]) {
    await expect(latest(page)).toContainText(text);
  }
  await expect(latest(page).locator(`time[datetime="${value.collectedAt}"]`)).toBeVisible();
  if (value.sourceScanAt === null) await expect(latest(page).getByText("Unknown source time", { exact: true })).toBeVisible();
  else await expect(latest(page).locator(`time[datetime="${value.sourceScanAt}"]`)).toBeVisible();
}
async function publishAndRefresh(page: Page, formats: ReportFormatsAPI, id: string, state: "processing" | "succeeded" | "failed") {
  const value = formats.publishState(id, state);
  const before = formats.calls("GET", `${importsPath}/${id}`).length;
  await frames(page);
  expect(formats.calls("GET", `${importsPath}/${id}`), "Only an explicit status refresh may read new worker state.").toHaveLength(before);
  await refresh(page).click();
  await expect(status(page)).toContainText(new RegExp(`\\b${state}\\b`, "i"));
  expect(formats.calls("GET", `${importsPath}/${id}`).length).toBe(before + 1);
  if (state === "succeeded") await expect(status(page)).toContainText(/not.*resolution verification|does not verify/i);
  if (state === "failed") {
    await expect(status(page)).toContainText("invalid-report");
    await expect(status(page)).toContainText(value.failure!.message);
  }
}
async function displayedMapping(page: Page) {
  const profile = form(page).locator("details").filter({ has: page.locator("summary").filter({ hasText: /field profile|field mapping/i }) });
  await expect(profile).toHaveCount(1);
  if (!await profile.getAttribute("open")) {
    if (!await profile.locator("pre").isVisible()) await profile.locator("summary").click();
  }
  const text = profile.locator("pre").filter({ hasText: /"sourceFindingId"/ });
  await expect(text).toBeVisible();
  return JSON.parse(await text.innerText()) as unknown;
}
async function noOldData(page: Page, markers: string[]) {
  await expect(dialog(page)).toHaveCount(0);
  await expect(latest(page)).toHaveCount(0);
  for (const marker of markers) await expect(page.locator("body")).not.toContainText(marker);
  await expect(page.locator('input[type="file"]')).toHaveCount(0);
}

test("RF1 The import selector names exactly seven implemented profiles with honest JSON/CSV/manual guidance and literal mapping", async ({ page, formats }) => {
  await visit(page);
  await open(page);
  const options = await form(page).getByLabel("Format", { exact: true }).locator("option").evaluateAll((items) => items.map((item) => ({
    value: (item as HTMLOptionElement).value, label: item.textContent ?? "", disabled: (item as HTMLOptionElement).disabled,
  })));
  expect(options.filter((item) => item.value).map((item) => item.value).sort(),
    "The current two-format starter must expand to all seven implemented backend profiles, not arbitrary scanners or XML.").toEqual([...profiles].sort());
  for (const option of options.filter((item) => item.value)) expect(option.disabled).toBe(false);
  const labels: Record<ReportProfile, RegExp> = {
    sarif: /SARIF\s*2\.1(?:\.0)?/i, trivy: /Trivy.*JSON/i, zap: /ZAP.*JSON/i, gitleaks: /Gitleaks.*JSON/i,
    "generic-json": /Generic.*JSON/i, "generic-csv": /Generic.*CSV/i, manual: /Manual.*JSON/i,
  };
  for (const profile of profiles) expect(options.find((item) => item.value === profile)?.label).toMatch(labels[profile]);
  await choose(page, "trivy");
  await expect(form(page)).toContainText(/SchemaVersion[\s\S]*2|schema(?: version)?\s*[:=]?\s*2/i);
  await choose(page, "zap");
  await expect(form(page)).toContainText("2.16.1");
  await choose(page, "gitleaks");
  await expect(form(page)).toContainText(/JSON array|array.*JSON/i);
  await expect(form(page)).toContainText(/Fingerprint/i);
  await choose(page, "generic-json");
  const mapping = await displayedMapping(page);
  expect(mapping).toEqual(literalMapping);
  await choose(page, "generic-csv");
  expect(await displayedMapping(page)).toEqual(mapping);
  await expect(form(page)).toContainText(/CSV.*(?:header|column)|(?:header|column).*CSV/i);
  const accept = await form(page).getByLabel("Report file", { exact: true }).getAttribute("accept");
  expect(accept === null || accept === "" || /(?:\.csv|text\/csv|text\/plain|\*\/\*)/i.test(accept),
    "The real file chooser must not exclude the newly supported CSV profile.").toBe(true);
  await choose(page, "manual");
  await expect(form(page)).toContainText(/human.authored|structured human|manual.*structured|structured.*manual/i);
  await expect(form(page)).toContainText(/JSON.*object|object.*JSON/i);
  for (const field of ["sourceFindingId", "title", "sourceLocation"]) await expect(form(page)).toContainText(field);
  await expect(form(page).getByRole("button", { name: /^(?:Run|Launch|Execute|Start) (?:scan|scanner|command|AI extraction)/i })).toHaveCount(0);
  expect(formats.requests.filter((call) => call.method !== "GET")).toEqual([]);
  await page.keyboard.press("Escape");
  await expect(launch(page)).toBeFocused();
});

test("RF2 Trivy, ZAP and Gitleaks keep exact real-file bytes and explicit provenance in one JSON request without mapping or local success", async ({ page, formats }) => {
  await visit(page);
  for (const [index, file] of [trivyFile, zapFile, gitleaksFile].entries()) await test.step(file.format, async () => {
    await open(page);
    const input = metadata(file.format, index === 2 ? { sourceScanAt: null, sourceStatus: "failed", scanKind: "delta", completeness: "partial" } :
      index === 1 ? { completeness: "unknown" } : {});
    const started = Date.now();
    await prepare(page, file, input);
    await frames(page);
    expect(formats.calls("POST", importsPath)).toHaveLength(index);
    formats.expectUpload(file, input);
    const held = formats.queueUpload(202, true);
    await submit(page).click();
    const call = await requested(held);
    exactUpload(call, file, input, started);
    await expect(form(page).getByRole("status").filter({ hasText: /reading|submitting/i })).toBeVisible();
    const value = receipt(call);
    await expect(latest(page).getByText(value.id, { exact: true })).toHaveCount(0);
    await release(page, held);
    await acknowledged(page, value);
    await provenance(page, value);
    expect(formats.calls("GET", `${importsPath}/${value.id}`)).toHaveLength(0);
    await publishAndRefresh(page, formats, value.id, "processing");
    await publishAndRefresh(page, formats, value.id, index === 1 ? "failed" : "succeeded");
    await expect(inventory(page)).toContainText(selectedAsset.name);
    await expect(page).toHaveURL(/#\/assets$/);
  });
});

test("RF3 Generic CSV uses the unchanged literal JSON mapping; manual JSON stays human-authored and plain prose is never converted", async ({ page, formats }) => {
  await visit(page);
  for (const file of [csvFile, manualFile]) await test.step(file.format, async () => {
    await open(page);
    const input = metadata(file.format, { sourceScanAt: null, scanKind: "delta", completeness: "partial" });
    const started = Date.now();
    await prepare(page, file, input);
    if (file.format === "generic-csv") expect(await displayedMapping(page)).toEqual(literalMapping);
    formats.expectUpload(file, input);
    const ack = formats.queueUpload();
    await submit(page).click();
    const call = await requested(ack);
    exactUpload(call, file, input, started);
    await ack.delivered;
    const value = receipt(call);
    await acknowledged(page, value);
    await provenance(page, value);
    await publishAndRefresh(page, formats, value.id, "succeeded");
  });
  await open(page);
  const input = metadata("manual-prose", { sourceScanAt: null }), started = Date.now();
  await prepare(page, manualProseFile, input);
  formats.expectUpload(manualProseFile, input);
  const before = formats.calls("POST", importsPath).length, ack = formats.queueUpload();
  await submit(page).click();
  await expect.poll(async () => formats.calls("POST", importsPath).length > before || await form(page).getByRole("alert").isVisible()).toBe(true);
  if (formats.calls("POST", importsPath).length === before) {
    await expect(form(page).getByRole("alert")).toContainText(/JSON|structured|object|manual/i);
    await expect(form(page).getByLabel("Format", { exact: true })).toHaveValue("manual");
  } else {
    const call = await requested(ack);
    exactUpload(call, manualProseFile, input, started);
    await ack.delivered;
    await acknowledged(page, receipt(call));
    await publishAndRefresh(page, formats, receipt(call).id, "failed");
  }
  expect(formats.imports.size).toBeLessThanOrEqual(3);
  await expect(page.getByText(/^verified$|^verified resolution$/i)).toHaveCount(0);
});

test("RF4 CSV accepts a valid near-limit non-JSON file but rejects invalid UTF-8, excessive file bytes and JSON-wrapper expansion without truncation", async ({ page, formats }) => {
  await visit(page);
  await open(page);
  await choose(page, "generic-csv");
  const large = nearLimitCSV(), input = metadata("valid-near-limit"), started = Date.now();
  expect(large.buffer.length).toBe(uploadLimit - 4096);
  expect(() => JSON.parse(large.buffer.toString("utf8"))).toThrow();
  await prepare(page, large, input);
  formats.expectUpload(large, input);
  const accepted = formats.queueUpload();
  await submit(page).click();
  const call = await requested(accepted);
  exactUpload(call, large, input, started);
  await accepted.delivered;
  await acknowledged(page, receipt(call));
  const oversized = csvWithExtra("x".repeat(uploadLimit), "synthetic-oversized-file.csv"), escaped = encodedOverflowCSV();
  expect(oversized.buffer.length).toBeGreaterThan(uploadLimit);
  expect(escaped.buffer.length).toBeLessThan(uploadLimit);
  expect(Buffer.byteLength(JSON.stringify({ report: escaped.buffer.toString("utf8") }))).toBeGreaterThan(uploadLimit);
  for (const [file, error] of [[invalidUTF8File, /UTF.?8/i], [oversized, /8\s*MiB|limit|too large|exceeds/i],
    [escaped, /encoded|request|8\s*MiB|limit/i]] as const) {
    await open(page);
    await prepare(page, file, metadata(file.name));
    await submit(page).click();
    await expect(form(page).getByRole("alert")).toContainText(error);
    await expect(form(page).getByLabel("Asset", { exact: true })).toHaveValue(selectedAsset.id);
    await expect(form(page).getByLabel("Format", { exact: true })).toHaveValue("generic-csv");
    expect(formats.calls("POST", importsPath), "Invalid file/encoded bytes must not be truncated, repaired or posted as a different format.").toHaveLength(1);
    await page.keyboard.press("Escape");
  }
});

test("RF5 New profiles retain central viewer/current-role authority and surface server 403/415/413/401 without guessing another parser", async ({ page, formats }) => {
  formats.roles.set(intakeAlpha.id, "viewer"); formats.serverRoles.set(intakeAlpha.id, "viewer");
  await visit(page);
  for (const button of await launch(page).all()) await expect(button).toBeDisabled();
  await expect(form(page)).toHaveCount(0);
  formats.roles.set(intakeAlpha.id, "analyst"); formats.serverRoles.set(intakeAlpha.id, "analyst");
  await page.reload();
  await expect(inventory(page)).toBeVisible();
  await open(page);
  const input = metadata("denied-gitleaks", { sourceScanAt: null });
  await prepare(page, gitleaksFile, input);
  formats.expectUpload(gitleaksFile, input);
  formats.serverRoles.set(intakeAlpha.id, "viewer");
  for (const [code, error] of [[403, /not permitted|permission|forbidden/i], [415, /not supported|unsupported|format/i],
    [413, /size limit|too large|exceeds/i]] as const) {
    if (code !== 403) formats.serverRoles.set(intakeAlpha.id, "analyst");
    const response = formats.queueUpload(code === 403 ? 202 : code);
    const before = formats.calls("POST", importsPath).length;
    await submit(page).click();
    const call = await requested(response);
    await response.delivered;
    expect(call.status).toBe(code);
    await expect(form(page).getByRole("alert")).toContainText(error);
    await expect(form(page).getByLabel("Format", { exact: true })).toHaveValue("gitleaks");
    await expect(form(page).getByLabel("Asset", { exact: true })).toHaveValue(selectedAsset.id);
    await expect(form(page).getByLabel("Scan ID", { exact: true })).toHaveValue(input.scanId);
    await frames(page, 4);
    expect(formats.calls("POST", importsPath)).toHaveLength(before + 1);
    expect(formats.imports.size).toBe(0);
  }
  const ended = formats.queueUpload(401, true);
  await submit(page).click();
  await requested(ended);
  await release(page, ended);
  await expect(page.getByRole("form", { name: "Sign in", exact: true })).toBeVisible();
  await noOldData(page, [gitleaksFile.name, input.scanId, input.sourceId, selectedAsset.name]);
  await expect(inventory(page)).toHaveCount(0);
  await formats.assertPrivate(page, true);
  await page.reload();
  await expect(page.getByRole("form", { name: "Sign in", exact: true })).toBeVisible();
  await noOldData(page, [gitleaksFile.name, input.scanId, selectedAsset.name]);
});

async function mobileModal(page: Page) {
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1)).toBe(true);
  expect(await dialog(page).evaluate((element) => {
    const rect = element.getBoundingClientRect();
    return rect.left >= -1 && rect.right <= innerWidth + 1 && rect.top >= -1 && rect.bottom <= innerHeight + 1;
  })).toBe(true);
  for (let index = 0; index < 8; index++) {
    await page.keyboard.press("Tab");
    expect(await dialog(page).evaluate((element) => element.contains(document.activeElement))).toBe(true);
  }
}
async function noDecorativeMotion(page: Page) {
  const moving = await page.evaluate(async () => {
    const found = new Set<string>();
    for (let sample = 0; sample < 6; sample++) {
      for (const animation of document.getAnimations()) {
        if (animation.playState !== "running" || !(animation.effect instanceof KeyframeEffect)) continue;
        if (animation.effect.getTiming().iterations === Infinity) found.add("continuous");
        const frames = animation.effect.getKeyframes() as Array<Record<string, unknown>>;
        for (const key of ["transform", "translate", "scale", "rotate", "top", "left"]) {
          if (new Set(frames.map((frame) => frame[key]).filter((value) => value !== undefined).map(String)).size > 1) found.add(key);
        }
      }
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
    }
    return [...found];
  });
  expect(moving).toEqual([]);
}

test("RF6 At 390px CSV file-read cancellation and held-import scope loss clear old bytes/progress while keeping keyboard asset context", async ({ page, formats }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await visit(page);
  const rowsBefore = await inventory(page).allTextContents();
  await open(page);
  await prepare(page, csvFile, metadata("cancel-real-file-read"));
  await mobileModal(page);
  await noDecorativeMotion(page);
  const cancel = await form(page).getByRole("button", { name: "Cancel", exact: true }).elementHandle();
  if (!cancel) throw new Error("No actual form cancel control.");
  // Both native UI events occur before FileReader can deliver its asynchronous load event.
  await form(page).evaluate((element, cancel) => {
    if (!(element instanceof HTMLFormElement) || !(cancel instanceof HTMLButtonElement)) throw new Error("Expected the real native form and cancel button.");
    element.requestSubmit();
    cancel.click();
  }, cancel);
  await expect(dialog(page)).toHaveCount(0);
  await expect(launch(page)).toBeFocused();
  await frames(page, 6);
  expect(formats.calls("POST", importsPath)).toHaveLength(0);
  await noOldData(page, [csvFile.name, "synthetic-format-cancel-real-file-read"]);
  expect(await inventory(page).allTextContents()).toEqual(rowsBefore);
  await formats.assertPrivate(page, true);
  await open(page);
  await expect(form(page).getByLabel("Report file", { exact: true })).toHaveValue("");
  const input = metadata("late-csv-ack", { sourceScanAt: null });
  await prepare(page, csvFile, input);
  formats.expectUpload(csvFile, input);
  const old = formats.queueUpload(202, true);
  await submit(page).focus();
  await submit(page).press("Enter");
  const oldCall = await requested(old), value = receipt(oldCall);
  await expect(form(page).getByRole("status").filter({ hasText: /reading|submitting/i })).toBeVisible();
  await mobileModal(page);
  await page.keyboard.press("Escape");
  await expect(launch(page)).toBeFocused();
  await page.getByRole("combobox", { name: "Workspace", exact: true }).selectOption(intakeBeta.id);
  await expect(inventory(page)).toContainText(betaAsset.name);
  const markers = [csvFile.name, input.scanId, input.sourceId, value.id, value.reportDigest, selectedAsset.name];
  await noOldData(page, markers);
  await release(page, old);
  await expect.poll(() => oldCall.failure, "Closing/scope loss must cancel the old protected request.").toMatch(/abort/i);
  await noOldData(page, markers);
  expect(formats.imports.get(value.id)?.workspace, "Browser cancellation does not falsely roll back an already persisted server intent.").toBe(intakeAlpha.id);
  await expect(inventory(page)).not.toContainText(selectedAsset.name);
  await formats.assertPrivate(page, true);
  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  await expect(page.getByRole("form", { name: "Sign in", exact: true })).toBeVisible();
  await noOldData(page, markers);
  await formats.assertPrivate(page, true);
  expect(formats.requests.filter((call) => call.method !== "GET").map((call) => call.path)).toEqual([importsPath, "/api/v1/logout"]);
  expect(formats.calls("GET", assetsPath).length).toBeGreaterThan(1);
});
