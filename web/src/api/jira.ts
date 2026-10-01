import { APIError, request } from "./client";
import { requestAuthority } from "./authorization";
import { apiVersion } from "./types";
import type { FindingDetail } from "./types";
import { jiraFieldSources, jiraProfile } from "./jira-types";
import type {
  JiraConnection, JiraConnectionInput, JiraConnectionPatch, JiraConnectionResponse, JiraDelivery, JiraDeliveryResponse,
  JiraFieldSource, JiraPage, JiraPayload, JiraPreview, JiraQueueInput, JiraState, JiraTarget,
} from "./jira-types";
import {
  jiraCustomField, jiraDigest, jiraNativeID, sameJiraPayload, sameJiraTarget, utf8Size,
  validateJiraName, validateJiraTarget, validateJiraToken,
} from "./jira-input";

function invalid(field: string): never {
  throw new APIError(`The service returned invalid Jira ${field}. The result could not be confirmed.`, "invalid-response", false);
}
function record(value: unknown, required: readonly string[], optional: readonly string[] = []): Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value) ||
    Object.keys(value).some((key) => !required.includes(key) && !optional.includes(key)) ||
    required.some((key) => !Object.hasOwn(value, key))) return invalid("metadata");
  return value as Record<string, unknown>;
}
function text(value: unknown, empty = false): string {
  if (typeof value !== "string" || value.includes("\0") || !empty && /^\p{White_Space}*$/u.test(value)) return invalid("text");
  return value;
}
function id(value: unknown): string {
  const result = text(value);
  if (!jiraNativeID.test(result)) return invalid("identifier");
  return result;
}
function integer(value: unknown, minimum = 0): number {
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value < minimum) return invalid("revision or count");
  return value;
}
function boolean(value: unknown): boolean {
  if (typeof value !== "boolean") return invalid("enabled or credential state");
  return value;
}
function time(value: unknown): string {
  const result = text(value);
  if (!/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?(?:Z|[+-]\d\d:\d\d)$/.test(result) ||
    !Number.isFinite(Date.parse(result))) return invalid("timestamp");
  return result;
}
const nullableTime = (value: unknown) => value === null ? null : time(value);
function envelope(value: unknown, fields: readonly string[]) {
  const result = record(value, ["apiVersion", ...fields]);
  if (result.apiVersion !== apiVersion) return invalid("API version");
  return result;
}
function target(value: unknown): JiraTarget {
  const item = record(value, ["credentialType", "cloudId", "apiBase", "siteOrigin", "project", "issueType", "fieldMappings"]);
  if (item.credentialType !== "oauth2-bearer") return invalid("credential type");
  const mappings = record(item.fieldMappings, [], Object.keys(
    item.fieldMappings && typeof item.fieldMappings === "object" ? item.fieldMappings : {}));
  const fieldMappings: Record<string, JiraFieldSource> = {};
  for (const [key, value] of Object.entries(mappings)) {
    const source = jiraFieldSources.find((source) => source === value);
    if (!source || !jiraCustomField.test(key) || utf8Size(key) > 128) return invalid("mapping source or ID");
    fieldMappings[key] = source;
  }
  const result: JiraTarget = {
    credentialType: item.credentialType, cloudId: text(item.cloudId), apiBase: text(item.apiBase),
    siteOrigin: text(item.siteOrigin), project: text(item.project), issueType: text(item.issueType), fieldMappings,
  };
  try { validateJiraTarget(result); } catch { return invalid("target"); }
  return result;
}
function connection(value: unknown, workspace: string | null): JiraConnection {
  const item = record(value, ["id", "workspaceId", "profile", "name", "channel", "enabled", "credentialConfigured",
    "revision", "createdAt", "updatedAt", "jira", "permissionState"]);
  if (item.profile !== jiraProfile || item.channel !== "" || item.permissionState !== "not-verified" ||
    id(item.workspaceId) !== workspace) return invalid("connection scope, profile or permission metadata");
  const name = text(item.name);
  try { validateJiraName(name); } catch { return invalid("connection name"); }
  return {
    id: id(item.id), workspaceId: workspace!, profile: jiraProfile, name, channel: "", enabled: boolean(item.enabled),
    credentialConfigured: boolean(item.credentialConfigured), revision: integer(item.revision, 1),
    createdAt: time(item.createdAt), updatedAt: time(item.updatedAt), jira: target(item.jira), permissionState: "not-verified",
  };
}
function connectionResponse(value: unknown, workspace: string | null): JiraConnectionResponse {
  return { apiVersion, connection: connection(envelope(value, ["connection"]).connection, workspace) };
}
function findingLink(value: unknown, findingId: string) {
  const result = text(value);
  try {
    const url = new URL(result);
    if (/[\p{White_Space}\p{Cc}\\]/u.test(result) || !["http:", "https:"].includes(url.protocol) ||
      url.username || url.password || url.pathname !== "/" || url.search ||
      url.hash !== `#/work?finding=${findingId}`) return invalid("finding link");
  } catch { return invalid("finding link"); }
  return result;
}
function payload(value: unknown, findingId: string, jira: JiraTarget): JiraPayload {
  const item = record(value, ["title", "body", "deepLink", "fields"]);
  const values = record(item.fields, [], Object.keys(
    item.fields && typeof item.fields === "object" ? item.fields : {}));
  const fields = Object.fromEntries(Object.entries(values).map(([key, value]) => {
    if (!jiraCustomField.test(key) || !Object.hasOwn(jira.fieldMappings, key)) return invalid("payload mapping");
    return [key, text(value, true)];
  }));
  if (Object.keys(fields).length !== Object.keys(jira.fieldMappings).length) return invalid("payload mappings");
  const title = text(item.title);
  if ([...title].length > 255) return invalid("issue summary");
  return { title, body: text(item.body), deepLink: findingLink(item.deepLink, findingId), fields };
}
function preview(value: unknown, finding: FindingDetail, selected: JiraConnection, actor: string): JiraPreview {
  const item = record(envelope(value, ["preview"]).preview, ["workspaceId", "findingId", "connectionId",
    "connectionRevision", "profile", "requestedBy", "jira", "payload", "bindingDigest", "nativeValidation", "reviewRequirements"]);
  const jira = target(item.jira);
  if (id(item.workspaceId) !== selected.workspaceId || item.workspaceId !== finding.workspaceId ||
    id(item.findingId) !== finding.id || id(item.connectionId) !== selected.id ||
    integer(item.connectionRevision, 1) !== selected.revision || item.profile !== jiraProfile ||
    id(item.requestedBy) !== actor || !sameJiraTarget(jira, selected.jira) || item.nativeValidation !== "not-run" ||
    !jiraDigest.test(text(item.bindingDigest)) || JSON.stringify(item.reviewRequirements) !==
      JSON.stringify(["explicit-queue-consent", "native-required-fields", "jira-permission"])) return invalid("preview binding or scope");
  const snapshot = payload(item.payload, finding.id, jira);
  const resolved: Record<JiraFieldSource, string> = {
    "finding.id": finding.id, "finding.title": finding.title, "finding.severity": finding.severity,
    "asset.name": finding.assetName, "finding.deepLink": snapshot.deepLink,
  };
  if (snapshot.title !== finding.title || snapshot.body !== `Severity: ${finding.severity}\nAsset: ${finding.assetName}` ||
    Object.entries(jira.fieldMappings).some(([key, source]) => snapshot.fields[key] !== resolved[source])) {
    return invalid("canonical preview payload");
  }
  return {
    workspaceId: selected.workspaceId, findingId: finding.id, connectionId: selected.id, connectionRevision: selected.revision,
    profile: jiraProfile, requestedBy: actor, jira, payload: snapshot, bindingDigest: text(item.bindingDigest),
    nativeValidation: "not-run", reviewRequirements: ["explicit-queue-consent", "native-required-fields", "jira-permission"],
  };
}
const states: readonly JiraState[] = ["queued", "dispatching", "confirmed", "accepted", "blocked", "failed", "rate-limited", "uncertain"];
function delivery(value: unknown, workspace: string | null, findingId: string): JiraDelivery {
  const item = record(value, ["id", "workspaceId", "findingId", "connectionId", "connectionRevision", "profile", "channel",
    "requestedBy", "state", "jira", "payload", "createdAt", "dispatchStartedAt", "createAttemptedAt", "completedAt", "receipt", "failure"]);
  const state = states.find((state) => state === item.state), jira = target(item.jira);
  if (!state || id(item.workspaceId) !== workspace || id(item.findingId) !== findingId ||
    item.profile !== jiraProfile || item.channel !== "") return invalid("delivery scope or state");
  const dispatchStartedAt = nullableTime(item.dispatchStartedAt), createAttemptedAt = nullableTime(item.createAttemptedAt),
    completedAt = nullableTime(item.completedAt);
  let receipt: JiraDelivery["receipt"] = null, failure: JiraDelivery["failure"] = null;
  if (item.receipt !== null) {
    const value = record(item.receipt, ["remoteId", "remoteUrl"]);
    receipt = { remoteId: text(value.remoteId, true), remoteUrl: text(value.remoteUrl, true) };
    const prefix = `${jira.project}-`, suffix = receipt.remoteId.slice(prefix.length);
    if ((state === "confirmed" || receipt.remoteId !== "" || receipt.remoteUrl !== "") &&
      (!receipt.remoteId.startsWith(prefix) || !/^[1-9][0-9]*$/.test(suffix) ||
        receipt.remoteUrl !== `${jira.siteOrigin}/browse/${receipt.remoteId}`)) return invalid("native receipt or untrusted issue URL");
  }
  if (item.failure !== null) {
    const value = record(item.failure, ["code", "nativeCode", "httpStatus", "retryAfterSeconds", "retryable"], ["stage", "missingFields"]);
    if (value.retryable !== false || integer(value.httpStatus) > 599 ||
      value.stage !== undefined && value.stage !== "metadata" && value.stage !== "create" ||
      value.missingFields !== undefined && !Array.isArray(value.missingFields)) return invalid("failure");
    failure = {
      code: text(value.code), nativeCode: text(value.nativeCode, true), httpStatus: integer(value.httpStatus),
      retryAfterSeconds: integer(value.retryAfterSeconds), retryable: false,
      ...(value.stage !== undefined && { stage: value.stage as "metadata" | "create" }),
      ...(Array.isArray(value.missingFields) && { missingFields: value.missingFields.map((value) => text(value)) }),
    };
  }
  const waiting = state === "queued" || state === "dispatching", successful = state === "confirmed" || state === "accepted";
  if (waiting && (completedAt !== null || receipt !== null || failure !== null) ||
    !waiting && completedAt === null || state === "queued" && (dispatchStartedAt !== null || createAttemptedAt !== null) ||
    state === "dispatching" && dispatchStartedAt === null || successful && (receipt === null || failure !== null) ||
    !waiting && !successful && (failure === null || receipt !== null) ||
    state === "confirmed" && (receipt === null || !receipt.remoteId || createAttemptedAt === null) ||
    createAttemptedAt !== null && dispatchStartedAt === null || failure?.stage === "metadata" && createAttemptedAt !== null) {
    return invalid("delivery outcome");
  }
  return {
    id: id(item.id), workspaceId: workspace!, findingId, connectionId: id(item.connectionId),
    connectionRevision: integer(item.connectionRevision, 1), profile: jiraProfile, channel: "", requestedBy: id(item.requestedBy),
    state, jira, payload: payload(item.payload, findingId, jira), createdAt: time(item.createdAt),
    dispatchStartedAt, createAttemptedAt, completedAt, receipt, failure,
  };
}
function deliveryResponse(value: unknown, workspace: string | null, findingId: string): JiraDeliveryResponse {
  return { apiVersion, delivery: delivery(envelope(value, ["delivery"]).delivery, workspace, findingId) };
}
function page<T extends { id: string }>(value: unknown, parse: (value: unknown) => T, cursor: string | null): JiraPage<T> {
  const body = envelope(value, ["items", "total", "nextCursor"]);
  if (!Array.isArray(body.items)) return invalid("collection");
  const items = body.items.map(parse), total = integer(body.total),
    nextCursor = body.nextCursor === null ? null : id(body.nextCursor);
  if (items.length > 100 || total < items.length ||
    items.some((item, index) => item.id <= (index ? items[index - 1].id : cursor ?? "")) ||
    nextCursor !== null && (nextCursor !== items.at(-1)?.id || total <= items.length)) return invalid("native page");
  return { apiVersion, items, total, nextCursor };
}
function query(cursor: string | null) {
  if (cursor !== null && !jiraNativeID.test(cursor)) throw new APIError("Select a native Jira continuation.", "invalid-input", false);
  const result = new URLSearchParams({ profile: jiraProfile, limit: "100" });
  if (cursor !== null) result.set("cursor", cursor);
  return result.toString();
}
function bodyLimit(value: object, limit: number) {
  if (utf8Size(JSON.stringify(value)) > limit) throw new APIError("The encoded Jira request exceeds the service limit. Your draft is unchanged.", "invalid-input", false);
}
function safeFailure(cause: unknown, operation: "read" | "configuration" | "queue"): never {
  if (!(cause instanceof APIError)) throw cause;
  const code = cause.httpStatus === 401 ? "unauthorized" : cause.httpStatus === 403 ? "forbidden" :
    cause.httpStatus === 404 ? "not-found" : cause.httpStatus === 409 ? "conflict" : cause.code;
  let message: string;
  switch (code) {
    case "unauthorized": message = "Your session ended. Sign in to continue."; break;
    case "forbidden": message = "Permission denied. Current server authority does not permit this Jira operation."; break;
    case "not-found": message = "This Jira resource is unavailable in the current workspace. Its fields are withheld."; break;
    case "conflict": message = "The Jira intent or target changed or conflicts with current state. A new preview and review are required. Do not blindly resend an unresolved intent."; break;
    case "network":
    case "unavailable": message = operation === "queue"
      ? "The acknowledgement could not be confirmed. This work item may already be queued or possibly created. Only explicit confirmation of the same original intent is permitted."
      : operation === "configuration"
        ? "Jira configuration could not be confirmed and may already have been saved. Your draft is retained; read current metadata before saving again. Configuration never tests credentials or creates an issue."
        : "Jira metadata is unavailable. Use an explicit retry to request an authorized read."; break;
    case "invalid-response": message = operation === "queue"
      ? "The Jira acknowledgement was invalid. Its outcome is unknown and may already be queued or possibly created. Keep the original intent; do not blindly resend."
      : "The service returned invalid Jira metadata. The result could not be confirmed and its fields are withheld."; break;
    default: message = "The Jira request was rejected. Check the permitted fields and size limits. Your draft has not been changed.";
  }
  throw new APIError(message, code, (code === "network" || code === "unavailable") && cause.retryable, null, cause.httpStatus);
}
async function read<T>(path: string, parse: (value: unknown) => T, signal: AbortSignal) {
  const scoped = AbortSignal.any([signal, requestAuthority().signal]);
  await Promise.resolve();
  scoped.throwIfAborted();
  return request(path, parse, { signal: scoped, expectedStatus: 200 }).catch((cause: unknown) => safeFailure(cause, "read"));
}
function patch(input: JiraConnectionPatch): JiraConnectionPatch {
  const result: JiraConnectionPatch = {};
  if (input.name !== undefined) { validateJiraName(input.name); result.name = input.name; }
  if (input.token !== undefined) { validateJiraToken(input.token); result.token = input.token; }
  if (input.jira !== undefined) { validateJiraTarget(input.jira); result.jira = input.jira; }
  if (input.enabled !== undefined) {
    if (typeof input.enabled !== "boolean") throw new APIError("Choose an enabled state.", "invalid-input", false);
    result.enabled = input.enabled;
  }
  bodyLimit(result, 32 << 10);
  return result;
}

export const jiraApi = {
  connections: (cursor: string | null, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    return read(`/api/v1/integrations/connections?${query(cursor)}`,
      (value) => page(value, (item) => connection(item, workspace), cursor), signal);
  },
  connection: (connectionId: string, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    return read(`/api/v1/integrations/connections/${encodeURIComponent(connectionId)}`, (value) => {
      const response = connectionResponse(value, workspace);
      if (response.connection.id !== connectionId) return invalid("selected connection");
      return response;
    }, signal);
  },
  createConnection: (input: JiraConnectionInput, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace, fields = patch(input);
    if (fields.name === undefined || fields.enabled === undefined || fields.token === undefined || fields.jira === undefined) {
      throw new APIError("Complete the Jira Cloud v3 name, target, bearer token and enabled choice.", "invalid-input", false);
    }
    const body = { profile: jiraProfile, ...fields };
    bodyLimit(body, 32 << 10);
    return request("/api/v1/integrations/connections", (value) => {
      const response = connectionResponse(value, workspace), saved = response.connection;
      if (saved.name !== body.name || saved.enabled !== body.enabled || !saved.credentialConfigured ||
        saved.revision !== 1 || !sameJiraTarget(saved.jira, body.jira!)) return invalid("created connection acknowledgement");
      return response;
    }, { method: "POST", body, signal, expectedStatus: 201 }).catch((cause: unknown) => safeFailure(cause, "configuration"));
  },
  updateConnection: (original: JiraConnection, input: JiraConnectionPatch, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace, body = patch(input);
    if (!Object.keys(body).length) throw new APIError("There are no Jira connection changes to save.", "invalid-input", false);
    return request(`/api/v1/integrations/connections/${encodeURIComponent(original.id)}`, (value) => {
      const response = connectionResponse(value, workspace), saved = response.connection;
      const changed = body.name !== undefined && body.name !== original.name ||
        body.enabled !== undefined && body.enabled !== original.enabled || body.jira !== undefined && !sameJiraTarget(body.jira, original.jira);
      if (saved.id !== original.id || saved.name !== (body.name ?? original.name) || saved.enabled !== (body.enabled ?? original.enabled) ||
        saved.revision < original.revision + (changed ? 1 : 0) || saved.revision > original.revision + 1 ||
        !sameJiraTarget(saved.jira, body.jira ?? original.jira) ||
        saved.credentialConfigured !== (body.token !== undefined || original.credentialConfigured)) return invalid("updated connection acknowledgement");
      return response;
    }, { method: "PATCH", body, signal, expectedStatus: 200 }).catch((cause: unknown) => safeFailure(cause, "configuration"));
  },
  preview: (finding: FindingDetail, selected: JiraConnection, actor: string, signal: AbortSignal) => {
    if (!selected.enabled || !selected.credentialConfigured || selected.workspaceId !== requestAuthority().workspace ||
      finding.workspaceId !== selected.workspaceId) throw new APIError("Choose a current enabled Jira target and authorized finding.", "invalid-input", false);
    return request(`/api/v1/findings/${encodeURIComponent(finding.id)}/delivery-previews`,
      (value) => preview(value, finding, selected, actor),
      { method: "POST", body: { connectionId: selected.id }, signal, expectedStatus: 200 })
      .catch((cause: unknown) => safeFailure(cause, "read"));
  },
  enqueue: (value: JiraPreview, input: JiraQueueInput, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    if (value.workspaceId !== workspace || input.connectionId !== value.connectionId || input.previewDigest !== value.bindingDigest ||
      input.confirm !== true || !jiraDigest.test(input.previewDigest) || !input.idempotencyKey ||
      /[\p{Cc}\p{White_Space}]/u.test(input.idempotencyKey) || utf8Size(input.idempotencyKey) > 256) {
      throw new APIError("A current bound Jira preview and explicit queue intent are required.", "invalid-input", false);
    }
    const body = { connectionId: input.connectionId, idempotencyKey: input.idempotencyKey, previewDigest: input.previewDigest, confirm: true };
    bodyLimit(body, 16 << 10);
    return request(`/api/v1/findings/${encodeURIComponent(value.findingId)}/deliveries`, (response, status) => {
      const parsed = deliveryResponse(response, workspace, value.findingId), saved = parsed.delivery;
      if (saved.connectionId !== value.connectionId || saved.connectionRevision !== value.connectionRevision ||
        saved.requestedBy !== value.requestedBy || !sameJiraTarget(saved.jira, value.jira) ||
        !sameJiraPayload(saved.payload, value.payload) || status === 202 && saved.state !== "queued") return invalid("queue acknowledgement binding");
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
      if (parsed.delivery.id !== deliveryId) return invalid("selected delivery");
      return parsed;
    }, signal);
  },
};
