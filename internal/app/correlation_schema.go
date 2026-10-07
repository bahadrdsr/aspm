package app

const schemaV13 = `
ALTER TABLE {{findings}}
 ADD COLUMN decision_revision bigint NOT NULL DEFAULT 1 CHECK(decision_revision>0),
 ADD COLUMN evidence_revision bigint NOT NULL DEFAULT 1 CHECK(evidence_revision>0);

CREATE TABLE {{finding_correlations}} (
 id text PRIMARY KEY,
 workspace_id text NOT NULL REFERENCES {{workspaces}}(id),
 primary_finding_id text NOT NULL,
 state text NOT NULL DEFAULT 'active' CHECK(state IN ('active','split')),
 revision bigint NOT NULL DEFAULT 1 CHECK(revision>0),
 created_by text NOT NULL,
 created_at timestamptz NOT NULL,
 updated_at timestamptz NOT NULL,
 CONSTRAINT app_finding_correlations_workspace_id_key UNIQUE(workspace_id,id),
 FOREIGN KEY(workspace_id,primary_finding_id) REFERENCES {{findings}}(workspace_id,id),
 FOREIGN KEY(workspace_id,created_by) REFERENCES {{memberships}}(workspace_id,user_id)
);
CREATE TABLE {{finding_correlation_members}} (
 id text PRIMARY KEY,
 workspace_id text NOT NULL,
 correlation_id text NOT NULL,
 finding_id text NOT NULL,
 ordinal integer NOT NULL CHECK(ordinal BETWEEN 0 AND 63),
 original_decision jsonb NOT NULL CHECK(jsonb_typeof(original_decision)='object'),
 added_at timestamptz NOT NULL,
 added_by text NOT NULL,
 released_at timestamptz,
 released_by text,
 CONSTRAINT app_finding_correlation_members_group_finding_key UNIQUE(workspace_id,correlation_id,finding_id),
 CONSTRAINT app_finding_correlation_members_group_ordinal_key UNIQUE(workspace_id,correlation_id,ordinal),
 FOREIGN KEY(workspace_id,correlation_id) REFERENCES {{finding_correlations}}(workspace_id,id),
 FOREIGN KEY(workspace_id,finding_id) REFERENCES {{findings}}(workspace_id,id),
 FOREIGN KEY(workspace_id,added_by) REFERENCES {{memberships}}(workspace_id,user_id),
 FOREIGN KEY(workspace_id,released_by) REFERENCES {{memberships}}(workspace_id,user_id),
 CHECK((released_at IS NULL)=(released_by IS NULL))
);
CREATE UNIQUE INDEX app_finding_correlation_members_active_finding
 ON {{finding_correlation_members}}(workspace_id,finding_id) WHERE released_at IS NULL;
CREATE INDEX app_finding_correlation_members_group
 ON {{finding_correlation_members}}(workspace_id,correlation_id,ordinal);

CREATE TABLE {{finding_correlation_events}} (
 id text PRIMARY KEY,
 workspace_id text NOT NULL,
 correlation_id text NOT NULL,
 sequence bigint NOT NULL CHECK(sequence>0),
 event_type text NOT NULL CHECK(event_type IN ('merge','split')),
 actor_id text NOT NULL,
 rationale text NOT NULL CHECK(octet_length(rationale) BETWEEN 1 AND 8192),
 idempotency_key text NOT NULL,
 binding_digest bytea NOT NULL CHECK(octet_length(binding_digest)=32),
 before_state jsonb NOT NULL CHECK(jsonb_typeof(before_state)='object'),
 after_state jsonb NOT NULL CHECK(jsonb_typeof(after_state)='object'),
 created_at timestamptz NOT NULL,
 CONSTRAINT app_finding_correlation_events_workspace_id_key UNIQUE(workspace_id,id),
 CONSTRAINT app_finding_correlation_events_idempotency_key UNIQUE(workspace_id,idempotency_key),
 CONSTRAINT app_finding_correlation_events_group_sequence_key UNIQUE(workspace_id,correlation_id,sequence),
 FOREIGN KEY(workspace_id,correlation_id) REFERENCES {{finding_correlations}}(workspace_id,id),
 FOREIGN KEY(workspace_id,actor_id) REFERENCES {{memberships}}(workspace_id,user_id)
);
CREATE INDEX app_finding_correlation_events_group
 ON {{finding_correlation_events}}(workspace_id,correlation_id,sequence);
`
