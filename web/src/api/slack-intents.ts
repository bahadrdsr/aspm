import { requestAuthority } from "./authorization";
import { APIError } from "./client";
import type { FindingDeliveryInput } from "./types";

let revision = -1;
let intents = new Map<string, Readonly<FindingDeliveryInput>>();

function scopedIntents() {
  const authority = requestAuthority();
  if (!authority.workspace || authority.signal.aborted) throw new APIError("Sign in to continue.", "unauthorized", false);
  if (authority.revision !== revision) {
    intents.clear();
    intents = new Map();
    revision = authority.revision;
    const current = intents;
    authority.signal.addEventListener("abort", () => current.clear(), { once: true });
  }
  return intents;
}

export function unresolvedNotification(findingId: string): Readonly<FindingDeliveryInput> | null {
  return scopedIntents().get(findingId) ?? null;
}

export function notificationIntent(findingId: string, connectionId: string): Readonly<FindingDeliveryInput> {
  const scope = scopedIntents();
  const prior = scope.get(findingId);
  if (prior) {
    if (prior.connectionId !== connectionId) throw new APIError("An unresolved notification must keep its original connection and intent. Review delivery history.", "conflict", false);
    return prior;
  }
  const intent = Object.freeze({ connectionId, idempotencyKey: crypto.randomUUID() });
  scope.set(findingId, intent);
  return intent;
}

export function acknowledgeNotification(findingId: string, intent: Readonly<FindingDeliveryInput>): void {
  const scope = scopedIntents();
  if (scope.get(findingId) === intent) scope.delete(findingId);
}
