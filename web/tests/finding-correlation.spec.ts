import type { Page, Route } from "@playwright/test";
import { alpha, expect, originalAsset, test } from "./application-fixture";
import { apiVersion } from "./api-contract";
import { syntheticSession } from "./fixtures";

test.use({ reducedMotion: "reduce", timezoneId: "UTC" });

const primaryId = "a1000000000000000000000000000001";
const memberId = "a2000000000000000000000000000002";
const correlationId = "c1000000000000000000000000000001";
const mergeEventId = "e1000000000000000000000000000001";
const splitEventId = "e2000000000000000000000000000002";
const now = "2026-10-07T16:00:00Z";
const adminId = syntheticSession().user.id;
const analystId = "33333333-3333-4333-8333-333333333334";
const expiry = "2026-11-07T16:00:00Z";

function workItem(id: string, title: string, ownerName: string | null) {
  return {
    id, title, assetName: originalAsset.name, severity: id === primaryId ? "medium" : "high",
    ownerName, workflowState: id === primaryId ? "open" : "in-progress",
    sourceScanAt: "2026-10-07T12:00:00Z", collectedAt: "2026-10-07T12:01:00Z",
    importedAt: "2026-10-07T12:02:00Z",
  };
}

function decision(ownerId: string | null, workflowState: "open" | "in-progress" | "resolved",
  disposition: "none" | "accepted-risk", acceptedRiskExpiresAt: string | null) {
  return { ownerId, workflowState, disposition, acceptedRiskExpiresAt };
}

const primaryDecision = decision(adminId, "open", "accepted-risk", expiry);
const memberDecision = decision(analystId, "in-progress", "none", null);

function member(id: string, sourceId: string, title: string, current = id === primaryId ? primaryDecision : memberDecision) {
  return {
    findingId: id, sourceId, title, severity: id === primaryId ? "medium" : "high",
    decisionRevision: id === primaryId ? 3 : 4, evidenceRevision: 1,
    observationCount: 1, noteCount: 1, decision: current,
    originalDecision: id === primaryId ? primaryDecision : memberDecision,
  };
}

function detail(id: string, merged: boolean) {
  const primary = id === primaryId;
  const current = primary ? primaryDecision : memberDecision;
  return {
    ...workItem(id, primary ? "Primary scanner issue" : "Secondary scanner issue",
      current.ownerId === adminId ? "Synthetic Admin" : "Synthetic analyst"),
    scopeLabel: "owned-repository / refs/heads/main (revision 1)",
    description: primary ? "Primary source detail." : "Secondary source detail.",
    remediation: "Review the synthetic correlation.",
    evidence: { text: primary ? "primary evidence" : "secondary evidence", sourceLabel: primary ? "Scanner A" : "Scanner B", verificationState: "not-run" },
    assetId: originalAsset.id, workspaceId: alpha.id, ownerId: current.ownerId,
    sourceState: "observed", sourceFreshnessAt: "2026-10-07T12:00:00Z",
    disposition: current.disposition, acceptedRiskExpiresAt: current.acceptedRiskExpiresAt,
    riskAcceptanceExpired: false, verifiedResolution: false,
    decisionRevision: primary ? 3 : 4, evidenceRevision: 1,
    notes: [{ id: primary ? "note-primary" : "note-secondary", text: primary ? "Primary note." : "Secondary note." }],
    observations: [{
      id: primary ? "observation-primary" : "observation-secondary", runId: primary ? "run-primary" : "run-secondary",
      sourceId: primary ? "scanner-a" : "scanner-b", scanId: primary ? "scan-a" : "scan-b",
      scope: { id: "owned-repository", revision: "1", branch: "refs/heads/main" },
      sourceScanAt: "2026-10-07T12:00:00Z", sourceFindingId: primary ? "A-1" : "B-1",
      sourceSeverity: primary ? "warning" : "error", normalizedSeverity: primary ? "medium" : "high",
      sourceLocation: { uri: primary ? "src/a.go" : "src/b.go", line: primary ? 10 : 20 },
      impact: "Synthetic impact.", remediation: "Synthetic remediation.", unmapped: {}, evidenceDigest: `sha256:${primary ? "1" : "2"}`.repeat(1).padEnd(71, primary ? "1" : "2"),
    }],
    notesNextCursor: null, observationsNextCursor: null,
    ...(primary && merged ? { correlation: activeCorrelation() } : {}),
  };
}

function activeCorrelation() {
  return {
    id: correlationId, workspaceId: alpha.id, primaryFindingId: primaryId, state: "active", revision: 1,
    members: [member(primaryId, "scanner-a", "Primary scanner issue"),
      member(memberId, "scanner-b", "Secondary scanner issue")],
    events: [{ id: mergeEventId, type: "merge", actorId: adminId,
      rationale: "Reviewed synthetic cross-source identity.", createdAt: now }],
  };
}

async function fulfill(route: Route, status: number, body: Record<string, unknown>) {
  await route.fulfill({ status, json: { apiVersion, ...body } });
}

function row(page: Page, id: string) {
  return page.getByRole("table", { name: "Findings", exact: true }).getByRole("row").filter({ hasText: id === primaryId ? "Primary scanner issue" : "Secondary scanner issue" });
}

test("M06C1 Explicit merge and split retain variants, conflict decisions and refresh Work membership", async ({ page, app }) => {
  expect(app.requests).toEqual([]);
  let state: "separate" | "merged" | "split" = "separate";
  const writes: Array<{ path: string; body: Record<string, unknown> }> = [];
  await page.route("**/api/v1/work**", async (route) => {
    const items = state === "merged"
      ? [workItem(primaryId, "Primary scanner issue", "Synthetic analyst")]
      : [workItem(primaryId, "Primary scanner issue", "Synthetic Admin"),
        workItem(memberId, "Secondary scanner issue", "Synthetic analyst")];
    await fulfill(route, 200, { dataOrigin: "synthetic", items, total: items.length, nextCursor: null });
  });
  await page.route("**/api/v1/findings/**", async (route) => {
    const request = route.request(), url = new URL(request.url()), path = url.pathname, method = request.method();
    const primaryDetail = path === `/api/v1/findings/${primaryId}`;
    const memberDetail = path === `/api/v1/findings/${memberId}`;
    if (method === "GET" && (primaryDetail || memberDetail)) {
      await fulfill(route, 200, { dataOrigin: "synthetic", finding: detail(primaryDetail ? primaryId : memberId, state === "merged") });
      return;
    }
    const body = request.postDataJSON() as Record<string, unknown>;
    writes.push({ path, body });
    if (path === `/api/v1/findings/${primaryId}/merge-previews`) {
      expect(body).toEqual({ otherFindingId: memberId });
      await fulfill(route, 200, { mergePreview: {
        primary: member(primaryId, "scanner-a", "Primary scanner issue"),
        other: member(memberId, "scanner-b", "Secondary scanner issue"),
        conflicts: ["ownerId", "workflowState", "disposition", "acceptedRiskExpiresAt"],
      } });
      return;
    }
    if (path === `/api/v1/findings/${primaryId}/merges`) {
      expect(body.otherFindingId).toBe(memberId);
      expect(body.rationale).toBe("Reviewed synthetic cross-source identity.");
      state = "merged";
      await fulfill(route, 201, { correlation: activeCorrelation() });
      return;
    }
    if (path === `/api/v1/findings/${primaryId}/split-previews`) {
      expect(body).toEqual({ memberFindingId: memberId });
      await fulfill(route, 200, { splitPreview: {
        correlation: activeCorrelation(),
        primary: member(primaryId, "scanner-a", "Primary scanner issue"),
        member: member(memberId, "scanner-b", "Secondary scanner issue"),
      } });
      return;
    }
    if (path === `/api/v1/findings/${primaryId}/splits`) {
      expect(body.memberFindingId).toBe(memberId);
      expect(body.rationale).toBe("Reviewed synthetic split applicability.");
      state = "split";
      await fulfill(route, 201, { correlation: {
        ...activeCorrelation(), state: "split", revision: 2,
        events: [...activeCorrelation().events, { id: splitEventId, type: "split", actorId: adminId,
          rationale: "Reviewed synthetic split applicability.", createdAt: "2026-10-07T16:05:00Z" }],
      } });
      return;
    }
    await route.fallback();
  });

  await page.goto("/#/work");
  await expect(page.getByRole("table", { name: "Findings", exact: true }).getByRole("row")).toHaveCount(3);
  await row(page, primaryId).getByRole("button", { name: "Primary scanner issue", exact: true }).click();
  let dialog = page.getByRole("dialog", { name: "Primary scanner issue", exact: true });
  const correlation = dialog.getByRole("region", { name: "Finding correlation", exact: true });
  await correlation.getByRole("textbox", { name: "Other finding ID", exact: true }).fill(memberId);
  await correlation.getByRole("button", { name: "Preview merge", exact: true }).click();
  await expect(correlation).toContainText("2 observations");
  await expect(correlation).toContainText(/owner.*workflow.*disposition/i);
  await correlation.getByRole("textbox", { name: "Merge rationale", exact: true }).fill("Reviewed synthetic cross-source identity.");
  await correlation.getByRole("button", { name: "Confirm merge", exact: true }).click();
  await expect(correlation).toContainText(correlationId);
  await expect(correlation).toContainText("Reviewed synthetic cross-source identity.");
  await dialog.getByRole("button", { name: "Close finding details", exact: true }).click();
  await expect(page.getByRole("table", { name: "Findings", exact: true }).getByRole("row")).toHaveCount(2);

  await row(page, primaryId).getByRole("button", { name: "Primary scanner issue", exact: true }).click();
  dialog = page.getByRole("dialog", { name: "Primary scanner issue", exact: true });
  const active = dialog.getByRole("region", { name: "Finding correlation", exact: true });
  await active.getByRole("button", { name: "Preview split", exact: true }).click();
  await active.getByRole("textbox", { name: "Split rationale", exact: true }).fill("Reviewed synthetic split applicability.");
  await active.getByRole("button", { name: "Confirm split", exact: true }).click();
  await expect(active.getByRole("button", { name: "Preview merge", exact: true })).toBeVisible();
  await dialog.getByRole("button", { name: "Close finding details", exact: true }).click();
  await expect(page.getByRole("table", { name: "Findings", exact: true }).getByRole("row")).toHaveCount(3);

  expect(writes.map((write) => write.path)).toEqual([
    `/api/v1/findings/${primaryId}/merge-previews`,
    `/api/v1/findings/${primaryId}/merges`,
    `/api/v1/findings/${primaryId}/split-previews`,
    `/api/v1/findings/${primaryId}/splits`,
  ]);
});
