import { APIError } from "./client";
import { requestAuthority } from "./authorization";
import type { Assessment } from "./assessment-types";

interface PendingAssessment {
  workspaceId: string;
  requestedBy: string;
  findingId: string;
  previewId: string;
  idempotencyKey: string;
}

// Only unresolved identities survive a closed view. They are never replayed, persisted, or used in another scope.
const pending = new Map<string, Readonly<PendingAssessment>>();
function identity(workspaceId: string, requestedBy: string, findingId: string): string {
  return JSON.stringify([workspaceId, requestedBy, findingId]);
}
export function pendingAssessment(workspaceId: string, requestedBy: string, findingId: string): Readonly<PendingAssessment> | null {
  if (requestAuthority().workspace !== workspaceId) return null;
  return pending.get(identity(workspaceId, requestedBy, findingId)) ?? null;
}
export function beginAssessment(workspaceId: string, requestedBy: string, findingId: string, previewId: string): Readonly<PendingAssessment> {
  if (requestAuthority().workspace !== workspaceId) throw new APIError("The assessment workspace changed. Reopen its authorized history.", "forbidden", false);
  const key = identity(workspaceId, requestedBy, findingId);
  if (pending.has(key)) throw new APIError("A queue outcome remains unresolved. Read authorized history before starting another assessment.", "conflict", false);
  if (pending.size >= 100) throw new APIError("Unresolved assessment intents must be reconciled before queueing more work.", "conflict", false);
  const intent = Object.freeze({ workspaceId, requestedBy, findingId, previewId, idempotencyKey: crypto.randomUUID() });
  pending.set(key, intent);
  return intent;
}
export function rejectAssessment(intent: Readonly<PendingAssessment>): void {
  const key = identity(intent.workspaceId, intent.requestedBy, intent.findingId);
  if (pending.get(key) === intent) pending.delete(key);
}
export function reconcileAssessment(receipt: Assessment): void {
  if (requestAuthority().workspace !== receipt.workspaceId) return;
  const key = identity(receipt.workspaceId, receipt.requestedBy, receipt.findingId);
  const intent = pending.get(key);
  if (intent?.idempotencyKey === receipt.idempotencyKey && intent.previewId === receipt.previewId) pending.delete(key);
}
