import { useLayoutEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import { APIError } from "@/api/client";
import { webhookApi } from "@/api/generic-webhooks";
import type {
  WebhookConnection, WebhookConnectionPatch, WebhookConnectionResponse,
} from "@/api/generic-webhooks";
import { useScopedAction } from "@/lib/use-scoped-action";
import { useSession } from "@/lib/session";
import { ActionButton } from "./action-button";
import { FormDialog, FormError } from "./form-dialog";
import { Button } from "./ui/button";

function WebhookConnectionForm({ connection, onClose, onSaved }: {
  connection: WebhookConnection | null;
  onClose: () => void;
  onSaved: (response: WebhookConnectionResponse) => void;
}) {
  const { workspace } = useSession();
  const action = useScopedAction();
  const secret = useRef<HTMLInputElement>(null);
  const pending = useRef<HTMLParagraphElement>(null);
  const [draft, setDraft] = useState({
    name: connection?.name ?? "",
    webhookUrl: connection ? connection.webhook.origin + connection.webhook.path : "",
    enabled: connection?.enabled ?? true,
  });
  const [secretPresent, setSecretPresent] = useState(false);
  const clear = () => { if (secret.current) secret.current.value = ""; setSecretPresent(false); };
  useLayoutEffect(() => { const field = secret.current; return () => { if (field) field.value = ""; }; }, []);
  useLayoutEffect(() => { if (action.pending) pending.current?.focus({ preventScroll: true }); }, [action.pending]);
  const changed = connection === null || draft.name !== connection.name ||
    draft.webhookUrl !== connection.webhook.origin + connection.webhook.path ||
    draft.enabled !== connection.enabled || secretPresent;
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!changed || action.pending) return;
    void action.run(async (signal) => {
      if (workspace.role !== "admin") throw new APIError(
        "Only a current administrator may configure generic webhooks.", "forbidden", false);
      const privateSecret = secret.current?.value ?? "";
      if (!connection) return webhookApi.createConnection({
        profile: "generic-webhook-v1", name: draft.name, enabled: draft.enabled,
        webhookUrl: draft.webhookUrl, secret: privateSecret,
      }, signal);
      const patch: WebhookConnectionPatch = {
        ...(draft.name !== connection.name && { name: draft.name }),
        ...(draft.enabled !== connection.enabled && { enabled: draft.enabled }),
        ...(draft.webhookUrl !== connection.webhook.origin + connection.webhook.path &&
          { webhookUrl: draft.webhookUrl }),
        ...(privateSecret !== "" && { secret: privateSecret }),
      };
      return webhookApi.updateConnection(connection, patch, signal);
    }, (response) => { clear(); onSaved(response); }, (error) => {
      if (["forbidden", "not-found", "unauthorized"].includes(error.code)) clear();
    });
  }
  return <form aria-label={connection ? "Edit webhook connection" : "Add webhook connection"}
    className="application-form webhook-form" noValidate onSubmit={submit} aria-busy={action.pending}>
    <p className="form-help">Fixed signed JSON only. The operator must approve the HTTPS origin separately.
      Saving configuration never sends a test request.</p>
    {connection && <p className="form-help">Revision: {connection.revision}. Stored HMAC secret:
      {" "}{connection.credentialConfigured ? "configured" : "not configured"}. A blank replacement preserves it.</p>}
    <fieldset className="form-grid" disabled={action.pending}>
      <legend className="sr-only">Generic webhook configuration</legend>
      <label className="full-width">Name<input value={draft.name} autoComplete="off" maxLength={256}
        onChange={(event) => setDraft((value) => ({ ...value, name: event.target.value }))} /></label>
      <label className="full-width">Webhook URL<input value={draft.webhookUrl} autoComplete="off"
        autoCapitalize="none" spellCheck={false}
        onChange={(event) => setDraft((value) => ({ ...value, webhookUrl: event.target.value }))} /></label>
      <label className="full-width">{connection ? "Replacement HMAC Secret" : "HMAC Secret"}
        <input ref={secret} type="password" defaultValue="" autoComplete="off" autoCapitalize="none"
          spellCheck={false} onChange={(event) => setSecretPresent(event.target.value !== "")} /></label>
      <label className="webhook-checkbox full-width"><input type="checkbox" checked={draft.enabled}
        onChange={(event) => setDraft((value) => ({ ...value, enabled: event.target.checked }))} />Enabled</label>
    </fieldset>
    <FormError error={action.error} />
    {action.pending && <p ref={pending} tabIndex={-1} role="status" className="form-help">
      Saving webhook configuration. This does not contact the receiver.</p>}
    <footer className="form-actions"><Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
      <ActionButton type="submit" disabled={!changed || action.pending || workspace.role !== "admin"}>
        {connection ? "Save webhook connection" : "Add webhook connection"}
      </ActionButton></footer>
  </form>;
}

export function WebhookConnectionEditor({ connection, returnFocus, onClose, onSaved }: {
  connection: WebhookConnection | null; returnFocus: HTMLElement; onClose: () => void;
  onSaved: (response: WebhookConnectionResponse) => void;
}) {
  return <FormDialog title={connection ? "Edit webhook connection" : "Add webhook connection"} containFocus
    description="Explicit fixed-payload webhook configuration for this workspace."
    returnFocus={returnFocus} onClose={onClose}>
    <WebhookConnectionForm connection={connection} onClose={onClose} onSaved={onSaved} />
  </FormDialog>;
}
