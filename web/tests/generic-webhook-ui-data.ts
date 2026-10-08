import { hexID } from "./slack-ui-data";
import { alpha, apiVersion, beta, first, stamp, started, attempted, completed, user } from "./jira-work-items-ui-data";
import type { NotificationPolicy, PolicyConnection } from "./notification-policies-ui-data";

export { alpha, apiVersion, beta, first, user };
export const profile = "generic-webhook-v1" as const;
export const connectionsPath = "/api/v1/integrations/connections";
export const connectionPath = (id: string) => `${connectionsPath}/${id}`;
export const previewPath = (id = first.id) => `/api/v1/findings/${id}/delivery-previews`;
export const historyPath = (id = first.id) => `/api/v1/findings/${id}/deliveries`;
export const deliveryPath = (id: string) => `/api/v1/integrations/deliveries/${id}`;
export const receiverOrigin = "https://receiver.synthetic.invalid:8443";
export const receiverPath = "/aspm/hooks/finding";
export const webhookURL = receiverOrigin + receiverPath;
export const rotatedPath = "/aspm/hooks/rotated";
export const rotatedURL = receiverOrigin + rotatedPath;
export const draftSecret = "SYNTHETIC-WEBHOOK-HMAC-KEY-1234567890";
export const replacementSecret = "SYNTHETIC-WEBHOOK-HMAC-KEY-0987654321";

export interface WebhookMetadata {
  origin: string; path: string; signature: "hmac-sha256";
}
export interface WebhookConnection {
  id: string; workspaceId: string; profile: typeof profile; name: string;
  enabled: boolean; credentialConfigured: boolean; revision: number;
  createdAt: string; updatedAt: string; webhook: WebhookMetadata;
  permissionState: "not-verified";
}
export interface WebhookPreview {
  workspaceId: string; findingId: string; connectionId: string; connectionRevision: number;
  profile: typeof profile; requestedBy: string; webhook: WebhookMetadata;
  payload: { title: string; body: string; deepLink: string };
  bindingDigest: string; nativeValidation: "not-run";
  reviewRequirements: [
    "explicit-queue-consent", "operator-approved-origin", "receiver-signature-verification",
  ];
}
export interface WebhookFailure {
  code: string; nativeCode: string; httpStatus: number; retryAfterSeconds: number; retryable: false;
}
export interface WebhookDelivery {
  id: string; workspaceId: string; findingId: string; connectionId: string; connectionRevision: number;
  profile: typeof profile; requestedBy: string; state:
    "queued" | "dispatching" | "accepted" | "blocked" | "failed" | "rate-limited" | "uncertain";
  payload: WebhookPreview["payload"]; webhook: WebhookMetadata;
  createdAt: string; dispatchStartedAt: string | null; outboundAttemptedAt: string | null;
  completedAt: string | null; receipt: null; failure: WebhookFailure | null;
}

export const connection: WebhookConnection = {
  id: hexID("bc", 1), workspaceId: alpha.id, profile, name: "Synthetic signed webhook",
  enabled: true, credentialConfigured: true, revision: 4, createdAt: stamp, updatedAt: stamp,
  webhook: { origin: receiverOrigin, path: receiverPath, signature: "hmac-sha256" },
  permissionState: "not-verified",
};
export const created: WebhookConnection = {
  ...structuredClone(connection), id: hexID("bc", 2), name: "Synthetic newly configured webhook",
  enabled: false, revision: 1,
};
export const createInput = {
  profile, name: created.name, enabled: created.enabled, webhookUrl: webhookURL, secret: draftSecret,
};
export const preview: WebhookPreview = {
  workspaceId: alpha.id, findingId: first.id, connectionId: connection.id,
  connectionRevision: connection.revision, profile, requestedBy: user.id,
  webhook: structuredClone(connection.webhook),
  payload: {
    title: first.title, body: `Severity: ${first.severity}\nAsset: ${first.assetName}`,
    deepLink: `https://aspm.synthetic.invalid/#/work?finding=${first.id}`,
  },
  bindingDigest: `sha256:${"b".repeat(64)}`, nativeValidation: "not-run",
  reviewRequirements: [
    "explicit-queue-consent", "operator-approved-origin", "receiver-signature-verification",
  ],
};
export const queued: WebhookDelivery = {
  id: hexID("bd", 1), workspaceId: alpha.id, findingId: first.id,
  connectionId: connection.id, connectionRevision: connection.revision,
  profile, requestedBy: user.id, state: "queued", payload: structuredClone(preview.payload),
  webhook: structuredClone(preview.webhook), createdAt: stamp, dispatchStartedAt: null,
  outboundAttemptedAt: null, completedAt: null, receipt: null, failure: null,
};
export const accepted: WebhookDelivery = {
  ...structuredClone(queued), id: hexID("bd", 2), state: "accepted",
  dispatchStartedAt: started, outboundAttemptedAt: attempted, completedAt: completed,
};
export const uncertain: WebhookDelivery = {
  ...structuredClone(accepted), id: hexID("bd", 3), state: "uncertain",
  failure: {
    code: "uncertain", nativeCode: "", httpStatus: 0,
    retryAfterSeconds: 0, retryable: false,
  },
};
export const history = [queued, accepted, uncertain];

const policyConnection: PolicyConnection = {
  id: connection.id, name: connection.name, profile, revision: connection.revision,
  enabled: true, current: true,
};
export const webhookPolicy: NotificationPolicy = {
  id: hexID("be", 1), workspaceId: alpha.id, name: "Signed webhook lifecycle changes",
  connectionId: connection.id, connectionProfile: profile, connectionRevision: connection.revision,
  enabled: true, changeKinds: ["new", "changed", "reopened"], minimumSeverity: "medium",
  revision: 2, epoch: 9, approvedBy: user.id, approvedByName: user.name,
  rationale: "Send the fixed signed payload to the operator-approved receiver.",
  createdAt: stamp, updatedAt: stamp, connection: policyConnection,
};

export const connectionPage = (items: readonly WebhookConnection[]) => ({
  apiVersion, dataOrigin: "synthetic", items: structuredClone(items),
  total: items.length, nextCursor: null,
});
export const connectionDetail = (value: WebhookConnection) => ({
  apiVersion, connection: structuredClone(value),
});
export const previewDetail = (value: WebhookPreview) => ({
  apiVersion, preview: structuredClone(value),
});
export const deliveryDetail = (value: WebhookDelivery) => ({
  apiVersion, delivery: structuredClone(value),
});
export const deliveryPage = (items: readonly WebhookDelivery[]) => ({
  apiVersion, dataOrigin: "synthetic", items: structuredClone(items),
  total: items.length, nextCursor: null,
});
