import { alpha, beta, queries } from "./work-search-data";
import { apiVersion } from "./work-search-data";

export { apiVersion };
export const viewsPath = "/api/v1/work/views";
export const viewPath = (id: string) => `${viewsPath}/${id}`;
export const viewSorts = ["source-order", "severity", "title"] as const;
export interface SavedView {
  id: string; name: string; query: string; sort: typeof viewSorts[number];
  revision: string; createdAt: string; updatedAt: string;
}
export function viewAt(index: number, workspace = alpha.id): SavedView {
  if (!Number.isInteger(index) || index < 1 || index > 110 || ![alpha.id, beta.id].includes(workspace)) {
    throw new Error("Use bounded native synthetic personal-view metadata.");
  }
  return {
    id: (workspace === alpha.id ? "71" : "81") + index.toString(16).padStart(30, "0"),
    name: `Personal ${workspace === alpha.id ? "Alpha" : "Beta"} view ${String(index).padStart(4, "0")}`,
    query: index === 2 ? "" : queries.initial, sort: index === 2 ? "severity" : "source-order",
    revision: "1", createdAt: "2026-09-30T12:00:00Z", updatedAt: "2026-09-30T12:00:00Z",
  };
}
export const primaryView = viewAt(1);
export const emptyView = viewAt(2);
export const currentView: SavedView = {
  ...primaryView, query: queries.asset, sort: "title", revision: "2", updatedAt: "2026-09-30T12:01:00Z",
};
export const viewCursor100 = viewAt(100).id;
export const firstViews = Array.from({ length: 100 }, (_, i) => viewAt(i + 1));
export const tailViews = [viewAt(101), viewAt(102), viewAt(103)];
export const viewDetail = (view: SavedView) => ({ apiVersion, view: structuredClone(view) });
export const viewPage = (items: SavedView[], total = items.length, nextCursor: string | null = null) =>
  ({ apiVersion, items: structuredClone(items), total, nextCursor });
export const maxViewName = "\u00e9".repeat(128);
export const maxViewQuery = "\u00e9".repeat(256);
