import type { apiVersion } from "./types";
import type { PolicyDeliveryProvenance } from "./types";

export const jiraProfile = "jira-cloud-v3" as const;
export const jiraFieldSources = ["finding.id", "finding.title", "finding.severity", "asset.name", "finding.deepLink"] as const;
export type JiraFieldSource = typeof jiraFieldSources[number];

export interface JiraTarget {
  credentialType: "oauth2-bearer";
  cloudId: string;
  apiBase: string;
  siteOrigin: string;
  project: string;
  issueType: string;
  fieldMappings: Record<string, JiraFieldSource>;
}

export interface JiraConnection {
  id: string;
  workspaceId: string;
  profile: typeof jiraProfile;
  name: string;
  channel: "";
  enabled: boolean;
  credentialConfigured: boolean;
  revision: number;
  createdAt: string;
  updatedAt: string;
  jira: JiraTarget;
  permissionState: "not-verified";
}

export interface JiraConnectionInput {
  profile: typeof jiraProfile;
  name: string;
  enabled: boolean;
  token: string;
  jira: JiraTarget;
}
export type JiraConnectionPatch = Partial<Omit<JiraConnectionInput, "profile">>;
export interface JiraConnectionResponse { apiVersion: typeof apiVersion; connection: JiraConnection }
export interface JiraPage<T> { apiVersion: typeof apiVersion; items: T[]; total: number; nextCursor: string | null }
export interface JiraPayload { title: string; body: string; deepLink: string; fields: Record<string, string> }

export interface JiraPreview {
  workspaceId: string;
  findingId: string;
  connectionId: string;
  connectionRevision: number;
  profile: typeof jiraProfile;
  requestedBy: string;
  jira: JiraTarget;
  payload: JiraPayload;
  bindingDigest: string;
  nativeValidation: "not-run";
  reviewRequirements: ["explicit-queue-consent", "native-required-fields", "jira-permission"];
}

export type JiraState = "queued" | "dispatching" | "confirmed" | "accepted" | "blocked" | "failed" | "rate-limited" | "uncertain";
export interface JiraDelivery {
  id: string;
  workspaceId: string;
  findingId: string;
  connectionId: string;
  connectionRevision: number;
  profile: typeof jiraProfile;
  channel: "";
  requestedBy: string;
  state: JiraState;
  jira: JiraTarget;
  payload: JiraPayload;
  createdAt: string;
  dispatchStartedAt: string | null;
  createAttemptedAt: string | null;
  completedAt: string | null;
  receipt: { remoteId: string; remoteUrl: string } | null;
  failure: {
    code: string;
    nativeCode: string;
    httpStatus: number;
    retryAfterSeconds: number;
    retryable: false;
    stage?: "metadata" | "create";
    missingFields?: string[];
  } | null;
  triggerKind?: PolicyDeliveryProvenance["triggerKind"];
  policyId?: string;
  policyRevision?: number;
  findingChangeRevision?: number;
}
export interface JiraDeliveryResponse { apiVersion: typeof apiVersion; delivery: JiraDelivery }
export interface JiraQueueInput {
  connectionId: string;
  idempotencyKey: string;
  previewDigest: string;
  confirm: true;
}
