import type { DataOrigin, PostureReport } from "@/api/types";
import { timestampLabel } from "@/lib/format";
import { DataNotice } from "./states";
import { Icon } from "./icon";

const mainTotals = [["assets", "Assets"], ["findings", "Findings"], ["openFindings", "Open findings"]] as const;
const resolutionTotals = [
  ["acceptedRisk", "Accepted risk"], ["expiredAcceptedRisk", "Expired accepted risk"],
  ["inferredResolved", "Inferred resolved"], ["verifiedResolved", "Verified resolved"],
] as const;
const severities = [["critical", "Critical"], ["high", "High"], ["medium", "Medium"], ["low", "Low"], ["info", "Info"]] as const;
const coverage = [
  ["scannedAssets", "Scanned assets"], ["unscannedAssets", "Unscanned assets"],
  ["staleAssets", "Stale assets"], ["unknownFreshnessAssets", "Unknown freshness"],
] as const;

export function ReportMetrics({ report, origin }: { report: PostureReport; origin: DataOrigin }) {
  return <div className="report-metrics">
    <div className="report-as-of"><p>As of <time dateTime={report.asOf}>{timestampLabel(report.asOf)}</time></p><DataNotice origin={origin} /></div>
    {report.totals.assets === 0 && report.totals.findings === 0 &&
      <p className="report-empty">No assets or findings were returned for this workspace. These are the service's empty totals, not a completed scan.</p>}
    <dl className="report-counts report-main-totals">
      {mainTotals.map(([key, title]) => <div key={key}><dt>{title}</dt><dd>{report.totals[key].toLocaleString("en-US")}</dd></div>)}
    </dl>
    <div className="report-metric-group">
      <h3>Risk &amp; resolution</h3>
      <dl className="report-counts report-resolution-totals">
        {resolutionTotals.map(([key, title]) => <div key={key}><dt>{title}</dt><dd>{report.totals[key].toLocaleString("en-US")}</dd></div>)}
      </dl>
      <p className="report-help">Accepted risk and source-inferred resolution are independent dimensions, not additional verified closures.</p>
    </div>
    <div className="report-metric-group">
      <h3>All findings by severity</h3>
      <dl className="report-counts report-severities">
        {severities.map(([key, title]) => <div key={key} className={`report-severity-${key}`}><dt>{title}</dt><dd>{report.bySeverity[key].toLocaleString("en-US")}</dd></div>)}
      </dl>
      <p className="report-help">Includes open and resolved canonical findings.</p>
    </div>
    <div className="report-metric-group">
      <h3>Scan coverage</h3>
      <dl className="report-counts report-coverage">
        {coverage.map(([key, title]) => <div key={key}><dt>{title}</dt><dd>{report.coverage[key].toLocaleString("en-US")}</dd></div>)}
      </dl>
      <div className="report-window"><strong>Freshness window: {report.freshnessWindow.days} days.</strong>
        <p><time dateTime={report.freshnessWindow.from}>{timestampLabel(report.freshnessWindow.from)}</time>
          <span> to </span><time dateTime={report.freshnessWindow.to}>{timestampLabel(report.freshnessWindow.to)}</time></p>
      </div>
      <p className="report-help">Scanned assets have a successful, complete full-scan import. Stale and unknown source freshness are distinct from unscanned assets; unknown does not mean current.</p>
    </div>
    <div className="report-verification"><Icon name="shield" size={17} /><div><strong>Independent verification not run</strong>
      <p>{report.verification.reason}</p></div></div>
  </div>;
}
