import { APIError, request } from "./client";
import { requestAuthority } from "./authorization";
import { apiVersion } from "./types";
import type { FindingDetail } from "./types";
import { teamsProfile } from "./teams-types";
import type {
  TeamsConnection, TeamsConnectionInput, TeamsConnectionPatch, TeamsConnectionResponse, TeamsDelivery,
  TeamsDeliveryResponse, TeamsDestination, TeamsMetadata, TeamsPage, TeamsPayload, TeamsPreview, TeamsQueueInput, TeamsState,
} from "./teams-types";
import {
  canonicalTeamsOrigin, sameTeamsDestination, sameTeamsPayload, teamsBytes, teamsDigest, teamsNativeID,
  validateTeamsName, validateTeamsWorkflow,
} from "./teams-input";

function invalid(): never {
  throw new APIError("The service returned invalid Teams metadata. The result could not be confirmed; its fields are withheld.", "invalid-response", false);
}
function record(value: unknown, keys: readonly string[]): Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value) ||
    Object.keys(value).length !== keys.length || keys.some((key) => !Object.hasOwn(value, key))) return invalid();
  return value as Record<string, unknown>;
}
function text(value: unknown): string {
  if (typeof value !== "string" || !value.trim() || value.includes("\0")) return invalid();
  return value;
}
function id(value: unknown) { const result = text(value); return teamsNativeID.test(result) ? result : invalid(); }
function integer(value: unknown, minimum = 0): number {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= minimum ? value : invalid();
}
function boolean(value: unknown): boolean { return typeof value === "boolean" ? value : invalid(); }
function time(value: unknown): string {
  const result = text(value);
  if (!/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?(?:Z|[+-]\d\d:\d\d)$/.test(result) ||
    !Number.isFinite(Date.parse(result))) return invalid();
  return result;
}
const nullableTime = (value: unknown) => value === null ? null : time(value);
function envelope(value: unknown, keys: string[]) {
  const result = record(value, ["apiVersion", ...keys]);
  return result.apiVersion === apiVersion ? result : invalid();
}
function metadata(value: unknown): TeamsMetadata {
  const item = record(value, ["workflowOrigin", "channelType", "ownershipAcknowledged"]);
  if (!canonicalTeamsOrigin(text(item.workflowOrigin)) || item.channelType !== "standard" ||
    item.ownershipAcknowledged !== true) return invalid();
  return { workflowOrigin: item.workflowOrigin as string, channelType: "standard", ownershipAcknowledged: true };
}
function destination(value: unknown): TeamsDestination {
  const item = record(value, ["name", "workflowOrigin", "channelType", "ownershipAcknowledged"]);
  const name = text(item.name);
  try { validateTeamsName(name); } catch { return invalid(); }
  return { name, ...metadata({ workflowOrigin: item.workflowOrigin, channelType: item.channelType,
    ownershipAcknowledged: item.ownershipAcknowledged }) };
}
function connection(value: unknown, workspace: string | null): TeamsConnection {
  const item = record(value, ["id", "workspaceId", "profile", "name", "channel", "enabled", "credentialConfigured",
    "revision", "createdAt", "updatedAt", "teams", "permissionState"]);
  if (id(item.workspaceId) !== workspace || item.profile !== teamsProfile || item.channel !== "" ||
    item.permissionState !== "not-verified") return invalid();
  const name = text(item.name);
  try { validateTeamsName(name); } catch { return invalid(); }
  return { id: id(item.id), workspaceId: workspace!, profile: teamsProfile, name, channel: "",
    enabled: boolean(item.enabled), credentialConfigured: boolean(item.credentialConfigured), revision: integer(item.revision, 1),
    createdAt: time(item.createdAt), updatedAt: time(item.updatedAt), teams: metadata(item.teams), permissionState: "not-verified" };
}
function connectionResponse(value: unknown, workspace: string | null): TeamsConnectionResponse {
  return { apiVersion, connection: connection(envelope(value, ["connection"]).connection, workspace) };
}
function payload(value: unknown, findingId: string): TeamsPayload {
  const item = record(value, ["title", "body", "deepLink"]), deepLink = text(item.deepLink);
  try {
    const link = new URL(deepLink);
    if (/[\p{Cc}\p{White_Space}\\]/u.test(deepLink) || !["https:", "http:"].includes(link.protocol) ||
      link.username || link.password || link.pathname !== "/" || link.search ||
      link.hash !== `#/work?finding=${findingId}`) return invalid();
  } catch { return invalid(); }
  return { title: text(item.title), body: text(item.body), deepLink };
}
function preview(value: unknown, finding: FindingDetail, selected: TeamsConnection, actor: string): TeamsPreview {
  const item = record(envelope(value, ["preview"]).preview, ["workspaceId", "findingId", "connectionId",
    "connectionRevision", "profile", "requestedBy", "destination", "payload", "bindingDigest", "nativeValidation", "reviewRequirements"]);
  const target = destination(item.destination), snapshot = payload(item.payload, finding.id);
  if (id(item.workspaceId) !== selected.workspaceId || item.workspaceId !== finding.workspaceId ||
    id(item.findingId) !== finding.id || id(item.connectionId) !== selected.id ||
    integer(item.connectionRevision, 1) !== selected.revision || item.profile !== teamsProfile ||
    id(item.requestedBy) !== actor || !sameTeamsDestination(target, { name: selected.name, ...selected.teams }) ||
    item.nativeValidation !== "not-run" || !teamsDigest.test(text(item.bindingDigest)) ||
    JSON.stringify(item.reviewRequirements) !== JSON.stringify([
      "explicit-queue-consent", "operator-declared-standard-channel", "workflow-owner-continuity",
    ]) || snapshot.title !== finding.title || snapshot.body !== `Severity: ${finding.severity}\nAsset: ${finding.assetName}`) return invalid();
  return { workspaceId: selected.workspaceId, findingId: finding.id, connectionId: selected.id,
    connectionRevision: selected.revision, profile: teamsProfile, requestedBy: actor, destination: target, payload: snapshot,
    bindingDigest: item.bindingDigest as string, nativeValidation: "not-run",
    reviewRequirements: ["explicit-queue-consent", "operator-declared-standard-channel", "workflow-owner-continuity"] };
}
const states: TeamsState[] = ["queued", "dispatching", "accepted", "blocked", "failed", "rate-limited", "uncertain"];
const failureCodes = new Set(["auth", "scope", "rate_limited", "uncertain", "unavailable", "required_fields", "limit",
  "protocol", "unsupported", "canceled", "deadline_exceeded", "lease-lost", "lease-expired", "binding-changed",
  "authorization-revoked", "connection-changed", "connection-disabled", "create-already-attempted"]);
function delivery(value: unknown, workspace: string | null, findingId: string): TeamsDelivery {
  const item = record(value, ["id", "workspaceId", "findingId", "connectionId", "connectionRevision", "profile", "channel",
    "requestedBy", "state", "payload", "createdAt", "dispatchStartedAt", "completedAt", "receipt", "failure",
    "destination", "outboundAttemptedAt"]);
  const state = states.find((state) => state === item.state);
  if (!state || id(item.workspaceId) !== workspace || id(item.findingId) !== findingId ||
    item.profile !== teamsProfile || item.channel !== "" || item.receipt !== null) return invalid();
  const dispatchStartedAt = nullableTime(item.dispatchStartedAt), outboundAttemptedAt = nullableTime(item.outboundAttemptedAt),
    completedAt = nullableTime(item.completedAt);
  let failure: TeamsDelivery["failure"] = null;
  if (item.failure !== null) {
    const fields = record(item.failure, ["code", "nativeCode", "httpStatus", "retryAfterSeconds", "retryable"]);
    if (!failureCodes.has(text(fields.code)) || fields.nativeCode !== "" || fields.retryable !== false ||
      integer(fields.httpStatus) > 599) return invalid();
    failure = { code: fields.code as string, nativeCode: "", httpStatus: integer(fields.httpStatus),
      retryAfterSeconds: integer(fields.retryAfterSeconds), retryable: false };
  }
  const waiting = state === "queued" || state === "dispatching";
  if (waiting && (completedAt !== null || failure !== null) || !waiting && completedAt === null ||
    state === "queued" && (dispatchStartedAt !== null || outboundAttemptedAt !== null) ||
    state === "dispatching" && dispatchStartedAt === null ||
    state === "accepted" && (failure !== null || outboundAttemptedAt === null) ||
    !waiting && state !== "accepted" && failure === null ||
    outboundAttemptedAt !== null && dispatchStartedAt === null) return invalid();
  return { id: id(item.id), workspaceId: workspace!, findingId, connectionId: id(item.connectionId),
    connectionRevision: integer(item.connectionRevision, 1), profile: teamsProfile, channel: "", requestedBy: id(item.requestedBy),
    state, payload: payload(item.payload, findingId), destination: destination(item.destination),
    createdAt: time(item.createdAt), dispatchStartedAt, outboundAttemptedAt, completedAt, receipt: null, failure };
}
function deliveryResponse(value: unknown, workspace: string | null, findingId: string): TeamsDeliveryResponse {
  return { apiVersion, delivery: delivery(envelope(value, ["delivery"]).delivery, workspace, findingId) };
}
function page<T extends { id: string }>(value: unknown, parse: (value: unknown) => T, cursor: string | null): TeamsPage<T> {
  const item = envelope(value, ["items", "total", "nextCursor"]);
  if (!Array.isArray(item.items)) return invalid();
  const items = item.items.map(parse), total = integer(item.total),
    nextCursor = item.nextCursor === null ? null : id(item.nextCursor);
  if (items.length > 100 || total < items.length ||
    items.some((item, index) => item.id <= (index ? items[index - 1].id : cursor ?? "")) ||
    nextCursor !== null && (nextCursor !== items.at(-1)?.id || total <= items.length)) return invalid();
  return { apiVersion, items, total, nextCursor };
}
function query(cursor: string | null) {
  if (cursor !== null && !teamsNativeID.test(cursor)) throw new APIError("Select a native continuation.", "invalid-input", false);
  const result = new URLSearchParams({ profile: teamsProfile, limit: "100" });
  if (cursor !== null) result.set("cursor", cursor);
  return result.toString();
}
function safeFailure(cause: unknown, operation: "read" | "configuration" | "queue"): never {
  if (cause instanceof DOMException && cause.name === "AbortError") throw cause;
  const error = cause instanceof APIError ? cause : new APIError("", "invalid-response", false);
  const code = error.httpStatus === 401 ? "unauthorized" : error.httpStatus === 403 ? "forbidden" :
    error.httpStatus === 404 ? "not-found" : error.httpStatus === 409 ? "conflict" : error.code;
  let message = "The Teams request was rejected. Check the permitted fields; your private draft is unchanged.";
  if (code === "unauthorized") message = "Your session ended. Sign in to continue.";
  else if (code === "forbidden") message = "Permission denied. Current server authority does not permit this Teams operation.";
  else if (code === "not-found") message = "This Teams resource is unavailable in this workspace. Its fields are withheld.";
  else if (code === "conflict") message = "The Teams target or consent changed. The unresolved original intent is retained; read authorized history and do not blindly resend.";
  else if (code === "invalid-response") message = operation === "queue"
    ? "The Teams acknowledgement was invalid. Its outcome is unknown and may already be queued. Retain the original intent; do not blindly resend."
    : "The service returned invalid Teams metadata. The result could not be confirmed; its fields are withheld.";
  else if (code === "network" || code === "unavailable") message = operation === "queue"
    ? "The acknowledgement could not be confirmed. This notification may already be queued. Only explicit confirmation of the same original intent is permitted."
    : operation === "configuration" ? "Teams configuration could not be confirmed and may already be saved. Your private draft is retained; read current metadata before saving again."
      : "Teams metadata is unavailable. Use an explicit retry for an authorized read.";
  throw new APIError(message, code, false, null, error.httpStatus);
}
async function read<T>(path: string, parse: (value: unknown) => T, signal: AbortSignal) {
  const scoped = AbortSignal.any([signal, requestAuthority().signal]);
  await Promise.resolve(); scoped.throwIfAborted();
  return request(path, parse, { signal: scoped, expectedStatus: 200 }).catch((cause: unknown) => safeFailure(cause, "read"));
}
function patch(input: TeamsConnectionPatch): TeamsConnectionPatch {
  const result: TeamsConnectionPatch = {};
  if (input.name !== undefined) { validateTeamsName(input.name); result.name = input.name; }
  if (input.enabled !== undefined) {
    if (typeof input.enabled !== "boolean") throw new APIError("Choose an enabled state.", "invalid-input", false);
    result.enabled = input.enabled;
  }
  if (input.workflowUrl !== undefined) { validateTeamsWorkflow(input.workflowUrl); result.workflowUrl = input.workflowUrl; }
  if (input.teams !== undefined) {
    if (input.teams.channelType !== "standard" || input.teams.ownershipAcknowledged !== true ||
      Object.keys(input.teams).length !== 2) throw new APIError("Acknowledge standard-channel Workflow ownership.", "invalid-input", false);
    result.teams = { channelType: "standard", ownershipAcknowledged: true };
  }
  return result;
}
function bodyLimit(value: object, limit: number) {
  if (teamsBytes(JSON.stringify(value)) > limit) throw new APIError("The encoded Teams request exceeds the native byte limit. The private draft is unchanged.", "invalid-input", false);
}
export const teamsApi = {
  connections: (cursor: string | null, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    return read(`/api/v1/integrations/connections?${query(cursor)}`,
      (value) => page(value, (item) => connection(item, workspace), cursor), signal);
  },
  connection: (connectionId: string, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    return read(`/api/v1/integrations/connections/${encodeURIComponent(connectionId)}`, (value) => {
      const parsed = connectionResponse(value, workspace);
      return parsed.connection.id === connectionId ? parsed : invalid();
    }, signal);
  },
  createConnection: (input: TeamsConnectionInput, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace, fields = patch(input);
    if (fields.name === undefined || fields.enabled === undefined || fields.workflowUrl === undefined || fields.teams === undefined) {
      throw new APIError("Complete the name, signed Workflow URL, enabled choice and ownership acknowledgement.", "invalid-input", false);
    }
    const origin = validateTeamsWorkflow(fields.workflowUrl), body = { profile: teamsProfile, ...fields };
    bodyLimit(body, 32 << 10);
    return request("/api/v1/integrations/connections", (value) => {
      const parsed = connectionResponse(value, workspace), saved = parsed.connection;
      if (saved.name !== fields.name || saved.enabled !== fields.enabled || !saved.credentialConfigured ||
        saved.revision !== 1 || saved.teams.workflowOrigin !== origin) return invalid();
      return parsed;
    }, { method: "POST", body, signal, expectedStatus: 201 }).catch((cause: unknown) => safeFailure(cause, "configuration"));
  },
  updateConnection: (original: TeamsConnection, input: TeamsConnectionPatch, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace, body = patch(input);
    if (!Object.keys(body).length) throw new APIError("There are no Teams changes to save.", "invalid-input", false);
    bodyLimit(body, 32 << 10);
    const origin = body.workflowUrl === undefined ? original.teams.workflowOrigin : validateTeamsWorkflow(body.workflowUrl);
    return request(`/api/v1/integrations/connections/${encodeURIComponent(original.id)}`, (value) => {
      const parsed = connectionResponse(value, workspace), saved = parsed.connection;
      const changed = body.name !== undefined && body.name !== original.name ||
        body.enabled !== undefined && body.enabled !== original.enabled || origin !== original.teams.workflowOrigin;
      if (saved.id !== original.id || saved.createdAt !== original.createdAt || saved.name !== (body.name ?? original.name) ||
        saved.enabled !== (body.enabled ?? original.enabled) || saved.teams.workflowOrigin !== origin ||
        saved.revision < original.revision + (changed ? 1 : 0) || saved.revision > original.revision + 1 ||
        saved.credentialConfigured !== (body.workflowUrl !== undefined || original.credentialConfigured)) return invalid();
      return parsed;
    }, { method: "PATCH", body, signal, expectedStatus: 200 }).catch((cause: unknown) => safeFailure(cause, "configuration"));
  },
  preview: (finding: FindingDetail, selected: TeamsConnection, actor: string, signal: AbortSignal) => {
    if (!selected.enabled || !selected.credentialConfigured || selected.workspaceId !== requestAuthority().workspace ||
      finding.workspaceId !== selected.workspaceId) throw new APIError("Choose a current enabled Teams destination and authorized finding.", "invalid-input", false);
    return request(`/api/v1/findings/${encodeURIComponent(finding.id)}/delivery-previews`,
      (value) => preview(value, finding, selected, actor), { method: "POST", body: { connectionId: selected.id }, signal, expectedStatus: 200 })
      .catch((cause: unknown) => safeFailure(cause, "read"));
  },
  enqueue: (value: TeamsPreview, input: TeamsQueueInput, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    if (value.workspaceId !== workspace || input.connectionId !== value.connectionId ||
      input.previewDigest !== value.bindingDigest || !teamsDigest.test(input.previewDigest) || input.confirm !== true ||
      !input.idempotencyKey || /[\p{Cc}\p{White_Space}]/u.test(input.idempotencyKey) || teamsBytes(input.idempotencyKey) > 256) {
      throw new APIError("A current bound Teams preview and explicit original-key intent are required.", "invalid-input", false);
    }
    const body = { connectionId: input.connectionId, idempotencyKey: input.idempotencyKey, previewDigest: input.previewDigest, confirm: true };
    bodyLimit(body, 16 << 10);
    return request(`/api/v1/findings/${encodeURIComponent(value.findingId)}/deliveries`, (response, status) => {
      const parsed = deliveryResponse(response, workspace, value.findingId), saved = parsed.delivery;
      if (saved.connectionId !== value.connectionId || saved.connectionRevision !== value.connectionRevision ||
        saved.requestedBy !== value.requestedBy || !sameTeamsDestination(saved.destination, value.destination) ||
        !sameTeamsPayload(saved.payload, value.payload) || status === 202 && saved.state !== "queued") return invalid();
      return { response: parsed, replay: status === 200 };
    }, { method: "POST", body, signal, expectedStatus: [200, 202] }).catch((cause: unknown) => safeFailure(cause, "queue"));
  },
  history: (findingId: string, cursor: string | null, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    return read(`/api/v1/findings/${encodeURIComponent(findingId)}/deliveries?${query(cursor)}`,
      (value) => page(value, (item) => delivery(item, workspace, findingId), cursor), signal);
  },
  delivery: (deliveryId: string, findingId: string, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    return read(`/api/v1/integrations/deliveries/${encodeURIComponent(deliveryId)}`, (value) => {
      const parsed = deliveryResponse(value, workspace, findingId);
      return parsed.delivery.id === deliveryId ? parsed : invalid();
    }, signal);
  },
};
