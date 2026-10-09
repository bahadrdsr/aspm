import { useEffect, useState } from "react";
import { api, APIError } from "@/api/client";
import type {
  FindingMetric, FindingMetricResponse, PostureReport,
} from "@/api/types";
import { label, timestampLabel } from "@/lib/format";
import { ActionButton } from "./action-button";
import { FormError } from "./form-dialog";
import { Icon } from "./icon";
import { DataNotice, LoadingState } from "./states";
import { Button } from "./ui/button";

const metricLabels: Record<FindingMetric, string> = {
  findings: "Findings", "open-findings": "Open findings",
  "accepted-risk": "Accepted risk", "expired-accepted-risk": "Expired accepted risk",
  suppressed: "Suppressed", "expired-suppression": "Expired suppression",
  "false-positive": "False positive", "inferred-resolved": "Inferred resolved",
  critical: "Critical", high: "High", medium: "Medium", low: "Low", info: "Info",
};

interface ViewState {
  data: FindingMetricResponse | null;
  pending: boolean;
  error: APIError | null;
}

export function ReportFindingMetrics({ metric, report, onClose }: {
  metric: FindingMetric;
  report: PostureReport;
  onClose: () => void;
}) {
  const [read, setRead] = useState({ cursor: null as string | null, revision: 0 });
  const [view, setView] = useState<ViewState>({ data: null, pending: true, error: null });
  useEffect(() => {
    const controller = new AbortController();
    let current = true;
    setView((previous) => ({ ...previous, pending: true, error: null }));
    void api.reportFindingMetrics(metric, 100, read.cursor, report, controller.signal).then(
      (response) => {
        if (!current) return;
        setView((previous) => {
          const items = read.cursor === null ? response.drilldown.items :
            [...new Map([...(previous.data?.drilldown.items ?? []), ...response.drilldown.items]
              .map((item) => [item.findingId, item])).values()];
          return {
            data: { ...response, drilldown: { ...response.drilldown, items } },
            pending: false, error: null,
          };
        });
      },
      (cause: unknown) => {
        if (!current || controller.signal.aborted) return;
        const error = cause instanceof APIError ? cause :
          new APIError("Unable to load current finding membership. Please refresh.", "unavailable", true);
        setView((previous) => ({
          data: error.code === "forbidden" || error.code === "not-found" ? null : previous.data,
          pending: false, error,
        }));
      },
    );
    return () => { current = false; controller.abort(); };
  }, [metric, read, report]);
  function readPage(cursor: string | null) {
    if (view.pending) return;
    setView((previous) => ({ ...previous, pending: true, error: null }));
    setRead((previous) => ({ cursor, revision: previous.revision + 1 }));
  }
  const drilldown = view.data?.drilldown;
  return <section className="surface report-panel report-finding-metrics"
    aria-label="Current live finding membership">
    <header className="report-panel-heading"><div><h2>Current live finding membership</h2>
      <p className="report-help">Visible canonical findings behind one displayed Live overview metric.</p></div>
      <Button type="button" variant="ghost" onClick={onClose}>Close finding membership</Button></header>
    <div className="report-panel-content">
      <div className="report-coverage-actions"><div><strong>{metricLabels[metric]}</strong>
        <p className="report-help">Current membership only. Saved aggregates do not retain these rows.</p></div>
        <ActionButton type="button" variant="outline" disabled={view.pending} onClick={() => readPage(null)}>
          <Icon name="refresh" />Refresh finding membership</ActionButton></div>
      <FormError error={view.error} />
      {view.pending && !drilldown && <LoadingState label="Loading current finding membership" />}
      {view.pending && drilldown && <p className="report-help" role="status">
        Loading current findings. Previously received rows remain available.</p>}
      {view.error && drilldown && <p className="report-help">
        Finding membership could not be refreshed. Previously received rows remain available.</p>}
      {drilldown && <>
        <div className="report-history-summary"><span>Finding page contains{" "}
          {drilldown.items.length.toLocaleString("en-US")} of {drilldown.total.toLocaleString("en-US")} matching findings.</span>
          <DataNotice origin={view.data!.dataOrigin} /></div>
        <p className="report-help">As of{" "}
          <time dateTime={drilldown.asOf}>{timestampLabel(drilldown.asOf)}</time>.{" "}
          {drilldown.verification.reason}</p>
        <div className="report-history-scroll" tabIndex={0} role="region" aria-label="Finding membership results">
          <table className="report-snapshot-table report-finding-metric-table"
            aria-label="Current finding membership">
            <thead><tr><th scope="col">Finding</th><th scope="col">Asset</th><th scope="col">Severity</th>
              <th scope="col">Owner</th><th scope="col">Workflow</th><th scope="col">Disposition</th>
              <th scope="col">Accepted risk expiry</th><th scope="col">Risk acceptance expired</th>
              <th scope="col">Source state</th><th scope="col">Source freshness</th></tr></thead>
            <tbody>{drilldown.items.map((item) => <tr key={item.findingId}>
              <td><strong>{item.title}</strong><code>{item.findingId}</code></td>
              <td>{item.assetName}<code>{item.assetId}</code></td>
              <td>{label(item.severity)}</td>
              <td>{item.ownerName === null ? "Unassigned" : <>{item.ownerName}<code>{item.ownerId}</code></>}</td>
              <td>{label(item.workflowState)}</td><td>{label(item.disposition)}</td>
              <td>{item.acceptedRiskExpiresAt === null ? "No expiry" :
                <time dateTime={item.acceptedRiskExpiresAt}>{timestampLabel(item.acceptedRiskExpiresAt)}</time>}</td>
              <td>{item.riskAcceptanceExpired ? "Yes" : "No"}</td>
              <td>{label(item.sourceState)}</td>
              <td>{item.sourceFreshnessAt === null ? "No known source freshness" :
                <time dateTime={item.sourceFreshnessAt}>{timestampLabel(item.sourceFreshnessAt)}</time>}</td>
            </tr>)}</tbody>
          </table>
        </div>
        {drilldown.nextCursor !== null && <footer className="report-history-footer">
          <ActionButton type="button" variant="outline" disabled={view.pending}
            onClick={() => readPage(drilldown.nextCursor)}>
            Load more finding membership<Icon name="chevron" /></ActionButton></footer>}
      </>}
    </div>
  </section>;
}
