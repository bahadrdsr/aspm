import { APIError, request } from "./client";
import { requestAuthority } from "./authorization";
import { apiVersion } from "./types";
import type { Observation } from "./types";
import { aiFamilies } from "./ai-types";
import type { AIGrant, AIPolicy, AIProfile } from "./ai-types";
import { validAIEndpoint } from "./ai-input";
import { assessmentDispatchStates, assessmentStates } from "./assessment-types";
import type {
  Assessment, AssessmentBinding, AssessmentPage, AssessmentPreview, AssessmentPreviewInput, AssessmentQueueInput,
} from "./assessment-types";

const encoder = new TextEncoder();
const bindingFields = ["workspaceId", "findingId", "observationId", "sourceEvidenceDigest", "requestedBy", "profileId",
  "profileRevision", "policyRevision", "grantId", "destination", "family", "model", "deployment", "task", "dataClass",
  "promptRevision", "contextRef", "contextDigest", "contextOrigin", "context"] as const;
const previewFields = [...bindingFields, "id", "createdAt", "expiresAt"];
const jobFields = [...bindingFields, "id", "previewId", "idempotencyKey", "scope", "state", "attempts", "dispatchState",
  "advisoryOnly", "createdAt", "consentExpiresAt", "dispatchStartedAt", "completedAt", "result", "failure", "usage",
  "requestId", "requestedModel", "returnedModel", "stopReason", "retryAfterMillis"];

function invalid(field: string): never {
  throw new APIError(`The service returned invalid assessment ${field}. No replacement data or successful action was confirmed.`, "invalid-response", false);
}
function record(value: unknown, fields: readonly string[], field: string): Record<string, unknown> {
  if (value === null || typeof value !== "object" || Array.isArray(value)) return invalid(field);
  const item = value as Record<string, unknown>;
  if (Object.keys(item).length !== fields.length || Object.keys(item).some((key) => !fields.includes(key))) return invalid(field);
  return item;
}
function text(value: unknown, field: string, maximum = 512, empty = false): string {
  if (typeof value !== "string" || !empty && value.trim() === "" || value.includes("\0") ||
    encoder.encode(value).byteLength > maximum) return invalid(field);
  return value;
}
function identifier(value: unknown): string {
  const id = text(value, "identifier", 32);
  if (!/^[a-f0-9]{32}$/.test(id)) return invalid("identifier");
  return id;
}
function choice<T extends string>(value: unknown, choices: readonly T[], field: string): T {
  const result = choices.find((item) => item === value);
  if (result === undefined) return invalid(field);
  return result;
}
function count(value: unknown): number {
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value < 0) return invalid("count");
  return value;
}
function timestamp(value: unknown): string {
  const result = text(value, "timestamp");
  if (!/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?(?:Z|[+-]\d\d:\d\d)$/.test(result) || !Number.isFinite(Date.parse(result))) return invalid("timestamp");
  return result;
}
function nullableTime(value: unknown): string | null { return value === null ? null : timestamp(value); }
function digest(value: unknown): string {
  const result = text(value, "digest");
  if (!/^sha256:[a-f0-9]{64}$/.test(result)) return invalid("digest");
  return result;
}
function envelope(value: unknown, fields: readonly string[]) {
  const body = record(value, ["apiVersion", ...fields], "response");
  if (body.apiVersion !== apiVersion) return invalid("API version");
  return body;
}
function binding(item: Record<string, unknown>, workspace: string | null, findingId: string, previewId: string): AssessmentBinding {
  const workspaceId = identifier(item.workspaceId), actualFinding = identifier(item.findingId);
  const family = choice(item.family, aiFamilies, "family");
  const destination = text(item.destination, "destination", 16384);
  const deployment = text(item.deployment, "deployment", 256, family !== "azure-foundry");
  const contextRef = text(item.contextRef, "context reference");
  if (workspaceId !== workspace || actualFinding !== findingId || contextRef !== `reviewed-context:${previewId}` ||
    !validAIEndpoint(destination, family) || family !== "azure-foundry" && deployment !== "") return invalid("snapshot scope or destination");
  return {
    workspaceId, findingId: actualFinding, observationId: identifier(item.observationId),
    sourceEvidenceDigest: digest(item.sourceEvidenceDigest), requestedBy: identifier(item.requestedBy),
    profileId: identifier(item.profileId), profileRevision: text(item.profileRevision, "profile revision"),
    policyRevision: text(item.policyRevision, "policy revision"), grantId: item.grantId === "" ? "" : identifier(item.grantId),
    destination, family, model: text(item.model, "model", 256), deployment,
    task: choice(item.task, ["finding-validity"], "task"), dataClass: choice(item.dataClass, ["finding-evidence"], "data class"),
    promptRevision: choice(item.promptRevision, ["finding-validity-reviewed-context/v1"], "prompt revision"),
    contextRef, contextDigest: digest(item.contextDigest),
    contextOrigin: choice(item.contextOrigin, ["user-reviewed-derived"], "context origin"),
    context: text(item.context, "reviewed context", 32768),
  };
}
function preview(value: unknown, workspace: string | null, findingId: string): AssessmentPreview {
  const item = record(value, previewFields, "preview"), id = identifier(item.id);
  const createdAt = timestamp(item.createdAt), expiresAt = timestamp(item.expiresAt);
  if (Date.parse(expiresAt) <= Date.parse(createdAt) || Date.parse(expiresAt) - Date.parse(createdAt) > 300_000) return invalid("consent lifetime");
  return { ...binding(item, workspace, findingId, id), id, createdAt, expiresAt };
}
function assessment(value: unknown, workspace: string | null, findingId: string): Assessment {
  const item = record(value, jobFields, "job"), previewId = identifier(item.previewId);
  const snapshot = binding(item, workspace, findingId, previewId);
  const state = choice(item.state, assessmentStates, "state"), attempts = count(item.attempts);
  const dispatchState = choice(item.dispatchState, assessmentDispatchStates, "dispatch state");
  const dispatchStartedAt = nullableTime(item.dispatchStartedAt), completedAt = nullableTime(item.completedAt);
  const createdAt = timestamp(item.createdAt), consentExpiresAt = timestamp(item.consentExpiresAt);
  if (item.advisoryOnly !== true || attempts > 1 || (attempts === 0) !== (dispatchState === "not-started") ||
    (attempts === 0) !== (dispatchStartedAt === null) || (["queued", "dispatching"].includes(state)) !== (completedAt === null) ||
    state === "queued" && attempts !== 0 || state === "dispatching" && dispatchState !== "possibly-sent") return invalid("execution metadata");
  let result: Assessment["result"] = null;
  if (item.result !== null) {
    const body = record(item.result, ["conclusion", "uncertainty", "evidenceRefs"], "advisory result");
    if (!Array.isArray(body.evidenceRefs) || body.evidenceRefs.length === 0 || body.evidenceRefs.length > 16 ||
      body.evidenceRefs.some((ref) => ref !== snapshot.contextRef)) return invalid("approved context references");
    result = {
      conclusion: choice(body.conclusion, ["supported", "contradicted", "inconclusive"], "advisory conclusion"),
      uncertainty: text(body.uncertainty, "uncertainty", 8192),
      evidenceRefs: body.evidenceRefs.map((ref) => text(ref, "context reference")),
    };
  }
  let failure: Assessment["failure"] = null;
  if (item.failure !== null) {
    const body = record(item.failure, ["code", "message", "retryable"], "failure");
    if (body.retryable !== false) return invalid("failure retry policy");
    failure = { code: text(body.code, "failure code"), message: text(body.message, "failure message", 1024), retryable: false };
  }
  if ((state === "succeeded") !== (result !== null) || result !== null && (failure !== null || dispatchState !== "response-received")) return invalid("advisory outcome");
  const usage = record(item.usage, ["known", "inputTokens", "outputTokens", "cachedInputTokens", "cacheWriteTokens"], "usage");
  if (typeof usage.known !== "boolean") return invalid("usage knowledge");
  return {
    ...snapshot, id: identifier(item.id), previewId, idempotencyKey: text(item.idempotencyKey, "intent key", 128),
    scope: text(item.scope, "worker scope"), state, attempts, dispatchState, advisoryOnly: true,
    createdAt, consentExpiresAt, dispatchStartedAt, completedAt, result, failure,
    usage: { known: usage.known, inputTokens: count(usage.inputTokens), outputTokens: count(usage.outputTokens),
      cachedInputTokens: count(usage.cachedInputTokens), cacheWriteTokens: count(usage.cacheWriteTokens) },
    requestId: text(item.requestId, "request identifier", 512, true),
    requestedModel: text(item.requestedModel, "requested model", 512, true),
    returnedModel: text(item.returnedModel, "returned model", 512, true),
    stopReason: text(item.stopReason, "stop reason", 512, true), retryAfterMillis: count(item.retryAfterMillis),
  };
}
function nativeInput(value: string): void {
  if (!/^[a-f0-9]{32}$/.test(value)) throw new APIError("Select an existing native assessment record in this workspace.", "invalid-input", false);
}
function findingPath(findingId: string): string { nativeInput(findingId); return `/api/v1/findings/${findingId}`; }
function jobPath(id: string): string { nativeInput(id); return `/api/v1/ai/assessments/${id}`; }
const rejectionCodes: Partial<Record<number, APIError["code"]>> = {
  400: "invalid-input", 403: "forbidden", 404: "not-found", 409: "conflict", 413: "too-large",
};
class AssessmentRequestError extends APIError {
  constructor(message: string, code: APIError["code"], cause: APIError, readonly definitiveRejection: boolean) {
    super(message, code, code === "forbidden" || code === "not-found" ? false : cause.retryable, null, cause.httpStatus);
  }
}
export function isDefinitiveAssessmentRejection(error: APIError): boolean {
  return error instanceof AssessmentRequestError && error.definitiveRejection;
}
function safeFailure(cause: unknown): never {
  if (!(cause instanceof APIError)) throw cause;
  const status = cause.httpStatus;
  const rejection = status === null ? undefined : rejectionCodes[status];
  const definitiveRejection = rejection !== undefined && rejection === cause.code && !cause.retryable;
  let code: APIError["code"];
  if (status === null) {
    code = cause.code === "network" || cause.code === "invalid-response" || cause.code === "unauthorized" ? cause.code : "unavailable";
  } else if (status >= 500 && status <= 599) code = "unavailable";
  else if (status === 401) code = "unauthorized";
  else if (status === 403) code = "forbidden";
  else if (status === 404) code = "not-found";
  else code = definitiveRejection ? rejection! : "invalid-response";
  const messages: Partial<Record<APIError["code"], string>> = {
    unauthorized: "Your session ended. Sign in to continue.",
    forbidden: "Permission denied. The current service role does not permit this assessment operation.",
    "not-found": "This assessment or its selected source is not available in the current workspace. Protected details are withheld.",
    conflict: "The preview expired or its configuration changed. Review current facts and prepare a new preview with explicit consent. Nothing was automatically resubmitted.",
    "invalid-response": "The service returned invalid assessment metadata. No replacement data or successful action was confirmed.",
    network: "The assessment request could not be confirmed. It may have reached the service. Closing or aborting is not a rollback.",
    unavailable: "Assessments are unavailable or their receipt could not be confirmed. Ask the operator to check service configuration. No execution is assumed.",
    "too-large": "The service rejected the assessment size. Context is limited to 32768 UTF-8 bytes; no text was trimmed or truncated.",
  };
  throw new AssessmentRequestError(
    messages[code] ?? "The service rejected the assessment input. Check the explicit selections and reviewed, nonblank, NUL-free context.",
    code, cause, definitiveRejection);
}
async function read<T>(path: string, parse: (value: unknown) => T, signal: AbortSignal): Promise<T> {
  const scoped = AbortSignal.any([signal, requestAuthority().signal]);
  await Promise.resolve();
  scoped.throwIfAborted();
  return request(path, parse, { signal: scoped, expectedStatus: 200 }).catch(safeFailure);
}
export function contextProblem(context: string): string | null {
  if (context.trim() === "") return "Enter nonblank, explicitly reviewed derived context.";
  if (context.includes("\0")) return "Context cannot contain NUL characters. Your text has not been changed.";
  if (encoder.encode(context).byteLength > 32768) return "Context exceeds 32768 UTF-8 bytes. Shorten it explicitly, then review it again.";
  return null;
}
export function sameAssessmentSnapshot(left: Assessment, right: Assessment): boolean {
  const fields = [...bindingFields, "id", "previewId", "idempotencyKey", "scope"] as const;
  return fields.every((key) => left[key] === right[key]) &&
    Date.parse(left.createdAt) === Date.parse(right.createdAt) && Date.parse(left.consentExpiresAt) === Date.parse(right.consentExpiresAt);
}
export interface AssessmentReview {
  observation: Observation;
  profile: AIProfile;
  policy: AIPolicy;
  grant: AIGrant | null;
  requestedBy: string;
}
export const assessmentsApi = {
  history: (findingId: string, cursor: string | null, signal: AbortSignal, limit = 100): Promise<AssessmentPage> => {
    const workspace = requestAuthority().workspace;
    if (!Number.isInteger(limit) || limit < 1 || limit > 500) throw new APIError("Assessment pages require a limit from 1 to 500.", "invalid-input", false);
    const query = new URLSearchParams({ limit: String(limit) });
    if (cursor !== null) { nativeInput(cursor); query.set("cursor", cursor); }
    return read(`${findingPath(findingId)}/assessments?${query}`, (value) => {
      const body = envelope(value, ["items", "total", "nextCursor"]);
      if (!Array.isArray(body.items) || body.items.length > limit) return invalid("history");
      const items = body.items.map((item) => assessment(item, workspace, findingId));
      const total = count(body.total), nextCursor = body.nextCursor === null ? null : identifier(body.nextCursor);
      if (items.length > limit || total < items.length || items.some((item, index) => item.id <= (index === 0 ? cursor ?? "" : items[index - 1].id)) ||
        nextCursor !== null && nextCursor !== items.at(-1)?.id) return invalid("history order or cursor");
      return { apiVersion, items, total, nextCursor };
    }, signal);
  },
  detail: (id: string, findingId: string, signal: AbortSignal): Promise<Assessment> => {
    const workspace = requestAuthority().workspace;
    return read(jobPath(id), (value) => {
      const result = assessment(envelope(value, ["assessment"]).assessment, workspace, findingId);
      if (result.id !== id) return invalid("selected job");
      return result;
    }, signal);
  },
  preview: async (findingId: string, input: AssessmentPreviewInput, review: AssessmentReview, signal: AbortSignal): Promise<AssessmentPreview> => {
    const workspace = requestAuthority().workspace;
    nativeInput(input.observationId); nativeInput(input.profileId);
    if (input.grantId !== "") nativeInput(input.grantId);
    const problem = contextProblem(input.context);
    if (input.reviewed !== true || problem) throw new APIError(problem ?? "Review the derived context explicitly before preparing a preview.", "invalid-input", false);
    const body = { observationId: input.observationId, profileId: input.profileId, grantId: input.grantId, context: input.context, reviewed: true };
    if (encoder.encode(JSON.stringify(body)).byteLength > 256 << 10) throw new APIError("The encoded preview exceeds 256 KiB.", "too-large", false);
    const hash = await crypto.subtle.digest("SHA-256", encoder.encode(input.context));
    const expectedDigest = "sha256:" + [...new Uint8Array(hash)].map((byte) => byte.toString(16).padStart(2, "0")).join("");
    signal.throwIfAborted();
    return request(`${findingPath(findingId)}/assessment-previews`, (value) => {
      const result = preview(envelope(value, ["preview"]).preview, workspace, findingId);
      if (result.context !== input.context || result.contextDigest !== expectedDigest || result.observationId !== input.observationId ||
        result.observationId !== review.observation.id || result.sourceEvidenceDigest !== review.observation.evidenceDigest ||
        result.profileId !== input.profileId || result.profileId !== review.profile.id || result.requestedBy !== review.requestedBy ||
        result.grantId !== input.grantId || result.grantId !== (review.grant?.id ?? "") ||
        result.profileRevision !== review.profile.revision || result.policyRevision !== review.policy.revision ||
        result.destination !== review.profile.endpoint || result.family !== review.profile.family || result.model !== review.profile.model ||
        result.deployment !== review.profile.deployment) return invalid("reviewed preview binding");
      return result;
    }, { method: "POST", body, signal, expectedStatus: 201 }).catch(safeFailure);
  },
  queue: (findingId: string, requestedBy: string, input: AssessmentQueueInput, signal: AbortSignal): Promise<Assessment> => {
    const workspace = requestAuthority().workspace;
    nativeInput(input.previewId);
    if (input.consent !== true || !input.idempotencyKey.trim() || input.idempotencyKey.includes("\0") ||
      encoder.encode(input.idempotencyKey).byteLength > 128) throw new APIError("Queueing requires explicit consent and a bounded intent key.", "invalid-input", false);
    const body = { previewId: input.previewId, idempotencyKey: input.idempotencyKey, consent: true };
    return request(`${findingPath(findingId)}/assessments`, (value, status) => {
      const result = assessment(envelope(value, ["assessment"]).assessment, workspace, findingId);
      if (result.requestedBy !== requestedBy || result.previewId !== input.previewId || result.idempotencyKey !== input.idempotencyKey ||
        status === 202 && (result.state !== "queued" || result.attempts !== 0)) return invalid("queue receipt");
      return result;
    }, { method: "POST", body, signal, expectedStatus: [200, 202] }).catch(safeFailure);
  },
  cancel: (original: Assessment, signal: AbortSignal): Promise<Assessment> => {
    const workspace = requestAuthority().workspace;
    return request(`${jobPath(original.id)}/cancel`, (value) => {
      const result = assessment(envelope(value, ["assessment"]).assessment, workspace, original.findingId);
      if (!sameAssessmentSnapshot(original, result) || ["queued", "dispatching"].includes(result.state)) return invalid("cancellation receipt");
      return result;
    }, { method: "POST", body: {}, signal, expectedStatus: 200 }).catch(safeFailure);
  },
};
