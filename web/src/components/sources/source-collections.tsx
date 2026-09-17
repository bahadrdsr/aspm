import { useCallback, useLayoutEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import { APIError } from "@/api/client";
import { sourcesApi } from "@/api/sources";
import { acknowledgeCollectionIntent, collectionIntent, pendingCollectionIntent } from "@/api/source-intents";
import type { CollectionResponse, SourceConnection } from "@/api/source-types";
import { useScopedAction } from "@/lib/use-scoped-action";
import { useSession } from "@/lib/session";
import { label } from "@/lib/format";
import { ActionButton } from "@/components/action-button";
import { FormDialog, FormError } from "@/components/form-dialog";
import { Button } from "@/components/ui/button";
import { CollectionDetail } from "./collection-detail";
import { SourceReadState, SourceTime } from "./source-ui";
import { useSourcePage } from "./use-source-page";

function CollectSource({ source, returnFocus, onClose, onAccepted }: {
  source: SourceConnection; returnFocus: HTMLElement; onClose: () => void; onAccepted: (response: CollectionResponse) => void;
}) {
  const { workspace, session } = useSession();
  const action = useScopedAction();
  const [intent, setIntent] = useState(() => pendingCollectionIntent(source.id));
  const pending = useRef<HTMLParagraphElement>(null);
  useLayoutEffect(() => { if (action.pending) pending.current?.focus({ preventScroll: true }); }, [action.pending]);
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    void action.run(async (signal) => {
      if (workspace.role === "viewer") throw new APIError("Your workspace role is read only.", "forbidden", false);
      if (!source.enabled) throw new APIError("This source is disabled for collection.", "conflict", false);
      const key = collectionIntent(source.id);
      setIntent(key);
      return { key, response: await sourcesApi.enqueue(source.id, key, session.user.id, signal) };
    }, ({ key, response }) => {
      acknowledgeCollectionIntent(source.id, key);
      onAccepted(response);
    });
  }
  return <FormDialog title="Collect source" containFocus returnFocus={returnFocus} onClose={onClose}
    description="Confirm collection of this one selected repository. Opening or cancelling this dialog performs no collection.">
    <form aria-label="Confirm source collection" className="application-form source-form" onSubmit={submit} aria-busy={action.pending}>
      <div className="source-selection"><h3>{source.name}</h3><p>{source.repository}</p>
        <p className="form-help">Source revision: {source.revision}. Selected GitHub repository only.</p></div>
      <p className="form-help">Confirmation creates a durable queued intent. An independent worker may collect selected raw feeds;
        this is not a scanner run, normalized finding, closure or live-verification claim.</p>
      {intent && <p className="form-help">An unresolved intent is retained in this session. It may already be queued.
        {" "}Another explicit confirmation uses the same idempotency key. Closing does not roll back a request already sent.</p>}
      <FormError error={action.error} />
      {action.pending && <p ref={pending} tabIndex={-1} role="status" className="form-help">Queuing collection. Awaiting the service acknowledgement, not a completed collection.</p>}
      <footer className="form-actions"><Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
        <ActionButton type="submit" disabled={action.pending || !source.enabled || workspace.role === "viewer"}>Queue collection</ActionButton></footer>
    </form>
  </FormDialog>;
}

interface Selection { id: string; generation: number; initial: CollectionResponse | null }
export function SourceCollections({ source }: { source: SourceConnection }) {
  const { workspace } = useSession();
  const load = useCallback((cursor: string | null, signal: AbortSignal) => sourcesApi.collections(source.id, cursor, signal), [source.id]);
  const page = useSourcePage(load);
  const [confirmation, setConfirmation] = useState<HTMLElement | null>(null);
  const [selected, setSelected] = useState<Selection | null>(null);
  function select(id: string, initial: CollectionResponse | null = null) {
    setSelected((previous) => ({ id, initial, generation: (previous?.generation ?? 0) + 1 }));
  }
  function accepted(response: CollectionResponse) {
    page.accept(response.collection);
    select(response.collection.id, response);
    setConfirmation(null);
  }
  return <div className="source-selected">
    <section className="source-panel surface" aria-label="Source collections">
      <header className="source-panel-heading"><div><h2>Source collections</h2><p className="source-name">{source.name}</p>
        <p className="form-help">{source.repository}</p></div>
        <div className="source-actions">
          <ActionButton variant="outline" aria-disabled={page.pending} onClick={page.refresh}>Refresh collections</ActionButton>
          {workspace.role !== "viewer" && <ActionButton disabled={!source.enabled} onClick={(event) => setConfirmation(event.currentTarget)}>Collect source</ActionButton>}
        </div>
      </header>
      <div className="source-panel-content">
        <p className="form-help">{source.enabled ? "Enabled" : "Disabled"} for collection. Stored credential presence is not connected or live-verified status.</p>
        <SourceReadState error={page.error} pending={page.pending} loaded={page.data !== null} retry={page.retry} subject="collections" />
        {page.data && <>
          <p className="form-help">Loaded: {page.data.items.length} collections in this page; {page.data.total} total at the last read.</p>
          {page.data.items.length === 0 ? <p>No collections yet</p> : <div className="source-table-scroll" tabIndex={0} role="region" aria-label="Collection history results">
            <table className="source-table" aria-label="Collections"><thead><tr><th scope="col">Collection</th><th scope="col">Action</th></tr></thead>
              <tbody>{page.data.items.map((item) => <tr key={item.id}>
                <td><code>{item.id}</code><p>{label(item.state)} - {item.recordCount} raw records</p><p><SourceTime value={item.createdAt} /></p></td>
                <td><Button type="button" variant="outline" size="sm" onClick={() => select(item.id)}>Open collection</Button></td>
              </tr>)}</tbody>
            </table>
          </div>}
          {page.data.nextCursor !== null && <ActionButton variant="outline" aria-disabled={page.pending} onClick={page.more}>Load more collections</ActionButton>}
        </>}
      </div>
    </section>
    {selected && <CollectionDetail key={selected.generation} id={selected.id} sourceId={source.id} initial={selected.initial} />}
    {confirmation && <CollectSource source={source} returnFocus={confirmation} onClose={() => setConfirmation(null)} onAccepted={accepted} />}
  </div>;
}
