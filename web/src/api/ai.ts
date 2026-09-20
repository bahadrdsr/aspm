import { APIError, request } from "./client";
import { requestAuthority } from "./authorization";
import { apiVersion } from "./types";
import { aiFamilies, aiPolicyModes } from "./ai-types";
import type { AIGrant, AIGrantInput, AIPage, AIPolicy, AIPolicyMode, AIProfile, AIProfileInput, AIProfilePatch } from "./ai-types";
import { aiBodyLimit, validAIEndpoint, validateAIProfile } from "./ai-input";

const encoder = new TextEncoder();
const profilesPath = "/api/v1/ai/profiles", policyPath = "/api/v1/ai/policy", grantsPath = "/api/v1/ai/grants";
const profileFields = ["id", "workspaceId", "name", "family", "endpoint", "model", "deployment", "enabled",
  "structuredOutput", "credentialConfigured", "revision", "createdAt", "updatedAt"];
const grantFields = ["id", "workspaceId", "profileId", "profileRevision", "policyRevision", "destination", "task",
  "dataClass", "expiresAt", "createdAt", "grantedBy", "revokedAt", "revokedBy"];

function invalid(field: string): never {
  throw new APIError(`The service returned invalid AI ${field}. No replacement metadata was loaded.`, "invalid-response", false);
}
function isRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === "object" && !Array.isArray(value);
}
function record(value: unknown, fields: readonly string[], field: string): Record<string, unknown> {
  if (!isRecord(value) || Object.keys(value).length !== fields.length || Object.keys(value).some((key) => !fields.includes(key))) return invalid(field);
  return value;
}
function text(value: unknown, field: string, maximum?: number, empty = false): string {
  if (typeof value !== "string" || (!empty && value.trim() === "") || value.includes("\0") ||
    maximum !== undefined && encoder.encode(value).byteLength > maximum) return invalid(field);
  return value;
}
function identifier(value: unknown): string {
  const id = text(value, "identifier");
  if (!/^[a-f0-9]{32}$/.test(id)) return invalid("identifier");
  return id;
}
function choice<T extends string>(value: unknown, choices: readonly T[], field: string): T {
  const selected = choices.find((item) => item === value);
  if (selected === undefined) return invalid(field);
  return selected;
}
function boolean(value: unknown): boolean {
  if (typeof value !== "boolean") return invalid("boolean");
  return value;
}
function timestamp(value: unknown): string {
  const result = text(value, "timestamp");
  if (!/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?(?:Z|[+-]\d\d:\d\d)$/.test(result) || !Number.isFinite(Date.parse(result))) return invalid("timestamp");
  return result;
}
function nullableTime(value: unknown): string | null { return value === null ? null : timestamp(value); }
function envelope(value: unknown, fields: readonly string[]): Record<string, unknown> {
  const body = record(value, ["apiVersion", ...fields], "response");
  if (body.apiVersion !== apiVersion) return invalid("API version");
  return body;
}
function profile(value: unknown, workspace: string | null): AIProfile {
  const item = record(value, profileFields, "profile");
  const workspaceId = identifier(item.workspaceId), family = choice(item.family, aiFamilies, "family");
  const endpoint = text(item.endpoint, "endpoint", 16384), deployment = text(item.deployment, "deployment", 256, family !== "azure-foundry");
  if (workspaceId !== workspace || !validAIEndpoint(endpoint, family) || family !== "azure-foundry" && deployment !== "") return invalid("profile scope or endpoint");
  return {
    id: identifier(item.id), workspaceId, name: text(item.name, "profile name", 256), family, endpoint,
    model: text(item.model, "model", 256), deployment, enabled: boolean(item.enabled), structuredOutput: boolean(item.structuredOutput),
    credentialConfigured: boolean(item.credentialConfigured), revision: text(item.revision, "profile revision"),
    createdAt: timestamp(item.createdAt), updatedAt: timestamp(item.updatedAt),
  };
}
function policy(value: unknown, workspace: string | null): AIPolicy {
  const item = record(value, ["workspaceId", "mode", "revision", "updatedAt", "updatedBy"], "policy");
  const workspaceId = identifier(item.workspaceId);
  const updatedAt = nullableTime(item.updatedAt), updatedBy = item.updatedBy === null ? null : identifier(item.updatedBy);
  if (workspaceId !== workspace || (updatedAt === null) !== (updatedBy === null)) return invalid("policy scope or author");
  return { workspaceId, mode: choice(item.mode, aiPolicyModes, "policy mode"), revision: text(item.revision, "policy revision"), updatedAt, updatedBy };
}
function grant(value: unknown, workspace: string | null): AIGrant {
  const item = record(value, grantFields, "grant");
  const workspaceId = identifier(item.workspaceId), destination = text(item.destination, "grant destination", 16384);
  const revokedAt = nullableTime(item.revokedAt), revokedBy = item.revokedBy === null ? null : identifier(item.revokedBy);
  if (workspaceId !== workspace || !validAIEndpoint(destination, "local") || (revokedAt === null) !== (revokedBy === null)) return invalid("grant scope or revocation");
  return {
    id: identifier(item.id), workspaceId, profileId: identifier(item.profileId), profileRevision: text(item.profileRevision, "profile revision"),
    policyRevision: text(item.policyRevision, "policy revision"), destination,
    task: choice(item.task, ["finding-validity"], "task"), dataClass: choice(item.dataClass, ["finding-evidence"], "data class"),
    expiresAt: timestamp(item.expiresAt), createdAt: timestamp(item.createdAt), grantedBy: identifier(item.grantedBy), revokedAt, revokedBy,
  };
}
function page<T extends { id: string }>(value: unknown, parse: (value: unknown) => T, cursor: string | null): AIPage<T> {
  const body = envelope(value, ["items", "total", "nextCursor"]);
  if (!Array.isArray(body.items)) return invalid("list");
  const items = body.items.map(parse), total = body.total;
  const nextCursor = body.nextCursor === null ? null : identifier(body.nextCursor);
  if (typeof total !== "number" || !Number.isSafeInteger(total) || total < items.length || items.length > 100 ||
    items.some((item, index) => item.id <= (index === 0 ? cursor ?? "" : items[index - 1].id)) ||
    nextCursor !== null && nextCursor !== items.at(-1)?.id) return invalid("page count or cursor");
  return { apiVersion, items, total, nextCursor };
}
function pathFor(path: string, id: string): string {
  if (!/^[a-f0-9]{32}$/.test(id)) throw new APIError("Select a native AI record from this workspace.", "invalid-input", false);
  return `${path}/${id}`;
}
function pagePath(path: string, cursor: string | null): string {
  if (cursor === null) return path;
  if (!/^[a-f0-9]{32}$/.test(cursor)) throw new APIError("A native AI continuation cursor is required.", "invalid-input", false);
  return `${path}?${new URLSearchParams({ cursor })}`;
}
function safeFailure(cause: unknown): never {
  if (!(cause instanceof APIError)) throw cause;
  let message: string;
  switch (cause.code) {
    case "unauthorized": message = "Your session ended. Sign in to continue."; break;
    case "forbidden": message = "Permission denied. This operation is not permitted by the service. Ask an administrator to review workspace access."; break;
    case "not-found": message = "The requested AI metadata was not found in this workspace. Review current configuration again."; break;
    case "conflict": message = "Configuration changed or conflicted with this request. Review current profile and policy facts and approve again. Nothing was automatically resubmitted."; break;
    case "network":
    case "unavailable": message = "AI configuration is unavailable or the receipt could not be confirmed. Ask the operator to check the service and server-side credential encryption. Keyless local profiles do not require a stored key."; break;
    case "invalid-response": message = "The service returned invalid AI metadata. No replacement data or successful write was confirmed."; break;
    case "too-large": message = "The service rejected the request size. AI configuration JSON is limited to 32 KiB."; break;
    default: message = "The service rejected this AI configuration request. Check the selected fields, endpoint and input size limits.";
  }
  // Diagnostics and request IDs are untrusted and may echo credential drafts.
  throw new APIError(message, cause.code, cause.retryable);
}
async function read<T>(path: string, parse: (value: unknown) => T, signal: AbortSignal): Promise<T> {
  const scoped = AbortSignal.any([signal, requestAuthority().signal]);
  await Promise.resolve();
  scoped.throwIfAborted();
  return request(path, parse, { signal: scoped, expectedStatus: 200 }).catch(safeFailure);
}
function profileReceipt(value: unknown, workspace: string | null, id?: string): AIProfile {
  const result = profile(envelope(value, ["profile"]).profile, workspace);
  if (id !== undefined && result.id !== id) return invalid("selected profile");
  return result;
}
function savedProfile(value: unknown, workspace: string | null, input: AIProfilePatch, original?: AIProfile): AIProfile {
  const result = profileReceipt(value, workspace, original?.id);
  if ((input.name !== undefined && result.name !== input.name) || (input.family !== undefined && result.family !== input.family) ||
    (input.endpoint !== undefined && result.endpoint !== input.endpoint) || (input.model !== undefined && result.model !== input.model) ||
    (input.deployment !== undefined && result.deployment !== input.deployment) || (input.enabled !== undefined && result.enabled !== input.enabled) ||
    (input.structuredOutput !== undefined && result.structuredOutput !== input.structuredOutput) ||
    (input.apiKey !== undefined && result.credentialConfigured !== (input.apiKey !== null)) ||
    (!original && result.credentialConfigured !== (typeof input.apiKey === "string"))) return invalid("profile receipt fields");
  return result;
}
function grantReceipt(value: unknown, workspace: string | null, id?: string): AIGrant {
  const result = grant(envelope(value, ["grant"]).grant, workspace);
  if (id !== undefined && result.id !== id) return invalid("selected grant");
  return result;
}
function matchesGrantInput(result: AIGrant, input: AIGrantInput): boolean {
  return result.profileId === input.profileId && result.profileRevision === input.profileRevision &&
    result.policyRevision === input.policyRevision && result.destination === input.destination && result.task === input.task &&
    result.dataClass === input.dataClass && Date.parse(result.expiresAt) === Date.parse(input.expiresAt);
}

export const aiApi = {
  profiles: (cursor: string | null, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    return read(pagePath(profilesPath, cursor), (value) => page(value, (item) => profile(item, workspace), cursor), signal);
  },
  profile: (id: string, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    return read(pathFor(profilesPath, id), (value) => profileReceipt(value, workspace, id), signal);
  },
  createProfile: (input: AIProfileInput, signal: AbortSignal) => {
    validateAIProfile(input);
    const workspace = requestAuthority().workspace;
    return request(profilesPath, (value) => savedProfile(value, workspace, input),
      { method: "POST", body: input, signal, expectedStatus: 201 }).catch(safeFailure);
  },
  updateProfile: (original: AIProfile, input: AIProfilePatch, signal: AbortSignal) => {
    validateAIProfile(input, original);
    if (Object.keys(input).length === 0) throw new APIError("There are no profile changes to save. A blank API key keeps the stored key.", "invalid-input", false);
    const workspace = requestAuthority().workspace;
    if (original.workspaceId !== workspace) throw new APIError("The profile workspace changed. Reopen its current details.", "forbidden", false);
    return request(pathFor(profilesPath, original.id), (value) => savedProfile(value, workspace, input, original),
      { method: "PATCH", body: input, signal, expectedStatus: 200 }).catch(safeFailure);
  },
  policy: (signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    return read(policyPath, (value) => policy(envelope(value, ["policy"]).policy, workspace), signal);
  },
  updatePolicy: (mode: AIPolicyMode, signal: AbortSignal) => {
    if (!aiPolicyModes.some((value) => value === mode)) throw new APIError("Choose an explicit AI policy mode.", "invalid-input", false);
    const workspace = requestAuthority().workspace;
    return request(policyPath, (value) => {
      const result = policy(envelope(value, ["policy"]).policy, workspace);
      if (result.mode !== mode) return invalid("policy receipt mode");
      return result;
    },
      { method: "PATCH", body: { mode }, signal, expectedStatus: 200 }).catch(safeFailure);
  },
  grants: (cursor: string | null, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    return read(pagePath(grantsPath, cursor), (value) => page(value, (item) => grant(item, workspace), cursor), signal);
  },
  grant: (id: string, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    return read(pathFor(grantsPath, id), (value) => grantReceipt(value, workspace, id), signal);
  },
  createGrant: (input: AIGrantInput, signal: AbortSignal) => {
    if (!/^[a-f0-9]{32}$/.test(input.profileId) || !input.profileRevision || !input.policyRevision ||
      !validAIEndpoint(input.destination, "local") || input.task !== "finding-validity" || input.dataClass !== "finding-evidence" ||
      !/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?Z$/.test(input.expiresAt) ||
      !Number.isFinite(Date.parse(input.expiresAt)) || Date.parse(input.expiresAt) <= Date.now()) {
      throw new APIError("Review current configuration and choose a finite future UTC expiry before approving.", "invalid-input", false);
    }
    aiBodyLimit(input);
    const workspace = requestAuthority().workspace;
    return request(grantsPath, (value) => {
      const result = grantReceipt(value, workspace);
      if (!matchesGrantInput(result, input) || result.revokedAt !== null || result.revokedBy !== null) return invalid("grant receipt bindings");
      return result;
    }, { method: "POST", body: input, signal, expectedStatus: 201 }).catch(safeFailure);
  },
  revokeGrant: (original: AIGrant, signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    if (original.workspaceId !== workspace) throw new APIError("The grant workspace changed. Reopen its current details.", "forbidden", false);
    return request(`${pathFor(grantsPath, original.id)}/revoke`, (value) => {
      const result = grantReceipt(value, workspace, original.id);
      if (!matchesGrantInput(result, original) || result.createdAt !== original.createdAt || result.grantedBy !== original.grantedBy ||
        result.revokedAt === null || result.revokedBy === null ||
        original.revokedAt !== null && (result.revokedAt !== original.revokedAt || result.revokedBy !== original.revokedBy)) return invalid("revocation receipt");
      return result;
    }, { method: "POST", body: {}, signal, expectedStatus: 200 }).catch(safeFailure);
  },
};
