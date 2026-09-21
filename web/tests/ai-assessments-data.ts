import { createHash } from "node:crypto";
import { primaryFinding as publishedFinding } from "./finding-actions-data";
import type { ActionFinding } from "./finding-actions-data";
import { nativeID, backendID, pageOf, pageParameters } from "./ai-settings-ui-data";
import type { AIGrant, AIProfile, AIPolicy, Family, Role } from "./ai-settings-ui-data";

export { nativeID, backendID, pageOf, pageParameters };
export type { AIGrant, AIProfile, AIPolicy, Family, Role, ActionFinding };
export const apiVersion = "aspm/v1alpha1";
export const profilesPath = "/api/v1/ai/profiles";
export const policyPath = "/api/v1/ai/policy";
export const grantsPath = "/api/v1/ai/grants";
export const jobsPath = "/api/v1/ai/assessments";
export const alpha = { id: nativeID("1a", 1), name: "Synthetic assessment Alpha" };
export const beta = { id: nativeID("1b", 1), name: "Synthetic assessment Beta" };
export const user = { id: nativeID("2a", 1), name: "Synthetic assessment reviewer", email: "assessment-ui@synthetic.invalid" };
export const owner = { id: nativeID("2b", 1), name: "Synthetic original finding owner" };
export const cookie = "b".repeat(43);
export const password = "SYNTHETIC-ASSESSMENT-LOGIN-NOT-A-REAL-PASSWORD";
export const credentialCanary = "SYNTHETIC-SELECTED-PROVIDER-KEY-NEVER-RETURNED";
export const savedAt = "2026-09-18T12:34:56.123456Z";
export const promptRevision = "finding-validity-reviewed-context/v1";
export const reviewedContext = "  SYNTHETIC REVIEWED DERIVED CONTEXT\nOperator-reviewed source location and uncertainty. 安全\n<img src=\"https://assessment.synthetic.invalid/must-not-fetch\"> literal only.\n  ";
export const otherContext = "SYNTHETIC DIFFERENT EXPLICITLY REVIEWED CONTEXT\nNo original scanner bytes or automatic analyst notes.";
export const draftMarker = "SYNTHETIC-PRIVATE-ASSESSMENT-DRAFT-";
export const previewFields = ["observationId", "profileId", "grantId", "context", "reviewed"];
export const queueFields = ["previewId", "idempotencyKey", "consent"];
export const finding: ActionFinding = {
  ...structuredClone(publishedFinding), id: nativeID("5a", 1), assetId: nativeID("3a", 1), workspaceId: alpha.id,
  title: "Assessment Alpha source review", assetName: "assessment-alpha-repository",
  ownerId: owner.id, ownerName: owner.name, workflowState: "in-progress", disposition: "accepted-risk",
  acceptedRiskExpiresAt: "2027-09-18T12:34:56Z", riskAcceptanceExpired: false,
  description: "SYNTHETIC RAW DESCRIPTION - not automatically approved as model context.",
  evidence: { text: "SYNTHETIC ORIGINAL SCANNER BYTES - do not automatically upload.", sourceLabel: "Synthetic original report", verificationState: "not-run" },
  notes: [{ id: nativeID("7a", 1), text: "SYNTHETIC ANALYST NOTE - not approved context." }],
  observations: publishedFinding.observations.map((item, index) => ({
    ...structuredClone(item), id: nativeID("8a", index + 1), runId: nativeID("9a", index + 1),
    sourceId: "synthetic-source-" + "W".repeat(130), scanId: `Synthetic explicitly selected scan ${index + 1}`,
    sourceFindingId: "synthetic-real-linked-source-record",
    unmapped: { retained: "SYNTHETIC UNMAPPED SOURCE DATA - not approved context." },
  })),
};
export const betaFinding: ActionFinding = {
  ...structuredClone(finding), id: nativeID("5b", 1), workspaceId: beta.id, assetId: nativeID("3b", 1),
  title: "Assessment Beta isolated review", assetName: "assessment-beta-repository",
  description: "Synthetic Beta description only.", evidence: { text: "Synthetic Beta source evidence only.", sourceLabel: "Synthetic Beta report", verificationState: "not-run" },
  notes: [{ id: nativeID("7b", 1), text: "Synthetic Beta note." }],
  observations: finding.observations.map((item, index) => ({ ...structuredClone(item), id: nativeID("8b", index + 1), runId: nativeID("9b", index + 1) })),
};
export const primaryProfile: AIProfile = {
  id: nativeID("ca", 1), workspaceId: alpha.id, name: "Synthetic reviewed Foundry profile", family: "azure-foundry",
  endpoint: "https://provider.synthetic.invalid/explicit-reviewed-base/", model: "explicit-model-and-deployment", deployment: "explicit-model-and-deployment",
  enabled: true, structuredOutput: true, credentialConfigured: true, revision: "profile-Z/opaque.9", createdAt: savedAt, updatedAt: savedAt,
};
export const localProfile: AIProfile = {
  ...primaryProfile, id: nativeID("ca", 2), name: "Synthetic local configuration only", family: "local",
  endpoint: "http://127.0.0.1:19999/operator-local-base/", model: "operator-local-model", deployment: "",
  credentialConfigured: false, revision: "local:opaque/Z",
};
export const disabledProfile: AIProfile = { ...primaryProfile, id: nativeID("ca", 3), name: "Synthetic disabled assessment profile", enabled: false };
export const unreviewedProfile: AIProfile = { ...primaryProfile, id: nativeID("ca", 4), name: "Synthetic unreviewed assessment profile", structuredOutput: false };
export const betaProfile: AIProfile = { ...localProfile, id: nativeID("cb", 1), workspaceId: beta.id, name: "Synthetic Beta local profile" };
export const policy: AIPolicy = { workspaceId: alpha.id, mode: "approved-hosted", revision: "policy-Z/opaque.9", updatedAt: savedAt, updatedBy: user.id };
export function future(milliseconds = 3_600_000) { return new Date(Date.now() + milliseconds).toISOString(); }
export function offsetStamp(milliseconds: number) { return new Date(milliseconds + 7_200_000).toISOString().replace("Z", "+02:00"); }
export function grantFor(p = primaryProfile, pol = policy, index = 1, expiresAt = future()): AIGrant {
  return { id: nativeID("da", index), workspaceId: p.workspaceId, profileId: p.id, profileRevision: p.revision, policyRevision: pol.revision,
    destination: p.endpoint, task: "finding-validity", dataClass: "finding-evidence", expiresAt, createdAt: savedAt,
    grantedBy: user.id, revokedAt: null, revokedBy: null };
}
export function findingPath(id = finding.id) { return `/api/v1/findings/${id}`; }
export function previewPath(id = finding.id) { return `${findingPath(id)}/assessment-previews`; }
export function historyPath(id = finding.id) { return `${findingPath(id)}/assessments`; }
export function jobPath(id: string) { return `${jobsPath}/${id}`; }
export function cancelPath(id: string) { return `${jobPath(id)}/cancel`; }
export function exactKeys(value: object, keys: readonly string[]) { return Object.keys(value).sort().join(",") === [...keys].sort().join(","); }
export function validContext(value: unknown): value is string {
  return typeof value === "string" && value.trim() !== "" && !value.includes("\0") && Buffer.byteLength(value, "utf8") <= 32768;
}
export function digest(text: string) { return "sha256:" + createHash("sha256").update(text, "utf8").digest("hex"); }

export interface AssessmentBinding {
  workspaceId: string; findingId: string; observationId: string; sourceEvidenceDigest: string;
  requestedBy: string; profileId: string; profileRevision: string; policyRevision: string;
  grantId: string; destination: string; family: Family; model: string; deployment: string;
  task: "finding-validity"; dataClass: "finding-evidence"; promptRevision: string;
  contextRef: string; contextDigest: string; contextOrigin: "user-reviewed-derived"; context: string;
}
export interface AssessmentPreview extends AssessmentBinding { id: string; createdAt: string; expiresAt: string }
export type JobState = "queued" | "dispatching" | "succeeded" | "failed" | "cancelled" | "invalidated" | "uncertain";
export type DispatchState = "not-started" | "possibly-sent" | "response-received";
export type Conclusion = "supported" | "contradicted" | "inconclusive";
export interface AssessmentJob extends AssessmentBinding {
  id: string; previewId: string; idempotencyKey: string; scope: string; state: JobState; attempts: number;
  dispatchState: DispatchState; advisoryOnly: true; createdAt: string; consentExpiresAt: string;
  dispatchStartedAt: string | null; completedAt: string | null;
  result: { conclusion: Conclusion; uncertainty: string; evidenceRefs: string[] } | null;
  failure: { code: string; message: string; retryable: false } | null;
  usage: { known: boolean; inputTokens: number; outputTokens: number; cachedInputTokens: number; cacheWriteTokens: number };
  requestId: string; requestedModel: string; returnedModel: string; stopReason: string; retryAfterMillis: number;
}
export function previewFor(f = finding, p = primaryProfile, pol = policy, grant: AIGrant | null = grantFor(),
  text = reviewedContext, sequence = 1, observationId = f.observations[0].id, now = Date.now()): AssessmentPreview {
  const observation = f.observations.find((item) => item.id === observationId);
  if (!observation || !validContext(text)) throw new Error("Preview HTTP fixture requires explicit existing observation and bounded context.");
  const id = nativeID("ea", sequence);
  return {
    id, workspaceId: f.workspaceId, findingId: f.id, observationId, sourceEvidenceDigest: observation.evidenceDigest,
    requestedBy: user.id, profileId: p.id, profileRevision: p.revision, policyRevision: pol.revision, grantId: grant?.id ?? "",
    destination: p.endpoint, family: p.family, model: p.model, deployment: p.deployment, task: "finding-validity", dataClass: "finding-evidence",
    promptRevision, contextRef: "reviewed-context:" + id, contextDigest: digest(text), contextOrigin: "user-reviewed-derived", context: text,
    createdAt: offsetStamp(now), expiresAt: offsetStamp(Math.min(now + 300_000, grant ? Date.parse(grant.expiresAt) : Number.POSITIVE_INFINITY)),
  };
}
export function jobFor(v = previewFor(), sequence = 1, state: JobState = "queued", conclusion: Conclusion = "supported"): AssessmentJob {
  const { id: previewId, expiresAt, ...binding } = v;
  const dispatched = !["queued", "cancelled"].includes(state);
  const terminal = !["queued", "dispatching"].includes(state);
  const received = state === "succeeded" || state === "failed";
  return {
    ...binding, id: nativeID("fa", sequence), previewId, idempotencyKey: `synthetic-explicit-intent-${sequence}`, scope: "synthetic-shared-pool",
    state, attempts: dispatched ? 1 : 0, dispatchState: received ? "response-received" : dispatched ? "possibly-sent" : "not-started",
    advisoryOnly: true, consentExpiresAt: expiresAt, dispatchStartedAt: dispatched ? v.createdAt : null,
    completedAt: terminal ? offsetStamp(Date.parse(v.createdAt) + 500) : null,
    result: state === "succeeded" ? { conclusion, uncertainty: "Synthetic advisory only, not proof. <script>literal-no-execution</script>",
      evidenceRefs: [v.contextRef] } : null,
    failure: state === "failed" || state === "invalidated" || state === "uncertain"
      ? { code: state === "failed" ? "grounding" : state === "uncertain" ? "uncertain" : "authority-invalid",
        message: "Synthetic bounded service diagnostic, not a successful model assessment.", retryable: false } : null,
    usage: { known: received, inputTokens: received ? 23 : 0, outputTokens: received ? 7 : 0,
      cachedInputTokens: received ? 5 : 0, cacheWriteTokens: received ? 2 : 0 },
    requestId: received ? "synthetic-native-request" : "", requestedModel: v.model,
    returnedModel: received ? "observed-alias-not-immutable-model-proof" : "", stopReason: received ? "completed" : "",
    retryAfterMillis: state === "failed" ? 3000 : 0,
  };
}
export function historySeries(count = 101) {
  if (!Number.isInteger(count) || count < 0 || count > 101) throw new Error("Only bounded synthetic history is allowed.");
  return Array.from({ length: count }, (_, index) => jobFor(previewFor(finding, primaryProfile, policy, grantFor(), reviewedContext, index + 1), index + 1));
}
