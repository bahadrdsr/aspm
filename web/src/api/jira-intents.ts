import { requestAuthority } from "./authorization";
import { APIError } from "./client";
import type { FindingDetail } from "./types";
import type { JiraPreview, JiraQueueInput } from "./jira-types";
import { sameJiraPayload, sameJiraTarget } from "./jira-input";

export interface JiraIntent {
  readonly input: Readonly<JiraQueueInput>;
  readonly preview: JiraPreview;
  readonly finding: string;
  invalidated: boolean;
}
let revision = -1;
let intents = new Map<string, JiraIntent>();
const maximum = 32;

export function jiraFindingIdentity(finding: FindingDetail) { return JSON.stringify(finding); }
function scope() {
  const authority = requestAuthority();
  if (!authority.workspace || authority.signal.aborted) throw new APIError("Sign in to continue.", "unauthorized", false);
  if (revision !== authority.revision) {
    intents.clear();
    intents = new Map();
    revision = authority.revision;
    const current = intents;
    authority.signal.addEventListener("abort", () => current.clear(), { once: true });
  }
  return intents;
}
export function pendingJiraIntent(findingId: string): JiraIntent | null { return scope().get(findingId) ?? null; }
export function sameJiraIntent(intent: JiraIntent, value: JiraPreview, finding: FindingDetail) {
  const prior = intent.preview;
  return !intent.invalidated && intent.finding === jiraFindingIdentity(finding) &&
    prior.workspaceId === value.workspaceId && prior.findingId === value.findingId && prior.requestedBy === value.requestedBy &&
    prior.connectionId === value.connectionId && prior.connectionRevision === value.connectionRevision &&
    prior.bindingDigest === value.bindingDigest && sameJiraTarget(prior.jira, value.jira) && sameJiraPayload(prior.payload, value.payload);
}
export function jiraQueueIntent(value: JiraPreview, finding: FindingDetail): JiraIntent {
  const current = scope(), prior = current.get(finding.id);
  if (prior) {
    if (!sameJiraIntent(prior, value, finding)) {
      prior.invalidated = true;
      throw new APIError("An unresolved original Jira intent may already be queued or created. Its consent changed. Read authorized history; no replacement key or blind resend is permitted.", "conflict", false);
    }
    return prior;
  }
  if (current.size >= maximum) throw new APIError("There are 32 unresolved Jira intents in this scope. No intent was discarded. Review authorized history before preparing another.", "conflict", false);
  const result: JiraIntent = {
    input: Object.freeze({ connectionId: value.connectionId, idempotencyKey: crypto.randomUUID(), previewDigest: value.bindingDigest, confirm: true }),
    preview: structuredClone(value), finding: jiraFindingIdentity(finding), invalidated: false,
  };
  current.set(finding.id, result);
  return result;
}
export function invalidateJiraIntent(findingId: string) {
  const current = scope().get(findingId);
  if (current) current.invalidated = true;
}
export function acknowledgeJiraIntent(findingId: string, intent: JiraIntent) {
  const current = scope();
  if (current.get(findingId) === intent) current.delete(findingId);
}
