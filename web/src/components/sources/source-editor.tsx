import { useCallback, useLayoutEffect, useRef } from "react";
import type { FormEvent } from "react";
import { APIError } from "@/api/client";
import { sourcesApi } from "@/api/sources";
import type { SourceConnection, SourcePatch, SourceResponse } from "@/api/source-types";
import { formText, inputChoice } from "@/lib/application-input";
import { useResource } from "@/lib/use-resource";
import { useScopedAction } from "@/lib/use-scoped-action";
import { useSession } from "@/lib/session";
import { ActionButton } from "@/components/action-button";
import { FormDialog, FormError } from "@/components/form-dialog";
import { DataNotice } from "@/components/states";
import { Button } from "@/components/ui/button";
import { SourceReadState } from "./source-ui";

function SourceForm({ source, onClose, onSaved }: {
  source: SourceConnection | null; onClose: () => void; onSaved: (response: SourceResponse) => void;
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
      const name = formText(data, "name"), repository = formText(data, "repository"), replacement = formText(data, "token");
      const enabled = inputChoice(formText(data, "enabled"), ["true", "false"], "enabled state") === "true";
      if (!source) return sourcesApi.create({ profile: "github-cloud-app", name, repository, token: replacement, enabled }, signal);
      const patch: SourcePatch = {
        ...(name !== source.name && { name }), ...(repository !== source.repository && { repository }),
        ...(replacement !== "" && { token: replacement }), ...(enabled !== source.enabled && { enabled }),
      };
      if (Object.keys(patch).length === 0) throw new APIError("There are no source changes to save. A blank bearer keeps the stored credential.", "invalid-input", false);
      return sourcesApi.update(source, patch, signal);
    }, (response) => { clearToken(); onSaved(response); }, clearToken);
  }
  return <form className="application-form source-form" aria-label={source ? "Edit source" : "Create source"} aria-busy={action.pending} onSubmit={submit}>
    {source && <p className="form-help">Revision: {source.revision}. Stored credential: {source.credentialConfigured ? "configured" : "not configured"}.
      {" "}This is metadata, not a live-verified connection.</p>}
    <fieldset className="form-grid" disabled={action.pending}>
      <legend className="sr-only">Selected GitHub source</legend>
      <label className="full-width">Name<input name="name" required maxLength={256} autoComplete="off" defaultValue={source?.name ?? ""} /></label>
      <label className="full-width">Repository (owner/repo)<input name="repository" required maxLength={256} autoComplete="off" spellCheck={false}
        defaultValue={source?.repository ?? ""} aria-describedby="source-repository-help" /></label>
      <p id="source-repository-help" className="form-help full-width">Select one owner/repo, not a URL or organization search. This does not discover repositories or start collection.</p>
      <label className="full-width">{source ? "Replacement installation bearer (optional)" : "Installation bearer"}<input ref={token} name="token"
        type="password" required={!source} maxLength={16384} autoComplete="off" autoCapitalize="none" spellCheck={false} defaultValue=""
        aria-describedby="source-bearer-help" /></label>
      <p id="source-bearer-help" className="form-help full-width">{source ? "Leave blank to keep the stored bearer. " : ""}
        Supply an existing GitHub App installation bearer. The service stores it encrypted; the browser does not install an app, refresh a token or verify access.</p>
      <label className="full-width">Enabled<select name="enabled" required defaultValue={source ? String(source.enabled) : ""}>
        <option value="" disabled>Choose enabled state</option><option value="true">Enabled</option><option value="false">Disabled</option>
      </select></label>
      <p className="form-help full-width">Enabled permits later, explicitly confirmed collection of this selected repository. Saving performs no collection.</p>
    </fieldset>
    <FormError error={action.error} />
    {action.pending && <p ref={pending} tabIndex={-1} role="status" className="form-help">Saving with the service. Closing does not undo a request already sent.</p>}
    <footer className="form-actions"><Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
      <ActionButton type="submit" disabled={action.pending || workspace.role !== "admin"}>{source ? "Save source" : "Create source"}</ActionButton></footer>
  </form>;
}
function SourceEdit({ id, onClose, onSaved }: { id: string; onClose: () => void; onSaved: (response: SourceResponse) => void }) {
  const load = useCallback((signal: AbortSignal) => sourcesApi.source(id, signal), [id]);
  const resource = useResource(load);
  return <>
    <SourceReadState {...resource} pending={resource.status === "loading"} loaded={resource.data !== null} retry={resource.reload} subject="source" />
    {resource.data && <>
      {resource.data.dataOrigin && <DataNotice origin={resource.data.dataOrigin} />}
      <SourceForm key={resource.data.source.revision} source={resource.data.source} onClose={onClose} onSaved={onSaved} />
    </>}
  </>;
}
export function SourceEditor({ id, returnFocus, onClose, onSaved }: {
  id: string | null; returnFocus: HTMLElement; onClose: () => void; onSaved: (response: SourceResponse) => void;
}) {
  return <FormDialog title={id ? "Edit source" : "Create source"} containFocus returnFocus={returnFocus} onClose={onClose}
    description="Configure one selected GitHub repository using the github-cloud-app profile. Stored credentials do not imply collection success or live verification.">
    {id ? <SourceEdit id={id} onClose={onClose} onSaved={onSaved} /> : <SourceForm source={null} onClose={onClose} onSaved={onSaved} />}
  </FormDialog>;
}
