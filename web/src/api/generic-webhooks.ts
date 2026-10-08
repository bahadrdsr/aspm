import { APIError, request } from "./client";
import { requestAuthority } from "./authorization";
import { apiVersion } from "./types";
import type { FindingDetail } from "./types";

export const genericWebhookProfile = "generic-webhook-v1" as const;
const idPattern = /^[a-f0-9]{32}$/;
const digestPattern = /^sha256:[a-f0-9]{64}$/;
const encoder = new TextEncoder();

export interface WebhookMetadata {
  origin: string;
  path: string;
  signature: "hmac-sha256";
}
export interface WebhookConnection {
  id: string; workspaceId: string; profile: typeof genericWebhookProfile; name: string;
  enabled: boolean; credentialConfigured: boolean; revision: number;
  createdAt: string; updatedAt: string; webhook: WebhookMetadata;
  permissionState: "not-verified";
}
export interface WebhookConnectionInput {
  profile: typeof genericWebhookProfile; name: string; enabled: boolean; webhookUrl: string; secret: string;
}
export interface WebhookConnectionPatch {
  name?: string; enabled?: boolean; webhookUrl?: string; secret?: string;
}
export interface WebhookPreview {
  workspaceId: string; findingId: string; connectionId: string; connectionRevision: number;
  profile: typeof genericWebhookProfile; requestedBy: string; webhook: WebhookMetadata;
  payload: { title: string; body: string; deepLink: string };
  bindingDigest: string; nativeValidation: "not-run";
  reviewRequirements: [
    "explicit-queue-consent", "operator-approved-origin", "receiver-signature-verification",
  ];
}
export interface WebhookDelivery {
  id: string; workspaceId: string; findingId: string; connectionId: string; connectionRevision: number;
  profile: typeof genericWebhookProfile; requestedBy: string;
  state: "queued" | "dispatching" | "accepted" | "blocked" | "failed" | "rate-limited" | "uncertain";
  payload: WebhookPreview["payload"]; webhook: WebhookMetadata;
  createdAt: string; dispatchStartedAt: string | null; outboundAttemptedAt: string | null;
  completedAt: string | null; receipt: null;
  failure: { code: string; nativeCode: string; httpStatus: number; retryAfterSeconds: number; retryable: false } | null;
  triggerKind?: "notification-policy"; policyId?: string; policyRevision?: number; findingChangeRevision?: number;
}
export interface WebhookPage<T> {
  apiVersion: typeof apiVersion; dataOrigin?: "live" | "synthetic";
  items: T[]; total: number; nextCursor: string | null;
}
export interface WebhookConnectionResponse { apiVersion: typeof apiVersion; connection: WebhookConnection }
export interface WebhookDeliveryResponse { apiVersion: typeof apiVersion; delivery: WebhookDelivery }

function invalid(): never {
  throw new APIError("The service returned invalid generic webhook metadata. No replacement state was loaded.",
    "invalid-response", false);
}
function object(value: unknown, required: readonly string[], optional: readonly string[] = []) {
  if (!value || typeof value !== "object" || Array.isArray(value) ||
    required.some((key) => !Object.hasOwn(value, key)) ||
    Object.keys(value).some((key) => !required.includes(key) && !optional.includes(key))) return invalid();
  return value as Record<string, unknown>;
}
function text(value: unknown, empty = false) {
  if (typeof value !== "string" || value.includes("\0") || !empty && !value.trim()) return invalid();
  return value;
}
function id(value: unknown) { const result = text(value); return idPattern.test(result) ? result : invalid(); }
function integer(value: unknown, minimum = 0) {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= minimum ? value : invalid();
}
function bool(value: unknown) { return typeof value === "boolean" ? value : invalid(); }
function time(value: unknown) {
  const result = text(value);
  return Number.isFinite(Date.parse(result)) ? result : invalid();
}
const nullableTime = (value: unknown) => value === null ? null : time(value);
function envelope(value: unknown, fields: string[]) {
  const result = object(value, ["apiVersion", ...fields]);
  return result.apiVersion === apiVersion ? result : invalid();
}
function metadata(value: unknown): WebhookMetadata {
  const item = object(value, ["origin", "path", "signature"]);
  const origin = text(item.origin), path = text(item.path);
  if (item.signature !== "hmac-sha256" || !origin.startsWith("https://") ||
    !path.startsWith("/") || path === "/" || /[?#\\\0]/.test(path)) return invalid();
  return { origin, path, signature: "hmac-sha256" };
}
function connection(value: unknown, workspace: string | null): WebhookConnection {
  const item = object(value, ["id", "workspaceId", "profile", "name", "enabled", "credentialConfigured",
    "revision", "createdAt", "updatedAt", "webhook", "permissionState"]);
  if (id(item.workspaceId) !== workspace || item.profile !== genericWebhookProfile ||
    item.permissionState !== "not-verified") return invalid();
  return { id: id(item.id), workspaceId: workspace!, profile: genericWebhookProfile,
    name: text(item.name), enabled: bool(item.enabled), credentialConfigured: bool(item.credentialConfigured),
    revision: integer(item.revision, 1), createdAt: time(item.createdAt), updatedAt: time(item.updatedAt),
    webhook: metadata(item.webhook), permissionState: "not-verified" };
}
function payload(value: unknown, findingId: string) {
  const item = object(value, ["title", "body", "deepLink"]);
  const deepLink = text(item.deepLink);
  if (!deepLink.endsWith(`#/work?finding=${findingId}`)) return invalid();
  return { title: text(item.title), body: text(item.body), deepLink };
}
function preview(value: unknown, finding: FindingDetail, selected: WebhookConnection, actor: string): WebhookPreview {
  const item = object(envelope(value, ["preview"]).preview, ["workspaceId", "findingId", "connectionId",
    "connectionRevision", "profile", "requestedBy", "webhook", "payload", "bindingDigest",
    "nativeValidation", "reviewRequirements"]);
  const target = metadata(item.webhook), snapshot = payload(item.payload, finding.id);
  if (id(item.workspaceId) !== selected.workspaceId || item.workspaceId !== finding.workspaceId ||
    id(item.findingId) !== finding.id || id(item.connectionId) !== selected.id ||
    integer(item.connectionRevision, 1) !== selected.revision || item.profile !== genericWebhookProfile ||
    id(item.requestedBy) !== actor || JSON.stringify(target) !== JSON.stringify(selected.webhook) ||
    !digestPattern.test(text(item.bindingDigest)) || item.nativeValidation !== "not-run" ||
    JSON.stringify(item.reviewRequirements) !== JSON.stringify([
      "explicit-queue-consent", "operator-approved-origin", "receiver-signature-verification",
    ]) || snapshot.title !== finding.title ||
    snapshot.body !== `Severity: ${finding.severity}\nAsset: ${finding.assetName}`) return invalid();
  return { workspaceId: selected.workspaceId, findingId: finding.id, connectionId: selected.id,
    connectionRevision: selected.revision, profile: genericWebhookProfile, requestedBy: actor,
    webhook: target, payload: snapshot, bindingDigest: item.bindingDigest as string, nativeValidation: "not-run",
    reviewRequirements: ["explicit-queue-consent", "operator-approved-origin", "receiver-signature-verification"] };
}
function delivery(value: unknown, workspace: string | null, findingId: string): WebhookDelivery {
  const item = object(value, ["id", "workspaceId", "findingId", "connectionId", "connectionRevision",
    "profile", "requestedBy", "state", "payload", "webhook", "createdAt", "dispatchStartedAt",
    "outboundAttemptedAt", "completedAt", "receipt", "failure"],
  ["triggerKind", "policyId", "policyRevision", "findingChangeRevision"]);
  const states = ["queued", "dispatching", "accepted", "blocked", "failed", "rate-limited", "uncertain"] as const;
  const state = states.find((value) => value === item.state);
  if (!state || id(item.workspaceId) !== workspace || id(item.findingId) !== findingId ||
    item.profile !== genericWebhookProfile || item.receipt !== null) return invalid();
  let failure: WebhookDelivery["failure"] = null;
  if (item.failure !== null) {
    const value = object(item.failure, ["code", "nativeCode", "httpStatus", "retryAfterSeconds", "retryable"]);
    if (value.retryable !== false) return invalid();
    failure = { code: text(value.code), nativeCode: text(value.nativeCode, true),
      httpStatus: integer(value.httpStatus), retryAfterSeconds: integer(value.retryAfterSeconds), retryable: false };
  }
  const policy = item.triggerKind === undefined ? {} : {
    triggerKind: item.triggerKind === "notification-policy" ? "notification-policy" as const : invalid(),
    policyId: id(item.policyId), policyRevision: integer(item.policyRevision, 1),
    findingChangeRevision: integer(item.findingChangeRevision, 1),
  };
  return { id: id(item.id), workspaceId: workspace!, findingId, connectionId: id(item.connectionId),
    connectionRevision: integer(item.connectionRevision, 1), profile: genericWebhookProfile,
    requestedBy: id(item.requestedBy), state, payload: payload(item.payload, findingId),
    webhook: metadata(item.webhook), createdAt: time(item.createdAt),
    dispatchStartedAt: nullableTime(item.dispatchStartedAt), outboundAttemptedAt: nullableTime(item.outboundAttemptedAt),
    completedAt: nullableTime(item.completedAt), receipt: null, failure, ...policy };
}
function page<T extends { id: string }>(value: unknown, parse: (item: unknown) => T, cursor: string | null): WebhookPage<T> {
  const item = object(value, ["apiVersion", "items", "total", "nextCursor"], ["dataOrigin"]);
  if (item.apiVersion !== apiVersion) return invalid();
  if (!Array.isArray(item.items)) return invalid();
  const items = item.items.map(parse), total = integer(item.total);
  const nextCursor = item.nextCursor === null ? null : id(item.nextCursor);
  if (items.length > 100 || total < items.length ||
    items.some((item, index) => item.id <= (index ? items[index - 1].id : cursor ?? "")) ||
    nextCursor !== null && nextCursor !== items.at(-1)?.id) return invalid();
  return { apiVersion, ...(item.dataOrigin === "live" || item.dataOrigin === "synthetic"
    ? { dataOrigin: item.dataOrigin } : {}), items, total, nextCursor };
}
function query(cursor: string | null) {
  const result = new URLSearchParams({ profile: genericWebhookProfile, limit: "100" });
  if (cursor) result.set("cursor", cursor);
  return result.toString();
}
function validateText(value: string, label: string, minimum: number, maximum: number) {
  const size = encoder.encode(value).byteLength;
  if (!value.trim() || value.includes("\0") || size < minimum || size > maximum) {
    throw new APIError(`${label} is outside its permitted bounds.`, "invalid-input", false);
  }
}
function safeFailure(cause: unknown): never {
  if (!(cause instanceof APIError)) throw cause;
  throw new APIError(cause.code === "conflict"
    ? "The webhook target or consent changed. Review current metadata and do not blindly resend."
    : cause.code === "forbidden" ? "Current workspace authority does not permit this webhook operation."
    : cause.code === "not-found" ? "This webhook resource is unavailable in the current workspace."
    : cause.code === "invalid-response" ? cause.message
    : "The webhook request could not be confirmed. Review current metadata and history before another action.",
  cause.code, false, null, cause.httpStatus);
}

export const webhookApi = {
  connections: (cursor: string | null, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    return request(`/api/v1/integrations/connections?${query(cursor)}`,
      (value) => page(value, (item) => connection(item, workspace), cursor),
      { signal, expectedStatus: 200 }).catch(safeFailure);
  },
  connection: (connectionId: string, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    return request(`/api/v1/integrations/connections/${encodeURIComponent(connectionId)}`,
      (value) => ({ apiVersion, connection: connection(envelope(value, ["connection"]).connection, workspace) }),
      { signal, expectedStatus: 200 }).catch(safeFailure);
  },
  createConnection: (input: WebhookConnectionInput, signal: AbortSignal) => {
    validateText(input.name, "Connection name", 1, 256);
    validateText(input.webhookUrl, "Webhook URL", 1, 16384);
    validateText(input.secret, "HMAC secret", 32, 4096);
    const workspace = requestAuthority().workspace;
    return request("/api/v1/integrations/connections",
      (value) => ({ apiVersion, connection: connection(envelope(value, ["connection"]).connection, workspace) }),
      { method: "POST", body: input, signal, expectedStatus: 201 }).catch(safeFailure);
  },
  updateConnection: (original: WebhookConnection, patch: WebhookConnectionPatch, signal: AbortSignal) => {
    if (!Object.keys(patch).length) throw new APIError("There are no webhook changes to save.", "invalid-input", false);
    if (patch.name !== undefined) validateText(patch.name, "Connection name", 1, 256);
    if (patch.webhookUrl !== undefined) validateText(patch.webhookUrl, "Webhook URL", 1, 16384);
    if (patch.secret !== undefined) validateText(patch.secret, "HMAC secret", 32, 4096);
    const workspace = requestAuthority().workspace;
    return request(`/api/v1/integrations/connections/${encodeURIComponent(original.id)}`,
      (value) => ({ apiVersion, connection: connection(envelope(value, ["connection"]).connection, workspace) }),
      { method: "PATCH", body: patch, signal, expectedStatus: 200 }).catch(safeFailure);
  },
  preview: (finding: FindingDetail, selected: WebhookConnection, actor: string, signal: AbortSignal) =>
    request(`/api/v1/findings/${encodeURIComponent(finding.id)}/delivery-previews`,
      (value) => preview(value, finding, selected, actor),
      { method: "POST", body: { connectionId: selected.id }, signal, expectedStatus: 200 }).catch(safeFailure),
  enqueue: (value: WebhookPreview, key: string, signal: AbortSignal) =>
    request(`/api/v1/findings/${encodeURIComponent(value.findingId)}/deliveries`,
      (response, status) => ({ response: { apiVersion, delivery: delivery(
        envelope(response, ["delivery"]).delivery, requestAuthority().workspace, value.findingId) }, replay: status === 200 }),
      { method: "POST", body: { connectionId: value.connectionId, idempotencyKey: key,
        previewDigest: value.bindingDigest, confirm: true }, signal, expectedStatus: [200, 202] }).catch(safeFailure),
  history: (findingId: string, cursor: string | null, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    return request(`/api/v1/findings/${encodeURIComponent(findingId)}/deliveries?${query(cursor)}`,
      (value) => page(value, (item) => delivery(item, workspace, findingId), cursor),
      { signal, expectedStatus: 200 }).catch(safeFailure);
  },
  delivery: (deliveryId: string, findingId: string, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    return request(`/api/v1/integrations/deliveries/${encodeURIComponent(deliveryId)}`,
      (value) => ({ apiVersion, delivery: delivery(envelope(value, ["delivery"]).delivery, workspace, findingId) }),
      { signal, expectedStatus: 200 }).catch(safeFailure);
  },
};
