import { useCallback, useState } from "react";
import { teamsApi } from "@/api/teams";
import { useResource } from "@/lib/use-resource";
import { label, timestampLabel } from "@/lib/format";
import { ActionButton } from "./action-button";
import { FormError } from "./form-dialog";
import { LoadingState } from "./states";
import { Button } from "./ui/button";

export function TeamsDeliveryHistory({ findingId, onSelect }: { findingId: string; onSelect: (id: string) => void }) {
  const [cursor, setCursor] = useState<string | null>(null);
  const load = useCallback((signal: AbortSignal) => teamsApi.history(findingId, cursor, signal), [findingId, cursor]);
  const resource = useResource(load), page = resource.data;
  function refresh() { if (cursor !== null) setCursor(null); else resource.reload(); }
  return <section className="surface slack-panel" aria-label="Teams notification history">
    <header className="slack-panel-heading"><h3>Teams notification history</h3>
      <ActionButton variant="outline" disabled={resource.status === "loading"} onClick={refresh}>Refresh Teams history</ActionButton></header>
    <div className="slack-panel-content">
      <FormError error={resource.error} />
      {resource.error && <ActionButton variant="outline" onClick={resource.reload}>Retry Teams history</ActionButton>}
      {resource.status === "loading" && !page && <LoadingState label="Loading Teams notification history" />}
      {page && <>
        <p className="form-help">{page.total} Teams notifications at the last native read; {page.items.length} shown.</p>
        <div className="slack-table-scroll" role="region" aria-label="Teams history results" tabIndex={0}>
          <table className="slack-table" aria-label="Teams notifications">
            <thead><tr><th scope="col">Notification</th><th scope="col">Action</th></tr></thead>
            <tbody>{page.items.map((item) => <tr key={item.id}>
              <td><code>{item.id}</code><p>{label(item.state)}</p>
                <p><time dateTime={item.createdAt}>{timestampLabel(item.createdAt)}</time></p></td>
              <td><Button type="button" variant="outline" size="sm" onClick={() => onSelect(item.id)}>Open Teams notification</Button></td>
            </tr>)}</tbody>
          </table>
        </div>
        {page.items.length === 0 && <p className="form-help">No Teams notifications were returned.</p>}
        {page.nextCursor !== null && <ActionButton variant="outline" disabled={resource.status === "loading"}
          onClick={() => setCursor(page.nextCursor)}>Next Teams notifications</ActionButton>}
      </>}
      <p className="form-help">Manual scoped history reads only. No polling or automatic sends; notifications do not change findings.</p>
    </div>
  </section>;
}
