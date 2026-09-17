import type { JSONValue } from "@/api/types";
import { apiVersion, backendID, primaryFinding, validNoteText, workItem } from "./finding-actions-data";
import type { ActionFinding, ActionNote, ActionObservation } from "./finding-actions-data";

export { apiVersion, backendID, validNoteText, workItem };
export type HistoryRole = "admin" | "analyst" | "viewer";
export type HistoryStream = "notes" | "observations";
export interface HistoryObservation extends Omit<ActionObservation, "unmapped"> { unmapped: Record<string, JSONValue> }
export interface HistoryFinding extends Omit<ActionFinding, "observations"> { observations: HistoryObservation[] }
export interface HistoryResponse { apiVersion: typeof apiVersion; dataOrigin: "synthetic"; finding: HistoryFinding }
export const historyAlpha = { id: "16000000000000000000000000000001", name: "Synthetic history Alpha" };
export const historyBeta = { id: "16000000000000000000000000000002", name: "Synthetic history Beta" };
export const historyUser = { id: "26000000000000000000000000000001", name: "Synthetic history analyst", email: "finding-history@synthetic.invalid" };
export const historyOwner = { id: "26000000000000000000000000000002", name: "Synthetic history owner" };
export const historyCookie = "h".repeat(43);
export const notesTotal = 503;
export const observationsTotal = 507;
export const sameNoteText = "  Synthetic same-text history note\n<em>literal café content</em>\n<script>literal-only</script>\nhttps://history.synthetic.invalid/not-a-link\n  ";
export const refreshedDescription = "Synthetic source metadata read after the confirmed human workflow ACK.";
export function historyID(prefix: string, ordinal: number) {
  return prefix + (ordinal * 16).toString(16).padStart(32 - prefix.length, "0");
}
export function noteAt(index: number): ActionNote {
  return { id: historyID("71", index), text: index === 1 || index === 501 ? sameNoteText : `Synthetic history note ${String(index).padStart(4, "0")}: café context.` };
}
export function observationAt(index: number): HistoryObservation {
  return {
    id: historyID("81", index), runId: historyID("91", index), sourceId: historyID("a1", index % 2 + 1),
    scanId: `Synthetic history scan ${String(index).padStart(4, "0")}`,
    scope: { id: `synthetic-history-scope-${index % 2}`, revision: String(index), branch: index % 2 ? "main" : "release" },
    sourceScanAt: index % 2 ? "2026-09-04T05:06:07.123456Z" : null,
    sourceFindingId: `synthetic-native-history-${index % 3}`, sourceSeverity: index % 2 ? "warning" : "HIGH",
    normalizedSeverity: index % 2 ? "medium" : "high", sourceLocation: { uri: `src/synthetic-history-${index}.ts`, line: index * 3 },
    impact: `Synthetic retained impact ${index}.`, remediation: `Synthetic retained source guidance ${index}.`,
    unmapped: {
      vendor: { ordinal: index, literal: `<b>Source variant ${index} is literal - 安全.</b>`, nullable: null, flags: [true, false] },
      sourceFields: ["retained", `variant-${index}`],
    },
    evidenceDigest: `sha256:${index.toString(16).padStart(64, "0")}`,
  };
}
export const historyFinding: HistoryFinding = {
  ...structuredClone(primaryFinding),
  id: "56000000000000000000000000000001", assetId: "36000000000000000000000000000001",
  workspaceId: historyAlpha.id, title: "Synthetic finding with independent history streams",
  assetName: "synthetic-history-repository", ownerId: historyOwner.id, ownerName: historyOwner.name,
  description: "Synthetic original source description, not independently verified.",
  remediation: "Synthetic source remediation remains independent of human workflow.",
  evidence: { text: "Synthetic original evidence: <em>literal, not markup</em>.\nKeep this exact line.", sourceLabel: "Synthetic history source", verificationState: "not-run" },
  sourceState: "inferred-resolved", disposition: "accepted-risk", acceptedRiskExpiresAt: "2026-09-10T04:05:06Z",
  riskAcceptanceExpired: true, verifiedResolution: false,
  notes: Array.from({ length: notesTotal }, (_, index) => noteAt(index + 1)),
  observations: Array.from({ length: observationsTotal }, (_, index) => observationAt(index + 1)),
  notesNextCursor: null, observationsNextCursor: null,
};
export const betaHistoryFinding: HistoryFinding = {
  ...structuredClone(historyFinding), id: "66000000000000000000000000000001",
  workspaceId: historyBeta.id, assetId: "36000000000000000000000000000002",
  title: "Synthetic isolated Beta history", assetName: "synthetic-beta-history-repository",
  description: "Synthetic Beta source context.", evidence: { text: "Synthetic Beta original evidence.", sourceLabel: "Synthetic Beta source", verificationState: "not-run" },
  notes: [{ id: historyID("72", 1), text: "Synthetic Beta note only." }], observations: [],
};
export const findingPath = `/api/v1/findings/${historyFinding.id}`;
export const notesPath = `${findingPath}/notes`;
export const newNoteID = "71" + (8 * 16 + 8).toString(16).padStart(30, "0");
export function cursorQuery(url: URL) {
  const notesCursor = url.searchParams.get("notesCursor") ?? "", observationsCursor = url.searchParams.get("observationsCursor") ?? "";
  if ([...url.searchParams.keys()].some((key) => !["notesCursor", "observationsCursor"].includes(key)) ||
    ["notesCursor", "observationsCursor"].some((key) => url.searchParams.getAll(key).length > 1) ||
    notesCursor !== "" && !backendID(notesCursor) || observationsCursor !== "" && !backendID(observationsCursor)) {
    throw new Error("Finding history admits only the existing native notesCursor and observationsCursor, without limit/offset/search/revision parameters.");
  }
  return { notesCursor, observationsCursor };
}
export function findingPage(finding: HistoryFinding, url: URL): HistoryResponse {
  const { notesCursor, observationsCursor } = cursorQuery(url);
  const notes = finding.notes.filter((note) => note.id > notesCursor).sort((a, b) => a.id.localeCompare(b.id));
  const observations = finding.observations.filter((item) => item.id > observationsCursor).sort((a, b) => a.id.localeCompare(b.id));
  return { apiVersion, dataOrigin: "synthetic", finding: {
    ...structuredClone(finding), notes: notes.slice(0, 500), observations: observations.slice(0, 500),
    notesNextCursor: notes.length > 500 ? notes[499].id : null,
    observationsNextCursor: observations.length > 500 ? observations[499].id : null,
  } };
}
