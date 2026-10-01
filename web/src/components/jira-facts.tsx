import type { JiraPayload, JiraTarget } from "@/api/jira-types";

export function JiraTargetFacts({ target, revision }: { target: JiraTarget; revision: number }) {
  return <dl className="slack-facts">
    <div><dt>Project</dt><dd>{target.project}</dd></div><div><dt>Issue type</dt><dd>{target.issueType}</dd></div>
    <div className="full-width"><dt>Site origin</dt><dd>{target.siteOrigin}</dd></div>
    <div className="full-width"><dt>Cloud ID</dt><dd><code>{target.cloudId}</code></dd></div>
    <div className="full-width"><dt>API base</dt><dd>{target.apiBase}</dd></div>
    <div><dt>Connection revision</dt><dd>{revision}</dd></div><div><dt>Permission metadata</dt><dd>Not verified</dd></div>
  </dl>;
}

export function JiraPayloadView({ payload }: { payload: JiraPayload }) {
  return <div className="slack-payload">
    <h4>{payload.title}</h4><p>{payload.body}</p>
    <a className="slack-link" href={payload.deepLink} target="_blank" rel="noopener noreferrer">View finding</a>
    {Object.keys(payload.fields).length > 0 && <dl className="slack-facts">{Object.entries(payload.fields).map(([id, value]) =>
      <div className="full-width" key={id}><dt><code>{id}</code></dt><dd>{value}</dd></div>)}</dl>}
    <p className="form-help">Canonical server payload and resolved mapping strings. No original evidence, report, source code, notes or remediation are exported.</p>
  </div>;
}
