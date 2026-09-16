import type { ReactNode } from "react";
import type { APIError } from "@/api/client";
import type { DataOrigin, Severity, WorkflowState } from "@/api/types";
import { label } from "@/lib/format";
import { Icon } from "./icon";
import type { IconName } from "./icon";
import { ActionButton } from "./action-button";

export function DataNotice({ origin }: { origin: DataOrigin }) {
  return <span className={`data-notice ${origin}`}><span className="status-dot" />{origin === "synthetic" ? "Synthetic API data" : "API-supplied data"}</span>;
}

export function SeverityBadge({ severity }: { severity: Severity }) {
  return <span className={`badge severity-${severity}`}><span className="status-dot" />{label(severity)}</span>;
}

export function WorkflowBadge({ value }: { value: WorkflowState }) {
  return <span className={`workflow workflow-${value}`}><Icon name={value === "resolved" ? "check" : value === "in-progress" ? "clock" : "minus"} size={14} />{label(value)}</span>;
}

export function EmptyState({ icon = "work", title, description, children }: { icon?: IconName; title: string; description: string; children?: ReactNode }) {
  return <div className="empty-state"><div className="empty-icon"><Icon name={icon} size={27} /></div><h2>{title}</h2><p>{description}</p>{children && <div className="empty-actions">{children}</div>}</div>;
}

export function LoadingState({ label: text }: { label: string }) {
  return <div className="loading-state" role="status"><div className="loading-caption"><Icon name="clock" /><span>{text}</span></div><div className="skeleton-lines" aria-hidden="true"><i /><i /><i /><i /></div><p>Waiting for the data service. Other views remain available.</p></div>;
}

export function ErrorState({ error, retry, stale = false }: { error: APIError; retry: () => void; stale?: boolean }) {
  const forbidden = error.code === "forbidden";
  return <div className={`error-state ${forbidden ? "permission" : ""}`} role="alert">
    <span className="error-icon"><Icon name={forbidden ? "lock" : "warning"} size={22} /></span>
    <div className="error-copy"><h2>{forbidden ? "Access restricted" : error.code === "not-found" ? "Finding not found" : "Data service unavailable"}</h2><p>{error.message}</p>
      {stale && <p className="muted">Showing the last received data. Its source timestamps have not been refreshed.</p>}
      {error.requestId && <p className="request-id">Request <code>{error.requestId}</code></p>}
      {forbidden && <p className="muted">Ask your administrator to review your access. Restricted records are not displayed.</p>}
      <ActionButton variant="outline" onClick={retry}><Icon name="refresh" />{error.retryable ? "Retry" : "Check again"}</ActionButton>
    </div>
  </div>;
}
