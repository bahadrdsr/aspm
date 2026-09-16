export const apiVersion = "aspm/v1alpha1" as const;
export type DataOrigin = "synthetic" | "live";
export type Severity = "critical" | "high" | "medium" | "low" | "info";
export type WorkflowState = "open" | "in-progress" | "resolved";

export interface Membership {
  id: string;
  name: string;
  role: "admin" | "analyst" | "viewer";
}

export interface Session {
  apiVersion: typeof apiVersion;
  user: { id: string; name: string; email: string };
  workspaces: Membership[];
  expiresAt: string;
}

export interface WorkItem {
  id: string;
  title: string;
  assetName: string;
  severity: Severity;
  ownerName: string | null;
  workflowState: WorkflowState;
  sourceScanAt: string | null;
  collectedAt: string;
  importedAt: string;
}

export interface WorkResponse {
  apiVersion: typeof apiVersion;
  dataOrigin: DataOrigin;
  items: WorkItem[];
  total: number;
  nextCursor: string | null;
}

export interface SourceScope { id: string; revision: string; branch: string }
export type JSONValue = null | boolean | number | string | JSONValue[] | { [key: string]: JSONValue };

export interface Observation {
  id: string;
  runId: string;
  sourceId: string;
  scanId: string;
  scope: SourceScope;
  sourceScanAt: string | null;
  sourceFindingId: string;
  sourceSeverity: string;
  normalizedSeverity: Severity;
  sourceLocation: { uri: string; line: number };
  impact: string;
  remediation: string;
  unmapped: Record<string, JSONValue>;
  evidenceDigest: string;
}

export interface FindingDetail extends WorkItem {
  scopeLabel: string;
  description: string;
  evidence: {
    text: string;
    sourceLabel: string;
    verificationState: "not-run" | "blocked" | "verified";
  };
  remediation: string;
  assetId?: string;
  workspaceId?: string;
  ownerId?: string | null;
  sourceState?: "observed" | "unknown" | "stale" | "inferred-resolved";
  sourceFreshnessAt?: string | null;
  disposition?: "none" | "accepted-risk";
  acceptedRiskExpiresAt?: string | null;
  riskAcceptanceExpired?: boolean;
  verifiedResolution?: boolean;
  notes?: { id: string; text: string }[];
  observations?: Observation[];
  notesNextCursor?: string | null;
  observationsNextCursor?: string | null;
}

export interface FindingResponse {
  apiVersion: typeof apiVersion;
  dataOrigin: DataOrigin;
  finding: FindingDetail;
}

export interface AssetFields {
  name: string;
  kind: string;
  environment: string;
  criticality: "low" | "medium" | "high" | "critical";
  tags: string[];
  ownerId: string | null;
}

export interface Asset extends AssetFields { id: string; workspaceId: string }
export interface AssetResponse { apiVersion: typeof apiVersion; asset: Asset }
export interface AssetsResponse {
  apiVersion: typeof apiVersion;
  items: Asset[];
  total: number;
  nextCursor: string | null;
}

export type ImportFormat = "sarif" | "generic-json";
export type ImportState = "queued" | "processing" | "succeeded" | "failed";
export interface ImportMapping {
  sourceFindingId: string;
  title: string;
  sourceSeverity: string;
  sourceLocation: string;
  sourceLine: string;
  impact: string;
  remediation: string;
  description: string;
}
export interface ImportInput {
  apiVersion: typeof apiVersion;
  assetId: string;
  format: ImportFormat;
  report: string;
  sourceId: string;
  scanId: string;
  scope: SourceScope;
  sourceScanAt: string | null;
  collectedAt: string;
  sourceStatus: "succeeded" | "failed";
  scanKind: "full" | "delta";
  completeness: "complete" | "partial" | "unknown";
  mapping?: ImportMapping;
}
export interface ImportReceipt {
  id: string;
  runId: string;
  state: ImportState;
  assetId: string;
  format: string;
  sourceId: string;
  scanId: string;
  scope: SourceScope;
  sourceScanAt: string | null;
  collectedAt: string;
  importedAt: string;
  reportDigest: string;
  observationCount: number;
  failure: { code: string; message: string } | null;
}

export type IntegrationId = "github" | "gitlab" | "azure-devops" | "aws" | "azure" | "jira" | "teams" | "slack";
export interface IntegrationSummary {
  id: IntegrationId;
  name: string;
  kind: "native";
  capabilities: string[];
  supportMaturity: "planned" | "experimental" | "supported";
  connectionState: "unconfigured" | "healthy" | "stale" | "failed" | "partially-authorized";
  readyToConnect: boolean;
  liveVerification: {
    state: "not-run" | "blocked" | "passed" | "failed";
    reason: string;
  };
}

export interface CatalogResponse {
  apiVersion: typeof apiVersion;
  dataOrigin: DataOrigin;
  items: IntegrationSummary[];
}

export interface ErrorResponse {
  apiVersion: typeof apiVersion;
  error: {
    code: "unauthorized" | "forbidden" | "not-found" | "unavailable" | "invalid-input" | "conflict" | "replay-expired" | "too-large" | "unsupported-format" | "method-not-allowed";
    message: string;
    requestId: string;
    retryable: boolean;
  };
}
