import { useCallback, useLayoutEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import { APIError } from "@/api/client";
import { teamsApi } from "@/api/teams";
import type { TeamsConnection, TeamsConnectionPatch, TeamsConnectionResponse } from "@/api/teams-types";
import { useResource } from "@/lib/use-resource";
import { useScopedAction } from "@/lib/use-scoped-action";
import { useSession } from "@/lib/session";
import { ActionButton } from "./action-button";
import { FormDialog, FormError } from "./form-dialog";
import { LoadingState } from "./states";
import { Button } from "./ui/button";

function TeamsConnectionForm({ connection, onClose, onSaved, onDenied }: {
  connection: TeamsConnection | null; onClose: () => void;
  onSaved: (response: TeamsConnectionResponse) => void; onDenied: () => void;
}) {
  const { workspace } = useSession();
  const action = useScopedAction(), secret = useRef<HTMLInputElement>(null), pending = useRef<HTMLParagraphElement>(null);
  const [present, setPresent] = useState(false), [denied, setDenied] = useState(false);
  const [draft, setDraft] = useState({ name: connection?.name ?? "", enabled: connection?.enabled ?? true,
    acknowledged: connection?.teams.ownershipAcknowledged ?? false });
  const clear = () => { if (secret.current) secret.current.value = ""; setPresent(false); };
  useLayoutEffect(() => { const input = secret.current; return () => { if (input) input.value = ""; }; }, []);
  useLayoutEffect(() => { if (action.pending) pending.current?.focus({ preventScroll: true }); }, [action.pending]);
  const changed = connection === null || draft.name !== connection.name || draft.enabled !== connection.enabled || present;
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!changed || !draft.acknowledged || denied || action.pending) return;
    void action.run(async (signal) => {
      if (workspace.role !== "admin") throw new APIError("Only a current workspace administrator may save Teams configuration.", "forbidden", false);
      const workflowUrl = secret.current?.value ?? "";
      if (!connection) return teamsApi.createConnection({ name: draft.name, enabled: draft.enabled, workflowUrl,
        teams: { channelType: "standard", ownershipAcknowledged: true } }, signal);
      const patch: TeamsConnectionPatch = {
        ...(draft.name !== connection.name && { name: draft.name }),
        ...(draft.enabled !== connection.enabled && { enabled: draft.enabled }),
        ...(workflowUrl !== "" && { workflowUrl }),
      };
      return teamsApi.updateConnection(connection, patch, signal);
    }, (response) => { clear(); onSaved(response); }, (error) => {
      if (error.code === "forbidden" || error.code === "not-found") { clear(); setDenied(true); onDenied(); }
    });
  }
  return <form aria-label={connection ? "Edit Teams connection" : "Add Teams connection"}
    className="application-form teams-form" noValidate onSubmit={submit} aria-busy={action.pending}>
    <p className="form-help">Standard channel / Anyone signed Workflow only. Maintain Workflow owner and co-owner continuity;
      team ownership does not supply Workflow ownership. Ownership, permissions and channel delivery are not verified.</p>
    {connection && !denied && <p className="form-help">Revision: {connection.revision}. Stored credential:
      {" "}{connection.credentialConfigured ? "configured" : "not configured"}. A blank Workflow URL preserves it by omission.
      Credentials are never read back.</p>}
    <fieldset className="form-grid" disabled={action.pending || denied}>
      <legend className="sr-only">Teams Workflow configuration</legend>
      <label className="full-width">Name<input value={draft.name} autoComplete="off"
        onChange={(event) => setDraft((value) => ({ ...value, name: event.target.value }))} /></label>
      <label className="full-width">Workflow URL<input ref={secret} type="password" defaultValue="" autoComplete="off"
        autoCapitalize="none" spellCheck={false} onChange={(event) => setPresent(event.target.value !== "")} /></label>
      <p className="form-help full-width">The whole signed URL is private, including path and query values. Drafts are removed
        on success, cancel, close, scope change, logout or session loss. No URL preview, test or credential export is performed.</p>
      <label className="teams-checkbox full-width"><input type="checkbox" checked={draft.enabled}
        onChange={(event) => setDraft((value) => ({ ...value, enabled: event.target.checked }))} />Enabled</label>
      <label className="teams-checkbox full-width"><input type="checkbox" checked={draft.acknowledged}
        onChange={(event) => setDraft((value) => ({ ...value, acknowledged: event.target.checked }))} />I acknowledge Workflow ownership</label>
    </fieldset>
    <FormError error={action.error} />
    {action.pending && <p ref={pending} tabIndex={-1} role="status" className="form-help">Saving configuration.
      Closing cannot undo a request already sent; configuration does not send a notification.</p>}
    <footer className="form-actions"><Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
      <ActionButton type="submit" disabled={!changed || !draft.acknowledged || action.pending || denied || workspace.role !== "admin"}>
        Save Teams connection</ActionButton></footer>
  </form>;
}
function EditTeamsConnection({ id, onClose, onSaved, onDenied }: {
  id: string; onClose: () => void; onSaved: (response: TeamsConnectionResponse) => void; onDenied: () => void;
}) {
  const load = useCallback((signal: AbortSignal) => teamsApi.connection(id, signal), [id]);
  const resource = useResource(load);
  return <>
    <FormError error={resource.error} />
    {resource.error && <ActionButton variant="outline" onClick={resource.reload}>Retry Teams connection</ActionButton>}
    {resource.status === "loading" && !resource.data && <LoadingState label="Loading Teams connection" />}
    {resource.data && <TeamsConnectionForm key={resource.data.connection.revision} connection={resource.data.connection}
      onClose={onClose} onSaved={onSaved} onDenied={onDenied} />}
  </>;
}
export function TeamsConnectionEditor({ id, returnFocus, onClose, onSaved, onDenied }: {
  id: string | null; returnFocus: HTMLElement; onClose: () => void;
  onSaved: (response: TeamsConnectionResponse) => void; onDenied: () => void;
}) {
  return <FormDialog title={id ? "Edit Teams connection" : "Add Teams connection"} containFocus
    description="Explicit standard-channel Workflow configuration for this workspace. Stored metadata is not permission or delivery proof."
    returnFocus={returnFocus} onClose={onClose}>
    {id ? <EditTeamsConnection id={id} onClose={onClose} onSaved={onSaved} onDenied={onDenied} />
      : <TeamsConnectionForm connection={null} onClose={onClose} onSaved={onSaved} onDenied={onDenied} />}
  </FormDialog>;
}
