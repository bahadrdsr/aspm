import { useEffect, useState } from "react";
import { api, APIError } from "@/api/client";
import type {
  CoverageAssetDrilldownResponse, CoverageAssetState,
} from "@/api/types";
import { label, timestampLabel } from "@/lib/format";
import { ActionButton } from "./action-button";
import { FormError } from "./form-dialog";
import { Icon } from "./icon";
import { DataNotice, LoadingState } from "./states";
import { Button } from "./ui/button";

const stateLabels: Record<CoverageAssetState, string> = {
  scanned: "Scanned assets",
  unscanned: "Unscanned assets",
  stale: "Stale assets",
  "unknown-freshness": "Unknown freshness",
};

interface ReadState {
  cursor: string | null;
  revision: number;
}

interface CoverageState {
  data: CoverageAssetDrilldownResponse | null;
  pending: boolean;
  error: APIError | null;
}

export function ReportCoverageAssets({ state, days, onClose }: {
  state: CoverageAssetState;
  days: number;
  onClose: () => void;
}) {
  const [read, setRead] = useState<ReadState>({ cursor: null, revision: 0 });
  const [coverage, setCoverage] = useState<CoverageState>({ data: null, pending: true, error: null });
  useEffect(() => {
    const controller = new AbortController();
    let current = true;
    setCoverage((previous) => ({ ...previous, pending: true, error: null }));
    void api.reportCoverageAssets(state, days, 100, read.cursor, controller.signal).then(
      (response) => {
        if (!current) return;
        setCoverage((previous) => {
          const items = read.cursor === null ? response.drilldown.items :
            [...new Map([...(previous.data?.drilldown.items ?? []), ...response.drilldown.items]
              .map((item) => [item.asset.id, item])).values()];
          return {
            data: { ...response, drilldown: { ...response.drilldown, items } },
            pending: false, error: null,
          };
        });
      },
      (cause: unknown) => {
        if (!current || controller.signal.aborted) return;
        const error = cause instanceof APIError ? cause :
          new APIError("Unable to load current coverage membership. Please refresh.", "unavailable", true);
        setCoverage((previous) => ({
          data: error.code === "forbidden" || error.code === "not-found" ? null : previous.data,
          pending: false, error,
        }));
      },
    );
    return () => { current = false; controller.abort(); };
  }, [days, read, state]);
  function readPage(cursor: string | null) {
    if (coverage.pending) return;
    setCoverage((previous) => ({ ...previous, pending: true, error: null }));
    setRead((previous) => ({ cursor, revision: previous.revision + 1 }));
  }
  const drilldown = coverage.data?.drilldown;
  return <section className="surface report-panel report-coverage-assets" aria-label="Current live asset membership">
    <header className="report-panel-heading"><div><h2>Current live asset membership</h2>
      <p className="report-help">Current asset membership for one Live overview coverage count.</p></div>
      <Button type="button" variant="ghost" onClick={onClose}>Close asset membership</Button></header>
    <div className="report-panel-content">
      <div className="report-coverage-actions">
        <div><strong>{stateLabels[state]}</strong>
          <p className="report-help">Manual pages only. Saved snapshots cannot claim this current membership.</p></div>
        <ActionButton type="button" variant="outline" disabled={coverage.pending} onClick={() => readPage(null)}>
          <Icon name="refresh" />Refresh coverage assets</ActionButton>
      </div>
      <FormError error={coverage.error} />
      {coverage.error?.code === "forbidden" && <p className="report-help">
        Access restricted. Ask your administrator to review report permissions.</p>}
      {coverage.pending && !drilldown && <LoadingState label="Loading coverage asset membership" />}
      {coverage.pending && drilldown && <p className="report-help" role="status">
        Loading coverage assets. Previously received rows remain available.</p>}
      {coverage.error && drilldown && <p className="report-help">
        Coverage membership could not be refreshed. Previously received rows are unchanged.</p>}
      {drilldown && <>
        <div className="report-history-summary">
          <span>Coverage page contains {drilldown.items.length.toLocaleString("en-US")} of{" "}
            {drilldown.total.toLocaleString("en-US")} matching assets.</span>
          <DataNotice origin={coverage.data!.dataOrigin} />
        </div>
        <p className="report-help">Freshness window: {drilldown.freshnessWindow.days} days,{" "}
          <time dateTime={drilldown.freshnessWindow.from}>{timestampLabel(drilldown.freshnessWindow.from)}</time>
          {" "}to{" "}
          <time dateTime={drilldown.freshnessWindow.to}>{timestampLabel(drilldown.freshnessWindow.to)}</time>.
        </p>
        <p className="report-help">{drilldown.verification.reason}</p>
        <div className="report-history-scroll" tabIndex={0} role="region" aria-label="Coverage asset results">
          <table className="report-snapshot-table report-coverage-table" aria-label="Coverage assets">
            <thead><tr><th scope="col">Asset</th><th scope="col">Scanned</th><th scope="col">Stale</th>
              <th scope="col">Unknown freshness</th><th scope="col">Latest known source scan</th></tr></thead>
            <tbody>{drilldown.items.map((item) => <tr key={item.asset.id}>
              <td><strong className="report-history-name">{item.asset.name}</strong>
                <p className="report-history-meta"><span>{item.asset.kind}</span>
                  <span>{item.asset.environment || "No environment"}</span>
                  <span>{label(item.asset.criticality)}</span></p>
                <code>{item.asset.id}</code></td>
              <td>{item.coverage.scanned ? "Yes" : "No"}</td>
              <td>{item.coverage.stale ? "Yes" : "No"}</td>
              <td>{item.coverage.unknownFreshness ? "Yes" : "No"}</td>
              <td>{item.coverage.latestSourceScanAt === null ? "No known source scan" :
                <time dateTime={item.coverage.latestSourceScanAt}>
                  {timestampLabel(item.coverage.latestSourceScanAt)}
                </time>}</td>
            </tr>)}</tbody>
          </table>
        </div>
        {drilldown.nextCursor !== null && <footer className="report-history-footer">
          <ActionButton type="button" variant="outline" disabled={coverage.pending}
            onClick={() => readPage(drilldown.nextCursor)}>
            Load more coverage assets<Icon name="chevron" /></ActionButton></footer>}
      </>}
    </div>
  </section>;
}
