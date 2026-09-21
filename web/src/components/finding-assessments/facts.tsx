import type { ReactNode } from "react";
import type { AssessmentBinding } from "@/api/assessment-types";
import { timestampLabel } from "@/lib/format";

export function AssessmentTime({ value, label }: { value: string; label?: string }) {
  return <time dateTime={value} aria-label={label}>{timestampLabel(value)}</time>;
}

export function AssessmentFact({ name, children }: { name: string; children: ReactNode }) {
  return <div><dt>{name}</dt><dd>{children}</dd></div>;
}

export function AssessmentBindingFacts({ value }: { value: AssessmentBinding }) {
  return <dl className="assessment-facts">
    <AssessmentFact name="Workspace">{value.workspaceId}</AssessmentFact>
    <AssessmentFact name="Finding">{value.findingId}</AssessmentFact>
    <AssessmentFact name="Observation">{value.observationId}</AssessmentFact>
    <AssessmentFact name="Source evidence digest">{value.sourceEvidenceDigest}</AssessmentFact>
    <AssessmentFact name="Requested by">{value.requestedBy}</AssessmentFact>
    <AssessmentFact name="Profile">{value.profileId}</AssessmentFact>
    <AssessmentFact name="Profile revision">{value.profileRevision}</AssessmentFact>
    <AssessmentFact name="Policy revision">{value.policyRevision}</AssessmentFact>
    <AssessmentFact name="Grant">{value.grantId || "None (local-only)"}</AssessmentFact>
    <AssessmentFact name="Destination">{value.destination}</AssessmentFact>
    <AssessmentFact name="Family">{value.family}</AssessmentFact>
    <AssessmentFact name="Model">{value.model}</AssessmentFact>
    <AssessmentFact name="Deployment">{value.deployment || "Not applicable"}</AssessmentFact>
    <AssessmentFact name="Task">{value.task}</AssessmentFact>
    <AssessmentFact name="Data class">{value.dataClass}</AssessmentFact>
    <AssessmentFact name="Prompt revision">{value.promptRevision}</AssessmentFact>
    <AssessmentFact name="Context origin">{value.contextOrigin}</AssessmentFact>
    <AssessmentFact name="Context reference">{value.contextRef}</AssessmentFact>
    <AssessmentFact name="Context digest">{value.contextDigest}</AssessmentFact>
  </dl>;
}

export function ApprovedContext({ context }: { context: string }) {
  return <div className="assessment-context"><h4>Approved context</h4>
    <pre className="evidence-text" aria-label="Approved context">{context}</pre>
    <p className="form-help">This is user-reviewed derived text, not original scanner bytes or cryptographic proof.
      {" "}The source evidence digest links the selected observation; it is not the digest of this edited context.</p>
  </div>;
}

export function AdvisoryNotice() {
  return <p className="form-help">Advisory only. A model statement is not source verification, reproduced proof, a false-positive decision,
    {" "}or permission to close a finding. No finding workflow, owner, risk, evidence, notes or source fields are changed.</p>;
}
