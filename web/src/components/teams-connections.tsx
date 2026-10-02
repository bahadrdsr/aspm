import { useCallback, useRef, useState } from "react";
import { teamsApi } from "@/api/teams";
import type { TeamsConnectionResponse } from "@/api/teams-types";
import { useResource } from "@/lib/use-resource";
import { useSession } from "@/lib/session";
import { timestampLabel } from "@/lib/format";
import { ActionButton } from "./action-button";
import { FormError } from "./form-dialog";
import { TeamsConnectionEditor } from "./teams-connection-editor";
import { LoadingState } from "./states";
import { Button } from "./ui/button";
import "./slack.css";
import "./teams.css";

function TeamsConnectionPanel() {
  const { workspace } = useSession();
  const [cursor, setCursor] = useState<string | null>(null), [denied, setDenied] = useState(false);
  const [editor, setEditor] = useState<{ id: string | null; trigger: HTMLElement } | null>(null);
  const [receipt, setReceipt] = useState<{ response: TeamsConnectionResponse; order: number } | null>(null);
  const order = useRef(0);
  const load = useCallback(async (signal: AbortSignal) => {
    const started = order.current;
    return { page: await teamsApi.connections(cursor, signal), started };
  }, [cursor]);
  const resource = useResource(load), page = resource.data?.page;
  const saved = receipt && resource.data && receipt.order > resource.data.started ? receipt.response.connection : null;
  const rows = page ? [...new Map([...page.items, ...(saved ? [saved] : [])].map((item) => [item.id, item])).values()] : [];
  const canEdit = !denied && resource.status === "ready";
  function refresh() { if (cursor !== null) setCursor(null); else resource.reload(); }
  return <section className="surface slack-panel" aria-label="Teams connections">
    <header className="slack-panel-heading"><div><h2>Teams connections</h2>
      <p className="form-help">Standard-channel Workflows. Permission state: not-verified. Configuration is not delivery proof.</p></div>
      <div className="slack-actions"><ActionButton variant="outline" disabled={resource.status === "loading"} onClick={refresh}>
        Refresh Teams connections</ActionButton>
        {workspace.role === "admin" && <ActionButton disabled={!canEdit}
          onClick={(event) => setEditor({ id: null, trigger: event.currentTarget })}>Add Teams connection</ActionButton>}</div>
    </header>
    <div className="slack-panel-content">
      {workspace.role !== "admin" && <p className="form-help">Read only. Only a current administrator may configure Teams connections.</p>}
      {denied && <p className="form-help">Configuration writes are withheld after current administrator access was denied.</p>}
      <FormError error={resource.error} />
      {resource.error && <ActionButton variant="outline" onClick={resource.reload}>Retry Teams connections</ActionButton>}
      {resource.status === "loading" && !page && <LoadingState label="Loading Teams connections" />}
      {page && <>
        <p className="form-help">{page.total} Teams connections at the last native read; {rows.length} shown.
          {saved && " Includes an acknowledged configuration write, not a new collection total."}</p>
        <div className="slack-table-scroll" tabIndex={0} role="region" aria-label="Teams connection results">
          <table className="slack-table" aria-label="Teams connections">
            <thead><tr><th scope="col">Connection</th><th scope="col">Action</th></tr></thead>
            <tbody>{rows.map((item) => <tr key={item.id}>
              <td><strong>{item.name}</strong><p>{item.enabled ? "Enabled" : "Disabled"} · Revision: {item.revision}</p>
                <p>{item.teams.workflowOrigin} · Channel: standard · Ownership acknowledged by operator</p>
                <p>Stored credential: {item.credentialConfigured ? "configured" : "not configured"} · Permission: not-verified</p>
                <p>Updated <time dateTime={item.updatedAt}>{timestampLabel(item.updatedAt)}</time></p></td>
              <td>{workspace.role === "admin" ? <Button type="button" variant="outline" size="sm" disabled={!canEdit}
                onClick={(event) => setEditor({ id: item.id, trigger: event.currentTarget })}>Edit Teams connection</Button> : "Read only"}</td>
            </tr>)}</tbody>
          </table>
        </div>
        {rows.length === 0 && <p className="form-help">No Teams connections were returned. No Workflow callback was invoked.</p>}
        {page.nextCursor !== null && <ActionButton variant="outline" disabled={resource.status === "loading"}
          onClick={() => setCursor(page.nextCursor)}>Next Teams connections</ActionButton>}
      </>}
    </div>
    {workspace.role === "admin" && editor && <TeamsConnectionEditor id={editor.id} returnFocus={editor.trigger}
      onClose={() => setEditor(null)} onDenied={() => setDenied(true)}
      onSaved={(response) => { setReceipt({ response, order: ++order.current }); setEditor(null); }} />}
  </section>;
}
export function TeamsConnections({ open, onToggle }: { open: boolean; onToggle: () => void }) {
  return <div className="teams-entry"><Button type="button" variant="outline" aria-expanded={open} onClick={onToggle}>
    Teams connections</Button>{open && <TeamsConnectionPanel />}</div>;
}
