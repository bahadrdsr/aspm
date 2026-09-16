import { useRef, useState } from "react";
import { api } from "@/api/client";
import type { Asset, ImportReceipt } from "@/api/types";
import { label } from "@/lib/format";
import { useResource } from "@/lib/use-resource";
import { useSession } from "@/lib/session";
import { ActionButton } from "@/components/action-button";
import { AssetEditor } from "@/components/asset-editor";
import { Icon } from "@/components/icon";
import { ImportStatus } from "@/components/import-status";
import { ReportImport } from "@/components/report-import";
import { EmptyState, ErrorState, LoadingState } from "@/components/states";
import { Button } from "@/components/ui/button";

type OpenForm = { kind: "asset"; asset: Asset | null; trigger: HTMLElement } | { kind: "import"; trigger: HTMLElement };

export function AssetsPage() {
  const resource = useResource(api.assets);
  const inventoryResults = useRef<HTMLDivElement>(null);
  const { workspace, session } = useSession();
  const [form, setForm] = useState<OpenForm | null>(null);
  const [saved, setSaved] = useState<string | null>(null);
  // Same-ID replays still start a new receipt lifetime and cancel older status reads.
  const [acknowledgement, setAcknowledgement] = useState<{ receipt: ImportReceipt; generation: number } | null>(null);
  const canWrite = workspace.role !== "viewer";
  const assets = resource.data?.items ?? [];
  return <>
    <header className="page-heading assets-heading">
      <div><p className="eyebrow">Understand ownership &amp; coverage</p><h1 id="assets-heading" tabIndex={-1}>Assets</h1>
        <p className="page-description">Manage inventory and bring existing reports into {workspace.name}.</p></div>
      <div className="heading-actions">
        {canWrite && <><ActionButton variant="outline" disabled={resource.status !== "ready" || assets.length === 0} onClick={(event) => {
          setForm({ kind: "import", trigger: event.currentTarget });
        }}><Icon name="file" />Import report</ActionButton>
          <ActionButton onClick={(event) => { setSaved(null); setForm({ kind: "asset", asset: null, trigger: event.currentTarget }); }}><Icon name="assets" />Create asset</ActionButton></>}
      </div>
    </header>
    {saved && <p className="asset-save-status" role="status"><Icon name="check" size={16} />{saved}</p>}
    {acknowledgement && <ImportStatus key={acknowledgement.generation} initial={acknowledgement.receipt} />}
    <section className="surface queue-surface" aria-label="Asset inventory">
      <div className="queue-toolbar"><div className="queue-title"><span className="section-mark" /><h2>Inventory</h2>{!canWrite && <span className="subtle-pill">Read only</span>}</div>
        <ActionButton variant="outline" onClick={resource.reload} disabled={resource.status === "loading"}><Icon name="refresh" />Refresh assets</ActionButton></div>
      {resource.error && <ErrorState error={resource.error} retry={resource.reload} stale={resource.data !== null} />}
      {resource.status === "loading" && !resource.data && <LoadingState label="Loading assets" />}
      {resource.status === "loading" && resource.data && <p className="inline-status" role="status">Refreshing inventory. Showing the last received assets.</p>}
      {resource.data && (assets.length === 0 ? <EmptyState icon="assets" title="No assets" description={canWrite ? "Create an asset to record ownership and import an existing report." : "No assets were returned for this workspace. Your current role is read only."} /> : <>
        <div ref={inventoryResults} className="table-scroll" tabIndex={0} role="region" aria-label="Asset results">
          <table aria-label="Assets" className="finding-table asset-table">
            <thead><tr><th>Asset</th><th>Environment</th><th>Criticality</th><th>Owner</th>{canWrite && <th>Actions</th>}</tr></thead>
            <tbody>{assets.map((asset) => <tr key={asset.id}>
              <td><strong className="asset-name">{asset.name}</strong><p className="asset-kind">{label(asset.kind)}</p>
                {asset.tags.length > 0 && <p className="asset-tags">{asset.tags.join(", ")}</p>}
              </td>
              <td>{asset.environment || <span className="muted">Not supplied</span>}</td>
              <td><span className={`badge severity-${asset.criticality}`}>{label(asset.criticality)}</span></td>
              <td>{asset.ownerId === null ? <span className="muted">Unassigned</span> : asset.ownerId === session.user.id ? session.user.name : <code>{asset.ownerId}</code>}</td>
              {canWrite && <td><Button variant="outline" size="sm" onClick={(event) => {
                setSaved(null); setForm({ kind: "asset", asset, trigger: event.currentTarget });
              }}>Edit asset</Button></td>}
            </tr>)}</tbody>
          </table>
        </div>
        <footer className="table-footer"><span>{assets.length.toLocaleString()} of {resource.data.total.toLocaleString()} assets loaded</span></footer>
      </>)}
      {resource.data?.nextCursor && <p className="inline-status"><Icon name="info" size={15} />More assets exist on the service. This view and its import selector currently show only this loaded page.</p>}
    </section>
    <p className="view-footnote"><Icon name="shield" size={15} />The service authorizes every change. Importing a report starts no native scan and does not verify a resolution.</p>
    {canWrite && form?.kind === "asset" && <AssetEditor asset={form.asset} returnFocus={form.trigger} savedFocus={inventoryResults} onClose={() => setForm(null)} onSaved={(asset) => {
      setForm(null); setSaved(`${asset.name} was saved by the service.`); resource.reload();
    }} />}
    {canWrite && form?.kind === "import" && <ReportImport assets={assets} returnFocus={form.trigger} onClose={() => setForm(null)} onAccepted={(value) => {
      setForm(null); setAcknowledgement((previous) => ({ receipt: value, generation: (previous?.generation ?? 0) + 1 }));
    }} />}
  </>;
}
