import { useCallback, useRef, useState } from "react";
import { APIError } from "@/api/client";
import { jiraApi } from "@/api/jira";
import { sameJiraPayload, sameJiraTarget } from "@/api/jira-input";
import type { JiraDelivery, JiraDeliveryResponse } from "@/api/jira-types";
import { useResource } from "@/lib/use-resource";
import { label, timestampLabel } from "@/lib/format";
import { ActionButton } from "./action-button";
import { FormError } from "./form-dialog";
import { JiraPayloadView, JiraTargetFacts } from "./jira-facts";
import { LoadingState } from "./states";

function immutable(a: JiraDelivery, b: JiraDelivery) {
  return a.id === b.id && a.workspaceId === b.workspaceId && a.findingId === b.findingId && a.connectionId === b.connectionId &&
    a.connectionRevision === b.connectionRevision && a.profile === b.profile && a.requestedBy === b.requestedBy &&
    a.createdAt === b.createdAt && sameJiraTarget(a.jira, b.jira) && sameJiraPayload(a.payload, b.payload);
}
const meanings = {
  queued: "Queued, not yet created. Waiting for the independent worker.",
  dispatching: "Dispatching. The worker has started; Jira creation is not confirmed.",
  confirmed: "The service reports a confirmed native issue key. This does not verify a Jira account or its permissions, or resolve the finding.",
  accepted: "Accepted only. This is not native confirmation or proof that an issue was created.",
  blocked: "Blocked by the service. No automatic retries.",
  failed: "Failed according to the service. No automatic retries.",
  "rate-limited": "Rate-limited. Retry-After is data, not an automatic retry timer.",
  uncertain: "Uncertain: the request may have been sent and an issue possibly created. Do not retry or resend.",
};

export function JiraDeliveryDetail({ id, findingId, initial, replay = false }: {
  id: string; findingId: string; initial: JiraDeliveryResponse | null; replay?: boolean;
}) {
  const [checks, setChecks] = useState(0);
  const original = useRef<JiraDelivery | null>(initial?.delivery ?? null);
  const load = useCallback(async (signal: AbortSignal) => {
    const result = initial !== null && checks === 0 ? initial : await jiraApi.delivery(id, findingId, signal);
    signal.throwIfAborted();
    if (original.current && !immutable(original.current, result.delivery)) {
      throw new APIError("The service returned invalid changed immutable Jira details. Their fields are withheld.", "invalid-response", false);
    }
    original.current = result.delivery;
    return result;
  }, [id, findingId, initial, checks]);
  const resource = useResource(load), value = resource.data?.delivery;
  return <section className="surface slack-panel" aria-label="Jira work item details">
    <header className="slack-panel-heading"><h3>Jira work item details</h3>
      {!resource.error && <ActionButton variant="outline" disabled={resource.status === "loading"}
        onClick={() => setChecks((count) => count + 1)}>Refresh Jira work item</ActionButton>}</header>
    <div className="slack-panel-content">
      <FormError error={resource.error} />
      {resource.error && <ActionButton variant="outline" onClick={() => setChecks((count) => count + 1)}>Retry Jira work item</ActionButton>}
      {resource.status === "loading" && !value && <LoadingState label="Loading Jira work item details" />}
      {value && <>
        <p className="form-help"><code>{value.id}</code></p>
        {replay && <p className="form-help">Same original intent replay, not a newly queued work item.</p>}
        <div className={`slack-delivery-state slack-state-${value.state}`} role="status" aria-label="Jira work item status">
          <strong>{label(value.state)}</strong><p>{meanings[value.state]}</p>
          {value.failure && <>
            <p>Failure code: <code>{value.failure.code}</code></p>
            {value.failure.nativeCode && <p>Native code: <code>{value.failure.nativeCode}</code></p>}
            <p>HTTP status: {value.failure.httpStatus}.</p>
            <p>Stage: {value.failure.stage ?? "not supplied"}</p>
            {value.failure.missingFields && <p>Missing fields: {value.failure.missingFields.join(", ")}</p>}
            <p>Retry-After: {value.failure.retryAfterSeconds} seconds. Retryable: false. No automatic retries.</p>
          </>}
          {value.createAttemptedAt === null
            ? <p>No native create POST was attempted in this received state. Metadata checks are not issue creation.</p>
            : <p>A native create attempt is recorded. A create marker alone is not confirmation or a safe-to-resend claim.</p>}
        </div>
        <JiraPayloadView payload={value.payload} /><JiraTargetFacts target={value.jira} revision={value.connectionRevision} />
        <dl className="slack-facts">
          <div><dt>Connection</dt><dd><code>{value.connectionId}</code></dd></div>
          <div><dt>Requested by</dt><dd><code>{value.requestedBy}</code></dd></div>
          <div><dt>Queued at</dt><dd><time dateTime={value.createdAt}>{timestampLabel(value.createdAt)}</time></dd></div>
          {value.dispatchStartedAt && <div><dt>Dispatch started</dt><dd><time dateTime={value.dispatchStartedAt}>{timestampLabel(value.dispatchStartedAt)}</time></dd></div>}
          {value.createAttemptedAt && <div><dt>Create attempted</dt><dd><time dateTime={value.createAttemptedAt}>{timestampLabel(value.createAttemptedAt)}</time></dd></div>}
          {value.completedAt && <div><dt>Completed</dt><dd><time dateTime={value.completedAt}>{timestampLabel(value.completedAt)}</time></dd></div>}
        </dl>
        {value.receipt && <p className="form-help">Native receipt: <code>{value.receipt.remoteId || "No confirmed key supplied"}</code></p>}
        {value.state === "confirmed" && value.receipt && <a className="slack-link" href={value.receipt.remoteUrl}
          target="_blank" rel="noopener noreferrer">View Jira issue</a>}
        <p className="form-help">Last received server state. Refresh is a manual read only; there is no polling, retry of native creation or automatic navigation.</p>
      </>}
    </div>
  </section>;
}
