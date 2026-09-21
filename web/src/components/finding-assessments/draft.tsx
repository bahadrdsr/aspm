import { useLayoutEffect, useRef, useState } from "react";
import type { FormEvent } from "react";
import { APIError } from "@/api/client";
import { assessmentsApi, contextProblem, isDefinitiveAssessmentRejection } from "@/api/assessments";
import { beginAssessment, rejectAssessment } from "@/api/assessment-intents";
import type { Assessment, AssessmentPreview } from "@/api/assessment-types";
import type { AIGrant, AIPolicy, AIProfile } from "@/api/ai-types";
import type { FindingDetail } from "@/api/types";
import { useScopedAction } from "@/lib/use-scoped-action";
import { useSession } from "@/lib/session";
import { ActionButton } from "../action-button";
import { FormError } from "../form-dialog";
import { Button } from "../ui/button";
import { AdvisoryNotice, ApprovedContext, AssessmentBindingFacts, AssessmentTime } from "./facts";

export interface AssessmentConfiguration {
  profiles: AIProfile[];
  grants: AIGrant[];
  policy: AIPolicy | null;
  ready: boolean;
}

function eligibleProfile(profile: AIProfile, policy: AIPolicy | null): boolean {
  return policy !== null && policy.mode !== "disabled" && profile.enabled && profile.structuredOutput &&
    (policy.mode !== "local-only" || profile.family === "local") &&
    (profile.family === "local" || profile.credentialConfigured);
}
function eligibleGrant(grant: AIGrant, profile: AIProfile | undefined, policy: AIPolicy | null): boolean {
  return profile !== undefined && policy?.mode === "approved-hosted" && grant.profileId === profile.id &&
    grant.workspaceId === profile.workspaceId && grant.profileRevision === profile.revision &&
    grant.policyRevision === policy.revision && grant.destination === profile.endpoint &&
    grant.task === "finding-validity" && grant.dataClass === "finding-evidence" &&
    grant.revokedAt === null && Date.parse(grant.expiresAt) > Date.now();
}

export function AssessmentDraft({ finding, configuration, unresolved, canWrite, onPending, onReceipt, onDenied, onDiscard }: {
  finding: FindingDetail; configuration: AssessmentConfiguration; unresolved: boolean; canWrite: boolean;
  onPending: () => void; onReceipt: (receipt: Assessment) => void; onDenied: (error: APIError) => void; onDiscard: () => void;
}) {
  const { session, workspace } = useSession();
  const [observationId, setObservationId] = useState(""), [profileId, setProfileId] = useState(""), [grantId, setGrantId] = useState("");
  const [context, setContext] = useState(""), [reviewed, setReviewed] = useState(false), [consent, setConsent] = useState(false);
  const [snapshot, setSnapshot] = useState<AssessmentPreview | null>(null);
  const [feedback, setFeedback] = useState<APIError | null>(null);
  const generation = useRef(0);
  const preparing = useScopedAction(), queueing = useScopedAction();
  const busy = preparing.pending || queueing.pending;
  const { profiles, grants, policy, ready } = configuration;
  const profile = profiles.find((item) => item.id === profileId);
  const observation = finding.observations?.find((item) => item.id === observationId);
  const grant = grants.find((item) => item.id === grantId);
  const local = policy?.mode === "local-only";
  const profileAllowed = profile !== undefined && eligibleProfile(profile, policy);
  const grantAllowed = local ? grantId === "" : grant !== undefined && eligibleGrant(grant, profile, policy);
  const problem = contextProblem(context);
  const metadataIdentity = JSON.stringify(configuration);
  const observationIdentity = observation ? JSON.stringify([observation.id, observation.evidenceDigest]) : "";
  useLayoutEffect(() => {
    generation.current++;
    setSnapshot(null); setConsent(false); setReviewed(false);
    if (local) setGrantId("");
  }, [metadataIdentity, observationIdentity, local]);
  const valid = canWrite && !unresolved && ready && profileAllowed && grantAllowed && observation !== undefined && !problem && reviewed;
  const currentPreview = snapshot !== null && valid && snapshot.context === context && snapshot.observationId === observationId &&
    snapshot.profileId === profileId && snapshot.grantId === grantId && snapshot.profileRevision === profile?.revision &&
    snapshot.policyRevision === policy?.revision && Date.parse(snapshot.expiresAt) > Date.now();
  function edit(change: () => void) {
    generation.current++;
    setSnapshot(null); setConsent(false); setReviewed(false); setFeedback(null);
    change();
  }
  function expire(): boolean {
    if (!snapshot || Date.parse(snapshot.expiresAt) > Date.now()) return false;
    generation.current++;
    setSnapshot(null); setConsent(false); setReviewed(false);
    setFeedback(new APIError("This preview expired. Review current configuration and prepare a new preview explicitly.", "conflict", false));
    return true;
  }
  function prepare(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (!valid || !observation || !profile || !policy || busy) return;
    const started = ++generation.current;
    setSnapshot(null); setConsent(false); setFeedback(null);
    void preparing.run((signal) => assessmentsApi.preview(finding.id, {
      observationId, profileId, grantId: local ? "" : grantId, context, reviewed: true,
    }, { observation, profile, policy, grant: local ? null : grant ?? null, requestedBy: session.user.id }, signal), (receipt) => {
      if (started !== generation.current) return;
      if (Date.parse(receipt.expiresAt) <= Date.now()) {
        setReviewed(false);
        setFeedback(new APIError("The returned preview has expired. Review current facts and prepare again explicitly.", "conflict", false));
      } else { setSnapshot(receipt); setConsent(false); }
    }, (error) => {
      if (error.code === "forbidden") onDenied(error);
      if (started !== generation.current) return;
      setFeedback(error); setSnapshot(null); setConsent(false);
      if (["conflict", "not-found"].includes(error.code)) setReviewed(false);
    });
  }
  function queue() {
    if (expire() || !currentPreview || !snapshot || !consent || busy) return;
    const approved = snapshot;
    const acceptReceipt = onReceipt;
    let intent: ReturnType<typeof beginAssessment> | null = null;
    setFeedback(null);
    void queueing.run((signal) => {
      intent = beginAssessment(workspace.id, session.user.id, finding.id, approved.id);
      onPending();
      return assessmentsApi.queue(finding.id, session.user.id, {
        previewId: intent.previewId, idempotencyKey: intent.idempotencyKey, consent: true,
      }, signal);
    }, acceptReceipt, (error) => {
      if (intent && isDefinitiveAssessmentRejection(error)) rejectAssessment(intent);
      setSnapshot(null); setConsent(false); setReviewed(false);
      if (["network", "unavailable", "invalid-response"].includes(error.code)) setContext("");
      setFeedback(error);
      onPending();
      if (error.code === "forbidden") onDenied(error);
    });
  }
  return <div className="assessment-draft" onFocusCapture={() => { expire(); }}>
    <form aria-label="Prepare AI assessment" className="application-form assessment-form" aria-busy={busy} onSubmit={prepare}>
      <h3>Prepare AI assessment</h3>
      <p className="form-help">Choose an actual observation from this finding's loaded history, not an implicit latest scan.
        {" "}The existing Load more observations control can load additional choices. No evidence is copied into the context editor.</p>
      <fieldset className="form-grid" disabled={busy || !canWrite || unresolved}>
        <legend className="sr-only">Explicit assessment selections</legend>
        <label className="full-width">Observation<select required value={observationId}
          onChange={(event) => edit(() => setObservationId(event.target.value))}>
          <option value="" disabled>Select an observation</option>
          {(finding.observations ?? []).map((item) => <option key={item.id} value={item.id}>{item.id} - {item.scanId}</option>)}
        </select></label>
        <label className="full-width">AI profile<select required value={profileId} disabled={!ready}
          onChange={(event) => edit(() => { setProfileId(event.target.value); setGrantId(""); })}>
          <option value="" disabled>Select a profile</option>
          {profiles.map((item) => <option key={item.id} value={item.id} disabled={!eligibleProfile(item, policy)}>
            {item.name} - {item.family}{!item.enabled ? " (disabled)" : !item.structuredOutput ? " (capability not reviewed)" : ""}
          </option>)}
        </select></label>
        <label className="full-width">AI grant<select required={!local} value={grantId} disabled={local || !ready || !profileAllowed}
          onChange={(event) => edit(() => setGrantId(event.target.value))}>
          <option value="" disabled>{local ? "No grant for local-only" : "Select an exact current grant"}</option>
          {!local && grants.filter((item) => item.profileId === profileId).map((item) => <option key={item.id}
            value={item.id} disabled={!eligibleGrant(item, profile, policy)}>{item.id}
            {item.revokedAt !== null ? " (revoked)" : Date.parse(item.expiresAt) <= Date.now() ? " (expired)" :
              !eligibleGrant(item, profile, policy) ? " (configuration changed)" : ""}</option>)}
        </select></label>
        <label className="full-width">Reviewed finding context<textarea rows={7} value={context} required autoComplete="off" spellCheck={false}
          aria-invalid={context !== "" && problem !== null} aria-describedby="assessment-context-help"
          onChange={(event) => edit(() => setContext(event.target.value))} /></label>
        <p id="assessment-context-help" className="form-help full-width">{new TextEncoder().encode(context).byteLength} / 32768 UTF-8 bytes.
          {" "}Exact spaces, newlines and Unicode are preserved. Do not include credentials, raw reports, unreviewed notes or configuration secrets.
          {" "}Review and redact this derived text yourself; automated checks cannot guarantee universal secret removal.</p>
        {context !== "" && problem && <p className="form-help full-width">{problem}</p>}
        <label className="assessment-checkbox full-width"><input type="checkbox" checked={reviewed}
          onChange={(event) => { setReviewed(event.target.checked); setSnapshot(null); setConsent(false); generation.current++; }} />
          I reviewed and redacted this derived context</label>
      </fieldset>
      {profile && <p className="form-help">Selected family: {profile.family}; model: {profile.model}; deployment: {profile.deployment || "not applicable"}.
        {" "}Destination: {profile.endpoint}. Stored credential presence is configuration metadata, not ready or verified provider access.</p>}
      {local && <p className="form-help">Local-only permits only family local with an empty grant ID. A hosted family on loopback is not local processing.</p>}
      {!local && profile && !grantAllowed && <p className="form-help">An explicit, unexpired, nonrevoked grant must match this profile, exact destination,
        {" "}opaque profile and policy revisions, task and data class. Expired or changed grants cannot authorize a preview.</p>}
      {!profileAllowed && <p className="form-help">Choose an enabled, structured-output-reviewed profile permitted by the current policy.
        {" "}Capability review and stored configuration do not prove execution authority or model quality.</p>}
      <FormError error={feedback} />
      {preparing.pending && <p role="status" className="form-help">Preparing the server's canonical preview. This is not queue approval.</p>}
      {queueing.pending && <p role="status" className="form-help">Awaiting a queue receipt, not an assessment result. Closing does not undo a committed request.</p>}
      <div className="assessment-actions"><Button type="button" variant="outline" onClick={onDiscard}>Discard assessment draft</Button>
        <ActionButton type="submit" disabled={!valid || busy}>Prepare assessment preview</ActionButton></div>
    </form>
    {snapshot && <section className="assessment-preview" aria-label="Assessment preview">
      <h3>Assessment preview</h3><p className="form-help">Canonical server preview <code>{snapshot.id}</code></p>
      <AssessmentBindingFacts value={snapshot} />
      <p className="form-help">Created <AssessmentTime value={snapshot.createdAt} />.
        {" "}Preview expires at <AssessmentTime value={snapshot.expiresAt} label="Preview expires at" />.</p>
      <ApprovedContext context={snapshot.context} />
      <AdvisoryNotice />
      <p className="form-help">Only this reviewed derived context and fixed server instructions are approved.
        {" "}Credential exclusion is not a universal redaction guarantee. An expired or changed preview requires a new manual review.
        {" "}Queueing records a durable intent; a 202 receipt means queued, not completed.</p>
      <label className="assessment-checkbox"><input type="checkbox" checked={consent && currentPreview} disabled={!currentPreview || busy}
        onChange={(event) => { if (!expire()) setConsent(event.target.checked); }} />
        Approve this exact preview for queueing</label>
      <ActionButton disabled={!currentPreview || !consent || busy} onClick={queue}>Queue assessment</ActionButton>
    </section>}
  </div>;
}
