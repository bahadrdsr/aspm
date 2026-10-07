import type { Route } from "@playwright/test";
import { alpha, expect, originalAsset, test } from "./application-fixture";
import { apiVersion } from "./api-contract";
import { syntheticSession } from "./fixtures";

test.use({ reducedMotion: "reduce", timezoneId: "UTC" });

const findingId = "e1000000000000000000000000000001";
const observationId = "e2000000000000000000000000000002";
const runId = "e3000000000000000000000000000003";
const itemId = "e4000000000000000000000000000004";
const now = "2026-10-07T19:00:00Z";
const adminId = syntheticSession().user.id;
const archivedPayload = {
  id: observationId, runId: "normalized-run", sourceId: "retention-source", scanId: "retention-scan",
  scope: { id: "retention-scope", revision: "1", branch: "main" },
  sourceScanAt: "2026-10-01T10:00:00Z", sourceFindingId: "RET-1",
  sourceSeverity: "warning", normalizedSeverity: "medium",
  sourceLocation: { uri: "src/retention.ts", line: 42 },
  impact: "Archived exact impact.", remediation: "Archived exact remediation.",
  unmapped: { retained: "Archived exact source context." },
  evidenceDigest: `sha256:${"a".repeat(64)}`,
};

function workItem() {
  return {
    id: findingId, title: "Archived observation review", assetName: originalAsset.name,
    severity: "medium", ownerName: null, workflowState: "open",
    sourceScanAt: "2026-10-01T10:00:00Z", collectedAt: "2026-10-01T10:01:00Z",
    importedAt: "2026-10-01T10:02:00Z",
  };
}

function detail(restored: boolean) {
  return {
    ...workItem(), assetId: originalAsset.id, workspaceId: alpha.id,
    scopeLabel: "retention-scope / main (revision 1)", description: "Retention execution fixture.",
    remediation: "Review the archived observation.", ownerId: null,
    evidence: { text: "Canonical finding evidence remains available.", sourceLabel: "Retention scanner", verificationState: "not-run" },
    sourceState: "observed", sourceFreshnessAt: "2026-10-01T10:00:00Z",
    disposition: "none", acceptedRiskExpiresAt: null, riskAcceptanceExpired: false,
    verifiedResolution: false, decisionRevision: 1, evidenceRevision: 1, notes: [],
    observations: [{ ...archivedPayload, impact: restored ? archivedPayload.impact : "",
      remediation: restored ? archivedPayload.remediation : "", unmapped: restored ? archivedPayload.unmapped : {},
      evidenceAvailability: restored ? "available" : "archived" }],
    notesNextCursor: null, observationsNextCursor: null,
  };
}

function restoration(state: "queued" | "succeeded") {
  const done = state === "succeeded";
  return {
    id: runId, workspaceId: alpha.id, operation: "restore-observation",
    previewId: null, targetKind: "observation", targetId: observationId, state,
    requestedBy: adminId, rationale: "Restore exact archived observation.", createdAt: now,
    completedAt: done ? "2026-10-07T19:01:00Z" : null,
    total: 1, succeeded: done ? 1 : 0, protected: 0, missing: 0, corrupt: 0, failed: 0, failure: null,
    items: [{
      id: itemId, class: "archived-evidence", resourceKind: "observation", resourceId: observationId,
      action: "restore-archive", state: done ? "succeeded" : "queued",
      protectedReasons: [], outcome: done ? "restored" : "", failure: null,
      startedAt: done ? now : null, completedAt: done ? "2026-10-07T19:01:00Z" : null,
    }],
  };
}

async function fulfill(route: Route, status: number, body: Record<string, unknown>) {
  await route.fulfill({ status, json: { apiVersion, ...body } });
}

test("M06R2 Archived observation retrieval and restoration stay explicit", async ({ page, app }) => {
  expect(app.requests).toEqual([]);
  let restored = false;
  const calls: string[] = [];
  await page.route("**/api/v1/work**", async (route) => {
    await fulfill(route, 200, { dataOrigin: "synthetic", items: [workItem()], total: 1, nextCursor: null });
  });
  await page.route("**/api/v1/findings/**", async (route) => {
    const path = new URL(route.request().url()).pathname;
    if (route.request().method() === "GET" && path === `/api/v1/findings/${findingId}`) {
      await fulfill(route, 200, { dataOrigin: "synthetic", finding: detail(restored) });
      return;
    }
    await route.fallback();
  });
  await page.route("**/api/v1/observations/**", async (route) => {
    const request = route.request(), path = new URL(request.url()).pathname;
    calls.push(`${request.method()} ${path}`);
    if (request.method() === "GET" && path === `/api/v1/observations/${observationId}/evidence`) {
      const body = JSON.stringify(archivedPayload);
      await route.fulfill({ status: 200, body, headers: {
        "Content-Type": "application/json", "Content-Length": String(Buffer.byteLength(body)),
      } });
      return;
    }
    if (request.method() === "POST" && path === `/api/v1/observations/${observationId}/restorations`) {
      expect(request.postDataJSON()).toMatchObject({ rationale: "Restore exact archived observation." });
      await fulfill(route, 202, { retentionRun: restoration("queued") });
      return;
    }
    await route.abort("blockedbyclient");
  });
  await page.route(`**/api/v1/retention/runs/${runId}`, async (route) => {
    calls.push(`GET /api/v1/retention/runs/${runId}`);
    restored = true;
    await fulfill(route, 200, { retentionRun: restoration("succeeded") });
  });

  await page.goto("/#/work");
  await page.getByRole("button", { name: "Archived observation review", exact: true }).click();
  const dialog = page.getByRole("dialog", { name: "Archived observation review", exact: true });
  const history = dialog.getByRole("list", { name: "Observations", exact: true });
  await expect(history).toContainText("Evidence availabilityArchived");
  await history.getByRole("button", { name: "Retrieve archived observation", exact: true }).click();
  await expect(history).toContainText("Archived exact source context.");
  await history.getByLabel("Restoration rationale", { exact: true }).fill("Restore exact archived observation.");
  await history.getByRole("button", { name: "Queue observation restoration", exact: true }).click();
  await expect(history).toContainText("Restoration Queued");
  await history.getByRole("button", { name: "Refresh restoration", exact: true }).click();
  await expect(history).toContainText("Evidence availabilityAvailable");
  await expect(history).toContainText("Archived exact impact.");
  expect(calls).toEqual([
    `GET /api/v1/observations/${observationId}/evidence`,
    `POST /api/v1/observations/${observationId}/restorations`,
    `GET /api/v1/retention/runs/${runId}`,
  ]);
});
