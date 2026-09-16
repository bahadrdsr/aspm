import { useRef, useState } from "react";
import type { FormEvent } from "react";
import { api, APIError } from "@/api/client";
import type { FindingDetail, FindingNote, FindingPatch, FindingResponse } from "@/api/types";
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

function riskExpiry(value: string): string | null {
  if (value === "") return null;
  if (!/^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})$/.test(value) ||
    !Number.isFinite(Date.parse(value))) {
    throw new APIError("Risk acceptance expiry must be an RFC3339 timestamp with a timezone, or blank for no expiry.", "invalid-input", false);
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
  const workflow = useDraft<string>(finding.workflowState);
  const disposition = useDraft<string>(finding.disposition ?? "none");
  const expiry = useDraft(finding.acceptedRiskExpiresAt ?? "");
  const riskChanged = disposition.value !== (finding.disposition ?? "none") ||
    (disposition.value === "accepted-risk" && expiry.value !== (finding.acceptedRiskExpiresAt ?? ""));

  function save(kind: typeof intent, fields: () => FindingPatch) {
    if (action.pending) return;
    onBegin();
    setIntent(kind);
    void action.run(async (signal) => {
      if (workspace.role === "viewer") throw new APIError("Your selected workspace role is read only.", "forbidden", false);
      return api.updateFinding(finding.id, fields(), signal);
    }, (response) => {
      if (kind === "workflow") workflow.reset();
      if (kind === "risk") { disposition.reset(); expiry.reset(); }
      onConfirmed(response, kind === "owner" ? "Owner updated from the service." :
        kind === "workflow" ? "Human workflow saved." : "Risk acceptance saved.");
    });
  }
  function saveWorkflow(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!event.currentTarget.reportValidity()) return;
    save("workflow", () => ({
      workflowState: inputChoice(workflow.value, ["open", "in-progress", "resolved"] as const, "workflow state"),
    }));
  }
  function saveRisk(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!event.currentTarget.reportValidity()) return;
    save("risk", () => {
      const selected = inputChoice(disposition.value, ["none", "accepted-risk"] as const, "disposition");
      if (selected === "none") return { disposition: "none" };
      const acceptedRiskExpiresAt = riskExpiry(expiry.value);
      return finding.disposition === "accepted-risk" ? { acceptedRiskExpiresAt } :
        { disposition: selected, acceptedRiskExpiresAt };
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
        <option value="open">Open</option><option value="in-progress">In progress</option><option value="resolved">Resolved</option>
      </select></label>
      <ActionButton type="submit" variant="outline" disabled={action.pending || workflow.value === finding.workflowState}>Save workflow</ActionButton>
    </form>
    <form aria-label="Change risk acceptance" className="finding-risk-form" onSubmit={saveRisk}>
      <label className="finding-edit-field">Disposition<select value={disposition.value} disabled={action.pending}
        onChange={(event) => disposition.change(event.target.value)}>
        <option value="none">None</option><option value="accepted-risk">Accepted risk</option>
      </select></label>
      <label className="finding-edit-field">Risk acceptance expiry (RFC3339, optional)
        <input type="text" value={expiry.value} autoComplete="off" placeholder="YYYY-MM-DDTHH:mm:ssZ"
          disabled={action.pending || disposition.value !== "accepted-risk"}
          aria-invalid={intent === "risk" && action.error?.code === "invalid-input" ? true : undefined}
          onChange={(event) => expiry.change(event.target.value)} /></label>
      <p className="section-note">Blank means no expiry. The service determines whether a saved acceptance is expired; changing workflow never extends it.</p>
      <ActionButton type="submit" variant="outline" disabled={action.pending || !riskChanged}>Save risk acceptance</ActionButton>
    </form>
    <div className="finding-action-feedback">
      {action.pending && <p role="status">Saving {intent === "risk" ? "risk acceptance" : intent}. Please wait.</p>}
      <FormError error={action.error} />
      {message && <p role="status">{message}</p>}
      {outsideFilter && <p role="status">This finding no longer matches the current Work filter. Closing returns to the Work heading; your filter is preserved.</p>}
    </div>
    <p className="section-note">Human workflow and risk acceptance do not independently verify a resolution or change source evidence.</p>
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
