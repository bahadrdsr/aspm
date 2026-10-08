import type { apiVersion } from "./types";
import type { PolicyDeliveryProvenance } from "./types";

export const teamsProfile = "teams-workflows-channel" as const;
export interface TeamsDeclaration { channelType: "standard"; ownershipAcknowledged: true }
export interface TeamsMetadata extends TeamsDeclaration { workflowOrigin: string }
export interface TeamsDestination extends TeamsMetadata { name: string }
export interface TeamsConnection {
  id: string; workspaceId: string; profile: typeof teamsProfile; name: string; channel: "";
  enabled: boolean; credentialConfigured: boolean; revision: number; createdAt: string; updatedAt: string;
  teams: TeamsMetadata; permissionState: "not-verified";
}
export interface TeamsConnectionPatch { name?: string; enabled?: boolean; workflowUrl?: string; teams?: TeamsDeclaration }
export interface TeamsConnectionInput extends TeamsConnectionPatch {
  name: string; enabled: boolean; workflowUrl: string; teams: TeamsDeclaration;
}
export interface TeamsPayload { title: string; body: string; deepLink: string }
export interface TeamsPreview {
  workspaceId: string; findingId: string; connectionId: string; connectionRevision: number;
  profile: typeof teamsProfile; requestedBy: string; destination: TeamsDestination; payload: TeamsPayload;
  bindingDigest: string; nativeValidation: "not-run";
  reviewRequirements: ["explicit-queue-consent", "operator-declared-standard-channel", "workflow-owner-continuity"];
}
export interface TeamsQueueInput { connectionId: string; idempotencyKey: string; previewDigest: string; confirm: true }
export type TeamsState = "queued" | "dispatching" | "accepted" | "blocked" | "failed" | "rate-limited" | "uncertain";
export interface TeamsDelivery {
  id: string; workspaceId: string; findingId: string; connectionId: string; connectionRevision: number;
  profile: typeof teamsProfile; channel: ""; requestedBy: string; state: TeamsState;
  payload: TeamsPayload; destination: TeamsDestination; createdAt: string; dispatchStartedAt: string | null;
  outboundAttemptedAt: string | null; completedAt: string | null; receipt: null;
  failure: { code: string; nativeCode: ""; httpStatus: number; retryAfterSeconds: number; retryable: false } | null;
  triggerKind?: PolicyDeliveryProvenance["triggerKind"]; policyId?: string;
  policyRevision?: number; findingChangeRevision?: number;
}
export interface TeamsPage<T> { apiVersion: typeof apiVersion; items: T[]; total: number; nextCursor: string | null }
export interface TeamsConnectionResponse { apiVersion: typeof apiVersion; connection: TeamsConnection }
export interface TeamsDeliveryResponse { apiVersion: typeof apiVersion; delivery: TeamsDelivery }
