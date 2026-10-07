import { useCallback, useLayoutEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import { APIError } from "@/api/client";
import {
  acknowledgeAzureDevOpsCollectionIntent, azureDevOpsCollectionIntent, pendingAzureDevOpsCollectionIntent,
} from "@/api/source-intents";
import { sourcesApi } from "@/api/sources";
import type {
  AzureDevOpsCollectionResponse, AzureDevOpsSelection, AzureDevOpsSourceConnection,
} from "@/api/source-types";
import { formText } from "@/lib/application-input";
import { label } from "@/lib/format";
import { useScopedAction } from "@/lib/use-scoped-action";
import { useSession } from "@/lib/session";
import { ActionButton } from "@/components/action-button";
import { FormDialog, FormError } from "@/components/form-dialog";
import { Button } from "@/components/ui/button";
import { CollectionDetail } from "./collection-detail";
import { SourceReadState, SourceTime } from "./source-ui";
import { useSourcePage } from "./use-source-page";

function CollectAzureDevOpsSource({ source, returnFocus, onClose, onAccepted }: {
  source: AzureDevOpsSourceConnection;
  returnFocus: HTMLElement;
  onClose: () => void;
  onAccepted: (response: AzureDevOpsCollectionResponse) => void;
}) {
  const { workspace, session } = useSession();
  const action = useScopedAction();
  const [intent, setIntent] = useState(() => pendingAzureDevOpsCollectionIntent(source.id));
  const pending = useRef<HTMLParagraphElement>(null);
  useLayoutEffect(() => { if (action.pending) pending.current?.focus({ preventScroll: true }); }, [action.pending]);
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    void action.run(async (signal) => {
      if (workspace.role === "viewer") throw new APIError("Your workspace role is read only.", "forbidden", false);
      if (!source.enabled) throw new APIError("This source is disabled for collection.", "conflict", false);
      const selection: AzureDevOpsSelection = {
        buildId: formText(data, "buildId"),
        artifactName: formText(data, "artifactName"),
        artifactPath: formText(data, "artifactPath"),
      };
      const current = azureDevOpsCollectionIntent(source.id, selection);
      setIntent(current);
      return {
        current,
        response: await sourcesApi.enqueueAzureDevOps(source.id, current.selection, current.key, session.user.id, signal),
      };
    }, ({ current, response }) => {
      acknowledgeAzureDevOpsCollectionIntent(source.id, current.key);
      onAccepted(response);
    });
  }
  return <FormDialog title="Collect Azure DevOps report" containFocus returnFocus={returnFocus} onClose={onClose}
    description="Select one existing build, named artifact and report path. The service reads only that immutable scope and never triggers a pipeline.">
    <form aria-label="Confirm Azure DevOps collection" className="application-form source-form" onSubmit={submit} aria-busy={action.pending}>
      <div className="source-selection"><h3>{source.name}</h3><p>{source.azureDevOps.organization}</p>
        <p><code>{source.azureDevOps.projectId}</code> / <code>{source.azureDevOps.repositoryId}</code></p>
        <p className="form-help">Source revision: {source.revision}. Azure DevOps Services PAT profile only.</p></div>
      <fieldset className="form-grid" disabled={action.pending}>
        <legend className="sr-only">Selected build artifact report</legend>
        <label>Build ID<input name="buildId" required maxLength={20} autoComplete="off" spellCheck={false}
          defaultValue={intent?.selection.buildId ?? ""} /></label>
        <label>Artifact name<input name="artifactName" required maxLength={255} autoComplete="off" spellCheck={false}
          defaultValue={intent?.selection.artifactName ?? ""} /></label>
        <label className="full-width">Report path inside artifact ZIP<input name="artifactPath" required maxLength={2048}
          autoComplete="off" spellCheck={false} defaultValue={intent?.selection.artifactPath ?? ""} /></label>
        <p className="form-help full-width">Use the canonical positive build ID, exact artifact name and relative report path. No latest-build selection, wildcard or disk extraction is performed.</p>
      </fieldset>
      {intent && <p className="form-help">An earlier acknowledgement is unresolved. The exact retained build/artifact/path and idempotency key will be reused; a different selection is rejected.</p>}
      <FormError error={action.error} />
      {action.pending && <p ref={pending} tabIndex={-1} role="status" className="form-help">Queuing the selected collection. Awaiting an API acknowledgement, not completed collection.</p>}
      <footer className="form-actions"><Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
        <ActionButton type="submit" disabled={action.pending || !source.enabled || workspace.role === "viewer"}>Queue selected report</ActionButton></footer>
    </form>
  </FormDialog>;
}

interface Selection {
  id: string;
  generation: number;
  initial: AzureDevOpsCollectionResponse | null;
}

export function AzureDevOpsSourceCollections({ source }: { source: AzureDevOpsSourceConnection }) {
  const { workspace } = useSession();
  const load = useCallback((cursor: string | null, signal: AbortSignal) =>
    sourcesApi.azureDevOpsCollections(source.id, cursor, signal), [source.id]);
  const page = useSourcePage(load);
  const [confirmation, setConfirmation] = useState<HTMLElement | null>(null);
  const [selected, setSelected] = useState<Selection | null>(null);
  function select(id: string, initial: AzureDevOpsCollectionResponse | null = null) {
    setSelected((previous) => ({ id, initial, generation: (previous?.generation ?? 0) + 1 }));
  }
  function accepted(response: AzureDevOpsCollectionResponse) {
    page.accept(response.collection);
    select(response.collection.id, response);
    setConfirmation(null);
  }
  return <div className="source-selected">
    <section className="source-panel surface" aria-label="Azure DevOps source collections">
      <header className="source-panel-heading"><div><h2>Azure DevOps collections</h2><p className="source-name">{source.name}</p>
        <p className="form-help">{source.azureDevOps.organization} / {source.azureDevOps.projectId} / {source.azureDevOps.repositoryId}</p></div>
        <div className="source-actions">
          <ActionButton variant="outline" aria-disabled={page.pending} onClick={page.refresh}>Refresh collections</ActionButton>
          {workspace.role !== "viewer" && <ActionButton disabled={!source.enabled}
            onClick={(event) => setConfirmation(event.currentTarget)}>Collect report</ActionButton>}
        </div>
      </header>
      <div className="source-panel-content">
        <p className="form-help">{source.enabled ? "Enabled" : "Disabled"} for explicit collection. Stored PAT presence is not connected or live-verified status.</p>
        <SourceReadState error={page.error} pending={page.pending} loaded={page.data !== null} retry={page.retry} subject="Azure DevOps collections" />
        {page.data && <>
          <p className="form-help">Loaded: {page.data.items.length} collections in this page; {page.data.total} total at the last read.</p>
          {page.data.items.length === 0 ? <p>No Azure DevOps collections yet.</p> :
            <div className="source-table-scroll" tabIndex={0} role="region" aria-label="Azure DevOps collection history results">
              <table className="source-table" aria-label="Azure DevOps collections"><thead><tr><th scope="col">Collection</th><th scope="col">Action</th></tr></thead>
                <tbody>{page.data.items.map((item) => <tr key={item.id}>
                  <td><code>{item.id}</code><p>Build {item.selection.buildId}, {item.selection.artifactName} / {item.selection.artifactPath}</p>
                    <p>{label(item.state)} - {item.recordCount} raw records</p><p><SourceTime value={item.createdAt} /></p></td>
                  <td><Button type="button" variant="outline" size="sm" onClick={() => select(item.id)}>Open collection</Button></td>
                </tr>)}</tbody>
              </table>
            </div>}
          {page.data.nextCursor !== null && <ActionButton variant="outline" aria-disabled={page.pending} onClick={page.more}>Load more collections</ActionButton>}
        </>}
      </div>
    </section>
    {selected && <CollectionDetail key={selected.generation} id={selected.id} sourceId={source.id}
      profile="ado-services-build-artifacts" initial={selected.initial} />}
    {confirmation && <CollectAzureDevOpsSource source={source} returnFocus={confirmation}
      onClose={() => setConfirmation(null)} onAccepted={accepted} />}
  </div>;
}
