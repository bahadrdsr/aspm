import { createHash } from "node:crypto";
import { apiVersion } from "./api-contract";
import { backendID, pageParameters, validText, validToken } from "./slack-ui-data";

export { apiVersion, backendID, pageParameters, validText, validToken };
export type SourceRole = "admin" | "analyst" | "viewer";
export type CollectionState = "queued" | "collecting" | "succeeded" | "partial" | "blocked" | "failed";
export interface UISource {
  id: string; workspaceId: string; profile: "github-cloud-app"; name: string; repository: string;
  enabled: boolean; credentialConfigured: boolean; revision: number; createdAt: string; updatedAt: string;
}
export interface NativeFailure {
  code: string; nativeCode: string; httpStatus: number; retryAfterSeconds: number; retryable: false;
}
export interface UICollection {
  id: string; workspaceId: string; sourceId: string; profile: "github-cloud-app"; connectionRevision: number;
  repository: string; requestedBy: string; state: CollectionState; complete: boolean;
  assetId: string | null; repositoryId: string | null; recordCount: number; gaps: string[];
  createdAt: string; collectedAt: string | null; completedAt: string | null; failure: NativeFailure | null;
}
export interface UIRecord {
  id: string; collectionId: string; ordinal: number; kind: "repository" | "finding"; externalId: string;
  parentId: string; nativeRunId: string; state: string; severity: string; location: string; rawURL: string;
  sourceScanAt: null; sourceUpdatedAt: string | null; evidence: { sha256: string; sizeBytes: number };
}
export interface SourcePage<T> { apiVersion: typeof apiVersion; items: T[]; total: number; nextCursor: string | null }
export const sourcesPath = "/api/v1/sources";
export const sourceAlpha = { id: "17000000000000000000000000000001", name: "Synthetic source Alpha" };
export const sourceBeta = { id: "17000000000000000000000000000002", name: "Synthetic source Beta" };
export const sourceUser = { id: "27000000000000000000000000000001", name: "Synthetic source reviewer", email: "source-ui@synthetic.invalid" };
export const sourceCookie = "u".repeat(43);
export const sourceToken = "SYNTHETIC-INSTALLATION-BEARER-NOT-A-LIVE-CREDENTIAL-1";
export const replacementSourceToken = "SYNTHETIC-INSTALLATION-BEARER-NOT-A-LIVE-CREDENTIAL-2";
export const sourceCreatedAt = "2026-09-16T12:13:14.123456Z";
export const sourceUpdatedAt = "2026-09-17T12:13:14.654321Z";
export const collectedAt = "2026-09-17T12:14:15.123456Z";
export const completedAt = "2026-09-17T12:14:16.654321Z";
export const nativeUpdatedAt = "2026-09-16T09:10:11Z";
export const sourceAssetID = "37000000000000000000000000000001";
export const longSourceName = "Synthetic selected source " + "W".repeat(215);
export function nativeID(prefix: string, number: number) {
  if (!/^[a-f0-9]{1,6}$/.test(prefix) || !Number.isInteger(number) || number < 1) throw new Error("Invalid synthetic native ID seed.");
  return prefix + number.toString(16).padStart(32 - prefix.length, "0");
}
export function sourcePath(id: string) { return `${sourcesPath}/${id}`; }
export function collectionsPath(id: string) { return `${sourcePath(id)}/collections`; }
export function collectionPath(id: string) { return `${sourcesPath}/collections/${id}`; }
export function recordsPath(id: string) { return `${collectionPath(id)}/records`; }
export function evidencePath(collection: string, record: string) { return `${recordsPath(collection)}/${record}/evidence`; }
export function validRepository(value: unknown): value is string {
  return typeof value === "string" && Buffer.byteLength(value) <= 256 &&
    /^[A-Za-z0-9_][A-Za-z0-9_.-]{0,254}\/[A-Za-z0-9_][A-Za-z0-9_.-]{0,254}$/.test(value);
}
export const githubSource: UISource = {
  id: nativeID("c1", 1), workspaceId: sourceAlpha.id, profile: "github-cloud-app",
  name: "Synthetic selected GitHub repository", repository: "synthetic-owner/selected-repo",
  enabled: true, credentialConfigured: true, revision: 4, createdAt: sourceCreatedAt, updatedAt: sourceCreatedAt,
};
export const disabledSource: UISource = {
  ...githubSource, id: nativeID("c1", 2), name: "Synthetic disabled source", repository: "synthetic-owner/disabled-repo",
  enabled: false, revision: 7,
};
export const betaSource: UISource = {
  ...githubSource, id: nativeID("c2", 1), workspaceId: sourceBeta.id,
  name: "Synthetic Beta source only", repository: "synthetic-beta/isolated-repo", revision: 2,
};
export const sourceInput = {
  profile: "github-cloud-app" as const, name: "Synthetic newly configured source",
  repository: "synthetic-owner/new-selected-repo", token: sourceToken, enabled: false,
};
export function sourceSeries(count: number) {
  if (!Number.isInteger(count) || count < 2 || count > 102) throw new Error("Only a bounded 102-source cursor fixture is authorized.");
  return [githubSource, disabledSource, ...Array.from({ length: count - 2 }, (_, index) => ({
    ...githubSource, id: nativeID("c1", index + 3), name: `Synthetic source ${String(index + 3).padStart(3, "0")}`,
    repository: `synthetic-owner/selected-${index + 3}`,
  }))].map((value) => structuredClone(value));
}
export function pageOf<T extends { id: string }>(values: readonly T[], url: URL): SourcePage<T> {
  const { limit, cursor } = pageParameters(url), all = [...values].sort((a, b) => a.id.localeCompare(b.id));
  const remaining = all.filter((value) => value.id > cursor), items = remaining.slice(0, limit);
  return { apiVersion, items: structuredClone(items), total: all.length, nextCursor: remaining.length > limit ? items.at(-1)!.id : null };
}
export function queuedCollection(source = githubSource, number = 1): UICollection {
  return {
    id: nativeID(source.workspaceId === sourceAlpha.id ? "d1" : "d2", number),
    workspaceId: source.workspaceId, sourceId: source.id, profile: source.profile,
    connectionRevision: source.revision, repository: source.repository, requestedBy: sourceUser.id,
    state: "queued", complete: false, assetId: null, repositoryId: null, recordCount: 0, gaps: [],
    createdAt: sourceCreatedAt, collectedAt: null, completedAt: null, failure: null,
  };
}
export function collectionResult(value: UICollection, state: CollectionState, records = 3): UICollection {
  const finalized = state === "succeeded" || state === "partial";
  const failure: NativeFailure | null = state === "partial"
    ? { code: "rate_limited", nativeCode: "", httpStatus: 429, retryAfterSeconds: 7, retryable: false }
    : state === "failed" ? { code: "auth", nativeCode: "", httpStatus: 403, retryAfterSeconds: 0, retryable: false }
      : state === "blocked" ? { code: "connection-disabled", nativeCode: "", httpStatus: 0, retryAfterSeconds: 0, retryable: false } : null;
  return {
    ...structuredClone(value), state, complete: state === "succeeded",
    assetId: finalized ? sourceAssetID : null, repositoryId: finalized ? "12345" : null, recordCount: finalized ? records : 0,
    gaps: state === "partial" ? ["alerts:rate_limited"] : [],
    collectedAt: finalized ? collectedAt : null, completedAt: ["queued", "collecting"].includes(state) ? null : completedAt, failure,
  };
}
export function rawDigest(bytes: Buffer) { return `sha256:${createHash("sha256").update(bytes).digest("hex")}`; }
export function recordsFor(collection: UICollection, count = collection.recordCount) {
  if (!Number.isInteger(count) || count < 0 || count > 102) throw new Error("Record fixtures are bounded to 102, not a worker or full directory.");
  const seed = parseInt(collection.id.slice(-6), 16) * 1000;
  return Array.from({ length: count }, (_, ordinal) => {
    const repository = ordinal === 0, externalId = repository ? "12345" : String(17000 + ordinal);
    const raw = repository
      ? Buffer.from('{\r\n  "id" : 12345,\r\n  "full_name" : "' + collection.repository +
        '",\r\n  "description" : "Synthetic café repository <script>literal-only</script> - 安全",\r\n  "html_url" : "https://source.synthetic.invalid/not-a-fetch"\r\n}\r\n\r\n')
      : Buffer.from('{\r\n "number" : ' + externalId + ', "state" : "dismissed",\r\n "updated_at" : "' + nativeUpdatedAt +
        '", "rule" : {"security_severity_level":"high"},\r\n "most_recent_instance" : {"location":{"path":"src/synthetic.ts","start_line":7},' +
        '"analysis_key":"synthetic-analysis", "ref":"refs/heads/main"},\r\n "tool" : {"name":"Synthetic source fixture"},\r\n "literal" : "<b>not markup</b> café 安全"\r\n}');
    const row: UIRecord = {
      id: nativeID(collection.workspaceId === sourceAlpha.id ? "e1" : "e2", seed + (ordinal === 0 ? 2 : ordinal === 1 ? 1 : ordinal + 1)),
      collectionId: collection.id, ordinal, kind: repository ? "repository" : "finding",
      externalId, parentId: repository ? "" : "12345", nativeRunId: "",
      state: repository ? "" : "dismissed", severity: repository ? "" : "high",
      location: repository ? "" : "src/synthetic.ts:7",
      rawURL: `https://github-collection.synthetic.invalid/repos/${collection.repository}${repository ? "" : "/code-scanning/alerts?page=1&per_page=100"}`,
      sourceScanAt: null, sourceUpdatedAt: repository ? null : nativeUpdatedAt,
      evidence: { sha256: rawDigest(raw), sizeBytes: raw.length },
    };
    return { row, raw };
  });
}
