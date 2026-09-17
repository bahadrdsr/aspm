import { useRef, useState } from "react";
import type { KeyboardEvent } from "react";
import { createPortal } from "react-dom";
import { motion } from "motion/react";
import type { FindingNote, FindingResponse, WorkItem } from "@/api/types";
import { useFindingHistory } from "@/lib/use-finding-history";
import type { FindingHistoryStream } from "@/lib/use-finding-history";
import { usePreferences } from "@/lib/preferences";
import { label, timestampLabel } from "@/lib/format";
import { useModal } from "@/lib/use-modal";
import { useSession } from "@/lib/session";
import { matchesWorkQuery } from "@/pages/work";
import { FindingActions, FindingNoteForm } from "./finding-actions";
import { FindingNotifications } from "./finding-notifications";
import { FindingNoteHistory, FindingObservationHistory } from "./finding-history";
import { ActionButton } from "./action-button";
import { FormError } from "./form-dialog";
import { Button } from "./ui/button";
import { DataNotice, ErrorState, LoadingState, SeverityBadge, WorkflowBadge } from "./states";
import { Icon } from "./icon";
import "./finding-actions.css";

function DetailTime({ value }: { value: string | null | undefined }) {
  if (value === undefined) return <>Not supplied</>;
  return value === null ? <>Unknown source time</> : <time dateTime={value}>{timestampLabel(value)}</time>;
}

function HistoryControls({ stream, resource }: { stream: FindingHistoryStream; resource: ReturnType<typeof useFindingHistory> }) {
  const page = resource.pages[stream];
  const active = resource.target === stream;
  if (!page.visible && !active) return null;
  return <>
    <div className="finding-owner-actions"><ActionButton variant="outline"
      aria-disabled={resource.pending || page.next == null} onClick={() => resource.loadMore(stream)}>Load more {stream}</ActionButton></div>
    {active && resource.pending && <p role="status" className="section-note">Loading {stream}. Confirmed source facts and both histories remain available.</p>}
    {active && resource.error && <div className="finding-action-feedback"><FormError error={resource.error} />
      <ActionButton variant="outline" onClick={resource.retry}>Retry {stream}</ActionButton></div>}
    <p className="section-note">{page.next ? "More records are available. " : "No continuation in the last returned page. "}
      Counts describe loaded records, not a total or a single history snapshot.</p>
  </>;
}

export function FindingDialog({ id, initialTitle, returnFocus, query, onConfirmed, onClose }: {
  id: string; initialTitle: string; returnFocus: HTMLElement | null; query: string;
  onConfirmed: (finding: WorkItem) => void; onClose: () => void;
}) {
  const dialog = useRef<HTMLDialogElement>(null);
  const closeButton = useRef<HTMLButtonElement>(null);
  const [message, setMessage] = useState<{ target: "decision" | "note"; text: string } | null>(null);
  const resource = useFindingHistory(id);
  const response = resource.data;
  const { workspace } = useSession();
  const canWrite = workspace.role !== "viewer";
  const { reducedMotion } = usePreferences();
  useModal(dialog, closeButton, returnFocus, "work-heading");
  const close = () => {
    dialog.current?.close();
    onClose();
  };
  function acceptPatch(result: FindingResponse, text: string) {
    resource.acceptPatch(result);
    setMessage({ target: "decision", text });
    onConfirmed(result.finding);
  }
  function addNote(note: FindingNote) {
    resource.addNote(note);
    setMessage({ target: "note", text: "Note added." });
  }
  function trapTab(event: KeyboardEvent<HTMLDialogElement>) {
    if (event.key !== "Tab") return;
    const controls = [...event.currentTarget.querySelectorAll<HTMLElement>("button, input, select, textarea, a[href], [tabindex]")]
      .filter((element) => element.tabIndex >= 0 && !element.matches(":disabled") && !element.closest("[inert]") &&
        element.getClientRects().length > 0 && getComputedStyle(element).visibility !== "hidden");
    const first = controls[0], last = controls.at(-1);
    if (first && last && (event.shiftKey ? document.activeElement === first : document.activeElement === last)) {
      event.preventDefault();
      (event.shiftKey ? last : first).focus();
    }
  }
  const finding = response?.finding;
  const title = finding?.title ?? initialTitle;
  return createPortal(<dialog ref={dialog} className="finding-dialog" aria-labelledby="finding-dialog-title" aria-describedby="finding-dialog-purpose"
    onKeyDown={trapTab} onCancel={(event) => { event.preventDefault(); close(); }}>
    <motion.div className="dialog-surface" initial={reducedMotion ? false : { opacity: 0, x: 14 }} animate={{ opacity: 1, x: 0 }} transition={{ duration: 0.22 }}>
      <header className="dialog-topbar"><span><Icon name="file" size={17} />Finding evidence</span><Button ref={closeButton} type="button" variant="ghost" size="icon" aria-label="Close finding details" onClick={close}><Icon name="close" /></Button></header>
      <div className="dialog-heading"><p id="finding-dialog-purpose" className="eyebrow">{canWrite ? "Source context & analyst actions" : "Read-only source context"}</p><h2 id="finding-dialog-title">{title}</h2>{response && <DataNotice origin={response.dataOrigin} />}</div>
      <div className="dialog-content">
        {resource.error && (resource.target === null
          ? <ErrorState error={resource.error} retry={resource.retry} stale={finding !== undefined} />
          : !finding && <div className="finding-action-feedback"><FormError error={resource.error} />
            <ActionButton variant="outline" onClick={resource.retry}>Retry finding history</ActionButton></div>)}
        {resource.status === "loading" && !finding && <LoadingState label="Loading finding evidence" />}
        {finding && <>
          <div className="finding-status-line"><SeverityBadge severity={finding.severity} /><WorkflowBadge value={finding.workflowState} /><span className="badge neutral"><Icon name="shield" size={13} />{finding.evidence.verificationState === "verified" && response?.dataOrigin === "live" ? "Source reports verified" : finding.evidence.verificationState === "blocked" ? "Verification blocked" : "Not verified"}</span></div>
          <dl className="detail-facts"><div><dt>Asset</dt><dd><Icon name="code" size={15} />{finding.assetName}</dd></div><div><dt>Owner</dt><dd><Icon name="user" size={15} />{finding.ownerName ?? "Unassigned"}</dd></div><div className="full-width"><dt>Scope</dt><dd>{finding.scopeLabel}</dd></div></dl>
          {canWrite && <FindingActions finding={finding} message={message?.target === "decision" ? message.text : null}
            outsideFilter={resource.hasPatched && !matchesWorkQuery(finding, query)}
            onBegin={() => setMessage(null)} onConfirmed={acceptPatch} />}
          {response && <FindingNotifications finding={finding} origin={response.dataOrigin} />}
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
          <section className="detail-section"><div className="section-heading"><h3>Analyst notes</h3>{" "}
            {finding.notes && <span className="subtle-pill">{finding.notes.length} loaded</span>}</div>
            {canWrite && <FindingNoteForm id={id} message={message?.target === "note" ? message.text : null}
              onBegin={() => setMessage(null)} onAdded={addNote} />}
            {finding.notes && finding.notes.length > 0 ? <FindingNoteHistory notes={finding.notes} /> : <p>{finding.notes === undefined ? "Analyst notes were not supplied." : "No analyst notes in this response."}</p>}
            <HistoryControls stream="notes" resource={resource} />
          </section>
          <section className="detail-section"><div className="section-heading"><h3>Observations</h3>{" "}{finding.observations && <span className="subtle-pill">{finding.observations.length} loaded</span>}</div>
            {finding.observations && finding.observations.length > 0 ? <FindingObservationHistory observations={finding.observations} /> : <p>{finding.observations === undefined ? "Observation history was not supplied." : "No observations in this response."}</p>}
            <HistoryControls stream="observations" resource={resource} />
          </section>
        </>}
      </div>
      <footer className="dialog-footer"><span><Icon name="lock" size={15} />No AI or proof execution</span><Button type="button" variant="outline" onClick={close}>Back to work<Icon name="arrow" size={15} /></Button></footer>
    </motion.div>
  </dialog>, document.body);
}
