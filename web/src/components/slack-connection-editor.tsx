import { useCallback, useLayoutEffect, useRef } from "react";
import type { FormEvent } from "react";
import { APIError } from "@/api/client";
import { slackApi } from "@/api/slack";
import type { SlackConnection, SlackConnectionPatch, SlackConnectionResponse } from "@/api/types";
import { formText, inputChoice } from "@/lib/application-input";
import { useResource } from "@/lib/use-resource";
import { useScopedAction } from "@/lib/use-scoped-action";
import { useSession } from "@/lib/session";
import { ActionButton } from "./action-button";
import { FormDialog, FormError } from "./form-dialog";
import { DataNotice, LoadingState } from "./states";
import { Button } from "./ui/button";
import "./slack.css";

function ConnectionForm({ connection, onClose, onSaved }: {
  connection: SlackConnection | null; onClose: () => void; onSaved: (response: SlackConnectionResponse) => void;
}) {
  const { workspace } = useSession();
  const action = useScopedAction();
  const token = useRef<HTMLInputElement>(null);
  const pending = useRef<HTMLParagraphElement>(null);
  useLayoutEffect(() => {
    const field = token.current;
    return () => { if (field) field.value = ""; };
  }, []);
  useLayoutEffect(() => {
    if (action.pending) pending.current?.focus({ preventScroll: true });
  }, [action.pending]);
  const clearToken = () => { if (token.current) token.current.value = ""; };
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const data = new FormData(event.currentTarget);
    void action.run(async (signal) => {
      if (workspace.role !== "admin") throw new APIError("Only a workspace administrator can change connections.", "forbidden", false);
      const name = formText(data, "name"), channel = formText(data, "channel"), replacement = formText(data, "token");
      const enabled = inputChoice(formText(data, "enabled"), ["true", "false"], "enabled state") === "true";
      if (!connection) return slackApi.createConnection({ profile: "slack-workspace-bot", name, channel, token: replacement, enabled }, signal);
      const patch: SlackConnectionPatch = {
        ...(name !== connection.name && { name }), ...(channel !== connection.channel && { channel }),
        ...(enabled !== connection.enabled && { enabled }), ...(replacement !== "" && { token: replacement }),
      };
      if (Object.keys(patch).length === 0) throw new APIError("There are no connection changes to save. A blank token keeps the stored credential unchanged.", "invalid-input", false);
      return slackApi.updateConnection(connection, patch, signal);
    }, (response) => { clearToken(); onSaved(response); }, clearToken);
  }
  return <form aria-label={connection ? "Edit connection" : "Create connection"} className="application-form slack-form"
    onSubmit={submit} aria-busy={action.pending}>
    {connection && <p className="form-help">Revision: {connection.revision}. Stored credential: {connection.credentialConfigured ? "configured" : "not configured"}.
      {" "}This metadata is not live verification.</p>}
    <fieldset className="form-grid" disabled={action.pending}>
      <legend className="sr-only">Slack connection details</legend>
      <label className="full-width">Name<input name="name" required maxLength={256} defaultValue={connection?.name ?? ""} autoComplete="off" /></label>
      <label className="full-width">Channel ID<input name="channel" required pattern="[CG][A-Z0-9]{2,127}" maxLength={128}
        defaultValue={connection?.channel ?? ""} autoComplete="off" spellCheck={false} aria-describedby="slack-channel-help" /></label>
      <p id="slack-channel-help" className="form-help full-width">Use a C or G channel ID, not a #name or webhook URL. No Slack lookup or test send is performed.</p>
      <label className="full-width">{connection ? "Replacement bot token (optional)" : "Bot token"}<input ref={token} name="token" type="password"
        required={!connection} defaultValue="" maxLength={16384} autoComplete="off" autoCapitalize="none" spellCheck={false}
        aria-describedby="slack-token-help" /></label>
      <p id="slack-token-help" className="form-help full-width">{connection ? "Leave blank to keep the stored token. Enter a new token only to replace it. " : ""}
        The service stores the credential encrypted. Token drafts are cleared on close, success or session/workspace changes.</p>
      <label className="full-width">Enabled<select name="enabled" required defaultValue={connection ? String(connection.enabled) : ""}>
        <option value="" disabled>Choose enabled state</option>
        <option value="true">Enabled</option><option value="false">Disabled</option>
      </select></label>
      <p className="form-help full-width">Enabled allows explicitly confirmed finding notifications. It does not send or verify anything now.</p>
    </fieldset>
    <FormError error={action.error} />
    {action.pending && <p ref={pending} tabIndex={-1} role="status" className="form-help">Saving with the service. Closing this form does not undo a request already sent.</p>}
    <footer className="form-actions"><Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
      <ActionButton type="submit" disabled={action.pending || workspace.role !== "admin"}>{connection ? "Save connection" : "Create connection"}</ActionButton>
    </footer>
  </form>;
}

function EditConnection({ id, onClose, onSaved }: {
  id: string; onClose: () => void; onSaved: (response: SlackConnectionResponse) => void;
}) {
  const load = useCallback((signal: AbortSignal) => slackApi.connection(id, signal), [id]);
  const resource = useResource(load);
  return <>
    <FormError error={resource.error} />
    {resource.error && <ActionButton variant="outline" onClick={resource.reload}>Retry connection</ActionButton>}
    {resource.status === "loading" && !resource.data && <LoadingState label="Loading connection details" />}
    {resource.data && <>
      {resource.data.dataOrigin && <DataNotice origin={resource.data.dataOrigin} />}
      <ConnectionForm key={resource.data.connection.revision} connection={resource.data.connection} onClose={onClose} onSaved={onSaved} />
    </>}
  </>;
}

export function SlackConnectionEditor({ id, returnFocus, onClose, onSaved }: {
  id: string | null; returnFocus: HTMLElement; onClose: () => void; onSaved: (response: SlackConnectionResponse) => void;
}) {
  return <FormDialog title={id ? "Edit connection" : "Create connection"} containFocus returnFocus={returnFocus} onClose={onClose}
    description="Slack outbound finding notifications for the selected workspace. Configuration and credential presence are not connected or live-verified status.">
    {id ? <EditConnection id={id} onClose={onClose} onSaved={onSaved} /> : <ConnectionForm connection={null} onClose={onClose} onSaved={onSaved} />}
  </FormDialog>;
}
