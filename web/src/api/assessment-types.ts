import type { AIFamily, AIPage } from "./ai-types";

export const assessmentStates = ["queued", "dispatching", "succeeded", "failed", "cancelled", "invalidated", "uncertain"] as const;
export const assessmentDispatchStates = ["not-started", "possibly-sent", "response-received"] as const;
export type AssessmentState = typeof assessmentStates[number];
export type AssessmentDispatchState = typeof assessmentDispatchStates[number];

export interface AssessmentBinding {
  workspaceId: string;
  findingId: string;
  observationId: string;
  sourceEvidenceDigest: string;
  requestedBy: string;
  profileId: string;
  profileRevision: string;
  policyRevision: string;
  grantId: string;
  destination: string;
  family: AIFamily;
  model: string;
  deployment: string;
  task: "finding-validity";
  dataClass: "finding-evidence";
  promptRevision: string;
  contextRef: string;
  contextDigest: string;
  contextOrigin: "user-reviewed-derived";
  context: string;
}

export interface AssessmentPreview extends AssessmentBinding {
  id: string;
  createdAt: string;
  expiresAt: string;
}

export interface Assessment extends AssessmentBinding {
  id: string;
  previewId: string;
  idempotencyKey: string;
  scope: string;
  state: AssessmentState;
  attempts: number;
  dispatchState: AssessmentDispatchState;
  advisoryOnly: true;
  createdAt: string;
  consentExpiresAt: string;
  dispatchStartedAt: string | null;
  completedAt: string | null;
  result: {
    conclusion: "supported" | "contradicted" | "inconclusive";
    uncertainty: string;
    evidenceRefs: string[];
  } | null;
  failure: { code: string; message: string; retryable: false } | null;
  usage: {
    known: boolean;
    inputTokens: number;
    outputTokens: number;
    cachedInputTokens: number;
    cacheWriteTokens: number;
  };
  requestId: string;
  requestedModel: string;
  returnedModel: string;
  stopReason: string;
  retryAfterMillis: number;
}

export interface AssessmentPreviewInput {
  observationId: string;
  profileId: string;
  grantId: string;
  context: string;
  reviewed: true;
}

export interface AssessmentQueueInput {
  previewId: string;
  idempotencyKey: string;
  consent: true;
}

export type AssessmentPage = AIPage<Assessment>;
export type AssessmentSummary = Pick<Assessment, "id" | "state" | "dispatchState" | "attempts" | "createdAt">;
