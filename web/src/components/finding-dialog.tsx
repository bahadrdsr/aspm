import { useCallback, useRef } from "react";
import { createPortal } from "react-dom";
import { motion } from "motion/react";
import { api } from "@/api/client";
import type { Observation } from "@/api/types";
import { useResource } from "@/lib/use-resource";
import { usePreferences } from "@/lib/preferences";
import { label, timestampLabel } from "@/lib/format";
import { useModal } from "@/lib/use-modal";
import { Button } from "./ui/button";
import { DataNotice, ErrorState, LoadingState, SeverityBadge, WorkflowBadge } from "./states";
import { Icon } from "./icon";

function DetailTime({ value }: { value: string | null | undefined }) {
  if (value === undefined) return <>Not supplied</>;
  return value === null ? <>Unknown source time</> : <time dateTime={value}>{timestampLabel(value)}</time>;
}

function ObservationEntry({ observation }: { observation: Observation }) {
  const location = observation.sourceLocation;
  return <li className="observation-entry">
    <div className="section-heading"><h4>{observation.scanId}</h4><span className="subtle-pill">Source observation</span></div>
    <dl className="observation-facts">
      <div><dt>Source ID</dt><dd><code>{observation.sourceId}</code></dd></div>
      <div><dt>Run ID</dt><dd><code>{observation.runId}</code></dd></div>
      <div><dt>Source finding ID</dt><dd><code>{observation.sourceFindingId}</code></dd></div>
      <div><dt>Location</dt><dd><code>{location.uri ? `${location.uri}${location.line > 0 ? `:${location.line}` : ""}` : "Not supplied"}</code></dd></div>
      <div><dt>Source severity</dt><dd>{observation.sourceSeverity || "Not supplied"}</dd></div>
      <div><dt>Normalized severity</dt><dd><SeverityBadge severity={observation.normalizedSeverity} /></dd></div>
      <div><dt>Source scan</dt><dd><DetailTime value={observation.sourceScanAt} /></dd></div>
      <div><dt>Scope</dt><dd>{observation.scope.id}</dd></div>
      <div><dt>Revision / branch</dt><dd>{observation.scope.revision} / {observation.scope.branch}</dd></div>
      <div><dt>Evidence digest</dt><dd><code>{observation.evidenceDigest}</code></dd></div>
    </dl>
    <p><strong>Impact: </strong>{observation.impact || "Not supplied"}</p>
    <p><strong>Remediation: </strong>{observation.remediation || "Not supplied"}</p>
    <h5>Unmapped source context</h5>
    {Object.keys(observation.unmapped).length > 0 ? <pre className="evidence-text">{JSON.stringify(observation.unmapped, null, 2)}</pre> : <p>No unmapped fields were supplied.</p>}
  </li>;
}

export function FindingDialog({ id, initialTitle, returnFocus, onClose }: {
  id: string; initialTitle: string; returnFocus: HTMLElement | null; onClose: () => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const closeButton = useRef<HTMLButtonElement>(null);
  const load = useCallback((signal: AbortSignal) => api.finding(id, signal), [id]);
  const resource = useResource(load);
  const { reducedMotion } = usePreferences();
  useModal(dialog, closeButton, returnFocus, "work-heading");
  const close = () => {
    dialog.current?.close();
    onClose();
  };
  const finding = resource.data?.finding;
  const title = finding?.title ?? initialTitle;
  return createPortal(<dialog ref={dialog} className="finding-dialog" aria-labelledby="finding-dialog-title" aria-describedby="finding-dialog-purpose" onCancel={(event) => { event.preventDefault(); close(); }}>
    <motion.div className="dialog-surface" initial={reducedMotion ? false : { opacity: 0, x: 14 }} animate={{ opacity: 1, x: 0 }} transition={{ duration: 0.22 }}>
      <header className="dialog-topbar"><span><Icon name="file" size={17} />Finding evidence</span><Button ref={closeButton} type="button" variant="ghost" size="icon" aria-label="Close finding details" onClick={close}><Icon name="close" /></Button></header>
      <div className="dialog-heading"><p id="finding-dialog-purpose" className="eyebrow">Read-only source context</p><h2 id="finding-dialog-title">{title}</h2>{resource.data && <DataNotice origin={resource.data.dataOrigin} />}</div>
      <div className="dialog-content">
        {resource.error && <ErrorState error={resource.error} retry={resource.reload} stale={finding !== undefined} />}
        {resource.status === "loading" && !finding && <LoadingState label="Loading finding evidence" />}
        {finding && <>
          <div className="finding-status-line"><SeverityBadge severity={finding.severity} /><WorkflowBadge value={finding.workflowState} /><span className="badge neutral"><Icon name="shield" size={13} />{finding.evidence.verificationState === "verified" && resource.data?.dataOrigin === "live" ? "Source reports verified" : finding.evidence.verificationState === "blocked" ? "Verification blocked" : "Not verified"}</span></div>
          <dl className="detail-facts"><div><dt>Asset</dt><dd><Icon name="code" size={15} />{finding.assetName}</dd></div><div><dt>Owner</dt><dd><Icon name="user" size={15} />{finding.ownerName ?? "Unassigned"}</dd></div><div className="full-width"><dt>Scope</dt><dd>{finding.scopeLabel}</dd></div></dl>
          <section className="detail-section"><h3>What the source observed</h3><p>{finding.description || "The source did not supply a description."}</p></section>
          <section className="detail-section"><div className="section-heading"><h3>Original evidence</h3><span className="subtle-pill">Literal text</span></div><p className="evidence-source"><Icon name="file" size={14} />{finding.evidence.sourceLabel}</p><pre className="evidence-text">{finding.evidence.text || "The source did not supply evidence text."}</pre></section>
          <section className="detail-section"><h3>Source remediation context</h3><p>{finding.remediation || "No remediation guidance was supplied by the source."}</p><p className="section-note">Source text is evidence to review, not an instruction to execute.</p></section>
          <section className="detail-section"><h3>Freshness &amp; provenance</h3><dl className="timeline">
            <div><dt><span className="timeline-dot" />Source scan</dt><dd><DetailTime value={finding.sourceScanAt} /></dd></div>
            <div><dt><span className="timeline-dot" />Source freshness</dt><dd><DetailTime value={finding.sourceFreshnessAt} /></dd></div>
            <div><dt><span className="timeline-dot" />Collected</dt><dd><DetailTime value={finding.collectedAt} /></dd></div>
            <div><dt><span className="timeline-dot" />Imported</dt><dd><DetailTime value={finding.importedAt} /></dd></div>
          </dl><p className="section-note">Collection and import never substitute for an unknown source scan time. Source freshness reflects comparable scan coverage, not a human decision.</p></section>
          <section className="detail-section"><h3>Analyst decisions &amp; source state</h3>
            <dl className="detail-facts">
              <div><dt>Human workflow</dt><dd><WorkflowBadge value={finding.workflowState} /></dd></div>
              <div><dt>Disposition</dt><dd>{finding.disposition === undefined ? "Not supplied" : label(finding.disposition)}</dd></div>
              <div className="full-width"><dt>Scanner-inferred state</dt><dd>{finding.sourceState === undefined ? "Not supplied" : label(finding.sourceState)}</dd></div>
              {finding.disposition === "accepted-risk" && <>
                <div className="full-width"><dt>Risk acceptance expiry</dt><dd>{finding.acceptedRiskExpiresAt ? <time dateTime={finding.acceptedRiskExpiresAt}>{timestampLabel(finding.acceptedRiskExpiresAt)}</time> : "No expiry supplied"}</dd></div>
                <div className="full-width"><dt>Expiry state from service</dt><dd>{finding.riskAcceptanceExpired === undefined ? "Not supplied" : finding.riskAcceptanceExpired ? "Expired" : "Not expired"}</dd></div>
              </>}
            </dl>
            {finding.sourceState === "inferred-resolved" && <p className="section-note">Source absence supports an inferred resolution only. It does not change the analyst's workflow, risk acceptance or verification.</p>}
            <p className="section-note">{finding.verifiedResolution === true ? "The service records an independently verified resolution." : finding.verifiedResolution === false ? "No independently verified resolution is recorded." : "Independent resolution verification was not supplied."}</p>
          </section>
          <section className="detail-section"><h3>Analyst notes</h3>
            {finding.notes && finding.notes.length > 0 ? <ul className="analyst-notes" aria-label="Analyst notes">{finding.notes.map((note) => <li key={note.id}><p>{note.text}</p></li>)}</ul> : <p>{finding.notes === undefined ? "Analyst notes were not supplied." : "No analyst notes in this response."}</p>}
            {finding.notesNextCursor && <p className="section-note">More notes exist on the service. This view shows the returned page only.</p>}
          </section>
          <section className="detail-section"><div className="section-heading"><h3>Observations</h3>{finding.observations && <span className="subtle-pill">{finding.observations.length} supplied</span>}</div>
            {finding.observations && finding.observations.length > 0 ? <ol className="observations-list" aria-label="Observations">{finding.observations.map((observation) => <ObservationEntry key={observation.id} observation={observation} />)}</ol> : <p>{finding.observations === undefined ? "Observation history was not supplied." : "No observations in this response."}</p>}
            {finding.observationsNextCursor && <p className="section-note">More observations exist on the service. This view shows the returned page only, not the full history.</p>}
          </section>
        </>}
      </div>
      <footer className="dialog-footer"><span><Icon name="lock" size={15} />No AI or proof execution</span><Button type="button" variant="outline" onClick={close}>Back to work<Icon name="arrow" size={15} /></Button></footer>
    </motion.div>
  </dialog>, document.body);
}
