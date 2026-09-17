import { useState } from "react";
import { sourcesApi } from "@/api/sources";
import type { SourceConnection, SourceResponse } from "@/api/source-types";
import { useSession } from "@/lib/session";
import { ActionButton } from "@/components/action-button";
import { DataNotice } from "@/components/states";
import { Button } from "@/components/ui/button";
import { SourceEditor } from "./source-editor";
import { SourceCollections } from "./source-collections";
import { SourceReadState, SourceTime } from "./source-ui";
import { useSourcePage } from "./use-source-page";
import "./sources.css";

function newestSource(current: SourceConnection | null, incoming: SourceConnection): SourceConnection {
  return current?.id === incoming.id && current.revision > incoming.revision ? current : incoming;
}

export function Sources() {
  const { workspace } = useSession();
  const page = useSourcePage(sourcesApi.sources);
  const [editor, setEditor] = useState<{ id: string | null; trigger: HTMLElement } | null>(null);
  const [selected, setSelected] = useState<SourceConnection | null>(null);
  const received = selected && page.data?.items.find((source) => source.id === selected.id);
  const currentSource = received ? newestSource(selected, received) : selected;
  // Reconcile before children commit; an absent page entry is not deletion.
  if (currentSource !== selected) setSelected(currentSource);
  function saved(response: SourceResponse) {
    page.accept(response.source);
    setSelected((previous) => previous?.id === response.source.id ? newestSource(previous, response.source) : previous);
    setEditor(null);
  }
  return <>
    <section className="source-panel surface" aria-label="Sources">
      <header className="source-panel-heading"><div><h2>Sources</h2>
        <p className="form-help">Selected GitHub repositories. Configure metadata first; collect only after explicit confirmation.</p></div>
        <div className="source-actions"><ActionButton variant="outline" aria-disabled={page.pending} onClick={page.refresh}>Refresh sources</ActionButton>
          {workspace.role === "admin" && <ActionButton onClick={(event) => setEditor({ id: null, trigger: event.currentTarget })}>Add source</ActionButton>}
        </div>
      </header>
      <div className="source-panel-content">
        <SourceReadState error={page.error} pending={page.pending} loaded={page.data !== null} retry={page.retry} subject="sources" />
        {page.data && <>
          <div className="source-summary"><span>Total at last read: {page.data.total} sources; {page.data.items.length} shown.</span>
            {page.data.dataOrigin && <DataNotice origin={page.data.dataOrigin} />}</div>
          {page.data.items.length === 0 ? <div><h3>No sources</h3><p className="form-help">No configured sources were returned for this workspace.
            {" "}An administrator can configure one selected repository without starting collection.</p></div> :
            <div className="source-table-scroll" tabIndex={0} role="region" aria-label="Source results">
              <table className="source-table" aria-label="Sources"><thead><tr><th scope="col">Source</th><th scope="col">Actions</th></tr></thead>
                <tbody>{page.data.items.map((source) => <tr key={source.id}>
                  <td><strong>{source.name}</strong><p>{source.repository}</p>
                    <p>{source.enabled ? "Enabled" : "Disabled"} - Revision: {source.revision}.</p>
                    <p>Stored credential: {source.credentialConfigured ? "configured" : "not configured"}. Not live verified.</p>
                    <p>Updated <SourceTime value={source.updatedAt} /></p></td>
                  <td><div className="source-actions">
                    <Button type="button" variant="outline" size="sm" onClick={() => setSelected((previous) => newestSource(previous, source))}>Collections</Button>
                    {workspace.role === "admin" && <Button type="button" variant="outline" size="sm"
                      onClick={(event) => setEditor({ id: source.id, trigger: event.currentTarget })}>Edit source</Button>}
                  </div></td>
                </tr>)}</tbody>
              </table>
            </div>}
          {page.data.nextCursor !== null && <ActionButton variant="outline" aria-disabled={page.pending} onClick={page.more}>Load more sources</ActionButton>}
        </>}
      </div>
    </section>
    {currentSource && <SourceCollections key={currentSource.id} source={currentSource} />}
    {workspace.role === "admin" && editor && <SourceEditor id={editor.id} returnFocus={editor.trigger} onClose={() => setEditor(null)} onSaved={saved} />}
  </>;
}
