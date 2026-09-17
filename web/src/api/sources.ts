import { APIError, request } from "./client";
import { requestAuthority } from "./authorization";
import { apiVersion } from "./types";
import type { DataOrigin } from "./types";
import type {
  CollectionResponse, SourceCollection, SourceConnection, SourceEvidence, SourceInput, SourcePage,
  SourcePatch, SourceRecord, SourceResponse,
} from "./source-types";

const encoder = new TextEncoder();
const repositoryPattern = /^[A-Za-z0-9_][A-Za-z0-9_.-]{0,254}\/[A-Za-z0-9_][A-Za-z0-9_.-]{0,254}$/;
const sourceFields = ["id", "workspaceId", "profile", "name", "repository", "enabled", "credentialConfigured", "revision", "createdAt", "updatedAt"];
const collectionFields = ["id", "workspaceId", "sourceId", "profile", "connectionRevision", "repository", "requestedBy", "state",
  "complete", "assetId", "repositoryId", "recordCount", "gaps", "createdAt", "collectedAt", "completedAt", "failure"];
const recordFields = ["id", "collectionId", "ordinal", "kind", "externalId", "parentId", "nativeRunId", "state", "severity",
  "location", "rawURL", "sourceScanAt", "sourceUpdatedAt", "evidence"];
const states = ["queued", "collecting", "succeeded", "partial", "blocked", "failed"] as const;

function invalid(field: string): never {
  throw new APIError(`The service returned invalid ${field}. No replacement data was loaded.`, "invalid-response", false);
}
function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}
function record(value: unknown, fields: readonly string[], field: string): Record<string, unknown> {
  if (!isRecord(value) || Object.keys(value).some((key) => !fields.includes(key))) return invalid(field);
  return value;
}
function text(value: unknown, field: string, empty = false): string {
  if (typeof value !== "string" || (!empty && /^\p{White_Space}*$/u.test(value)) || value.includes("\0")) return invalid(field);
  return value;
}
function identifier(value: unknown, field: string): string {
  const id = text(value, field);
  if (!/^[a-f0-9]{32}$/.test(id)) return invalid(field);
  return id;
}
function integer(value: unknown, field: string, minimum = 0, maximum = Number.MAX_SAFE_INTEGER): number {
  if (typeof value !== "number" || !Number.isSafeInteger(value) || value < minimum || value > maximum) return invalid(field);
  return value;
}
function boolean(value: unknown, field: string): boolean {
  if (typeof value !== "boolean") return invalid(field);
  return value;
}
function timestamp(value: unknown): string {
  const stamp = text(value, "source timestamp");
  if (!/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?(?:Z|[+-]\d\d:\d\d)$/.test(stamp) || !Number.isFinite(Date.parse(stamp))) return invalid("source timestamp");
  return stamp;
}
function nullableTime(value: unknown): string | null { return value === null ? null : timestamp(value); }
function profile(value: unknown): "github-cloud-app" {
  if (value !== "github-cloud-app") return invalid("source profile");
  return value;
}
function repository(value: unknown): string {
  const name = text(value, "selected repository");
  if (encoder.encode(name).byteLength > 256 || !repositoryPattern.test(name)) return invalid("selected repository");
  return name;
}
function nativeId(value: unknown, empty = false): string {
  const id = text(value, "native identifier", empty);
  if (!(empty && id === "") && !/^[1-9][0-9]{0,19}$/.test(id)) return invalid("native identifier");
  return id;
}
function envelope(value: unknown, fields: readonly string[]) {
  const body = record(value, ["apiVersion", "dataOrigin", ...fields], "source response");
  if (body.apiVersion !== apiVersion) return invalid("API version");
  let dataOrigin: DataOrigin | undefined;
  if (body.dataOrigin !== undefined) {
    if (body.dataOrigin !== "live" && body.dataOrigin !== "synthetic") return invalid("data origin");
    dataOrigin = body.dataOrigin;
  }
  return { body, dataOrigin };
}
function source(value: unknown, workspace: string | null): SourceConnection {
  const item = record(value, sourceFields, "source metadata");
  const workspaceId = identifier(item.workspaceId, "source workspace");
  const name = text(item.name, "source name");
  if (workspaceId !== workspace || encoder.encode(name).byteLength > 256) return invalid("source metadata");
  return {
    id: identifier(item.id, "source identifier"), workspaceId, profile: profile(item.profile), name,
    repository: repository(item.repository), enabled: boolean(item.enabled, "source enabled state"),
    credentialConfigured: boolean(item.credentialConfigured, "stored credential metadata"),
    revision: integer(item.revision, "source revision", 1), createdAt: timestamp(item.createdAt), updatedAt: timestamp(item.updatedAt),
  };
}
function sourceResponse(value: unknown, workspace: string | null): SourceResponse {
  const { body, dataOrigin } = envelope(value, ["source"]);
  return { apiVersion, dataOrigin, source: source(body.source, workspace) };
}
function collection(value: unknown, workspace: string | null, sourceId: string): SourceCollection {
  const item = record(value, collectionFields, "collection");
  const workspaceId = identifier(item.workspaceId, "collection workspace");
  if (workspaceId !== workspace || identifier(item.sourceId, "collection source") !== sourceId) return invalid("collection scope");
  const state = states.find((state) => state === item.state);
  if (!state) return invalid("collection state");
  const complete = boolean(item.complete, "selected feed completeness");
  const completedAt = nullableTime(item.completedAt);
  if ((complete && state !== "succeeded") || (["queued", "collecting"].includes(state) !== (completedAt === null))) return invalid("collection outcome");
  let failure: SourceCollection["failure"] = null;
  if (item.failure !== null) {
    const error = record(item.failure, ["code", "nativeCode", "httpStatus", "retryAfterSeconds", "retryable"], "native failure");
    if (error.retryable !== false) return invalid("collection retry policy");
    failure = {
      code: text(error.code, "collection failure code"), nativeCode: text(error.nativeCode, "native failure code", true),
      httpStatus: integer(error.httpStatus, "native HTTP status", 0, 599),
      retryAfterSeconds: integer(error.retryAfterSeconds, "retry-after seconds"), retryable: false,
    };
  }
  if (!Array.isArray(item.gaps)) return invalid("collection gaps");
  return {
    id: identifier(item.id, "collection identifier"), workspaceId, sourceId, profile: profile(item.profile),
    connectionRevision: integer(item.connectionRevision, "collection source revision", 1),
    repository: repository(item.repository), requestedBy: identifier(item.requestedBy, "collection requester"),
    state, complete, assetId: item.assetId === null ? null : identifier(item.assetId, "collection asset"),
    repositoryId: item.repositoryId === null ? null : nativeId(item.repositoryId),
    recordCount: integer(item.recordCount, "collection record count", 0, 6401),
    gaps: item.gaps.map((gap) => text(gap, "collection gap")),
    createdAt: timestamp(item.createdAt), collectedAt: nullableTime(item.collectedAt), completedAt, failure,
  };
}
function collectionResponse(value: unknown, workspace: string | null, sourceId: string): CollectionResponse {
  const { body, dataOrigin } = envelope(value, ["collection"]);
  return { apiVersion, dataOrigin, collection: collection(body.collection, workspace, sourceId) };
}
function sourceRecord(value: unknown, collectionId: string): SourceRecord {
  const item = record(value, recordFields, "source record");
  if (identifier(item.collectionId, "record collection") !== collectionId) return invalid("record scope");
  if (item.kind !== "repository" && item.kind !== "finding") return invalid("raw record kind");
  const evidence = record(item.evidence, ["sha256", "sizeBytes"], "source evidence metadata");
  const sha256 = text(evidence.sha256, "evidence digest");
  if (!/^sha256:[a-f0-9]{64}$/.test(sha256)) return invalid("evidence digest");
  return {
    id: identifier(item.id, "record identifier"), collectionId, ordinal: integer(item.ordinal, "record ordinal", 0, 6400),
    kind: item.kind, externalId: nativeId(item.externalId), parentId: nativeId(item.parentId, true),
    nativeRunId: text(item.nativeRunId, "native run identifier", true), state: text(item.state, "native state", true),
    severity: text(item.severity, "native severity", true), location: text(item.location, "source location", true),
    rawURL: text(item.rawURL, "raw provenance URL", true),
    sourceScanAt: nullableTime(item.sourceScanAt), sourceUpdatedAt: nullableTime(item.sourceUpdatedAt),
    evidence: { sha256, sizeBytes: integer(evidence.sizeBytes, "evidence byte count", 0, 32 << 20) },
  };
}
function page<T extends { id: string }>(value: unknown, parse: (value: unknown) => T, cursor: string | null): SourcePage<T> {
  const { body, dataOrigin } = envelope(value, ["items", "total", "nextCursor"]);
  if (!Array.isArray(body.items)) return invalid("source page");
  const items = body.items.map(parse);
  const total = integer(body.total, "source page total");
  const nextCursor = body.nextCursor === null ? null : identifier(body.nextCursor, "source page cursor");
  if (items.length > 100 || total < items.length ||
    items.some((item, index) => item.id <= (index === 0 ? cursor ?? "" : items[index - 1].id)) ||
    (nextCursor !== null && nextCursor !== items.at(-1)?.id)) return invalid("source page order");
  return { apiVersion, dataOrigin, items, total, nextCursor };
}
function pagePath(path: string, cursor: string | null): string {
  if (cursor === null) return path;
  if (!/^[a-f0-9]{32}$/.test(cursor)) throw new APIError("A native source cursor is required.", "invalid-input", false);
  return `${path}?${new URLSearchParams({ cursor })}`;
}
function validInput(value: string, label: string, maximum: number) {
  if (/^\p{White_Space}*$/u.test(value) || value.includes("\0") || encoder.encode(value).byteLength > maximum) {
    throw new APIError(`${label} must be nonblank, NUL-free and at most ${maximum} UTF-8 bytes.`, "invalid-input", false);
  }
}
function sourceInput(input: SourcePatch): SourcePatch {
  const body: SourcePatch = {};
  if (input.name !== undefined) { validInput(input.name, "Source name", 256); body.name = input.name; }
  if (input.repository !== undefined) {
    if (encoder.encode(input.repository).byteLength > 256 || !repositoryPattern.test(input.repository)) {
      throw new APIError("Enter one selected repository as owner/repo, not a URL or a search.", "invalid-input", false);
    }
    body.repository = input.repository;
  }
  if (input.token !== undefined) {
    if (!input.token || encoder.encode(input.token).byteLength > 16384 ||
      /^\p{White_Space}|\p{White_Space}$/u.test(input.token) || /\p{Cc}/u.test(input.token)) {
      throw new APIError("Enter an opaque installation bearer of at most 16384 UTF-8 bytes, without edge whitespace or control characters.", "invalid-input", false);
    }
    body.token = input.token;
  }
  if (input.enabled !== undefined) {
    if (typeof input.enabled !== "boolean") throw new APIError("Choose an explicit enabled state.", "invalid-input", false);
    body.enabled = input.enabled;
  }
  if (encoder.encode(JSON.stringify(body)).byteLength > 32 << 10) throw new APIError("The encoded source request exceeds 32 KiB.", "too-large", false);
  return body;
}
function safeFailure(cause: unknown, operation: "read" | "source" | "enqueue" | "evidence"): never {
  if (!(cause instanceof APIError)) throw cause;
  let message: string;
  switch (cause.code) {
    case "unauthorized": message = "Your session ended. Sign in to continue."; break;
    case "forbidden": message = "Permission denied. This operation is not permitted by the service. Ask an administrator to review your workspace access."; break;
    case "not-found": message = "The requested source, collection or evidence was not found in this workspace."; break;
    case "conflict": message = "The request conflicts with current state. The source may be disabled or the original collection binding may have changed. Review authorized history before another attempt."; break;
    case "network":
    case "unavailable": message = operation === "source"
      ? "Source configuration is unavailable. Ask your operator to check the service and server-side credential encryption. Your nonsecret draft is unchanged."
      : operation === "enqueue"
        ? "Collection is unavailable or its acknowledgement could not be confirmed. Ask your operator to check collection evidence storage and service configuration. Keep the same intent if you confirm again."
        : operation === "evidence" ? "Raw evidence is unavailable. Storage or integrity could not be confirmed. Retry an authorized evidence read."
          : "The service is unavailable. Source data could not be loaded; retry this read."; break;
    case "invalid-response": message = operation === "enqueue"
      ? "The collection acknowledgement was invalid. Its outcome cannot be confirmed. Keep the same intent and review authorized history."
      : operation === "evidence" ? "Evidence integrity or format could not be confirmed. No replacement body was loaded."
        : "The service returned invalid source metadata. No replacement data was loaded."; break;
    default: message = "The service rejected this request. Check the permitted fields and size limits.";
  }
  throw new APIError(message, cause.code, cause.retryable);
}
async function read<T>(path: string, parse: (value: unknown) => T, signal: AbortSignal): Promise<T> {
  const scopedSignal = AbortSignal.any([signal, requestAuthority().signal]);
  await Promise.resolve();
  scopedSignal.throwIfAborted();
  return request(path, parse, { signal: scopedSignal, expectedStatus: 200 }).catch((cause: unknown) => safeFailure(cause, "read"));
}
async function evidenceBytes(response: Response, expected: number): Promise<ArrayBuffer> {
  if (response.headers.get("Content-Type")?.split(";")[0].trim().toLowerCase() !== "application/octet-stream" || !response.body) return invalid("evidence response");
  const reader = response.body.getReader();
  const chunks: Uint8Array[] = [];
  let length = 0;
  try {
    while (true) {
      const part = await reader.read();
      if (part.done) break;
      length += part.value.byteLength;
      if (length > expected) { await reader.cancel(); return invalid("evidence byte count"); }
      chunks.push(part.value);
    }
  } finally { reader.releaseLock(); }
  if (length !== expected) return invalid("evidence byte count");
  const bytes = new Uint8Array(length);
  let offset = 0;
  for (const chunk of chunks) { bytes.set(chunk, offset); offset += chunk.byteLength; }
  return bytes.buffer;
}

export const sourcesApi = {
  sources: (cursor: string | null, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    return read(pagePath("/api/v1/sources", cursor), (value) => page(value, (item) => source(item, workspace), cursor), signal);
  },
  source: (id: string, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    return read(`/api/v1/sources/${encodeURIComponent(id)}`, (value) => {
      const result = sourceResponse(value, workspace);
      if (result.source.id !== id) return invalid("selected source");
      return result;
    }, signal);
  },
  create: (input: SourceInput, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    const body = { profile: "github-cloud-app", ...sourceInput(input) };
    if (body.name === undefined || body.repository === undefined || body.token === undefined || body.enabled === undefined) {
      throw new APIError("Complete the source name, selected repository, installation bearer and enabled choice.", "invalid-input", false);
    }
    if (encoder.encode(JSON.stringify(body)).byteLength > 32 << 10) throw new APIError("The encoded source request exceeds 32 KiB.", "too-large", false);
    return request("/api/v1/sources", (value) => {
      const result = sourceResponse(value, workspace), saved = result.source;
      if (saved.name !== body.name || saved.repository !== body.repository || saved.enabled !== body.enabled ||
        !saved.credentialConfigured || saved.revision !== 1) return invalid("created source acknowledgement");
      return result;
    }, { method: "POST", body, signal, expectedStatus: 201 }).catch((cause: unknown) => safeFailure(cause, "source"));
  },
  update: (original: SourceConnection, input: SourcePatch, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace, body = sourceInput(input);
    return request(`/api/v1/sources/${encodeURIComponent(original.id)}`, (value) => {
      const result = sourceResponse(value, workspace), saved = result.source;
      if (saved.id !== original.id || saved.revision < original.revision ||
        (body.name !== undefined && saved.name !== body.name) || (body.repository !== undefined && saved.repository !== body.repository) ||
        (body.enabled !== undefined && saved.enabled !== body.enabled) || (body.token !== undefined && !saved.credentialConfigured)) return invalid("updated source acknowledgement");
      return result;
    }, { method: "PATCH", body, signal, expectedStatus: 200 }).catch((cause: unknown) => safeFailure(cause, "source"));
  },
  collections: (sourceId: string, cursor: string | null, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    return read(pagePath(`/api/v1/sources/${encodeURIComponent(sourceId)}/collections`, cursor),
      (value) => page(value, (item) => collection(item, workspace, sourceId), cursor), signal);
  },
  collection: (id: string, sourceId: string, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    return read(`/api/v1/sources/collections/${encodeURIComponent(id)}`, (value) => {
      const result = collectionResponse(value, workspace, sourceId);
      if (result.collection.id !== id) return invalid("selected collection");
      return result;
    }, signal);
  },
  enqueue: (sourceId: string, idempotencyKey: string, actorId: string, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    validInput(idempotencyKey, "Collection intent", 256);
    const body = { idempotencyKey };
    return request(`/api/v1/sources/${encodeURIComponent(sourceId)}/collections`, (value, status) => {
      const result = collectionResponse(value, workspace, sourceId), saved = result.collection;
      if (saved.requestedBy !== actorId || (status === 202 && (saved.state !== "queued" || saved.complete ||
        saved.recordCount !== 0 || saved.failure !== null || saved.collectedAt !== null || saved.assetId !== null ||
        saved.repositoryId !== null || saved.gaps.length !== 0))) return invalid("queued collection acknowledgement");
      return result;
    }, { method: "POST", body, signal, expectedStatus: [200, 202] }).catch((cause: unknown) => safeFailure(cause, "enqueue"));
  },
  records: (collectionId: string, cursor: string | null, signal: AbortSignal) =>
    read(pagePath(`/api/v1/sources/collections/${encodeURIComponent(collectionId)}/records`, cursor),
      (value) => page(value, (item) => sourceRecord(item, collectionId), cursor), signal),
  evidence: async (record: SourceRecord, signal: AbortSignal): Promise<SourceEvidence> => {
    const scopedSignal = AbortSignal.any([signal, requestAuthority().signal]);
    try {
      const bytes = await request(`/api/v1/sources/collections/${encodeURIComponent(record.collectionId)}/records/${encodeURIComponent(record.id)}/evidence`,
        (value) => { if (!(value instanceof ArrayBuffer)) return invalid("evidence bytes"); return value; },
        { signal: scopedSignal, expectedStatus: 200, headers: { Accept: "application/octet-stream" },
          decodeBody: (response) => evidenceBytes(response, record.evidence.sizeBytes) });
      const hash = await crypto.subtle.digest("SHA-256", bytes);
      scopedSignal.throwIfAborted();
      const sha256 = `sha256:${[...new Uint8Array(hash)].map((value) => value.toString(16).padStart(2, "0")).join("")}`;
      if (sha256 !== record.evidence.sha256) return invalid("evidence digest");
      let decoded: string | null;
      try { decoded = new TextDecoder("utf-8", { fatal: true, ignoreBOM: true }).decode(bytes); }
      catch (cause) { if (!(cause instanceof TypeError)) throw cause; decoded = null; }
      return { bytes, text: decoded };
    } catch (cause: unknown) { return safeFailure(cause, "evidence"); }
  },
};
