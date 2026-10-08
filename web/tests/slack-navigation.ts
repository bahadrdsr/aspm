import { apiVersion, connectionsPath, pageParameters } from "./slack-ui-data";

export const notificationPoliciesPath = "/api/v1/integrations/notification-policies";

export function emptySlackNavigation(
  url: URL, method: string, workspace: string | undefined, known: ReadonlyMap<string, readonly string[]>,
): Record<string, unknown> | null {
  const history = /^\/api\/v1\/findings\/([^/]+)\/deliveries$/.exec(url.pathname);
  const policies = url.pathname === notificationPoliciesPath;
  if (url.pathname !== connectionsPath && !history && !policies) return null;
  if (method !== "GET" || !workspace || !known.has(workspace) ||
    history && !known.get(workspace)!.includes(history[1])) {
    throw new Error("Additive integration navigation permits only selected-workspace GETs for owned synthetic metadata.");
  }
  if (policies) {
    if (url.search !== "") throw new Error("Notification-policy navigation has no implicit paging or filter query.");
    return { apiVersion, items: [], total: 0, nextCursor: null };
  }
  pageParameters(url);
  return { apiVersion, dataOrigin: "synthetic", items: [], total: 0, nextCursor: null };
}
