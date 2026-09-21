import { useCallback, useLayoutEffect, useRef, useState } from "react";
import type { FindingDetail } from "@/api/types";
import type { APIError } from "@/api/client";
import type { Assessment } from "@/api/assessment-types";
import { pendingAssessment } from "@/api/assessment-intents";
import { useSession } from "@/lib/session";
import { useAIData } from "../ai-settings/use-ai-data";
import { ActionButton } from "../action-button";
import { FormError } from "../form-dialog";
import { Button } from "../ui/button";
import { AssessmentDraft } from "./draft";
import { AssessmentHistory, useAssessmentHistory } from "./history";
import { AssessmentDetail } from "./detail";
import { AdvisoryNotice } from "./facts";
import "./finding-assessments.css";

function AssessmentPane({ finding, onClose }: { finding: FindingDetail; onClose: () => void }) {
  const { session, workspace } = useSession();
  const data = useAIData(workspace.id);
  const history = useAssessmentHistory(finding.id);
  const [draft, setDraft] = useState(false);
  const [selection, setSelection] = useState<{ id: string; sequence: number } | null>(null);
  const [denied, setDenied] = useState<APIError | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const [, intentChanged] = useState(0);
  const unresolved = pendingAssessment(workspace.id, session.user.id, finding.id) !== null;
  const canWrite = (workspace.role === "admin" || workspace.role === "analyst") && denied === null;
  const configurationError = data.profilePage.error ?? data.policyRead.error ?? data.grantPage.error;
  const configurationPending = data.profilePage.pending || data.policyRead.status === "loading" || data.grantPage.pending;
  const configurationReady = !configurationPending && configurationError === null && data.policy !== null;
  const onDenied = useCallback((error: APIError) => { setDenied(error); setDraft(false); }, []);
  const onPending = useCallback(() => intentChanged((value) => value + 1), []);
  useLayoutEffect(() => {
    if (history.error && ["forbidden", "not-found", "invalid-response"].includes(history.error.code)) {
      setSelection(null); setDraft(false); setNotice(null);
    }
    if (!canWrite) setDraft(false);
  }, [history.error, canWrite]);
  function refreshConfiguration() {
    data.profilePage.refresh(); data.policyRead.reload(); data.grantPage.refresh();
  }
  function retryConfiguration() {
    if (data.profilePage.error) data.profilePage.retry();
    if (data.policyRead.error) data.policyRead.reload();
    if (data.grantPage.error) data.grantPage.retry();
  }
  function queued(receipt: Assessment) {
    const accepted = history.accept(receipt);
    setDraft(false);
    setNotice(accepted ? `Assessment ${receipt.id}: ${receipt.state}. This is the canonical receipt, not a claim of completed inference or finding verification.` : null);
  }
  return <section className="finding-assessments" aria-label="AI assessments">
    <header className="assessment-heading"><h3>AI assessments</h3>
      <Button type="button" variant="outline" onClick={onClose}>Close AI assessments</Button></header>
    <AdvisoryNotice />
    <FormError error={denied} />
    {!canWrite && <p className="form-help">Read only. Only a currently authorized workspace admin or analyst may prepare, queue or cancel.
      {" "}Cached roles never override a service denial.</p>}
    <AssessmentHistory data={history} onSelect={(id) => setSelection((previous) => ({ id, sequence: (previous?.sequence ?? 0) + 1 }))} />
    {selection && <AssessmentDetail key={`${selection.id}:${selection.sequence}`} id={selection.id} findingId={finding.id}
      canWrite={canWrite} onDenied={onDenied} onReceipt={history.accept} onClose={() => setSelection(null)} />}
    {unresolved && <p className="form-help" role="status">A queue acknowledgement remains unresolved for this workspace, requester and finding.
      {" "}It may already be queued. New assessment is blocked until authorized history returns its matching preview and idempotency receipt.
      {" "}A failed, denied or incomplete page, or closing this pane, is not reconciliation or rollback. No blind retry or fresh-key duplicate is offered.</p>}
    {notice && <p role="status" className="form-help">{notice}</p>}
    <section className="assessment-configuration" aria-label="Assessment configuration">
      <h4>Assessment configuration</h4>
      <div className="assessment-actions"><ActionButton variant="outline" disabled={configurationPending}
        onClick={refreshConfiguration}>Refresh assessment configuration</ActionButton>
        {configurationError && <ActionButton variant="outline" disabled={configurationPending}
          onClick={retryConfiguration}>Retry assessment configuration</ActionButton>}</div>
      <FormError error={configurationError} />
      {configurationPending && <p role="status" className="form-help">Loading authorized assessment configuration...</p>}
      {data.policyRead.status === "ready" && data.policy && <p role="status" aria-label="Current assessment policy" className="form-help">
        Current assessment policy: <strong>{data.policy.mode}</strong>. Revision: {data.policy.revision}.
      </p>}
      {data.policyRead.status === "ready" && data.policy?.mode === "disabled" && <p className="form-help">Assessment policy is disabled.
        {" "}Read-only history remains available; no preview or queue is authorized.</p>}
      <p className="form-help">Stored profiles and credential presence are configuration, not tested access or permission to execute.
        {" "}Only explicit preview and queue confirmations can request an assessment. Current server authority is checked again on writes.</p>
      {data.profilePage.page?.nextCursor && <ActionButton variant="outline" disabled={configurationPending}
        onClick={data.profilePage.more}>Load more assessment profiles</ActionButton>}
      {data.grantPage.page?.nextCursor && <ActionButton variant="outline" disabled={configurationPending}
        onClick={data.grantPage.more}>Load more assessment grants</ActionButton>}
    </section>
    {canWrite && <ActionButton disabled={draft || unresolved || !history.authorized || history.pending}
      onClick={() => { setDraft(true); setNotice(null); }}>New assessment</ActionButton>}
    {draft && canWrite && <AssessmentDraft finding={finding} configuration={{
      profiles: data.profilePage.rows, grants: data.grantPage.rows, policy: data.policy, ready: configurationReady,
    }} unresolved={unresolved} canWrite={canWrite} onPending={onPending} onReceipt={queued} onDenied={onDenied}
      onDiscard={() => setDraft(false)} />}
  </section>;
}

export function FindingAssessments({ finding }: { finding: FindingDetail }) {
  const [open, setOpen] = useState(false);
  const entry = useRef<HTMLButtonElement>(null);
  function close() { setOpen(false); entry.current?.focus({ preventScroll: true }); }
  return <div className="assessment-entry">
    <Button ref={entry} type="button" variant="outline" aria-expanded={open}
      onClick={() => { if (open) close(); else setOpen(true); }}>AI assessments</Button>
    {open && <AssessmentPane finding={finding} onClose={close} />}
  </div>;
}
