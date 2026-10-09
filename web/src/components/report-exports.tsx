import { useCallback, useEffect, useId, useMemo, useRef, useState } from "react";
import type { FormEvent } from "react";
import { api, APIError } from "@/api/client";
import { requestAuthority } from "@/api/authorization";
import type {
  ReportExport, ReportExportFormat, ReportExportsResponse, ReportSnapshotSummary,
} from "@/api/types";
import { label, timestampLabel } from "@/lib/format";
import { useSession } from "@/lib/session";
import { ActionButton } from "./action-button";
import { FormError } from "./form-dialog";
import { Icon } from "./icon";
import { LoadingState } from "./states";
import { Button } from "./ui/button";

let exportsExpanded = false;

interface HistoryState {
  data: ReportExportsResponse | null;
  pending: boolean;
  error: APIError | null;
}

interface DetailState {
  id: string | null;
  data: ReportExport | null;
  pending: boolean;
  denied: boolean;
  error: APIError | null;
}

interface UnresolvedIntent {
  snapshotId: string;
  format: ReportExportFormat;
  idempotencyKey: string;
  knownIds: Set<string>;
}

function actionError(cause: unknown, fallback: string): APIError {
  return cause instanceof APIError ? cause : new APIError(fallback, "unavailable", true);
}

function intentKey(): string {
  return crypto.randomUUID().replaceAll("-", "");
}

export function ReportExports({ snapshots }: { snapshots: ReportSnapshotSummary[] }) {
  const { workspace } = useSession();
  const [open, setOpen] = useState(exportsExpanded);
  const [history, setHistory] = useState<HistoryState>({ data: null, pending: false, error: null });
  const [detail, setDetail] = useState<DetailState>({
    id: null, data: null, pending: false, denied: false, error: null,
  });
  const [snapshotId, setSnapshotId] = useState("");
  const [format, setFormat] = useState<ReportExportFormat>("json");
  const [createPending, setCreatePending] = useState(false);
  const [createError, setCreateError] = useState<APIError | null>(null);
  const [unresolved, setUnresolved] = useState<UnresolvedIntent | null>(null);
  const [downloadPending, setDownloadPending] = useState(false);
  const snapshotField = useId();
  const formatField = useId();
  const historyRequest = useRef<AbortController | null>(null);
  const detailRequest = useRef<AbortController | null>(null);
  const createRequest = useRef<AbortController | null>(null);
  const contentRequest = useRef<AbortController | null>(null);
  const unresolvedRef = useRef(unresolved);
  unresolvedRef.current = unresolved;

  const availableSnapshots = useMemo(
    () => snapshots.filter((snapshot) => snapshot.state === "succeeded"),
    [snapshots],
  );

  useEffect(() => {
    if (availableSnapshots.some((snapshot) => snapshot.id === snapshotId)) return;
    setSnapshotId(availableSnapshots[0]?.id ?? "");
  }, [availableSnapshots, snapshotId]);

  useEffect(() => () => {
    historyRequest.current?.abort();
    detailRequest.current?.abort();
    createRequest.current?.abort();
    contentRequest.current?.abort();
    if (requestAuthority().workspace === null) exportsExpanded = false;
  }, []);

  const selectExport = useCallback((item: ReportExport) => {
    detailRequest.current?.abort();
    contentRequest.current?.abort();
    setDownloadPending(false);
    setDetail({ id: item.id, data: item, pending: false, denied: false, error: null });
  }, []);

  const loadHistory = useCallback(async (cursor: string | null, append: boolean, controller: AbortController) => {
    historyRequest.current?.abort();
    historyRequest.current = controller;
    setHistory((previous) => ({ ...previous, pending: true, error: null }));
    try {
      const page = await api.reportExports(100, cursor, controller.signal);
      if (controller.signal.aborted || historyRequest.current !== controller) return;
      setHistory((previous) => {
        const items = append
          ? [...new Map([...(previous.data?.items ?? []), ...page.items].map((item) => [item.id, item])).values()]
          : page.items;
        return { data: { ...page, items }, pending: false, error: null };
      });
      const pending = unresolvedRef.current;
      if (pending) {
        const match = page.items.find((item) =>
          !pending.knownIds.has(item.id) &&
          item.snapshotId === pending.snapshotId && item.format === pending.format);
        if (match) {
          setUnresolved(null);
          setCreateError(null);
          selectExport(match);
        }
      }
    } catch (cause) {
      if (controller.signal.aborted || historyRequest.current !== controller) return;
      const error = actionError(cause, "Unable to load report exports. Refresh exports to try again.");
      setHistory((previous) => ({
        data: error.code === "forbidden" || error.code === "not-found" ? null : previous.data,
        pending: false,
        error,
      }));
    } finally {
      if (historyRequest.current === controller) historyRequest.current = null;
    }
  }, [selectExport]);

  useEffect(() => {
    if (!open) return;
    const controller = new AbortController();
    void loadHistory(null, false, controller);
    return () => controller.abort();
  }, [loadHistory, open, workspace.id]);

  function refreshHistory() {
    if (history.pending) return;
    void loadHistory(null, false, new AbortController());
  }

  function loadMore() {
    if (history.pending || history.data?.nextCursor === null || history.data?.nextCursor === undefined) return;
    void loadHistory(history.data.nextCursor, true, new AbortController());
  }

  async function createExport(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (createPending || unresolved || snapshotId === "") return;
    const controller = new AbortController();
    createRequest.current?.abort();
    createRequest.current = controller;
    const pending: UnresolvedIntent = {
      snapshotId, format, idempotencyKey: intentKey(),
      knownIds: new Set(history.data?.items.map((item) => item.id) ?? []),
    };
    setCreatePending(true);
    setCreateError(null);
    setHistory((previous) => ({ ...previous, error: null }));
    setDetail((previous) => ({ ...previous, error: null }));
    try {
      const response = await api.createReportExport({
        snapshotId: pending.snapshotId,
        format: pending.format,
        idempotencyKey: pending.idempotencyKey,
      }, controller.signal);
      if (controller.signal.aborted || createRequest.current !== controller) return;
      setUnresolved(null);
      selectExport(response.export);
    } catch (cause) {
      if (controller.signal.aborted || createRequest.current !== controller) return;
      const error = actionError(cause, "The report export acknowledgement was not confirmed.");
      setCreateError(error);
      if (error.code === "network" || error.code === "invalid-response" || error.httpStatus === null) {
        setUnresolved(pending);
      }
    } finally {
      if (!controller.signal.aborted && createRequest.current === controller) setCreatePending(false);
      if (createRequest.current === controller) createRequest.current = null;
    }
  }

  function refreshDetail() {
    if (detail.id === null || detail.pending) return;
    const id = detail.id;
    const controller = new AbortController();
    detailRequest.current?.abort();
    detailRequest.current = controller;
    setDetail((previous) => ({ ...previous, pending: true, error: null }));
    void api.reportExport(id, controller.signal).then(
      (response) => {
        if (controller.signal.aborted || detailRequest.current !== controller) return;
        setDetail({ id, data: response.export, pending: false, denied: false, error: null });
      },
      (cause: unknown) => {
        if (controller.signal.aborted || detailRequest.current !== controller) return;
        const error = actionError(cause, "Unable to refresh the selected report export.");
        const denied = error.code === "forbidden" || error.code === "not-found";
        setDetail((previous) => ({
          id,
          data: denied || previous.denied ? null : previous.data,
          pending: false,
          denied: denied || previous.denied,
          error,
        }));
      },
    ).finally(() => {
      if (detailRequest.current === controller) detailRequest.current = null;
    });
  }

  function downloadExport() {
    const item = detail.data;
    if (!item || item.state !== "succeeded" || downloadPending) return;
    const controller = new AbortController();
    contentRequest.current?.abort();
    contentRequest.current = controller;
    setDownloadPending(true);
    setDetail((previous) => ({ ...previous, error: null }));
    void api.reportExportContent(item, controller.signal).then(
      (artifact) => {
        if (controller.signal.aborted || contentRequest.current !== controller) return;
        const url = URL.createObjectURL(new Blob([artifact.bytes], { type: artifact.contentType }));
        const anchor = document.createElement("a");
        anchor.href = url;
        anchor.download = artifact.filename;
        document.body.append(anchor);
        anchor.click();
        anchor.remove();
        window.setTimeout(() => URL.revokeObjectURL(url), 0);
      },
      (cause: unknown) => {
        if (controller.signal.aborted || contentRequest.current !== controller) return;
        setDetail((previous) => ({
          ...previous,
          error: actionError(cause, "The report export download could not be verified."),
        }));
      },
    ).finally(() => {
      if (!controller.signal.aborted && contentRequest.current === controller) setDownloadPending(false);
      if (contentRequest.current === controller) contentRequest.current = null;
    });
  }

  if (!open) {
    return <ActionButton className="report-exports-toggle" variant="outline" onClick={() => {
      exportsExpanded = true;
      setOpen(true);
    }}><Icon name="file" />Show report exports</ActionButton>;
  }

  return <section className="surface report-panel report-exports" aria-label="Report exports">
    <header className="report-panel-heading">
      <div><h2>Report exports</h2>
        <p className="report-help">Create bounded JSON or CSV artifacts from succeeded immutable snapshots.</p></div>
      <ActionButton variant="outline" disabled={history.pending} onClick={refreshHistory}>
        <Icon name="refresh" />Refresh exports
      </ActionButton>
    </header>
    <div className="report-panel-content report-export-content">
      <FormError error={history.error} />
      <FormError error={unresolved ? null : createError} />
      {unresolved && <p role="alert" className="report-export-unresolved">
        The export acknowledgement was not confirmed. This intent is unresolved until Refresh exports finds its receipt.
      </p>}
      {workspace.role !== "viewer" && <form className="report-export-form" aria-label="Create report export"
        onSubmit={(event) => { void createExport(event); }}>
        <div className="report-export-field"><label htmlFor={snapshotField}>Saved snapshot</label>
          <select id={snapshotField} value={snapshotId} required disabled={createPending || unresolved !== null}
            onChange={(event) => setSnapshotId(event.target.value)}>
            {availableSnapshots.length === 0 && <option value="">No succeeded snapshots loaded</option>}
            {availableSnapshots.map((snapshot) =>
              <option key={snapshot.id} value={snapshot.id}>{snapshot.name}</option>)}
          </select></div>
        <div className="report-export-field"><label htmlFor={formatField}>Export format</label>
          <select id={formatField} value={format} disabled={createPending || unresolved !== null}
            onChange={(event) => setFormat(event.target.value as ReportExportFormat)}>
            <option value="json">JSON</option><option value="csv">CSV</option>
          </select></div>
        <ActionButton type="submit" disabled={createPending || unresolved !== null || snapshotId === ""}>
          <Icon name="file" />{createPending ? "Creating export..." : "Create export"}
        </ActionButton>
      </form>}
      {history.pending && !history.data && <LoadingState label="Loading report exports" />}
      {history.pending && history.data && <p className="report-help" role="status">
        Loading exports. Previously received rows remain available.</p>}
      {history.error && history.data && <p className="report-help">
        Export history could not be refreshed. Previously received rows and the continuation are unchanged.</p>}
      {history.data && <div className="report-export-history">
        <p className="report-history-summary">{history.data.items.length.toLocaleString("en-US")} of{" "}
          {history.data.total.toLocaleString("en-US")} exports loaded</p>
        <div className="report-history-scroll" tabIndex={0} role="region" aria-label="Report export history results">
          <table className="report-snapshot-table report-export-table" aria-label="Report exports">
            <thead><tr><th scope="col">Export</th><th scope="col">State</th><th scope="col">Action</th></tr></thead>
            <tbody>{history.data.items.map((item) => <tr key={item.id}>
              <td><code>{item.id}</code><strong className="report-history-name">{item.snapshotName}</strong>
                <time dateTime={item.createdAt}>{timestampLabel(item.createdAt)}</time></td>
              <td>{label(item.state)}<span className="report-export-format">{item.format.toUpperCase()}</span></td>
              <td><Button type="button" variant="outline" size="sm" onClick={() => selectExport(item)}>
                Open export
              </Button></td>
            </tr>)}</tbody>
          </table>
        </div>
        {history.data.nextCursor !== null && <footer className="report-history-footer">
          <ActionButton variant="outline" disabled={history.pending} onClick={loadMore}>
            Load more exports<Icon name="chevron" />
          </ActionButton>
        </footer>}
      </div>}
      <section className="report-export-detail" aria-label="Selected report export">
        <header><div><h3>Selected report export</h3>
          <p className="report-help">Refresh explicitly to read the current durable worker state.</p></div>
          {detail.id && <ActionButton variant="outline" disabled={detail.pending} onClick={refreshDetail}>
            <Icon name="refresh" />Refresh export
          </ActionButton>}
        </header>
        <FormError error={detail.error} />
        {detail.pending && <p className="report-help" role="status">Refreshing the selected export.</p>}
        {!detail.id && <p className="report-help">Open an export from history or create a new one.</p>}
        {detail.id && detail.data === null && !detail.pending && <p className="report-help">
          Current metadata is withheld until an authorized refresh succeeds.</p>}
        {detail.data && <div className="report-export-receipt">
          <code>{detail.data.id}</code>
          <p>{detail.data.snapshotName}</p>
          <div className={`report-snapshot-state report-state-${detail.data.state}`}
            role={detail.data.state === "failed" ? "alert" : "status"} aria-label="Export status">
            <strong>{label(detail.data.state)}</strong>
            {detail.data.state === "queued" && <p>The durable export is queued. No artifact is available yet.</p>}
            {detail.data.state === "processing" && <p>The report worker is generating the artifact.</p>}
            {detail.data.failure && <p><code>{detail.data.failure.code}</code><br />{detail.data.failure.message}</p>}
            {detail.data.state === "succeeded" && <p>The exact artifact is ready for verified download.</p>}
          </div>
          {detail.data.state === "succeeded" && <dl className="report-export-facts">
            <div><dt>Digest</dt><dd>{detail.data.digest}</dd></div>
            <div><dt>Size bytes</dt><dd>{detail.data.sizeBytes}</dd></div>
            <div><dt>Filename</dt><dd>{detail.data.filename}</dd></div>
          </dl>}
          {detail.data.state === "succeeded" && <ActionButton disabled={downloadPending} onClick={downloadExport}>
            <Icon name="arrow" />{downloadPending ? "Verifying download..." : "Download"}
          </ActionButton>}
        </div>}
      </section>
    </div>
  </section>;
}
