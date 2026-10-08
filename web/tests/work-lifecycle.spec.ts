import type { Route } from "@playwright/test";
import { expect, test } from "./application-fixture";
import { apiVersion } from "./api-contract";

test.use({ reducedMotion: "reduce", timezoneId: "UTC" });

const changedId = "91000000000000000000000000000001";
const unchangedId = "92000000000000000000000000000002";
const reopenedId = "93000000000000000000000000000003";

function item(id: string, title: string, changeKind: "new" | "changed" | "unchanged" | "reopened") {
  return {
    id, title, assetName: "Lifecycle repository", severity: "medium", ownerName: null,
    workflowState: "open", sourceScanAt: "2026-10-07T18:00:00Z",
    collectedAt: "2026-10-07T18:01:00Z", importedAt: "2026-10-07T18:02:00Z",
    changeKind, changeAt: "2026-10-07T18:02:00Z",
    decisionRevision: 1, disposition: "none",
    acceptedRiskExpiresAt: null, riskAcceptanceExpired: false,
  };
}

const allItems = [
  item(changedId, "Changed lifecycle issue", "changed"),
  item(unchangedId, "Unchanged lifecycle issue", "unchanged"),
  item(reopenedId, "Reopened lifecycle issue", "reopened"),
];
const meaningfulItems = [allItems[0], allItems[2]];

test("M06L1 Meaningful Work refresh preserves confirmed rows, selection and moved focus", async ({ page, app }) => {
  expect(app.requests).toEqual([]);
  let release!: () => void;
  const held = new Promise<void>((resolve) => { release = resolve; });
  let meaningfulRequested!: () => void;
  const requested = new Promise<void>((resolve) => { meaningfulRequested = resolve; });
  const reads: string[] = [];
  await page.route("**/api/v1/work**", async (route: Route) => {
    const url = new URL(route.request().url());
    reads.push(url.search);
    const meaningful = url.searchParams.get("change") === "meaningful";
    if (meaningful) {
      meaningfulRequested();
      await held;
    }
    const items = meaningful ? meaningfulItems : allItems;
    await route.fulfill({ status: 200, json: {
      apiVersion, dataOrigin: "synthetic", items, total: items.length, nextCursor: null,
      changeMode: meaningful ? "meaningful" : "all",
    } });
  });

  await page.goto("/#/work");
  const table = page.getByRole("table", { name: "Findings", exact: true });
  await expect(table.getByRole("row")).toHaveCount(4);
  await expect(table.getByText("changed", { exact: true })).toBeVisible();
  await expect(table.getByText("unchanged", { exact: true })).toBeVisible();
  await expect(table.getByText("reopened", { exact: true })).toBeVisible();
  await page.getByRole("checkbox", { name: "Select Reopened lifecycle issue", exact: true }).check();

  const toggle = page.getByRole("button", { name: "Meaningful changes only", exact: true });
  await toggle.focus();
  await toggle.click();
  await requested;
  await expect(toggle).toHaveAttribute("aria-pressed", "true");
  await expect(page.getByRole("status").filter({ hasText: "Refreshing findings." })).toBeVisible();
  await expect(table.getByRole("row")).toHaveCount(4);
  await expect(page.getByRole("checkbox", { name: "Select Reopened lifecycle issue", exact: true })).toBeChecked();

  const filter = page.getByRole("textbox", { name: "Filter findings", exact: true });
  await filter.focus();
  release();
  await expect(table.getByRole("row")).toHaveCount(3);
  await expect(table.getByText("Unchanged lifecycle issue", { exact: true })).toHaveCount(0);
  await expect(page.getByRole("checkbox", { name: "Select Reopened lifecycle issue", exact: true })).toBeChecked();
  await expect(page.getByRole("status", { name: "Workspace search", exact: true }))
    .toContainText("Showing only new, changed, or reopened");
  await expect(filter).toBeFocused();
  const selection = page.locator(".selection-toolbar");
  await expect(selection).toBeVisible();
  await expect(selection.locator(".selection-count")).toHaveText("1");
  expect(reads.some((query) => query.includes("change=meaningful"))).toBe(true);
});
