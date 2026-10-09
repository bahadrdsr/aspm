import type {
  CoverageAssetState, DataOrigin, FindingMetric, PostureReport,
} from "@/api/types";
import { timestampLabel } from "@/lib/format";
import { DataNotice } from "./states";
import { Icon } from "./icon";

const mainTotals = [
  ["assets", "Assets", null], ["findings", "Findings", "findings"],
  ["openFindings", "Open findings", "open-findings"],
] as const;
const resolutionTotals = [
  ["acceptedRisk", "Accepted risk", "accepted-risk"],
  ["expiredAcceptedRisk", "Expired accepted risk", "expired-accepted-risk"],
  ["suppressed", "Suppressed", "suppressed"],
  ["expiredSuppression", "Expired suppression", "expired-suppression"],
  ["falsePositive", "False positive", "false-positive"],
  ["inferredResolved", "Inferred resolved", "inferred-resolved"],
  ["verifiedResolved", "Verified resolved", null],
] as const;
const severities = [
  ["critical", "Critical", "critical"], ["high", "High", "high"],
  ["medium", "Medium", "medium"], ["low", "Low", "low"], ["info", "Info", "info"],
] as const;
const coverage = [
  ["scannedAssets", "Scanned assets", "scanned"], ["unscannedAssets", "Unscanned assets", "unscanned"],
  ["staleAssets", "Stale assets", "stale"], ["unknownFreshnessAssets", "Unknown freshness", "unknown-freshness"],
] as const;

export function ReportMetrics({ report, origin, onCoverageSelect, onFindingMetricSelect }: {
  report: PostureReport;
  origin: DataOrigin;
  onCoverageSelect?: (state: CoverageAssetState, trigger: HTMLButtonElement) => void;
  onFindingMetricSelect?: (metric: FindingMetric, trigger: HTMLButtonElement) => void;
}) {
  return <div className="report-metrics">
    <div className="report-as-of"><p>As of <time dateTime={report.asOf}>{timestampLabel(report.asOf)}</time></p><DataNotice origin={origin} /></div>
    {report.totals.assets === 0 && report.totals.findings === 0 &&
      <p className="report-empty">No assets or findings were returned for this workspace. These are the service's empty totals, not a completed scan.</p>}
    <dl className="report-counts report-main-totals">
      {mainTotals.map(([key, title, metric]) => <div key={key}><dt>{title}</dt><dd>
        {onFindingMetricSelect && metric ? <button type="button" className="report-metric-drilldown"
          aria-label={`View ${title}`} onClick={(event) => onFindingMetricSelect(metric, event.currentTarget)}>
          {report.totals[key].toLocaleString("en-US")}</button> : report.totals[key].toLocaleString("en-US")}
      </dd></div>)}
    </dl>
    <div className="report-metric-group">
      <h3>Risk &amp; resolution</h3>
      <dl className="report-counts report-resolution-totals">
        {resolutionTotals.map(([key, title, metric]) => <div key={key}><dt>{title}</dt><dd>
          {onFindingMetricSelect && metric ? <button type="button" className="report-metric-drilldown"
            aria-label={`View ${title}`} onClick={(event) => onFindingMetricSelect(metric, event.currentTarget)}>
            {report.totals[key].toLocaleString("en-US")}</button> : report.totals[key].toLocaleString("en-US")}
        </dd></div>)}
      </dl>
      <p className="report-help">Accepted risk and source-inferred resolution are independent dimensions, not additional verified closures.</p>
    </div>
    <div className="report-metric-group">
      <h3>All findings by severity</h3>
      <dl className="report-counts report-severities">
        {severities.map(([key, title, metric]) => <div key={key} className={`report-severity-${key}`}><dt>{title}</dt><dd>
          {onFindingMetricSelect ? <button type="button" className="report-metric-drilldown"
            aria-label={`View ${title}`} onClick={(event) => onFindingMetricSelect(metric, event.currentTarget)}>
            {report.bySeverity[key].toLocaleString("en-US")}</button> : report.bySeverity[key].toLocaleString("en-US")}
        </dd></div>)}
      </dl>
      <p className="report-help">Includes open and resolved canonical findings.</p>
    </div>
    <div className="report-metric-group">
      <h3>Scan coverage</h3>
      <dl className="report-counts report-coverage">
        {coverage.map(([key, title, state]) => <div key={key}><dt>{title}</dt><dd>
          {onCoverageSelect ? <button type="button" className="report-metric-drilldown"
            aria-label={`View ${title}`}
            onClick={(event) => onCoverageSelect(state, event.currentTarget)}>
            {report.coverage[key].toLocaleString("en-US")}</button> :
            report.coverage[key].toLocaleString("en-US")}
        </dd></div>)}
      </dl>
      <div className="report-window"><strong>Freshness window: {report.freshnessWindow.days} days.</strong>
        <p><time dateTime={report.freshnessWindow.from}>{timestampLabel(report.freshnessWindow.from)}</time>
          <span> to </span>{report.freshnessWindow.to === report.asOf ?
            <span>{timestampLabel(report.freshnessWindow.to)}</span> :
            <time dateTime={report.freshnessWindow.to}>{timestampLabel(report.freshnessWindow.to)}</time>}</p>
      </div>
      <p className="report-help">Scanned assets have a successful, complete full-scan import. Stale and unknown source freshness are distinct from unscanned assets; unknown does not mean current.</p>
    </div>
    <div className="report-verification"><Icon name="shield" size={17} /><div><strong>Independent verification not run</strong>
      <p>{report.verification.reason}</p></div></div>
  </div>;
}
