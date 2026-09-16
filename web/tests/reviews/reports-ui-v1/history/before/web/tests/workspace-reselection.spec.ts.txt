import { alpha, detailedFinding, expect, test } from "./application-fixture";
import { requireProductionUI } from "./network";

test.use({ reducedMotion: "reduce" });
test.beforeEach(async ({ app }) => {
  requireProductionUI();
  expect(app.requests).toEqual([]);
});

test.afterEach(async ({ page, app }) => {
  await expect(page.getByRole("combobox", { name: "Workspace", exact: true })).toHaveValue(alpha.id);
  await expect(page.getByRole("complementary").getByText(alpha.role, { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Sign out", exact: true })).toBeVisible();
  await expect(page.getByRole("form", { name: "Sign in", exact: true, includeHidden: true })).toHaveCount(0);
  expect(app.requests.filter((call) => call.method !== "GET"), "Reselection must not sign in, sign out or change authority.").toEqual([]);
  const protectedReads = app.requests.filter((call) => call.path !== "/api/v1/session");
  expect(protectedReads.length).toBeGreaterThan(0);
  expect(protectedReads.every((call) => call.workspace === alpha.id), "All protected reads must retain the authenticated Alpha scope.").toBe(true);
});

test("reselecting the active workspace preserves a pending authenticated Findings read", async ({ page, app }) => {
  app.holdWork(alpha.id);
  await page.goto("/#/work");
  await expect.poll(() => app.calls("GET", "/api/v1/work").some((call) => call.workspace === alpha.id)).toBe(true);
  const workspace = page.getByRole("combobox", { name: "Workspace", exact: true });
  const loading = page.getByRole("status").filter({ hasText: /loading findings/i });
  await expect(workspace).toHaveValue(alpha.id);
  await expect(loading).toBeVisible();
  const selectedMembership = await workspace.locator("option:checked").innerText();

  await workspace.selectOption(alpha.id);
  await expect(workspace).toHaveValue(alpha.id);
  await expect(workspace.locator("option:checked")).toHaveText(selectedMembership);
  app.releaseWork();

  const findings = page.getByRole("table", { name: "Findings", exact: true });
  await expect(findings, "Releasing the held response after same-scope reselection must complete loading without refresh or navigation.").toBeVisible();
  await expect(findings.getByRole("row").filter({ hasText: detailedFinding.title })).toBeVisible();
  await expect(loading).toHaveCount(0);
});

test("reselecting the loaded active workspace preserves its filter and selected finding", async ({ page, app }) => {
  await page.goto("/#/work");
  const findings = page.getByRole("table", { name: "Findings", exact: true });
  await expect(findings).toBeVisible();
  const workspace = page.getByRole("combobox", { name: "Workspace", exact: true });
  await expect(workspace).toHaveValue(alpha.id);
  const selectedMembership = await workspace.locator("option:checked").innerText();
  const filter = page.getByRole("textbox", { name: "Filter findings", exact: true });
  await filter.fill(detailedFinding.title);
  await expect(findings.getByRole("row").filter({ has: page.getByRole("cell") })).toHaveCount(1);
  const selected = findings.getByRole("checkbox", { name: `Select ${detailedFinding.title}`, exact: true });
  await selected.check();
  await expect(selected).toBeChecked();

  await workspace.selectOption(alpha.id);

  await expect(findings).toBeVisible();
  await expect(workspace).toHaveValue(alpha.id);
  await expect(workspace.locator("option:checked")).toHaveText(selectedMembership);
  await expect(filter).toHaveValue(detailedFinding.title);
  await expect(selected).toBeChecked();
  await expect(page.getByRole("status").filter({ hasText: /loading findings/i })).toHaveCount(0);
  expect(app.calls("GET", "/api/v1/work").every((call) => call.workspace === alpha.id)).toBe(true);
});
