import { useCallback, useLayoutEffect, useRef, useState } from "react";
import { APIError } from "@/api/client";
import { requestAuthority } from "@/api/authorization";
import { assessmentsApi, sameAssessmentSnapshot } from "@/api/assessments";
import type { Assessment } from "@/api/assessment-types";
import { useResource } from "@/lib/use-resource";
import { useScopedAction } from "@/lib/use-scoped-action";
import { ActionButton } from "../action-button";
import { FormError } from "../form-dialog";
import { Button } from "../ui/button";
import { AdvisoryNotice, ApprovedContext, AssessmentBindingFacts, AssessmentFact, AssessmentTime } from "./facts";

const explanations = {
  queued: "Queued with the service, not a completed assessment. No provider response is confirmed.",
  dispatching: "A single dispatch has started. Context may already have been sent; its outcome is not confirmed.",
  succeeded: "The service returned a structured advisory about this exact approved snapshot, not an independent finding decision.",
  failed: "Assessment failed. A bounded service diagnostic is not a successful inconclusive advisory. No automatic retry.",
  cancelled: "The service records cancellation. This cannot undo context that may already have been sent and is not a refund.",
  invalidated: "The service invalidated this assessment. Prior approval does not override current authority.",
  uncertain: "The outcome is uncertain and context may already have been sent. Do not resend. No retry or fallback is offered.",
};

export function AssessmentDetail({ id, findingId, canWrite, onDenied, onReceipt, onClose }: {
  id: string; findingId: string; canWrite: boolean; onDenied: (error: APIError) => void;
  onReceipt: (value: Assessment) => boolean; onClose: () => void;
}) {
  const canonical = useRef<Assessment | null>(null);
  const sequence = useRef(0), received = useRef(0);
  const [, changed] = useState(0);
  const [confirming, setConfirming] = useState(false);
  const clear = useCallback(() => { canonical.current = null; received.current = ++sequence.current; }, []);
  useLayoutEffect(() => {
    const signal = requestAuthority().signal;
    signal.addEventListener("abort", clear, { once: true });
    return () => { signal.removeEventListener("abort", clear); clear(); };
  }, [clear]);
  const load = useCallback(async (signal: AbortSignal) => {
    const started = ++sequence.current, scoped = AbortSignal.any([signal, requestAuthority().signal]);
    try {
      const value = await assessmentsApi.detail(id, findingId, scoped);
      scoped.throwIfAborted();
      if (canonical.current && !sameAssessmentSnapshot(canonical.current, value)) {
        throw new APIError("The service changed immutable assessment bindings. Details are withheld; request an authorized read again.", "invalid-response", false);
      }
      if (started >= received.current) { canonical.current = value; received.current = started; }
      return canonical.current;
    } catch (cause) {
      if (!scoped.aborted && (!(cause instanceof APIError) || !["network", "unavailable"].includes(cause.code))) clear();
      throw cause;
    }
  }, [id, findingId, clear]);
  const resource = useResource(load);
  const cancellation = useScopedAction();
  const value = canonical.current;
  const cancellable = value !== null && ["queued", "dispatching"].includes(value.state);
  useLayoutEffect(() => {
    if (!canWrite || !value || resource.error) setConfirming(false);
  }, [canWrite, value, resource.error]);
  function cancel() {
    if (!value || !confirming || !canWrite || !cancellable) return;
    const acceptReceipt = onReceipt;
    void cancellation.run((signal) => assessmentsApi.cancel(value, signal), (receipt) => {
      if (!acceptReceipt(receipt)) {
        clear(); changed(sequence.current); setConfirming(false);
        return;
      }
      canonical.current = receipt;
      received.current = ++sequence.current;
      changed(received.current);
      setConfirming(false);
    }, (error) => {
      setConfirming(false);
      if (error.code === "forbidden") onDenied(error);
      if (error.code === "not-found") { clear(); changed(sequence.current); }
    });
  }
  const busy = resource.status === "loading" || cancellation.pending;
  return <section className="surface assessment-section" aria-label="Assessment details">
    <header className="assessment-heading"><h3>Assessment details</h3><div className="assessment-actions">
      <ActionButton variant="outline" disabled={busy} onClick={resource.reload}>
        {resource.error ? "Retry assessment" : "Refresh assessment"}</ActionButton>
      <Button type="button" variant="ghost" onClick={onClose}>Close assessment details</Button>
    </div></header>
    <div className="assessment-content">
      <FormError error={resource.error} />
      <FormError error={cancellation.error?.code === "forbidden" ? null : cancellation.error} />
      {resource.status === "loading" && <p role="status" className="form-help">Loading assessment details. No new assessment is started.</p>}
      {!value && resource.error && <p className="form-help">Protected details remain withheld until an authorized detail read succeeds.</p>}
      {value && <>
        <p className="form-help"><code>{value.id}</code></p>
        <dl className="assessment-facts">
          <AssessmentFact name="State">{value.state}</AssessmentFact>
          <AssessmentFact name="Dispatch state">{value.dispatchState}</AssessmentFact>
          <AssessmentFact name="Attempts">{value.attempts}</AssessmentFact>
          <AssessmentFact name="Advisory only">true</AssessmentFact>
          <AssessmentFact name="Preview">{value.previewId}</AssessmentFact>
          <AssessmentFact name="Worker scope">{value.scope}</AssessmentFact>
          <AssessmentFact name="Created"><AssessmentTime value={value.createdAt} /></AssessmentFact>
          <AssessmentFact name="Consent expires at"><AssessmentTime value={value.consentExpiresAt} /></AssessmentFact>
          <AssessmentFact name="Dispatch started">{value.dispatchStartedAt ? <AssessmentTime value={value.dispatchStartedAt} /> : "Not started"}</AssessmentFact>
          <AssessmentFact name="Completed">{value.completedAt ? <AssessmentTime value={value.completedAt} /> : "Not completed"}</AssessmentFact>
        </dl>
        <p className="form-help">{explanations[value.state]}</p>
        {value.dispatchState === "possibly-sent" && <p className="form-help">Context may already have been sent. Closing, aborting or cancellation is not a rollback.</p>}
        <AdvisoryNotice />
        <AssessmentBindingFacts value={value} />
        <ApprovedContext context={value.context} />
        {value.result && <section className="assessment-result" aria-label="Advisory result">
          <h4>Advisory result</h4><p>{value.result.conclusion}</p><p className="assessment-literal">{value.result.uncertainty}</p>
          <h5>Approved context references</h5><ul>{value.result.evidenceRefs.map((ref, index) => <li key={`${ref}:${index}`}><code>{ref}</code></li>)}</ul>
          <AdvisoryNotice />
        </section>}
        {value.failure && <section aria-label="Assessment failure" className="assessment-result">
          <h4>Assessment failure</h4><p>{value.failure.code}</p><p className="assessment-literal">{value.failure.message}</p>
          <p className="form-help">Bounded service diagnostic. Retryable: false. This is not an inconclusive model result.</p>
        </section>}
        <dl className="assessment-facts">
          <AssessmentFact name="Requested model">{value.requestedModel || "Not supplied"}</AssessmentFact>
          <AssessmentFact name="Returned model">{value.returnedModel || "Not supplied"}</AssessmentFact>
          <AssessmentFact name="Request ID">{value.requestId || "Not supplied"}</AssessmentFact>
          <AssessmentFact name="Stop reason">{value.stopReason || "Not supplied"}</AssessmentFact>
          <AssessmentFact name="Usage known">{String(value.usage.known)}</AssessmentFact>
          <AssessmentFact name="Input tokens">{value.usage.known ? value.usage.inputTokens : "Unknown"}</AssessmentFact>
          <AssessmentFact name="Output tokens">{value.usage.known ? value.usage.outputTokens : "Unknown"}</AssessmentFact>
          <AssessmentFact name="Cached input tokens">{value.usage.known ? value.usage.cachedInputTokens : "Unknown"}</AssessmentFact>
          <AssessmentFact name="Cache write tokens">{value.usage.known ? value.usage.cacheWriteTokens : "Unknown"}</AssessmentFact>
          <AssessmentFact name="Retry hint">{value.retryAfterMillis} milliseconds; no automatic retry</AssessmentFact>
        </dl>
        <p className="form-help">Unknown usage is not a zero-use or cost claim. Returned model metadata is an observation, not an immutable model certificate.
          {" "}Requested model and Foundry deployment are separate fields. Refresh manually for current server state.</p>
        {canWrite && cancellable && <ActionButton variant="outline" disabled={busy || resource.error !== null}
          onClick={() => setConfirming(true)}>Cancel assessment</ActionButton>}
        {canWrite && confirming && <section aria-label="Cancel assessment confirmation" className="assessment-result">
          <h4>Cancel assessment?</h4>
          <p className="form-help">Only a canonical service receipt confirms cancellation. Context may already have been sent.
            {" "}This cannot undo a dispatch, guarantee that data was unsent, or promise a refund.</p>
          <div className="assessment-actions"><Button type="button" variant="outline" disabled={cancellation.pending}
            onClick={() => setConfirming(false)}>Keep assessment</Button>
            <ActionButton disabled={busy} onClick={cancel}>Confirm cancellation</ActionButton></div>
        </section>}
        {cancellation.pending && <p className="form-help" role="status">Requesting cancellation. Showing the last confirmed server state, not an optimistic cancellation.</p>}
      </>}
    </div>
  </section>;
}
