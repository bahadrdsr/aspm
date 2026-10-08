import { useEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import { api, APIError } from "@/api/client";
import type {
  RemediationSLAFindingPage, RemediationSLAResponse, RemediationSLAStatus,
  ReportSLAPolicy, ReportSLAPolicyInput,
} from "@/api/types";
import { label, timestampLabel } from "@/lib/format";
import { useScopedAction } from "@/lib/use-scoped-action";
import { useSession } from "@/lib/session";
import { ActionButton } from "./action-button";
import { FormError } from "./form-dialog";
import { Icon } from "./icon";
import { DataNotice, LoadingState } from "./states";
import { Button } from "./ui/button";

interface SummaryState {
  data: RemediationSLAResponse | null;
  pending: boolean;
  error: APIError | null;
}

interface FindingState {
  data: RemediationSLAFindingPage | null;
  pending: boolean;
  error: APIError | null;
}

function SLAPolicyEditor({ policy, onClose, onSaved }: {
  policy: ReportSLAPolicy;
  onClose: () => void;
  onSaved: (policy: ReportSLAPolicy) => void;
}) {
  const action = useScopedAction();
  const [critical, setCritical] = useState(String(policy.criticalDays));
  const [high, setHigh] = useState(String(policy.highDays));
  const [medium, setMedium] = useState(String(policy.mediumDays));
  const [low, setLow] = useState(String(policy.lowDays));
  const [info, setInfo] = useState(String(policy.infoDays));
  const [rationale, setRationale] = useState(policy.rationale);
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!event.currentTarget.reportValidity()) return;
    const input: ReportSLAPolicyInput = {
      revision: policy.revision,
      criticalDays: Number(critical), highDays: Number(high), mediumDays: Number(medium),
      lowDays: Number(low), infoDays: Number(info), rationale,
    };
    void action.run((signal) => api.updateReportSLAPolicy(policy, input, signal),
      (response) => onSaved(response.policy));
  }
  return <form className="report-sla-editor" aria-label="Edit SLA targets" onSubmit={submit}>
    <header><div><h3>Edit SLA targets</h3><p className="report-help">Current revision {policy.revision}.</p></div>
      <Button type="button" variant="ghost" onClick={onClose}>Close editor</Button></header>
    <p className="report-help">Targets are elapsed calendar days. Saving does not change finding workflow,
      disposition, source state, or verification.</p>
    <div className="report-sla-target-fields">
      {[
        ["Critical days", critical, setCritical],
        ["High days", high, setHigh],
        ["Medium days", medium, setMedium],
        ["Low days", low, setLow],
        ["Info days", info, setInfo],
      ].map(([title, value, setValue]) => <label key={title as string}>{title as string}
        <input type="number" min={1} max={3650} step={1} required value={value as string}
          onChange={(event) => (setValue as (value: string) => void)(event.target.value)} /></label>)}
    </div>
    <label>Rationale<textarea aria-label="Rationale" rows={3} maxLength={8192} required
      value={rationale} onChange={(event) => setRationale(event.target.value)} /></label>
    <FormError error={action.error} />
    <footer><Button type="button" variant="ghost" onClick={onClose}>Cancel</Button>
      <ActionButton type="submit" disabled={action.pending}>Save</ActionButton></footer>
  </form>;
}

function SLAFindingResults({ status, summary, onClose }: {
  status: RemediationSLAStatus;
  summary: RemediationSLAResponse["sla"];
  onClose: () => void;
}) {
  const [read, setRead] = useState({ cursor: null as string | null, revision: 0 });
  const [view, setView] = useState<FindingState>({ data: null, pending: true, error: null });
  useEffect(() => {
    const controller = new AbortController();
    let current = true;
    setView((previous) => ({ ...previous, pending: true, error: null }));
    void api.reportSLAFindings(status, 100, read.cursor, summary, controller.signal).then(
      (response) => {
        if (!current) return;
        setView((previous) => {
          const items = read.cursor === null ? response.items :
            [...new Map([...(previous.data?.items ?? []), ...response.items]
              .map((item) => [item.findingId, item])).values()];
          return { data: { ...response, items }, pending: false, error: null };
        });
      },
      (cause: unknown) => {
        if (!current || controller.signal.aborted) return;
        const error = cause instanceof APIError ? cause :
          new APIError("Unable to load remediation SLA findings. Please refresh.", "unavailable", true);
        setView((previous) => ({
          data: error.code === "forbidden" || error.code === "not-found" ? null : previous.data,
          pending: false, error,
        }));
      },
    );
    return () => { current = false; controller.abort(); };
  }, [read, status]);
  function readPage(cursor: string | null) {
    if (view.pending) return;
    setView((previous) => ({ ...previous, pending: true, error: null }));
    setRead((previous) => ({ cursor, revision: previous.revision + 1 }));
  }
  const title = status === "breached" ? "Breached" : "Within target";
  return <section className="report-sla-findings" aria-label="Remediation SLA findings">
    <header><div><h3>Remediation SLA findings</h3><p className="report-help">{title} current findings.</p></div>
      <div className="report-sla-actions">
        <ActionButton type="button" variant="outline" disabled={view.pending} onClick={() => readPage(null)}>
          <Icon name="refresh" />Refresh SLA findings</ActionButton>
        <Button type="button" variant="ghost" onClick={onClose}>Close SLA findings</Button>
      </div></header>
    <FormError error={view.error} />
    {view.pending && !view.data && <LoadingState label="Loading remediation SLA findings" />}
    {view.pending && view.data && <p className="report-help" role="status">
      Loading SLA findings. Previously received rows remain available.</p>}
    {view.error && view.data && <p className="report-help">
      SLA findings could not be refreshed. Previously received rows remain available.</p>}
    {view.data && <>
      <p className="report-help">SLA page contains {view.data.items.length.toLocaleString("en-US")} of{" "}
        {view.data.total.toLocaleString("en-US")} matching findings.</p>
      <div className="report-history-scroll" tabIndex={0} role="region" aria-label="SLA finding results">
        <table className="report-snapshot-table report-sla-table" aria-label="Remediation SLA findings">
          <thead><tr><th scope="col">Finding</th><th scope="col">Asset</th><th scope="col">Severity</th>
            <th scope="col">Owner</th><th scope="col">Workflow</th><th scope="col">Disposition</th>
            <th scope="col">Source state</th><th scope="col">First observed</th><th scope="col">Due</th>
            <th scope="col">Target days</th><th scope="col">Status</th><th scope="col">Overdue</th></tr></thead>
          <tbody>{view.data.items.map((item) => <tr key={item.findingId}>
            <td><strong>{item.title}</strong><code>{item.findingId}</code></td>
            <td>{item.assetName}<code>{item.assetId}</code></td>
            <td>{label(item.severity)}</td><td>{item.ownerName ?? "Unassigned"}</td>
            <td>{label(item.workflowState)}</td><td>{label(item.disposition)}</td>
            <td>{label(item.sourceState)}</td>
            <td><time dateTime={item.firstObservedAt}>{timestampLabel(item.firstObservedAt)}</time></td>
            <td><time dateTime={item.dueAt}>{timestampLabel(item.dueAt)}</time></td>
            <td>Target {item.targetDays} days.</td><td>{item.status === "breached" ? "Breached" : "Within target"}</td>
            <td>Overdue {item.overdueSeconds.toLocaleString("en-US")} seconds</td>
          </tr>)}</tbody>
        </table>
      </div>
      {view.data.nextCursor !== null && <footer className="report-history-footer">
        <ActionButton type="button" variant="outline" disabled={view.pending}
          onClick={() => readPage(view.data!.nextCursor)}>
          Load more SLA findings<Icon name="chevron" /></ActionButton></footer>}
    </>}
  </section>;
}

function SLAPanel({ onClose }: { onClose: () => void }) {
  const { workspace } = useSession();
  const [revision, setRevision] = useState(0);
  const [summary, setSummary] = useState<SummaryState>({ data: null, pending: true, error: null });
  const [status, setStatus] = useState<RemediationSLAStatus | null>(null);
  const [statusGeneration, setStatusGeneration] = useState(0);
  const [editing, setEditing] = useState(false);
  const refreshListAfterSummary = useRef(false);
  useEffect(() => {
    const controller = new AbortController();
    let current = true;
    setSummary((previous) => ({ ...previous, pending: true, error: null }));
    void api.reportSLA(controller.signal).then(
      (response) => {
        if (!current) return;
        setSummary({ data: response, pending: false, error: null });
        if (refreshListAfterSummary.current) {
          refreshListAfterSummary.current = false;
          setStatusGeneration((value) => value + 1);
        }
      },
      (cause: unknown) => {
        if (!current || controller.signal.aborted) return;
        const error = cause instanceof APIError ? cause :
          new APIError("Unable to load remediation SLA. Please refresh.", "unavailable", true);
        setSummary((previous) => ({
          data: error.code === "forbidden" || error.code === "not-found" ? null : previous.data,
          pending: false, error,
        }));
      },
    );
    return () => { current = false; controller.abort(); };
  }, [revision]);
  function refresh() {
    if (summary.pending) return;
    setSummary((previous) => ({ ...previous, pending: true, error: null }));
    setRevision((value) => value + 1);
  }
  function selectStatus(next: RemediationSLAStatus) {
    setStatus(next);
    setStatusGeneration((value) => value + 1);
  }
  function saved(policy: ReportSLAPolicy) {
    setSummary((previous) => previous.data ? {
      data: { ...previous.data, sla: { ...previous.data.sla, policy } },
      pending: false, error: null,
    } : previous);
    setEditing(false);
    refreshListAfterSummary.current = status !== null;
    setRevision((value) => value + 1);
  }
  const data = summary.data?.sla;
  return <section className="surface report-panel report-sla" aria-label="Remediation SLA">
    <header className="report-panel-heading"><div><h2>Remediation SLA</h2>
      <p className="report-help">Current elapsed-time targets for unresolved human workflow.</p></div>
      <div className="report-sla-actions">
        <ActionButton type="button" variant="outline" disabled={summary.pending} onClick={refresh}>
          <Icon name="refresh" />Refresh SLA</ActionButton>
        <Button type="button" variant="ghost" onClick={onClose}>Close remediation SLA</Button>
      </div></header>
    <div className="report-panel-content">
      <FormError error={summary.error} />
      {summary.pending && !data && <LoadingState label="Loading remediation SLA" />}
      {summary.pending && data && <p className="report-help" role="status">
        Refreshing remediation SLA. Previously received values remain available.</p>}
      {summary.error && data && <p className="report-help">
        SLA refresh failed. Previously received values remain available.</p>}
      {data && <>
        <div className="report-as-of"><p>As of <time dateTime={data.asOf}>{timestampLabel(data.asOf)}</time></p>
          <DataNotice origin={summary.data!.dataOrigin} /></div>
        <p className="report-help">Policy revision {data.policy.revision}. Accepted risk does not stop the clock.
          Suppression does not stop the clock. Source inference does not stop the clock.</p>
        <dl className="report-counts report-sla-targets">
          <div><dt>Critical target</dt><dd>{data.policy.criticalDays} days</dd></div>
          <div><dt>High target</dt><dd>{data.policy.highDays} days</dd></div>
          <div><dt>Medium target</dt><dd>{data.policy.mediumDays} days</dd></div>
          <div><dt>Low target</dt><dd>{data.policy.lowDays} days</dd></div>
          <div><dt>Info target</dt><dd>{data.policy.infoDays} days</dd></div>
          <div><dt>Tracked</dt><dd>{data.totals.tracked.toLocaleString("en-US")}</dd></div>
        </dl>
        <div className="report-sla-statuses">
          <button type="button" aria-label={`Breached findings ${data.totals.breached}`}
            onClick={() => selectStatus("breached")}>
            <span>Breached</span><strong>{data.totals.breached.toLocaleString("en-US")}</strong></button>
          <button type="button" aria-label={`Within target findings ${data.totals.withinTarget}`}
            onClick={() => selectStatus("within-target")}>
            <span>Within target</span><strong>{data.totals.withinTarget.toLocaleString("en-US")}</strong></button>
        </div>
        <div className="report-sla-severity">
          {Object.entries(data.bySeverity).map(([severity, counts]) => <p key={severity}>
            <strong>{label(severity)}</strong> {counts.tracked.toLocaleString("en-US")} tracked,{" "}
            {counts.breached.toLocaleString("en-US")} breached
          </p>)}
        </div>
        <p className="report-help">{data.verification.reason}</p>
        {workspace.role === "admin" &&
          <Button type="button" variant="outline" onClick={() => setEditing(true)}>Edit SLA targets</Button>}
        {status && <SLAFindingResults key={`${status}-${statusGeneration}`} status={status}
          summary={data} onClose={() => setStatus(null)} />}
        {editing && workspace.role === "admin" &&
          <SLAPolicyEditor policy={data.policy} onClose={() => setEditing(false)} onSaved={saved} />}
      </>}
    </div>
  </section>;
}

export function ReportSLA() {
  const [open, setOpen] = useState(false);
  return open ? <SLAPanel onClose={() => setOpen(false)} /> :
    <section className="surface report-panel report-sla-entry" aria-label="Remediation SLA entry">
      <header className="report-panel-heading"><div><h2>Remediation SLA</h2>
        <p className="report-help">Compare unresolved human workflow against explicit severity targets.</p></div>
        <Button type="button" variant="outline" onClick={() => setOpen(true)}>Remediation SLA</Button></header>
    </section>;
}
