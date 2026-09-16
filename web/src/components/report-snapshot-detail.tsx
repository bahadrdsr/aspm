import { useCallback, useState } from "react";
import { api } from "@/api/client";
import type { ReportSnapshotResponse } from "@/api/types";
import { label, timestampLabel } from "@/lib/format";
import { useResource } from "@/lib/use-resource";
import { ActionButton } from "./action-button";
import { FormError } from "./form-dialog";
import { Icon } from "./icon";
import { ReportMetrics } from "./report-metrics";
import { DataNotice, LoadingState } from "./states";

const explanations = {
  queued: "Accepted and queued. Waiting for the reporting worker; no report data yet.",
  processing: "The reporting worker is processing this snapshot. No report data is available yet.",
  failed: "The worker could not complete this snapshot. No saved posture metrics are available.",
  succeeded: "The worker completed this saved report. Its metrics and as-of time are immutable.",
};

export function ReportSnapshotDetail({ id, initial }: { id: string; initial: ReportSnapshotResponse | null }) {
  const [checks, setChecks] = useState(0);
  // A 202 is the first receipt only, never a fallback after a denied detail read.
  const load = useCallback((signal: AbortSignal) => initial !== null && checks === 0
    ? Promise.resolve(initial) : api.reportSnapshot(id, signal), [id, initial, checks]);
  const resource = useResource(load);
  const snapshot = resource.data?.snapshot;
  return <section className="surface report-panel report-selected" aria-label="Selected snapshot">
    <header className="report-panel-heading"><h2>Selected snapshot</h2>
      <ActionButton variant="outline" disabled={resource.status === "loading"} onClick={() => setChecks((value) => value + 1)}>
        <Icon name="refresh" />Refresh snapshot</ActionButton></header>
    <div className="report-panel-content">
      <FormError error={resource.error} />
      {resource.status === "loading" && !snapshot && <LoadingState label="Loading snapshot details" />}
      {!snapshot && resource.status === "error" && <p className="report-help">
        {resource.error.code === "forbidden" ? "Access restricted. Ask your administrator to review access. " : ""}
        Snapshot details are unavailable. Refresh to request an authorized response.</p>}
      {snapshot && <>
        <h3 className="report-snapshot-name">{snapshot.name}</h3>
        <p className="report-snapshot-id"><code>{snapshot.id}</code></p>
        <div className={`report-snapshot-state report-state-${snapshot.state}`} role={snapshot.state === "failed" ? "alert" : "status"} aria-label="Snapshot status">
          <strong>{label(snapshot.state)}</strong><p>{explanations[snapshot.state]}</p>
          {snapshot.failure && <p>{snapshot.failure.message} <code>({snapshot.failure.code})</code>
            {snapshot.failure.retryable && <span> The worker has queued a retry.</span>}</p>}
        </div>
        <p className="report-help">{resource.status === "loading" ? "Refreshing. Showing the last received server state." :
          resource.error ? "The refresh failed. Showing only the last received server state; it has not been refreshed." :
            "Last received server state. Use Refresh snapshot to check progress; there is no automatic polling."}</p>
        <dl className="report-snapshot-facts">
          <div><dt>Requested</dt><dd><time dateTime={snapshot.createdAt}>{timestampLabel(snapshot.createdAt)}</time></dd></div>
          <div><dt>Freshness</dt><dd>{snapshot.freshnessDays} days</dd></div>
          {snapshot.completedAt !== null && <div><dt>Completed</dt><dd><time dateTime={snapshot.completedAt}>{timestampLabel(snapshot.completedAt)}</time></dd></div>}
          <div><dt>Requested by</dt><dd><code>{snapshot.requestedBy}</code></dd></div>
        </dl>
        {resource.data && (snapshot.report === null ? <DataNotice origin={resource.data.dataOrigin} /> :
          <ReportMetrics report={snapshot.report} origin={resource.data.dataOrigin} />)}
      </>}
    </div>
  </section>;
}
