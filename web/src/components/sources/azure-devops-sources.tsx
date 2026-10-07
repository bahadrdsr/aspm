import { useState } from "react";
import { sourcesApi } from "@/api/sources";
import type { AzureDevOpsSourceConnection, AzureDevOpsSourceResponse } from "@/api/source-types";
import { useSession } from "@/lib/session";
import { ActionButton } from "@/components/action-button";
import { DataNotice } from "@/components/states";
import { Button } from "@/components/ui/button";
import { AzureDevOpsSourceCollections } from "./azure-devops-source-collections";
import { AzureDevOpsSourceEditor } from "./azure-devops-source-editor";
import { SourceReadState, SourceTime } from "./source-ui";
import { useSourcePage } from "./use-source-page";

function newestSource(current: AzureDevOpsSourceConnection | null,
  incoming: AzureDevOpsSourceConnection): AzureDevOpsSourceConnection {
  return current?.id === incoming.id && current.revision > incoming.revision ? current : incoming;
}

export function AzureDevOpsSources() {
  const { workspace } = useSession();
  const page = useSourcePage(sourcesApi.azureDevOpsSources);
  const [editor, setEditor] = useState<{ id: string | null; trigger: HTMLElement } | null>(null);
  const [selected, setSelected] = useState<AzureDevOpsSourceConnection | null>(null);
  const received = selected && page.data?.items.find((source) => source.id === selected.id);
  const currentSource = received ? newestSource(selected, received) : selected;
  if (currentSource !== selected) setSelected(currentSource);
  function saved(response: AzureDevOpsSourceResponse) {
    page.accept(response.source);
    setSelected((previous) => previous?.id === response.source.id ? newestSource(previous, response.source) : previous);
    setEditor(null);
  }
  return <>
    <section className="source-panel surface" aria-label="Azure DevOps sources">
      <header className="source-panel-heading"><div><h2>Azure DevOps sources</h2>
        <p className="form-help">Selected Azure DevOps Services repositories. Collection requires an explicit build, artifact and report path.</p></div>
        <div className="source-actions"><ActionButton variant="outline" aria-disabled={page.pending}
          onClick={page.refresh}>Refresh Azure DevOps sources</ActionButton>
          {workspace.role === "admin" && <ActionButton
            onClick={(event) => setEditor({ id: null, trigger: event.currentTarget })}>Add Azure DevOps source</ActionButton>}
        </div>
      </header>
      <div className="source-panel-content">
        <SourceReadState error={page.error} pending={page.pending} loaded={page.data !== null}
          retry={page.retry} subject="Azure DevOps sources" />
        {page.data && <>
          <div className="source-summary"><span>Total at last read: {page.data.total} sources; {page.data.items.length} shown.</span>
            {page.data.dataOrigin && <DataNotice origin={page.data.dataOrigin} />}</div>
          {page.data.items.length === 0 ? <div><h3>No Azure DevOps sources</h3>
            <p className="form-help">An administrator can configure one selected organization/project/repository without starting collection.</p></div> :
            <div className="source-table-scroll" tabIndex={0} role="region" aria-label="Azure DevOps source results">
              <table className="source-table" aria-label="Azure DevOps sources"><thead><tr><th scope="col">Source</th><th scope="col">Actions</th></tr></thead>
                <tbody>{page.data.items.map((source) => <tr key={source.id}>
                  <td><strong>{source.name}</strong><p>{source.azureDevOps.organization}</p>
                    <p><code>{source.azureDevOps.projectId}</code> / <code>{source.azureDevOps.repositoryId}</code></p>
                    <p>{source.enabled ? "Enabled" : "Disabled"} - Revision: {source.revision}.</p>
                    <p>Stored PAT: {source.credentialConfigured ? "configured" : "not configured"}. Not live verified.</p>
                    <p>Updated <SourceTime value={source.updatedAt} /></p></td>
                  <td><div className="source-actions">
                    <Button type="button" variant="outline" size="sm"
                      onClick={() => setSelected((previous) => newestSource(previous, source))}>Collections</Button>
                    {workspace.role === "admin" && <Button type="button" variant="outline" size="sm"
                      onClick={(event) => setEditor({ id: source.id, trigger: event.currentTarget })}>Edit source</Button>}
                  </div></td>
                </tr>)}</tbody>
              </table>
            </div>}
          {page.data.nextCursor !== null && <ActionButton variant="outline" aria-disabled={page.pending}
            onClick={page.more}>Load more Azure DevOps sources</ActionButton>}
        </>}
      </div>
    </section>
    {currentSource && <AzureDevOpsSourceCollections key={currentSource.id} source={currentSource} />}
    {workspace.role === "admin" && editor && <AzureDevOpsSourceEditor id={editor.id} returnFocus={editor.trigger}
      onClose={() => setEditor(null)} onSaved={saved} />}
  </>;
}
