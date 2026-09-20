import type { Locator, Page } from "@playwright/test";
import { requireProductionUI } from "./network";
import { catalogResponse } from "./fixtures";
import {
  aiAlpha, aiBeta, aiGamma, aiPassword, aiUser, apiVersion, betaProfile, changedAt, configuredPolicy, createInput,
  defaultPolicy, disabledProfile, draftKey, expiry, families, gammaProfile, grantFields, grantFor, grantPath,
  grantSeries, grantsPath, hostedProfile, inertProfile, policyPath, profilePath, profileSeries, profilesPath,
  refreshedProfile, replacementKey, revokePath, unreviewedProfile,
} from "./ai-settings-ui-data";
import type { AIGrant, AIPolicy, AIProfile, Mode, ProfileInput } from "./ai-settings-ui-data";
import { expect, test } from "./ai-settings-ui-fixture";
import type { AICall, AIControl, AISettingsHTTP } from "./ai-settings-ui-fixture";
import { expectIneligibleOption } from "./reviews/ai-settings-ui-v1/native-option-A1";

test.use({ reducedMotion: "reduce", timezoneId: "UTC" });
test.beforeEach(async ({ ai }) => { requireProductionUI(); expect(ai.requests).toEqual([]); });

function entry(page: Page) { return page.getByRole("button", { name: "AI settings", exact: true }); }
function panel(page: Page) { return page.getByRole("region", { name: "AI settings", exact: true, includeHidden: true }); }
function profiles(page: Page) { return page.getByRole("region", { name: "AI profiles", exact: true, includeHidden: true }); }
function policy(page: Page) { return page.getByRole("region", { name: "AI policy", exact: true, includeHidden: true }); }
function policyStatus(page: Page) { return policy(page).getByRole("status", { name: "Current AI policy", exact: true, includeHidden: true }); }
function grants(page: Page) { return page.getByRole("region", { name: "AI grants", exact: true, includeHidden: true }); }
function profileRow(page: Page, name: string) {
  return profiles(page).getByRole("table", { name: "AI profiles", exact: true, includeHidden: true })
    .getByRole("row", { includeHidden: true }).filter({ has: page.getByText(name, { exact: true }) });
}
function grantRow(page: Page, id: string) {
  return grants(page).getByRole("table", { name: "AI grants", exact: true, includeHidden: true })
    .getByRole("row", { includeHidden: true }).filter({ hasText: id });
}
function editor(page: Page) { return page.getByRole("dialog", { name: /^(Create|Edit) AI profile$/ }); }
function profileForm(page: Page) { return editor(page).getByRole("form", { name: /^(Create|Edit) AI profile$/ }); }
function approval(page: Page) { return page.getByRole("region", { name: "Grant approval", exact: true, includeHidden: true }); }
function grantForm(page: Page) { return approval(page).getByRole("form", { name: "Approve AI grant", exact: true }); }
function consent(page: Page) { return approval(page).getByRole("region", { name: "Grant consent", exact: true }); }
function acknowledge(page: Page) { return grantForm(page).getByRole("checkbox", { name: "Approve potentially sensitive finding evidence", exact: true }); }
function confirmGrant(page: Page) { return grantForm(page).getByRole("button", { name: "Create grant", exact: true }); }
function grantDetails(page: Page) { return page.getByRole("region", { name: "Grant details", exact: true, includeHidden: true }); }
function key(page: Page) { return profileForm(page).getByLabel("API key", { exact: true }); }
function workspace(page: Page) { return page.getByRole("combobox", { name: "Workspace", exact: true }); }
function writeCalls(ai: AISettingsHTTP) { return ai.aiCalls().filter((call) => call.method !== "GET"); }
async function frames(page: Page, count = 2) {
  await page.evaluate(async (count) => {
    for (let index = 0; index < count; index++) await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
  }, count);
}
async function requested(control: AIControl) {
  await expect.poll(() => control.call !== null, "The explicit action must reach the declared HTTP boundary.").toBe(true);
  return control.call!;
}
async function release(page: Page, control: AIControl, aborted = false) {
  control.release(); await control.delivered; await frames(page);
  if (aborted) for (const call of control.calls) {
    await expect.poll(() => call.failure, "Old AI requests must actually abort, not just hide their late response.").toMatch(/abort/i);
  }
}
async function noEnabledActions(actions: Locator) { for (const action of await actions.all()) await expect(action).toBeDisabled(); }
async function shell(page: Page, ai: AISettingsHTTP) {
  await page.goto("/#/integrations");
  await expect(page.getByRole("heading", { name: "Integrations", exact: true, level: 1 })).toBeVisible();
  await expect(page.getByRole("navigation", { name: "Primary", exact: true }).getByRole("link")).toHaveCount(5);
  const catalog = page.getByRole("list", { name: "Native integrations", exact: true });
  await expect(catalog.getByRole("listitem")).toHaveCount(8);
  for (const family of catalogResponse.items) await expect(catalog.getByRole("heading", { name: family.name, exact: true })).toBeVisible();
  await expect(page.getByRole("region", { name: "Connections", exact: true })).toBeVisible();
  await expect(page.getByRole("region", { name: "Sources", exact: true })).toBeVisible();
  await frames(page);
  expect(ai.aiCalls(), "Integrations must not eagerly add AI requests to the 93 legacy workflows.").toEqual([]);
  await expect(panel(page)).toHaveCount(0);
  ai.mark("closed-shell: real Integrations, five destinations, eight families, Connections/Sources, zero AI calls");
}
async function openSettings(page: Page, ai: AISettingsHTTP) {
  await expect(entry(page), "Missing AI settings controls: add an explicit, initially closed entry to real Integrations.").toBeVisible();
  await expect(entry(page)).toHaveAttribute("aria-expanded", "false");
  ai.settingsOpen = true;
  await entry(page).click();
  await expect(entry(page)).toHaveAttribute("aria-expanded", "true");
  await expect(panel(page)).toBeVisible();
  await expect(panel(page).getByLabel(/operator encryption key|application encryption key/i)).toHaveCount(0);
  await expect(panel(page).getByRole("button", { name: /test connection|discover models|probe|inference|run model|start assessment|execute/i })).toHaveCount(0);
  ai.mark("AI settings explicitly opened");
}
async function visit(page: Page, ai: AISettingsHTTP) { await shell(page, ai); await openSettings(page, ai); }
async function closeSettings(page: Page, ai: AISettingsHTTP) {
  await panel(page).getByRole("button", { name: "Close AI settings", exact: true }).click();
  ai.settingsOpen = false;
  await expect(panel(page)).toHaveCount(0);
  await expect(entry(page)).toHaveAttribute("aria-expanded", "false");
  await expect(entry(page)).toBeFocused();
  await ai.assertPrivate(page, true);
}
async function addProfile(page: Page) {
  await profiles(page).getByRole("button", { name: "Add profile", exact: true }).click();
  await expect(profileForm(page)).toBeVisible();
  await expect(key(page)).toHaveAttribute("type", "password");
  await expect(key(page)).toHaveValue("");
  for (const name of ["Family", "Enabled", "Structured output reviewed"]) {
    await expect(profileForm(page).getByRole("combobox", { name, exact: true })).toHaveValue("");
  }
}
async function fillProfile(page: Page, input: ProfileInput) {
  const form = profileForm(page);
  await form.getByRole("textbox", { name: "Name", exact: true }).fill(input.name);
  await form.getByRole("combobox", { name: "Family", exact: true }).selectOption(input.family);
  await form.getByRole("textbox", { name: "Endpoint", exact: true }).fill(input.endpoint);
  await form.getByRole("textbox", { name: "Model", exact: true }).fill(input.model);
  const deployment = form.getByRole("textbox", { name: "Deployment", exact: true });
  if (input.family === "azure-foundry") await deployment.fill(input.deployment);
  else if (await deployment.count()) { await expect(deployment).toBeDisabled(); await expect(deployment).toHaveValue(""); }
  await key(page).fill(input.apiKey ?? "");
  await form.getByRole("combobox", { name: "Enabled", exact: true }).selectOption(String(input.enabled));
  await form.getByRole("combobox", { name: "Structured output reviewed", exact: true }).selectOption(String(input.structuredOutput));
}
async function editProfile(page: Page, profile: AIProfile) {
  await profileRow(page, profile.name).getByRole("button", { name: "Edit profile", exact: true, includeHidden: true }).click();
  await expect(profileForm(page)).toBeVisible();
  await expect(key(page)).toHaveAttribute("type", "password"); await expect(key(page)).toHaveValue("");
  await expect(profileForm(page).getByRole("textbox", { name: "Model", exact: true })).toHaveValue(profile.model);
}
function returnedProfile(call: AICall) {
  expect(call.response).toMatchObject({ apiVersion, profile: { id: expect.stringMatching(/^[a-f0-9]{32}$/), revision: expect.any(String) } });
  return call.response!.profile as AIProfile;
}
async function profileReceipt(page: Page, call: AICall) {
  const value = returnedProfile(call), row = profileRow(page, value.name);
  await expect(editor(page)).toHaveCount(0);
  await expect(row).toContainText(value.revision);
  await expect(row).toContainText(value.endpoint); await expect(row).toContainText(value.model);
  if (value.deployment) await expect(row).toContainText(value.deployment);
  await expect(row).toContainText(value.credentialConfigured ? /credential(?:\s+is)?\s*:?\s*configured\b/i : /credential(?:\s+is)?\s*:?\s*not configured\b/i);
  await expect(row.locator(`time[datetime="${value.updatedAt}"]`)).toBeVisible();
  await expect(row.getByText(/^(Verified|Healthy|Authorized|Connected|Allowed)$/i)).toHaveCount(0);
  return value;
}
async function saveProfile(page: Page, ai: AISettingsHTTP, body: Record<string, unknown>, prior?: AIProfile) {
  const held = ai.expectWrite(prior ? "PATCH" : "POST", prior ? profilePath(prior.id) : profilesPath, prior ? 200 : 201, true);
  await profileForm(page).getByRole("button", { name: prior ? "Save profile" : "Create profile", exact: true }).click();
  const call = await requested(held);
  expect(call.body).toEqual(body);
  if (prior) await expect(profileRow(page, prior.name)).toHaveCount(1);
  await release(page, held);
  const receipt = await profileReceipt(page, call);
  await ai.assertPrivate(page, true);
  return receipt;
}
async function rejectedEndpoint(page: Page, ai: AISettingsHTTP, endpoint: string) {
  await profileForm(page).getByRole("textbox", { name: "Endpoint", exact: true }).fill(endpoint);
  const failure = ai.expectWrite("POST", profilesPath, 400);
  await profileForm(page).getByRole("button", { name: "Create profile", exact: true }).click();
  await expect.poll(async () => await editor(page).getByRole("alert").isVisible() ||
    await profileForm(page).evaluate((form: HTMLFormElement) => !form.checkValidity()),
  "An invalid endpoint must fail locally or at the declared 400 boundary.").toBe(true);
  await frames(page);
  if (failure.call) expect(failure.call.status).toBe(400);
  else ai.discardUnsentWrite(failure);
  await expect(editor(page)).toBeVisible();
}
async function currentPolicy(page: Page, value: AIPolicy) {
  await expect(policyStatus(page)).toContainText(value.mode);
  await expect(policyStatus(page)).toContainText(value.revision);
}
async function savePolicy(page: Page, ai: AISettingsHTTP, mode: Mode, status: 200 | 403 | 503 = 200) {
  const before = ai.calls("PATCH", policyPath).length, confirmed = structuredClone(ai.policies.get(aiAlpha.id)!);
  await policy(page).getByRole("combobox", { name: "AI policy mode", exact: true }).selectOption(mode);
  expect(ai.calls("PATCH", policyPath)).toHaveLength(before);
  const held = ai.expectWrite("PATCH", policyPath, status, true);
  await policy(page).getByRole("button", { name: "Save policy", exact: true }).click();
  const call = await requested(held);
  expect(call.body).toEqual({ mode });
  await currentPolicy(page, confirmed);
  await release(page, held);
  if (call.status === 200) await currentPolicy(page, call.response!.policy as AIPolicy);
  else { await expect(policy(page).getByRole("alert")).toBeVisible(); await currentPolicy(page, confirmed); }
  return call;
}
async function addGrant(page: Page, selected = hostedProfile) {
  await grants(page).getByRole("button", { name: "Add grant", exact: true }).click();
  await expect(grantForm(page)).toBeVisible();
  await expect(grantForm(page).getByLabel("Expires at (UTC)", { exact: true })).toHaveValue("");
  await expect(confirmGrant(page)).toBeDisabled();
  await grantForm(page).getByRole("combobox", { name: "Profile", exact: true }).selectOption(selected.id);
}
async function consentFacts(page: Page, profile: AIProfile, selectedPolicy: AIPolicy) {
  const facts = consent(page);
  await expect(facts).toBeVisible();
  for (const text of [profile.name, profile.endpoint, profile.model, profile.revision, selectedPolicy.revision, "finding-validity", "finding-evidence"]) {
    await expect(facts).toContainText(text);
  }
  await expect(facts).toContainText(new RegExp(profile.family.replace("-", "[- ]"), "i"));
  if (profile.deployment) await expect(facts).toContainText(profile.deployment);
  await expect(facts).toContainText(/potentially sensitive/i);
  await expect(facts).toContainText(/credentials.*(?:excluded|not|never)|(?:exclude|never).*credentials/i);
  await expect(facts).toContainText(/snapshot|point.in.time/i);
  await expect(facts).toContainText(/no assessment|does not start.*assessment|not.*execution permission/i);
}
async function reviewGrant(page: Page, ai: AISettingsHTTP, profile = hostedProfile, selectedPolicy = ai.policies.get(aiAlpha.id)!) {
  const profileRead = ai.queueRead(profilePath(profile.id), 200, true);
  const policyRead = ai.queueRead(policyPath, 200, true);
  await grantForm(page).getByRole("button", { name: "Review current configuration", exact: true }).click();
  expect((await requested(profileRead)).response).toEqual({ apiVersion, profile });
  expect((await requested(policyRead)).response).toEqual({ apiVersion, policy: selectedPolicy });
  await expect(confirmGrant(page)).toBeDisabled();
  await release(page, profileRead); await release(page, policyRead);
  await consentFacts(page, profile, selectedPolicy);
  await expect(acknowledge(page)).not.toBeChecked();
}
async function consentExpiry(page: Page, value: string) {
  await grantForm(page).getByLabel("Expires at (UTC)", { exact: true }).fill(value.slice(0, 16));
}
function grantBody(call: AICall, profile: AIProfile, selectedPolicy: AIPolicy, expiresAt: string) {
  expect(Object.keys(call.body).sort()).toEqual([...grantFields].sort());
  expect(call.body).toEqual({
    profileId: profile.id, profileRevision: profile.revision, policyRevision: selectedPolicy.revision,
    destination: profile.endpoint, task: "finding-validity", dataClass: "finding-evidence", expiresAt: call.body.expiresAt,
  });
  expect(String(call.body.expiresAt)).toMatch(/Z$/);
  expect(Date.parse(String(call.body.expiresAt))).toBe(Date.parse(expiresAt));
}
async function createdGrant(page: Page, call: AICall) {
  expect(call.status).toBe(201);
  const grant = call.response!.grant as AIGrant;
  expect(call.response!.apiVersion).toBe(apiVersion);
  await expect(approval(page)).toHaveCount(0);
  await expect(grantRow(page, grant.id)).toContainText("Matches current configuration");
  await expect(grantRow(page, grant.id).locator(`time[datetime="${grant.expiresAt}"]`)).toBeVisible();
  await expect(grantRow(page, grant.id).getByText(/^(Allowed|Authorized|Verified|Healthy)$/i)).toHaveCount(0);
  return grant;
}
async function noOldScope(page: Page, ai: AISettingsHTTP, anonymous = false) {
  await expect(panel(page)).toHaveCount(0); await expect(editor(page)).toHaveCount(0); await expect(approval(page)).toHaveCount(0);
  for (const text of [hostedProfile.name, inertProfile.name, refreshedProfile.name, betaProfile.name]) {
    await expect(page.locator("body")).not.toContainText(text);
  }
  if (anonymous) await expect(page.getByRole("form", { name: "Sign in", exact: true })).toBeVisible();
  await ai.assertPrivate(page, true);
}
async function noMotionOrOverflow(page: Page) {
  const observed = await page.evaluate(async () => {
    const motion = new Set<string>();
    for (let sample = 0; sample < 6; sample++) {
      for (const animation of document.getAnimations()) {
        if (animation.playState !== "running" || !(animation.effect instanceof KeyframeEffect)) continue;
        if (animation.effect.getTiming().iterations === Infinity) motion.add("continuous");
        const frames = animation.effect.getKeyframes() as Array<Record<string, unknown>>;
        for (const key of ["transform", "translate", "scale", "rotate", "left", "top"]) {
          if (new Set(frames.map((frame) => frame[key]).filter((value) => value !== undefined).map(String)).size > 1) motion.add(key);
        }
      }
      await new Promise<void>((resolve) => requestAnimationFrame(() => resolve()));
    }
    return { motion: [...motion], overflow: document.documentElement.scrollWidth - innerWidth, reduced: matchMedia("(prefers-reduced-motion: reduce)").matches };
  });
  expect(observed.reduced).toBe(true); expect(observed.motion).toEqual([]); expect(observed.overflow).toBeLessThanOrEqual(1);
}

test("AIU1 Opt-in setup distinguishes fetched default, loading, failure, keyless local and readonly roles", async ({ page, ai }) => {
  ai.seedProfiles(aiAlpha.id, []); ai.encryptionAvailable = false;
  const failedProfiles = ai.queueRead(profilesPath, 503, true), failedPolicy = ai.queueRead(policyPath, 503, true);
  await visit(page, ai);
  await requested(failedProfiles); await requested(failedPolicy);
  await expect(profiles(page).getByRole("status").filter({ hasText: /loading.*profiles/i })).toBeVisible();
  await expect(profiles(page).getByRole("heading", { name: "No AI profiles", exact: true })).toHaveCount(0);
  await expect(policyStatus(page)).toHaveCount(0);
  await release(page, failedProfiles); await release(page, failedPolicy);
  await expect(profiles(page).getByRole("alert")).toBeVisible(); await expect(policy(page).getByRole("alert")).toBeVisible();
  await expect(policyStatus(page)).toHaveCount(0);
  await expect(profiles(page).getByRole("heading", { name: "No AI profiles", exact: true })).toHaveCount(0);
  const recoveredProfiles = ai.queueRead(profilesPath), recoveredPolicy = ai.queueRead(policyPath);
  await profiles(page).getByRole("button", { name: "Retry profiles", exact: true }).click();
  await policy(page).getByRole("button", { name: "Retry policy", exact: true }).click();
  await requested(recoveredProfiles);
  expect((await requested(recoveredPolicy)).response).toEqual({ apiVersion, policy: defaultPolicy() });
  await expect(profiles(page).getByRole("heading", { name: "No AI profiles", exact: true })).toBeVisible();
  await expect(grants(page).getByRole("heading", { name: "No AI grants", exact: true })).toBeVisible();
  await currentPolicy(page, defaultPolicy());
  await expect(panel(page)).toContainText(/setup only|configuration only/i);
  await expect(panel(page)).toContainText(/no assessment|does not start.*assessment/i);
  expect(writeCalls(ai)).toEqual([]);
  ai.mark("AIU1 fetched default/error/empty/retry assertions completed");

  const input = createInput("local");
  await addProfile(page); await fillProfile(page, input);
  const accepted = ai.expectWrite("POST", profilesPath, 201, true);
  await profileForm(page).getByRole("button", { name: "Create profile", exact: true }).click();
  const call = await requested(accepted);
  expect(call.body).toEqual({ ...input, ...(call.body.apiKey === null ? { apiKey: null } : {}) });
  expect(call.status).toBe(201);
  await expect(profileRow(page, input.name)).toHaveCount(0);
  await release(page, accepted);
  expect((await profileReceipt(page, call)).credentialConfigured).toBe(false);
  await closeSettings(page, ai);
  for (const [selected, profile] of [[aiBeta, betaProfile], [aiGamma, gammaProfile]] as const) {
    const before = ai.aiCalls().length;
    await workspace(page).selectOption(selected.id); await noOldScope(page, ai);
    await frames(page); expect(ai.aiCalls()).toHaveLength(before);
    await openSettings(page, ai); await expect(profileRow(page, profile.name)).toBeVisible();
    await expect(panel(page)).toContainText(/read.only|only.*administrators|administrator.*required/i);
    await noEnabledActions(panel(page).getByRole("button", { name: /^(Add profile|Edit profile|Save policy|Add grant|Revoke grant)$/ }));
    await expect(profileForm(page)).toHaveCount(0); await expect(grantForm(page)).toHaveCount(0);
    await closeSettings(page, ai);
  }
  expect(writeCalls(ai)).toEqual([call]);
  ai.mark("AIU1 keyless local without encryption and analyst/viewer readonly completed");
});

test("AIU2 Four explicit families preserve models, endpoints, sparse keys and authoritative receipts", async ({ page, ai }) => {
  ai.seedProfiles(aiAlpha.id, []);
  await visit(page, ai);
  const created = new Map<string, AIProfile>();
  for (const family of families) {
    const input = createInput(family, draftKey);
    await addProfile(page); await fillProfile(page, input);
    await expect(editor(page).getByLabel(/encryption key|approvalRef|actor|grant ID/i)).toHaveCount(0);
    await expect(editor(page).getByRole("button", { name: /test connection|discover|probe|infer|assess|verify model/i })).toHaveCount(0);
    await ai.assertPrivate(page);
    if (family === "openai" || family === "local") {
      await rejectedEndpoint(page, ai, family === "local" ? "http://localhost:8899/v1" : "http://127.0.0.1:8899/v1");
      await profileForm(page).getByRole("textbox", { name: "Endpoint", exact: true }).fill(input.endpoint);
      await key(page).fill(draftKey);
    }
    if (family === "openai") {
      ai.encryptionAvailable = false;
      const unavailable = ai.expectWrite("POST", profilesPath, 201, true);
      await profileForm(page).getByRole("button", { name: "Create profile", exact: true }).click();
      expect((await requested(unavailable)).status).toBe(503);
      await release(page, unavailable);
      await expect(editor(page).getByRole("alert")).toContainText(/unavailable|operator|server/i);
      await expect(profileRow(page, input.name)).toHaveCount(0);
      await expect(editor(page).getByLabel(/encryption key/i)).toHaveCount(0);
      await ai.assertPrivate(page);
      ai.encryptionAvailable = true; await key(page).fill(draftKey);
    }
    created.set(family, await saveProfile(page, ai, { ...input }));
  }
  ai.mark("AIU2 four family creation, endpoint rejection and hosted-key 503 completed");

  let hosted = created.get("openai")!;
  await editProfile(page, hosted);
  await expect(profileForm(page).getByRole("checkbox", { name: "Clear stored API key", exact: true })).toHaveCount(0);
  const renamed = "Synthetic intentionally renamed profile";
  await profileForm(page).getByRole("textbox", { name: "Name", exact: true }).fill(renamed);
  hosted = await saveProfile(page, ai, { name: renamed }, hosted);
  expect(hosted.credentialConfigured).toBe(true);
  await editProfile(page, hosted);
  await profileForm(page).getByRole("combobox", { name: "Enabled", exact: true }).selectOption("false");
  await profileForm(page).getByRole("combobox", { name: "Structured output reviewed", exact: true }).selectOption("false");
  hosted = await saveProfile(page, ai, { enabled: false, structuredOutput: false }, hosted);
  await editProfile(page, hosted); await key(page).fill(replacementKey);
  const refused = ai.expectWrite("PATCH", profilePath(hosted.id), 503, true, `Synthetic service diagnostic must not echo ${replacementKey}`);
  await profileForm(page).getByRole("button", { name: "Save profile", exact: true }).click();
  expect((await requested(refused)).body).toEqual({ apiKey: replacementKey });
  await release(page, refused); await expect(editor(page).getByRole("alert")).toBeVisible();
  expect(ai.profiles.get(hosted.id)).toEqual(hosted); await ai.assertPrivate(page);
  await key(page).fill(replacementKey);
  const replacement = await saveProfile(page, ai, { apiKey: replacementKey }, hosted);
  expect(replacement.revision).not.toBe(hosted.revision);
  const local = created.get("local")!;
  await editProfile(page, local); await key(page).fill(replacementKey);
  await profileForm(page).getByRole("checkbox", { name: "Clear stored API key", exact: true }).check();
  await expect(key(page)).toHaveValue(""); await expect(key(page)).toBeDisabled();
  const cleared = await saveProfile(page, ai, { apiKey: null }, local);
  expect(cleared.credentialConfigured).toBe(false); expect(cleared.revision).not.toBe(local.revision);
  expect(ai.requests.filter((call) => call.method === "DELETE")).toEqual([]);
  ai.mark("AIU2 sparse rename/booleans/retain/replacement/error-redaction/local-null receipts completed");
});

test("AIU3 Explicit policy and sensitive finite grants retain workspace decisions, expiry and revoke receipts", async ({ page, ai }) => {
  await visit(page, ai); await currentPolicy(page, defaultPolicy());
  await savePolicy(page, ai, "local-only");
  await expect(policy(page)).toContainText(/family.*local|local.*family/i);
  await expect(policy(page)).toContainText(/hosted.*(?:loopback|local)|loopback.*hosted/i);
  await noEnabledActions(grants(page).getByRole("button", { name: "Add grant", exact: true }));
  await savePolicy(page, ai, "disabled", 503);
  expect(ai.policies.get(aiAlpha.id)!.mode).toBe("local-only");
  const modeReceipt = await savePolicy(page, ai, "approved-hosted");
  const selectedPolicy = modeReceipt.response!.policy as AIPolicy;
  expect(ai.grants.size).toBe(0);
  await expect(panel(page).getByText(/^(Allowed|Authorized|Ready to run|Execution permitted)$/i)).toHaveCount(0);
  await addGrant(page);
  for (const profile of [disabledProfile, unreviewedProfile]) {
    await expectIneligibleOption(grantForm(page).getByRole("combobox", { name: "Profile", exact: true }).getByRole("option").filter({ hasText: profile.name }));
  }
  expect(ai.calls("POST", grantsPath)).toEqual([]);
  await reviewGrant(page, ai);
  await acknowledge(page).check(); await expect(confirmGrant(page)).toBeDisabled();
  await consentExpiry(page, expiry(-60)); await expect(confirmGrant(page)).toBeDisabled();
  const expiresAt = expiry();
  await consentExpiry(page, expiresAt); await expect(confirmGrant(page)).toBeEnabled();
  expect(ai.calls("POST", grantsPath)).toEqual([]);
  const accepted = ai.expectWrite("POST", grantsPath, 201, true);
  await confirmGrant(page).click();
  const call = await requested(accepted); grantBody(call, hostedProfile, selectedPolicy, expiresAt);
  const pending = call.response!.grant as AIGrant;
  await expect(grantRow(page, pending.id)).toHaveCount(0);
  await release(page, accepted);
  const created = await createdGrant(page, call);
  ai.mark("AIU3 policy receipts, failed disabled save and exact sensitive finite grant completed");

  const expired = grantFor(hostedProfile, selectedPolicy, 99, expiry(-60));
  ai.seedGrant(expired);
  await grants(page).getByRole("button", { name: "Refresh grants", exact: true }).click();
  await expect(grantRow(page, expired.id)).toContainText("Expired");
  await expect(grantRow(page, expired.id)).not.toContainText("Matches current configuration");
  ai.roles.set(aiAlpha.id, "analyst"); ai.serverRoles.set(aiAlpha.id, "analyst"); ai.settingsOpen = false;
  await page.reload(); await openSettings(page, ai);
  await expect(grantRow(page, created.id)).toContainText("Matches current configuration");
  await expect(grantRow(page, created.id)).not.toContainText("Revoked");
  await noEnabledActions(grantRow(page, created.id).getByRole("button", { name: "Revoke grant", exact: true }));
  expect(ai.grants.get(created.id)).toEqual(created);
  ai.roles.set(aiAlpha.id, "admin"); ai.serverRoles.set(aiAlpha.id, "admin"); ai.settingsOpen = false;
  await page.reload(); await openSettings(page, ai);
  const revoke = page.getByRole("dialog", { name: "Revoke AI grant", exact: true });
  await grantRow(page, created.id).getByRole("button", { name: "Revoke grant", exact: true }).click();
  await expect(revoke).toContainText(created.id); await expect(revoke).toContainText(created.destination);
  await page.keyboard.press("Escape"); expect(ai.calls("POST", revokePath(created.id))).toEqual([]);
  await grantRow(page, created.id).getByRole("button", { name: "Revoke grant", exact: true }).click();
  const revoked = ai.expectWrite("POST", revokePath(created.id), 200, true);
  await revoke.getByRole("button", { name: "Confirm revocation", exact: true }).click();
  const receipt = await requested(revoked); expect(receipt.body).toEqual({});
  await expect(grantRow(page, created.id)).not.toContainText("Revoked");
  await release(page, revoked);
  const canonical = receipt.response!.grant as AIGrant;
  expect(canonical).toMatchObject({ ...created, revokedAt: expect.any(String), revokedBy: aiUser.id });
  await expect(grantRow(page, created.id)).toContainText("Revoked");
  await expect(grantRow(page, created.id)).not.toContainText("Matches current configuration");
  await expect(grantRow(page, created.id).locator(`time[datetime="${canonical.revokedAt}"]`)).toBeVisible();
  expect(ai.grants.size).toBe(2);
  ai.mark("AIU3 expired history, issuer demotion without revocation and canonical revoke completed");
});

test("AIU4 Authorized current facts and opaque revisions invalidate stale consent without automatic conflict resubmission", async ({ page, ai }) => {
  const initialPolicy = configuredPolicy();
  ai.policies.set(aiAlpha.id, initialPolicy);
  const historical = grantFor(hostedProfile, initialPolicy);
  ai.seedGrant(historical);
  await visit(page, ai); await addGrant(page); await reviewGrant(page, ai);
  await consentExpiry(page, expiry()); await acknowledge(page).check(); await expect(confirmGrant(page)).toBeEnabled();
  const refreshedPolicy: AIPolicy = { ...initialPolicy, revision: "policy-A/opaque.2", updatedAt: changedAt };
  ai.seedProfile(refreshedProfile); ai.policies.set(aiAlpha.id, refreshedPolicy);
  const freshProfiles = ai.queueRead(profilesPath, 200, true), freshPolicy = ai.queueRead(policyPath, 200, true);
  await profiles(page).getByRole("button", { name: "Refresh profiles", exact: true }).click();
  await policy(page).getByRole("button", { name: "Refresh policy", exact: true }).click();
  await requested(freshProfiles); await requested(freshPolicy);
  expect(ai.calls("POST", grantsPath)).toEqual([]);
  await release(page, freshProfiles); await release(page, freshPolicy);
  await expect(profileRow(page, refreshedProfile.name)).toContainText(refreshedProfile.revision);
  await currentPolicy(page, refreshedPolicy);
  await expect(confirmGrant(page)).toBeDisabled(); await expect(acknowledge(page)).not.toBeChecked();
  if (await consent(page).isVisible() && !(await consent(page).textContent())?.includes(hostedProfile.endpoint)) {
    await consentFacts(page, refreshedProfile, refreshedPolicy);
  } else await expect(approval(page).getByRole("alert")).toContainText(/changed|review.*again|stale/i);
  await expect(grantRow(page, historical.id)).toContainText("Configuration changed");
  await expect(grantRow(page, historical.id)).not.toContainText("Matches current configuration");
  await reviewGrant(page, ai, refreshedProfile, refreshedPolicy);
  const expiresAt = expiry();
  await consentExpiry(page, expiresAt); await acknowledge(page).check();
  ai.mark("AIU4 authorized refresh reconciled current facts and invalidated old opaque bindings");

  const laterProfile: AIProfile = { ...refreshedProfile, endpoint: refreshedProfile.endpoint.slice(0, -1), revision: "profile:another/opaque", updatedAt: changedAt };
  const laterPolicy: AIPolicy = { ...refreshedPolicy, revision: "policy:another/opaque" };
  ai.seedProfile(laterProfile); ai.policies.set(aiAlpha.id, laterPolicy);
  const conflict = ai.expectWrite("POST", grantsPath, 201, true);
  await confirmGrant(page).click();
  const staleCall = await requested(conflict);
  grantBody(staleCall, refreshedProfile, refreshedPolicy, expiresAt);
  expect(staleCall.status, "The HTTP fixture checks exact stored bindings, including the trailing slash.").toBe(409);
  await release(page, conflict);
  await expect(approval(page).getByRole("alert")).toContainText(/conflict|changed|review/i);
  await expect(confirmGrant(page)).toBeDisabled(); await expect(acknowledge(page)).not.toBeChecked();
  await frames(page); expect(ai.calls("POST", grantsPath)).toHaveLength(1);
  expect(ai.grants.size).toBe(1);
  await reviewGrant(page, ai, laterProfile, laterPolicy);
  await consentExpiry(page, expiresAt); await acknowledge(page).check();
  const accepted = ai.expectWrite("POST", grantsPath, 201, true);
  await confirmGrant(page).click();
  const reviewed = await requested(accepted); grantBody(reviewed, laterProfile, laterPolicy, expiresAt);
  await release(page, accepted); await createdGrant(page, reviewed);
  expect(ai.calls("POST", grantsPath)).toEqual([staleCall, reviewed]);
  expect(ai.grants.get(historical.id)).toEqual(historical);
  await expect(grantRow(page, historical.id)).not.toContainText("Matches current configuration");
  ai.mark("AIU4 409 made no guessed resubmission; fresh explicit review and exact new grant completed");
});

test("AIU5 Native manual pages and denial retries preserve confirmed data without stale authority", async ({ page, ai }) => {
  const values = profileSeries(), selectedPolicy = configuredPolicy();
  ai.seedProfiles(aiAlpha.id, values); ai.policies.set(aiAlpha.id, selectedPolicy);
  const history = grantSeries(values[0], selectedPolicy);
  for (const grant of history) ai.seedGrant(grant);
  await visit(page, ai);
  await expect(profileRow(page, values[0].name)).toBeVisible();
  await expect(grantRow(page, history[0].id)).toBeVisible();
  const firstProfiles = ai.pages.find((value) => value.call.path === profilesPath)!;
  const firstGrants = ai.pages.find((value) => value.call.path === grantsPath)!;
  expect(firstProfiles.response.items).toHaveLength(100); expect(firstGrants.response.items).toHaveLength(100);
  const cursor = firstProfiles.response.nextCursor!, grantCursor = firstGrants.response.nextCursor!;
  expect(cursor).toBe(values[99].id); expect(grantCursor).toBe(history[99].id);
  await frames(page);
  expect(ai.calls("GET", profilesPath).filter((call) => call.query.cursor)).toEqual([]);
  expect(ai.calls("GET", grantsPath).filter((call) => call.query.cursor)).toEqual([]);
  await addGrant(page, values[0]); await reviewGrant(page, ai, values[0], selectedPolicy);
  const unavailable = ai.queueRead(profilesPath, 503, true, aiAlpha.id, cursor);
  await profiles(page).getByRole("button", { name: "Load more profiles", exact: true }).click();
  const failed = await requested(unavailable);
  expect(failed.query).toEqual({ ...firstProfiles.call.query, cursor });
  await expect(profileRow(page, values[0].name)).toBeVisible();
  await release(page, unavailable); await expect(profiles(page).getByRole("alert")).toBeVisible();
  await expect(profileRow(page, values[0].name)).toBeVisible();
  await expect(profiles(page).getByRole("heading", { name: "No AI profiles", exact: true })).toHaveCount(0);
  const recovered = ai.queueRead(profilesPath, 200, false, aiAlpha.id, cursor);
  await profiles(page).getByRole("button", { name: "Retry profiles", exact: true }).click();
  expect((await requested(recovered)).query).toEqual(failed.query);
  await expect(profileRow(page, values[100].name)).toHaveCount(1);
  await consentFacts(page, values[0], selectedPolicy);
  await expect(grantForm(page).getByRole("combobox", { name: "Profile", exact: true })).toHaveValue(values[0].id);
  await grantForm(page).getByRole("button", { name: "Cancel grant", exact: true }).click();
  const moreGrants = ai.queueRead(grantsPath, 200, false, aiAlpha.id, grantCursor);
  await grants(page).getByRole("button", { name: "Load more grants", exact: true }).click();
  expect((await requested(moreGrants)).query).toEqual({ ...firstGrants.call.query, cursor: grantCursor });
  await expect(grantRow(page, history[100].id)).toHaveCount(1);
  ai.mark("AIU5 native 100+1 manual pages, same-cursor retry and selected-profile absence safety completed");

  const forbiddenProfile = ai.queueRead(profilePath(values[100].id), 403, true);
  await profileRow(page, values[100].name).getByRole("button", { name: "Edit profile", exact: true }).click();
  await requested(forbiddenProfile); await release(page, forbiddenProfile);
  await expect(editor(page).getByRole("alert")).toBeVisible();
  await expect(profileForm(page)).toHaveCount(0); await expect(editor(page)).not.toContainText(values[100].endpoint);
  const unavailableProfile = ai.queueRead(profilePath(values[100].id), 503, true);
  await editor(page).getByRole("button", { name: "Retry profile", exact: true }).click();
  await requested(unavailableProfile); await release(page, unavailableProfile);
  await expect(editor(page).getByRole("alert")).toBeVisible(); await expect(profileForm(page)).toHaveCount(0);
  const authorizedProfile = ai.queueRead(profilePath(values[100].id), 200, true);
  await editor(page).getByRole("button", { name: "Retry profile", exact: true }).click();
  await requested(authorizedProfile); await release(page, authorizedProfile);
  await expect(profileForm(page).getByRole("textbox", { name: "Endpoint", exact: true })).toHaveValue(values[100].endpoint);
  await page.keyboard.press("Escape");
  const lastGrant = history[100];
  await grantRow(page, lastGrant.id).getByRole("button", { name: "View grant", exact: true }).click();
  await expect(grantDetails(page)).toContainText(lastGrant.destination);
  for (const status of [404, 503, 200] as const) {
    const pending = ai.queueRead(grantPath(lastGrant.id), status, true);
    await grantDetails(page).getByRole("button", { name: status === 404 ? "Refresh grant" : "Retry grant", exact: true }).click();
    await requested(pending); await release(page, pending);
    if (status === 200) await expect(grantDetails(page)).toContainText(lastGrant.destination);
    else {
      await expect(grantDetails(page).getByRole("alert")).toBeVisible();
      await expect(grantDetails(page)).not.toContainText(lastGrant.destination);
      await expect(grantDetails(page)).not.toContainText(lastGrant.profileRevision);
    }
  }
  for (const status of [403, 503, 200] as const) {
    const pending = ai.queueRead(profilesPath, status, true, aiAlpha.id, "");
    await profiles(page).getByRole("button", { name: status === 403 ? "Refresh profiles" : "Retry profiles", exact: true }).click();
    await requested(pending); await release(page, pending);
    if (status === 200) await expect(profileRow(page, values[0].name)).toBeVisible();
    else {
      await expect(profiles(page).getByRole("alert")).toBeVisible();
      await expect(profiles(page).getByRole("table", { name: "AI profiles", exact: true })).toHaveCount(0);
      await expect(profiles(page).getByRole("heading", { name: "No AI profiles", exact: true })).toHaveCount(0);
    }
  }
  ai.serverRoles.set(aiAlpha.id, "analyst");
  const denied = await savePolicy(page, ai, "disabled");
  expect(denied.status).toBe(403); expect(ai.policies.get(aiAlpha.id)).toEqual(selectedPolicy);
  await expect(policy(page).getByRole("alert")).toContainText(/denied|permission|not permitted|administrator/i);
  expect(ai.calls("POST", grantsPath)).toEqual([]);
  ai.mark("AIU5 detail/list denials survived 503 retries; stale admin policy write respected server 403");
});

test("AIU6 Mobile reduced-motion focus and privacy survive close, workspace, 401 and logout epochs", async ({ page, ai }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  ai.seedProfiles(aiAlpha.id, [hostedProfile, inertProfile]);
  ai.policies.set(aiAlpha.id, configuredPolicy());
  await visit(page, ai);
  const inert = profileRow(page, inertProfile.name);
  await expect(inert.getByText(inertProfile.name, { exact: true })).toBeVisible();
  await expect(inert.getByText(inertProfile.model, { exact: true })).toBeVisible();
  await expect(inert.locator("img,iframe,script")).toHaveCount(0);
  await expect(inert.getByRole("link", { name: inertProfile.endpoint, exact: true })).toHaveCount(0);
  await noMotionOrOverflow(page);
  const trigger = profiles(page).getByRole("button", { name: "Add profile", exact: true });
  await addProfile(page); await fillProfile(page, createInput("openai"));
  await noMotionOrOverflow(page);
  await editor(page).getByRole("button", { name: "Close create ai profile", exact: true }).focus();
  await page.keyboard.press("Shift+Tab");
  expect(await editor(page).evaluate((element) => element.contains(document.activeElement))).toBe(true);
  await page.keyboard.press("Tab");
  await expect(editor(page).getByRole("button", { name: "Close create ai profile", exact: true })).toBeFocused();
  await ai.assertPrivate(page); await page.keyboard.press("Escape");
  await expect(trigger).toBeFocused(); await ai.assertPrivate(page, true);
  await addProfile(page); await expect(key(page)).toHaveValue(""); await page.keyboard.press("Escape");
  await addGrant(page); await reviewGrant(page, ai); await consentExpiry(page, expiry()); await acknowledge(page).check();
  const closedRead = ai.queueRead(profilesPath, 200, true);
  await profiles(page).getByRole("button", { name: "Refresh profiles", exact: true }).click(); await requested(closedRead);
  await closeSettings(page, ai); await release(page, closedRead, true);
  await noOldScope(page, ai);
  ai.mark("AIU6 390px, reduced motion, inert text, dialog focus/secret clear and closed-settings abort completed");

  ai.queueRead(profilesPath);
  await openSettings(page, ai); await addGrant(page); await reviewGrant(page, ai);
  await consentExpiry(page, expiry()); await acknowledge(page).check();
  const oldScope = ai.queueRead(profilesPath, 200, true);
  await profiles(page).getByRole("button", { name: "Refresh profiles", exact: true }).click(); await requested(oldScope);
  const beforeSwitch = ai.aiCalls().length;
  ai.settingsOpen = false;
  await workspace(page).selectOption(aiBeta.id); await noOldScope(page, ai);
  await release(page, oldScope, true); await frames(page);
  expect(ai.aiCalls()).toHaveLength(beforeSwitch);
  await openSettings(page, ai); await expect(profileRow(page, betaProfile.name)).toBeVisible();
  await expect(panel(page)).not.toContainText(inertProfile.name);
  ai.settingsOpen = false; await workspace(page).selectOption(aiAlpha.id); await noOldScope(page, ai);
  ai.queueRead(profilesPath); await openSettings(page, ai);
  const pendingProfiles = ai.queueRead(profilesPath, 200, true), unauthorized = ai.queueRead(policyPath, 401, true);
  await profiles(page).getByRole("button", { name: "Refresh profiles", exact: true }).click(); await requested(pendingProfiles);
  await policy(page).getByRole("button", { name: "Refresh policy", exact: true }).click(); await requested(unauthorized);
  await addProfile(page); await fillProfile(page, createInput("openai")); await ai.assertPrivate(page);
  await release(page, unauthorized); await noOldScope(page, ai, true);
  await release(page, pendingProfiles, true); await noOldScope(page, ai, true);
  ai.mark("AIU6 workspace and 401 cleared grant/profile/secret scope and aborted old reads");

  ai.queueRead(profilesPath); ai.queueRead(policyPath);
  const login = page.getByRole("form", { name: "Sign in", exact: true });
  await login.getByRole("textbox", { name: "Email", exact: true }).fill(aiUser.email);
  await login.getByLabel("Password", { exact: true }).fill(aiPassword);
  await login.getByRole("button", { name: "Sign in", exact: true }).click();
  await openSettings(page, ai); await addGrant(page); await reviewGrant(page, ai);
  await consentExpiry(page, expiry()); await acknowledge(page).check();
  const pendingGrants = ai.queueRead(grantsPath, 200, true);
  await grants(page).getByRole("button", { name: "Refresh grants", exact: true }).click(); await requested(pendingGrants);
  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  await noOldScope(page, ai, true); await release(page, pendingGrants, true);
  await noOldScope(page, ai, true); await noMotionOrOverflow(page);
  expect(ai.calls("POST", "/api/v1/logout")).toHaveLength(1);
  expect(writeCalls(ai)).toEqual([]);
  ai.mark("AIU6 actual sign-in/sign-out HTTP, cleared consent and logout abort completed");
});
