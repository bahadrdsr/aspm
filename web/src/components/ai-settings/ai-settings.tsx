import { useState } from "react";
import { useSession } from "@/lib/session";
import { ActionButton } from "@/components/action-button";
import { Button } from "@/components/ui/button";
import { AIGrants } from "./ai-grants";
import { AIPolicySettings } from "./ai-policy";
import { ProfileEditor } from "./profile-editor";
import { AIReadState, AITime } from "./ai-ui";
import { useAIData } from "./use-ai-data";
import "./ai-settings.css";

export function AISettings({ onClose }: { onClose: () => void }) {
  const { workspace } = useSession();
  const data = useAIData(workspace.id);
  const page = data.profilePage;
  const [writeDenied, setWriteDenied] = useState(false);
  const [editor, setEditor] = useState<{ id: string | null; trigger: HTMLElement } | null>(null);
  const canWrite = workspace.role === "admin" && !writeDenied;
  function saved() { setEditor(null); }
  const denied = () => setWriteDenied(true);
  return <section className="ai-settings" aria-label="AI settings">
    <header className="ai-section-heading"><div><h2>AI settings</h2>
      <p className="form-help">Configuration only. Setup starts no assessment, sends no evidence and makes no provider requests.</p></div>
      <ActionButton variant="outline" onClick={onClose}>Close AI settings</ActionButton></header>
    <p className="form-help">Profiles, policy and grants are persisted by the selected workspace's API, not in browser storage.
      {" "}Credential presence and capability review are configuration metadata, not tested provider access.</p>
    {!canWrite && <p className="form-help" role={writeDenied ? "alert" : undefined}>
      {writeDenied ? "Permission denied by the service. AI settings are read only; cached administrator membership does not override this denial."
        : "Read only. Only workspace administrators can change AI configuration or grant decisions."}
    </p>}
    <section className="surface ai-section" aria-label="AI profiles">
      <header className="ai-section-heading"><div><h3>AI profiles</h3><p className="form-help">Explicit provider choices, with no default model, endpoint or tested capability.</p></div>
        <div className="ai-actions">
          <ActionButton variant="outline" disabled={page.pending} onClick={page.refresh}>Refresh profiles</ActionButton>
          <ActionButton disabled={!canWrite} onClick={(event) => setEditor({ id: null, trigger: event.currentTarget })}>Add profile</ActionButton>
        </div>
      </header>
      <div className="ai-content">
        <AIReadState subject="profiles" error={page.error} pending={page.pending} retry={page.retry} />
        {page.page && <p className="form-help">Total at last read: {page.page.total} profiles; {page.rows.length} shown.</p>}
        {page.page?.total === 0 && page.rows.length === 0 && !page.error && !page.pending && <div><h4>No AI profiles</h4>
          <p className="form-help">The service returned an empty collection. An administrator can configure a profile without contacting a provider.</p></div>}
        {page.rows.length > 0 && <div className="ai-table-scroll" role="region" aria-label="AI profile results" tabIndex={0}>
          <table className="ai-table" aria-label="AI profiles"><thead><tr><th scope="col">Profile</th><th scope="col">Actions</th></tr></thead>
            <tbody>{page.rows.map((profile) => <tr key={profile.id}>
              <td><strong>{profile.name}</strong><p>{profile.family}</p><p>{profile.endpoint}</p><p>{profile.model}</p>
                {profile.deployment !== "" && <p>Deployment: {profile.deployment}</p>}
                <p>{profile.enabled ? "Enabled" : "Disabled"} - Structured output: {profile.structuredOutput ? "reviewed" : "not reviewed"}</p>
                <p>Credential: {profile.credentialConfigured ? "configured" : "not configured"}. Not provider verification.</p>
                <p>Revision: {profile.revision}</p>
                <p>Updated <AITime value={profile.updatedAt} /></p>
              </td>
              <td><Button type="button" variant="outline" size="sm" disabled={!canWrite}
                onClick={(event) => setEditor({ id: profile.id, trigger: event.currentTarget })}>Edit profile</Button></td>
            </tr>)}</tbody>
          </table>
        </div>}
        {page.page?.nextCursor && <div className="ai-actions"><ActionButton variant="outline" disabled={page.pending} onClick={page.more}>Load more profiles</ActionButton></div>}
      </div>
    </section>
    <AIPolicySettings data={data} canWrite={canWrite} onDenied={denied} />
    <AIGrants data={data} canWrite={canWrite} onDenied={denied} />
    {editor && <ProfileEditor id={editor.id} data={data} canWrite={canWrite} returnFocus={editor.trigger}
      onClose={() => setEditor(null)} onSaved={saved} onDenied={denied} />}
  </section>;
}
