import type { Route } from "@playwright/test";
import type { AzureDevOpsSourceCollection } from "../src/api/source-types";
import { requireProductionUI } from "./network";
import { apiVersion, nativeID, sourceAlpha, sourceUser, sourcesPath } from "./source-ui-data";
import { expect, test } from "./source-ui-fixture";

test.use({ reducedMotion: "reduce", timezoneId: "UTC" });

const profile = "ado-services-build-artifacts";
const pat = "SYNTHETIC-ADO-PAT-NOT-A-LIVE-CREDENTIAL";
const target = {
  organization: "owned-org",
  projectId: "11111111-2222-4333-8444-aaaaaaaaaaaa",
  repositoryId: "55555555-6666-4777-8888-bbbbbbbbbbbb",
};
const selection = { buildId: "81", artifactName: "aspm-report", artifactPath: "reports/report.sarif" };
const source = {
  id: nativeID("a1", 1), workspaceId: sourceAlpha.id, profile, name: "Owned Azure DevOps report source",
  enabled: true, credentialConfigured: true, revision: 1,
  createdAt: "2026-10-07T12:00:00Z", updatedAt: "2026-10-07T12:00:00Z", azureDevOps: target,
};
const collectionId = nativeID("a2", 1);
const assetId = nativeID("a3", 1);
const collectedAt = "2026-10-07T12:01:00Z";
const completedAt = "2026-10-07T12:01:01Z";
const baseCollection: AzureDevOpsSourceCollection = {
  id: collectionId, workspaceId: sourceAlpha.id, sourceId: source.id, profile,
  connectionRevision: 1, requestedBy: sourceUser.id, state: "queued", complete: false,
  assetId: null, repositoryId: null, recordCount: 0, gaps: [],
  createdAt: "2026-10-07T12:00:30Z", collectedAt: null, completedAt: null, failure: null,
  azureDevOps: target, selection,
};
const recordIds = [nativeID("a4", 1), nativeID("a4", 2), nativeID("a4", 3), nativeID("a4", 4)];
const records = [
  { kind: "repository", externalId: target.repositoryId, parentId: "", nativeRunId: "" },
  { kind: "pipeline", externalId: selection.buildId, parentId: target.repositoryId, nativeRunId: selection.buildId },
  { kind: "artifact", externalId: "9", parentId: target.repositoryId, nativeRunId: selection.buildId },
  { kind: "report", externalId: `9:${selection.artifactPath}`, parentId: target.repositoryId, nativeRunId: selection.buildId },
].map((value, ordinal) => ({
  id: recordIds[ordinal], collectionId, ordinal, ...value, state: ordinal === 1 ? "completed" : "",
  severity: "", location: "", rawURL: `https://dev.azure.com/${target.organization}/${target.projectId}/_apis/synthetic/${ordinal}`,
  sourceScanAt: null, sourceUpdatedAt: null,
  evidence: { sha256: `sha256:${String(ordinal + 1).repeat(64)}`, sizeBytes: 128 + ordinal },
}));

test("ADO1 Explicit Azure DevOps setup, selected collection and reviewed SARIF intake stay separate", async ({ page, sources }) => {
  requireProductionUI();
  const calls: Array<{ method: string; path: string; query: string; body: Record<string, unknown> }> = [];
  let configured = false;
  let collection = structuredClone(baseCollection);
  const importId = nativeID("a5", 1);

  await page.route("**/api/v1/sources**", async (route: Route) => {
    const request = route.request(), url = new URL(request.url());
    const method = request.method(), path = url.pathname;
    let body: Record<string, unknown> = {};
    if (method === "POST" || method === "PATCH") {
      const parsed: unknown = request.postDataJSON();
      if (parsed && typeof parsed === "object" && !Array.isArray(parsed)) body = parsed as Record<string, unknown>;
    }
    const isADO = url.searchParams.get("profile") === profile ||
      path.includes(source.id) || path.includes(collectionId) || recordIds.some((id) => path.includes(id)) ||
      method === "POST" && path === sourcesPath && body.profile === profile;
    if (!isADO) { await route.fallback(); return; }
    calls.push({ method, path, query: url.search, body });
    const response = (status: number, value: Record<string, unknown>) => route.fulfill({ status, json: { apiVersion, ...value } });
    if (method === "GET" && path === sourcesPath && url.searchParams.get("profile") === profile) {
      await response(200, { items: configured ? [source] : [], total: configured ? 1 : 0, nextCursor: null }); return;
    }
    if (method === "POST" && path === sourcesPath && body.profile === profile) {
      expect(body).toEqual({ profile, name: source.name, enabled: true, token: pat, azureDevOps: target });
      configured = true; await response(201, { source }); return;
    }
    if (path === `${sourcesPath}/${source.id}/collections` && method === "GET") {
      await response(200, { items: collection.id ? [collection] : [], total: collection.id ? 1 : 0, nextCursor: null }); return;
    }
    if (path === `${sourcesPath}/${source.id}/collections` && method === "POST") {
      expect(body).toEqual(expect.objectContaining(selection));
      expect(Object.keys(body).sort()).toEqual(["artifactName", "artifactPath", "buildId", "idempotencyKey"]);
      await response(202, { collection }); return;
    }
    if (path === `${sourcesPath}/collections/${collectionId}` && method === "GET") {
      await response(200, { collection }); return;
    }
    if (path === `${sourcesPath}/collections/${collectionId}/records` && method === "GET") {
      await response(200, { items: records, total: records.length, nextCursor: null }); return;
    }
    if (path === `${sourcesPath}/collections/${collectionId}/records/${recordIds[3]}/imports` && method === "POST") {
      expect(body).toEqual({
        apiVersion, format: "sarif",
        scope: { id: "selected-security-scope", revision: "source-revision-1", branch: "refs/heads/main" },
        sourceStatus: "succeeded", scanKind: "delta", completeness: "unknown",
      });
      await response(202, { import: {
        id: importId, runId: nativeID("a6", 1), state: "queued", assetId, format: "sarif",
        sourceId: `${profile}:${"1".repeat(64)}`, scanId: `ado-report-v1:${"2".repeat(64)}`,
        scope: body.scope, sourceScanAt: null, collectedAt, importedAt: "2026-10-07T12:02:00Z",
        reportDigest: `sha256:${"3".repeat(64)}`, observationCount: 0, failure: null,
      } }); return;
    }
    await response(404, { error: { code: "not-found", message: "Synthetic ADO route not found", requestId: "ado-ui", retryable: false } });
  });

  await page.goto("/#/integrations");
  await page.getByRole("button", { name: "Azure DevOps sources", exact: true }).click();
  const panel = page.getByRole("region", { name: "Azure DevOps sources", exact: true });
  await expect(panel).toBeVisible();
  await expect(panel.getByText("No Azure DevOps sources", { exact: true })).toBeVisible();

  await panel.getByRole("button", { name: "Add Azure DevOps source", exact: true }).click();
  const editor = page.getByRole("dialog", { name: "Create Azure DevOps source", exact: true });
  await editor.getByRole("textbox", { name: "Name", exact: true }).fill(source.name);
  await editor.getByRole("textbox", { name: "Organization", exact: true }).fill(target.organization);
  await editor.getByRole("textbox", { name: "Project UUID", exact: true }).fill(target.projectId);
  await editor.getByRole("textbox", { name: "Repository UUID", exact: true }).fill(target.repositoryId);
  await editor.getByLabel("Personal access token", { exact: true }).fill(pat);
  await editor.getByRole("combobox", { name: "Enabled", exact: true }).selectOption("true");
  await editor.getByRole("button", { name: "Create source", exact: true }).click();
  await expect(panel.getByText(source.name, { exact: true })).toBeVisible();
  await expect(page.locator("body")).not.toContainText(pat);

  const row = panel.getByRole("row").filter({ hasText: source.name });
  await row.getByRole("button", { name: "Collections", exact: true }).click();
  const history = page.getByRole("region", { name: "Azure DevOps source collections", exact: true });
  await history.getByRole("button", { name: "Collect report", exact: true }).click();
  const confirmation = page.getByRole("dialog", { name: "Collect Azure DevOps report", exact: true });
  await confirmation.getByRole("textbox", { name: "Build ID", exact: true }).fill(selection.buildId);
  await confirmation.getByRole("textbox", { name: "Artifact name", exact: true }).fill(selection.artifactName);
  await confirmation.getByRole("textbox", { name: "Report path inside artifact ZIP", exact: true }).fill(selection.artifactPath);
  await confirmation.getByRole("button", { name: "Queue selected report", exact: true }).click();
  const detail = page.getByRole("region", { name: "Selected collection", exact: true });
  await expect(detail.getByRole("status", { name: "Collection status", exact: true })).toContainText("Queued");

  collection = {
    ...collection, state: "succeeded", complete: true, assetId, repositoryId: target.repositoryId,
    recordCount: 4, collectedAt, completedAt,
  };
  await detail.getByRole("button", { name: "Refresh collection", exact: true }).click();
  const recordPanel = page.getByRole("region", { name: "Records", exact: true });
  const reportRow = recordPanel.getByRole("row").filter({ hasText: recordIds[3] });
  await reportRow.getByRole("button", { name: "Import SARIF", exact: true }).click();
  const intake = page.getByRole("dialog", { name: "Import collected SARIF", exact: true });
  await intake.getByRole("textbox", { name: "Scope ID", exact: true }).fill("selected-security-scope");
  await intake.getByRole("textbox", { name: "Scope revision", exact: true }).fill("source-revision-1");
  await intake.getByRole("textbox", { name: "Branch", exact: true }).fill("refs/heads/main");
  await intake.getByRole("combobox", { name: "Source status", exact: true }).selectOption("succeeded");
  await intake.getByRole("combobox", { name: "Scan kind", exact: true }).selectOption("delta");
  await intake.getByRole("combobox", { name: "Completeness", exact: true }).selectOption("unknown");
  await intake.getByRole("button", { name: "Import collected report", exact: true }).click();
  await expect(page.getByRole("region", { name: "Latest report import", exact: true })
    .getByRole("status", { name: "Import status", exact: true })).toContainText("Queued");

  expect(calls.some((call) => call.method === "GET" && call.path === sourcesPath && call.query.includes(`profile=${profile}`))).toBe(true);
  expect(calls.filter((call) => call.method === "POST")).toHaveLength(3);
  expect(page.url()).not.toContain(pat);
  await sources.assertPrivate(page);
});
