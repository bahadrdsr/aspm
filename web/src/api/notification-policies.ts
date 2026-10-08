import { APIError, request } from "./client";
import { requestAuthority } from "./authorization";
import { apiVersion } from "./types";
import type { Severity } from "./types";

export type NotificationChangeKind = "new" | "changed" | "reopened";
export type NotificationProfile =
  "slack-workspace-bot" | "teams-workflows-channel" | "jira-cloud-v3" | "generic-webhook-v1";
export type NotificationPolicyOutcome = "queued" | "duplicate-ticket" | "connection-stale" | "invalid-payload";

export interface NotificationPolicyConnection {
  id: string;
  name: string;
  profile: NotificationProfile;
  revision: number;
  enabled: boolean;
  current: boolean;
}

export interface NotificationPolicy {
  id: string;
  workspaceId: string;
  name: string;
  connectionId: string;
  connectionProfile: NotificationProfile;
  connectionRevision: number;
  enabled: boolean;
  changeKinds: NotificationChangeKind[];
  minimumSeverity: Severity;
  revision: number;
  epoch: number;
  approvedBy: string;
  approvedByName: string;
  rationale: string;
  createdAt: string;
  updatedAt: string;
  connection: NotificationPolicyConnection;
}

export interface NotificationPolicyEvent {
  id: string;
  workspaceId: string;
  policyId: string;
  policyRevision: number;
  findingId: string;
  findingChangeRevision: number;
  outcome: NotificationPolicyOutcome;
  deliveryId: string | null;
  createdAt: string;
}

export interface NotificationPolicyInput {
  name: string;
  connectionId: string;
  enabled: boolean;
  changeKinds: NotificationChangeKind[];
  minimumSeverity: Severity;
  rationale: string;
}

export type NotificationPolicyPatch = Partial<Omit<NotificationPolicyInput, "rationale">> & { rationale: string };

interface Page<T> {
  apiVersion: typeof apiVersion;
  items: T[];
  total: number;
  nextCursor: string | null;
}

const identifier = /^[a-f0-9]{32}$/;
const profiles: NotificationProfile[] = [
  "slack-workspace-bot", "teams-workflows-channel", "jira-cloud-v3", "generic-webhook-v1",
];
const changes: NotificationChangeKind[] = ["new", "changed", "reopened"];
const severities: Severity[] = ["critical", "high", "medium", "low", "info"];
const encoder = new TextEncoder();

function invalid(field: string): never {
  throw new APIError(`The service returned invalid notification-policy ${field}. No replacement state was loaded.`,
    "invalid-response", false);
}

function record(value: unknown, keys: readonly string[], field: string): Record<string, unknown> {
  if (!value || typeof value !== "object" || Array.isArray(value) ||
    Object.keys(value).length !== keys.length || keys.some((key) => !Object.hasOwn(value, key))) return invalid(field);
  return value as Record<string, unknown>;
}

function text(value: unknown, field: string): string {
  if (typeof value !== "string" || !value.trim() || value.includes("\0")) return invalid(field);
  return value;
}

function id(value: unknown, field: string): string {
  const result = text(value, field);
  return identifier.test(result) ? result : invalid(field);
}

function integer(value: unknown, field: string, minimum = 0): number {
  return typeof value === "number" && Number.isSafeInteger(value) && value >= minimum ? value : invalid(field);
}

function boolean(value: unknown, field: string): boolean {
  return typeof value === "boolean" ? value : invalid(field);
}

function timestamp(value: unknown, field: string): string {
  const result = text(value, field);
  if (!/^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?(?:Z|[+-]\d\d:\d\d)$/.test(result) ||
    !Number.isFinite(Date.parse(result))) return invalid(field);
  return result;
}

function choice<T extends string>(value: unknown, values: readonly T[], field: string): T {
  return typeof value === "string" && values.includes(value as T) ? value as T : invalid(field);
}

function envelope(value: unknown, keys: string[]) {
  const body = record(value, ["apiVersion", ...keys], "response");
  if (body.apiVersion !== apiVersion) return invalid("API version");
  return body;
}

function connection(value: unknown): NotificationPolicyConnection {
  const item = record(value, ["id", "name", "profile", "revision", "enabled", "current"], "connection");
  return {
    id: id(item.id, "connection identifier"),
    name: text(item.name, "connection name"),
    profile: choice(item.profile, profiles, "connection profile"),
    revision: integer(item.revision, "connection revision", 1),
    enabled: boolean(item.enabled, "connection enabled state"),
    current: boolean(item.current, "connection current state"),
  };
}

function policy(value: unknown, workspace: string | null): NotificationPolicy {
  const item = record(value, [
    "id", "workspaceId", "name", "connectionId", "connectionProfile", "connectionRevision",
    "enabled", "changeKinds", "minimumSeverity", "revision", "epoch", "approvedBy",
    "approvedByName", "rationale", "createdAt", "updatedAt", "connection",
  ], "policy");
  const workspaceId = id(item.workspaceId, "workspace");
  if (workspaceId !== workspace || !Array.isArray(item.changeKinds)) return invalid("scope or changes");
  const changeKinds = item.changeKinds.map((value) => choice(value, changes, "change kind"));
  if (changeKinds.length < 1 || changeKinds.length > 3 || new Set(changeKinds).size !== changeKinds.length) {
    return invalid("change kinds");
  }
  const selected = connection(item.connection), connectionId = id(item.connectionId, "selected connection");
  const profile = choice(item.connectionProfile, profiles, "selected profile");
  const connectionRevision = integer(item.connectionRevision, "selected connection revision", 1);
  if (selected.id !== connectionId || selected.profile !== profile ||
    selected.current !== (selected.profile === profile && selected.revision === connectionRevision)) {
    return invalid("connection snapshot");
  }
  return {
    id: id(item.id, "policy identifier"), workspaceId, name: text(item.name, "policy name"),
    connectionId, connectionProfile: profile, connectionRevision,
    enabled: boolean(item.enabled, "policy enabled state"), changeKinds,
    minimumSeverity: choice(item.minimumSeverity, severities, "minimum severity"),
    revision: integer(item.revision, "policy revision", 1), epoch: integer(item.epoch, "policy epoch", 1),
    approvedBy: id(item.approvedBy, "policy approver"), approvedByName: text(item.approvedByName, "approver name"),
    rationale: text(item.rationale, "policy rationale"), createdAt: timestamp(item.createdAt, "created time"),
    updatedAt: timestamp(item.updatedAt, "updated time"), connection: selected,
  };
}

function event(value: unknown, workspace: string | null, policyId: string): NotificationPolicyEvent {
  const item = record(value, [
    "id", "workspaceId", "policyId", "policyRevision", "findingId",
    "findingChangeRevision", "outcome", "deliveryId", "createdAt",
  ], "event");
  const workspaceId = id(item.workspaceId, "event workspace"), selectedPolicy = id(item.policyId, "event policy");
  if (workspaceId !== workspace || selectedPolicy !== policyId) return invalid("event scope");
  const outcome = choice(item.outcome,
    ["queued", "duplicate-ticket", "connection-stale", "invalid-payload"] as const, "event outcome");
  const deliveryId = item.deliveryId === null ? null : id(item.deliveryId, "event delivery");
  if ((outcome === "queued" || outcome === "duplicate-ticket") !== (deliveryId !== null)) {
    return invalid("event delivery semantics");
  }
  return {
    id: id(item.id, "event identifier"), workspaceId, policyId: selectedPolicy,
    policyRevision: integer(item.policyRevision, "event policy revision", 1),
    findingId: id(item.findingId, "event finding"),
    findingChangeRevision: integer(item.findingChangeRevision, "event finding revision", 1),
    outcome, deliveryId, createdAt: timestamp(item.createdAt, "event time"),
  };
}

function page<T extends { id: string }>(value: unknown, parse: (value: unknown) => T,
  cursor: string | null, maximum: number): Page<T> {
  const body = envelope(value, ["items", "total", "nextCursor"]);
  if (!Array.isArray(body.items)) return invalid("collection");
  const items = body.items.map(parse), total = integer(body.total, "collection total"),
    nextCursor = body.nextCursor === null ? null : id(body.nextCursor, "collection cursor");
  if (items.length > maximum || total < items.length ||
    items.some((item, index) => item.id <= (index ? items[index - 1].id : cursor ?? "")) ||
    nextCursor !== null && (nextCursor !== items.at(-1)?.id || total <= items.length)) {
    return invalid("collection page");
  }
  return { apiVersion, items, total, nextCursor };
}

function validateText(value: string, label: string, limit: number) {
  if (!value.trim() || value.includes("\0") || encoder.encode(value).byteLength > limit) {
    throw new APIError(`${label} must be nonblank, NUL-free and at most ${limit} UTF-8 bytes.`,
      "invalid-input", false);
  }
}

function validateInput(input: NotificationPolicyInput) {
  validateText(input.name, "Policy name", 256);
  validateText(input.rationale, "Rationale", 8192);
  if (!identifier.test(input.connectionId) || typeof input.enabled !== "boolean" ||
    input.changeKinds.length < 1 || input.changeKinds.length > 3 ||
    new Set(input.changeKinds).size !== input.changeKinds.length ||
    input.changeKinds.some((value) => !changes.includes(value)) ||
    !severities.includes(input.minimumSeverity)) {
    throw new APIError("Choose one current native connection, at least one supported change, and a severity threshold.",
      "invalid-input", false);
  }
}

function safeFailure(cause: unknown): never {
  if (!(cause instanceof APIError)) throw cause;
  const message = cause.code === "forbidden"
    ? "Only a current workspace administrator can change notification policies."
    : cause.code === "conflict"
      ? "The notification policy conflicts with current server state. Refresh before saving another revision."
      : cause.code === "not-found"
        ? "The notification policy or selected connection is unavailable in this workspace."
        : cause.code === "network" || cause.code === "unavailable"
          ? "Notification-policy state is unavailable. Retry the authorized read before changing anything."
          : cause.code === "invalid-response"
            ? cause.message
            : "The notification-policy request was rejected. Check the bounded fields and current workspace authority.";
  throw new APIError(message, cause.code, false, null, cause.httpStatus);
}

export const notificationPolicyApi = {
  policies: (signal: AbortSignal) => {
    const workspace = requestAuthority().workspace;
    return request("/api/v1/integrations/notification-policies",
      (value) => page(value, (item) => policy(item, workspace), null, 32),
      { signal, expectedStatus: 200 }).catch(safeFailure);
  },
  create: (input: NotificationPolicyInput, signal: AbortSignal) => {
    validateInput(input);
    const workspace = requestAuthority().workspace;
    return request("/api/v1/integrations/notification-policies",
      (value) => ({ apiVersion, policy: policy(envelope(value, ["policy"]).policy, workspace) }),
      { method: "POST", body: input, signal, expectedStatus: 201 }).catch(safeFailure);
  },
  update: (original: NotificationPolicy, input: NotificationPolicyPatch, signal: AbortSignal) => {
    validateText(input.rationale, "Rationale", 8192);
    const fields = Object.keys(input).filter((key) => key !== "rationale");
    if (!fields.length) throw new APIError("Change at least one notification-policy field.", "invalid-input", false);
    const workspace = requestAuthority().workspace;
    return request(`/api/v1/integrations/notification-policies/${encodeURIComponent(original.id)}`,
      (value) => {
        const saved = policy(envelope(value, ["policy"]).policy, workspace);
        if (saved.id !== original.id || saved.revision !== original.revision + 1 || saved.epoch <= original.epoch) {
          return invalid("update acknowledgement");
        }
        return { apiVersion, policy: saved };
      }, { method: "PATCH", body: input, signal, expectedStatus: 200 }).catch(safeFailure);
  },
  events: (policyId: string, cursor: string | null, signal: AbortSignal) => {
    if (!identifier.test(policyId) || cursor !== null && !identifier.test(cursor)) {
      throw new APIError("Choose a current notification policy and native continuation.", "invalid-input", false);
    }
    const workspace = requestAuthority().workspace, query = new URLSearchParams({ limit: "100" });
    if (cursor !== null) query.set("cursor", cursor);
    return request(`/api/v1/integrations/notification-policies/${encodeURIComponent(policyId)}/events?${query}`,
      (value) => page(value, (item) => event(item, workspace, policyId), cursor, 100),
      { signal, expectedStatus: 200 }).catch(safeFailure);
  },
};
