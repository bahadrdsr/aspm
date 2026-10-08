import { hexID } from "./slack-ui-data";
import { alpha, apiVersion } from "./work-search-data";

export { alpha, apiVersion };
export const policiesPath = "/api/v1/integrations/notification-policies";
export const policyPath = (id: string) => `${policiesPath}/${id}`;
export const policyEventsPath = (id: string) => `${policyPath(id)}/events`;
export const stamp = "2026-10-08T03:30:00.123456Z";
export const updatedStamp = "2026-10-08T03:31:00.123456Z";
export type NativeProfile = "slack-workspace-bot" | "teams-workflows-channel" | "jira-cloud-v3" | "generic-webhook-v1";
export type ChangeKind = "new" | "changed" | "reopened";
export type Severity = "critical" | "high" | "medium" | "low" | "info";
export type PolicyOutcome = "queued" | "duplicate-ticket" | "connection-stale" | "invalid-payload";

export interface PolicyConnection {
  id: string; name: string; profile: NativeProfile; revision: number; enabled: boolean; current: boolean;
}
export interface NotificationPolicy {
  id: string; workspaceId: string; name: string; connectionId: string; connectionProfile: NativeProfile;
  connectionRevision: number; enabled: boolean; changeKinds: ChangeKind[]; minimumSeverity: Severity;
  revision: number; epoch: number; approvedBy: string; approvedByName: string; rationale: string;
  createdAt: string; updatedAt: string; connection: PolicyConnection;
}
export interface NotificationPolicyEvent {
  id: string; workspaceId: string; policyId: string; policyRevision: number; findingId: string;
  findingChangeRevision: number; outcome: PolicyOutcome; deliveryId: string | null; createdAt: string;
}

export const connection: PolicyConnection = {
  id: hexID("c7", 1), name: "Synthetic Jira policy destination", profile: "jira-cloud-v3",
  revision: 4, enabled: true, current: true,
};
export const policy: NotificationPolicy = {
  id: hexID("b7", 1), workspaceId: alpha.id, name: "High severity lifecycle changes",
  connectionId: connection.id, connectionProfile: connection.profile, connectionRevision: connection.revision,
  enabled: true, changeKinds: ["new", "changed", "reopened"], minimumSeverity: "medium",
  revision: 3, epoch: 7, approvedBy: hexID("a7", 1), approvedByName: "Synthetic policy admin",
  rationale: "Notify the reviewed Jira destination for meaningful medium-or-higher changes.",
  createdAt: stamp, updatedAt: stamp, connection: structuredClone(connection),
};
export const savedPolicy: NotificationPolicy = {
  ...structuredClone(policy), minimumSeverity: "high", revision: 4, epoch: 8,
  rationale: "Raise the reviewed automatic threshold to high severity.", updatedAt: updatedStamp,
};
export const stalePolicy: NotificationPolicy = {
  ...structuredClone(policy), id: hexID("b7", 2), name: "Stale Jira policy",
  connectionRevision: 3, revision: 2, epoch: 6,
  connection: { ...structuredClone(connection), revision: 4, enabled: false, current: false },
  rationale: "Retained for read-only review after the selected connection changed.",
};
export const events: NotificationPolicyEvent[] = [
  {
    id: hexID("e7", 1), workspaceId: alpha.id, policyId: stalePolicy.id, policyRevision: 1,
    findingId: hexID("f7", 1), findingChangeRevision: 2, outcome: "queued",
    deliveryId: hexID("d7", 1), createdAt: "2026-10-08T03:20:00.123456Z",
  },
  {
    id: hexID("e7", 2), workspaceId: alpha.id, policyId: stalePolicy.id, policyRevision: 2,
    findingId: hexID("f7", 2), findingChangeRevision: 4, outcome: "connection-stale",
    deliveryId: null, createdAt: "2026-10-08T03:21:00.123456Z",
  },
  {
    id: hexID("e7", 3), workspaceId: alpha.id, policyId: stalePolicy.id, policyRevision: 2,
    findingId: hexID("f7", 3), findingChangeRevision: 5, outcome: "duplicate-ticket",
    deliveryId: hexID("d7", 3), createdAt: "2026-10-08T03:22:00.123456Z",
  },
];

export function policyPage(items: readonly NotificationPolicy[]) {
  return { apiVersion, items: structuredClone(items), total: items.length, nextCursor: null };
}
export function policyDetail(value: NotificationPolicy) {
  return { apiVersion, policy: structuredClone(value) };
}
export function eventPage(items: readonly NotificationPolicyEvent[]) {
  return { apiVersion, items: structuredClone(items), total: items.length, nextCursor: null };
}
