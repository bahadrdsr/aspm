import {
  actionAlpha, actionBeta, actionCookie, actionUser, apiVersion, backendID, currentOwner, primaryFinding, workItem,
} from "./finding-actions-data";
import type { ActionFinding, ActionRole, ActionWorkResponse } from "./finding-actions-data";
import { pageParameters } from "./asset-pagination-data";
import { password } from "./application-fixture";

export { actionAlpha as alpha, actionBeta as beta, actionCookie as cookie, actionUser as user,
  apiVersion, backendID, currentOwner, password, workItem };
export type { ActionFinding, ActionRole, ActionWorkResponse };
export const workPath = "/api/v1/work";
export const alphaTotal = 207;
export const betaTotal = 103;
export const pathFor = (id: string) => `/api/v1/findings/${id}`;
export function findingAt(index: number, workspace = actionAlpha.id): ActionFinding {
  if (!Number.isInteger(index) || index < 1 || index > 207 || ![actionAlpha.id, actionBeta.id].includes(workspace)) {
    throw new Error("Only bounded native synthetic Work records are permitted.");
  }
  const alpha = workspace === actionAlpha.id, label = alpha ? "Alpha" : "Beta";
  return {
    ...structuredClone(primaryFinding),
    id: (alpha ? "51" : "61") + index.toString(16).padStart(30, "0"),
    workspaceId: workspace,
    title: `Work ${label} finding ${String(208 - index).padStart(4, "0")}`,
    assetName: `work-${label.toLowerCase()}-${index <= 100 ? "initial" : "later"}-repository-${String(index).padStart(4, "0")}`,
    severity: (["high", "medium", "low", "info", "critical"] as const)[(index - 1) % 5],
    ownerId: index === 1 ? currentOwner.id : null,
    ownerName: index === 1 ? currentOwner.name : null,
    notes: [], observations: [],
    description: `Synthetic ${label} source context ${index}, never a verified decision.`,
    evidence: { ...primaryFinding.evidence, text: `SYNTHETIC ${label.toUpperCase()} ORIGINAL WORK EVIDENCE ${index}` },
  };
}
export const first = findingAt(1);
export const cursor100 = findingAt(100).id;
export const cursor200 = findingAt(200).id;
export const betaCursor = findingAt(100, actionBeta.id).id;
export const later = findingAt(207);
export function initialFindings() {
  return [...Array.from({ length: alphaTotal }, (_, i) => findingAt(i + 1)),
    ...Array.from({ length: betaTotal }, (_, i) => findingAt(i + 1, actionBeta.id))];
}
export function workParameters(url: URL) {
  const parameters = pageParameters(url);
  if (parameters.cursor === "") {
    if (url.search !== "") throw new Error("Work starts/refreshes with the published bare GET.");
  } else if (parameters.limit !== 100 || url.searchParams.get("limit") !== "100" || url.searchParams.size !== 2) {
    throw new Error("Only an explicit native cursor and limit=100 may continue Work.");
  }
  return parameters;
}
export function workPage(findings: Iterable<ActionFinding>, workspace: string, url: URL): ActionWorkResponse {
  const { cursor, limit } = workParameters(url);
  const all = [...findings].filter((f) => f.workspaceId === workspace).sort((a, b) => a.id.localeCompare(b.id));
  const remaining = all.filter((f) => f.id > cursor), items = remaining.slice(0, limit).map(workItem);
  return { apiVersion, dataOrigin: "synthetic", items, total: all.length,
    nextCursor: remaining.length > limit ? items.at(-1)!.id : null };
}
