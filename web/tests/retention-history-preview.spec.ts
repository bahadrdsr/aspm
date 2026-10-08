import { createHash } from "node:crypto";
import type { Page, Route } from "@playwright/test";
import { alpha, beta, expect, password, test } from "./application-fixture";
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
const runId = "24000000000000000000000000000001";
const failedDecisionId = "24000000000000000000000000000002";
const archivedHistoryText = JSON.stringify({
  schemaVersion: 1, resourceKind: "finding-decision-event", id: decisionId,
  workspaceId: alpha.id, findingId: "24000000000000000000000000000003",
  decisionRevision: 2, actorId: adminId, action: "update",
  rationale: "Exact archived V23 history.", changedFields: ["workflowState"],
  beforeState: { workflowState: "open" }, afterState: { workflowState: "pending-retest" },
  createdAt: "2024-01-02T03:04:05.006Z",
});
const archivedHistoryDigest = `sha256:${createHash("sha256").update(archivedHistoryText).digest("hex")}`;
const archivedHistorySize = Buffer.byteLength(archivedHistoryText);

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

function run(state: "queued" | "partial") {
  const terminal = state === "partial";
  const completedAt = terminal ? "2026-10-08T08:10:00Z" : null;
  const item = (value: {
    id: string; resourceKind: string; resourceId: string;
    state: "succeeded" | "protected" | "missing" | "corrupt" | "failed";
    reasons?: string[]; outcome: string; failure?: Record<string, unknown> | null;
  }) => ({
    id: value.id, class: "audit", resourceKind: value.resourceKind, resourceId: value.resourceId,
    action: "archive-audit", state: terminal ? value.state : "queued",
    protectedReasons: terminal ? value.reasons ?? [] : [], outcome: terminal ? value.outcome : "",
    failure: terminal ? value.failure ?? null : null,
    objectKey: null, objectDigest: null, objectRevision: null,
    startedAt: terminal ? createdAt : null, completedAt,
  });
  return {
    id: runId, workspaceId: alpha.id, operation: "apply-preview",
    previewId, targetKind: null, targetId: null, state,
    requestedBy: adminId, rationale: "Execute exact approved V24 history archive.",
    createdAt, completedAt, total: 5,
    succeeded: terminal ? 1 : 0, protected: terminal ? 1 : 0,
    missing: terminal ? 1 : 0, corrupt: terminal ? 1 : 0, failed: terminal ? 1 : 0,
    failure: null,
    items: [
      item({ id: "24000000000000000000000000000011", resourceKind: "finding-decision-event",
        resourceId: decisionId, state: "succeeded", outcome: "archived" }),
      item({ id: "24000000000000000000000000000012", resourceKind: "notification-policy-revision",
        resourceId: revisionId, state: "protected",
        reasons: ["current-policy-revision", "pending-policy-evaluation"], outcome: "protected" }),
      item({ id: "24000000000000000000000000000013", resourceKind: "finding-change-event",
        resourceId: changeId, state: "missing", outcome: "missing" }),
      item({ id: "24000000000000000000000000000014", resourceKind: "notification-policy-event",
        resourceId: policyEventId, state: "corrupt", outcome: "corrupt" }),
      item({ id: "24000000000000000000000000000015", resourceKind: "finding-decision-event",
        resourceId: failedDecisionId, state: "failed", outcome: "",
        failure: { code: "attempt-limit", message: "Bounded archive attempts were exhausted.", retryable: false } }),
    ],
  };
}

function retrievalHeaders(overrides: Record<string, string> = {}) {
  return {
    "Content-Type": "application/json",
    "Content-Disposition": `attachment; filename="history-finding-decision-event-${decisionId}.json"`,
    "Content-Length": String(archivedHistorySize),
    "Cache-Control": "no-store",
    "X-Content-Type-Options": "nosniff",
    "X-ASPM-History-Resource-Kind": "finding-decision-event",
    "X-ASPM-History-Resource-ID": decisionId,
    "X-ASPM-History-Availability": "archived",
    "X-ASPM-Archive-Digest": archivedHistoryDigest,
    "X-ASPM-Archive-Size": String(archivedHistorySize),
    "X-ASPM-Detail-Revision": "2",
    ...overrides,
  };
}

async function fulfill(route: Route, status: number, body: Record<string, unknown>) {
  await route.fulfill({ status, json: { apiVersion, ...body } });
}

async function installV24HistoryRoutes(page: Page, retrieve: (route: Route, call: number) => Promise<void>,
  runReceipt: (call: number) => Record<string, unknown> = () => run("partial")) {
  let runCalls = 0;
  let retrievalCalls = 0;
  await page.route("**/api/v1/retention/**", async (route) => {
    const request = route.request();
    const path = new URL(request.url()).pathname;
    const method = request.method();
    const headers = await request.allHeaders();
    const workspaceId = headers["x-aspm-workspace-id"] ?? alpha.id;
    if (path === "/api/v1/retention/policy" && method === "GET") {
      await fulfill(route, 200, { retentionPolicy: { ...policy, workspaceId } });
      return;
    }
    if (path === "/api/v1/retention/holds" && method === "GET") {
      await fulfill(route, 200, { retentionHolds: [] });
      return;
    }
    if (path === `/api/v1/retention/runs/${runId}` && method === "GET") {
      runCalls++;
      await fulfill(route, 200, { retentionRun: runReceipt(runCalls) });
      return;
    }
    if (path === `/api/v1/retention/history/finding-decision-event/${decisionId}` && method === "GET") {
      retrievalCalls++;
      await retrieve(route, retrievalCalls);
      return;
    }
    const body = request.postDataJSON() as Record<string, unknown>;
    if (path === "/api/v1/retention/previews" && method === "POST") {
      await fulfill(route, 201, { retentionPreview: preview("ready") });
      return;
    }
    if (path === `/api/v1/retention/previews/${previewId}/approvals` && method === "POST") {
      await fulfill(route, 201, { retentionPreview: preview("approved") });
      return;
    }
    if (path === `/api/v1/retention/previews/${previewId}/executions` && method === "POST") {
      expect(body.rationale).toBe("Execute exact approved V24 history archive.");
      await fulfill(route, 202, { retentionRun: run("queued") });
      return;
    }
    await route.abort("blockedbyclient");
  });
  return {
    runCalls: () => runCalls,
    retrievalCalls: () => retrievalCalls,
  };
}

async function openV24ArchivedRun(page: Page) {
  await page.goto("/#/settings");
  await page.getByRole("button", { name: "Open retention controls", exact: true }).click();
  const region = page.getByRole("region", { name: "Retention and archive", exact: true });
  await region.getByRole("button", { name: "Create retention preview", exact: true }).click();
  await region.getByLabel("Approval rationale", { exact: true }).fill("Approve exact V23 history preview.");
  await region.getByRole("button", { name: "Approve exact preview", exact: true }).click();
  const execution = region.getByRole("form", { name: "Queue retention execution", exact: true });
  await expect(execution).toHaveCount(1);
  await execution.getByLabel("Execution rationale", { exact: true })
    .fill("Execute exact approved V24 history archive.");
  await execution.getByRole("button", { name: "Queue approved execution", exact: true }).click();
  const runStatus = region.getByRole("region", { name: "Retention execution status", exact: true });
  await runStatus.getByRole("button", { name: "Refresh execution", exact: true }).click();
  await expect(runStatus).toContainText("Execution Partial");
  return { region, runStatus };
}

test("V24R1 admins queue one history execution, refresh manually and retrieve exact JSON", async ({ page, app }) => {
  expect(app.requests).toEqual([]);
  const consoleMessages: string[] = [];
  page.on("console", (message) => consoleMessages.push(message.text()));
  await page.setViewportSize({ width: 390, height: 844 });
  let holds: ReturnType<typeof hold>[] = [];
  let runReads = 0;
  let retrievalReads = 0;
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
    if (path === `/api/v1/retention/runs/${runId}` && method === "GET") {
      runReads++;
      await fulfill(route, 200, { retentionRun: run("partial") });
      return;
    }
    if (path === `/api/v1/retention/history/finding-decision-event/${decisionId}` && method === "GET") {
      retrievalReads++;
      await route.fulfill({ status: 200, body: archivedHistoryText, headers: retrievalHeaders() });
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
    if (path === `/api/v1/retention/previews/${previewId}/executions` && method === "POST") {
      expect(body.rationale).toBe("Execute exact approved V24 history archive.");
      expect(body.idempotencyKey).toMatch(/^[0-9a-f-]{36}$/);
      await fulfill(route, 202, { retentionRun: run("queued") });
      return;
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
  await expect(region).not.toContainText("Preview only: history archive execution is unavailable until V24.");
  const execution = region.getByRole("form", { name: "Queue retention execution", exact: true });
  await expect(execution).toHaveCount(1);
  await execution.getByLabel("Execution rationale", { exact: true })
    .fill("Execute exact approved V24 history archive.");
  await execution.getByRole("button", { name: "Queue approved execution", exact: true }).click();
  const runStatus = region.getByRole("region", { name: "Retention execution status", exact: true });
  await expect(runStatus).toContainText("Execution Queued");
  await page.waitForTimeout(250);
  expect(runReads, "V24 retention status must not poll.").toBe(0);
  await runStatus.getByRole("button", { name: "Refresh execution", exact: true }).click();
  await expect(runStatus).toContainText("Execution Partial");
  for (const value of [
    "1 succeeded, 1 protected, 1 missing, 1 corrupt, 1 failed, 5 total.",
    "Archive Audit: Succeeded", "Archive Audit: Protected", "Archive Audit: Missing",
    "Archive Audit: Corrupt", "Archive Audit: Failed",
    "Protected: Current Policy Revision, Pending Policy Evaluation.",
    "Bounded archive attempts were exhausted.",
  ]) {
    await expect(runStatus).toContainText(value);
  }
  const retrieve = runStatus.getByRole("button", { name: "Retrieve archived history", exact: true });
  await expect(retrieve).toHaveCount(1);
  await retrieve.click();
  const archived = region.getByRole("region", { name: "Archived history retrieval", exact: true });
  await expect(archived).toContainText(archivedHistoryText);
  await expect(archived).toContainText("Finding Decision Event");
  await expect(archived).toContainText(decisionId);
  await expect(archived).toContainText(archivedHistoryDigest);
  await expect(archived).toContainText(`${archivedHistorySize} bytes`);
  await expect(archived).toContainText("Detail revision 2");
  const download = page.waitForEvent("download");
  await archived.getByRole("button", { name: "Download archived history", exact: true }).click();
  const archivedDownload = await download;
  expect(archivedDownload.suggestedFilename()).toBe(`history-finding-decision-event-${decisionId}.json`);
  const stream = await archivedDownload.createReadStream();
  const chunks: Buffer[] = [];
  for await (const chunk of stream) chunks.push(Buffer.from(chunk));
  expect(Buffer.concat(chunks).toString("utf8")).toBe(archivedHistoryText);
  const stored = await page.evaluate(() => JSON.stringify({
    local: Object.fromEntries(Object.entries(localStorage)),
    session: Object.fromEntries(Object.entries(sessionStorage)),
  }));
  expect(stored).not.toContain(archivedHistoryText);
  expect(stored).not.toContain(archivedHistoryDigest);
  expect(consoleMessages.join("\n")).not.toContain(archivedHistoryText);
  expect(consoleMessages.join("\n")).not.toContain(archivedHistoryDigest);
  expect(runReads).toBe(1);
  expect(retrievalReads).toBe(1);
  await expect(region).toBeInViewport();
  await region.getByRole("button", { name: "Close retention controls", exact: true }).click();
  await expect(entry).toBeFocused();
  expect(writes.filter((write) => write.path.endsWith("/executions"))).toHaveLength(1);
});

test("V24R2 viewers read holds and preview without mutation controls", async ({ page, app }) => {
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

test("V24R4 strict execution and retrieval parsing retains only same-scope valid history", async ({ page, app }) => {
  expect(app.requests).toEqual([]);
  const controls = await installV24HistoryRoutes(page, async (route, call) => {
    if (call === 1) {
      await route.fulfill({ status: 200, body: archivedHistoryText, headers: retrievalHeaders() });
      return;
    }
    if (call === 2) {
      await route.fulfill({ status: 200, body: archivedHistoryText,
        headers: retrievalHeaders({ "X-ASPM-History-Resource-Kind": "future-history-event" }) });
      return;
    }
    if (call === 3) {
      await route.fulfill({ status: 200, body: archivedHistoryText,
        headers: retrievalHeaders({ "X-ASPM-History-Availability": "available" }) });
      return;
    }
    if (call === 4) {
      await route.fulfill({ status: 200, body: archivedHistoryText,
        headers: retrievalHeaders({ "X-ASPM-Archive-Digest": "sha256:1234" }) });
      return;
    }
    if (call === 5) {
      await route.fulfill({ status: 200, body: archivedHistoryText,
        headers: retrievalHeaders({ "X-ASPM-History-Resource-ID": failedDecisionId }) });
      return;
    }
    const body = "not-json";
    const digest = `sha256:${createHash("sha256").update(body).digest("hex")}`;
    await route.fulfill({ status: 200, body, headers: retrievalHeaders({
      "Content-Length": String(Buffer.byteLength(body)),
      "X-ASPM-Archive-Digest": digest,
      "X-ASPM-Archive-Size": String(Buffer.byteLength(body)),
    }) });
  }, (call) => {
    if (call === 1) return run("partial");
    const invalid = structuredClone(run("partial"));
    invalid.items[0].resourceKind = "future-history-event";
    return invalid;
  });
  const { region, runStatus } = await openV24ArchivedRun(page);
  await runStatus.getByRole("button", { name: "Refresh execution", exact: true }).click();
  await expect(runStatus.getByRole("alert")).toContainText(/invalid retention item resource kind/i);
  await expect(runStatus).toContainText(decisionId);
  const retrieve = runStatus.getByRole("button", { name: "Retrieve archived history", exact: true });
  await retrieve.click();
  const archived = region.getByRole("region", { name: "Archived history retrieval", exact: true });
  await expect(archived).toContainText(archivedHistoryText);

  for (const message of [
    /invalid archived history resource kind/i,
    /invalid archived history availability/i,
    /invalid archived history archive reference/i,
    /invalid archived history resource identity/i,
    /invalid archived history JSON/i,
  ]) {
    await retrieve.click();
    await expect(archived.getByRole("alert")).toContainText(message);
    await expect(archived).toContainText(archivedHistoryText);
  }
  expect(controls.runCalls()).toBe(2);
  expect(controls.retrievalCalls()).toBe(6);
  const stored = await page.evaluate(() => JSON.stringify({
    local: Object.fromEntries(Object.entries(localStorage)),
    session: Object.fromEntries(Object.entries(sessionStorage)),
  }));
  expect(stored).not.toContain(archivedHistoryText);
  expect(stored).not.toContain(archivedHistoryDigest);
});

test("V24R5 workspace change clears retrieval and ignores a held old-scope response", async ({ page, app }) => {
  expect(app.requests).toEqual([]);
  let arrive!: () => void;
  let release!: () => void;
  const requested = new Promise<void>((resolve) => { arrive = resolve; });
  const held = new Promise<void>((resolve) => { release = resolve; });
  await installV24HistoryRoutes(page, async (route) => {
    arrive();
    await held;
    try {
      await route.fulfill({ status: 200, body: archivedHistoryText, headers: retrievalHeaders() });
    } catch {
      // Aborted old-scope responses are expected after workspace change.
    }
  });
  const { runStatus } = await openV24ArchivedRun(page);
  const pending = runStatus.getByRole("button", { name: "Retrieve archived history", exact: true }).click();
  await requested;
  await page.getByRole("combobox", { name: "Workspace", exact: true }).selectOption(beta.id);
  release();
  await pending.catch(() => undefined);
  await expect(page.getByText(archivedHistoryText, { exact: true })).toHaveCount(0);
  await expect(page.getByRole("complementary").getByText(beta.role, { exact: true })).toBeVisible();
  const stored = await page.evaluate(() => JSON.stringify({
    local: Object.fromEntries(Object.entries(localStorage)),
    session: Object.fromEntries(Object.entries(sessionStorage)),
  }));
  expect(stored).not.toContain(archivedHistoryText);
  expect(stored).not.toContain(archivedHistoryDigest);
});

test("V24R6 logout and retrieval 401 clear archived payloads", async ({ page, app }) => {
  expect(app.requests).toEqual([]);
  await installV24HistoryRoutes(page, async (route, call) => {
    if (call === 1) {
      await route.fulfill({ status: 200, body: archivedHistoryText, headers: retrievalHeaders() });
      return;
    }
    await fulfill(route, 401, { error: {
      code: "unauthorized", message: "Session expired.", requestId: "v24-history-session", retryable: false,
    } });
  });
  const { region, runStatus } = await openV24ArchivedRun(page);
  const retrieve = runStatus.getByRole("button", { name: "Retrieve archived history", exact: true });
  await retrieve.click();
  await expect(region.getByRole("region", { name: "Archived history retrieval", exact: true }))
    .toContainText(archivedHistoryText);
  await retrieve.click();
  await expect(page.getByRole("form", { name: "Sign in", exact: true })).toBeVisible();
  await expect(page.getByText(archivedHistoryText, { exact: true })).toHaveCount(0);

  await page.getByLabel("Email", { exact: true }).fill(syntheticSession().user.email);
  await page.getByLabel("Password", { exact: true }).fill(password);
  await page.getByRole("button", { name: "Sign in", exact: true }).click();
  await page.goto("/#/settings");
  await expect(page.getByRole("button", { name: "Open retention controls", exact: true })).toBeVisible();
  await page.getByRole("button", { name: "Sign out", exact: true }).click();
  await expect(page.getByRole("form", { name: "Sign in", exact: true })).toBeVisible();
  await expect(page.getByText(archivedHistoryText, { exact: true })).toHaveCount(0);
  const stored = await page.evaluate(() => JSON.stringify({
    local: Object.fromEntries(Object.entries(localStorage)),
    session: Object.fromEntries(Object.entries(sessionStorage)),
  }));
  expect(stored).not.toContain(archivedHistoryText);
  expect(stored).not.toContain(archivedHistoryDigest);
});
