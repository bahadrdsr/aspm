import { apiVersion } from "./api-contract";
import { backendID, validExpiry } from "./finding-actions-data";
import { digest, manualFile, metadata, utc, validText } from "./report-formats-data";
import type { IntakeBody, IntakeMetadata, IntakeReceipt } from "./report-formats-data";

export { apiVersion, backendID, validExpiry, digest, manualFile, utc, validText };
export type { IntakeBody, IntakeMetadata, IntakeReceipt };
export type PagingRole = "admin" | "analyst" | "viewer";
export interface PagingAsset {
  id: string; workspaceId: string; name: string; kind: string; environment: string;
  criticality: "low" | "medium" | "high" | "critical"; tags: string[]; ownerId: string | null;
}
export interface AssetPage {
  apiVersion: typeof apiVersion; items: PagingAsset[]; total: number; nextCursor: string | null;
}
export const pagingAlpha = { id: "14000000000000000000000000000001", name: "Synthetic paging Alpha" };
export const pagingBeta = { id: "14000000000000000000000000000002", name: "Synthetic paging Beta" };
export const pagingUser = { id: "24000000000000000000000000000001", name: "Synthetic asset pager", email: "asset-pages@synthetic.invalid" };
export const pagingCookie = "p".repeat(43);
export const assetsPath = "/api/v1/assets";
export const importsPath = "/api/v1/imports";
export const alphaCount = 1103;
export const betaCount = 507;
export const createdName = "Synthetic service-acknowledged created asset";
export const editedName = "Synthetic service-acknowledged edited asset";
export const createFields = {
  name: createdName, kind: "repository", environment: "test", criticality: "high" as const,
  tags: ["synthetic-created"], ownerId: null,
};
export function assetID(workspace: string, index: number) {
  if (![pagingAlpha.id, pagingBeta.id].includes(workspace) || !Number.isInteger(index) || index < 1 || index > 1600) {
    throw new Error("Only bounded known synthetic asset IDs may be generated.");
  }
  return (workspace === pagingAlpha.id ? "41" : "42") + (index * 16).toString(16).padStart(30, "0");
}
export const createdID = "41" + (8 * 16 + 8).toString(16).padStart(30, "0");
export function assetAt(workspace: string, index: number): PagingAsset {
  return {
    id: assetID(workspace, index), workspaceId: workspace,
    name: `Synthetic ${workspace === pagingAlpha.id ? "Alpha" : "Beta"} asset ${String(index).padStart(4, "0")}`,
    kind: "repository", environment: "test", criticality: "medium", tags: ["synthetic"], ownerId: null,
  };
}
export const firstAlpha = assetAt(pagingAlpha.id, 1);
export function assetSeries(workspace: string, count: number) {
  if (!Number.isInteger(count) || count < 0 || count > 1600) throw new Error("Synthetic inventory must remain bounded.");
  return Array.from({ length: count }, (_, index) => assetAt(workspace, index + 1));
}
export function pageParameters(url: URL) {
  const raw = url.searchParams.get("limit") ?? "", cursor = url.searchParams.get("cursor") ?? "";
  const limit = raw === "" ? 100 : Number(raw);
  if ([...url.searchParams.keys()].some((key) => !["limit", "cursor"].includes(key)) ||
    ["limit", "cursor"].some((key) => url.searchParams.getAll(key).length > 1) ||
    raw !== "" && !/^[+-]?\d+$/.test(raw) || !Number.isInteger(limit) || limit < 1 || limit > 500 ||
    cursor !== "" && !backendID(cursor)) {
    throw new Error("Assets use only native limit (default 100, 1..500) and a 32-lowercase-hex cursor, not page/offset/search filters.");
  }
  return { limit, cursor };
}
export function assetPage(values: Iterable<PagingAsset>, workspace: string, url: URL): AssetPage {
  const { limit, cursor } = pageParameters(url);
  const all = [...values].filter((asset) => asset.workspaceId === workspace).sort((a, b) => a.id.localeCompare(b.id));
  const remaining = all.filter((asset) => asset.id > cursor), items = remaining.slice(0, limit);
  return { apiVersion, items: structuredClone(items), total: all.length, nextCursor: remaining.length > limit ? items.at(-1)!.id : null };
}
export function validAsset(asset: PagingAsset) {
  return backendID(asset.id) && backendID(asset.workspaceId) && validText(asset.name, 256) && validText(asset.kind, 64) &&
    typeof asset.environment === "string" && Buffer.byteLength(asset.environment) <= 128 && !asset.environment.includes("\0") &&
    ["low", "medium", "high", "critical"].includes(asset.criticality) && Array.isArray(asset.tags) && asset.tags.length <= 64 &&
    asset.tags.every((tag) => validText(tag, 128)) && (asset.ownerId === null || asset.ownerId === pagingUser.id);
}
export function importMetadata(assetId: string): IntakeMetadata {
  return metadata("paged-selection", {
    assetId, sourceId: "synthetic-paged-intake", scanId: "synthetic-selected-later-page",
    scope: { id: "synthetic-paged-scope", revision: "revision-after-format-edit", branch: "refs/heads/paged-selection" },
    sourceScanAt: null, sourceStatus: "succeeded", scanKind: "delta", completeness: "partial",
  });
}
