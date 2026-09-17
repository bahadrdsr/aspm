import { apiVersion, connectionsPath, pageParameters } from "./slack-ui-data";

export function emptySlackNavigation(
  url: URL, method: string, workspace: string | undefined, known: ReadonlyMap<string, readonly string[]>,
): Record<string, unknown> | null {
  const history = /^\/api\/v1\/findings\/([^/]+)\/deliveries$/.exec(url.pathname);
  if (url.pathname !== connectionsPath && !history) return null;
  if (method !== "GET" || !workspace || !known.has(workspace) ||
    history && !known.get(workspace)!.includes(history[1])) {
    throw new Error("Additive Slack navigation permits only selected-workspace GETs for already-owned synthetic finding IDs.");
  }
  pageParameters(url);
  return { apiVersion, dataOrigin: "synthetic", items: [], total: 0, nextCursor: null };
}
