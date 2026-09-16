import { useState } from "react";
import { api } from "@/api/client";
import type { ImportReceipt } from "@/api/types";
import { label, timestampLabel } from "@/lib/format";
import { useScopedAction } from "@/lib/use-scoped-action";
import { ActionButton } from "./action-button";
import { FormError } from "./form-dialog";
import { Icon } from "./icon";

const explanations = {
  queued: "Accepted and queued. Processing has not completed.",
  processing: "The service is processing this report.",
  succeeded: "The service finished processing this report. Review its observations in Work.",
  failed: "The service could not process this report. The failure remains recorded.",
};

export function ImportStatus({ initial }: { initial: ImportReceipt }) {
  const [receipt, setReceipt] = useState<ImportReceipt | null>(initial);
  const action = useScopedAction();
  return <section className="surface import-status-card" aria-label="Latest report import">
    <header className="section-heading"><h2>Latest report import</h2>
      <ActionButton variant="outline" disabled={action.pending} onClick={() => {
        void action.run((signal) => api.importStatus(initial.id, signal), setReceipt, (error) => {
          if (error.code === "forbidden" || error.code === "not-found") setReceipt(null);
        });
      }}><Icon name="refresh" />Refresh import status</ActionButton>
    </header>
    <FormError error={action.error} />
    {receipt === null && <p className="form-help">{action.pending
      ? "Checking access to this import. Previous receipt details remain unavailable."
      : "Receipt details are unavailable. Refresh to request an authorized response from the service."}</p>}
    {receipt && <>
      <div role={receipt.state === "failed" ? "alert" : "status"} aria-label="Import status" className={`import-state import-${receipt.state}`}>
        <strong>{label(receipt.state)}</strong><p>{explanations[receipt.state]}</p>
        {receipt.failure && <p className="import-failure">{receipt.failure.message} <code>({receipt.failure.code})</code></p>}
        {receipt.state === "succeeded" && <p>{receipt.observationCount.toLocaleString()} observations in this run. Processing success is not resolution verification.</p>}
      </div>
      <dl className="import-facts">
        <div><dt>Import ID</dt><dd><code>{receipt.id}</code></dd></div>
        <div><dt>Scan ID</dt><dd><code>{receipt.scanId}</code></dd></div>
        <div><dt>Source ID</dt><dd><code>{receipt.sourceId}</code></dd></div>
        <div><dt>Accepted by service</dt><dd><time dateTime={receipt.importedAt}>{timestampLabel(receipt.importedAt)}</time></dd></div>
      </dl>
      <details className="import-metadata"><summary>Report identity and provenance</summary>
        <dl className="import-facts">
          <div><dt>Asset ID</dt><dd><code>{receipt.assetId}</code></dd></div><div><dt>Run ID</dt><dd><code>{receipt.runId}</code></dd></div>
          <div><dt>Format</dt><dd>{receipt.format}</dd></div><div><dt>Scope ID</dt><dd>{receipt.scope.id}</dd></div>
          <div><dt>Revision</dt><dd>{receipt.scope.revision}</dd></div><div><dt>Branch</dt><dd>{receipt.scope.branch}</dd></div>
          <div><dt>Source scan</dt><dd>{receipt.sourceScanAt === null ? "Unknown source time" : <time dateTime={receipt.sourceScanAt}>{timestampLabel(receipt.sourceScanAt)}</time>}</dd></div>
          <div><dt>Collected</dt><dd><time dateTime={receipt.collectedAt}>{timestampLabel(receipt.collectedAt)}</time></dd></div>
          <div className="full-width"><dt>Original report digest</dt><dd><code>{receipt.reportDigest}</code></dd></div>
        </dl>
      </details>
      <p className="form-help">{action.pending ? "Refreshing the last received status..." : "Last received server state. Refresh to check progress; this view does not poll automatically."}</p>
    </>}
  </section>;
}
