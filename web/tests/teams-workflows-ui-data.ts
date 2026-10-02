import { catalogResponse } from "./fixtures";
import { alpha, apiVersion, beta, first, user } from "./work-search-data";
import { hexID } from "./slack-ui-data";
import { attempted, completed, nativePage, stamp, started } from "./jira-work-items-ui-data";

export { alpha, apiVersion, attempted, beta, first, nativePage, user };
export const profile = "teams-workflows-channel" as const;
export const connectionsPath = "/api/v1/integrations/connections";
export const connectionPath = (id: string) => `${connectionsPath}/${id}`;
export const findingPath = (id = first.id) => `/api/v1/findings/${id}`;
export const previewPath = (id = first.id) => `${findingPath(id)}/delivery-previews`;
export const historyPath = (id = first.id) => `${findingPath(id)}/deliveries`;
export const deliveryPath = (id: string) => `/api/v1/integrations/deliveries/${id}`;
export const workflowOrigin = "https://workflow.synthetic.invalid:8443";
export const workflowURL = workflowOrigin + "/callback/%77orkflow-SYNTHETIC-CALLBACK-ONE/run" +
  "?tenantHint=SYNTHETIC-TENANT-HINT&tag=SYNTHETIC-TAG-ONE&%73ig=SYNTHETIC-SIG%2fUpper%2BValue%3d" +
  "&tag=SYNTHETIC-TAG-TWO&KeepCase=SYNTHETIC-RAW%2fTAIL";
export const rotatedOrigin = "https://other-workflow.synthetic.invalid";
export const rotatedURL = rotatedOrigin + "/callback/%72otation-SYNTHETIC-CALLBACK-TWO/run" +
  "?tag=SYNTHETIC-ROTATE-ONE&sig=SYNTHETIC-ROTATE%2bSig%3D&tag=SYNTHETIC-ROTATE-TWO";
export const declaration = { channelType: "standard", ownershipAcknowledged: true } as const;
export interface TeamsMetadata {
  workflowOrigin: string; channelType: "standard"; ownershipAcknowledged: true;
}
export interface TeamsConnection {
  id: string; workspaceId: string; profile: typeof profile; name: string; channel: "";
  enabled: boolean; credentialConfigured: boolean; revision: number; createdAt: string; updatedAt: string;
  teams: TeamsMetadata; permissionState: "not-verified";
}
export interface TeamsPreview {
  workspaceId: string; findingId: string; connectionId: string; connectionRevision: number;
  profile: typeof profile; requestedBy: string; destination: TeamsMetadata & { name: string };
  payload: { title: string; body: string; deepLink: string }; bindingDigest: string; nativeValidation: "not-run";
  reviewRequirements: ["explicit-queue-consent", "operator-declared-standard-channel", "workflow-owner-continuity"];
}
export interface TeamsDelivery {
  id: string; workspaceId: string; findingId: string; connectionId: string; connectionRevision: number;
  profile: typeof profile; channel: ""; requestedBy: string;
  state: "queued" | "dispatching" | "accepted" | "blocked" | "failed" | "rate-limited" | "uncertain";
  payload: TeamsPreview["payload"]; destination: TeamsPreview["destination"]; createdAt: string;
  dispatchStartedAt: string | null; outboundAttemptedAt: string | null; completedAt: string | null; receipt: null;
  failure: { code: string; nativeCode: ""; httpStatus: number; retryAfterSeconds: number; retryable: false } | null;
}
export const connection: TeamsConnection = {
  id: hexID("cc", 1), workspaceId: alpha.id, profile, name: "Synthetic Teams Alpha", channel: "",
  enabled: true, credentialConfigured: true, revision: 4, createdAt: stamp, updatedAt: stamp,
  teams: { workflowOrigin, ...declaration }, permissionState: "not-verified",
};
export const alternate: TeamsConnection = {
  ...connection, id: hexID("cc", 2), name: "Synthetic Teams second destination",
  teams: { workflowOrigin: rotatedOrigin, ...declaration },
};
export const created: TeamsConnection = {
  ...connection, id: hexID("cd", 1), name: "Synthetic Teams explicitly added", revision: 1, enabled: false,
};
export const createInput = {
  profile, name: created.name, enabled: created.enabled, workflowUrl: workflowURL, teams: declaration,
};
export const connectionDetail = (value: TeamsConnection) => ({ apiVersion, connection: structuredClone(value) });
export const previewDetail = (value: TeamsPreview) => ({ apiVersion, preview: structuredClone(value) });
export const deliveryDetail = (value: TeamsDelivery) => ({ apiVersion, delivery: structuredClone(value) });
export const connectionPages = Array.from({ length: 101 }, (_, index): TeamsConnection => ({
  ...structuredClone(connection), id: hexID("cc", index + 1),
  name: index === 0 ? connection.name : `Synthetic Teams page ${index + 1}`, enabled: index === 0,
}));
// Opaque literal server digests, never a browser implementation of native authorization.
export function localPreview(selected = connection, digest = "a"): TeamsPreview {
  return {
    workspaceId: alpha.id, findingId: first.id, connectionId: selected.id, connectionRevision: selected.revision,
    profile, requestedBy: user.id, destination: { name: selected.name, ...selected.teams },
    payload: { title: first.title, body: `Severity: ${first.severity}\nAsset: ${first.assetName}`,
      deepLink: `https://aspm.synthetic.invalid/#/work?finding=${first.id}` },
    bindingDigest: `sha256:${digest.repeat(64)}`, nativeValidation: "not-run",
    reviewRequirements: ["explicit-queue-consent", "operator-declared-standard-channel", "workflow-owner-continuity"],
  };
}
export const preview = localPreview();
export function queued(value = preview, index = 1): TeamsDelivery {
  return {
    id: hexID("dd", index), workspaceId: value.workspaceId, findingId: value.findingId,
    connectionId: value.connectionId, connectionRevision: value.connectionRevision, profile, channel: "",
    requestedBy: value.requestedBy, state: "queued", payload: structuredClone(value.payload),
    destination: structuredClone(value.destination), createdAt: stamp, dispatchStartedAt: null,
    outboundAttemptedAt: null, completedAt: null, receipt: null, failure: null,
  };
}
export const accepted: TeamsDelivery = {
  ...queued(preview, 3), state: "accepted", dispatchStartedAt: started,
  outboundAttemptedAt: attempted, completedAt: completed,
};
export const blocked: TeamsDelivery = {
  ...queued(preview, 4), state: "blocked", completedAt: completed,
  failure: { code: "connection-disabled", nativeCode: "", httpStatus: 0, retryAfterSeconds: 0, retryable: false },
};
export const failed: TeamsDelivery = {
  ...accepted, id: hexID("dd", 5), state: "failed",
  failure: { code: "auth", nativeCode: "", httpStatus: 403, retryAfterSeconds: 0, retryable: false },
};
export const limited: TeamsDelivery = {
  ...accepted, id: hexID("dd", 6), state: "rate-limited",
  failure: { code: "rate_limited", nativeCode: "", httpStatus: 429, retryAfterSeconds: 1, retryable: false },
};
export const uncertain: TeamsDelivery = {
  ...accepted, id: hexID("dd", 7), state: "uncertain",
  failure: { code: "uncertain", nativeCode: "", httpStatus: 0, retryAfterSeconds: 0, retryable: false },
};
export const historyRows: TeamsDelivery[] = [
  queued(), { ...queued(preview, 2), state: "dispatching", dispatchStartedAt: started },
  accepted, blocked, failed, limited, uncertain,
  ...Array.from({ length: 94 }, (_, index) => queued(preview, index + 8)),
];
export const invalidWorkflowURLs = [
  workflowURL + "&sig=SYNTHETIC-DUPLICATE",
  workflowOrigin + "/callback/a%2fb?sig=SYNTHETIC-SEPARATOR",
  workflowOrigin + "/callback/signed?sig=SYNTHETIC%20SPACE",
  workflowOrigin + "/callback/signed?sig=SYNTHETIC-CONTROL&extra=%0a",
  workflowURL.replace("workflow.synthetic.invalid", "WORKFLOW.synthetic.invalid"),
  workflowOrigin + "/" + "\u00e9".repeat(2049) + "?sig=SYNTHETIC-PATH-BYTES",
  workflowURL + "&pad=" + "x".repeat(16385 - Buffer.byteLength(workflowURL + "&pad=", "utf8")),
];
export const noteDraft = "SYNTHETIC UNSAVED NOTE FOR TEAMS FOCUS";
export const notePage = Array.from({ length: 100 }, (_, index) => ({
  id: hexID("ed", index + 1), text: `Synthetic private note ${index + 1}, not an outbound notification field.`,
}));
export const noteTail = { id: hexID("ed", 101), text: "Synthetic private history continuation, not notification content." };
export const findingWithHistory = { ...first, notes: notePage, notesNextCursor: notePage[99].id };
// Actual catalog.go shape: live family metadata, not successful native validation.
export const nativeCatalog = {
  ...catalogResponse, dataOrigin: "live",
  items: catalogResponse.items.map((item) => ({
    ...item, capabilities: [], supportMaturity: "planned", connectionState: "unconfigured", readyToConnect: false,
    liveVerification: { state: "not-run",
      reason: "Native connector implementation has not landed; report imports do not establish native support." },
  })),
};
