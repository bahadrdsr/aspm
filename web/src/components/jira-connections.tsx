import { useCallback, useRef, useState } from "react";
import { jiraApi } from "@/api/jira";
import type { JiraConnectionResponse } from "@/api/jira-types";
import { useResource } from "@/lib/use-resource";
import { useSession } from "@/lib/session";
import { timestampLabel } from "@/lib/format";
import { ActionButton } from "./action-button";
import { FormError } from "./form-dialog";
import { JiraConnectionEditor } from "./jira-connection-editor";
import { LoadingState } from "./states";
import { Button } from "./ui/button";
import "./slack.css";
import "./jira.css";

function JiraConnectionPanel({ denied, onDenied }: { denied: boolean; onDenied: () => void }) {
  const { workspace } = useSession();
  const [cursor, setCursor] = useState<string | null>(null);
  const [editor, setEditor] = useState<{ id: string | null; trigger: HTMLElement } | null>(null);
  const [receipt, setReceipt] = useState<{ response: JiraConnectionResponse; order: number } | null>(null);
  const order = useRef(0);
  const load = useCallback(async (signal: AbortSignal) => {
    const started = order.current;
    return { page: await jiraApi.connections(cursor, signal), started };
  }, [cursor]);
  const resource = useResource(load), page = resource.data?.page;
  const saved = receipt && resource.data && receipt.order > resource.data.started ? receipt.response.connection : null;
  const rows = page ? [...new Map([...page.items, ...(saved ? [saved] : [])].map((item) => [item.id, item])).values()] : [];
  const canEdit = !denied && resource.status === "ready";
  function refresh() { if (cursor !== null) setCursor(null); else resource.reload(); }
  return <section className="surface slack-panel" aria-label="Jira connections">
    <header className="slack-panel-heading"><div><h2>Jira connections</h2>
      <p className="form-help">Jira Cloud v3 / OAuth bearer. Permission state: not-verified, including configured credentials.</p></div>
      <div className="slack-actions"><ActionButton variant="outline" disabled={resource.status === "loading"} onClick={refresh}>Refresh Jira connections</ActionButton>
        {workspace.role === "admin" && <ActionButton disabled={!canEdit} onClick={(event) => setEditor({ id: null, trigger: event.currentTarget })}>Add Jira connection</ActionButton>}</div>
    </header>
    <div className="slack-panel-content">
      {workspace.role !== "admin" && <p className="form-help">Read only. Only a current workspace administrator may add or edit Jira configuration.</p>}
      {denied && <p className="form-help">Configuration writes are withheld after the service denied current administrator access.</p>}
      <FormError error={resource.error} />
      {resource.error && <ActionButton variant="outline" onClick={resource.reload}>Retry Jira connections</ActionButton>}
      {resource.status === "loading" && !page && <LoadingState label="Loading Jira connections" />}
      {page && <>
        <p className="form-help">Total at last native read: {page.total.toLocaleString("en-US")} Jira connections; {rows.length} shown.
          {saved && " Includes an acknowledged configuration write, not a new collection total."}</p>
        <div className="slack-table-scroll" tabIndex={0} role="region" aria-label="Jira connection results">
          <table className="slack-table" aria-label="Jira connections">
            <thead><tr><th scope="col">Connection</th><th scope="col">Action</th></tr></thead>
            <tbody>{rows.map((item) => <tr key={item.id}>
              <td><strong>{item.name}</strong><p>{item.enabled ? "Enabled" : "Disabled"} · Revision: {item.revision}</p>
                <p>{item.jira.siteOrigin} · Project: {item.jira.project} · Issue type: {item.jira.issueType}</p>
                <p>Cloud ID: <code>{item.jira.cloudId}</code></p><p>API base: {item.jira.apiBase}</p>
                <p>Stored credential: {item.credentialConfigured ? "configured" : "not configured"} · Permission: not-verified</p>
                <p>Updated <time dateTime={item.updatedAt}>{timestampLabel(item.updatedAt)}</time></p></td>
              <td>{workspace.role === "admin" ? <Button type="button" variant="outline" size="sm" disabled={!canEdit}
                onClick={(event) => setEditor({ id: item.id, trigger: event.currentTarget })}>Edit Jira connection</Button> : "Read only"}</td>
            </tr>)}</tbody>
          </table>
        </div>
        {rows.length === 0 && <p className="form-help">No Jira connections were returned. Configuration never probes Jira or creates a work item.</p>}
        {page.nextCursor !== null && <ActionButton variant="outline" disabled={resource.status === "loading"}
          onClick={() => setCursor(page.nextCursor)}>Next Jira connections</ActionButton>}
      </>}
    </div>
    {workspace.role === "admin" && editor && <JiraConnectionEditor id={editor.id} returnFocus={editor.trigger}
      onClose={() => setEditor(null)} onDenied={onDenied}
      onSaved={(response) => { setReceipt({ response, order: ++order.current }); setEditor(null); }} />}
  </section>;
}

export function JiraConnections({ open, onToggle }: { open: boolean; onToggle: () => void }) {
  const [denied, setDenied] = useState(false);
  return <div className="jira-entry">
    <Button type="button" variant="outline" aria-expanded={open} onClick={onToggle}>Jira connections</Button>
    {open && <JiraConnectionPanel denied={denied} onDenied={() => setDenied(true)} />}
  </div>;
}
