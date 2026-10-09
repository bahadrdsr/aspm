import { apiVersion, backendID, primaryFinding, workItem } from "./finding-actions-data";
import type { ActionFinding, ActionWorkItem } from "./finding-actions-data";

export { apiVersion, backendID };
export type VerificationRole = "admin" | "analyst" | "viewer";
export type VerificationState = "queued" | "processing" | "succeeded" | "blocked" | "failed" | "cancelled";
export type VerificationOutcome = "reproduced" | "not-reproduced";

export interface VerificationEvidence {
  id: string;
  workspaceId: string;
  findingId: string;
  submittedBy: string;
  method: "deterministic-evidence";
  schema: "aspm.synthetic-fixture/v1";
  environmentId: string;
  scopeRevision: string;
  findingEvidenceRevision: number;
  digest: string;
  sizeBytes: number;
  createdAt: string;
}

export interface VerificationApproval {
  id: string;
  workspaceId: string;
  findingId: string;
  evidenceId: string;
  approvedBy: string;
  method: "deterministic-evidence";
  environmentId: string;
  scopeRevision: string;
  findingEvidenceRevision: number;
  evidenceDigest: string;
  rationale: string;
  createdAt: string;
  expiresAt: string;
  revokedAt: string | null;
  revokedBy: string | null;
  revocationRationale: string | null;
  current: boolean;
}

export interface VerificationFailure {
  code: string;
  message: string;
  retryable: false;
}

export interface VerificationResult {
  method: "deterministic-evidence";
  environmentId: string;
  scopeRevision: string;
  evidenceId: string;
  evidenceDigest: string;
  outcome: VerificationOutcome;
  closeFinding: false;
  falsePositive: false;
}

export interface VerificationJob {
  id: string;
  workspaceId: string;
  findingId: string;
  approvalId: string;
  evidenceId: string;
  requestedBy: string;
  method: "deterministic-evidence";
  environmentId: string;
  scopeRevision: string;
  findingEvidenceRevision: number;
  evidenceDigest: string;
  state: VerificationState;
  createdAt: string;
  completedAt: string | null;
  failure: VerificationFailure | null;
  result: VerificationResult | null;
}

export interface VerificationPage<T> {
  apiVersion: typeof apiVersion;
  dataOrigin: "synthetic";
  items: T[];
  total: number;
  nextCursor: string | null;
}

export const verificationAlpha = { id: "17000000000000000000000000000001", name: "Synthetic verification Alpha" };
export const verificationBeta = { id: "17000000000000000000000000000002", name: "Synthetic verification Beta" };
export const verificationUser = {
  id: "27000000000000000000000000000001",
  name: "Synthetic verification admin",
  email: "deterministic-verification@synthetic.invalid",
};
export const verificationAnalyst = {
  id: "27000000000000000000000000000002",
  name: "Synthetic verification analyst",
};
export const verificationCookie = "v".repeat(43);
export const verificationMethod = "deterministic-evidence" as const;
export const verificationSchema = "aspm.synthetic-fixture/v1" as const;
export const verificationEnvironment = "synthetic-browser-environment";
export const verificationScopeRevision = "synthetic-browser-scope-revision-1";
export const verificationRationale = "Approve only this bounded synthetic fixture condition.";
export const verificationRevocationRationale = "Revoke this synthetic fixture authority.";
export const verificationExpiresAt = "2026-10-09T12:00:00Z";
export const canonicalFixture = `{"schema":"${verificationSchema}","environmentId":"${verificationEnvironment}","condition":true}`;

export const verificationFinding: ActionFinding = {
  ...structuredClone(primaryFinding),
  id: "57000000000000000000000000000001",
  assetId: "37000000000000000000000000000001",
  workspaceId: verificationAlpha.id,
  title: "Synthetic finding for deterministic fixture review",
  assetName: "synthetic-verification-repository",
  description: "Synthetic source context only. It is not proof of a real vulnerability.",
  remediation: "No command, target, browser automation, provider or external action is authorized.",
  evidence: {
    text: "Synthetic finding evidence remains separate from the verification fixture.",
    sourceLabel: "Synthetic verification source",
    verificationState: "not-run",
  },
  workflowState: "open",
  disposition: "none",
  verifiedResolution: false,
  decisionRevision: 7,
  notes: [],
  observations: [],
};

export const betaVerificationFinding: ActionFinding = {
  ...structuredClone(verificationFinding),
  id: "67000000000000000000000000000001",
  assetId: "37000000000000000000000000000002",
  workspaceId: verificationBeta.id,
  title: "Synthetic isolated verification finding",
  assetName: "synthetic-beta-verification-repository",
};

export const findingPath = `/api/v1/findings/${verificationFinding.id}`;
export const verificationBasePath = `${findingPath}/verification`;
export const evidencePath = `${verificationBasePath}/evidence`;
export const approvalsPath = `${verificationBasePath}/approvals`;
export const jobsPath = `${verificationBasePath}/jobs`;

export function verificationWorkItem(finding: ActionFinding): ActionWorkItem {
  return workItem(finding);
}

export function verificationID(prefix: string, ordinal: number) {
  return prefix + ordinal.toString(16).padStart(32 - prefix.length, "0");
}

export const initialEvidence: VerificationEvidence = {
  id: verificationID("71", 1),
  workspaceId: verificationAlpha.id,
  findingId: verificationFinding.id,
  submittedBy: verificationAnalyst.id,
  method: verificationMethod,
  schema: verificationSchema,
  environmentId: verificationEnvironment,
  scopeRevision: verificationScopeRevision,
  findingEvidenceRevision: 3,
  digest: `sha256:${"1".repeat(64)}`,
  sizeBytes: Buffer.byteLength(canonicalFixture, "utf8"),
  createdAt: "2026-10-09T09:30:00Z",
};

export const initialApproval: VerificationApproval = {
  id: verificationID("81", 1),
  workspaceId: verificationAlpha.id,
  findingId: verificationFinding.id,
  evidenceId: initialEvidence.id,
  approvedBy: verificationUser.id,
  method: verificationMethod,
  environmentId: initialEvidence.environmentId,
  scopeRevision: initialEvidence.scopeRevision,
  findingEvidenceRevision: initialEvidence.findingEvidenceRevision,
  evidenceDigest: initialEvidence.digest,
  rationale: verificationRationale,
  createdAt: "2026-10-09T09:31:00Z",
  expiresAt: verificationExpiresAt,
  revokedAt: null,
  revokedBy: null,
  revocationRationale: null,
  current: true,
};

export function verificationJobAt(index: number, state: VerificationState = "queued"): VerificationJob {
  const outcome: VerificationOutcome = index % 2 ? "reproduced" : "not-reproduced";
  const terminal = ["succeeded", "blocked", "failed", "cancelled"].includes(state);
  const failed = ["blocked", "failed", "cancelled"].includes(state);
  return {
    id: verificationID("91", index),
    workspaceId: verificationAlpha.id,
    findingId: verificationFinding.id,
    approvalId: initialApproval.id,
    evidenceId: initialEvidence.id,
    requestedBy: verificationAnalyst.id,
    method: verificationMethod,
    environmentId: initialEvidence.environmentId,
    scopeRevision: initialEvidence.scopeRevision,
    findingEvidenceRevision: initialEvidence.findingEvidenceRevision,
    evidenceDigest: initialEvidence.digest,
    state,
    createdAt: new Date(Date.parse("2026-10-09T09:32:00Z") + index).toISOString(),
    completedAt: terminal ? new Date(Date.parse("2026-10-09T09:33:00Z") + index).toISOString() : null,
    failure: failed ? {
      code: state === "blocked" ? "approval-expired" : state === "cancelled" ? "approval-revoked" : "fixture-format",
      message: `Synthetic ${state} deterministic verification diagnostic.`,
      retryable: false,
    } : null,
    result: state === "succeeded" ? {
      method: verificationMethod,
      environmentId: initialEvidence.environmentId,
      scopeRevision: initialEvidence.scopeRevision,
      evidenceId: initialEvidence.id,
      evidenceDigest: initialEvidence.digest,
      outcome,
      closeFinding: false,
      falsePositive: false,
    } : null,
  };
}

export const initialJob = verificationJobAt(1, "queued");
