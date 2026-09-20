import assert from "node:assert/strict";
import { createHash } from "node:crypto";
import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join, relative } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";
import { chromium, expect } from "@playwright/test";
import { expectIneligibleOption } from "./native-option-A1.ts";

const root = fileURLToPath(new URL("../../../../", import.meta.url));
const label = process.argv[2];
if (!label || !/^[a-z0-9]+(?:-[a-z0-9]+)*$/.test(label)) throw new Error("A fresh bounded calibration label is required.");
const output = join(root, ".artifacts", "ai-settings-ui-v1-author", label);
if (existsSync(output)) throw new Error("Calibration outputs must never overwrite prior evidence.");
mkdirSync(output, { recursive: true });
const fixture = fileURLToPath(new URL("./native-option-A1.html", import.meta.url));
const fixtureURL = pathToFileURL(fixture).href;
function witness(file) {
  const bytes = readFileSync(file);
  return { path: relative(root, file), bytes: bytes.length, sha256: createHash("sha256").update(bytes).digest("hex") };
}
const corePath = join(root, "web", "node_modules", "playwright-core", "lib", "coreBundle.js");
const core = readFileSync(corePath, "utf8");
assert.equal(witness(corePath).sha256, "549070af3acabb3efcc4f55bfe6210f9f7c2fcf633cf7eaa59bfe60719969171");
const checks = [], attemptedNetwork = [], pageErrors = [];
const report = {
  schemaVersion: 1, capturedAt: new Date().toISOString(), run: label,
  author: "successor-independent-ai-settings-ui-a1-native-option-20260920",
  scope: "Isolated static synthetic native DOM fixture measurement proof only, NOT product acceptance.",
  constraints: {
    applicationDOMOrStateInjection: false, applicationNavigation: false, http: false, providers: false,
    nativeApplicationAPI: false, database: false, serviceWorkers: "block", pages: 1,
    assertionTimeoutMs: 5_000, perCheckCapMs: 20_000, totalCapMs: 60_000, retries: 0,
  },
  sources: [
    witness(fileURLToPath(import.meta.url)), witness(fixture),
    witness(fileURLToPath(new URL("./native-option-A1.ts", import.meta.url))), witness(corePath),
  ],
  installedMechanism: [
    ["function getAriaDisabled(", 610], ["retarget(node, behavior) {", 970], ["elementState(node, state) {", 1150],
  ].map(([needle, length]) => {
    const offset = core.indexOf(needle);
    assert.ok(offset >= 0, `Installed source must contain ${needle}`);
    return { needle, offset, escapedSourceExcerpt: core.slice(offset, offset + length) };
  }),
  checks, attemptedNetwork, pageErrors, outcome: "running",
};
const browser = await chromium.launch({ headless: true });
const deadline = setTimeout(() => {
  report.deadlineExceeded = true;
  void browser.close();
}, 60_000);
const context = await browser.newContext({ serviceWorkers: "block", acceptDownloads: false });
context.setDefaultTimeout(5_000);
await context.route("**/*", async (route) => {
  if (route.request().url() === fixtureURL && route.request().isNavigationRequest()) {
    await route.continue(); return;
  }
  attemptedNetwork.push({ method: route.request().method(), url: route.request().url() });
  await route.abort();
});
await context.routeWebSocket("**/*", (socket) => {
  attemptedNetwork.push({ websocket: socket.url() });
  socket.close();
});
await context.tracing.start({ snapshots: true, screenshots: true, sources: true });
const page = await context.newPage();
page.on("pageerror", (error) => pageErrors.push(error.message));
async function check(name, run) {
  const started = performance.now();
  try {
    const evidence = await run();
    const durationMs = performance.now() - started;
    assert.ok(durationMs < 20_000, "Each calibration check retains a 20-second cap.");
    checks.push({ name, passed: true, durationMs, evidence });
    console.log(`PASS ${name}`);
  } catch (error) {
    checks.push({ name, passed: false, durationMs: performance.now() - started, error: String(error.stack ?? error) });
    throw error;
  }
}
async function rejected(run, expected) {
  let failure;
  const started = performance.now();
  try { await run(); } catch (error) { failure = error; }
  assert.ok(failure, "The negative calibration must actually reject.");
  const message = String(failure.message).replace(/\u001b\[[0-9;]*m/g, "");
  assert.match(message, expected);
  return { expectedRejection: true, durationMs: performance.now() - started, message };
}
async function native(option) {
  return option.evaluate((element) => ({
    tag: element.tagName, nativeDisabled: element.matches(":disabled"),
    ownDisabled: element instanceof HTMLOptionElement ? element.disabled : null,
    ariaDisabled: element.getAttribute("aria-disabled"),
    labelControlTag: element.closest("label")?.control?.tagName ?? null,
    labelControlDisabled: element.closest("label")?.control?.matches(":disabled") ?? null,
    html: element.outerHTML,
  }));
}
try {
  await page.goto(fixtureURL);
  const wrapped = page.getByRole("combobox", { name: "Wrapped Profile", exact: true });
  const option = (name) => wrapped.getByRole("option", { name, exact: true });
  await check("Wrapped enabled SELECT: disabled OPTION passes new check and old assertion fails", async () => {
    await expect(wrapped).toBeEnabled();
    const disabled = option("Disabled profile"), observed = await native(disabled);
    assert.equal(observed.ownDisabled, true);
    assert.equal(observed.nativeDisabled, true);
    assert.equal(observed.labelControlTag, "SELECT");
    assert.equal(observed.labelControlDisabled, false);
    await expectIneligibleOption(disabled);
    const oldAssertion = await rejected(() => expect(disabled).toBeDisabled(), /Received:\s+enabled/);
    assert.match(oldAssertion.message, /5000ms/);
    return { observed, oldAssertion, newAssertion: "passed" };
  });
  await check("Paired unwrapped SELECT: the same native disabled OPTION passes the old assertion", async () => {
    const select = page.getByRole("combobox", { name: "Unwrapped Profile", exact: true });
    const disabled = select.getByRole("option", { name: "Unwrapped disabled", exact: true });
    await expect(select).toBeEnabled();
    const observed = await native(disabled);
    assert.equal(observed.nativeDisabled, true);
    assert.equal(observed.labelControlTag, null);
    await expect(disabled).toBeDisabled();
    await expectIneligibleOption(disabled);
    return observed;
  });
  await check("Present enabled ineligible OPTION fails the new check", async () => {
    const enabled = option("Enabled ineligible profile"), observed = await native(enabled);
    assert.equal(observed.nativeDisabled, false);
    const rejection = await rejected(() => expectIneligibleOption(enabled), /Received string:\s+"invalid"/);
    assert.match(rejection.message, /5000ms/);
    return { observed, rejection };
  });
  await check("Omitted ineligible OPTION is allowed explicitly", async () => {
    const omitted = option("Omitted ineligible profile");
    await expect(omitted).toHaveCount(0);
    await expectIneligibleOption(omitted);
    return { matchedCount: await omitted.count(), result: "omitted, permitted by original contract" };
  });
  await check("Disabled OPTGROUP inheritance passes without an own OPTION disabled property", async () => {
    const inherited = option("Group disabled profile"), observed = await native(inherited);
    assert.equal(observed.ownDisabled, false);
    assert.equal(observed.nativeDisabled, true);
    await expectIneligibleOption(inherited);
    return observed;
  });
  await check("Enabled eligible options remain selectable and native keyboard skips disabled options", async () => {
    await expect(wrapped).toBeEnabled();
    await wrapped.selectOption("eligible-a");
    await expect(wrapped).toHaveValue("eligible-a");
    await wrapped.press("ArrowDown");
    await expect(wrapped).toHaveValue("eligible-b");
    await wrapped.selectOption("eligible-a");
    await expect(wrapped).toHaveValue("eligible-a");
    await wrapped.selectOption("eligible-b");
    await expect(wrapped).toHaveValue("eligible-b");
    await expect(wrapped).toBeEnabled();
    return { nativeActions: ["selectOption eligible-a", "ArrowDown skips disabled OPTION and OPTGROUP", "selectOption eligible-a", "selectOption eligible-b"], selected: await wrapped.inputValue() };
  });
  await check("aria-disabled alone is selectable and fails native selection-blocking measurement", async () => {
    const select = page.getByRole("combobox", { name: "Aria Profile", exact: true });
    const aria = select.getByRole("option", { name: "Aria-only profile", exact: true }), observed = await native(aria);
    assert.equal(observed.ariaDisabled, "true");
    assert.equal(observed.nativeDisabled, false);
    const rejection = await rejected(() => expectIneligibleOption(aria), /Received string:\s+"invalid"/);
    await select.selectOption("aria-only");
    await expect(select).toHaveValue("aria-only");
    return { observed, rejection, selected: await select.inputValue() };
  });
  await check("A natively disabled non-OPTION cannot pass via a role or disabled attribute", async () => {
    const impostor = page.getByRole("option", { name: "Non-option disabled impostor", exact: true });
    const observed = await native(impostor);
    assert.equal(observed.tag, "BUTTON");
    assert.equal(observed.nativeDisabled, true);
    const rejection = await rejected(() => expectIneligibleOption(impostor), /Received string:\s+"invalid"/);
    return { observed, rejection };
  });
  await check("Duplicate option identity fails strictly without first/nth/index fallbacks", async () => {
    const duplicate = page.getByRole("combobox", { name: "Ambiguous Profile", exact: true })
      .getByRole("option", { name: "Duplicate identity", exact: true });
    await expect(duplicate).toHaveCount(2);
    const rejection = await rejected(() => expectIneligibleOption(duplicate), /strict mode violation/);
    return { matchedCount: await duplicate.count(), rejection };
  });
  assert.equal(checks.length, 9);
  assert.deepEqual(attemptedNetwork, []);
  assert.deepEqual(pageErrors, []);
  assert.equal(report.deadlineExceeded, undefined);
  report.outcome = "passed";
} catch (error) {
  report.outcome = "failed";
  report.error = String(error.stack ?? error);
  process.exitCode = 1;
} finally {
  clearTimeout(deadline);
  try {
    await context.tracing.stop({ path: join(output, "trace.zip") });
    report.trace = witness(join(output, "trace.zip"));
  } finally {
    await browser.close();
    report.completedAt = new Date().toISOString();
    writeFileSync(join(output, "calibration.json"), JSON.stringify(report, null, 2) + "\n", { flag: "wx" });
  }
}
console.log(`${checks.filter((check) => check.passed).length}/${checks.length} native DOM calibration checks passed. Measurement proof only.`);
