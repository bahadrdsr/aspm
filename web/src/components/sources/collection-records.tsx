import { useCallback, useState } from "react";
import { sourcesApi } from "@/api/sources";
import type { SourceRecord } from "@/api/source-types";
import type { ImportReceipt } from "@/api/types";
import { useSession } from "@/lib/session";
import { ActionButton } from "@/components/action-button";
import { ImportStatus } from "@/components/import-status";
import { Button } from "@/components/ui/button";
import { AzureDevOpsImportDialog } from "./azure-devops-import";
import { SourceReadState } from "./source-ui";
import { SourceEvidenceDialog } from "./source-evidence";
import { useSourcePage } from "./use-source-page";

export function CollectionRecords({ id, profile }: { id: string; profile: "github-cloud-app" | "ado-services-build-artifacts" }) {
  const { workspace } = useSession();
  const load = useCallback((cursor: string | null, signal: AbortSignal) =>
    profile === "ado-services-build-artifacts"
      ? sourcesApi.azureDevOpsRecords(id, cursor, signal)
      : sourcesApi.records(id, cursor, signal), [id, profile]);
  const page = useSourcePage(load);
  const [selected, setSelected] = useState<{ record: SourceRecord; trigger: HTMLElement } | null>(null);
  const [importing, setImporting] = useState<{ record: SourceRecord; trigger: HTMLElement } | null>(null);
  const [receipt, setReceipt] = useState<ImportReceipt | null>(null);
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
              <td><div className="source-actions"><Button type="button" variant="outline" size="sm"
                onClick={(event) => setSelected({ record, trigger: event.currentTarget })}>View evidence</Button>
                {profile === "ado-services-build-artifacts" && record.kind === "report" && workspace.role !== "viewer" &&
                  <Button type="button" size="sm" onClick={(event) => setImporting({ record, trigger: event.currentTarget })}>Import SARIF</Button>}
              </div></td>
            </tr>)}</tbody>
          </table>
        </div>}
        {page.data.nextCursor !== null && <ActionButton variant="outline" aria-disabled={page.pending} onClick={page.more}>Load more records</ActionButton>}
        <p className="form-help">Raw source records only. Collection does not normalize findings, close work or run a scan. Pages follow server IDs, not ordinal.</p>
      </>}
    </div>
    {selected && <SourceEvidenceDialog key={selected.record.id} record={selected.record} returnFocus={selected.trigger} onClose={() => setSelected(null)} />}
    {importing && <AzureDevOpsImportDialog key={importing.record.id} record={importing.record} returnFocus={importing.trigger}
      onClose={() => setImporting(null)} onAccepted={(value) => { setReceipt(value); setImporting(null); }} />}
    {receipt && <ImportStatus initial={receipt} />}
  </section>;
}
