import { useId, useState } from "react";
import { APIError } from "@/api/client";
import { aiApi } from "@/api/ai";
import { utcExpiry } from "@/api/ai-input";
import type { AIGrant, AIPolicy, AIProfile } from "@/api/ai-types";
import { useScopedAction } from "@/lib/use-scoped-action";
import { ActionButton } from "@/components/action-button";
import { Button } from "@/components/ui/button";
import { FormError } from "@/components/form-dialog";
import { useExpiryClock } from "./ai-ui";
import { sameMetadata, useAIMutation } from "./use-ai-data";
import type { AIData } from "./use-ai-data";

interface ApprovalProps {
  data: AIData;
  canWrite: boolean;
  onClose: () => void;
  onSaved: (grant: AIGrant) => void;
  onDenied: () => void;
}
interface ReviewedFacts { profile: AIProfile; policy: AIPolicy }

function ConsentDraft({ selectedId, data, canWrite, onClose, onSaved, onDenied }: ApprovalProps & { selectedId: string }) {
  const review = useScopedAction(), create = useAIMutation(data.grants);
  const [reviewed, setReviewed] = useState<ReviewedFacts | null>(null);
  const [acknowledged, setAcknowledged] = useState(false);
  const [expiry, setExpiry] = useState("");
  const [error, setError] = useState<APIError | null>(null);
  const expiryHelp = useId();
  useExpiryClock(expiry === "" ? [] : [`${expiry}Z`]);
  const selected = data.profiles.get(selectedId);
  const current = reviewed !== null && canWrite && sameMetadata(reviewed.profile, selected) && sameMetadata(reviewed.policy, data.policy);
  if (reviewed && !current) {
    setReviewed(null);
    setAcknowledged(false);
    setError(new APIError("Configuration changed or metadata was withheld. Review current configuration again and approve the new facts.", "conflict", false));
  }
  function reviewCurrent() {
    setReviewed(null);
    setAcknowledged(false);
    setError(null);
    void review.run(async (signal) => {
      if (!canWrite || selectedId === "") throw new APIError("Select a profile as a workspace administrator before reviewing.", "forbidden", false);
      const [profile, policy] = await Promise.all([data.readProfile(selectedId, signal), data.readPolicy(signal)]);
      return { profile, policy };
    }, (facts) => {
      if (!sameMetadata(facts.profile, data.profiles.get(selectedId)) || !sameMetadata(facts.policy, data.policies.get(facts.policy.workspaceId)) ||
        !facts.profile.enabled || !facts.profile.structuredOutput || facts.policy.mode !== "approved-hosted") {
        setError(new APIError("The current profile must be enabled and structured-output reviewed, with approved-hosted policy. Review current configuration again after changes.", "conflict", false));
        return;
      }
      setReviewed(facts);
    }, setError);
  }
  const validExpiry = utcExpiry(expiry);
  const canCreate = current && acknowledged && validExpiry !== null && !create.pending && !review.pending;
  function approve() {
    setError(null);
    void create.run(async (signal) => {
      const expiresAt = utcExpiry(expiry);
      if (!canWrite || !current || !reviewed || !acknowledged || expiresAt === null ||
        !sameMetadata(reviewed.profile, data.profiles.get(selectedId)) || !sameMetadata(reviewed.policy, data.policies.get(reviewed.policy.workspaceId))) {
        throw new APIError("Review current facts, explicitly approve sensitive finding evidence and choose a future UTC expiry.", "conflict", false);
      }
      return aiApi.createGrant({
        profileId: reviewed.profile.id, profileRevision: reviewed.profile.revision, policyRevision: reviewed.policy.revision,
        destination: reviewed.profile.endpoint, task: "finding-validity", dataClass: "finding-evidence", expiresAt,
      }, signal);
    }, onSaved, (cause) => {
      setReviewed(null);
      setAcknowledged(false);
      setError(cause);
      if (cause.code === "conflict" || cause.code === "forbidden" || cause.code === "not-found") {
        data.profiles.deny(selectedId);
        data.policies.clear();
      }
      if (cause.code === "forbidden") onDenied();
    });
  }
  return <>
    <fieldset className="form-grid" disabled={!canWrite || create.pending}>
      <legend className="sr-only">Finite consent</legend>
      <label className="full-width">Expires at (UTC)<input type="datetime-local" value={expiry} required step="60"
        onChange={(event) => setExpiry(event.target.value)} autoComplete="off" aria-describedby={expiryHelp} /></label>
      <p id={expiryHelp} className="form-help full-width">Enter a finite future UTC time, not your browser's local timezone.
        {" "}No default, indefinite approval or automatic extension is supplied.</p>
    </fieldset>
    <div className="ai-actions"><ActionButton variant="outline" disabled={!canWrite || selectedId === "" || review.pending || create.pending}
      onClick={reviewCurrent}>Review current configuration</ActionButton></div>
    {review.pending && <p role="status" className="form-help">Loading current profile and policy for consent...</p>}
    {current && reviewed && <section className="ai-selection" aria-label="Grant consent">
      <h4>Grant consent</h4>
      <dl className="ai-facts">
        <div><dt>Profile</dt><dd>{reviewed.profile.name}</dd></div>
        <div><dt>Family</dt><dd>{reviewed.profile.family}</dd></div>
        <div><dt>Model</dt><dd>{reviewed.profile.model}</dd></div>
        {reviewed.profile.deployment !== "" && <div><dt>Deployment</dt><dd>{reviewed.profile.deployment}</dd></div>}
        <div className="full-width"><dt>Destination</dt><dd>{reviewed.profile.endpoint}</dd></div>
        <div><dt>Profile revision</dt><dd>{reviewed.profile.revision}</dd></div>
        <div><dt>Policy revision</dt><dd>{reviewed.policy.revision}</dd></div>
        <div><dt>Task</dt><dd>finding-validity</dd></div><div><dt>Data class</dt><dd>finding-evidence</dd></div>
      </dl>
      <p>Finding evidence may include potentially sensitive application/repository metadata, source locations and untrusted evidence or code context.
        {" "}Credentials and configuration keys are excluded from approved content. This setup does not read or transmit that content.</p>
      <p>This review is a point-in-time snapshot, not execution permission or a future permission guarantee. No assessment starts here.</p>
    </section>}
    <label className="ai-checkbox"><input type="checkbox" checked={acknowledged && current} disabled={!current || create.pending || review.pending}
      onChange={(event) => setAcknowledged(event.target.checked)} />Approve potentially sensitive finding evidence</label>
    <FormError error={error} />
    {create.pending && <p role="status" className="form-help">Saving the grant decision. Closing does not undo a request already sent.</p>}
    <div className="ai-actions"><Button type="button" variant="outline" onClick={onClose}>Cancel grant</Button>
      <ActionButton disabled={!canCreate} onClick={approve}>Create grant</ActionButton></div>
  </>;
}

export function GrantApproval(props: ApprovalProps) {
  const [selectedId, setSelectedId] = useState("");
  const choices = props.data.profiles.values();
  const selected = props.data.profiles.get(selectedId);
  return <section className="ai-selection" aria-label="Grant approval">
    <h4>Grant approval</h4>
    <form aria-label="Approve AI grant" className="application-form ai-form" onSubmit={(event) => event.preventDefault()}>
      <div className="form-grid"><label className="full-width">Profile<select value={selectedId} disabled={!props.canWrite}
        onChange={(event) => {
          const id = event.target.value;
          if (id === "" || choices.some((profile) => profile.id === id && profile.enabled && profile.structuredOutput)) setSelectedId(id);
        }}>
        <option value="">Choose an eligible profile</option>
        {selectedId !== "" && !selected && <option value={selectedId} disabled>Selected profile metadata withheld - review again</option>}
        {choices.map((profile) => <option key={profile.id} value={profile.id} disabled={!profile.enabled || !profile.structuredOutput}>
          {profile.name}{!profile.enabled ? " (disabled)" : !profile.structuredOutput ? " (not reviewed)" : ""}
        </option>)}
      </select></label></div>
      <ConsentDraft key={selectedId} selectedId={selectedId} {...props} />
    </form>
  </section>;
}
