import { useCallback, useState } from "react";
import { sourcesApi } from "@/api/sources";
import type { SourceRecord } from "@/api/source-types";
import { ActionButton } from "@/components/action-button";
import { Button } from "@/components/ui/button";
import { SourceReadState } from "./source-ui";
import { SourceEvidenceDialog } from "./source-evidence";
import { useSourcePage } from "./use-source-page";

export function CollectionRecords({ id }: { id: string }) {
  const load = useCallback((cursor: string | null, signal: AbortSignal) => sourcesApi.records(id, cursor, signal), [id]);
  const page = useSourcePage(load);
  const [selected, setSelected] = useState<{ record: SourceRecord; trigger: HTMLElement } | null>(null);
  return <section className="source-panel surface" aria-label="Records">
    <header className="source-panel-heading"><h3>Records</h3>
      <ActionButton variant="outline" aria-disabled={page.pending} onClick={page.refresh}>Refresh records</ActionButton></header>
    <div className="source-panel-content">
      <SourceReadState error={page.error} pending={page.pending} loaded={page.data !== null} retry={page.retry} subject="records" />
      {page.data && <>
        <p className="form-help">Loaded: {page.data.items.length} records in this page; {page.data.total} total at the last read.</p>
        {page.data.items.length === 0 ? <p>No records in this page.</p> : <div className="source-table-scroll" tabIndex={0} role="region" aria-label="Record results">
          <table className="source-table" aria-label="Records"><thead><tr><th scope="col">Raw record</th><th scope="col">Action</th></tr></thead>
            <tbody>{page.data.items.map((record) => <tr key={record.id}>
              <td><code>{record.id}</code><p>{record.kind} - external ID {record.externalId}</p><p>Ordinal: {record.ordinal}</p>
                <p>{record.state || "No native state supplied"}{record.severity && ` / ${record.severity}`}</p></td>
              <td><Button type="button" variant="outline" size="sm" onClick={(event) => setSelected({ record, trigger: event.currentTarget })}>View evidence</Button></td>
            </tr>)}</tbody>
          </table>
        </div>}
        {page.data.nextCursor !== null && <ActionButton variant="outline" aria-disabled={page.pending} onClick={page.more}>Load more records</ActionButton>}
        <p className="form-help">Raw source records only. Collection does not normalize findings, close work or run a scan. Pages follow server IDs, not ordinal.</p>
      </>}
    </div>
    {selected && <SourceEvidenceDialog key={selected.record.id} record={selected.record} returnFocus={selected.trigger} onClose={() => setSelected(null)} />}
  </section>;
}
