import { useCallback, useEffect, useState } from "react";
import type { FormEvent } from "react";
import { api, APIError } from "@/api/client";
import type { ReportSnapshotResponse, ReportSnapshotsResponse } from "@/api/types";
import { label, timestampLabel } from "@/lib/format";
import { useResource } from "@/lib/use-resource";
import { useSession } from "@/lib/session";
import { ActionButton } from "@/components/action-button";
import { FormError } from "@/components/form-dialog";
import { Icon } from "@/components/icon";
import { ReportMetrics } from "@/components/report-metrics";
import { ReportSnapshotCreate } from "@/components/report-snapshot-create";
import { ReportSnapshotDetail } from "@/components/report-snapshot-detail";
import { DataNotice, LoadingState } from "@/components/states";
import { Button } from "@/components/ui/button";
import "./reports.css";

function LiveOverview({ days, applyDays }: { days: number; applyDays: (days: number) => void }) {
  const [draft, setDraft] = useState(String(days));
  const load = useCallback((signal: AbortSignal) => api.reportOverview(days, signal), [days]);
  const resource = useResource(load);
  function refresh(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const value = Number(draft);
    if (!event.currentTarget.reportValidity() || !Number.isInteger(value) || value < 1 || value > 365) return;
    if (value === days) resource.reload();
    else applyDays(value);
  }
  return <section className="surface report-panel report-live" aria-label="Live overview">
    <header className="report-panel-heading">
      <div><h2>Live overview</h2><p className="report-help">Current workspace posture, calculated by the service.</p></div>
      <form className="report-refresh-form" aria-label="Refresh report" onSubmit={refresh}>
        <label>Freshness days<input type="number" name="freshnessDays" min={1} max={365} step={1} required value={draft}
          onChange={(event) => setDraft(event.target.value)} /></label>
        <ActionButton type="submit" variant="outline" disabled={resource.status === "loading"}><Icon name="refresh" />Refresh report</ActionButton>
      </form>
    </header>
    <div className="report-panel-content">
      <FormError error={resource.error} />
      {resource.error?.code === "forbidden" && <p className="report-help">Access restricted. Ask your administrator to review your report permissions.</p>}
      {resource.status === "loading" && !resource.data && <LoadingState label="Loading report overview" />}
      {resource.data && <>
        {resource.status !== "ready" && <p className="report-help" role="status">
          {resource.status === "loading" ? "Refreshing report." : "Report refresh failed."} Showing the last received report and its original timestamps.</p>}
        <ReportMetrics report={resource.data.report} origin={resource.data.dataOrigin} />
      </>}
    </div>
  </section>;
}

interface HistoryState { data: ReportSnapshotsResponse | null; pending: boolean; error: APIError | null }

function SavedSnapshots({ onSelect, refreshRevision }: { onSelect: (id: string) => void; refreshRevision: number }) {
  const [read, setRead] = useState<{ cursor: string | null; revision: number }>({ cursor: null, revision: 0 });
  const [history, setHistory] = useState<HistoryState>({ data: null, pending: true, error: null });
  useEffect(() => {
    const controller = new AbortController();
    let current = true;
    setHistory((previous) => ({ ...previous, pending: true, error: null }));
    void api.reportSnapshots(100, read.cursor, controller.signal).then(
      (page) => {
        if (!current) return;
        setHistory((previous) => {
          const items = read.cursor === null ? page.items :
            [...new Map([...(previous.data?.items ?? []), ...page.items].map((item) => [item.id, item])).values()];
          return { data: { ...page, items }, pending: false, error: null };
        });
      },
      (cause: unknown) => {
        if (!current || controller.signal.aborted) return;
        const error = cause instanceof APIError ? cause : new APIError("Unable to load saved snapshots. Please refresh.", "unavailable", true);
        setHistory((previous) => ({
          data: error.code === "forbidden" || error.code === "not-found" || error.code === "invalid-response" ? null : previous.data,
          pending: false, error,
        }));
      },
    );
    return () => { current = false; controller.abort(); };
  }, [read]);
  useEffect(() => {
    if (refreshRevision > 0) setRead((previous) => ({ cursor: null, revision: previous.revision + 1 }));
  }, [refreshRevision]);
  function readPage(cursor: string | null) {
    if (history.pending) return;
    setHistory((previous) => ({ ...previous, pending: true, error: null }));
    setRead((previous) => ({ cursor, revision: previous.revision + 1 }));
  }
  return <section className="surface report-panel report-history" aria-label="Saved snapshots">
    <header className="report-panel-heading"><h2>Saved snapshots</h2>
      <ActionButton variant="outline" disabled={history.pending} onClick={() => readPage(null)}><Icon name="refresh" />Refresh history</ActionButton></header>
    <div className="report-history-notice">
      <FormError error={history.error} />
      {history.error?.code === "forbidden" && <p className="report-help">Access restricted. Ask your administrator to review snapshot permissions.</p>}
      {history.pending && !history.data && <LoadingState label="Loading saved snapshots" />}
      {history.pending && history.data && <p className="report-help" role="status">Loading snapshots. Previously received rows remain available.</p>}
      {history.error && history.data && <p className="report-help">History could not be refreshed. Previously received rows are unchanged; retry the same continuation below.</p>}
    </div>
    {history.data && <>
      <div className="report-history-summary"><span>{history.data.items.length.toLocaleString("en-US")} of {history.data.total.toLocaleString("en-US")} snapshots loaded</span>
        <DataNotice origin={history.data.dataOrigin} /></div>
      {history.data.items.length === 0 ? <div className="report-history-empty"><Icon name="reports" size={24} /><h3>No saved snapshots</h3>
        <p className="report-help">The service returned an empty history for this workspace. New snapshots appear only after the service accepts them.</p></div> :
        <div className="report-history-scroll" tabIndex={0} role="region" aria-label="Snapshot history results">
          <table className="report-snapshot-table" aria-label="Snapshots">
            <thead><tr><th scope="col">Snapshot</th><th scope="col">Action</th></tr></thead>
            <tbody>{history.data.items.map((snapshot) => <tr key={snapshot.id}>
              <td><strong className="report-history-name">{snapshot.name}</strong>
                <p className="report-history-meta"><span>{label(snapshot.state)}</span><span>{snapshot.freshnessDays} days</span></p>
                <time dateTime={snapshot.createdAt}>{timestampLabel(snapshot.createdAt)}</time></td>
              <td><Button type="button" variant="outline" size="sm" onClick={() => onSelect(snapshot.id)}>Open snapshot</Button></td>
            </tr>)}</tbody>
          </table>
        </div>}
      {history.data.nextCursor !== null && <footer className="report-history-footer"><ActionButton variant="outline" disabled={history.pending}
        onClick={() => readPage(history.data!.nextCursor)}>Load more snapshots<Icon name="chevron" /></ActionButton></footer>}
    </>}
  </section>;
}

interface Selection { id: string; initial: ReportSnapshotResponse | null; generation: number }

export function ReportsPage() {
  const { workspace } = useSession();
  const [days, setDays] = useState(7);
  const [form, setForm] = useState<{ trigger: HTMLElement } | null>(null);
  const [selection, setSelection] = useState<Selection | null>(null);
  const [historyRevision, setHistoryRevision] = useState(0);
  const canWrite = workspace.role !== "viewer";
  function select(id: string, initial: ReportSnapshotResponse | null = null) {
    setSelection((previous) => ({ id, initial, generation: (previous?.generation ?? 0) + 1 }));
  }
  return <div className="reports-page">
    <header className="page-heading">
      <div><p className="eyebrow">Make posture explainable</p><h1 id="reports-heading" tabIndex={-1}>Reports</h1>
        <p className="page-description">Understand coverage now. Keep an immutable snapshot for later.</p></div>
      <div className="heading-actions">{canWrite ?
        <ActionButton onClick={(event) => setForm({ trigger: event.currentTarget })}><Icon name="reports" />Create snapshot</ActionButton> :
        <span className="subtle-pill">Read only</span>}</div>
    </header>
    <LiveOverview days={days} applyDays={setDays} />
    <div className="report-saved-layout">
      {selection ? <ReportSnapshotDetail key={selection.generation} id={selection.id} initial={selection.initial} /> :
        <section className="surface report-panel report-selected" aria-label="Selected snapshot">
          <header className="report-panel-heading"><h2>Selected snapshot</h2></header>
          <div className="report-panel-content report-selection-empty"><Icon name="file" size={24} /><h3>A saved point in time</h3>
            <p className="report-help">Open a snapshot from history to see its actual worker state and saved report. Refreshing the live overview never changes a saved result.</p></div>
        </section>}
      <SavedSnapshots onSelect={select} refreshRevision={historyRevision} />
    </div>
    <p className="view-footnote"><Icon name="shield" size={15} />The service authorizes each read and creation. Snapshots do not run scans or independently verify resolutions. No trends or report exports are generated here.</p>
    {canWrite && form && <ReportSnapshotCreate freshnessDays={days} returnFocus={form.trigger} onClose={() => setForm(null)}
      onAccepted={(response) => { setForm(null); select(response.snapshot.id, response); setHistoryRevision((value) => value + 1); }} />}
  </div>;
}
