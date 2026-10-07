import { useCallback, useEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import { api } from "@/api/client";
import type {
  RetentionHold, RetentionPolicyInput, RetentionPreview, RetentionResourceKind,
} from "@/api/types";
import { label, timestampLabel } from "@/lib/format";
import { useResource } from "@/lib/use-resource";
import { useScopedAction } from "@/lib/use-scoped-action";
import { useSession } from "@/lib/session";
import { ActionButton } from "./action-button";
import { FormError } from "./form-dialog";
import { Button } from "./ui/button";

function bytesLabel(value: number) {
  return `${new Intl.NumberFormat().format(value)} bytes`;
}

function PolicyEditor({ policy, disabled, onSaved }: {
  policy: RetentionPolicyInput;
  disabled: boolean;
  onSaved: () => void;
}) {
  const action = useScopedAction();
  const [draft, setDraft] = useState(policy);
  useEffect(() => setDraft(policy), [policy]);
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    void action.run((signal) => api.updateRetentionPolicy(draft, signal), () => onSaved());
  }
  const field = (name: keyof Omit<RetentionPolicyInput, "revision">, title: string) =>
    <label>{title}<input type="number" min={1} max={3650} required value={draft[name]}
      onChange={(event) => setDraft((value) => ({ ...value, [name]: Number(event.target.value) }))} /></label>;
  return <form className="application-form" aria-label="Retention policy" onSubmit={submit}>
    <fieldset className="form-grid" disabled={disabled || action.pending}>
      {field("hotHistoryDays", "Hot history days")}
      {field("rawReportDays", "Raw report days")}
      {field("archivedEvidenceDays", "Archived evidence days")}
      {field("auditDays", "Audit days")}
    </fieldset>
    <FormError error={action.error} />
    <ActionButton type="submit" disabled={disabled || action.pending}>Save retention policy</ActionButton>
  </form>;
}

function HoldCreator({ disabled, onSaved }: { disabled: boolean; onSaved: () => void }) {
  const action = useScopedAction();
  const [kind, setKind] = useState<RetentionResourceKind>("import");
  const [id, setID] = useState("");
  const [reason, setReason] = useState("");
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    void action.run((signal) => api.createRetentionHold({
      resourceKind: kind, resourceId: id, reason,
    }, signal), () => {
      setID("");
      setReason("");
      onSaved();
    });
  }
  return <form className="application-form" aria-label="Create retention hold" onSubmit={submit}>
    <fieldset className="form-grid" disabled={disabled || action.pending}>
      <label>Resource type<select value={kind}
        onChange={(event) => setKind(event.target.value as RetentionResourceKind)}>
        <option value="import">Raw report import</option>
        <option value="observation">Observation history</option>
        <option value="correlation-event">Correlation audit event</option>
      </select></label>
      <label>Resource ID<input value={id} required pattern="[a-f0-9]{32}" maxLength={32}
        autoComplete="off" spellCheck={false} onChange={(event) => setID(event.target.value)} /></label>
      <label className="full-width">Hold reason<textarea value={reason} required rows={2} maxLength={8192}
        onChange={(event) => setReason(event.target.value)} /></label>
    </fieldset>
    <FormError error={action.error} />
    <ActionButton type="submit" disabled={disabled || action.pending}>Create hold</ActionButton>
  </form>;
}

function HoldHistory({ holds, disabled, onReleased }: {
  holds: RetentionHold[];
  disabled: boolean;
  onReleased: () => void;
}) {
  const action = useScopedAction();
  const [rationale, setRationale] = useState<Record<string, string>>({});
  function release(hold: RetentionHold) {
    const reason = rationale[hold.id] ?? "";
    void action.run((signal) => api.releaseRetentionHold(hold.id, hold.revision, reason, signal), () => {
      setRationale((value) => ({ ...value, [hold.id]: "" }));
      onReleased();
    });
  }
  return <div>
    <FormError error={action.error} />
    {holds.length === 0 ? <p>No retention holds have been recorded.</p> :
      <ul className="history-list">{holds.map((hold) => <li key={hold.id}>
        <strong>{label(hold.resourceKind)}</strong> <code>{hold.resourceId}</code>
        <p>{hold.reason}</p>
        <p>Created by <code>{hold.createdBy}</code> at{" "}
          <time dateTime={hold.createdAt}>{timestampLabel(hold.createdAt)}</time>. Revision {hold.revision}.</p>
        {hold.releasedAt === null ? <div className="finding-owner-actions">
          <input aria-label={`Release rationale for ${hold.resourceId}`} placeholder="Release rationale"
            value={rationale[hold.id] ?? ""} maxLength={8192}
            onChange={(event) => setRationale((value) => ({ ...value, [hold.id]: event.target.value }))} />
          <ActionButton variant="outline" disabled={disabled || action.pending || !(rationale[hold.id] ?? "").trim()}
            onClick={() => release(hold)}>Release hold</ActionButton>
        </div> : <p>Released by <code>{hold.releasedBy}</code> at{" "}
          <time dateTime={hold.releasedAt}>{timestampLabel(hold.releasedAt)}</time>: {hold.releaseRationale}</p>}
      </li>)}</ul>}
  </div>;
}

function Preview({ preview, canApprove, onRefresh, onApproved }: {
  preview: RetentionPreview;
  canApprove: boolean;
  onRefresh: () => void;
  onApproved: (preview: RetentionPreview) => void;
}) {
  const action = useScopedAction();
  const [rationale, setRationale] = useState("");
  const [intent] = useState(() => crypto.randomUUID());
  function approve(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    void action.run((signal) => api.approveRetentionPreview(preview.id, {
      revision: preview.revision, snapshotDigest: preview.snapshotDigest,
      rationale, idempotencyKey: intent,
    }, signal), (response) => onApproved(response.retentionPreview));
  }
  return <section aria-label="Retention preview result">
    <div className="section-heading"><div><h3>Preview {preview.state}</h3>
      <p><code>{preview.id}</code>, policy revision {preview.policyRevision}, preview revision {preview.revision}.</p></div>
      <Button type="button" variant="outline" onClick={onRefresh}>Refresh preview receipt</Button></div>
    <p>No object move, deletion, history compaction, source-state change or finding update runs from preview or approval.</p>
    <div className="table-wrap"><table><thead><tr><th>Class</th><th>Policy</th><th>Action</th>
      <th>Eligible</th><th>Protected</th><th>Candidate bytes</th></tr></thead><tbody>
      {preview.summaries.map((summary) => <tr key={summary.class}><td>{label(summary.class)}</td>
        <td>{summary.retainDays} days</td><td>{label(summary.action)}</td>
        <td>{summary.eligibleCount} / {summary.totalCount}</td><td>{summary.protectedCount}</td>
        <td>{bytesLabel(summary.sizeBytes)}</td></tr>)}
    </tbody></table></div>
    <details><summary>Review {preview.items.length} resource decisions</summary>
      <ul className="history-list">{preview.items.map((item) => <li
        key={`${item.class}:${item.resourceKind}:${item.resourceId}`}>
        <strong>{label(item.class)} / {label(item.action)}</strong>
        <p><code>{item.resourceId}</code> ({label(item.resourceKind)}), {bytesLabel(item.sizeBytes)},
          observed <time dateTime={item.observedAt}>{timestampLabel(item.observedAt)}</time>.</p>
        <p>{item.protectedReasons.length === 0 ? "Eligible in this preview." :
          `Protected: ${item.protectedReasons.map(label).join(", ")}.`}</p>
      </li>)}</ul>
    </details>
    {preview.state === "ready" && <form className="application-form" aria-label="Approve retention preview"
      onSubmit={approve}>
      <label>Approval rationale<textarea value={rationale} required rows={3} maxLength={8192}
        onChange={(event) => setRationale(event.target.value)} /></label>
      <FormError error={action.error} />
      <ActionButton type="submit" disabled={!canApprove || action.pending}>Approve exact preview</ActionButton>
    </form>}
    {preview.state === "approved" && <p role="status">Approved by <code>{preview.approvedBy}</code> at{" "}
      <time dateTime={preview.approvedAt ?? ""}>{preview.approvedAt ? timestampLabel(preview.approvedAt) : ""}</time>.
      This approval is non-destructive and does not start an executor.</p>}
    {preview.state === "stale" && <p role="alert">This preview is stale. Create a new preview after reviewing current policy, holds and references.</p>}
  </section>;
}

function RetentionControls({ onClose }: { onClose: () => void }) {
  const { workspace } = useSession();
  const canAdminister = workspace.role === "admin";
  const load = useCallback(async (signal: AbortSignal) => {
    const policy = await api.retentionPolicy(signal);
    if (!canAdminister) return { policy: policy.retentionPolicy, holds: [] as RetentionHold[] };
    const holds = await api.retentionHolds(signal);
    return { policy: policy.retentionPolicy, holds: holds.retentionHolds };
  }, [canAdminister, workspace.id]);
  const resource = useResource(load);
  const previewAction = useScopedAction();
  const refreshAction = useScopedAction();
  const [preview, setPreview] = useState<RetentionPreview | null>(null);
  useEffect(() => setPreview(null), [workspace.id]);
  const changed = () => {
    setPreview(null);
    resource.reload();
  };
  function createPreview() {
    void previewAction.run((signal) => api.previewRetention(signal),
      (response) => setPreview(response.retentionPreview));
  }
  function refreshPreview() {
    if (!preview) return;
    void refreshAction.run((signal) => api.retentionPreview(preview.id, signal),
      (response) => setPreview(response.retentionPreview));
  }
  const policy = resource.data?.policy;
  return <section className="surface settings-card full-width" aria-label="Retention and archive">
    <div className="section-heading"><div><h2>Retention and archive preview</h2>
      <p>Review exact age-based cohorts before any future archive or expiry executor is enabled.</p></div>
      <div className="finding-owner-actions">
        <ActionButton variant="outline" disabled={resource.status === "loading"} onClick={resource.reload}>Refresh retention</ActionButton>
        <Button type="button" variant="ghost" onClick={onClose}>Close retention controls</Button>
      </div>
    </div>
    <FormError error={resource.error} />
    {resource.status === "loading" && !resource.data && <p role="status">Loading retention policy and holds.</p>}
    {policy && <>
      <p>Policy revision {policy.revision}. Durations are workspace decisions, not legal advice.
        Every class remains separate in preview.</p>
      <PolicyEditor policy={{
        revision: policy.revision, hotHistoryDays: policy.hotHistoryDays,
        rawReportDays: policy.rawReportDays, archivedEvidenceDays: policy.archivedEvidenceDays,
        auditDays: policy.auditDays,
      }} disabled={!canAdminister} onSaved={changed} />
      <h3>Retention holds</h3>
      {canAdminister ? <>
        <HoldCreator disabled={false} onSaved={changed} />
        <HoldHistory holds={resource.data?.holds ?? []} disabled={false} onReleased={changed} />
      </> : <p>Hold reasons and release history are restricted to workspace administrators.
        Protected resources remain labeled in previews.</p>}
      <div className="finding-owner-actions">
        <ActionButton variant="outline" disabled={previewAction.pending} onClick={createPreview}>Create retention preview</ActionButton>
      </div>
      <FormError error={previewAction.error ?? refreshAction.error} />
      {preview && <Preview key={preview.id} preview={preview} canApprove={canAdminister}
        onRefresh={refreshPreview} onApproved={setPreview} />}
    </>}
    <p className="muted small">This increment implements policy, holds, preview and stale approval fencing only.
      Physical archive, expiry, resume, restore and availability-state downloads remain disabled.</p>
  </section>;
}

export function RetentionSettings() {
  const { workspace } = useSession();
  const [open, setOpen] = useState(false);
  const entry = useRef<HTMLButtonElement>(null);
  if (open) return <RetentionControls key={`${workspace.id}:${workspace.role}`} onClose={() => {
    setOpen(false);
    requestAnimationFrame(() => entry.current?.focus({ preventScroll: true }));
  }} />;
  return <section className="surface settings-card full-width" aria-label="Retention and archive entry">
    <div className="section-heading"><div><h2>Retention and archive</h2>
      <p>Configure separate policy classes, holds and exact non-destructive previews.</p></div>
      <span className="subtle-pill">Preview only</span></div>
    <p>Nothing is read or changed until you explicitly open the workspace retention controls.</p>
    <Button ref={entry} type="button" variant="outline" onClick={() => setOpen(true)}>Open retention controls</Button>
  </section>;
}
