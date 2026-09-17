import { useCallback, useRef, useState } from "react";
import { slackApi } from "@/api/slack";
import type { SlackConnectionResponse } from "@/api/types";
import { useResource } from "@/lib/use-resource";
import { useSession } from "@/lib/session";
import { timestampLabel } from "@/lib/format";
import { ActionButton } from "./action-button";
import { FormError } from "./form-dialog";
import { Icon } from "./icon";
import { SlackConnectionEditor } from "./slack-connection-editor";
import { DataNotice, LoadingState } from "./states";
import { Button } from "./ui/button";
import "./slack.css";

export function SlackConnections() {
  const { workspace } = useSession();
  const [cursor, setCursor] = useState<string | null>(null);
  const [editor, setEditor] = useState<{ id: string | null; trigger: HTMLElement } | null>(null);
  const [confirmed, setConfirmed] = useState<{ response: SlackConnectionResponse; revision: number } | null>(null);
  const mutationRevision = useRef(0);
  const load = useCallback(async (signal: AbortSignal) => {
    const revision = mutationRevision.current;
    return { response: await slackApi.connections(100, cursor, signal), revision };
  }, [cursor]);
  const resource = useResource(load);
  const data = resource.data?.response;
  const recent = confirmed && resource.data && confirmed.revision > resource.data.revision ? confirmed.response.connection : null;
  const rows = data ? [...new Map([...data.items, ...(recent ? [recent] : [])].map((item) => [item.id, item])).values()] : [];
  function refresh() {
    if (cursor !== null) setCursor(null);
    else resource.reload();
  }
  function saved(response: SlackConnectionResponse) {
    mutationRevision.current += 1;
    setConfirmed({ response, revision: mutationRevision.current });
    setEditor(null);
    refresh();
  }
  return <section className="surface slack-panel slack-connections" aria-label="Connections">
    <header className="slack-panel-heading">
      <div><h2>Connections</h2><p className="form-help">Workspace Slack destinations. Stored credentials are metadata, not live verification.</p></div>
      <div className="slack-actions">
        <ActionButton variant="outline" disabled={resource.status === "loading"} onClick={refresh}><Icon name="refresh" />Refresh connections</ActionButton>
        {workspace.role === "admin" && <ActionButton onClick={(event) => setEditor({ id: null, trigger: event.currentTarget })}>Add connection</ActionButton>}
      </div>
    </header>
    <div className="slack-panel-content">
      <FormError error={resource.error} />
      {resource.error && <ActionButton variant="outline" onClick={resource.reload}>Retry connections</ActionButton>}
      {resource.status === "loading" && !data && <LoadingState label="Loading connections" />}
      {resource.status === "loading" && data && <p role="status" className="form-help">Loading connections. Showing previously received metadata.</p>}
      {data && <>
        <div className="slack-summary"><span>Total at last read: {data.total.toLocaleString("en-US")} connections; {rows.length.toLocaleString("en-US")} shown</span>
          {data.dataOrigin && <DataNotice origin={data.dataOrigin} />}</div>
        {rows.length === 0 ? <div className="slack-empty"><h3>No connections</h3><p className="form-help">
          The service returned an empty collection. An administrator can add a Slack destination; configuring it does not enqueue notifications.</p></div> :
          <div className="slack-table-scroll" tabIndex={0} role="region" aria-label="Connection results">
            <table className="slack-table" aria-label="Connections">
              <thead><tr><th scope="col">Connection</th><th scope="col">Action</th></tr></thead>
              <tbody>{rows.map((item) => <tr key={item.id}>
                <td><strong>{item.name}</strong><p><code>{item.channel}</code></p>
                  <p>{item.enabled ? "Enabled" : "Disabled"} · Revision: {item.revision}</p>
                  <p>Stored credential: {item.credentialConfigured ? "configured" : "not configured"} · Not live verified</p>
                  <p>Updated <time dateTime={item.updatedAt}>{timestampLabel(item.updatedAt)}</time></p></td>
                <td>{workspace.role === "admin" ? <Button type="button" variant="outline" size="sm"
                  onClick={(event) => setEditor({ id: item.id, trigger: event.currentTarget })}>Edit connection</Button> : <span className="form-help">Read only</span>}</td>
              </tr>)}</tbody>
            </table>
          </div>}
        {data.nextCursor !== null && <footer className="slack-actions"><ActionButton variant="outline" disabled={resource.status === "loading"}
          onClick={() => setCursor(data.nextCursor)}>Next connections<Icon name="chevron" /></ActionButton></footer>}
      </>}
    </div>
    {workspace.role === "admin" && editor && <SlackConnectionEditor id={editor.id} returnFocus={editor.trigger} onClose={() => setEditor(null)} onSaved={saved} />}
  </section>;
}
