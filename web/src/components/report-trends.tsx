import { useCallback, useEffect, useState } from "react";
import type { FormEvent } from "react";
import { api, APIError } from "@/api/client";
import type { HistoricalTrend, HistoricalTrendDelta } from "@/api/types";
import { timestampLabel } from "@/lib/format";
import { useScopedAction } from "@/lib/use-scoped-action";
import { ActionButton } from "./action-button";
import { FormError } from "./form-dialog";
import { Icon } from "./icon";
import { Button } from "./ui/button";

const deltas: Array<[keyof HistoricalTrendDelta, string]> = [
  ["findings", "Findings delta"], ["openFindings", "Open findings delta"],
  ["acceptedRisk", "Accepted risk delta"], ["suppressed", "Suppressed delta"],
  ["falsePositive", "False positive delta"], ["critical", "Critical delta"],
  ["high", "High delta"], ["medium", "Medium delta"], ["low", "Low delta"],
  ["info", "Info delta"], ["scannedAssets", "Scanned assets delta"],
  ["unscannedAssets", "Unscanned assets delta"], ["staleAssets", "Stale assets delta"],
  ["unknownFreshnessAssets", "Unknown freshness delta"],
];

function signed(value: number) {
  return value > 0 ? `+${value}` : String(value);
}

function TrendPanel({ onClose }: { onClose: () => void }) {
  const action = useScopedAction();
  const run = action.run;
  const [draft, setDraft] = useState("30");
  const [trend, setTrend] = useState<HistoricalTrend | null>(null);
  const load = useCallback((days: number) => {
    void run((signal) => api.reportTrends(days, signal), (response) => setTrend(response.trend),
      (error: APIError) => {
        if (error.code === "forbidden" || error.code === "not-found") {
          setTrend(null);
        }
      });
  }, [run]);
  useEffect(() => {
    // Let React discard a development remount before the scoped action owns a request.
    const timer = window.setTimeout(() => load(30), 0);
    return () => window.clearTimeout(timer);
  }, [load]);
  function refresh(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const days = Number(draft);
    if (!event.currentTarget.reportValidity() || !Number.isInteger(days) || days < 1 || days > 365) return;
    load(days);
  }
  return <section className="surface report-panel report-trends" aria-label="Historical trends">
    <header className="report-panel-heading"><div><h2>Historical trends</h2>
      <p className="report-help">Succeeded immutable snapshot points only. Missing periods are not filled.</p></div>
      <Button type="button" variant="ghost" onClick={onClose}>Close historical trends</Button></header>
    <form className="report-refresh-form" aria-label="Refresh historical trends" onSubmit={refresh}>
      <label>Trend days<input type="number" min={1} max={365} step={1} required value={draft}
        onChange={(event) => setDraft(event.target.value)} /></label>
      <ActionButton type="submit" variant="outline" disabled={action.pending}>
        <Icon name="refresh" />Refresh trends</ActionButton>
    </form>
    <FormError error={action.error} />
    {action.pending && !trend && <p role="status">Loading historical trends.</p>}
    {trend && <>
      <p>Trend contains <strong>{trend.points.length} {trend.points.length === 1 ? "point" : "points"}</strong> from{" "}
        <time dateTime={trend.from}>{timestampLabel(trend.from)}</time> to{" "}
        <time dateTime={trend.to}>{timestampLabel(trend.to)}</time>.</p>
      <p className="report-help">{trend.verification.reason}</p>
      {trend.points.length === 0 ? <p role="status">No historical saved snapshot points were returned.</p> :
        <div className="report-history-scroll" tabIndex={0} role="region" aria-label="Historical trend results">
          <table className="report-snapshot-table" aria-label="Historical trend snapshots">
            <thead><tr><th scope="col">Snapshot</th><th scope="col">Completed</th><th scope="col">Findings</th>
              <th scope="col">Open findings</th><th scope="col">Critical</th><th scope="col">High</th>
              <th scope="col">Scanned assets</th></tr></thead>
            <tbody>{trend.points.map((point) => <tr key={point.snapshotId}>
              <td>{point.name}</td>
              <td><time dateTime={point.completedAt}>{timestampLabel(point.completedAt)}</time></td>
              <td>{point.totals.findings}</td><td>{point.totals.openFindings}</td>
              <td>{point.bySeverity.critical}</td><td>{point.bySeverity.high}</td>
              <td>{point.coverage.scannedAssets}</td>
            </tr>)}</tbody>
          </table>
        </div>}
      {trend.points.length === 1 && <p>Insufficient history for a first-to-last delta. At least two saved snapshots are required.</p>}
      {trend.points.length > 1 && <p>Points are saved snapshots. Missing days and periods are not filled or interpolated.</p>}
      <section aria-label="First-to-last deltas">
        <h3>First-to-last deltas</h3>
        {trend.delta === null ? <p>No delta is available.</p> :
          <dl className="report-counts">{deltas.map(([key, title]) =>
            <div key={key}><dt>{title}</dt><dd>{signed(trend.delta![key])}</dd></div>)}</dl>}
      </section>
    </>}
  </section>;
}

export function ReportTrends() {
  const [open, setOpen] = useState(false);
  return open ? <TrendPanel onClose={() => setOpen(false)} /> :
    <section className="surface report-panel report-trends-entry" aria-label="Historical trends entry">
      <header className="report-panel-heading"><div><h2>Historical trends</h2>
        <p className="report-help">Compare bounded immutable snapshot points without interpolation or SLA claims.</p></div>
        <Button type="button" variant="outline" onClick={() => setOpen(true)}>Historical trends</Button></header>
    </section>;
}
