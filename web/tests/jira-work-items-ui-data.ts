import { alpha, apiVersion, beta, first, user } from "./work-search-data";
import type { ActionFinding } from "./work-search-data";
import { hexID } from "./slack-ui-data";

export { alpha, apiVersion, beta, first, user };
export const profile = "jira-cloud-v3" as const;
export const connectionsPath = "/api/v1/integrations/connections";
export const connectionPath = (id: string) => `${connectionsPath}/${id}`;
export const findingPath = (id = first.id) => `/api/v1/findings/${id}`;
export const previewPath = (id = first.id) => `${findingPath(id)}/delivery-previews`;
export const historyPath = (id = first.id) => `${findingPath(id)}/deliveries`;
export const deliveryPath = (id: string) => `/api/v1/integrations/deliveries/${id}`;
export const stamp = "2026-10-01T08:00:00.123456Z";
export const started = "2026-10-01T08:01:00.123456Z";
export const attempted = "2026-10-01T08:01:01.123456Z";
export const completed = "2026-10-01T08:01:02.123456Z";
export const draftToken = "SYNTHETIC-JIRA-OAUTH-NOT-A-CREDENTIAL-ONE";
export const replacementToken = "SYNTHETIC-JIRA-OAUTH-NOT-A-CREDENTIAL-TWO";
export const sources = ["finding.id", "finding.title", "finding.severity", "asset.name", "finding.deepLink"] as const;
export type FieldSource = typeof sources[number];

export interface JiraTarget {
  credentialType: "oauth2-bearer"; cloudId: string; apiBase: string; siteOrigin: string;
  project: string; issueType: string; fieldMappings: Record<string, FieldSource>;
}
export interface JiraConnection {
  id: string; workspaceId: string; profile: typeof profile; name: string; channel: "";
  enabled: boolean; credentialConfigured: boolean; revision: number;
  createdAt: string; updatedAt: string; jira: JiraTarget; permissionState: "not-verified";
}
export interface JiraPayload { title: string; body: string; deepLink: string; fields: Record<string, string> }
export interface JiraPreview {
  workspaceId: string; findingId: string; connectionId: string; connectionRevision: number;
  profile: typeof profile; requestedBy: string; jira: JiraTarget; payload: JiraPayload;
  bindingDigest: string; nativeValidation: "not-run";
  reviewRequirements: ["explicit-queue-consent", "native-required-fields", "jira-permission"];
}
export type JiraState = "queued" | "dispatching" | "confirmed" | "accepted" | "blocked" | "failed" | "rate-limited" | "uncertain";
export interface JiraDelivery {
  id: string; workspaceId: string; findingId: string; connectionId: string; connectionRevision: number;
  profile: typeof profile; channel: ""; requestedBy: string; state: JiraState; jira: JiraTarget; payload: JiraPayload;
  createdAt: string; dispatchStartedAt: string | null; createAttemptedAt: string | null; completedAt: string | null;
  receipt: { remoteId: string; remoteUrl: string } | null;
  failure: { code: string; nativeCode: string; httpStatus: number; retryAfterSeconds: number; retryable: false;
    stage?: "metadata" | "create"; missingFields?: string[] } | null;
}
export const target: JiraTarget = {
  credentialType: "oauth2-bearer", cloudId: "f1234567-89ab-4cde-8123-456789abcdef",
  apiBase: "https://api.atlassian.com/ex/jira/f1234567-89ab-4cde-8123-456789abcdef",
  siteOrigin: "https://jira.synthetic.invalid", project: "SYN", issueType: "10001",
  fieldMappings: { customfield_10010: "asset.name", customfield_10011: "finding.title" },
};
export const connection: JiraConnection = {
  id: hexID("ca", 1), workspaceId: alpha.id, profile, name: "Synthetic Jira Alpha",
  channel: "", enabled: true, credentialConfigured: true, revision: 4,
  createdAt: stamp, updatedAt: stamp, jira: target, permissionState: "not-verified",
};
export const alternate: JiraConnection = {
  ...connection, id: hexID("ca", 2), name: "Synthetic Jira other approved tenant",
  jira: { ...target, cloudId: "01234567-89ab-4cde-8123-456789abcdef",
    apiBase: "https://jira-gateway.synthetic.invalid/ex/jira/01234567-89ab-4cde-8123-456789abcdef",
    siteOrigin: "https://other-jira.synthetic.invalid", project: "ALT", fieldMappings: {} },
};
export const created: JiraConnection = {
  ...connection, id: hexID("cb", 1), name: "Synthetic Jira explicitly added", revision: 1, enabled: false,
};
export const createInput = {
  profile, name: created.name, enabled: created.enabled, token: draftToken, jira: structuredClone(target),
};
export const connectionDetail = (value: JiraConnection) => ({ apiVersion, connection: structuredClone(value) });
export const previewDetail = (value: JiraPreview) => ({ apiVersion, preview: structuredClone(value) });
export const deliveryDetail = (value: JiraDelivery) => ({ apiVersion, delivery: structuredClone(value) });
export function nativePage<T extends { id: string }>(items: readonly T[], total = items.length, nextCursor: string | null = null) {
  if (items.length > 100 || total < items.length || !Number.isSafeInteger(total) ||
    items.some((item, index) => !/^[a-f0-9]{32}$/.test(item.id) || index > 0 && items[index - 1].id >= item.id) ||
    nextCursor !== null && (nextCursor !== items.at(-1)?.id || total <= items.length)) {
    throw new Error("Declare native ascending exclusive pages with preserved totals.");
  }
  return { apiVersion, items: structuredClone(items), total, nextCursor };
}
export const connectionPages = Array.from({ length: 101 }, (_, index): JiraConnection => ({
  ...structuredClone(connection), id: hexID("ca", index + 1),
  name: index === 0 ? connection.name : `Synthetic Jira page row ${index + 1}`, enabled: index === 0,
}));

// Digests are opaque declared server fixtures, not a client implementation of binding or dispatch.
export function localPreview(selected = connection, finding: ActionFinding = first, digest = "a"): JiraPreview {
  const deepLink = `https://aspm.synthetic.invalid/#/work?finding=${finding.id}`;
  const values: Record<FieldSource, string> = {
    "finding.id": finding.id, "finding.title": finding.title, "finding.severity": finding.severity,
    "asset.name": finding.assetName, "finding.deepLink": deepLink,
  };
  return {
    workspaceId: finding.workspaceId, findingId: finding.id, connectionId: selected.id,
    connectionRevision: selected.revision, profile, requestedBy: user.id, jira: structuredClone(selected.jira),
    payload: { title: finding.title, body: `Severity: ${finding.severity}\nAsset: ${finding.assetName}`, deepLink,
      fields: Object.fromEntries(Object.entries(selected.jira.fieldMappings).map(([id, source]) => [id, values[source]])) },
    bindingDigest: `sha256:${digest.repeat(64)}`, nativeValidation: "not-run",
    reviewRequirements: ["explicit-queue-consent", "native-required-fields", "jira-permission"],
  };
}
export const preview = localPreview();
export function queued(value = preview, index = 1): JiraDelivery {
  return {
    id: hexID("da", index), workspaceId: value.workspaceId, findingId: value.findingId,
    connectionId: value.connectionId, connectionRevision: value.connectionRevision, profile, channel: "",
    requestedBy: value.requestedBy, state: "queued", jira: structuredClone(value.jira), payload: structuredClone(value.payload),
    createdAt: stamp, dispatchStartedAt: null, createAttemptedAt: null, completedAt: null, receipt: null, failure: null,
  };
}
export const confirmed: JiraDelivery = {
  ...queued(preview, 3), state: "confirmed", dispatchStartedAt: started, createAttemptedAt: attempted, completedAt: completed,
  receipt: { remoteId: "SYN-42", remoteUrl: `${target.siteOrigin}/browse/SYN-42` },
};
export const missingFields: JiraDelivery = {
  ...queued(preview, 4), state: "failed", dispatchStartedAt: started, completedAt: completed,
  failure: { code: "required_fields", nativeCode: "", httpStatus: 0, retryAfterSeconds: 0, retryable: false,
    stage: "metadata", missingFields: ["customfield_10020", "priority"] },
};
export const limited: JiraDelivery = {
  ...queued(preview, 5), state: "rate-limited", dispatchStartedAt: started, createAttemptedAt: attempted, completedAt: completed,
  failure: { code: "rate_limited", nativeCode: "", httpStatus: 429, retryAfterSeconds: 7, retryable: false, stage: "create" },
};
export const uncertain: JiraDelivery = {
  ...queued(preview, 6), state: "uncertain", dispatchStartedAt: started, createAttemptedAt: attempted, completedAt: completed,
  failure: { code: "uncertain", nativeCode: "", httpStatus: 0, retryAfterSeconds: 0, retryable: false, stage: "create" },
};
export const blocked: JiraDelivery = {
  ...queued(preview, 7), state: "blocked", completedAt: completed,
  failure: { code: "connection-disabled", nativeCode: "", httpStatus: 0, retryAfterSeconds: 0, retryable: false, stage: "metadata" },
};
export const nativeAuth: JiraDelivery = {
  ...queued(preview, 8), state: "failed", dispatchStartedAt: started, completedAt: completed,
  failure: { code: "auth", nativeCode: "synthetic_permission_denied", httpStatus: 403, retryAfterSeconds: 0, retryable: false, stage: "metadata" },
};
export const createFields: JiraDelivery = {
  ...queued(preview, 9), state: "failed", dispatchStartedAt: started, createAttemptedAt: attempted, completedAt: completed,
  failure: { code: "required_fields", nativeCode: "", httpStatus: 400, retryAfterSeconds: 0, retryable: false,
    stage: "create", missingFields: ["customfield_10020"] },
};
export const historyRows: JiraDelivery[] = [
  queued(), { ...queued(preview, 2), state: "dispatching", dispatchStartedAt: started },
  confirmed, missingFields, limited, uncertain, blocked, nativeAuth, createFields,
  ...Array.from({ length: 92 }, (_, index) => queued(preview, index + 10)),
];
