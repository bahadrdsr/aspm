import { useCallback, useState } from "react";
import { APIError } from "@/api/client";
import { aiApi } from "@/api/ai";
import type { AIGrant } from "@/api/ai-types";
import { useResource } from "@/lib/use-resource";
import { ActionButton } from "@/components/action-button";
import { Button } from "@/components/ui/button";
import { FormDialog, FormError } from "@/components/form-dialog";
import { GrantApproval } from "./grant-approval";
import { AIReadState, AITime, grantStatus, useExpiryClock } from "./ai-ui";
import { useAIMutation } from "./use-ai-data";
import type { AIData } from "./use-ai-data";

function GrantFacts({ grant }: { grant: AIGrant }) {
  return <dl className="ai-facts">
    <div className="full-width"><dt>Grant ID</dt><dd>{grant.id}</dd></div>
    <div><dt>Profile ID</dt><dd>{grant.profileId}</dd></div>
    <div><dt>Profile revision</dt><dd>{grant.profileRevision}</dd></div>
    <div><dt>Policy revision</dt><dd>{grant.policyRevision}</dd></div>
    <div className="full-width"><dt>Destination</dt><dd>{grant.destination}</dd></div>
    <div><dt>Task</dt><dd>{grant.task}</dd></div><div><dt>Data class</dt><dd>{grant.dataClass}</dd></div>
    <div><dt>Expires</dt><dd><AITime value={grant.expiresAt} /></dd></div>
    <div><dt>Created</dt><dd><AITime value={grant.createdAt} /></dd></div>
    <div><dt>Granted by</dt><dd>{grant.grantedBy}</dd></div>
    {grant.revokedAt !== null && <><div><dt>Revoked</dt><dd><AITime value={grant.revokedAt} /></dd></div>
      <div><dt>Revoked by</dt><dd>{grant.revokedBy}</dd></div></>}
  </dl>;
}

function GrantDetails({ id, data, onClose }: { id: string; data: AIData; onClose: () => void }) {
  const load = useCallback((signal: AbortSignal) => data.readGrant(id, signal), [data.readGrant, id]);
  const resource = useResource(load);
  const grant = resource.data ? data.grants.get(id) : null;
  useExpiryClock(grant ? [grant.expiresAt] : []);
  const withheld = resource.status === "ready" && grant === null;
  return <section className="ai-selection" aria-label="Grant details">
    <header className="ai-section-heading"><h4>Grant details</h4><div className="ai-actions">
      <ActionButton variant="outline" disabled={resource.status === "loading"} onClick={resource.reload}>Refresh grant</ActionButton>
      <Button type="button" variant="ghost" onClick={onClose}>Close grant details</Button>
    </div></header>
    <div className="ai-content">
      <AIReadState subject="grant" error={resource.error} pending={resource.status === "loading"} retry={resource.reload} />
      {withheld && <><p role="alert" className="form-error">Grant metadata is withheld. Read its current details again.</p>
        <ActionButton variant="outline" onClick={resource.reload}>Retry grant</ActionButton></>}
      {grant && <><p>{grantStatus(grant, data.profiles.get(grant.profileId), data.policy)}</p><GrantFacts grant={grant} /></>}
    </div>
  </section>;
}

function RevokeGrant({ id, data, canWrite, returnFocus, onClose, onDenied }: {
  id: string; data: AIData; canWrite: boolean; returnFocus: HTMLElement; onClose: () => void; onDenied: () => void;
}) {
  const grant = data.grants.get(id);
  const action = useAIMutation(data.grants);
  function revoke() {
    void action.run(async (signal) => {
      const current = data.grants.get(id);
      if (!canWrite || !current) throw new APIError("An administrator and current grant metadata are required for revocation.", "forbidden", false);
      return aiApi.revokeGrant(current, signal);
    }, onClose, (error) => {
      if (error.code === "forbidden" || error.code === "not-found") data.grants.deny(id);
      if (error.code === "forbidden") onDenied();
    });
  }
  return <FormDialog title="Revoke AI grant" returnFocus={returnFocus} onClose={onClose} containFocus
    description="Explicitly revoke this stored workspace decision. Its original binding, issuer and history remain. No assessment is started.">
    <div className="application-form ai-form">
      {grant ? <GrantFacts grant={grant} /> : <p className="form-help">Grant metadata is withheld. Close and retry an authorized grant read.</p>}
      <FormError error={action.error} />
      {action.pending && <p role="status" className="form-help">Confirming revocation with the service...</p>}
      <footer className="form-actions"><Button type="button" variant="outline" onClick={onClose}>Cancel</Button>
        <ActionButton disabled={!grant || !canWrite || action.pending} onClick={revoke}>Confirm revocation</ActionButton></footer>
    </div>
  </FormDialog>;
}

export function AIGrants({ data, canWrite, onDenied }: { data: AIData; canWrite: boolean; onDenied: () => void }) {
  const page = data.grantPage;
  const [approving, setApproving] = useState(false);
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const [revoking, setRevoking] = useState<{ id: string; trigger: HTMLElement } | null>(null);
  useExpiryClock(page.rows.map((grant) => grant.expiresAt));
  function saved() { setApproving(false); }
  return <section className="surface ai-section" aria-label="AI grants">
    <header className="ai-section-heading"><div><h3>AI grants</h3><p className="form-help">Stored workspace decisions and their original history.</p></div>
      <div className="ai-actions">
        <ActionButton variant="outline" disabled={page.pending} onClick={page.refresh}>Refresh grants</ActionButton>
        <ActionButton disabled={!canWrite || data.policy?.mode !== "approved-hosted" || approving}
          onClick={() => setApproving(true)}>Add grant</ActionButton>
      </div>
    </header>
    <div className="ai-content">
      <AIReadState subject="grants" error={page.error} pending={page.pending} retry={page.retry} />
      <p className="form-help">Matches current configuration is only a point-in-time metadata label, never execution permission or a future guarantee.
        {" "}Expiry, revocation or changed configuration prevents a match. A later issuer role change alone is not revocation.</p>
      {page.page && <p className="form-help">Total at last read: {page.page.total} grants; {page.rows.length} shown.</p>}
      {page.page?.total === 0 && page.rows.length === 0 && !page.error && !page.pending && <div><h4>No AI grants</h4>
        <p className="form-help">The service returned an empty collection. Policy alone does not create a grant.</p></div>}
      {page.rows.length > 0 && <div className="ai-table-scroll" role="region" aria-label="AI grant results" tabIndex={0}>
        <table className="ai-table" aria-label="AI grants"><thead><tr><th scope="col">Grant</th><th scope="col">Actions</th></tr></thead>
          <tbody>{page.rows.map((grant) => <tr key={grant.id}>
            <td><strong>{grant.id}</strong><p>{grant.destination}</p>
              <p>{grantStatus(grant, data.profiles.get(grant.profileId), data.policy)}</p>
              <p>Profile {grant.profileId} - Revision: {grant.profileRevision}</p><p>Policy revision: {grant.policyRevision}</p>
              <p>{grant.task} / {grant.dataClass}</p>
              <p>Expires <AITime value={grant.expiresAt} /></p>
              <p>Created <AITime value={grant.createdAt} /> by {grant.grantedBy}</p>
              {grant.revokedAt !== null && <p>Revoked <AITime value={grant.revokedAt} /> by {grant.revokedBy}</p>}
            </td>
            <td><div className="ai-actions">
              <Button type="button" size="sm" variant="outline" onClick={() => setSelectedId(grant.id)}>View grant</Button>
              <Button type="button" size="sm" variant="outline" disabled={!canWrite || grant.revokedAt !== null}
                onClick={(event) => setRevoking({ id: grant.id, trigger: event.currentTarget })}>Revoke grant</Button>
            </div></td>
          </tr>)}</tbody>
        </table>
      </div>}
      {page.page?.nextCursor && <div className="ai-actions"><ActionButton variant="outline" disabled={page.pending} onClick={page.more}>Load more grants</ActionButton></div>}
      {approving && <GrantApproval data={data} canWrite={canWrite} onClose={() => setApproving(false)} onSaved={saved} onDenied={onDenied} />}
      {selectedId && <GrantDetails key={selectedId} id={selectedId} data={data} onClose={() => setSelectedId(null)} />}
    </div>
    {revoking && <RevokeGrant id={revoking.id} data={data} canWrite={canWrite} returnFocus={revoking.trigger}
      onClose={() => setRevoking(null)} onDenied={onDenied} />}
  </section>;
}
