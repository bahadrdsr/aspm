package app

const schemaV20 = `
ALTER TABLE {{findings}}
 DROP CONSTRAINT app_findings_disposition_check,
 ADD CONSTRAINT app_findings_disposition_check
  CHECK(disposition IN ('none','accepted-risk','suppressed','false-positive'));

CREATE TABLE {{finding_disposition_approvals}} (
 id text PRIMARY KEY CHECK(id ~ '^[0-9a-f]{32}$'),
 workspace_id text NOT NULL,
 finding_id text NOT NULL,
 decision_revision bigint NOT NULL CHECK(decision_revision>1),
 actor_id text NOT NULL,
 disposition text NOT NULL CHECK(disposition IN ('accepted-risk','suppressed','false-positive')),
 scope_kind text NOT NULL CHECK(scope_kind IN ('finding','asset','source','scope')),
 scope_value text NOT NULL CHECK(octet_length(scope_value) BETWEEN 1 AND 4096),
 rationale text NOT NULL CHECK(octet_length(rationale) BETWEEN 1 AND 8192),
 expires_at timestamptz,
 created_at timestamptz NOT NULL,
 CONSTRAINT app_finding_disposition_approvals_workspace_id_key UNIQUE(workspace_id,id),
 CONSTRAINT app_finding_disposition_approvals_finding_revision_key
  UNIQUE(workspace_id,finding_id,decision_revision),
 CONSTRAINT app_finding_disposition_approvals_semantics_check CHECK(
  (disposition='accepted-risk' AND scope_kind='finding') OR
  (disposition='suppressed' AND expires_at IS NOT NULL) OR
  (disposition='false-positive' AND scope_kind='finding' AND expires_at IS NULL)),
 FOREIGN KEY(workspace_id,finding_id) REFERENCES {{findings}}(workspace_id,id) ON DELETE CASCADE,
 FOREIGN KEY(actor_id) REFERENCES {{users}}(id)
);
CREATE INDEX app_finding_disposition_approvals_finding_idx
 ON {{finding_disposition_approvals}}(workspace_id,finding_id,decision_revision);
`
