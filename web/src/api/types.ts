export const apiVersion = "aspm/v1alpha1" as const;
export type DataOrigin = "synthetic" | "live";
export type Severity = "critical" | "high" | "medium" | "low" | "info";
export type WorkflowState = "open" | "in-progress" | "resolved";
export type FindingDisposition = "none" | "accepted-risk";

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
  disposition?: FindingDisposition;
  acceptedRiskExpiresAt?: string | null;
  riskAcceptanceExpired?: boolean;
  verifiedResolution?: boolean;
  decisionRevision?: number;
  evidenceRevision?: number;
  correlation?: FindingCorrelation;
  notes?: FindingNote[];
  observations?: Observation[];
  notesNextCursor?: string | null;
  observationsNextCursor?: string | null;
}

export interface FindingResponse {
  apiVersion: typeof apiVersion;
  dataOrigin: DataOrigin;
  finding: FindingDetail;
}

export interface FindingPatch {
  ownerId?: string | null;
  workflowState?: WorkflowState;
  disposition?: FindingDisposition;
  acceptedRiskExpiresAt?: string | null;
}

export interface FindingNote { id: string; text: string }
export interface FindingNoteResponse { apiVersion: typeof apiVersion; note: FindingNote }

export interface FindingDecision {
  ownerId: string | null;
  workflowState: WorkflowState;
  disposition: FindingDisposition;
  acceptedRiskExpiresAt: string | null;
}

export interface FindingCorrelationMember {
  findingId: string;
  sourceId: string;
  title: string;
  severity: Severity;
  decisionRevision: number;
  evidenceRevision: number;
  observationCount: number;
  noteCount: number;
  decision: FindingDecision;
  originalDecision: FindingDecision;
}

export interface FindingCorrelationEvent {
  id: string;
  type: "merge" | "split";
  actorId: string;
  rationale: string;
  createdAt: string;
}

export interface FindingCorrelation {
  id: string;
  workspaceId: string;
  primaryFindingId: string;
  state: "active" | "split";
  revision: number;
  members: FindingCorrelationMember[];
  events: FindingCorrelationEvent[];
}

export interface FindingMergePreview {
  primary: FindingCorrelationMember;
  other: FindingCorrelationMember;
  conflicts: Array<"ownerId" | "workflowState" | "disposition" | "acceptedRiskExpiresAt">;
}

export interface FindingSplitPreview {
  correlation: FindingCorrelation;
  primary: FindingCorrelationMember;
  member: FindingCorrelationMember;
}

export interface FindingMergeInput {
  otherFindingId: string;
  primaryDecisionRevision: number;
  primaryEvidenceRevision: number;
  otherDecisionRevision: number;
  otherEvidenceRevision: number;
  decision: FindingDecision;
  rationale: string;
  idempotencyKey: string;
}

export interface FindingSplitInput {
  memberFindingId: string;
  correlationRevision: number;
  primaryDecisionRevision: number;
  primaryEvidenceRevision: number;
  memberDecisionRevision: number;
  memberEvidenceRevision: number;
  primaryDecision: FindingDecision;
  memberDecision: FindingDecision;
  rationale: string;
  idempotencyKey: string;
}

export interface FindingMergePreviewResponse {
  apiVersion: typeof apiVersion;
  mergePreview: FindingMergePreview;
}

export interface FindingSplitPreviewResponse {
  apiVersion: typeof apiVersion;
  splitPreview: FindingSplitPreview;
}

export interface FindingCorrelationResponse {
  apiVersion: typeof apiVersion;
  correlation: FindingCorrelation;
}

export type RetentionClass = "hot-history" | "archived-evidence" | "raw-report" | "audit";
export type RetentionResourceKind = "import" | "observation" | "correlation-event";
export type RetentionAction = "archive-history" | "expire-archive" | "expire-raw-report" | "archive-audit";

export interface RetentionPolicy {
  workspaceId: string;
  revision: number;
  hotHistoryDays: number;
  rawReportDays: number;
  archivedEvidenceDays: number;
  auditDays: number;
  updatedBy: string | null;
  updatedAt: string | null;
}

export interface RetentionPolicyInput {
  revision: number;
  hotHistoryDays: number;
  rawReportDays: number;
  archivedEvidenceDays: number;
  auditDays: number;
}

export interface RetentionPolicyResponse {
  apiVersion: typeof apiVersion;
  retentionPolicy: RetentionPolicy;
}

export interface RetentionHold {
  id: string;
  workspaceId: string;
  resourceKind: RetentionResourceKind;
  resourceId: string;
  reason: string;
  revision: number;
  createdBy: string;
  createdAt: string;
  releasedBy: string | null;
  releasedAt: string | null;
  releaseRationale: string | null;
}

export interface RetentionHoldResponse {
  apiVersion: typeof apiVersion;
  retentionHold: RetentionHold;
}

export interface RetentionHoldsResponse {
  apiVersion: typeof apiVersion;
  retentionHolds: RetentionHold[];
}

export interface RetentionClassSummary {
  class: RetentionClass;
  action: RetentionAction;
  retainDays: number;
  totalCount: number;
  eligibleCount: number;
  protectedCount: number;
  sizeBytes: number;
}

export interface RetentionPreviewItem {
  class: RetentionClass;
  resourceKind: RetentionResourceKind;
  resourceId: string;
  action: RetentionAction;
  observedAt: string;
  sizeBytes: number;
  protectedReasons: Array<
    "legal-hold" | "active-decision" | "shared-observation-references" |
    "assessment-reference" | "active-correlation"
  >;
}

export interface RetentionPreview {
  id: string;
  workspaceId: string;
  revision: number;
  state: "ready" | "approved" | "stale";
  policyRevision: number;
  snapshotDigest: string;
  createdBy: string;
  createdAt: string;
  expiresAt: string;
  summaries: RetentionClassSummary[];
  items: RetentionPreviewItem[];
  approvedBy: string | null;
  approvedAt: string | null;
  approvalRationale: string | null;
}

export interface RetentionPreviewResponse {
  apiVersion: typeof apiVersion;
  retentionPreview: RetentionPreview;
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

export const importFormats = ["sarif", "trivy", "zap", "gitleaks", "generic-json", "generic-csv", "manual"] as const;
export type ImportFormat = typeof importFormats[number];
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

export interface PostureReport {
  workspaceId: string;
  asOf: string;
  totals: {
    assets: number;
    findings: number;
    openFindings: number;
    acceptedRisk: number;
    expiredAcceptedRisk: number;
    inferredResolved: number;
    verifiedResolved: number;
  };
  bySeverity: Record<Severity, number>;
  coverage: {
    scannedAssets: number;
    unscannedAssets: number;
    staleAssets: number;
    unknownFreshnessAssets: number;
    freshnessWindowDays: number;
  };
  freshnessWindow: { from: string; to: string; days: number };
  verification: { state: "not-run"; reason: string };
}

export interface ReportOverviewResponse {
  apiVersion: typeof apiVersion;
  dataOrigin: DataOrigin;
  report: PostureReport;
}

export type ReportSnapshotState = "queued" | "processing" | "succeeded" | "failed";
export interface ReportSnapshotInput { name: string; freshnessDays: number }
export interface ReportSnapshotSummary extends ReportSnapshotInput {
  id: string;
  workspaceId: string;
  requestedBy: string;
  state: ReportSnapshotState;
  createdAt: string;
  completedAt: string | null;
  failure: { code: string; message: string; retryable: boolean } | null;
}
export interface ReportSnapshot extends ReportSnapshotSummary { report: PostureReport | null }
export interface ReportSnapshotResponse {
  apiVersion: typeof apiVersion;
  dataOrigin: DataOrigin;
  snapshot: ReportSnapshot;
}
export interface ReportSnapshotsResponse {
  apiVersion: typeof apiVersion;
  dataOrigin: DataOrigin;
  items: ReportSnapshotSummary[];
  total: number;
  nextCursor: string | null;
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

export interface SlackConnection {
  id: string;
  workspaceId: string;
  profile: "slack-workspace-bot";
  name: string;
  channel: string;
  enabled: boolean;
  credentialConfigured: boolean;
  revision: number;
  createdAt: string;
  updatedAt: string;
}

export interface SlackConnectionInput {
  profile: "slack-workspace-bot";
  name: string;
  channel: string;
  token: string;
  enabled: boolean;
}
export type SlackConnectionPatch = Partial<Omit<SlackConnectionInput, "profile">>;
export interface SlackConnectionResponse {
  apiVersion: typeof apiVersion;
  dataOrigin?: DataOrigin;
  connection: SlackConnection;
}
export interface SlackPage<T> {
  apiVersion: typeof apiVersion;
  dataOrigin?: DataOrigin;
  items: T[];
  total: number;
  nextCursor: string | null;
}

export type FindingDeliveryState = "queued" | "dispatching" | "confirmed" | "accepted" | "blocked" | "failed" | "rate-limited" | "uncertain";
export interface FindingDelivery {
  id: string;
  workspaceId: string;
  findingId: string;
  connectionId: string;
  connectionRevision: number;
  profile: "slack-workspace-bot";
  channel: string;
  requestedBy: string;
  state: FindingDeliveryState;
  payload: { title: string; body: string; deepLink: string };
  createdAt: string;
  dispatchStartedAt: string | null;
  completedAt: string | null;
  receipt: { remoteId: string; remoteUrl: string } | null;
  failure: { code: string; nativeCode: string; httpStatus: number; retryAfterSeconds: number; retryable: false } | null;
}
export interface FindingDeliveryInput { connectionId: string; idempotencyKey: string }
export interface FindingDeliveryResponse {
  apiVersion: typeof apiVersion;
  dataOrigin?: DataOrigin;
  delivery: FindingDelivery;
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
