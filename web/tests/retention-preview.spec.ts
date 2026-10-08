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
const executionId = "d6000000000000000000000000000006";
const orphanId = "da00000000000000000000000000000a";
const orphanKey = `archive/${alpha.id}/observations/db00000000000000000000000000000b/${"b".repeat(64)}`;
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
      { class: "orphan-archive", action: "delete-orphan", retainDays: 1,
        totalCount: 1, eligibleCount: 1, protectedCount: 0, sizeBytes: 128 },
    ],
    items: [
      { class: "raw-report", resourceKind: "import", resourceId: eligibleImportId,
        action: "expire-raw-report", observedAt: "2026-01-01T00:00:00Z",
        sizeBytes: 2048, protectedReasons: [] },
      { class: "raw-report", resourceKind: "import", resourceId: heldImportId,
        action: "expire-raw-report", observedAt: "2026-01-01T00:00:00Z",
        sizeBytes: 1024, protectedReasons: ["legal-hold"] },
      { class: "orphan-archive", resourceKind: "archive-object", resourceId: orphanId,
        action: "delete-orphan", observedAt: "2026-10-01T00:00:00Z",
        sizeBytes: 128, protectedReasons: [], objectKey: orphanKey,
        objectDigest: `sha256:${"b".repeat(64)}`, objectRevision: 1 },
    ],
    approvedBy: approved ? adminId : null,
    approvedAt: approved ? "2026-10-07T18:05:00Z" : null,
    approvalRationale: approved ? "Approve exact synthetic preview." : null,
  };
}

function execution(state: "queued" | "partial") {
  const terminal = state === "partial";
  return {
    id: executionId, workspaceId: alpha.id, operation: "apply-preview",
    previewId: secondPreviewId, targetKind: null, targetId: null, state,
    requestedBy: adminId, rationale: "Apply exact synthetic preview.", createdAt,
    completedAt: terminal ? "2026-10-07T18:10:00Z" : null,
    total: 3, succeeded: terminal ? 2 : 0, protected: terminal ? 1 : 0, missing: 0, corrupt: 0, failed: 0,
    failure: null,
    items: [
      {
        id: "d7000000000000000000000000000007", class: "raw-report", resourceKind: "import",
        resourceId: eligibleImportId, action: "expire-raw-report",
        state: terminal ? "succeeded" : "queued", protectedReasons: [],
        outcome: terminal ? "expired" : "", failure: null,
        startedAt: terminal ? createdAt : null, completedAt: terminal ? "2026-10-07T18:09:00Z" : null,
      },
      {
        id: "d8000000000000000000000000000008", class: "raw-report", resourceKind: "import",
        resourceId: heldImportId, action: "expire-raw-report",
        state: terminal ? "protected" : "queued", protectedReasons: terminal ? ["legal-hold"] : [],
        outcome: terminal ? "protected" : "", failure: null,
        startedAt: terminal ? createdAt : null, completedAt: terminal ? "2026-10-07T18:10:00Z" : null,
      },
      {
        id: "d9000000000000000000000000000009", class: "orphan-archive", resourceKind: "archive-object",
        resourceId: orphanId, action: "delete-orphan", objectKey: orphanKey,
        objectDigest: `sha256:${"b".repeat(64)}`, objectRevision: 1,
        state: terminal ? "succeeded" : "queued", protectedReasons: [],
        outcome: terminal ? "deleted" : "", failure: null,
        startedAt: terminal ? createdAt : null, completedAt: terminal ? "2026-10-07T18:10:00Z" : null,
      },
    ],
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
  let executionComplete = false;
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
    if (method === "GET" && path === `/api/v1/retention/runs/${executionId}`) {
      executionComplete = true;
      await fulfill(route, 200, { retentionRun: execution("partial") });
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
    if (path === `/api/v1/retention/previews/${secondPreviewId}/executions` && method === "POST") {
      expect(body.rationale).toBe("Apply exact synthetic preview.");
      expect(body.idempotencyKey).toMatch(/^[0-9a-f-]{36}$/);
      await fulfill(route, 202, { retentionRun: execution("queued") });
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
  await expect(region).toContainText(orphanKey);
  await region.getByLabel("Approval rationale", { exact: true }).fill("Approve stale synthetic preview.");
  await region.getByRole("button", { name: "Approve exact preview", exact: true }).click();
  await expect(region.getByRole("alert")).toContainText(/stale|conflict/i);
  await region.getByRole("button", { name: "Refresh preview receipt", exact: true }).click();
  await expect(region).toContainText("This preview is stale.");

  await region.getByRole("button", { name: "Create retention preview", exact: true }).click();
  await region.getByLabel("Approval rationale", { exact: true }).fill("Approve exact synthetic preview.");
  await region.getByRole("button", { name: "Approve exact preview", exact: true }).click();
  await expect(region).toContainText("Approval alone is non-destructive and does not start an executor.");
  await region.getByLabel("Execution rationale", { exact: true }).fill("Apply exact synthetic preview.");
  await region.getByRole("button", { name: "Queue approved execution", exact: true }).click();
  const run = region.getByRole("region", { name: "Retention execution status", exact: true });
  await expect(run).toContainText("Execution Queued");
  await run.getByRole("button", { name: "Refresh execution", exact: true }).click();
  await expect(run).toContainText("Execution Partial");
  await expect(run).toContainText("2 succeeded, 1 protected");
  await expect(run).toContainText(orphanKey);
  expect(executionComplete).toBe(true);
  await region.getByRole("button", { name: "Close retention controls", exact: true }).click();
  await expect(page.getByRole("button", { name: "Open retention controls", exact: true })).toBeFocused();

  expect(writes.map((write) => write.path)).toEqual([
    "/api/v1/retention/policy",
    "/api/v1/retention/holds",
    "/api/v1/retention/previews",
    `/api/v1/retention/previews/${firstPreviewId}/approvals`,
    "/api/v1/retention/previews",
    `/api/v1/retention/previews/${secondPreviewId}/approvals`,
    `/api/v1/retention/previews/${secondPreviewId}/executions`,
  ]);
  expect(writes.filter((write) => write.path.endsWith("/executions"))).toHaveLength(1);
  expect(writes.some((write) => /\/(apply|archive|expire)$/.test(write.path))).toBe(false);
});
