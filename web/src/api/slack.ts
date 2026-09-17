import { APIError, request } from "./client";
import { requestAuthority } from "./authorization";
import { apiVersion } from "./types";
import type {
  DataOrigin, FindingDelivery, FindingDeliveryInput, FindingDeliveryResponse, SlackConnection,
  SlackConnectionInput, SlackConnectionPatch, SlackConnectionResponse, SlackPage,
} from "./types";

const encoder = new TextEncoder();
const channelPattern = /^[CG][A-Z0-9]{2,127}$/;
const connectionFields = ["id", "workspaceId", "profile", "name", "channel", "enabled", "credentialConfigured", "revision", "createdAt", "updatedAt"];
const deliveryFields = ["id", "workspaceId", "findingId", "connectionId", "connectionRevision", "profile", "channel", "requestedBy",
  "state", "payload", "createdAt", "dispatchStartedAt", "completedAt", "receipt", "failure"];
const states = ["queued", "dispatching", "confirmed", "accepted", "blocked", "failed", "rate-limited", "uncertain"] as const;

function invalid(field: string): never {
  throw new APIError(`The service returned invalid ${field}. No replacement data was loaded.`, "invalid-response", false);
}

function record(value: unknown, fields: readonly string[], field: string): Record<string, unknown> {
  if (value === null || typeof value !== "object" || Array.isArray(value) ||
    Object.keys(value).some((key) => !fields.includes(key))) return invalid(field);
  return value as Record<string, unknown>;
}

function text(value: unknown, field: string, empty = false): string {
  if (typeof value !== "string" || (!empty && /^\p{White_Space}*$/u.test(value)) || value.includes("\0")) return invalid(field);
  return value;
}

function identifier(value: unknown, field: string): string {
  const result = text(value, field);
  if (!/^[a-f0-9]{32}$/.test(result)) return invalid(field);
  return result;
}

function integer(value: unknown, field: string, minimum = 0): number {
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value < minimum) return invalid(field);
  return value;
}

function boolean(value: unknown, field: string): boolean {
  if (typeof value !== "boolean") return invalid(field);
  return value;
}

function timestamp(value: unknown): string {
  const result = text(value, "timestamp");
  if (!/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?(?:Z|[+-]\d\d:\d\d)$/.test(result) ||
    !Number.isFinite(Date.parse(result))) return invalid("timestamp");
  return result;
}

function channel(value: unknown): string {
  const result = text(value, "Slack channel");
  if (!channelPattern.test(result)) return invalid("Slack channel");
  return result;
}

function profile(value: unknown): "slack-workspace-bot" {
  if (value !== "slack-workspace-bot") return invalid("connection profile");
  return value;
}

function envelope(value: unknown, fields: readonly string[]) {
  const body = record(value, ["apiVersion", "dataOrigin", ...fields], "response");
  if (body.apiVersion !== apiVersion) return invalid("API version");
  let dataOrigin: DataOrigin | undefined;
  if (body.dataOrigin !== undefined) {
    if (body.dataOrigin !== "synthetic" && body.dataOrigin !== "live") return invalid("data origin");
    dataOrigin = body.dataOrigin;
  }
  return { body, dataOrigin };
}

function connection(value: unknown, workspace: string | null): SlackConnection {
  const item = record(value, connectionFields, "connection metadata");
  const workspaceId = identifier(item.workspaceId, "connection workspace");
  const name = text(item.name, "connection name");
  if (workspaceId !== workspace || encoder.encode(name).byteLength > 256) return invalid("connection metadata");
  return {
    id: identifier(item.id, "connection identifier"), workspaceId, profile: profile(item.profile), name,
    channel: channel(item.channel), enabled: boolean(item.enabled, "connection enabled state"),
    credentialConfigured: boolean(item.credentialConfigured, "stored credential metadata"),
    revision: integer(item.revision, "connection revision", 1), createdAt: timestamp(item.createdAt), updatedAt: timestamp(item.updatedAt),
  };
}

function connectionResponse(value: unknown, workspace: string | null): SlackConnectionResponse {
  const { body, dataOrigin } = envelope(value, ["connection"]);
  return { apiVersion, dataOrigin, connection: connection(body.connection, workspace) };
}

function browserLink(value: unknown, field: string): string {
  const result = text(value, field);
  if (!URL.canParse(result) || /[\p{Cc}\p{White_Space}\\]/u.test(result)) return invalid(field);
  const url = new URL(result);
  if (!["https:", "http:"].includes(url.protocol) || url.username || url.password) return invalid(field);
  return result;
}

function delivery(value: unknown, workspace: string | null, findingId: string): FindingDelivery {
  const item = record(value, deliveryFields, "delivery");
  const workspaceId = identifier(item.workspaceId, "delivery workspace");
  if (workspaceId !== workspace || identifier(item.findingId, "delivery finding") !== findingId) return invalid("delivery scope");
  const state = states.find((state) => state === item.state);
  if (!state) return invalid("delivery state");
  const payload = record(item.payload, ["title", "body", "deepLink"], "notification snapshot");
  const deepLink = browserLink(payload.deepLink, "finding link");
  const link = new URL(deepLink);
  if (link.hash !== `#/work?finding=${encodeURIComponent(findingId)}`) return invalid("snapshot finding link");
  const selectedChannel = channel(item.channel);
  const dispatchStartedAt = item.dispatchStartedAt === null ? null : timestamp(item.dispatchStartedAt);
  const completedAt = item.completedAt === null ? null : timestamp(item.completedAt);
  let receipt: FindingDelivery["receipt"] = null;
  let failure: FindingDelivery["failure"] = null;
  if (item.receipt !== null) {
    const value = record(item.receipt, ["remoteId", "remoteUrl"], "native receipt");
    receipt = {
      remoteId: text(value.remoteId, "native receipt identifier", true),
      remoteUrl: value.remoteUrl === "" ? "" : browserLink(value.remoteUrl, "native receipt URL"),
    };
  }
  if (item.failure !== null) {
    const value = record(item.failure, ["code", "nativeCode", "httpStatus", "retryAfterSeconds", "retryable"], "delivery failure");
    const httpStatus = integer(value.httpStatus, "native HTTP status");
    if (value.retryable !== false || httpStatus > 599) return invalid("delivery failure");
    failure = {
      code: text(value.code, "failure code"), nativeCode: text(value.nativeCode, "native failure code", true),
      httpStatus, retryAfterSeconds: integer(value.retryAfterSeconds, "retry-after seconds"), retryable: false,
    };
  }
  const waiting = state === "queued" || state === "dispatching";
  const successful = state === "confirmed" || state === "accepted";
  if ((waiting && (completedAt !== null || receipt !== null || failure !== null)) ||
    (!waiting && completedAt === null) || (state === "queued" && dispatchStartedAt !== null) ||
    (state === "dispatching" && dispatchStartedAt === null) ||
    (successful && (receipt === null || failure !== null)) ||
    (!waiting && !successful && (failure === null || receipt !== null)) ||
    (state === "confirmed" && (!receipt || !new RegExp(`^${selectedChannel}:[0-9]{1,20}\\.[0-9]{1,10}$`).test(receipt.remoteId)))) {
    return invalid("delivery outcome");
  }
  return {
    id: identifier(item.id, "delivery identifier"), workspaceId, findingId,
    connectionId: identifier(item.connectionId, "delivery connection"), connectionRevision: integer(item.connectionRevision, "connection revision", 1),
    profile: profile(item.profile), channel: selectedChannel, requestedBy: identifier(item.requestedBy, "delivery requester"), state,
    payload: { title: text(payload.title, "notification title"), body: text(payload.body, "notification body"), deepLink },
    createdAt: timestamp(item.createdAt), dispatchStartedAt, completedAt, receipt, failure,
  };
}

function deliveryResponse(value: unknown, workspace: string | null, findingId: string): FindingDeliveryResponse {
  const { body, dataOrigin } = envelope(value, ["delivery"]);
  return { apiVersion, dataOrigin, delivery: delivery(body.delivery, workspace, findingId) };
}

function page<T extends { id: string }>(value: unknown, parse: (value: unknown) => T, limit: number, cursor: string | null): SlackPage<T> {
  const { body, dataOrigin } = envelope(value, ["items", "total", "nextCursor"]);
  if (!Array.isArray(body.items)) return invalid("collection");
  const items = body.items.map(parse);
  const total = integer(body.total, "collection total");
  const nextCursor = body.nextCursor === null ? null : identifier(body.nextCursor, "collection cursor");
  if (total < items.length || items.length > limit ||
    items.some((item, index) => item.id <= (index === 0 ? cursor ?? "" : items[index - 1].id)) ||
    (nextCursor !== null && (nextCursor !== items.at(-1)?.id || total <= items.length))) return invalid("collection page");
  return { apiVersion, dataOrigin, items, total, nextCursor };
}

function pageQuery(limit: number, cursor: string | null): string {
  if (!Number.isInteger(limit) || limit < 1 || limit > 500 || (cursor !== null && !/^[a-f0-9]{32}$/.test(cursor))) {
    throw new APIError("Choose a supported collection limit and continuation.", "invalid-input", false);
  }
  const query = new URLSearchParams({ limit: String(limit) });
  if (cursor !== null) query.set("cursor", cursor);
  return query.toString();
}

function validateText(value: string, name: string, bytes: number) {
  if (/^\p{White_Space}*$/u.test(value) || value.includes("\0") || encoder.encode(value).byteLength > bytes) {
    throw new APIError(`${name} must be nonblank, NUL-free and at most ${bytes} UTF-8 bytes.`, "invalid-input", false);
  }
}

function connectionInput(input: SlackConnectionPatch): SlackConnectionPatch {
  const body: SlackConnectionPatch = {};
  if (input.name !== undefined) { validateText(input.name, "Connection name", 256); body.name = input.name; }
  if (input.channel !== undefined) {
    if (!channelPattern.test(input.channel)) throw new APIError("Enter a Slack channel ID beginning with C or G, not a channel name or URL.", "invalid-input", false);
    body.channel = input.channel;
  }
  if (input.token !== undefined) {
    if (!input.token || encoder.encode(input.token).byteLength > 16384 ||
      /^\p{White_Space}|\p{White_Space}$/u.test(input.token) || /\p{Cc}/u.test(input.token)) {
      throw new APIError("Enter an opaque bot token of at most 16384 UTF-8 bytes, without edge whitespace or control characters.", "invalid-input", false);
    }
    body.token = input.token;
  }
  if (input.enabled !== undefined) {
    if (typeof input.enabled !== "boolean") throw new APIError("Choose whether this connection is enabled for notifications.", "invalid-input", false);
    body.enabled = input.enabled;
  }
  return body;
}

function bodyLimit(body: object, maximum: number) {
  if (encoder.encode(JSON.stringify(body)).byteLength > maximum) throw new APIError("The encoded request is too large. Shorten the input.", "invalid-input", false);
}

function safeFailure(cause: unknown, operation: "read" | "connection" | "enqueue"): never {
  if (!(cause instanceof APIError)) throw cause;
  let message: string;
  switch (cause.code) {
    case "forbidden": message = "Permission denied. The service did not permit this operation. Ask an administrator to review your workspace access."; break;
    case "unauthorized": message = "Your session ended. Sign in to continue."; break;
    case "not-found": message = "The requested connection or delivery is unavailable in this workspace."; break;
    case "conflict": message = operation === "enqueue"
      ? "The notification conflicts with current state. The connection may be disabled or the original intent may have changed. Review authorized history; do not create a replacement attempt."
      : "The connection conflicts with current server state. Reopen its details before saving."; break;
    case "network":
    case "unavailable": message = operation === "connection"
      ? "Connection configuration is unavailable. Ask your operator to check the service and server-side credential encryption. Your nonsecret draft is unchanged; re-enter the bot token if needed."
      : operation === "enqueue"
        ? "The notification acknowledgement could not be confirmed. It may already be queued. Only explicitly confirm the same notification or read delivery history."
        : "The service is unavailable. The requested data could not be loaded. Retry this read when the service is available."; break;
    case "invalid-response": message = operation === "enqueue"
      ? "The notification acknowledgement was invalid. Its outcome cannot be confirmed. Keep the same intent or review authorized history."
      : "The service returned invalid metadata. No replacement data was loaded."; break;
    default: message = "The service rejected the request. Check the permitted fields and their size limits before trying again.";
  }
  // Errors from a credential write must never echo a response body or request identifier.
  throw new APIError(message, cause.code, cause.retryable);
}

async function read<T>(path: string, parse: (value: unknown) => T, signal: AbortSignal): Promise<T> {
  const scopedSignal = AbortSignal.any([signal, requestAuthority().signal]);
  await Promise.resolve();
  scopedSignal.throwIfAborted();
  return request(path, parse, { signal: scopedSignal, expectedStatus: 200 }).catch((cause: unknown) => safeFailure(cause, "read"));
}

export const slackApi = {
  connections: (limit: number, cursor: string | null, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    return read(`/api/v1/integrations/connections?${pageQuery(limit, cursor)}`,
      (value) => page(value, (item) => connection(item, workspace), limit, cursor), signal);
  },
  connection: (id: string, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    return read(`/api/v1/integrations/connections/${encodeURIComponent(id)}`, (value) => {
      const result = connectionResponse(value, workspace);
      if (result.connection.id !== id) return invalid("selected connection");
      return result;
    }, signal);
  },
  createConnection: (input: SlackConnectionInput, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    const body = { ...connectionInput(input), profile: "slack-workspace-bot" };
    if (body.name === undefined || body.channel === undefined || body.token === undefined || body.enabled === undefined) {
      throw new APIError("Complete the connection name, channel, bot token and explicit enabled choice.", "invalid-input", false);
    }
    bodyLimit(body, 32 << 10);
    return request("/api/v1/integrations/connections", (value) => {
      const result = connectionResponse(value, workspace);
      if (result.connection.name !== body.name || result.connection.channel !== body.channel ||
        result.connection.enabled !== body.enabled || !result.connection.credentialConfigured || result.connection.revision !== 1) {
        return invalid("created connection acknowledgement");
      }
      return result;
    }, { method: "POST", body, signal, expectedStatus: 201 }).catch((cause: unknown) => safeFailure(cause, "connection"));
  },
  updateConnection: (original: SlackConnection, input: SlackConnectionPatch, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    const body = connectionInput(input);
    bodyLimit(body, 32 << 10);
    return request(`/api/v1/integrations/connections/${encodeURIComponent(original.id)}`, (value) => {
      const result = connectionResponse(value, workspace), saved = result.connection;
      if (saved.id !== original.id || saved.revision < original.revision ||
        (body.name !== undefined && saved.name !== body.name) || (body.channel !== undefined && saved.channel !== body.channel) ||
        (body.enabled !== undefined && saved.enabled !== body.enabled) || (body.token !== undefined && !saved.credentialConfigured)) {
        return invalid("updated connection acknowledgement");
      }
      return result;
    }, { method: "PATCH", body, signal, expectedStatus: 200 }).catch((cause: unknown) => safeFailure(cause, "connection"));
  },
  history: (findingId: string, limit: number, cursor: string | null, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    return read(`/api/v1/findings/${encodeURIComponent(findingId)}/deliveries?${pageQuery(limit, cursor)}`,
      (value) => page(value, (item) => delivery(item, workspace, findingId), limit, cursor), signal);
  },
  delivery: (id: string, findingId: string, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    return read(`/api/v1/integrations/deliveries/${encodeURIComponent(id)}`, (value) => {
      const result = deliveryResponse(value, workspace, findingId);
      if (result.delivery.id !== id) return invalid("selected delivery");
      return result;
    }, signal);
  },
  enqueue: (findingId: string, input: FindingDeliveryInput, actorId: string, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    const body = { connectionId: input.connectionId, idempotencyKey: input.idempotencyKey };
    if (!/^[a-f0-9]{32}$/.test(body.connectionId)) throw new APIError("Select a workspace connection.", "invalid-input", false);
    validateText(body.idempotencyKey, "Notification intent", 256);
    bodyLimit(body, 16 << 10);
    return request(`/api/v1/findings/${encodeURIComponent(findingId)}/deliveries`, (value, status) => {
      const result = deliveryResponse(value, workspace, findingId);
      if (result.delivery.connectionId !== body.connectionId || result.delivery.requestedBy !== actorId ||
        (status === 202 && result.delivery.state !== "queued")) return invalid("notification acknowledgement");
      return result;
    }, { method: "POST", body, signal, expectedStatus: [200, 202] }).catch((cause: unknown) => safeFailure(cause, "enqueue"));
  },
};
