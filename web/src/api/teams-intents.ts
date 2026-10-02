import { requestAuthority } from "./authorization";
import { APIError } from "./client";
import type { FindingDetail } from "./types";
import type { TeamsPreview, TeamsQueueInput } from "./teams-types";
import { sameTeamsDestination, sameTeamsPayload } from "./teams-input";

export interface TeamsIntent {
  readonly input: Readonly<TeamsQueueInput>; readonly preview: TeamsPreview; readonly finding: string; invalidated: boolean;
}
let revision = -1;
let intents = new Map<string, TeamsIntent>();
export function teamsFindingIdentity(finding: FindingDetail) {
  return JSON.stringify([finding.workspaceId, finding.id, finding.title, finding.severity, finding.assetId, finding.assetName]);
}
function scope() {
  const authority = requestAuthority();
  if (!authority.workspace || authority.signal.aborted) throw new APIError("Sign in to continue.", "unauthorized", false);
  if (revision !== authority.revision) {
    intents.clear(); intents = new Map(); revision = authority.revision;
    const current = intents;
    authority.signal.addEventListener("abort", () => current.clear(), { once: true });
  }
  return intents;
}
export function pendingTeamsIntent(findingId: string) { return scope().get(findingId) ?? null; }
export function sameTeamsIntent(intent: TeamsIntent, value: TeamsPreview, finding: FindingDetail) {
  const prior = intent.preview;
  return !intent.invalidated && intent.finding === teamsFindingIdentity(finding) &&
    prior.workspaceId === value.workspaceId && prior.findingId === value.findingId && prior.requestedBy === value.requestedBy &&
    prior.connectionId === value.connectionId && prior.connectionRevision === value.connectionRevision &&
    prior.bindingDigest === value.bindingDigest && sameTeamsDestination(prior.destination, value.destination) &&
    sameTeamsPayload(prior.payload, value.payload);
}
export function teamsQueueIntent(value: TeamsPreview, finding: FindingDetail): TeamsIntent {
  const current = scope(), prior = current.get(finding.id);
  if (prior) {
    if (!sameTeamsIntent(prior, value, finding)) {
      prior.invalidated = true;
      throw new APIError("An unresolved original Teams intent may already be queued. Consent changed; read authorized history. No replacement key or blind resend is permitted.", "conflict", false);
    }
    return prior;
  }
  if (current.size >= 32) throw new APIError("There are 32 unresolved Teams intents in this scope. None was discarded. Review authorized history.", "conflict", false);
  const result: TeamsIntent = {
    input: Object.freeze({ connectionId: value.connectionId, idempotencyKey: crypto.randomUUID(), previewDigest: value.bindingDigest, confirm: true }),
    preview: structuredClone(value), finding: teamsFindingIdentity(finding), invalidated: false,
  };
  current.set(finding.id, result);
  return result;
}
export function invalidateTeamsIntent(findingId: string) { const intent = scope().get(findingId); if (intent) intent.invalidated = true; }
export function acknowledgeTeamsIntent(findingId: string, intent: TeamsIntent) {
  const current = scope(); if (current.get(findingId) === intent) current.delete(findingId);
}
