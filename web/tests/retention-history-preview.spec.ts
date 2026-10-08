import type { Route } from "@playwright/test";
import { alpha, expect, test } from "./application-fixture";
import { apiVersion } from "./api-contract";
import { syntheticSession } from "./fixtures";

test.use({ reducedMotion: "reduce", timezoneId: "UTC" });

const adminId = syntheticSession().user.id;
const createdAt = "2026-10-08T08:00:00Z";
const previewId = "23000000000000000000000000000001";
const decisionId = "23000000000000000000000000000002";
const revisionId = "23000000000000000000000000000003";
const changeId = "23000000000000000000000000000004";
const policyEventId = "23000000000000000000000000000005";
const holdId = "23000000000000000000000000000006";

const policy = {
  workspaceId: alpha.id, revision: 1,
  hotHistoryDays: 90, rawReportDays: 180, archivedEvidenceDays: 365, auditDays: 730,
  updatedBy: null, updatedAt: null,
};

const historyItems = [
  {
    class: "audit", resourceKind: "finding-change-event", resourceId: changeId,
    action: "archive-audit", observedAt: "2024-01-04T05:06:07.008Z",
    sizeBytes: 466, protectedReasons: ["pending-policy-evaluation"],
    objectKey: null, objectDigest: null, objectRevision: null,
  },
  {
    class: "audit", resourceKind: "finding-decision-event", resourceId: decisionId,
    action: "archive-audit", observedAt: "2024-01-02T03:04:05.006Z",
    sizeBytes: 819, protectedReasons: [],
    objectKey: null, objectDigest: null, objectRevision: null,
  },
  {
    class: "audit", resourceKind: "notification-policy-event", resourceId: policyEventId,
    action: "archive-audit", observedAt: "2024-01-05T06:07:08.009Z",
    sizeBytes: 395, protectedReasons: ["active-delivery"],
    objectKey: null, objectDigest: null, objectRevision: null,
  },
  {
    class: "audit", resourceKind: "notification-policy-revision", resourceId: revisionId,
    action: "archive-audit", observedAt: "2024-01-03T04:05:06.007Z",
    sizeBytes: 599, protectedReasons: ["current-policy-revision", "pending-policy-evaluation"],
    objectKey: null, objectDigest: null, objectRevision: null,
  },
] as const;

function hold(resourceKind = "finding-decision-event") {
  return {
    id: holdId, workspaceId: alpha.id, resourceKind, resourceId: decisionId,
    reason: "Preserve exact V23 history.", revision: 1, createdBy: adminId, createdAt,
    releasedBy: null, releasedAt: null, releaseRationale: null,
  };
}

function preview(state: "ready" | "approved", items: readonly Record<string, unknown>[] = historyItems) {
  const approved = state === "approved";
  return {
    id: previewId, workspaceId: alpha.id, revision: approved ? 2 : 1, state,
    policyRevision: 1, snapshotDigest: `sha256:${"2".repeat(64)}`,
    createdBy: adminId, createdAt, expiresAt: "2026-10-08T08:15:00Z",
    summaries: [
      { class: "hot-history", action: "archive-history", retainDays: 90,
        totalCount: 0, eligibleCount: 0, protectedCount: 0, sizeBytes: 0 },
      { class: "archived-evidence", action: "expire-archive", retainDays: 365,
        totalCount: 0, eligibleCount: 0, protectedCount: 0, sizeBytes: 0 },
      { class: "raw-report", action: "expire-raw-report", retainDays: 180,
        totalCount: 0, eligibleCount: 0, protectedCount: 0, sizeBytes: 0 },
      { class: "audit", action: "archive-audit", retainDays: 730,
        totalCount: items.length, eligibleCount: 1, protectedCount: items.length - 1, sizeBytes: 819 },
      { class: "orphan-archive", action: "delete-orphan", retainDays: 1,
        totalCount: 0, eligibleCount: 0, protectedCount: 0, sizeBytes: 0 },
    ],
    items,
    approvedBy: approved ? adminId : null,
    approvedAt: approved ? "2026-10-08T08:05:00Z" : null,
    approvalRationale: approved ? "Approve exact V23 history preview." : null,
  };
}

async function fulfill(route: Route, status: number, body: Record<string, unknown>) {
  await route.fulfill({ status, json: { apiVersion, ...body } });
}

test("V23R1 admins select history holds and approved history remains preview-only", async ({ page, app }) => {
  expect(app.requests).toEqual([]);
  await page.setViewportSize({ width: 390, height: 844 });
  let holds: ReturnType<typeof hold>[] = [];
  const writes: Array<{ path: string; body: Record<string, unknown> }> = [];
  await page.route("**/api/v1/retention/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    const method = request.method();
    if (path === "/api/v1/retention/policy" && method === "GET") {
      await fulfill(route, 200, { retentionPolicy: policy });
      return;
    }
    if (path === "/api/v1/retention/holds" && method === "GET") {
      await fulfill(route, 200, { retentionHolds: holds });
      return;
    }
    const body = request.postDataJSON() as Record<string, unknown>;
    writes.push({ path, body });
    if (path === "/api/v1/retention/holds" && method === "POST") {
      expect(body).toEqual({
        resourceKind: "notification-policy-revision",
        resourceId: revisionId,
        reason: "Preserve selected policy history.",
      });
      holds = [...holds, {
        ...hold("notification-policy-revision"), id: "23000000000000000000000000000007",
        resourceId: revisionId, reason: "Preserve selected policy history.",
      }];
      await fulfill(route, 201, { retentionHold: holds.at(-1)! });
      return;
    }
    if (path === "/api/v1/retention/previews" && method === "POST") {
      expect(body).toEqual({});
      await fulfill(route, 201, { retentionPreview: preview("ready") });
      return;
    }
    if (path === `/api/v1/retention/previews/${previewId}/approvals` && method === "POST") {
      expect(body.rationale).toBe("Approve exact V23 history preview.");
      await fulfill(route, 201, { retentionPreview: preview("approved") });
      return;
    }
    if (path.endsWith("/executions")) {
      throw new Error("The V23 history UI must not request archive execution.");
    }
    await route.abort("blockedbyclient");
  });

  await page.goto("/#/settings");
  const entry = page.getByRole("button", { name: "Open retention controls", exact: true });
  await entry.click();
  const region = page.getByRole("region", { name: "Retention and archive", exact: true });
  const kind = region.getByRole("combobox", { name: "Resource type", exact: true });
  await expect(kind.locator("option")).toHaveText([
    "Raw report import",
    "Observation history",
    "Correlation audit event",
    "Finding decision history",
    "Notification policy revision",
    "Finding change history",
    "Notification policy evaluation event",
  ]);
  await kind.selectOption("notification-policy-revision");
  await region.getByLabel("Resource ID", { exact: true }).fill(revisionId);
  await region.getByLabel("Hold reason", { exact: true }).fill("Preserve selected policy history.");
  await region.getByRole("button", { name: "Create hold", exact: true }).click();
  await expect(region).toContainText("Preserve selected policy history.");

  await region.getByRole("button", { name: "Create retention preview", exact: true }).click();
  const result = region.getByRole("region", { name: "Retention preview result", exact: true });
  await expect(result).toContainText("1 / 4");
  await result.getByText("Review 4 resource decisions", { exact: true }).click();
  for (const value of [
    decisionId, revisionId, changeId, policyEventId,
    "819 bytes", "599 bytes", "466 bytes", "395 bytes",
    "Protected: Current Policy Revision, Pending Policy Evaluation.",
    "Protected: Pending Policy Evaluation.",
    "Protected: Active Delivery.",
  ]) {
    await expect(result).toContainText(value);
  }
  await region.getByLabel("Approval rationale", { exact: true }).fill("Approve exact V23 history preview.");
  await region.getByRole("button", { name: "Approve exact preview", exact: true }).click();
  await expect(region).toContainText("Preview only: history archive execution is unavailable until V24.");
  await expect(region.getByRole("form", { name: "Queue retention execution", exact: true })).toHaveCount(0);
  await expect(region.getByRole("button", { name: "Queue approved execution", exact: true })).toHaveCount(0);
  await expect(region).toBeInViewport();
  await region.getByRole("button", { name: "Close retention controls", exact: true }).click();
  await expect(entry).toBeFocused();
  expect(writes.some((write) => write.path.endsWith("/executions"))).toBe(false);
});

test("V23R2 viewers read holds and preview without mutation controls", async ({ page, app }) => {
  app.setWorkspaceRole(alpha.id, "viewer");
  const writes: string[] = [];
  await page.route("**/api/v1/retention/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    const method = request.method();
    if (path === "/api/v1/retention/policy" && method === "GET") {
      await fulfill(route, 200, { retentionPolicy: policy });
      return;
    }
    if (path === "/api/v1/retention/holds" && method === "GET") {
      await fulfill(route, 200, { retentionHolds: [hold()] });
      return;
    }
    if (path === "/api/v1/retention/previews" && method === "POST") {
      writes.push(path);
      await fulfill(route, 201, { retentionPreview: preview("ready") });
      return;
    }
    writes.push(path);
    await route.abort("blockedbyclient");
  });

  await page.goto("/#/settings");
  await page.getByRole("button", { name: "Open retention controls", exact: true }).click();
  const region = page.getByRole("region", { name: "Retention and archive", exact: true });
  await expect(region).toContainText("Preserve exact V23 history.");
  await expect(region.getByRole("button", { name: "Save retention policy", exact: true })).toBeDisabled();
  await expect(region.getByRole("form", { name: "Create retention hold", exact: true })).toHaveCount(0);
  await expect(region.getByRole("button", { name: "Release hold", exact: true })).toHaveCount(0);
  await region.getByRole("button", { name: "Create retention preview", exact: true }).click();
  await expect(region).toContainText(decisionId);
  await expect(region.getByRole("form", { name: "Approve retention preview", exact: true })).toHaveCount(1);
  await expect(region.getByRole("button", { name: "Approve exact preview", exact: true })).toBeDisabled();
  await expect(region.getByRole("form", { name: "Queue retention execution", exact: true })).toHaveCount(0);
  expect(writes).toEqual(["/api/v1/retention/previews"]);
});

test("V23R3 strict parsing accepts declared history and rejects future kinds and reasons", async ({ page, app }) => {
  expect(app.requests).toEqual([]);
  let refresh = 0;
  await page.route("**/api/v1/retention/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    const method = request.method();
    if (path === "/api/v1/retention/policy" && method === "GET") {
      await fulfill(route, 200, { retentionPolicy: policy });
      return;
    }
    if (path === "/api/v1/retention/holds" && method === "GET") {
      await fulfill(route, 200, { retentionHolds: [] });
      return;
    }
    if (path === "/api/v1/retention/previews" && method === "POST") {
      await fulfill(route, 201, { retentionPreview: preview("ready") });
      return;
    }
    if (path === `/api/v1/retention/previews/${previewId}` && method === "GET") {
      refresh++;
      const items: Array<Record<string, unknown>> = historyItems.map((item) => ({ ...item }));
      if (refresh === 1) {
        items[0] = { ...items[0], resourceKind: "future-history-event",
          resourceId: "23000000000000000000000000000008" };
      } else {
        items[0] = { ...items[0], protectedReasons: ["future-protection"] };
      }
      await fulfill(route, 200, { retentionPreview: preview("ready", items) });
      return;
    }
    await route.abort("blockedbyclient");
  });

  await page.goto("/#/settings");
  await page.getByRole("button", { name: "Open retention controls", exact: true }).click();
  const region = page.getByRole("region", { name: "Retention and archive", exact: true });
  await region.getByRole("button", { name: "Create retention preview", exact: true }).click();
  await expect(region).toContainText(changeId);
  const refreshButton = region.getByRole("button", { name: "Refresh preview receipt", exact: true });
  await refreshButton.click();
  await expect(region.getByRole("alert")).toContainText(/invalid retention item kind/i);
  await expect(region).not.toContainText("23000000000000000000000000000008");
  await expect(region).toContainText(changeId);
  await refreshButton.click();
  await expect(region.getByRole("alert")).toContainText(/invalid retention protection reason/i);
  await expect(region).toContainText(changeId);
});
