import { useCallback, useState } from "react";
import { jiraApi } from "@/api/jira";
import { useResource } from "@/lib/use-resource";
import { label, timestampLabel } from "@/lib/format";
import { ActionButton } from "./action-button";
import { FormError } from "./form-dialog";
import { LoadingState } from "./states";
import { Button } from "./ui/button";

export function JiraDeliveryHistory({ findingId, onSelect }: { findingId: string; onSelect: (id: string) => void }) {
  const [cursor, setCursor] = useState<string | null>(null);
  const load = useCallback((signal: AbortSignal) => jiraApi.history(findingId, cursor, signal), [findingId, cursor]);
  const resource = useResource(load), page = resource.data;
  function refresh() { if (cursor !== null) setCursor(null); else resource.reload(); }
  return <section className="surface slack-panel" aria-label="Jira work item history">
    <header className="slack-panel-heading"><h3>Jira work item history</h3>
      <ActionButton variant="outline" disabled={resource.status === "loading"} onClick={refresh}>Refresh Jira history</ActionButton></header>
    <div className="slack-panel-content">
      <FormError error={resource.error} />
      {resource.error && <ActionButton variant="outline" onClick={resource.reload}>Retry Jira history</ActionButton>}
      {resource.status === "loading" && !page && <LoadingState label="Loading Jira work item history" />}
      {page && <>
        <p className="form-help">{page.total.toLocaleString("en-US")} Jira work items at the last native read; {page.items.length} shown.</p>
        <div className="slack-table-scroll" role="region" aria-label="Jira history results" tabIndex={0}>
          <table className="slack-table" aria-label="Jira work items">
            <thead><tr><th scope="col">Work item</th><th scope="col">Action</th></tr></thead>
            <tbody>{page.items.map((item) => <tr key={item.id}>
              <td><code>{item.id}</code><p>{label(item.state)}</p><p><time dateTime={item.createdAt}>{timestampLabel(item.createdAt)}</time></p></td>
              <td><Button type="button" variant="outline" size="sm" onClick={() => onSelect(item.id)}>Open Jira work item</Button></td>
            </tr>)}</tbody>
          </table>
        </div>
        {page.items.length === 0 && <p className="form-help">No Jira work items were returned.</p>}
        {page.nextCursor !== null && <ActionButton variant="outline" disabled={resource.status === "loading"}
          onClick={() => setCursor(page.nextCursor)}>Next Jira work items</ActionButton>}
      </>}
      <p className="form-help">Manual scoped history reads only. Work item states do not change finding workflow, ownership, evidence or verification.</p>
    </div>
  </section>;
}
