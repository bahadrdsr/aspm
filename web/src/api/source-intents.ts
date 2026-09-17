import { requestAuthority } from "./authorization";
import { APIError } from "./client";

let revision = -1;
let intents = new Map<string, string>();

function currentIntents() {
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
