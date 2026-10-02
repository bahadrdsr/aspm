import { useCallback, useRef, useState } from "react";
import { APIError } from "@/api/client";
import { teamsApi } from "@/api/teams";
import { sameTeamsDestination, sameTeamsPayload } from "@/api/teams-input";
import type { TeamsDelivery, TeamsDeliveryResponse } from "@/api/teams-types";
import { useResource } from "@/lib/use-resource";
import { label, timestampLabel } from "@/lib/format";
import { ActionButton } from "./action-button";
import { FormError } from "./form-dialog";
import { TeamsPayloadView, TeamsTargetFacts } from "./teams-facts";
import { LoadingState } from "./states";

function immutable(a: TeamsDelivery, b: TeamsDelivery) {
  return a.id === b.id && a.workspaceId === b.workspaceId && a.findingId === b.findingId &&
    a.connectionId === b.connectionId && a.connectionRevision === b.connectionRevision && a.requestedBy === b.requestedBy &&
    a.createdAt === b.createdAt && sameTeamsDestination(a.destination, b.destination) && sameTeamsPayload(a.payload, b.payload);
}
const meanings = {
  queued: "Queued, not yet sent or accepted. Waiting for the independent worker.",
  dispatching: "Dispatching. The worker has started; channel delivery is not confirmed.",
  accepted: "Workflow accepted; channel delivery not confirmed",
  blocked: "Blocked by the service. No automatic retries.",
  failed: "Failed according to the service. No automatic retries.",
  "rate-limited": "Rate-limited. Retry-After is data, not a retry timer.",
  uncertain: "Uncertain: the request may have been sent. Do not retry or resend.",
};
export function TeamsDeliveryDetail({ id, findingId, initial, replay = false }: {
  id: string; findingId: string; initial: TeamsDeliveryResponse | null; replay?: boolean;
}) {
  const [checks, setChecks] = useState(0);
  const original = useRef<TeamsDelivery | null>(initial?.delivery ?? null);
  const load = useCallback(async (signal: AbortSignal) => {
    const result = initial !== null && checks === 0 ? initial : await teamsApi.delivery(id, findingId, signal);
    signal.throwIfAborted();
    if (original.current && !immutable(original.current, result.delivery)) {
      throw new APIError("The service returned invalid changed immutable Teams details. Their fields are withheld.", "invalid-response", false);
    }
    original.current = result.delivery;
    return result;
  }, [id, findingId, initial, checks]);
  const resource = useResource(load), value = resource.data?.delivery;
  return <section className="surface slack-panel" aria-label="Teams notification details">
    <header className="slack-panel-heading"><h3>Teams notification details</h3>
      {!resource.error && <ActionButton variant="outline" disabled={resource.status === "loading"}
        onClick={() => setChecks((count) => count + 1)}>Refresh Teams notification</ActionButton>}</header>
    <div className="slack-panel-content">
      <FormError error={resource.error} />
      {resource.error && <ActionButton variant="outline" onClick={() => setChecks((count) => count + 1)}>Retry Teams notification</ActionButton>}
      {resource.status === "loading" && !value && <LoadingState label="Loading Teams notification details" />}
      {value && <>
        <p className="form-help"><code>{value.id}</code></p>
        {replay && <p className="form-help">Same original intent replay, not a newly queued notification.</p>}
        <div className={`slack-delivery-state slack-state-${value.state}`} role="status" aria-label="Teams notification status">
          <strong>{label(value.state)}</strong><p>{meanings[value.state]}</p>
          {value.failure && <><p>Failure code: <code>{value.failure.code}</code></p>
            <p>HTTP status: {value.failure.httpStatus}.</p>
            <p>Retry-After: {value.failure.retryAfterSeconds} seconds. Retryable: false. No automatic retries.</p></>}
          {value.outboundAttemptedAt === null
            ? <p>No outbound POST was attempted in this received state. Local metadata checks are not a send.</p>
            : <p>An outbound attempt is recorded. Its marker is not a receipt, permission proof or safe-to-resend claim.</p>}
        </div>
        <TeamsPayloadView payload={value.payload} /><TeamsTargetFacts target={value.destination} revision={value.connectionRevision} />
        <dl className="slack-facts">
          <div><dt>Connection</dt><dd><code>{value.connectionId}</code></dd></div>
          <div><dt>Requested by</dt><dd><code>{value.requestedBy}</code></dd></div>
          <div><dt>Queued at</dt><dd><time dateTime={value.createdAt}>{timestampLabel(value.createdAt)}</time></dd></div>
          {value.dispatchStartedAt && <div><dt>Dispatch started</dt><dd><time dateTime={value.dispatchStartedAt}>{timestampLabel(value.dispatchStartedAt)}</time></dd></div>}
          {value.outboundAttemptedAt && <div><dt>Outbound attempted</dt><dd><time dateTime={value.outboundAttemptedAt}>{timestampLabel(value.outboundAttemptedAt)}</time></dd></div>}
          {value.completedAt && <div><dt>Completed</dt><dd><time dateTime={value.completedAt}>{timestampLabel(value.completedAt)}</time></dd></div>}
        </dl>
        <p className="form-help">Last received server state. Refresh is a manual read, never a resend.
          Workflow acceptance cannot supply a channel receipt or verify a Teams account.</p>
      </>}
    </div>
  </section>;
}
