import { apiVersion } from "./api-contract";
import type { CatalogResponse, ErrorResponse, FindingResponse, WorkItem, WorkResponse } from "./api-contract";

export function syntheticSession() {
  return {
    apiVersion,
    user: { id: "aaaaaaaa-aaaa-4aaa-8aaa-aaaaaaaaaaaa", name: "Synthetic analyst", email: "analyst@synthetic.invalid" },
    workspaces: [{ id: "11111111-1111-4111-8111-111111111111", name: "Synthetic Alpha workspace", role: "admin" }],
    expiresAt: new Date(Date.now() + 3_600_000).toISOString(),
  };
}

export const workItems: WorkItem[] = [
  {
    id: "synthetic-finding-001",
    title: "Synthetic dependency warning",
    assetName: "alpha-repository",
    severity: "high",
    ownerName: null,
    workflowState: "open",
    sourceScanAt: "2026-09-01T08:00:00Z",
    collectedAt: "2026-09-02T10:00:00Z",
    importedAt: "2026-09-02T10:01:00Z",
  },
  {
    id: "synthetic-finding-002",
    title: "Synthetic configuration observation",
    assetName: "alpha-repository",
    severity: "medium",
    ownerName: "Synthetic owner",
    workflowState: "in-progress",
    sourceScanAt: "2026-09-01T08:00:00Z",
    collectedAt: "2026-09-02T10:00:00Z",
    importedAt: "2026-09-02T10:01:00Z",
  },
  {
    id: "synthetic-finding-003",
    title: "Synthetic policy observation",
    assetName: "beta-repository",
    severity: "low",
    ownerName: null,
    workflowState: "open",
    sourceScanAt: null,
    collectedAt: "2026-09-02T10:00:00Z",
    importedAt: "2026-09-02T10:01:00Z",
  },
];

export function workResponse(query = "", items = workItems): WorkResponse {
  const matching = items.filter((item) =>
    `${item.title} ${item.assetName} ${item.ownerName ?? ""}`.toLowerCase().includes(query.toLowerCase()),
  );
  return { apiVersion, dataOrigin: "synthetic", items: matching, total: matching.length, nextCursor: null };
}

export const findingResponse: FindingResponse = {
  apiVersion,
  dataOrigin: "synthetic",
  finding: {
    ...workItems[0],
    scopeLabel: "Synthetic development workspace",
    description: "A synthetic observation for design-system acceptance. No vulnerability has been verified.",
    evidence: {
      text: "Synthetic evidence: <em>display literally, not as markup</em>.",
      sourceLabel: "Synthetic report fixture",
      verificationState: "not-run",
    },
    remediation: "Review the synthetic fixture context. No target, command, or exploit is provided.",
  },
};

export const catalogResponse: CatalogResponse = {
  apiVersion,
  dataOrigin: "synthetic",
  items: [
    { id: "github", name: "GitHub", capabilities: ["repository-inventory", "code-scanning-intake"] },
    { id: "gitlab", name: "GitLab", capabilities: ["repository-inventory", "security-report-intake"] },
    { id: "azure-devops", name: "Azure DevOps", capabilities: ["pipeline-inventory", "security-report-intake"] },
    { id: "aws", name: "AWS", capabilities: ["cloud-inventory", "security-hub-intake"] },
    { id: "azure", name: "Azure", capabilities: ["cloud-inventory", "defender-for-cloud-intake"] },
    { id: "jira", name: "Jira", capabilities: ["work-item-create", "status-linkage"] },
    { id: "teams", name: "Microsoft Teams", capabilities: ["notifications", "deep-links"] },
    { id: "slack", name: "Slack", capabilities: ["notifications", "deep-links"] },
  ].map((item) => ({
    ...item,
    id: item.id as CatalogResponse["items"][number]["id"],
    kind: "native",
    supportMaturity: "planned",
    connectionState: "unconfigured",
    readyToConnect: false,
    liveVerification: {
      state: "not-run",
      reason: "Synthetic M01 catalog fixture, not an authorized live-endpoint verification.",
    },
  })),
};

export function apiError(status: 403 | 404 | 503): ErrorResponse {
  return {
    apiVersion,
    error: {
      code: status === 403 ? "forbidden" : status === 404 ? "not-found" : "unavailable",
      message: status === 403
        ? "You do not have permission to view these findings."
        : status === 404 ? "The requested synthetic record was not found." : "Findings are temporarily unavailable.",
      requestId: "synthetic-request-001",
      retryable: status === 503,
    },
  };
}
