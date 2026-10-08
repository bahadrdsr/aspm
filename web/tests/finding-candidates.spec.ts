import type { Page, Route } from "@playwright/test";
import { alpha, expect, originalAsset, test } from "./application-fixture";
import { apiVersion } from "./api-contract";
import { syntheticSession } from "./fixtures";

test.use({ reducedMotion: "reduce", timezoneId: "UTC" });

const primaryId = "a1000000000000000000000000000001";
const secondId = "b1000000000000000000000000000002";
const thirdId = "c1000000000000000000000000000003";
const correlationId = "d1000000000000000000000000000004";
const adminId = syntheticSession().user.id;
const now = "2026-10-07T20:00:00Z";

type GroupState = "separate" | "two" | "three" | "partial" | "split";

function decision() {
  return {
    ownerId: null, workflowState: "open" as const, disposition: "none" as const,
    acceptedRiskExpiresAt: null, dispositionScope: "" as const,
    suppressionExpiresAt: null, dispositionRationale: "",
  };
}

function member(id: string, sourceId: string, title: string, active = true) {
  return {
    findingId: id, sourceId, title, severity: "medium" as const, active,
    decisionRevision: 1, evidenceRevision: 5, observationCount: 5, noteCount: 0,
    decision: decision(), originalDecision: decision(),
  };
}

function members(state: GroupState) {
  if (state === "two") return [
    member(primaryId, "scanner-a", "Primary exact-location issue"),
    member(secondId, "scanner-b", "Second exact-location issue"),
  ];
  if (state === "three") return [
    member(primaryId, "scanner-a", "Primary exact-location issue"),
    member(secondId, "scanner-b", "Second exact-location issue"),
    member(thirdId, "scanner-c", "Third exact-location issue"),
  ];
  if (state === "partial") return [
    member(primaryId, "scanner-a", "Primary exact-location issue"),
    member(secondId, "scanner-b", "Second exact-location issue", false),
    member(thirdId, "scanner-c", "Third exact-location issue"),
  ];
  return [
    member(primaryId, "scanner-a", "Primary exact-location issue", false),
    member(secondId, "scanner-b", "Second exact-location issue", false),
    member(thirdId, "scanner-c", "Third exact-location issue", false),
  ];
}

function events(state: GroupState) {
  const values = [
    { id: "e1000000000000000000000000000001", type: "merge", rationale: "Start exact-location group." },
    { id: "e2000000000000000000000000000002", type: "merge", rationale: "Add third source." },
    { id: "e3000000000000000000000000000003", type: "split", rationale: "Release second source." },
    { id: "e4000000000000000000000000000004", type: "split", rationale: "Release final source." },
  ] as const;
  const count = state === "two" ? 1 : state === "three" ? 2 : state === "partial" ? 3 : 4;
  return values.slice(0, count).map((event, index) => ({
    ...event, actorId: adminId, createdAt: `2026-10-07T20:0${index}:00Z`, detailAvailability: "available",
  }));
}

function correlation(state: Exclude<GroupState, "separate">) {
  return {
    id: correlationId, workspaceId: alpha.id, primaryFindingId: primaryId,
    state: state === "split" ? "split" : "active", revision: state === "two" ? 1 : state === "three" ? 2 : state === "partial" ? 3 : 4,
    members: members(state), events: events(state),
  };
}

function workItem(id: string, title: string) {
  return {
    id, title, assetName: originalAsset.name, severity: "medium", ownerName: null, workflowState: "open",
    sourceScanAt: now, collectedAt: now, importedAt: now,
  };
}

function detail(state: GroupState) {
  return {
    ...workItem(primaryId, "Primary exact-location issue"),
    scopeLabel: "candidate-scope / main (revision 1)", description: "Primary candidate detail.",
    remediation: "Review exact source identity.", assetId: originalAsset.id, workspaceId: alpha.id,
    evidence: { text: "primary evidence", sourceLabel: "Scanner A", verificationState: "not-run" },
    ownerId: null, sourceState: "observed", sourceFreshnessAt: now, disposition: "none",
    acceptedRiskExpiresAt: null, riskAcceptanceExpired: false, verifiedResolution: false,
    decisionRevision: 1, evidenceRevision: 5, notes: [],
    observations: [{
      id: "f1000000000000000000000000000001", runId: "candidate-run", sourceId: "scanner-a", scanId: "scan-a",
      scope: { id: "candidate-scope", revision: "1", branch: "main" }, sourceScanAt: now,
      sourceFindingId: "A-1", sourceSeverity: "warning", normalizedSeverity: "medium",
      sourceLocation: { uri: "src/shared-candidate.go", line: 42 }, impact: "", remediation: "",
      unmapped: {}, evidenceDigest: `sha256:${"1".repeat(64)}`, evidenceAvailability: "available",
    }],
    notesNextCursor: null, observationsNextCursor: null,
    ...(["two", "three", "partial"].includes(state) ? { correlation: correlation(state as "two" | "three" | "partial") } : {}),
  };
}

async function fulfill(route: Route, status: number, body: Record<string, unknown>) {
  await route.fulfill({ status, json: { apiVersion, ...body } });
}

function row(page: Page, title: string) {
  return page.getByRole("table", { name: "Findings", exact: true }).getByRole("row").filter({ hasText: title });
}

test("M06C2 Bounded candidates add a third source and release members independently", async ({ page, app }) => {
  expect(app.requests).toEqual([]);
  let state: GroupState = "separate";
  const writes: Array<{ path: string; body: Record<string, unknown> }> = [];
  await page.route("**/api/v1/work**", async (route) => {
    const items = [workItem(primaryId, "Primary exact-location issue")];
    if (!["two", "three"].includes(state)) items.push(workItem(secondId, "Second exact-location issue"));
    if (!["three", "partial"].includes(state)) items.push(workItem(thirdId, "Third exact-location issue"));
    await fulfill(route, 200, { dataOrigin: "synthetic", items, total: items.length, nextCursor: null });
  });
  await page.route("**/api/v1/findings/**", async (route) => {
    const request = route.request(), url = new URL(request.url()), path = url.pathname, method = request.method();
    if (method === "GET" && path === `/api/v1/findings/${primaryId}`) {
      await fulfill(route, 200, { dataOrigin: "synthetic", finding: detail(state) });
      return;
    }
    if (method === "GET" && path === `/api/v1/findings/${primaryId}/correlation-candidates`) {
      const available = state === "separate"
        ? [member(secondId, "scanner-b", "Second exact-location issue"),
          member(thirdId, "scanner-c", "Third exact-location issue")]
        : state === "two" ? [member(thirdId, "scanner-c", "Third exact-location issue")] : [];
      await fulfill(route, 200, { correlationCandidates: {
        items: available.map((value) => ({
          member: value, match: { kind: "exact-location", branch: "main", uri: "src/shared-candidate.go", line: 42 },
        })),
        nextCursor: null,
      } });
      return;
    }
    const body = request.postDataJSON() as Record<string, unknown>;
    writes.push({ path, body });
    if (path === `/api/v1/findings/${primaryId}/merge-previews`) {
      const target = body.otherFindingId as string;
      await fulfill(route, 200, { mergePreview: {
        primary: member(primaryId, "scanner-a", "Primary exact-location issue"),
        other: target === secondId ? member(secondId, "scanner-b", "Second exact-location issue") :
          member(thirdId, "scanner-c", "Third exact-location issue"),
        conflicts: [], correlation: state === "two" ? correlation("two") : null,
      } });
      return;
    }
    if (path === `/api/v1/findings/${primaryId}/merges`) {
      if (state === "separate") {
        expect(body).toMatchObject({ otherFindingId: secondId, correlationRevision: 0 });
        state = "two";
      } else {
        expect(body).toMatchObject({ otherFindingId: thirdId, correlationRevision: 1 });
        state = "three";
      }
      await fulfill(route, 201, { correlation: correlation(state as "two" | "three") });
      return;
    }
    if (path === `/api/v1/findings/${primaryId}/split-previews`) {
      const target = body.memberFindingId as string;
      await fulfill(route, 200, { splitPreview: {
        correlation: correlation(state as "three" | "partial"),
        primary: member(primaryId, "scanner-a", "Primary exact-location issue"),
        member: target === secondId ? member(secondId, "scanner-b", "Second exact-location issue") :
          member(thirdId, "scanner-c", "Third exact-location issue"),
      } });
      return;
    }
    if (path === `/api/v1/findings/${primaryId}/splits`) {
      if (state === "three") {
        expect(body.memberFindingId).toBe(secondId);
        state = "partial";
      } else {
        expect(body.memberFindingId).toBe(thirdId);
        state = "split";
      }
      await fulfill(route, 201, { correlation: correlation(state as "partial" | "split") });
      return;
    }
    await route.fallback();
  });

  await page.goto("/#/work");
  await row(page, "Primary exact-location issue").getByRole("button", { name: "Primary exact-location issue", exact: true }).click();
  let panel = page.getByRole("dialog", { name: "Primary exact-location issue", exact: true })
    .getByRole("region", { name: "Finding correlation", exact: true });
  await panel.getByRole("button", { name: "Find exact-location candidates", exact: true }).click();
  const candidates = panel.getByRole("list", { name: "Correlation candidates", exact: true });
  await expect(candidates).toContainText("src/shared-candidate.go:42");
  await candidates.getByRole("button", { name: "Preview candidate Second exact-location issue", exact: true }).click();
  await panel.getByLabel("Merge rationale", { exact: true }).fill("Start exact-location group.");
  await panel.getByRole("button", { name: "Confirm merge", exact: true }).click();
  await expect(panel).toContainText("revision 1");

  await panel.getByRole("button", { name: "Find exact-location candidates", exact: true }).click();
  await panel.getByRole("button", { name: "Preview candidate Third exact-location issue", exact: true }).click();
  await expect(panel).toContainText("3 source variants");
  await panel.getByLabel("Merge rationale", { exact: true }).fill("Add third source.");
  await panel.getByRole("button", { name: "Confirm merge", exact: true }).click();
  await expect(panel).toContainText("revision 2");
  await expect(panel).toContainText("5 observations");

  await panel.getByRole("button", { name: "Preview release Second exact-location issue", exact: true }).click();
  await expect(panel).toContainText("remaining source group stays active");
  await panel.getByLabel("Split rationale", { exact: true }).fill("Release second source.");
  await panel.getByRole("button", { name: "Confirm split", exact: true }).click();
  await expect(panel).toContainText("Released source variant");
  await expect(panel).toContainText("revision 3");

  await panel.getByRole("button", { name: "Preview release Third exact-location issue", exact: true }).click();
  await expect(panel).toContainText("closes the source group");
  await panel.getByLabel("Split rationale", { exact: true }).fill("Release final source.");
  await panel.getByRole("button", { name: "Confirm split", exact: true }).click();
  await expect(panel.getByRole("button", { name: "Find exact-location candidates", exact: true })).toBeVisible();
  await page.getByRole("dialog", { name: "Primary exact-location issue", exact: true })
    .getByRole("button", { name: "Close finding details", exact: true }).click();
  await expect(page.getByRole("table", { name: "Findings", exact: true }).getByRole("row")).toHaveCount(4);

  expect(writes.map((write) => write.path)).toEqual([
    `/api/v1/findings/${primaryId}/merge-previews`,
    `/api/v1/findings/${primaryId}/merges`,
    `/api/v1/findings/${primaryId}/merge-previews`,
    `/api/v1/findings/${primaryId}/merges`,
    `/api/v1/findings/${primaryId}/split-previews`,
    `/api/v1/findings/${primaryId}/splits`,
    `/api/v1/findings/${primaryId}/split-previews`,
    `/api/v1/findings/${primaryId}/splits`,
  ]);
});
