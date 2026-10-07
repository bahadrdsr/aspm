import type { apiVersion, DataOrigin, ImportReceipt, SourceScope } from "./types";

export interface SourceConnection {
  id: string;
  workspaceId: string;
  profile: "github-cloud-app";
  name: string;
  repository: string;
  enabled: boolean;
  credentialConfigured: boolean;
  revision: number;
  createdAt: string;
  updatedAt: string;
}
export interface SourceInput {
  profile: "github-cloud-app";
  name: string;
  repository: string;
  token: string;
  enabled: boolean;
}
export type SourcePatch = Partial<Omit<SourceInput, "profile">>;
export interface SourceResponse {
  apiVersion: typeof apiVersion;
  dataOrigin?: DataOrigin;
  source: SourceConnection;
}
export interface AzureDevOpsTarget {
  organization: string;
  projectId: string;
  repositoryId: string;
}
export interface AzureDevOpsSelection {
  buildId: string;
  artifactName: string;
  artifactPath: string;
}
export interface AzureDevOpsSourceConnection {
  id: string;
  workspaceId: string;
  profile: "ado-services-build-artifacts";
  name: string;
  enabled: boolean;
  credentialConfigured: boolean;
  revision: number;
  createdAt: string;
  updatedAt: string;
  azureDevOps: AzureDevOpsTarget;
}
export interface AzureDevOpsSourceInput {
  profile: "ado-services-build-artifacts";
  name: string;
  enabled: boolean;
  token: string;
  azureDevOps: AzureDevOpsTarget;
}
export type AzureDevOpsSourcePatch = Partial<Pick<AzureDevOpsSourceInput, "name" | "enabled" | "token">>;
export interface AzureDevOpsSourceResponse {
  apiVersion: typeof apiVersion;
  dataOrigin?: DataOrigin;
  source: AzureDevOpsSourceConnection;
}
export interface SourcePage<T> {
  apiVersion: typeof apiVersion;
  dataOrigin?: DataOrigin;
  items: T[];
  total: number;
  nextCursor: string | null;
}
export type CollectionState = "queued" | "collecting" | "succeeded" | "partial" | "blocked" | "failed";
export interface SourceCollection {
  id: string;
  workspaceId: string;
  sourceId: string;
  profile: "github-cloud-app";
  connectionRevision: number;
  repository: string;
  requestedBy: string;
  state: CollectionState;
  complete: boolean;
  assetId: string | null;
  repositoryId: string | null;
  recordCount: number;
  gaps: string[];
  createdAt: string;
  collectedAt: string | null;
  completedAt: string | null;
  failure: { code: string; nativeCode: string; httpStatus: number; retryAfterSeconds: number; retryable: false } | null;
}
export interface CollectionResponse {
  apiVersion: typeof apiVersion;
  dataOrigin?: DataOrigin;
  collection: SourceCollection;
}
export interface AzureDevOpsSourceCollection {
  id: string;
  workspaceId: string;
  sourceId: string;
  profile: "ado-services-build-artifacts";
  connectionRevision: number;
  requestedBy: string;
  state: CollectionState;
  complete: boolean;
  assetId: string | null;
  repositoryId: string | null;
  recordCount: number;
  gaps: string[];
  createdAt: string;
  collectedAt: string | null;
  completedAt: string | null;
  failure: { code: string; nativeCode: string; httpStatus: number; retryAfterSeconds: number; retryable: false } | null;
  azureDevOps: AzureDevOpsTarget;
  selection: AzureDevOpsSelection;
}
export interface AzureDevOpsCollectionResponse {
  apiVersion: typeof apiVersion;
  dataOrigin?: DataOrigin;
  collection: AzureDevOpsSourceCollection;
}
export interface SourceRecord {
  id: string;
  collectionId: string;
  ordinal: number;
  kind: "repository" | "finding" | "pipeline" | "artifact" | "report";
  externalId: string;
  parentId: string;
  nativeRunId: string;
  state: string;
  severity: string;
  location: string;
  rawURL: string;
  sourceScanAt: string | null;
  sourceUpdatedAt: string | null;
  evidence: { sha256: string; sizeBytes: number };
}
export interface SourceEvidence {
  bytes: ArrayBuffer;
  text: string | null;
}
export interface CollectedSARIFImportInput {
  apiVersion: typeof apiVersion;
  format: "sarif";
  scope: SourceScope;
  sourceStatus: "succeeded" | "failed";
  scanKind: "full" | "delta";
  completeness: "complete" | "partial" | "unknown";
}
export interface CollectedSARIFImportResponse {
  apiVersion: typeof apiVersion;
  import: ImportReceipt;
}
