import { apiVersion, pageParameters, sourcesPath } from "./source-ui-data";

export function emptySourceNavigation(
  url: URL, method: string, workspace: string | undefined, knownWorkspaces: readonly string[],
): Record<string, unknown> | null {
  if (url.pathname !== sourcesPath) return null;
  if (method !== "GET" || !workspace || !knownWorkspaces.includes(workspace)) {
    throw new Error("Additive Sources navigation permits only GET for an already known selected workspace.");
  }
  pageParameters(url);
  return { apiVersion, items: [], total: 0, nextCursor: null };
}
