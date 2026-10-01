import { useCallback, useLayoutEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import { APIError } from "@/api/client";
import { jiraApi } from "@/api/jira";
import { jiraFieldSources, jiraProfile } from "@/api/jira-types";
import type { JiraConnection, JiraConnectionPatch, JiraConnectionResponse, JiraFieldSource, JiraTarget } from "@/api/jira-types";
import { jiraMappings, sameJiraTarget, validateJiraName, validateJiraTarget } from "@/api/jira-input";
import { useResource } from "@/lib/use-resource";
import { useScopedAction } from "@/lib/use-scoped-action";
import { useSession } from "@/lib/session";
import { ActionButton } from "./action-button";
import { FormDialog, FormError } from "./form-dialog";
import { LoadingState } from "./states";
import { Button } from "./ui/button";
import "./jira.css";

const standardBase = (cloudId: string) => `https://api.atlassian.com/ex/jira/${cloudId}`;
interface Mapping { key: number; id: string; source: JiraFieldSource }

function JiraConnectionForm({ connection, onClose, onSaved, onDenied }: {
  connection: JiraConnection | null; onClose: () => void;
  onSaved: (response: JiraConnectionResponse) => void; onDenied: () => void;
}) {
  const { workspace } = useSession();
  const action = useScopedAction();
  const secret = useRef<HTMLInputElement>(null), pending = useRef<HTMLParagraphElement>(null);
  const nextMapping = useRef(16);
  const [tokenPresent, setTokenPresent] = useState(false);
  const [denied, setDenied] = useState(false);
  const [draft, setDraft] = useState({
    name: connection?.name ?? "", enabled: connection?.enabled ?? true,
    cloudId: connection?.jira.cloudId ?? "", siteOrigin: connection?.jira.siteOrigin ?? "",
    apiBase: connection?.jira.apiBase ?? "", project: connection?.jira.project ?? "", issueType: connection?.jira.issueType ?? "",
  });
  const [rows, setRows] = useState<Mapping[]>(() => Object.entries(connection?.jira.fieldMappings ?? {})
    .map(([id, source], key) => ({ key, id, source })));
  const clearToken = () => { if (secret.current) secret.current.value = ""; setTokenPresent(false); };
  useLayoutEffect(() => {
    const input = secret.current;
    return () => { if (input) input.value = ""; };
  }, []);
  useLayoutEffect(() => { if (action.pending) pending.current?.focus({ preventScroll: true }); }, [action.pending]);
  const original = connection?.jira;
  const targetChanged = original === undefined || draft.cloudId !== original.cloudId || draft.apiBase !== original.apiBase ||
    draft.siteOrigin !== original.siteOrigin || draft.project !== original.project || draft.issueType !== original.issueType ||
    rows.length !== Object.keys(original.fieldMappings).length ||
    new Set(rows.map((row) => row.id)).size !== rows.length ||
    rows.some((row) => original.fieldMappings[row.id] !== row.source);
  const changed = connection === null || draft.name !== connection.name || draft.enabled !== connection.enabled || targetChanged || tokenPresent;
  function update(field: "name" | "cloudId" | "siteOrigin" | "apiBase" | "project" | "issueType", value: string) {
    setDraft((current) => ({
      ...current, [field]: value,
      ...(field === "cloudId" && (current.apiBase === "" || current.apiBase === standardBase(current.cloudId)) &&
        { apiBase: standardBase(value) }),
    }));
  }
  function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    void action.run(async (signal) => {
      if (denied || workspace.role !== "admin") throw new APIError("Only a currently authorized workspace administrator may save Jira configuration.", "forbidden", false);
      const jira: JiraTarget = {
        credentialType: "oauth2-bearer", cloudId: draft.cloudId, apiBase: draft.apiBase, siteOrigin: draft.siteOrigin,
        project: draft.project, issueType: draft.issueType, fieldMappings: jiraMappings(rows),
      };
      validateJiraName(draft.name);
      validateJiraTarget(jira);
      const token = secret.current?.value ?? "";
      if (connection === null) return jiraApi.createConnection({ profile: jiraProfile, name: draft.name, enabled: draft.enabled, token, jira }, signal);
      const patch: JiraConnectionPatch = {
        ...(draft.name !== connection.name && { name: draft.name }),
        ...(draft.enabled !== connection.enabled && { enabled: draft.enabled }),
        ...(token !== "" && { token }), ...(!sameJiraTarget(jira, connection.jira) && { jira }),
      };
      return jiraApi.updateConnection(connection, patch, signal);
    }, (response) => { clearToken(); onSaved(response); }, (error) => {
      if (error.code === "forbidden" || error.code === "not-found") {
        clearToken(); setDenied(true); onDenied();
      }
    });
  }
  return <form aria-label={connection ? "Edit Jira connection" : "Add Jira connection"} className="application-form jira-form"
    noValidate onSubmit={submit} aria-busy={action.pending}>
    <p className="form-help">Jira Cloud v3 / OAuth bearer. Configuration is always not verified.
      {" "}There is no credential test, tenant authorization probe or issue creation here.</p>
    {connection && <p className="form-help">Revision: {connection.revision}. Stored credential: {connection.credentialConfigured ? "configured" : "not configured"}.
      {" "}A blank token preserves it; credentials are never read back.</p>}
    <fieldset className="form-grid" disabled={action.pending || denied}>
      <legend className="sr-only">Jira Cloud v3 target</legend>
      <label className="full-width">Name<input value={draft.name} onChange={(event) => update("name", event.target.value)} autoComplete="off" /></label>
      <label className="full-width">Cloud ID<input value={draft.cloudId} onChange={(event) => update("cloudId", event.target.value)} autoComplete="off" spellCheck={false} /></label>
      <label className="full-width">Site origin<input value={draft.siteOrigin} onChange={(event) => update("siteOrigin", event.target.value)} autoComplete="off" spellCheck={false} /></label>
      <label className="full-width">API base<input value={draft.apiBase} onChange={(event) => update("apiBase", event.target.value)} autoComplete="off" spellCheck={false} aria-describedby="jira-api-base-help" /></label>
      <p id="jira-api-base-help" className="form-help full-width">The api.atlassian.com address is a visible proposal, not approval.
        {" "}Review the exact credential destination. An independently approved gateway must keep the same /ex/jira/Cloud-ID path.</p>
      <label>Project<input value={draft.project} onChange={(event) => update("project", event.target.value)} autoComplete="off" spellCheck={false} /></label>
      <label>Issue type<input value={draft.issueType} onChange={(event) => update("issueType", event.target.value)} autoComplete="off" inputMode="numeric" /></label>
      <label className="full-width">OAuth bearer token<input ref={secret} type="password" defaultValue="" autoComplete="off" autoCapitalize="none"
        spellCheck={false} onChange={(event) => setTokenPresent(event.target.value !== "")} /></label>
      <p className="form-help full-width">{connection ? "Leave blank to keep the stored credential. " : ""}
        Token drafts are removed on close, success, workspace changes, logout or session loss. No Basic email/API token or OAuth installation is performed.</p>
      <label className="jira-enabled full-width"><input type="checkbox" checked={draft.enabled}
        onChange={(event) => setDraft((current) => ({ ...current, enabled: event.target.checked }))} />Enabled</label>
      <p className="form-help full-width">Enabled permits a separate, explicit work item review and queue. It does not verify permissions.</p>
      <div className="jira-mappings full-width">
        <h3>Custom field mappings</h3><p className="form-help">Up to 16 explicit customfield_ numeric IDs, each using one fixed string source. No JSON, templates, literals or arbitrary properties.</p>
        {rows.map((row) => <fieldset key={row.key} aria-label="Custom field mapping" className="jira-mapping">
          <label>Custom field ID<input value={row.id} onChange={(event) => setRows((current) => current.map((item) =>
            item.key === row.key ? { ...item, id: event.target.value } : item))} autoComplete="off" spellCheck={false} /></label>
          <label>String source<select value={row.source} onChange={(event) => {
            const source = jiraFieldSources.find((source) => source === event.target.value);
            if (source) setRows((current) => current.map((item) => item.key === row.key ? { ...item, source } : item));
          }}>{jiraFieldSources.map((source) => <option key={source} value={source}>{source}</option>)}</select></label>
          <Button type="button" variant="outline" size="sm" onClick={() => setRows((current) => current.filter((item) => item.key !== row.key))}>Remove field mapping</Button>
        </fieldset>)}
        <Button type="button" variant="outline" disabled={rows.length >= 16} onClick={() =>
          setRows((current) => [...current, { key: nextMapping.current++, id: "", source: "finding.id" }])}>Add field mapping</Button>
        <p className="form-help">{rows.length} of 16 mappings.</p>
      </div>
    </fieldset>
    <FormError error={action.error} />
    {action.pending && <p ref={pending} tabIndex={-1} role="status" className="form-help">Saving configuration with the service. Closing cannot undo a request already sent.</p>}
    <footer className="form-actions"><Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
      <ActionButton type="submit" disabled={!changed || action.pending || denied || workspace.role !== "admin"}>Save Jira connection</ActionButton></footer>
  </form>;
}

function EditJiraConnection({ id, onClose, onSaved, onDenied }: {
  id: string; onClose: () => void; onSaved: (response: JiraConnectionResponse) => void; onDenied: () => void;
}) {
  const load = useCallback((signal: AbortSignal) => jiraApi.connection(id, signal), [id]);
  const resource = useResource(load);
  return <>
    <FormError error={resource.error} />
    {resource.error && <ActionButton variant="outline" onClick={resource.reload}>Retry Jira connection</ActionButton>}
    {resource.status === "loading" && !resource.data && <LoadingState label="Loading Jira connection" />}
    {resource.data && <JiraConnectionForm key={resource.data.connection.revision} connection={resource.data.connection}
      onClose={onClose} onSaved={onSaved} onDenied={onDenied} />}
  </>;
}

export function JiraConnectionEditor({ id, returnFocus, onClose, onSaved, onDenied }: {
  id: string | null; returnFocus: HTMLElement; onClose: () => void;
  onSaved: (response: JiraConnectionResponse) => void; onDenied: () => void;
}) {
  return <FormDialog title={id ? "Edit Jira connection" : "Add Jira connection"} containFocus returnFocus={returnFocus} onClose={onClose}
    description="Explicit Jira Cloud v3 OAuth bearer configuration for the selected workspace. Stored metadata is not vendor permission or live verification.">
    {id ? <EditJiraConnection id={id} onClose={onClose} onSaved={onSaved} onDenied={onDenied} />
      : <JiraConnectionForm connection={null} onClose={onClose} onSaved={onSaved} onDenied={onDenied} />}
  </FormDialog>;
}
