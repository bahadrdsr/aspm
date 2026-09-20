import { useEffect, useState } from "react";
import type { APIError } from "@/api/client";
import type { AIGrant, AIPolicy, AIProfile } from "@/api/ai-types";
import { timestampLabel } from "@/lib/format";
import { ActionButton } from "@/components/action-button";
import { FormError } from "@/components/form-dialog";

export function AITime({ value }: { value: string }) {
  return <time dateTime={value}>{timestampLabel(value)}</time>;
}

export function AIReadState({ error, pending, retry, subject }: {
  error: APIError | null; pending: boolean; retry: () => void; subject: string;
}) {
  return <>
    <FormError error={error} />
    {error && <ActionButton variant="outline" disabled={pending} onClick={retry}>Retry {subject}</ActionButton>}
    {pending && <p role="status" className="form-help">Loading {subject}...</p>}
  </>;
}

export function useExpiryClock(expiries: readonly string[]) {
  const [revision, tick] = useState(0);
  const times = expiries.join("\n");
  useEffect(() => {
    const future = times.split("\n").map(Date.parse).filter((value) => Number.isFinite(value) && value > Date.now());
    if (future.length === 0) return;
    const delay = Math.min(Math.max(1, Math.min(...future) - Date.now()), 2_147_483_647);
    const timer = setTimeout(() => tick((value) => value + 1), delay);
    return () => clearTimeout(timer);
  }, [times, revision]);
}

export function grantStatus(grant: AIGrant, profile: AIProfile | null, policy: AIPolicy | null): string {
  if (grant.revokedAt !== null) return "Revoked";
  if (Date.parse(grant.expiresAt) <= Date.now()) return "Expired";
  if (profile && (!profile.enabled || !profile.structuredOutput || profile.revision !== grant.profileRevision ||
    profile.endpoint !== grant.destination || profile.family !== "local" && !profile.credentialConfigured) ||
    policy && (policy.mode !== "approved-hosted" || policy.revision !== grant.policyRevision)) return "Configuration changed";
  if (!profile || !policy) return "Current configuration not reviewed";
  return "Matches current configuration";
}
