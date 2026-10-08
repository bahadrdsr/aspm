import { useCallback, useRef, useState } from "react";
import { webhookApi } from "@/api/generic-webhooks";
import type { WebhookConnectionResponse } from "@/api/generic-webhooks";
import type { WebhookConnection } from "@/api/generic-webhooks";
import { useResource } from "@/lib/use-resource";
import { useSession } from "@/lib/session";
import { timestampLabel } from "@/lib/format";
import { ActionButton } from "./action-button";
import { FormError } from "./form-dialog";
import { LoadingState } from "./states";
import { Button } from "./ui/button";
import { WebhookConnectionEditor } from "./webhook-connection-editor";
import "./slack.css";
import "./webhook.css";

function WebhookConnectionPanel() {
  const { workspace } = useSession();
  const [cursor, setCursor] = useState<string | null>(null);
  const [editor, setEditor] = useState<{ connection: WebhookConnection | null; trigger: HTMLElement } | null>(null);
  const [receipt, setReceipt] = useState<{ response: WebhookConnectionResponse; order: number } | null>(null);
  const order = useRef(0);
  const load = useCallback(async (signal: AbortSignal) => {
    const started = order.current;
    return { page: await webhookApi.connections(cursor, signal), started };
  }, [cursor]);
  const resource = useResource(load), page = resource.data?.page;
  const saved = receipt && resource.data && receipt.order > resource.data.started ? receipt.response.connection : null;
  const rows = page ? [...new Map([...page.items, ...(saved ? [saved] : [])]
    .map((item) => [item.id, item])).values()] : [];
  function refresh() { if (cursor !== null) setCursor(null); else resource.reload(); }
  return <section className="surface slack-panel webhook-connections" aria-label="Webhook connections">
    <header className="slack-panel-heading"><div><h2>Webhook connections</h2>
      <p className="form-help">Fixed HMAC-signed JSON to operator-approved HTTPS origins.
        Configuration is not receiver verification or a test send.</p></div>
      <div className="slack-actions"><ActionButton variant="outline" disabled={resource.status === "loading"}
        onClick={refresh}>Refresh webhook connections</ActionButton>
        {workspace.role === "admin" && <ActionButton
          onClick={(event) => setEditor({ connection: null, trigger: event.currentTarget })}>Add webhook connection</ActionButton>}</div>
    </header>
    <div className="slack-panel-content">
      {workspace.role !== "admin" && <p className="form-help">Read only. Only administrators configure webhook destinations.</p>}
      <FormError error={resource.error} />
      {resource.error && <ActionButton variant="outline" onClick={resource.reload}>Retry webhook connections</ActionButton>}
      {resource.status === "loading" && !page && <LoadingState label="Loading webhook connections" />}
      {page && <><p className="form-help">{page.total} webhook connections at the last read; {rows.length} shown.</p>
        <div className="slack-table-scroll" tabIndex={0} role="region" aria-label="Webhook connection results">
          <table className="slack-table" aria-label="Webhook connections"><thead><tr>
            <th scope="col">Connection</th><th scope="col">Action</th>
          </tr></thead><tbody>{rows.map((item) => <tr key={item.id}>
            <td><strong>{item.name}</strong><p>{item.webhook.origin}</p><p><code>{item.webhook.path}</code></p>
              <p>Signature: HMAC SHA-256 · {item.enabled ? "Enabled" : "Disabled"} · Revision: {item.revision}</p>
              <p>Stored secret: {item.credentialConfigured ? "configured" : "not configured"} · Not verified</p>
              <p>Updated <time dateTime={item.updatedAt}>{timestampLabel(item.updatedAt)}</time></p></td>
            <td>{workspace.role === "admin" ? <Button type="button" variant="outline" size="sm"
              onClick={(event) => setEditor({ connection: item, trigger: event.currentTarget })}>
              Edit webhook connection</Button> : "Read only"}</td>
          </tr>)}</tbody></table>
        </div>
        {rows.length === 0 && <p className="form-help">No webhook connections were returned.</p>}
        {page.nextCursor !== null && <ActionButton variant="outline"
          onClick={() => setCursor(page.nextCursor)}>Next webhook connections</ActionButton>}
      </>}
    </div>
    {workspace.role === "admin" && editor && <WebhookConnectionEditor connection={editor.connection}
      returnFocus={editor.trigger} onClose={() => setEditor(null)}
      onSaved={(response) => { setReceipt({ response, order: ++order.current }); setEditor(null); }} />}
  </section>;
}

export function WebhookConnections({ open, onToggle }: { open: boolean; onToggle: () => void }) {
  return <div className="webhook-entry"><Button type="button" variant="outline"
    aria-expanded={open} onClick={onToggle}>Webhook connections</Button>
    {open && <WebhookConnectionPanel />}</div>;
}
