import { useCallback, useEffect, useState } from "react";
import { sourcesApi } from "@/api/sources";
import type { SourceEvidence, SourceRecord } from "@/api/source-types";
import { useResource } from "@/lib/use-resource";
import { ActionButton } from "@/components/action-button";
import { FormDialog } from "@/components/form-dialog";
import { SourceReadState, SourceTime } from "./source-ui";

function EvidenceBody({ evidence, id }: { evidence: SourceEvidence; id: string }) {
  const [download, setDownload] = useState<string | null>(null);
  useEffect(() => {
    const url = URL.createObjectURL(new Blob([evidence.bytes], { type: "application/octet-stream" }));
    setDownload(url);
    return () => URL.revokeObjectURL(url);
  }, [evidence.bytes]);
  return <>
    {evidence.text === null ? <p role="alert" className="form-help">These verified stored bytes are not valid UTF-8 text. Download the exact evidence instead; no replacement characters or repaired JSON are displayed.</p> :
      <pre className="evidence-text source-raw-evidence">{evidence.text}</pre>}
    {download && <a className="source-link" href={download} download={`source-record-${id}.json`}>Download raw evidence</a>}
  </>;
}
export function SourceEvidenceDialog({ record, returnFocus, onClose }: {
  record: SourceRecord; returnFocus: HTMLElement; onClose: () => void;
}) {
  const load = useCallback((signal: AbortSignal) => sourcesApi.evidence(record, signal), [record]);
  const resource = useResource(load);
  return <FormDialog title="Raw source evidence" containFocus returnFocus={returnFocus} onClose={onClose}
    description="Stored raw source bytes and independently returned record metadata. A raw alert is not a normalized finding, source scan or verified resolution.">
    <div className="source-evidence-content">
      <dl className="source-facts">
        <div><dt>Record ID</dt><dd><code>{record.id}</code></dd></div>
        <div><dt>Kind</dt><dd>{record.kind}</dd></div>
        <div><dt>External ID</dt><dd>{record.externalId}</dd></div>
        <div><dt>Parent ID</dt><dd>{record.parentId || "None supplied"}</dd></div>
        <div><dt>Ordinal</dt><dd>{record.ordinal}</dd></div>
        <div><dt>Native run ID</dt><dd>{record.nativeRunId || "Unknown"}</dd></div>
        <div><dt>Native state</dt><dd>{record.state || "Not supplied"}</dd></div>
        <div><dt>Native severity</dt><dd>{record.severity || "Not supplied"}</dd></div>
        <div className="full-width"><dt>Location</dt><dd>{record.location || "Not supplied"}</dd></div>
        <div className="full-width"><dt>Raw URL (provenance only)</dt><dd>{record.rawURL || "Not supplied"}</dd></div>
        <div><dt>Source scan time</dt><dd>{record.sourceScanAt === null ? "Unknown source scan time" : <SourceTime value={record.sourceScanAt} />}</dd></div>
        <div><dt>Source updated</dt><dd>{record.sourceUpdatedAt === null ? "Not supplied" : <SourceTime value={record.sourceUpdatedAt} />}</dd></div>
        <div className="full-width"><dt>SHA-256</dt><dd><code>{record.evidence.sha256}</code></dd></div>
        <div><dt>Stored bytes</dt><dd>{record.evidence.sizeBytes}</dd></div>
      </dl>
      <p className="form-help">Native update time is not source scan time. The provenance URL is not fetched. Ordinal is not the pagination cursor.</p>
      <ActionButton variant="outline" disabled={resource.status === "loading"} onClick={resource.reload}>Refresh evidence</ActionButton>
      <SourceReadState error={resource.error} pending={resource.status === "loading"} loaded={resource.data !== null} retry={resource.reload} subject="evidence" />
      {resource.data && <EvidenceBody evidence={resource.data} id={record.id} />}
    </div>
  </FormDialog>;
}
