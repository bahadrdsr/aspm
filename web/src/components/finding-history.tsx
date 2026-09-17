import { memo } from "react";
import type { FindingNote, Observation } from "@/api/types";
import { label, timestampLabel } from "@/lib/format";
import "./finding-history.css";

const ObservationEntry = memo(function ObservationEntry({ observation }: { observation: Observation }) {
  const location = observation.sourceLocation;
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
    </dl>
    <h5>Unmapped source context</h5>
    {Object.keys(observation.unmapped).length > 0 ? <pre>{JSON.stringify(observation.unmapped, null, 2)}</pre> : <p>No unmapped fields were supplied.</p>}
  </li>;
});

export const FindingObservationHistory = memo(function FindingObservationHistory({ observations }: { observations: readonly Observation[] }) {
  return <ol className="observations-list finding-history-observations" aria-label="Observations" tabIndex={0}>
    {observations.map((observation) => <ObservationEntry key={observation.id} observation={observation} />)}
  </ol>;
});

export const FindingNoteHistory = memo(function FindingNoteHistory({ notes }: { notes: readonly FindingNote[] }) {
  return <ul className="analyst-notes finding-history-notes" aria-label="Analyst notes" tabIndex={0}>
    {notes.map((note) => <li key={note.id}>{note.text}</li>)}
  </ul>;
});
