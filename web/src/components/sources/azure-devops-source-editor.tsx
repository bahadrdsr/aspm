import { useCallback, useLayoutEffect, useRef } from "react";
import type { FormEvent } from "react";
import { APIError } from "@/api/client";
import { sourcesApi } from "@/api/sources";
import type {
  AzureDevOpsSourceConnection, AzureDevOpsSourcePatch, AzureDevOpsSourceResponse,
} from "@/api/source-types";
import { formText, inputChoice } from "@/lib/application-input";
import { useResource } from "@/lib/use-resource";
import { useScopedAction } from "@/lib/use-scoped-action";
import { useSession } from "@/lib/session";
import { ActionButton } from "@/components/action-button";
import { FormDialog, FormError } from "@/components/form-dialog";
import { DataNotice } from "@/components/states";
import { Button } from "@/components/ui/button";
import { SourceReadState } from "./source-ui";

function AzureDevOpsSourceForm({ source, onClose, onSaved }: {
  source: AzureDevOpsSourceConnection | null;
  onClose: () => void;
  onSaved: (response: AzureDevOpsSourceResponse) => void;
}) {
  const { workspace } = useSession();
  const action = useScopedAction();
  const token = useRef<HTMLInputElement>(null), pending = useRef<HTMLParagraphElement>(null);
  const clearToken = () => { if (token.current) token.current.value = ""; };
  useLayoutEffect(() => {
    const field = token.current;
    return () => { if (field) field.value = ""; };
  }, []);
  useLayoutEffect(() => { if (action.pending) pending.current?.focus({ preventScroll: true }); }, [action.pending]);
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    void action.run(async (signal) => {
      if (workspace.role !== "admin") throw new APIError("Only workspace administrators can edit sources.", "forbidden", false);
      const name = formText(data, "name"), replacement = formText(data, "token");
      const enabled = inputChoice(formText(data, "enabled"), ["true", "false"], "enabled state") === "true";
      if (!source) {
        return sourcesApi.createAzureDevOps({
          profile: "ado-services-build-artifacts", name, token: replacement, enabled,
          azureDevOps: {
            organization: formText(data, "organization"),
            projectId: formText(data, "projectId"),
            repositoryId: formText(data, "repositoryId"),
          },
        }, signal);
      }
      const patch: AzureDevOpsSourcePatch = {
        ...(name !== source.name && { name }),
        ...(replacement !== "" && { token: replacement }),
        ...(enabled !== source.enabled && { enabled }),
      };
      if (Object.keys(patch).length === 0) throw new APIError("There are no source changes to save. A blank PAT keeps the stored credential.", "invalid-input", false);
      return sourcesApi.updateAzureDevOps(source, patch, signal);
    }, (response) => { clearToken(); onSaved(response); }, clearToken);
  }
  return <form className="application-form source-form" aria-label={source ? "Edit Azure DevOps source" : "Create Azure DevOps source"}
    aria-busy={action.pending} onSubmit={submit}>
    {source && <div className="source-selection"><p>Revision: {source.revision}. Stored credential: {source.credentialConfigured ? "configured" : "not configured"}.</p>
      <p><strong>Immutable selected target</strong></p>
      <p>{source.azureDevOps.organization}</p><p><code>{source.azureDevOps.projectId}</code></p><p><code>{source.azureDevOps.repositoryId}</code></p>
      <p className="form-help">Create another source to change organization, project or repository.</p></div>}
    <fieldset className="form-grid" disabled={action.pending}>
      <legend className="sr-only">Selected Azure DevOps source</legend>
      <label className="full-width">Name<input name="name" required maxLength={256} autoComplete="off" defaultValue={source?.name ?? ""} /></label>
      {!source && <>
        <label className="full-width">Organization<input name="organization" required maxLength={255} autoComplete="off" spellCheck={false}
          aria-describedby="ado-target-help" /></label>
        <label>Project UUID<input name="projectId" required maxLength={36} autoComplete="off" spellCheck={false} /></label>
        <label>Repository UUID<input name="repositoryId" required maxLength={36} autoComplete="off" spellCheck={false} /></label>
        <p id="ado-target-help" className="form-help full-width">Enter one Azure DevOps Services organization and exact project/repository UUIDs. URLs, display-name discovery and organization enumeration are not performed.</p>
      </>}
      <label className="full-width">{source ? "Replacement PAT (optional)" : "Personal access token"}<input ref={token} name="token"
        type="password" required={!source} maxLength={16384} autoComplete="off" autoCapitalize="none" spellCheck={false}
        aria-describedby="ado-pat-help" /></label>
      <p id="ado-pat-help" className="form-help full-width">{source ? "Leave blank to keep the stored PAT. " : ""}
        Supply a short-lived PAT scoped to Code Read and Build Read for the selected organization. The service stores it encrypted and does not discover or renew it.</p>
      <label className="full-width">Enabled<select name="enabled" required defaultValue={source ? String(source.enabled) : ""}>
        <option value="" disabled>Choose enabled state</option><option value="true">Enabled</option><option value="false">Disabled</option>
      </select></label>
      <p className="form-help full-width">Saving configuration performs no Azure DevOps request and starts no collection.</p>
    </fieldset>
    <FormError error={action.error} />
    {action.pending && <p ref={pending} tabIndex={-1} role="status" className="form-help">Saving metadata with the service. Closing does not undo a request already sent.</p>}
    <footer className="form-actions"><Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
      <ActionButton type="submit" disabled={action.pending || workspace.role !== "admin"}>{source ? "Save source" : "Create source"}</ActionButton></footer>
  </form>;
}

function AzureDevOpsSourceEdit({ id, onClose, onSaved }: {
  id: string;
  onClose: () => void;
  onSaved: (response: AzureDevOpsSourceResponse) => void;
}) {
  const load = useCallback((signal: AbortSignal) => sourcesApi.azureDevOpsSource(id, signal), [id]);
  const resource = useResource(load);
  return <>
    <SourceReadState {...resource} pending={resource.status === "loading"} loaded={resource.data !== null}
      retry={resource.reload} subject="Azure DevOps source" />
    {resource.data && <>
      {resource.data.dataOrigin && <DataNotice origin={resource.data.dataOrigin} />}
      <AzureDevOpsSourceForm key={resource.data.source.revision} source={resource.data.source}
        onClose={onClose} onSaved={onSaved} />
    </>}
  </>;
}

export function AzureDevOpsSourceEditor({ id, returnFocus, onClose, onSaved }: {
  id: string | null;
  returnFocus: HTMLElement;
  onClose: () => void;
  onSaved: (response: AzureDevOpsSourceResponse) => void;
}) {
  return <FormDialog title={id ? "Edit Azure DevOps source" : "Create Azure DevOps source"} containFocus
    returnFocus={returnFocus} onClose={onClose}
    description="Configure one selected Azure DevOps Services repository. Stored PAT metadata is not live account verification.">
    {id ? <AzureDevOpsSourceEdit id={id} onClose={onClose} onSaved={onSaved} /> :
      <AzureDevOpsSourceForm source={null} onClose={onClose} onSaved={onSaved} />}
  </FormDialog>;
}
