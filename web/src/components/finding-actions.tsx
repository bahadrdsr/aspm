import { useRef, useState } from "react";
import type { FormEvent } from "react";
import { api, APIError } from "@/api/client";
import type { FindingDetail, FindingDispositionScope, FindingNote, FindingPatch, FindingResponse } from "@/api/types";
import { inputChoice } from "@/lib/application-input";
import { useScopedAction } from "@/lib/use-scoped-action";
import { useSession } from "@/lib/session";
import { ActionButton } from "./action-button";
import { FormError } from "./form-dialog";
import { Icon } from "./icon";

function useDraft<T>(confirmed: T) {
  const [edit, setEdit] = useState<{ value: T; baseline: T } | null>(null);
  return {
    value: edit && edit.value !== edit.baseline ? edit.value : confirmed,
    change: (value: T) => setEdit({ value, baseline: confirmed }),
    reset: () => setEdit(null),
  };
}

function dispositionExpiry(value: string, label: string): string | null {
  if (value === "") return null;
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/.test(value) ||
    !Number.isFinite(Date.parse(value))) {
    throw new APIError(`${label} must be an RFC3339 timestamp with a timezone.`, "invalid-input", false);
  }
  return value;
}

export function FindingActions({ finding, message, outsideFilter, onBegin, onConfirmed }: {
  finding: FindingDetail;
  message: string | null;
  outsideFilter: boolean;
  onBegin: () => void;
  onConfirmed: (response: FindingResponse, message: string) => void;
}) {
  const { session, workspace } = useSession();
  const action = useScopedAction();
  const [intent, setIntent] = useState<"owner" | "workflow" | "risk">("owner");
  const [workflowRationale, setWorkflowRationale] = useState("");
  const workflow = useDraft<string>(finding.workflowState);
  const disposition = useDraft<string>(finding.disposition ?? "none");
  const dispositionScope = useDraft(finding.dispositionApproval?.scopeKind ?? "finding");
  const riskExpiry = useDraft(finding.acceptedRiskExpiresAt ?? "");
  const suppressionExpiry = useDraft(finding.disposition === "suppressed"
    ? finding.dispositionApproval?.expiresAt ?? "" : "");
  const [dispositionRationale, setDispositionRationale] = useState("");
  const riskChanged = disposition.value !== (finding.disposition ?? "none") ||
    dispositionScope.value !== (finding.dispositionApproval?.scopeKind ?? "finding") ||
    (disposition.value === "accepted-risk" && riskExpiry.value !== (finding.acceptedRiskExpiresAt ?? "")) ||
    (disposition.value === "suppressed" && suppressionExpiry.value !==
      (finding.dispositionApproval?.expiresAt ?? ""));

  function save(kind: typeof intent, fields: () => FindingPatch) {
    if (action.pending) return;
    onBegin();
    setIntent(kind);
    void action.run(async (signal) => {
      if (workspace.role === "viewer") throw new APIError("Your selected workspace role is read only.", "forbidden", false);
      return api.updateFinding(finding.id, fields(), signal);
    }, (response) => {
      if (kind === "workflow") { workflow.reset(); setWorkflowRationale(""); }
      if (kind === "risk") {
        disposition.reset(); dispositionScope.reset(); riskExpiry.reset(); suppressionExpiry.reset();
        setDispositionRationale("");
      }
      onConfirmed(response, kind === "owner" ? "Owner updated from the service." :
        kind === "workflow" ? "Human workflow saved." : "Disposition approval saved.");
    });
  }
  function saveWorkflow(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!event.currentTarget.reportValidity()) return;
    save("workflow", () => {
      const patch: FindingPatch = {
        workflowState: inputChoice(workflow.value, ["open", "in-progress", "pending-retest", "resolved"] as const, "workflow state"),
      };
      if (workflowRationale !== "") patch.rationale = workflowRationale;
      return patch;
    });
  }
  function saveRisk(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!event.currentTarget.reportValidity()) return;
    save("risk", () => {
      const selected = inputChoice(disposition.value,
        ["none", "accepted-risk", "suppressed", "false-positive"] as const, "disposition");
      if (dispositionRationale.trim() === "") {
        throw new APIError("Disposition changes require a rationale.", "invalid-input", false);
      }
      if (selected === "none") return { disposition: "none", rationale: dispositionRationale };
      if (selected === "accepted-risk") return {
        disposition: selected, dispositionScope: "finding",
        acceptedRiskExpiresAt: riskExpiry.value === "" ? null :
          dispositionExpiry(riskExpiry.value, "Risk acceptance expiry"),
        suppressionExpiresAt: null, rationale: dispositionRationale,
      };
      if (selected === "suppressed") return {
        disposition: selected,
        dispositionScope: inputChoice(dispositionScope.value,
          ["finding", "asset", "source", "scope"] as const, "suppression scope"),
        acceptedRiskExpiresAt: null,
        suppressionExpiresAt: dispositionExpiry(suppressionExpiry.value, "Suppression expiry"),
        rationale: dispositionRationale,
      };
      return {
        disposition: selected, dispositionScope: "finding",
        acceptedRiskExpiresAt: null, suppressionExpiresAt: null, rationale: dispositionRationale,
      };
    });
  }
  return <section className="detail-section finding-actions" aria-label="Finding actions">
    <h3>Finding actions</h3>
    <div className="finding-owner-actions" role="group" aria-label="Owner actions">
      <ActionButton variant="outline" disabled={action.pending || finding.ownerId === session.user.id}
        onClick={() => save("owner", () => ({ ownerId: session.user.id }))}><Icon name="user" size={15} />Assign to me</ActionButton>
      <ActionButton variant="ghost" disabled={action.pending || finding.ownerId == null}
        onClick={() => save("owner", () => ({ ownerId: null }))}>Unassign</ActionButton>
    </div>
    <form aria-label="Change finding workflow" className="finding-workflow-form" onSubmit={saveWorkflow}>
      <label className="finding-edit-field">Workflow<select value={workflow.value} disabled={action.pending}
        onChange={(event) => workflow.change(event.target.value)}>
        <option value="open">Open</option><option value="in-progress">In progress</option>
        <option value="pending-retest">Pending retest</option><option value="resolved">Resolved</option>
      </select></label>
      <label className="finding-edit-field">Decision rationale (optional)
        <textarea rows={2} maxLength={8192} value={workflowRationale} disabled={action.pending}
          onChange={(event) => setWorkflowRationale(event.target.value)} /></label>
      <ActionButton type="submit" variant="outline" disabled={action.pending || workflow.value === finding.workflowState}>Save workflow</ActionButton>
    </form>
    <form aria-label="Change finding disposition" className="finding-risk-form" onSubmit={saveRisk}>
      <label className="finding-edit-field">Disposition<select value={disposition.value} disabled={action.pending}
        onChange={(event) => disposition.change(event.target.value)}>
        <option value="none">None</option><option value="accepted-risk">Accepted risk</option>
        <option value="suppressed">Suppressed</option><option value="false-positive">False positive</option>
      </select></label>
      <label className="finding-edit-field">Suppression scope<select value={dispositionScope.value}
        disabled={action.pending || disposition.value !== "suppressed"}
        onChange={(event) => dispositionScope.change(event.target.value as FindingDispositionScope)}>
        <option value="finding">Finding</option><option value="source">Source</option>
        <option value="scope">Scan scope</option><option value="asset">Asset</option>
      </select></label>
      <label className="finding-edit-field">Risk acceptance expiry (RFC3339, optional)
        <input type="text" value={riskExpiry.value} autoComplete="off" placeholder="YYYY-MM-DDTHH:mm:ssZ"
          disabled={action.pending || disposition.value !== "accepted-risk"}
          aria-invalid={intent === "risk" && action.error?.code === "invalid-input" ? true : undefined}
          onChange={(event) => riskExpiry.change(event.target.value)} /></label>
      <label className="finding-edit-field">Suppression expiry (RFC3339, required)
        <input type="text" value={suppressionExpiry.value} autoComplete="off" placeholder="YYYY-MM-DDTHH:mm:ssZ"
          required={disposition.value === "suppressed"}
          disabled={action.pending || disposition.value !== "suppressed"}
          onChange={(event) => suppressionExpiry.change(event.target.value)} /></label>
      <label className="finding-edit-field">Approval rationale
        <textarea rows={3} maxLength={8192} required={riskChanged} value={dispositionRationale}
          disabled={action.pending} onChange={(event) => setDispositionRationale(event.target.value)} /></label>
      <p className="section-note">Suppression requires a future expiry. False-positive and accepted-risk decisions are finding-scoped. Source, workflow, AI, and proof states remain separate.</p>
      <ActionButton type="submit" variant="outline"
        disabled={action.pending || !riskChanged || dispositionRationale.trim() === ""}>Save disposition</ActionButton>
    </form>
    <div className="finding-action-feedback">
      {action.pending && <p role="status">Saving {intent === "risk" ? "disposition approval" : intent}. Please wait.</p>}
      <FormError error={action.error} />
      {message && <p role="status">{message}</p>}
      {outsideFilter && <p role="status">This finding no longer matches the current Work filter. Closing returns to the Work heading; your filter is preserved.</p>}
    </div>
    <p className="section-note">Human workflow and disposition do not independently verify a resolution or change source evidence.</p>
  </section>;
}

export function FindingNoteForm({ id, message, onBegin, onAdded }: {
  id: string; message: string | null; onBegin: () => void; onAdded: (note: FindingNote) => void;
}) {
  const { workspace } = useSession();
  const action = useScopedAction();
  const [draft, setDraft] = useState("");
  const editRevision = useRef(0);
  const bytes = new TextEncoder().encode(draft).byteLength;
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (action.pending || !event.currentTarget.reportValidity()) return;
    const text = draft, revision = editRevision.current;
    onBegin();
    void action.run(async (signal) => {
      if (workspace.role === "viewer") throw new APIError("Your selected workspace role is read only.", "forbidden", false);
      return api.addFindingNote(id, text, signal);
    }, (response) => {
      if (editRevision.current === revision) { setDraft(""); editRevision.current += 1; }
      onAdded(response.note);
    });
  }
  return <form aria-label="Add analyst note" className="finding-note-form" onSubmit={submit} aria-busy={action.pending}>
    <label className="finding-edit-field">New note<textarea value={draft} required rows={4}
      aria-describedby="finding-note-limits" aria-invalid={action.error?.code === "invalid-input" ? true : undefined}
      onChange={(event) => { editRevision.current += 1; setDraft(event.target.value); onBegin(); }} /></label>
    <div className="finding-note-footer"><p id="finding-note-limits" className="section-note">{bytes.toLocaleString("en-US")} / 8,192 UTF-8 bytes. Text and line breaks are saved literally.</p>
      <ActionButton type="submit" variant="outline" disabled={action.pending}>Add note</ActionButton></div>
    {action.pending && <p className="finding-action-feedback" role="status">Adding note. Please wait.</p>}
    <FormError error={action.error} />
    {message && <p className="finding-action-feedback" role="status">{message}</p>}
  </form>;
}
