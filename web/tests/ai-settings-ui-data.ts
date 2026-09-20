import { isIP } from "node:net";

export const apiVersion = "aspm/v1alpha1";
export const profilesPath = "/api/v1/ai/profiles";
export const policyPath = "/api/v1/ai/policy";
export const grantsPath = "/api/v1/ai/grants";
export const families = ["openai", "azure-foundry", "anthropic", "local"] as const;
export const modes = ["disabled", "local-only", "approved-hosted"] as const;
export type Family = typeof families[number];
export type Mode = typeof modes[number];
export type Role = "admin" | "analyst" | "viewer";
export interface AIProfile {
  id: string; workspaceId: string; name: string; family: Family; endpoint: string; model: string; deployment: string;
  enabled: boolean; structuredOutput: boolean; credentialConfigured: boolean; revision: string; createdAt: string; updatedAt: string;
}
export interface AIPolicy {
  workspaceId: string; mode: Mode; revision: string; updatedAt: string | null; updatedBy: string | null;
}
export interface AIGrant {
  id: string; workspaceId: string; profileId: string; profileRevision: string; policyRevision: string;
  destination: string; task: "finding-validity"; dataClass: "finding-evidence"; expiresAt: string; createdAt: string;
  grantedBy: string; revokedAt: string | null; revokedBy: string | null;
}
export interface AIPage<T> { apiVersion: typeof apiVersion; items: T[]; total: number; nextCursor: string | null }
export interface ProfileInput {
  name: string; family: Family; endpoint: string; model: string; deployment: string;
  enabled: boolean; structuredOutput: boolean; apiKey?: string | null;
}
export const aiAlpha = { id: "18000000000000000000000000000001", name: "Synthetic AI Alpha" };
export const aiBeta = { id: "18000000000000000000000000000002", name: "Synthetic AI Beta" };
export const aiGamma = { id: "18000000000000000000000000000003", name: "Synthetic AI Gamma" };
export const aiUser = { id: "28000000000000000000000000000001", name: "Synthetic AI admin", email: "ai-ui@synthetic.invalid" };
export const aiCookie = "a".repeat(43);
export const aiPassword = "SYNTHETIC-AI-LOGIN-NOT-A-REAL-PASSWORD-1";
export const draftKey = "SYNTHETIC-AI-KEY-NOT-A-LIVE-CREDENTIAL-1";
export const replacementKey = "SYNTHETIC-AI-KEY-NOT-A-LIVE-CREDENTIAL-2";
export const savedAt = "2026-09-16T12:13:14.123456Z";
export const changedAt = "2026-09-18T07:15:16.654321Z";
export const revokedAt = "2026-09-18T07:16:17.654321Z";
export const profileFields = ["name", "family", "endpoint", "model", "deployment", "enabled", "structuredOutput", "apiKey"];
export const grantFields = ["profileId", "profileRevision", "policyRevision", "destination", "task", "dataClass", "expiresAt"];
export function profilePath(id: string) { return `${profilesPath}/${id}`; }
export function grantPath(id: string) { return `${grantsPath}/${id}`; }
export function revokePath(id: string) { return `${grantPath(id)}/revoke`; }
export function nativeID(prefix: string, index: number) {
  if (!/^[a-f0-9]{1,4}$/.test(prefix) || !Number.isSafeInteger(index) || index < 1) throw new Error("Invalid bounded synthetic ID.");
  return prefix + index.toString(16).padStart(32 - prefix.length, "0");
}
export function backendID(value: unknown): value is string { return typeof value === "string" && /^[a-f0-9]{32}$/.test(value); }
export function exactKeys(value: object, keys: readonly string[]) { return Object.keys(value).sort().join(",") === [...keys].sort().join(","); }
export function text(value: unknown, max: number): value is string {
  return typeof value === "string" && value.trim() !== "" && !value.includes("\0") && Buffer.byteLength(value) <= max;
}
export function timestamp(value: unknown): value is string {
  return typeof value === "string" && /^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?(?:Z|[+-]\d\d:\d\d)$/.test(value) && Number.isFinite(Date.parse(value));
}
export function expiry(minutes = 60) {
  const date = new Date(Date.now() + minutes * 60_000);
  date.setUTCSeconds(0, 0);
  return date.toISOString();
}
function privateLiteral(host: string) {
  const bare = host.replace(/^\[|\]$/g, "");
  if (isIP(bare) === 4) {
    const [a, b] = bare.split(".").map(Number);
    return a === 10 || a === 127 || a === 172 && b >= 16 && b <= 31 || a === 192 && b === 168;
  }
  if (isIP(bare) !== 6) return false;
  const canonical = new URL(`http://[${bare}]`).hostname.slice(1, -1);
  if (canonical === "::1") return true;
  const first = Number.parseInt(canonical.split(":")[0], 16);
  if (first >= 0xfc00 && first <= 0xfdff) return true;
  const mapped = /^::ffff:([a-f0-9]+):([a-f0-9]+)$/.exec(canonical);
  if (!mapped) return false;
  const a = Number.parseInt(mapped[1], 16), b = Number.parseInt(mapped[2], 16);
  return privateLiteral(`${a >> 8}.${a & 255}.${b >> 8}.${b & 255}`);
}
export function validEndpoint(value: unknown, family: Family): value is string {
  if (!text(value, 16384) || value !== value.trim() || /[\p{Cc}\\?#]/u.test(value)) return false;
  const parts = /^(https?):\/\/([^/]+)(\/.*)?$/.exec(value);
  if (!parts || /[@\s]/u.test(parts[2])) return false;
  const authority = /^(\[[^\]]+\]|[^:]+)(?::([0-9]+))?$/.exec(parts[2]);
  if (!authority || authority[2] !== undefined && (+authority[2] < 1 || +authority[2] > 65535)) return false;
  try {
    const decoded = decodeURIComponent(parts[3] ?? "");
    if (/[\p{Cc}\\]/u.test(decoded) || decoded.split("/").some((part) => part === "." || part === "..") ||
      /%2f|%5c/i.test(parts[3] ?? "")) return false;
    const url = new URL(value);
    if (!url.hostname || url.username || url.password || url.search || url.hash) return false;
    return url.protocol === "https:" || family === "local" && url.protocol === "http:" && privateLiteral(authority[1]);
  } catch { return false; }
}
export function profileBodyValid(body: Record<string, unknown>, prior?: AIProfile) {
  if (Object.keys(body).some((key) => !profileFields.includes(key)) ||
    !prior && !profileFields.filter((key) => key !== "apiKey").every((key) => key in body)) return false;
  const value = { ...prior, ...body };
  if (!families.includes(value.family as Family) || !text(value.name, 256) || !text(value.model, 256) ||
    typeof value.deployment !== "string" || Buffer.byteLength(value.deployment) > 256 || value.deployment.includes("\0") ||
    !validEndpoint(value.endpoint, value.family as Family) || typeof value.enabled !== "boolean" ||
    typeof value.structuredOutput !== "boolean") return false;
  if (value.family === "azure-foundry" ? !text(value.deployment, 256) : value.deployment !== "") return false;
  if ("apiKey" in body && body.apiKey !== null && (!text(body.apiKey, 16384) || /\p{Cc}/u.test(body.apiKey))) return false;
  const credential = "apiKey" in body ? body.apiKey !== null : prior?.credentialConfigured === true;
  return value.family === "local" || credential;
}
export function pageParameters(url: URL) {
  const raw = url.searchParams.get("limit"), cursor = url.searchParams.get("cursor") ?? "";
  const limit = raw === null || raw === "" ? 100 : Number(raw);
  if ([...url.searchParams.keys()].some((key) => !["limit", "cursor"].includes(key)) ||
    ["limit", "cursor"].some((key) => url.searchParams.getAll(key).length > 1) ||
    raw !== null && raw !== "" && !/^[+-]?\d+$/.test(raw) ||
    !Number.isInteger(limit) || limit < 1 || limit > 500 || cursor !== "" && !backendID(cursor)) {
    throw new Error("Only native limit (default 100, 1..500) and lowercase-hex cursor are permitted.");
  }
  return { limit, cursor };
}
export function pageOf<T extends { id: string }>(values: readonly T[], url: URL): AIPage<T> {
  const { limit, cursor } = pageParameters(url), all = [...values].sort((a, b) => a.id.localeCompare(b.id));
  const remaining = all.filter((value) => value.id > cursor), items = remaining.slice(0, limit);
  return { apiVersion, items: structuredClone(items), total: all.length, nextCursor: remaining.length > limit ? items.at(-1)!.id : null };
}
export function defaultPolicy(workspaceId = aiAlpha.id): AIPolicy {
  return { workspaceId, mode: "disabled", revision: "0", updatedAt: null, updatedBy: null };
}
export function configuredPolicy(mode: Mode = "approved-hosted", workspaceId = aiAlpha.id): AIPolicy {
  return { workspaceId, mode, revision: "policy-Z/opaque.9", updatedAt: savedAt, updatedBy: aiUser.id };
}
export const hostedProfile: AIProfile = {
  id: nativeID("c8", 1), workspaceId: aiAlpha.id, name: "Synthetic hosted profile", family: "openai",
  endpoint: "https://127.0.0.1:9443/selected-base/", model: "operator-selected-model", deployment: "",
  enabled: true, structuredOutput: true, credentialConfigured: true, revision: "profile-Z/opaque.9", createdAt: savedAt, updatedAt: savedAt,
};
export const localProfile: AIProfile = {
  ...hostedProfile, id: nativeID("c8", 2), name: "Synthetic local profile", family: "local",
  endpoint: "http://10.23.45.67:8899/v1/", model: "operator-local-model", credentialConfigured: false, revision: "local:opaque/Z",
};
export const disabledProfile: AIProfile = { ...hostedProfile, id: nativeID("c8", 3), name: "Synthetic disabled profile", enabled: false };
export const unreviewedProfile: AIProfile = {
  ...hostedProfile, id: nativeID("c8", 4), name: "Synthetic unreviewed profile", structuredOutput: false,
};
export const betaProfile: AIProfile = { ...localProfile, id: nativeID("c9", 1), workspaceId: aiBeta.id, name: "Synthetic Beta private profile" };
export const gammaProfile: AIProfile = { ...localProfile, id: nativeID("ca", 1), workspaceId: aiGamma.id, name: "Synthetic Gamma reader profile" };
export const refreshedProfile: AIProfile = {
  ...hostedProfile, name: "Synthetic refreshed current profile", model: "operator-reviewed-other-model",
  endpoint: "https://127.0.0.1:9443/reviewed-new-base/", revision: "profile-A/opaque.2", updatedAt: changedAt,
};
export const inertProfile: AIProfile = {
  ...localProfile, name: "Synthetic <img src=https://ai.synthetic.invalid/no-fetch> literal " + "W".repeat(145),
  model: "<script>literal-not-code</script> " + "M".repeat(190),
};
export function createInput(family: Family, key: string | undefined = family === "local" ? undefined : draftKey): ProfileInput {
  return {
    name: `Synthetic new ${family} profile`, family,
    endpoint: family === "local" ? "http://127.0.0.1:8899/operator-base/" : "https://provider.synthetic.invalid/operator-base/",
    model: `operator-chosen-${family}-model`, deployment: family === "azure-foundry" ? "operator-foundry-deployment" : "",
    enabled: true, structuredOutput: true, ...(key === undefined ? {} : { apiKey: key }),
  };
}
export function grantFor(profile = hostedProfile, policy = configuredPolicy(), index = 1, expiresAt = expiry()): AIGrant {
  return {
    id: nativeID("d8", index), workspaceId: profile.workspaceId, profileId: profile.id,
    profileRevision: profile.revision, policyRevision: policy.revision, destination: profile.endpoint,
    task: "finding-validity", dataClass: "finding-evidence", expiresAt, createdAt: savedAt,
    grantedBy: aiUser.id, revokedAt: null, revokedBy: null,
  };
}
export function profileSeries() {
  return Array.from({ length: 101 }, (_, index) => ({
    ...hostedProfile, id: nativeID("c8", index + 1), name: `Synthetic profile ${String(index + 1).padStart(3, "0")}`,
  }));
}
export function grantSeries(profile: AIProfile, policy: AIPolicy) {
  return Array.from({ length: 101 }, (_, index) => grantFor(profile, policy, index + 1));
}
