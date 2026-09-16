import { apiVersion } from "./api-contract";
import type { WorkItem } from "./api-contract";

export { apiVersion };
export type ActionRole = "admin" | "analyst" | "viewer";
export interface ActionNote { id: string; text: string }
export interface ActionObservation {
  id: string;
  runId: string;
  sourceId: string;
  scanId: string;
  scope: { id: string; revision: string; branch: string };
  sourceScanAt: string | null;
  sourceFindingId: string;
  sourceSeverity: string;
  normalizedSeverity: WorkItem["severity"];
  sourceLocation: { uri: string; line: number };
  impact: string;
  remediation: string;
  unmapped: Record<string, string>;
  evidenceDigest: string;
}
export interface ActionFinding extends WorkItem {
  assetId: string;
  workspaceId: string;
  scopeLabel: string;
  description: string;
  remediation: string;
  evidence: { text: string; sourceLabel: string; verificationState: "not-run" };
  ownerId: string | null;
  sourceState: "observed" | "inferred-resolved";
  sourceFreshnessAt: string | null;
  disposition: "none" | "accepted-risk";
  acceptedRiskExpiresAt: string | null;
  riskAcceptanceExpired: boolean;
  verifiedResolution: false;
  notes: ActionNote[];
  observations: ActionObservation[];
  notesNextCursor: string | null;
  observationsNextCursor: string | null;
}
export interface ActionFindingResponse {
  apiVersion: typeof apiVersion;
  dataOrigin: "synthetic";
  finding: ActionFinding;
}
export interface ActionWorkResponse {
  apiVersion: typeof apiVersion;
  dataOrigin: "synthetic";
  items: WorkItem[];
  total: number;
  nextCursor: string | null;
}

export const actionAlpha = { id: "11000000000000000000000000000001", name: "Synthetic triage Alpha" };
export const actionBeta = { id: "11000000000000000000000000000002", name: "Synthetic triage Beta" };
export const actionUser = {
  id: "22000000000000000000000000000001",
  name: "Synthetic session analyst",
  email: "triage-analyst@synthetic.invalid",
};
export const currentOwner = { id: "22000000000000000000000000000002", name: "Synthetic current owner" };
export const confirmedOwnerName = "Synthetic service-confirmed owner";
export const actionCookie = "a".repeat(43);
export const serverNow = "2026-09-16T12:00:00Z";
export const expiredRiskAt = "2026-09-10T12:34:00Z";
export const literalNote = "  Synthetic literal triage note\nSecond line: <b>not markup</b>\n<script>literal-only</script>\njavascript:literal-only\nhttps://synthetic.invalid/not-a-link\n  ";
export const originalNote = { id: "71000000000000000000000000000001", text: "Synthetic earlier analyst context.\nKeep this original line." };
export const identicalTextNote = { id: "71000000000000000000000000000002", text: literalNote };
export const actionObservations: ActionObservation[] = [1, 2].map((index) => ({
  id: `8100000000000000000000000000000${index}`,
  runId: `9100000000000000000000000000000${index}`,
  sourceId: "a1000000000000000000000000000001",
  scanId: `Synthetic triage scan ${index}`,
  scope: { id: "synthetic-triage-scope", revision: String(index), branch: "main" },
  sourceScanAt: index === 1 ? "2026-09-02T04:05:00Z" : null,
  sourceFindingId: "synthetic-stable-source-result",
  sourceSeverity: index === 1 ? "warning" : "high",
  normalizedSeverity: "high",
  sourceLocation: { uri: "src/synthetic-triage.ts", line: index * 11 },
  impact: `Synthetic preserved impact ${index}.`,
  remediation: `Synthetic preserved remediation ${index}.`,
  unmapped: { retained: `Synthetic source context ${index}` },
  evidenceDigest: `sha256:${String(index).repeat(64)}`,
}));
export const primaryFinding: ActionFinding = {
  id: "51000000000000000000000000000001",
  assetId: "31000000000000000000000000000001",
  workspaceId: actionAlpha.id,
  title: "Triage alpha dependency review",
  assetName: "triage-alpha-repository",
  severity: "high",
  ownerId: null,
  ownerName: null,
  workflowState: "open",
  sourceScanAt: null,
  sourceFreshnessAt: "2026-09-07T05:06:00Z",
  collectedAt: "2026-09-09T06:07:00Z",
  importedAt: "2026-09-10T07:08:00Z",
  scopeLabel: "synthetic-triage-scope / main (revision 2)",
  description: "Synthetic source observation, not an independently verified vulnerability.",
  remediation: "Preserve the supplied context. No command or external action is requested.",
  evidence: {
    text: "Synthetic finding evidence: <em>literal text only</em>.\nPreserve the original second line.",
    sourceLabel: "Synthetic triage report",
    verificationState: "not-run",
  },
  sourceState: "observed",
  disposition: "none",
  acceptedRiskExpiresAt: null,
  riskAcceptanceExpired: false,
  verifiedResolution: false,
  notes: [originalNote, identicalTextNote],
  observations: actionObservations,
  notesNextCursor: null,
  observationsNextCursor: null,
};
export const companionFinding: ActionFinding = {
  ...primaryFinding,
  id: "41000000000000000000000000000001",
  title: "Triage alpha configuration review",
  ownerId: currentOwner.id,
  ownerName: currentOwner.name,
  workflowState: "in-progress",
};
export const betaFinding: ActionFinding = {
  ...primaryFinding,
  id: "61000000000000000000000000000001",
  workspaceId: actionBeta.id,
  assetId: "31000000000000000000000000000002",
  title: "Triage beta isolated review",
  assetName: "triage-beta-repository",
  description: "Synthetic Beta context only.",
  evidence: { text: "Synthetic Beta literal evidence.", sourceLabel: "Synthetic Beta report", verificationState: "not-run" },
  notes: [{ id: "72000000000000000000000000000001", text: "Synthetic Beta note only." }],
  observations: [],
};
export const findingPath = `/api/v1/findings/${primaryFinding.id}`;
export const notesPath = `${findingPath}/notes`;
export const workPath = "/api/v1/work";

export function workItem(finding: ActionFinding): WorkItem {
  const { id, title, assetName, severity, ownerName, workflowState, sourceScanAt, collectedAt, importedAt } = finding;
  return { id, title, assetName, severity, ownerName, workflowState, sourceScanAt, collectedAt, importedAt };
}

export function backendID(value: unknown): value is string {
  return typeof value === "string" && /^[a-f0-9]{32}$/.test(value);
}

export function validNoteText(value: unknown): value is string {
  const whitespaceOnly = /^[\u0009-\u000d\u0020\u0085\u00a0\u1680\u2000-\u200a\u2028\u2029\u202f\u205f\u3000]*$/u;
  return typeof value === "string" && !whitespaceOnly.test(value) && !value.includes("\0") && Buffer.byteLength(value, "utf8") <= 8192;
}

export function validExpiry(value: unknown): value is string {
  if (typeof value !== "string") return false;
  const parts = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.\d+)?(?:Z|([+-])(\d{2}):(\d{2}))$/.exec(value);
  if (!parts) return false;
  const [year, month, day, hour, minute, second] = parts.slice(1, 7).map(Number);
  const leap = year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0);
  const days = [31, leap ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31][month - 1] ?? 0;
  return month >= 1 && month <= 12 && day >= 1 && day <= days && hour < 24 && minute < 60 && second < 60 &&
    Number(parts[8] ?? 0) < 24 && Number(parts[9] ?? 0) < 60 && Number.isFinite(Date.parse(value)) &&
    Date.parse(value) !== Date.parse("0001-01-01T00:00:00Z");
}
