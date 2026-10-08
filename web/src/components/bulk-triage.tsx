import { useState } from "react";
import type { FormEvent } from "react";
import { api } from "@/api/client";
import type { WorkItem } from "@/api/types";
import { useScopedAction } from "@/lib/use-scoped-action";
import { useSession } from "@/lib/session";
import { ActionButton } from "./action-button";
import { FormError } from "./form-dialog";

type BulkAction = "assign-self" | "unassign" | "open" | "in-progress" | "pending-retest" | "resolved";

export function BulkTriage({ findingIds, onConfirmed, onApplied }: {
  findingIds: string[];
  onConfirmed: (items: WorkItem[]) => void;
  onApplied: (message: string) => void;
}) {
  const { session } = useSession();
  const request = useScopedAction();
  const [action, setAction] = useState<BulkAction>("in-progress");
  const [rationale, setRationale] = useState("");
  const bytes = new TextEncoder().encode(rationale).byteLength;
  const tooMany = findingIds.length > 100;

  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (request.pending || !event.currentTarget.reportValidity() || tooMany) return;
    const input = action === "assign-self"
      ? { findingIds, ownerId: session.user.id, rationale }
      : action === "unassign"
        ? { findingIds, ownerId: null, rationale }
        : { findingIds, workflowState: action, rationale };
    void request.run((signal) => api.bulkUpdateFindings(input, signal), (response) => {
      onConfirmed(response.items);
      setRationale("");
      onApplied(`${response.total} selected finding${response.total === 1 ? "" : "s"} updated from the service.`);
    });
  }

  return <form className="bulk-triage-form" aria-label="Bulk triage selected findings" onSubmit={submit}>
    <label>Action<select value={action} disabled={request.pending}
      onChange={(event) => setAction(event.target.value as BulkAction)}>
      <option value="assign-self">Assign to me</option><option value="unassign">Unassign</option>
      <option value="open">Mark open</option><option value="in-progress">Mark in progress</option>
      <option value="pending-retest">Mark pending retest</option><option value="resolved">Mark resolved</option>
    </select></label>
    <label>Rationale<input type="text" required value={rationale} disabled={request.pending}
      aria-invalid={request.error?.code === "invalid-input" || bytes > 8192 || undefined}
      onChange={(event) => setRationale(event.target.value)} /></label>
    <ActionButton type="submit" variant="outline"
      disabled={request.pending || tooMany || rationale.trim() === "" || bytes > 8192}>Apply</ActionButton>
    {tooMany && <span className="selection-boundary">Bulk actions are limited to 100 selected findings.</span>}
    {request.pending && <span role="status">Applying bulk triage.</span>}
    <FormError error={request.error} />
  </form>;
}
