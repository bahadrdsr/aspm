import type { TeamsDestination, TeamsPayload } from "@/api/teams-types";

export function TeamsTargetFacts({ target, revision }: { target: TeamsDestination; revision: number }) {
  return <dl className="slack-facts">
    <div className="full-width"><dt>Destination</dt><dd>{target.name}</dd></div>
    <div className="full-width"><dt>Workflow origin</dt><dd>{target.workflowOrigin}</dd></div>
    <div><dt>Channel type</dt><dd>standard</dd></div><div><dt>Connection revision</dt><dd>{revision}</dd></div>
    <div className="full-width"><dt>Operator declaration</dt><dd>Workflow ownership acknowledged; not verified</dd></div>
  </dl>;
}
export function TeamsPayloadView({ payload }: { payload: TeamsPayload }) {
  return <div className="slack-payload"><h4>{payload.title}</h4><p>{payload.body}</p>
    <a className="slack-link" href={payload.deepLink} target="_blank" rel="noopener noreferrer">View finding</a>
    <p className="form-help">Canonical title, severity, asset and finding link only. No original evidence, code, reports,
      notes or remediation are exported.</p></div>;
}
