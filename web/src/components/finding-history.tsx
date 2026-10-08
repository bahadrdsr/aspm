import { memo, useState } from "react";
import type { FindingDecisionEvent, FindingNote, Observation } from "@/api/types";
import { api } from "@/api/client";
import { label, timestampLabel } from "@/lib/format";
import { useScopedAction } from "@/lib/use-scoped-action";
import { ActionButton } from "./action-button";
import { FormError } from "./form-dialog";
import "./finding-history.css";

const ObservationEntry = memo(function ObservationEntry({ observation, canRestore, onChanged }: {
  observation: Observation;
  canRestore: boolean;
  onChanged: () => void;
}) {
  const location = observation.sourceLocation;
  const evidence = useScopedAction();
  const restore = useScopedAction();
  const refresh = useScopedAction();
  const [evidenceText, setEvidenceText] = useState<string | null>(null);
  const [rationale, setRationale] = useState("");
  const [intent] = useState(() => crypto.randomUUID());
  const [run, setRun] = useState<Awaited<ReturnType<typeof api.restoreObservation>>["retentionRun"] | null>(null);
  function readEvidence() {
    void evidence.run((signal) => api.observationEvidence(observation.id, signal), (bytes) => {
      const text = new TextDecoder("utf-8", { fatal: true }).decode(bytes);
      setEvidenceText(JSON.stringify(JSON.parse(text), null, 2));
    });
  }
  function queueRestore() {
    void restore.run((signal) => api.restoreObservation(observation.id, {
      rationale, idempotencyKey: intent,
    }, signal), (response) => setRun(response.retentionRun));
  }
  function refreshRun() {
    if (!run) return;
    void refresh.run((signal) => api.retentionRun(run.id, signal), (response) => {
      setRun(response.retentionRun);
      if (response.retentionRun.state === "succeeded") onChanged();
    });
  }
  return <li>
    <h4>{observation.scanId}</h4>
    <p>{`Source ID: ${observation.sourceId}\nRun ID: ${observation.runId}\nSource finding ID: ${observation.sourceFindingId}\nLocation: ${
      location.uri ? `${location.uri}${location.line > 0 ? `:${location.line}` : ""}` : "Not supplied"
    }\nEvidence digest: ${observation.evidenceDigest}\nImpact: ${observation.impact || "Not supplied"}\nRemediation: ${observation.remediation || "Not supplied"}`}</p>
    <dl>
      <dt>Source severity</dt><dd>{observation.sourceSeverity || "Not supplied"}</dd>
      <dt>Normalized severity</dt><dd>{label(observation.normalizedSeverity)}</dd>
      <dt>Source scan</dt><dd>{observation.sourceScanAt === null ? "Unknown source time" :
        <time dateTime={observation.sourceScanAt}>{timestampLabel(observation.sourceScanAt)}</time>}</dd>
      <dt>Scope</dt><dd>{observation.scope.id}</dd>
      <dt>Revision / branch</dt><dd>{`${observation.scope.revision} / ${observation.scope.branch}`}</dd>
      <dt>Evidence availability</dt><dd>{label(observation.evidenceAvailability)}</dd>
      <dt>Source change</dt><dd>{label(observation.changeKind)}</dd>
      <dt>Change context</dt><dd>{observation.changeReasons.length === 0
        ? "Current authoritative observation" : observation.changeReasons.map(label).join(", ")}</dd>
    </dl>
    <div className="finding-owner-actions">
      <ActionButton variant="outline" disabled={evidence.pending || observation.evidenceAvailability === "expired"}
        onClick={readEvidence}>{observation.evidenceAvailability === "archived" ? "Retrieve archived observation" : "Read normalized observation"}</ActionButton>
    </div>
    <FormError error={evidence.error} />
    {evidenceText !== null && <details open><summary>Normalized observation evidence</summary>
      <pre>{evidenceText}</pre></details>}
    {canRestore && observation.evidenceAvailability === "archived" && <div className="finding-note-form">
      <label>Restoration rationale<textarea rows={2} maxLength={8192} value={rationale}
        onChange={(event) => setRationale(event.target.value)} /></label>
      <ActionButton variant="outline" disabled={restore.pending || !rationale.trim()}
        onClick={queueRestore}>Queue observation restoration</ActionButton>
      <FormError error={restore.error} />
    </div>}
    {run && <div role="status"><p>Restoration {label(run.state)}. {run.succeeded} of {run.total} items succeeded.</p>
      <ActionButton variant="outline" disabled={refresh.pending} onClick={refreshRun}>Refresh restoration</ActionButton>
      <FormError error={refresh.error} /></div>}
    <h5>Unmapped source context</h5>
    {Object.keys(observation.unmapped).length > 0 ? <pre>{JSON.stringify(observation.unmapped, null, 2)}</pre> : <p>No unmapped fields were supplied.</p>}
  </li>;
});

export const FindingObservationHistory = memo(function FindingObservationHistory({ observations, canRestore = false, onChanged = () => {} }: {
  observations: readonly Observation[];
  canRestore?: boolean;
  onChanged?: () => void;
}) {
  return <ol className="observations-list finding-history-observations" aria-label="Observations" tabIndex={0}>
    {observations.map((observation) => <ObservationEntry key={observation.id} observation={observation}
      canRestore={canRestore} onChanged={onChanged} />)}
  </ol>;
});

export const FindingNoteHistory = memo(function FindingNoteHistory({ notes }: { notes: readonly FindingNote[] }) {
  return <ul className="analyst-notes finding-history-notes" aria-label="Analyst notes" tabIndex={0}>
    {notes.map((note) => <li key={note.id}>{note.text}</li>)}
  </ul>;
});

function ownerLabel(value: string | null): string {
  return value ?? "Unassigned";
}

export const FindingDecisionHistory = memo(function FindingDecisionHistory({ events }: {
  events: readonly FindingDecisionEvent[];
}) {
  return <ol className="observations-list finding-history-decisions" aria-label="Decision history" tabIndex={0}>
    {events.map((event) => <li key={event.id}>
      <h4>{event.action === "bulk-update" ? "Bulk triage" : "Finding update"} by {event.actorName}</h4>
      <p>{event.rationale || "No rationale was supplied for this update."}</p>
      <dl>
        <dt>Revision</dt><dd>{event.decisionRevision}</dd>
        <dt>Changed</dt><dd>{event.changedFields.length === 0 ? "No decision fields" : event.changedFields.map(label).join(", ")}</dd>
        <dt>Workflow change</dt><dd>{label(event.before.workflowState)} to {label(event.after.workflowState)}</dd>
        <dt>Owner change</dt><dd>{ownerLabel(event.beforeOwnerName)} to {ownerLabel(event.afterOwnerName)}</dd>
        <dt>Disposition change</dt><dd>{label(event.before.disposition)} to {label(event.after.disposition)}</dd>
        <dt>Recorded</dt><dd><time dateTime={event.createdAt}>{timestampLabel(event.createdAt)}</time></dd>
      </dl>
    </li>)}
  </ol>;
});
