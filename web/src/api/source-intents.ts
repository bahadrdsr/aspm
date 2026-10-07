import { requestAuthority } from "./authorization";
import { APIError } from "./client";
import type { AzureDevOpsSelection } from "./source-types";

let revision = -1;
let intents = new Map<string, string>();
let azureDevOpsIntents = new Map<string, { key: string; selection: AzureDevOpsSelection }>();

function currentIntents() {
  const authority = requestAuthority();
  if (!authority.workspace || authority.signal.aborted) throw new APIError("Sign in to continue.", "unauthorized", false);
  if (revision !== authority.revision) {
    intents.clear();
    azureDevOpsIntents.clear();
    intents = new Map();
    azureDevOpsIntents = new Map();
    revision = authority.revision;
    const current = intents;
    const currentAzureDevOps = azureDevOpsIntents;
    authority.signal.addEventListener("abort", () => { current.clear(); currentAzureDevOps.clear(); }, { once: true });
  }
  return intents;
}
export function pendingCollectionIntent(sourceId: string): string | null { return currentIntents().get(sourceId) ?? null; }
export function collectionIntent(sourceId: string): string {
  const current = currentIntents();
  const key = current.get(sourceId) ?? crypto.randomUUID();
  current.set(sourceId, key);
  return key;
}
export function acknowledgeCollectionIntent(sourceId: string, key: string) {
  const current = currentIntents();
  if (current.get(sourceId) === key) current.delete(sourceId);
}
export function pendingAzureDevOpsCollectionIntent(sourceId: string) {
  currentIntents();
  return azureDevOpsIntents.get(sourceId) ?? null;
}
export function azureDevOpsCollectionIntent(sourceId: string, selection: AzureDevOpsSelection) {
  currentIntents();
  const existing = azureDevOpsIntents.get(sourceId);
  if (existing && JSON.stringify(existing.selection) !== JSON.stringify(selection)) {
    throw new APIError("A previous Azure DevOps collection acknowledgement is unresolved. Retry its exact build, artifact and path before creating another intent.", "conflict", false);
  }
  const intent = existing ?? { key: crypto.randomUUID(), selection };
  azureDevOpsIntents.set(sourceId, intent);
  return intent;
}
export function acknowledgeAzureDevOpsCollectionIntent(sourceId: string, key: string) {
  currentIntents();
  if (azureDevOpsIntents.get(sourceId)?.key === key) azureDevOpsIntents.delete(sourceId);
}
