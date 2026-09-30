import { pageParameters } from "./asset-pagination-data";
import {
  apiVersion, currentOwner, findingAt, later, workItem,
} from "./work-pagination-data";
import type { ActionFinding, ActionWorkResponse } from "./work-pagination-data";

export {
  alpha, alphaTotal, apiVersion, beta, betaTotal, cookie, currentOwner, cursor200,
  findingAt, first, later, password, pathFor, user, workItem, workPath,
} from "./work-pagination-data";
export type { ActionFinding } from "./work-pagination-data";
export const queries = { asset: "later", initial: "initial", title: later.title, owner: currentOwner.name };
export const searchFirst = findingAt(101);
export const searchTotal = 107;
export const maxByteQuery = "\u00e9".repeat(256);

export function validSearchQuery(q: string) {
  return q !== "" && q === q.trim() && !q.includes("\0") && Buffer.byteLength(q, "utf8") <= 512;
}

export function searchParameters(url: URL) {
  const q = url.searchParams.get("q") ?? "";
  if (url.searchParams.getAll("q").length !== 1 || !validSearchQuery(q)) {
    throw new Error("Search requires exactly one trimmed, nonempty, NUL-free q of at most 512 UTF-8 bytes.");
  }
  const native = new URL(url);
  native.searchParams.delete("q");
  const parameters = pageParameters(native);
  if (parameters.cursor === "" ? native.search !== "" :
    parameters.limit !== 100 || native.searchParams.get("limit") !== "100" || native.searchParams.size !== 2) {
    throw new Error("Search starts with q only and continues with exactly q, limit=100 and the native cursor.");
  }
  return { q, ...parameters };
}

export function searchPage(findings: Iterable<ActionFinding>, workspace: string, url: URL): ActionWorkResponse {
  const { q, cursor, limit } = searchParameters(url), needle = q.toLowerCase();
  const matching = [...findings].filter((finding) => finding.workspaceId === workspace &&
    [finding.title, finding.assetName, finding.ownerName ?? ""].some((value) => value.toLowerCase().includes(needle)))
    .sort((a, b) => a.id.localeCompare(b.id));
  const remaining = matching.filter((finding) => finding.id > cursor);
  const items = remaining.slice(0, limit).map(workItem);
  return { apiVersion, dataOrigin: "synthetic", items, total: matching.length,
    nextCursor: remaining.length > limit ? items.at(-1)!.id : null };
}
