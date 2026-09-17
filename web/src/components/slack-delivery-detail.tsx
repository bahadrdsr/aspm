import { useCallback, useRef, useState } from "react";
import { APIError } from "@/api/client";
import { slackApi } from "@/api/slack";
import type { FindingDelivery, FindingDeliveryResponse } from "@/api/types";
import { useResource } from "@/lib/use-resource";
import { label, timestampLabel } from "@/lib/format";
import { ActionButton } from "./action-button";
import { FormError } from "./form-dialog";
import { Icon } from "./icon";
import { DataNotice, LoadingState } from "./states";

const explanations = {
  queued: "Not sent. Waiting for the independent delivery worker.",
  dispatching: "The worker has started dispatch. Delivery is not confirmed.",
  accepted: "The service records acceptance only. Delivery is not confirmed.",
  confirmed: "The service reports a confirmed native receipt. This does not resolve or independently verify the finding.",
  failed: "The notification failed. No automatic retries are performed.",
  blocked: "Dispatch was blocked by the service. No automatic retries are performed.",
  "rate-limited": "The provider rate-limited the notification. Retry-After is data only; no automatic retries are performed.",
  uncertain: "The outcome cannot be confirmed. The notification may already have occurred. Do not resend; no automatic retries are performed.",
};

function immutable(value: FindingDelivery) {
  const { id, workspaceId, findingId, connectionId, connectionRevision, profile, channel, requestedBy, payload, createdAt } = value;
  return JSON.stringify({ id, workspaceId, findingId, connectionId, connectionRevision, profile, channel, requestedBy, payload, createdAt });
}

export function SlackDeliveryDetail({ id, findingId, initial }: {
  id: string; findingId: string; initial: FindingDeliveryResponse | null;
}) {
  const [checks, setChecks] = useState(0);
  const original = useRef<FindingDelivery | null>(initial?.delivery ?? null);
  const load = useCallback(async (signal: AbortSignal) => {
    const response = initial !== null && checks === 0 ? initial : await slackApi.delivery(id, findingId, signal);
    signal.throwIfAborted();
    if (original.current && immutable(original.current) !== immutable(response.delivery)) {
      throw new APIError("The service returned a changed immutable delivery. No replacement details were loaded.", "invalid-response", false);
    }
    original.current = response.delivery;
    return response;
  }, [id, findingId, initial, checks]);
  const resource = useResource(load);
  const value = resource.data?.delivery;
  return <section className="surface slack-panel" aria-label="Selected delivery">
    <header className="slack-panel-heading"><h3>Selected delivery</h3>
      <ActionButton variant="outline" disabled={resource.status === "loading"} onClick={() => setChecks((count) => count + 1)}>
        <Icon name="refresh" />Refresh delivery</ActionButton></header>
    <div className="slack-panel-content">
      <FormError error={resource.error} />
      {resource.error && <>
        <p className="form-help">Request an authorized detail read. Refreshing never enqueues or dispatches a notification.</p>
        <ActionButton variant="outline" onClick={() => setChecks((count) => count + 1)}>Retry delivery</ActionButton>
      </>}
      {resource.status === "loading" && !value && <LoadingState label="Loading delivery details" />}
      {value && <>
        {resource.data?.dataOrigin && <DataNotice origin={resource.data.dataOrigin} />}
        <p className="form-help"><code>{value.id}</code></p>
        <div className={`slack-delivery-state slack-state-${value.state}`} role="status" aria-label="Delivery status">
          <strong>{label(value.state)}</strong>{" "}
          <p>{value.state === "confirmed" && resource.data?.dataOrigin === "synthetic"
            ? "The synthetic API records a confirmed receipt. This view has not verified a live Slack send or resolved the finding."
            : explanations[value.state]}</p>
          {value.failure && <>
            <p>Failure code: <code>{value.failure.code}</code></p>
            <p>Native code: {value.failure.nativeCode ? <code>{value.failure.nativeCode}</code> : "Not supplied"}</p>
            <p>HTTP status: {value.failure.httpStatus}.</p>
            <p>Retry-After: {value.failure.retryAfterSeconds} seconds. Retryable: false. No automatic retries.</p>
          </>}
        </div>
        <p className="form-help">{resource.status === "loading" ? "Refreshing. Showing the last received server state." :
          resource.error ? "Refresh failed. The last received state has not been refreshed." :
            "Last received server state. Use Refresh delivery to read progress; there is no automatic polling."}</p>
        <div className="slack-payload"><h3>{value.payload.title}</h3><p>{value.payload.body}</p>
          <a className="slack-link" href={value.payload.deepLink}>View finding</a>
          <p className="form-help">Immutable notification snapshot from the service. Its finding link may use the operator's configured origin.</p>
        </div>
        <dl className="slack-facts">
          <div><dt>Channel</dt><dd><code>{value.channel}</code></dd></div>
          <div><dt>Connection revision</dt><dd>{value.connectionRevision}</dd></div>
          <div><dt>Connection</dt><dd><code>{value.connectionId}</code></dd></div>
          <div><dt>Requested by</dt><dd><code>{value.requestedBy}</code></dd></div>
          <div><dt>Created</dt><dd><time dateTime={value.createdAt}>{timestampLabel(value.createdAt)}</time></dd></div>
          {value.dispatchStartedAt !== null && <div><dt>Dispatch started</dt><dd><time dateTime={value.dispatchStartedAt}>{timestampLabel(value.dispatchStartedAt)}</time></dd></div>}
          {value.completedAt !== null && <div><dt>Completed</dt><dd><time dateTime={value.completedAt}>{timestampLabel(value.completedAt)}</time></dd></div>}
          {value.receipt && <div className="full-width"><dt>Native receipt</dt><dd><code>{value.receipt.remoteId || "No remote identifier supplied"}</code></dd></div>}
        </dl>
        {value.receipt && (value.receipt.remoteUrl
          ? <a className="slack-link" href={value.receipt.remoteUrl} rel="noreferrer">View Slack receipt</a>
          : <p className="form-help">No remote URL was supplied. No permalink was constructed or looked up.</p>)}
      </>}
    </div>
  </section>;
}
