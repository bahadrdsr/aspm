import { useCallback, useRef, useState } from "react";
import { APIError } from "@/api/client";
import { sourcesApi } from "@/api/sources";
import type { CollectionResponse, SourceCollection } from "@/api/source-types";
import { useResource } from "@/lib/use-resource";
import { label } from "@/lib/format";
import { ActionButton } from "@/components/action-button";
import { DataNotice } from "@/components/states";
import { CollectionRecords } from "./collection-records";
import { SourceReadState, SourceTime } from "./source-ui";

const explanations = {
  queued: "Queued, not yet collected. Waiting for the independent collection worker.",
  collecting: "Collecting the selected repository. It is not complete.",
  succeeded: "The service finished this collection. Completeness below refers only to selected feeds, not scanner success, finding normalization or independent verification.",
  partial: "Selected collection is incomplete. Valid raw records remain available alongside its gaps.",
  failed: "Collection failed. This is not an empty successful scan.",
  blocked: "Collection was blocked by the service. No source scan or verification is claimed.",
};
function binding(value: SourceCollection) {
  const { id, workspaceId, sourceId, profile, connectionRevision, repository, requestedBy, createdAt } = value;
  return JSON.stringify({ id, workspaceId, sourceId, profile, connectionRevision, repository, requestedBy, createdAt });
}
export function CollectionDetail({ id, sourceId, initial }: { id: string; sourceId: string; initial: CollectionResponse | null }) {
  const [checks, setChecks] = useState(0);
  const original = useRef<SourceCollection | null>(initial?.collection ?? null);
  const load = useCallback(async (signal: AbortSignal) => {
    const response = initial !== null && checks === 0 ? initial : await sourcesApi.collection(id, sourceId, signal);
    signal.throwIfAborted();
    if (original.current && binding(original.current) !== binding(response.collection)) throw new APIError("The service returned a changed immutable collection binding.", "invalid-response", false);
    original.current = response.collection;
    return response;
  }, [id, sourceId, initial, checks]);
  const resource = useResource(load);
  const collection = resource.data?.collection;
  return <>
    <section className="source-panel surface" aria-label="Selected collection">
      <header className="source-panel-heading"><h3>Selected collection</h3>
        <ActionButton variant="outline" disabled={resource.status === "loading"} onClick={() => setChecks((value) => value + 1)}>Refresh collection</ActionButton></header>
      <div className="source-panel-content">
        <SourceReadState error={resource.error} pending={resource.status === "loading"} loaded={collection !== undefined}
          retry={() => setChecks((value) => value + 1)} subject="collection" />
        {collection && <>
          {resource.data?.dataOrigin && <DataNotice origin={resource.data.dataOrigin} />}
          <p><code>{collection.id}</code></p>
          <div role="status" aria-label="Collection status" className={`source-collection-status source-state-${collection.state}`}>
            <strong>{label(collection.state)}</strong>{" "}<p>{explanations[collection.state]}</p>
            <p>Complete: {String(collection.complete)}. {collection.complete ? "Selected feeds exhausted only." : "Selected feeds are not complete."}</p>
            <p>Record count: {collection.recordCount}.</p>
            {collection.failure && <>
              <p>Failure code: <code>{collection.failure.code}</code></p>
              <p>Native code: {collection.failure.nativeCode || "Not supplied"}</p>
              <p>HTTP status: {collection.failure.httpStatus}.</p>
              <p>Retry-After: {collection.failure.retryAfterSeconds} seconds. Retryable: false. No automatic retries.</p>
            </>}
          </div>
          <dl className="source-facts">
            <div className="full-width"><dt>Selected repository</dt><dd>{collection.repository}</dd></div>
            <div><dt>Connection revision</dt><dd>{collection.connectionRevision}</dd></div>
            <div><dt>Requested by</dt><dd><code>{collection.requestedBy}</code></dd></div>
            <div><dt>Created</dt><dd><SourceTime value={collection.createdAt} /></dd></div>
            {collection.collectedAt && <div><dt>Collected</dt><dd><SourceTime value={collection.collectedAt} /></dd></div>}
            {collection.completedAt && <div><dt>Completed</dt><dd><SourceTime value={collection.completedAt} /></dd></div>}
            {collection.assetId && <div className="full-width"><dt>Asset reference</dt><dd><code>{collection.assetId}</code> <a className="source-link" href="#/assets">View assets</a></dd></div>}
            {collection.repositoryId && <div><dt>Repository ID</dt><dd>{collection.repositoryId}</dd></div>}
          </dl>
          {collection.gaps.length > 0 ? <div><h4>Collection gaps</h4><ul>{collection.gaps.map((gap, index) => <li key={`${index}:${gap}`}><code>{gap}</code></li>)}</ul></div> :
            <p className="form-help">No gaps supplied. That does not certify a scan or repository safety.</p>}
          <p className="form-help">Last received server state. Refresh manually to read progress. No polling, automatic collection or retry timer runs here.</p>
        </>}
      </div>
    </section>
    {collection && collection.recordCount > 0 && <CollectionRecords key={collection.id} id={collection.id} />}
  </>;
}
