import { useMemo, useState } from "react";
import type { FormEvent } from "react";
import { api, APIError } from "@/api/client";
import type {
  FindingCorrelationMember, FindingDecision, FindingDetail, FindingMergePreview,
  FindingResponse, FindingSplitPreview,
} from "@/api/types";
import { useScopedAction } from "@/lib/use-scoped-action";
import { useSession } from "@/lib/session";
import { label, timestampLabel } from "@/lib/format";
import { ActionButton } from "./action-button";
import { FormError } from "./form-dialog";
import { Button } from "./ui/button";

function ownerOptions(...members: FindingCorrelationMember[]) {
  return [...new Set(members.flatMap((member) => [
    member.decision.ownerId, member.originalDecision.ownerId,
  ]).filter((value): value is string => value !== null))];
}

function DecisionEditor({ title, value, owners, disabled, onChange }: {
  title: string;
  value: FindingDecision;
  owners: string[];
  disabled: boolean;
  onChange: (value: FindingDecision) => void;
}) {
  return <fieldset className="form-grid" disabled={disabled}>
    <legend>{title}</legend>
    <label>Owner<select value={value.ownerId ?? ""} onChange={(event) =>
      onChange({ ...value, ownerId: event.target.value || null })}>
      <option value="">Unassigned</option>
      {owners.map((owner) => <option value={owner} key={owner}>{owner}</option>)}
    </select></label>
    <label>Workflow<select value={value.workflowState} onChange={(event) =>
      onChange({ ...value, workflowState: event.target.value as FindingDecision["workflowState"] })}>
      <option value="open">Open</option><option value="in-progress">In progress</option><option value="resolved">Resolved</option>
    </select></label>
    <label>Disposition<select value={value.disposition} onChange={(event) => {
      const disposition = event.target.value as FindingDecision["disposition"];
      onChange({ ...value, disposition, acceptedRiskExpiresAt: disposition === "none" ? null : value.acceptedRiskExpiresAt });
    }}>
      <option value="none">None</option><option value="accepted-risk">Accepted risk</option>
    </select></label>
    <label>Risk expiry<input value={value.acceptedRiskExpiresAt ?? ""} disabled={disabled || value.disposition !== "accepted-risk"}
      placeholder="YYYY-MM-DDTHH:mm:ssZ" onChange={(event) =>
        onChange({ ...value, acceptedRiskExpiresAt: event.target.value || null })} /></label>
  </fieldset>;
}

function MemberFacts({ member }: { member: FindingCorrelationMember }) {
  return <li><strong>{member.title}</strong>
    <p><code>{member.findingId}</code></p>
    <p>Source: <code>{member.sourceId}</code>. Severity: {label(member.severity)}.</p>
    <p>{member.observationCount} observations, {member.noteCount} notes.
      Revisions: decision {member.decisionRevision}, evidence {member.evidenceRevision}.</p>
  </li>;
}

export function FindingCorrelationPanel({ finding, current, onBegin, onConfirmed, onMembershipChanged }: {
  finding: FindingDetail;
  current: boolean;
  onBegin: () => void;
  onConfirmed: (response: FindingResponse, message: string) => void;
  onMembershipChanged: () => void;
}) {
  const { workspace } = useSession();
  const canWrite = workspace.role !== "viewer";
  const previewAction = useScopedAction();
  const mutation = useScopedAction();
  const [otherFindingId, setOtherFindingId] = useState("");
  const [mergePreview, setMergePreview] = useState<FindingMergePreview | null>(null);
  const [mergeDecision, setMergeDecision] = useState<FindingDecision | null>(null);
  const [mergeRationale, setMergeRationale] = useState("");
  const [mergeIntent, setMergeIntent] = useState("");
  const [splitPreview, setSplitPreview] = useState<FindingSplitPreview | null>(null);
  const [primaryDecision, setPrimaryDecision] = useState<FindingDecision | null>(null);
  const [memberDecision, setMemberDecision] = useState<FindingDecision | null>(null);
  const [splitRationale, setSplitRationale] = useState("");
  const [splitIntent, setSplitIntent] = useState("");
  const active = finding.correlation?.state === "active" ? finding.correlation : null;
  const secondary = active?.members.find((member) => member.findingId !== finding.id) ?? null;
  const mergeOwners = useMemo(() => mergePreview ? ownerOptions(mergePreview.primary, mergePreview.other) : [],
    [mergePreview]);
  const splitOwners = useMemo(() => splitPreview ? ownerOptions(splitPreview.primary, splitPreview.member) : [],
    [splitPreview]);

  function previewMerge(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    onBegin();
    void previewAction.run((signal) => api.previewFindingMerge(finding.id, otherFindingId, signal), (response) => {
      setMergePreview(response.mergePreview);
      setMergeDecision(response.mergePreview.primary.decision);
      setMergeRationale("");
      setMergeIntent(crypto.randomUUID());
    });
  }
  function confirmMerge(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!mergePreview || !mergeDecision || !mergeIntent) return;
    onBegin();
    void mutation.run(async (signal) => {
      if (!canWrite) throw new APIError("Your selected workspace role is read only.", "forbidden", false);
      await api.mergeFindings(finding.id, {
        otherFindingId: mergePreview.other.findingId,
        primaryDecisionRevision: mergePreview.primary.decisionRevision,
        primaryEvidenceRevision: mergePreview.primary.evidenceRevision,
        otherDecisionRevision: mergePreview.other.decisionRevision,
        otherEvidenceRevision: mergePreview.other.evidenceRevision,
        decision: mergeDecision, rationale: mergeRationale, idempotencyKey: mergeIntent,
      }, signal);
      return api.finding(finding.id, signal);
    }, (response) => {
      setMergePreview(null);
      setMergeDecision(null);
      setOtherFindingId("");
      onConfirmed(response, "Findings merged with retained variants and audit history.");
      onMembershipChanged();
    });
  }
  function previewSplit() {
    if (!secondary) return;
    onBegin();
    void previewAction.run((signal) => api.previewFindingSplit(finding.id, secondary.findingId, signal), (response) => {
      setSplitPreview(response.splitPreview);
      setPrimaryDecision(response.splitPreview.primary.decision);
      setMemberDecision(response.splitPreview.member.originalDecision);
      setSplitRationale("");
      setSplitIntent(crypto.randomUUID());
    });
  }
  function confirmSplit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!splitPreview || !primaryDecision || !memberDecision || !splitIntent) return;
    onBegin();
    void mutation.run(async (signal) => {
      if (!canWrite) throw new APIError("Your selected workspace role is read only.", "forbidden", false);
      await api.splitFinding(finding.id, {
        memberFindingId: splitPreview.member.findingId,
        correlationRevision: splitPreview.correlation.revision,
        primaryDecisionRevision: splitPreview.primary.decisionRevision,
        primaryEvidenceRevision: splitPreview.primary.evidenceRevision,
        memberDecisionRevision: splitPreview.member.decisionRevision,
        memberEvidenceRevision: splitPreview.member.evidenceRevision,
        primaryDecision, memberDecision, rationale: splitRationale, idempotencyKey: splitIntent,
      }, signal);
      return api.finding(finding.id, signal);
    }, (response) => {
      setSplitPreview(null);
      setPrimaryDecision(null);
      setMemberDecision(null);
      onConfirmed(response, "Correlation split with both variant histories retained.");
      onMembershipChanged();
    });
  }

  return <section className="detail-section finding-actions" aria-label="Finding correlation">
    <h3>Correlation and source variants</h3>
    {!active && !mergePreview && <form className="finding-note-form" aria-label="Preview finding merge"
      onSubmit={previewMerge}>
      <label className="finding-edit-field">Other finding ID<input value={otherFindingId} required
        pattern="[a-f0-9]{32}" maxLength={32} autoComplete="off" spellCheck={false}
        onChange={(event) => { setOtherFindingId(event.target.value); onBegin(); }} /></label>
      <p className="section-note">Preview is read-only. Enter an explicit same-asset finding ID.
        No path, hostname or model similarity automatically merges identity.</p>
      <ActionButton type="submit" variant="outline" disabled={previewAction.pending || !current}>Preview merge</ActionButton>
    </form>}
    {mergePreview && mergeDecision && <form className="finding-note-form" aria-label="Confirm finding merge"
      onSubmit={confirmMerge}>
      <p>Review retains two source variants, {mergePreview.primary.observationCount + mergePreview.other.observationCount}
        {" "}observations and {mergePreview.primary.noteCount + mergePreview.other.noteCount} notes.</p>
      <ul><MemberFacts member={mergePreview.primary} /><MemberFacts member={mergePreview.other} /></ul>
      <p>Conflicts: {mergePreview.conflicts.length ? mergePreview.conflicts.map(label).join(", ") : "None"}.</p>
      <DecisionEditor title="Current issue decision" value={mergeDecision} owners={mergeOwners}
        disabled={mutation.pending} onChange={setMergeDecision} />
      <label className="finding-edit-field">Merge rationale<textarea value={mergeRationale} required rows={3}
        maxLength={8192} onChange={(event) => setMergeRationale(event.target.value)} /></label>
      <div className="finding-owner-actions"><Button type="button" variant="ghost"
        onClick={() => { setMergePreview(null); setMergeDecision(null); }}>Cancel preview</Button>
        <ActionButton type="submit" disabled={mutation.pending || !canWrite || !current}>Confirm merge</ActionButton></div>
    </form>}
    {active && !splitPreview && <>
      <p>Correlation <code>{active.id}</code>, revision {active.revision}. The canonical Work row retains
        every member's original evidence and decision snapshot.</p>
      <ul>{active.members.map((member) => <MemberFacts member={member} key={member.findingId} />)}</ul>
      <details><summary>Correlation audit history</summary><ul>{active.events.map((event) =>
        <li key={event.id}><strong>{label(event.type)}</strong> by <code>{event.actorId}</code> at{" "}
          <time dateTime={event.createdAt}>{timestampLabel(event.createdAt)}</time>: {event.rationale}</li>)}</ul></details>
      {secondary && <ActionButton variant="outline" disabled={previewAction.pending || !current}
        onClick={previewSplit}>Preview split</ActionButton>}
    </>}
    {splitPreview && primaryDecision && memberDecision && <form className="finding-note-form"
      aria-label="Confirm finding split" onSubmit={confirmSplit}>
      <p>Split restores two Work rows. Choose how current decisions apply; observations and raw evidence never move or duplicate.</p>
      <DecisionEditor title="Primary issue decision" value={primaryDecision} owners={splitOwners}
        disabled={mutation.pending} onChange={setPrimaryDecision} />
      <DecisionEditor title="Separated issue decision" value={memberDecision} owners={splitOwners}
        disabled={mutation.pending} onChange={setMemberDecision} />
      <label className="finding-edit-field">Split rationale<textarea value={splitRationale} required rows={3}
        maxLength={8192} onChange={(event) => setSplitRationale(event.target.value)} /></label>
      <div className="finding-owner-actions"><Button type="button" variant="ghost"
        onClick={() => { setSplitPreview(null); setPrimaryDecision(null); setMemberDecision(null); }}>Cancel preview</Button>
        <ActionButton type="submit" disabled={mutation.pending || !canWrite || !current}>Confirm split</ActionButton></div>
    </form>}
    <FormError error={previewAction.error ?? mutation.error} />
    {(previewAction.pending || mutation.pending) && <p role="status">Checking current correlation, evidence and decision revisions.</p>}
    <p className="section-note">Merge and split affect canonical Work membership only. Source observations,
      raw evidence, scanner state, human history and independent proof remain separate.</p>
  </section>;
}
