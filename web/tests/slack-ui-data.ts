import { apiVersion } from "./api-contract";
import { primaryFinding, validExpiry, workItem } from "./finding-actions-data";
import type { ActionFinding } from "./finding-actions-data";

export { apiVersion, workItem };
export type SlackRole = "admin" | "analyst" | "viewer";
export type DeliveryState = "queued" | "dispatching" | "confirmed" | "accepted" | "blocked" | "failed" | "rate-limited" | "uncertain";
export interface SlackConnection {
  id: string; workspaceId: string; profile: "slack-workspace-bot"; name: string; channel: string;
  enabled: boolean; credentialConfigured: boolean; revision: number; createdAt: string; updatedAt: string;
}
export interface SlackDelivery {
  id: string; workspaceId: string; findingId: string; connectionId: string; connectionRevision: number;
  profile: "slack-workspace-bot"; channel: string; requestedBy: string; state: DeliveryState;
  payload: { title: string; body: string; deepLink: string };
  createdAt: string; dispatchStartedAt: string | null; completedAt: string | null;
  receipt: { remoteId: string; remoteUrl: string } | null;
  failure: { code: string; nativeCode: string; httpStatus: number; retryAfterSeconds: number; retryable: false } | null;
}
export interface SlackPage<T> {
  apiVersion: typeof apiVersion; dataOrigin: "synthetic"; items: T[]; total: number; nextCursor: string | null;
}

export const slackAlpha = { id: "12000000000000000000000000000001", name: "Synthetic Slack Alpha workspace" };
export const slackBeta = { id: "12000000000000000000000000000002", name: "Synthetic Slack Beta workspace" };
export const slackUser = { id: "22000000000000000000000000000005", name: "Synthetic notification analyst", email: "slack-ui@synthetic.invalid" };
export const slackCookie = "s".repeat(43);
export const draftToken = "SYNTHETIC-SLACK-TOKEN-NOT-A-LIVE-CREDENTIAL-1";
export const replacementToken = "SYNTHETIC-SLACK-TOKEN-NOT-A-LIVE-CREDENTIAL-2";
export const savedAt = "2026-09-16T18:01:02.123456Z";
export const changedAt = "2026-09-16T18:04:05.654321Z";
export const dispatchedAt = "2026-09-16T18:05:06.123456Z";
export const completedAt = "2026-09-16T18:05:07.654321Z";
export const syntheticPublicOrigin = "https://aspm.synthetic.invalid";
export const connectionsPath = "/api/v1/integrations/connections";
export const workPath = "/api/v1/work";
export const longConnectionName = "Synthetic workspace " + "W".repeat(236);
export const longAssetName = "synthetic-slack-repository-" + "assetcontext".repeat(18);

export function hexID(prefix: string, index: number) {
  if (!/^[a-f0-9]{1,8}$/.test(prefix) || !Number.isSafeInteger(index) || index < 1) throw new Error("Invalid synthetic ID seed.");
  return prefix + index.toString(16).padStart(32 - prefix.length, "0");
}
export function backendID(value: unknown): value is string { return typeof value === "string" && /^[a-f0-9]{32}$/.test(value); }
const goWhitespace = /^[\u0009-\u000d\u0020\u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]*$/u;
const goEdgeWhitespace = /^[\u0009-\u000d\u0020\u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]|[\u0009-\u000d\u0020\u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]$/u;
export function validText(value: unknown, limit: number): value is string {
  return typeof value === "string" && !goWhitespace.test(value) && !value.includes("\0") && Buffer.byteLength(value, "utf8") <= limit;
}
export function validChannel(value: unknown): value is string { return typeof value === "string" && /^[CG][A-Z0-9]{2,127}$/.test(value); }
export function validToken(value: unknown): value is string {
  return typeof value === "string" && value !== "" && Buffer.byteLength(value, "utf8") <= 16384 &&
    !goEdgeWhitespace.test(value) && !/[\u0000-\u001f\u007f-\u009f]/u.test(value);
}
export function pageParameters(url: URL) {
  const raw = url.searchParams.get("limit") ?? "", cursor = url.searchParams.get("cursor") ?? "";
  const limit = raw === "" ? 100 : Number(raw);
  if ([...url.searchParams.keys()].some((key) => !["limit", "cursor"].includes(key)) ||
    ["limit", "cursor"].some((key) => url.searchParams.getAll(key).length > 1) ||
    raw !== "" && !/^[+-]?\d+$/.test(raw) || !Number.isSafeInteger(limit) || limit < 1 || limit > 500 ||
    cursor !== "" && !backendID(cursor)) {
    throw new Error("Use only native limit (default 100, 1..500) and a 32-lowercase-hex cursor.");
  }
  return { limit, cursor };
}
export function paginated<T extends { id: string }>(values: readonly T[], url: URL): SlackPage<T> {
  const { limit, cursor } = pageParameters(url);
  const all = [...values].sort((a, b) => a.id.localeCompare(b.id));
  const remaining = all.filter((item) => item.id > cursor), items = remaining.slice(0, limit);
  return { apiVersion, dataOrigin: "synthetic", items: structuredClone(items), total: all.length,
    nextCursor: remaining.length > limit ? items.at(-1)!.id : null };
}
export function exactKeys(value: object, names: readonly string[]) {
  return Object.keys(value).sort().join(",") === [...names].sort().join(",");
}

export const slackFinding: ActionFinding = {
  ...structuredClone(primaryFinding),
  id: "52000000000000000000000000000001", workspaceId: slackAlpha.id,
  assetId: "32000000000000000000000000000001",
  title: "Synthetic Slack finding review", assetName: "synthetic-slack-alpha",
};
export const betaSlackFinding: ActionFinding = {
  ...structuredClone(slackFinding),
  id: "62000000000000000000000000000001", workspaceId: slackBeta.id,
  assetId: "32000000000000000000000000000002",
  title: "Synthetic Beta notification review", assetName: "synthetic-slack-beta",
  evidence: { text: "Synthetic Beta evidence only.", sourceLabel: "Synthetic Beta source", verificationState: "not-run" },
};
export const findingPath = `/api/v1/findings/${slackFinding.id}`;
export const historyPath = `${findingPath}/deliveries`;
export function connectionPath(id: string) { return `${connectionsPath}/${id}`; }
export function deliveryPath(id: string) { return `/api/v1/integrations/deliveries/${id}`; }

export const alphaConnection: SlackConnection = {
  id: hexID("c1", 1), workspaceId: slackAlpha.id, profile: "slack-workspace-bot",
  name: "Synthetic Alpha on-call", channel: "C123SYNTHETIC", enabled: true, credentialConfigured: true,
  revision: 4, createdAt: savedAt, updatedAt: savedAt,
};
export const disabledConnection: SlackConnection = {
  ...alphaConnection, id: hexID("c1", 2), name: "Synthetic disabled response room",
  channel: "G999SYNTHETIC", enabled: false, revision: 7,
};
export const betaConnection: SlackConnection = {
  ...alphaConnection, id: hexID("c2", 1), workspaceId: slackBeta.id, name: "Synthetic Beta isolated room",
  channel: "CBETAROOM42", revision: 2,
};
export const createInput = {
  profile: "slack-workspace-bot" as const, name: "Synthetic new outbound room",
  channel: "GNEWROOM42", token: draftToken, enabled: false,
};
export const createdConnection: SlackConnection = {
  id: hexID("c3", 1), workspaceId: slackAlpha.id, profile: createInput.profile, name: createInput.name,
  channel: createInput.channel, enabled: createInput.enabled, credentialConfigured: true,
  revision: 1, createdAt: changedAt, updatedAt: changedAt,
};
export function connectionSeries(count: number) {
  if (!Number.isInteger(count) || count < 2 || count > 501) throw new Error("Seed only bounded native pagination.");
  return [alphaConnection, disabledConnection, ...Array.from({ length: count - 2 }, (_, index) => ({
    ...alphaConnection, id: hexID("c1", index + 3), name: `Synthetic extra room ${String(index + 3).padStart(3, "0")}`,
  }))].map((item) => structuredClone(item));
}
export function queuedDelivery(connection = alphaConnection, finding = slackFinding, index = 1): SlackDelivery {
  if (connection.workspaceId !== finding.workspaceId) throw new Error("Synthetic notification must belong to its selected workspace.");
  return {
    id: hexID(connection.workspaceId === slackAlpha.id ? "d1" : "d2", index), workspaceId: finding.workspaceId,
    findingId: finding.id, connectionId: connection.id, connectionRevision: connection.revision,
    profile: connection.profile, channel: connection.channel, requestedBy: slackUser.id, state: "queued",
    payload: { title: finding.title, body: `Severity: ${finding.severity}\nAsset: ${finding.assetName}`,
      deepLink: `${syntheticPublicOrigin}/#/work?finding=${finding.id}` },
    createdAt: savedAt, dispatchStartedAt: null, completedAt: null, receipt: null, failure: null,
  };
}
export function deliveryResult(queued: SlackDelivery, state: Exclude<DeliveryState, "accepted">): SlackDelivery {
  const delivery = structuredClone(queued);
  delivery.state = state;
  delivery.dispatchStartedAt = ["queued", "blocked"].includes(state) ? null : dispatchedAt;
  delivery.completedAt = ["queued", "dispatching"].includes(state) ? null : completedAt;
  delivery.receipt = state === "confirmed" ? { remoteId: `${delivery.channel}:1789581907.654321`, remoteUrl: "" } : null;
  delivery.failure = ["blocked", "failed", "rate-limited", "uncertain"].includes(state) ? {
    code: state === "blocked" ? "connection-disabled" : state === "failed" ? "auth" : state === "rate-limited" ? "rate_limited" : "uncertain",
    nativeCode: state === "failed" ? "invalid_auth" : state === "rate-limited" ? "ratelimited" : "",
    httpStatus: state === "failed" ? 200 : state === "rate-limited" ? 429 : 0,
    retryAfterSeconds: state === "rate-limited" ? 7 : 0, retryable: false,
  } : null;
  return delivery;
}
export function sameIntent(left: SlackDelivery, right: SlackDelivery) {
  const immutable = (value: SlackDelivery) => ({
    id: value.id, workspaceId: value.workspaceId, findingId: value.findingId, connectionId: value.connectionId,
    connectionRevision: value.connectionRevision, profile: value.profile, channel: value.channel,
    requestedBy: value.requestedBy, payload: value.payload, createdAt: value.createdAt,
  });
  return JSON.stringify(immutable(left)) === JSON.stringify(immutable(right));
}
export function validateConnection(value: SlackConnection) {
  if (!exactKeys(value, ["id", "workspaceId", "profile", "name", "channel", "enabled", "credentialConfigured", "revision", "createdAt", "updatedAt"]) ||
    !backendID(value.id) || !backendID(value.workspaceId) || value.profile !== "slack-workspace-bot" ||
    !validText(value.name, 256) || !validChannel(value.channel) || typeof value.enabled !== "boolean" ||
    typeof value.credentialConfigured !== "boolean" || !Number.isSafeInteger(value.revision) || value.revision < 1 ||
    !validExpiry(value.createdAt) || !validExpiry(value.updatedAt)) throw new Error("Invalid synthetic nonsecret connection DTO.");
}
export function validateDelivery(value: SlackDelivery) {
  if (!exactKeys(value, ["id", "workspaceId", "findingId", "connectionId", "connectionRevision", "profile", "channel", "requestedBy",
    "state", "payload", "createdAt", "dispatchStartedAt", "completedAt", "receipt", "failure"]) ||
    ![value.id, value.workspaceId, value.findingId, value.connectionId, value.requestedBy].every(backendID) ||
    value.profile !== "slack-workspace-bot" || !validChannel(value.channel) ||
    !Number.isSafeInteger(value.connectionRevision) || value.connectionRevision < 1 ||
    !["queued", "dispatching", "confirmed", "accepted", "blocked", "failed", "rate-limited", "uncertain"].includes(value.state) ||
    !exactKeys(value.payload, ["title", "body", "deepLink"]) ||
    ![value.createdAt, value.dispatchStartedAt, value.completedAt].every((stamp) => stamp === null || validExpiry(stamp)) ||
    !value.payload.deepLink.startsWith(`${syntheticPublicOrigin}/#/work?finding=`)) throw new Error("Invalid synthetic immutable delivery DTO.");
  if (value.receipt && (!exactKeys(value.receipt, ["remoteId", "remoteUrl"]) ||
    typeof value.receipt.remoteId !== "string" || typeof value.receipt.remoteUrl !== "string")) throw new Error("Invalid native receipt fields.");
  if (value.failure && (!exactKeys(value.failure, ["code", "nativeCode", "httpStatus", "retryAfterSeconds", "retryable"]) ||
    value.failure.retryable !== false || !Number.isSafeInteger(value.failure.httpStatus) || value.failure.httpStatus < 0 ||
    !Number.isSafeInteger(value.failure.retryAfterSeconds) || value.failure.retryAfterSeconds < 0)) throw new Error("Invalid terminal failure data.");
  if (value.state === "queued" && [value.dispatchStartedAt, value.completedAt, value.receipt, value.failure].some((item) => item !== null)) {
    throw new Error("A queued response is not a native receipt.");
  }
}
