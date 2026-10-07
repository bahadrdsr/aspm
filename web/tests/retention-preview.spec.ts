import type { Route } from "@playwright/test";
import { alpha, expect, test } from "./application-fixture";
import { apiVersion } from "./api-contract";
import { syntheticSession } from "./fixtures";

test.use({ reducedMotion: "reduce", timezoneId: "UTC" });

const adminId = syntheticSession().user.id;
const heldImportId = "d1000000000000000000000000000001";
const eligibleImportId = "d2000000000000000000000000000002";
const holdId = "d3000000000000000000000000000003";
const firstPreviewId = "d4000000000000000000000000000004";
const secondPreviewId = "d5000000000000000000000000000005";
const createdAt = "2026-10-07T18:00:00Z";

function policy(revision: number, days = [90, 180, 365, 730]) {
  return {
    workspaceId: alpha.id, revision,
    hotHistoryDays: days[0], rawReportDays: days[1],
    archivedEvidenceDays: days[2], auditDays: days[3],
    updatedBy: revision === 1 ? null : adminId,
    updatedAt: revision === 1 ? null : createdAt,
  };
}

function hold() {
  return {
    id: holdId, workspaceId: alpha.id, resourceKind: "import", resourceId: heldImportId,
    reason: "Synthetic legal hold.", revision: 1, createdBy: adminId, createdAt,
    releasedBy: null, releasedAt: null, releaseRationale: null,
  };
}

function preview(id: string, state: "ready" | "stale" | "approved") {
  const approved = state === "approved";
  return {
    id, workspaceId: alpha.id, revision: approved || state === "stale" ? 2 : 1,
    state, policyRevision: 2, snapshotDigest: `sha256:${"a".repeat(64)}`,
    createdBy: adminId, createdAt, expiresAt: "2026-10-07T18:15:00Z",
    summaries: [
      { class: "hot-history", action: "archive-history", retainDays: 30,
        totalCount: 2, eligibleCount: 1, protectedCount: 1, sizeBytes: 512 },
      { class: "archived-evidence", action: "expire-archive", retainDays: 120,
        totalCount: 0, eligibleCount: 0, protectedCount: 0, sizeBytes: 0 },
      { class: "raw-report", action: "expire-raw-report", retainDays: 60,
        totalCount: 2, eligibleCount: 1, protectedCount: 1, sizeBytes: 2048 },
      { class: "audit", action: "archive-audit", retainDays: 240,
        totalCount: 1, eligibleCount: 1, protectedCount: 0, sizeBytes: 256 },
    ],
    items: [
      { class: "raw-report", resourceKind: "import", resourceId: eligibleImportId,
        action: "expire-raw-report", observedAt: "2026-01-01T00:00:00Z",
        sizeBytes: 2048, protectedReasons: [] },
      { class: "raw-report", resourceKind: "import", resourceId: heldImportId,
        action: "expire-raw-report", observedAt: "2026-01-01T00:00:00Z",
        sizeBytes: 1024, protectedReasons: ["legal-hold"] },
    ],
    approvedBy: approved ? adminId : null,
    approvedAt: approved ? "2026-10-07T18:05:00Z" : null,
    approvalRationale: approved ? "Approve exact synthetic preview." : null,
  };
}

async function fulfill(route: Route, status: number, body: Record<string, unknown>) {
  await route.fulfill({ status, json: { apiVersion, ...body } });
}

test("M06R1 Retention policy, holds and stale approval remain preview-only", async ({ page, app }) => {
  test.setTimeout(30_000);
  expect(app.requests).toEqual([]);
  let currentPolicy = policy(1);
  let holds: ReturnType<typeof hold>[] = [];
  let firstStale = false;
  const writes: Array<{ path: string; body: Record<string, unknown> }> = [];

  await page.route("**/api/v1/retention/**", async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const path = url.pathname;
    const method = request.method();
    if (path === "/api/v1/retention/policy" && method === "GET") {
      await fulfill(route, 200, { retentionPolicy: currentPolicy });
      return;
    }
    if (path === "/api/v1/retention/holds" && method === "GET") {
      await fulfill(route, 200, { retentionHolds: holds });
      return;
    }
    if (method === "GET" && path === `/api/v1/retention/previews/${firstPreviewId}`) {
      await fulfill(route, 200, { retentionPreview: preview(firstPreviewId, "stale") });
      return;
    }
    const body = request.postDataJSON() as Record<string, unknown>;
    writes.push({ path, body });
    if (path === "/api/v1/retention/policy" && method === "PATCH") {
      expect(body).toEqual({
        revision: 1, hotHistoryDays: 30, rawReportDays: 60,
        archivedEvidenceDays: 120, auditDays: 240,
      });
      currentPolicy = policy(2, [30, 60, 120, 240]);
      await fulfill(route, 200, { retentionPolicy: currentPolicy });
      return;
    }
    if (path === "/api/v1/retention/holds" && method === "POST") {
      expect(body).toEqual({
        resourceKind: "import", resourceId: heldImportId, reason: "Synthetic legal hold.",
      });
      holds = [hold()];
      await fulfill(route, 201, { retentionHold: hold() });
      return;
    }
    if (path === "/api/v1/retention/previews" && method === "POST") {
      expect(body).toEqual({});
      const id = firstStale ? secondPreviewId : firstPreviewId;
      await fulfill(route, 201, { retentionPreview: preview(id, "ready") });
      return;
    }
    if (path === `/api/v1/retention/previews/${firstPreviewId}/approvals` && method === "POST") {
      expect(body.snapshotDigest).toBe(`sha256:${"a".repeat(64)}`);
      expect(body.rationale).toBe("Approve stale synthetic preview.");
      expect(body.idempotencyKey).toMatch(/^[0-9a-f-]{36}$/);
      firstStale = true;
      await fulfill(route, 409, { error: {
        code: "conflict", message: "The preview is stale.", requestId: "retention-stale", retryable: false,
      } });
      return;
    }
    if (path === `/api/v1/retention/previews/${secondPreviewId}/approvals` && method === "POST") {
      expect(body.rationale).toBe("Approve exact synthetic preview.");
      await fulfill(route, 201, { retentionPreview: preview(secondPreviewId, "approved") });
      return;
    }
    await route.abort("blockedbyclient");
  });

  await page.goto("/#/settings");
  await page.getByRole("button", { name: "Open retention controls", exact: true }).click();
  const region = page.getByRole("region", { name: "Retention and archive", exact: true });
  await expect(region).toContainText("Policy revision 1");
  await region.getByLabel("Hot history days", { exact: true }).fill("30");
  await region.getByLabel("Raw report days", { exact: true }).fill("60");
  await region.getByLabel("Archived evidence days", { exact: true }).fill("120");
  await region.getByLabel("Audit days", { exact: true }).fill("240");
  await region.getByRole("button", { name: "Save retention policy", exact: true }).click();
  await expect(region).toContainText("Policy revision 2");

  await region.getByRole("combobox", { name: "Resource type", exact: true }).selectOption("import");
  await region.getByLabel("Resource ID", { exact: true }).fill(heldImportId);
  await region.getByLabel("Hold reason", { exact: true }).fill("Synthetic legal hold.");
  await region.getByRole("button", { name: "Create hold", exact: true }).click();
  await expect(region).toContainText("Synthetic legal hold.");

  await region.getByRole("button", { name: "Create retention preview", exact: true }).click();
  await expect(region.getByRole("region", { name: "Retention preview result", exact: true })).toContainText("1 / 2");
  await expect(region).toContainText("Protected: Legal Hold.");
  await region.getByLabel("Approval rationale", { exact: true }).fill("Approve stale synthetic preview.");
  await region.getByRole("button", { name: "Approve exact preview", exact: true }).click();
  await expect(region.getByRole("alert")).toContainText(/stale|conflict/i);
  await region.getByRole("button", { name: "Refresh preview receipt", exact: true }).click();
  await expect(region).toContainText("This preview is stale.");

  await region.getByRole("button", { name: "Create retention preview", exact: true }).click();
  await region.getByLabel("Approval rationale", { exact: true }).fill("Approve exact synthetic preview.");
  await region.getByRole("button", { name: "Approve exact preview", exact: true }).click();
  await expect(region).toContainText("This approval is non-destructive and does not start an executor.");
  await region.getByRole("button", { name: "Close retention controls", exact: true }).click();
  await expect(page.getByRole("button", { name: "Open retention controls", exact: true })).toBeFocused();

  expect(writes.map((write) => write.path)).toEqual([
    "/api/v1/retention/policy",
    "/api/v1/retention/holds",
    "/api/v1/retention/previews",
    `/api/v1/retention/previews/${firstPreviewId}/approvals`,
    "/api/v1/retention/previews",
    `/api/v1/retention/previews/${secondPreviewId}/approvals`,
  ]);
  expect(writes.some((write) => /execut|apply|archive|expire/.test(write.path))).toBe(false);
});
