package app

const schemaV19 = `
ALTER TABLE {{findings}}
 DROP CONSTRAINT app_findings_workflow_state_check,
 ADD CONSTRAINT app_findings_workflow_state_check
  CHECK(workflow_state IN ('open','in-progress','pending-retest','resolved'));

CREATE TABLE {{finding_decision_events}} (
 id text PRIMARY KEY CHECK(id ~ '^[0-9a-f]{32}$'),
 workspace_id text NOT NULL,
 finding_id text NOT NULL,
 decision_revision bigint NOT NULL CHECK(decision_revision>1),
 actor_id text NOT NULL,
 action text NOT NULL CHECK(action IN ('update','bulk-update')),
 rationale text NOT NULL CHECK(octet_length(rationale)<=8192),
 changed_fields jsonb NOT NULL CHECK(jsonb_typeof(changed_fields)='array'),
 before_state jsonb NOT NULL CHECK(jsonb_typeof(before_state)='object'),
 after_state jsonb NOT NULL CHECK(jsonb_typeof(after_state)='object'),
 created_at timestamptz NOT NULL,
 CONSTRAINT app_finding_decision_events_workspace_id_key UNIQUE(workspace_id,id),
 CONSTRAINT app_finding_decision_events_finding_revision_key
  UNIQUE(workspace_id,finding_id,decision_revision),
 FOREIGN KEY(workspace_id,finding_id) REFERENCES {{findings}}(workspace_id,id) ON DELETE CASCADE,
 FOREIGN KEY(actor_id) REFERENCES {{users}}(id)
);
CREATE INDEX app_finding_decision_events_finding_idx
 ON {{finding_decision_events}}(workspace_id,finding_id,decision_revision);
`
