package app

const schemaV14 = `
CREATE TABLE {{retention_policies}} (
 workspace_id text PRIMARY KEY REFERENCES {{workspaces}}(id),
 revision bigint NOT NULL DEFAULT 1 CHECK(revision>0),
 hot_history_days integer NOT NULL DEFAULT 90 CHECK(hot_history_days BETWEEN 1 AND 3650),
 raw_report_days integer NOT NULL DEFAULT 180 CHECK(raw_report_days BETWEEN 1 AND 3650),
 archived_evidence_days integer NOT NULL DEFAULT 365 CHECK(archived_evidence_days BETWEEN 1 AND 3650),
 audit_days integer NOT NULL DEFAULT 730 CHECK(audit_days BETWEEN 1 AND 3650),
 updated_by text,
 updated_at timestamptz,
 FOREIGN KEY(workspace_id,updated_by) REFERENCES {{memberships}}(workspace_id,user_id),
 CHECK(hot_history_days<raw_report_days AND raw_report_days<archived_evidence_days
  AND archived_evidence_days<audit_days),
 CHECK((updated_by IS NULL)=(updated_at IS NULL))
);
INSERT INTO {{retention_policies}}(workspace_id)
 SELECT id FROM {{workspaces}} ON CONFLICT(workspace_id) DO NOTHING;

CREATE INDEX app_imports_retention_idx
 ON {{imports}}(workspace_id,imported_at,id) WHERE state IN ('succeeded','failed');
CREATE INDEX app_observations_retention_run_idx
 ON {{observations}}(workspace_id,run_id,id);
CREATE INDEX app_assessment_previews_retention_observation_idx
 ON {{assessment_previews}}(workspace_id,observation_id);
CREATE INDEX app_finding_correlation_events_retention_idx
 ON {{finding_correlation_events}}(workspace_id,created_at,id);

CREATE TABLE {{retention_holds}} (
 id text PRIMARY KEY CHECK(id ~ '^[0-9a-f]{32}$'),
 workspace_id text NOT NULL REFERENCES {{workspaces}}(id),
 resource_kind text NOT NULL CHECK(resource_kind IN ('import','observation','correlation-event')),
 resource_id text NOT NULL CHECK(resource_id ~ '^[0-9a-f]{32}$'),
 reason text NOT NULL CHECK(octet_length(reason) BETWEEN 1 AND 8192),
 revision bigint NOT NULL DEFAULT 1 CHECK(revision>0),
 created_by text NOT NULL,
 created_at timestamptz NOT NULL,
 released_by text,
 released_at timestamptz,
 release_rationale text CHECK(release_rationale IS NULL OR octet_length(release_rationale) BETWEEN 1 AND 8192),
 CONSTRAINT app_retention_holds_workspace_id_key UNIQUE(workspace_id,id),
 FOREIGN KEY(workspace_id,created_by) REFERENCES {{memberships}}(workspace_id,user_id),
 FOREIGN KEY(workspace_id,released_by) REFERENCES {{memberships}}(workspace_id,user_id),
 CHECK((released_by IS NULL)=(released_at IS NULL)),
 CHECK((released_at IS NULL)=(release_rationale IS NULL))
);
CREATE UNIQUE INDEX app_retention_holds_active_resource
 ON {{retention_holds}}(workspace_id,resource_kind,resource_id) WHERE released_at IS NULL;
CREATE INDEX app_retention_holds_workspace_created
 ON {{retention_holds}}(workspace_id,created_at,id);

CREATE TABLE {{retention_previews}} (
 id text PRIMARY KEY CHECK(id ~ '^[0-9a-f]{32}$'),
 workspace_id text NOT NULL REFERENCES {{workspaces}}(id),
 revision bigint NOT NULL DEFAULT 1 CHECK(revision>0),
 state text NOT NULL DEFAULT 'ready' CHECK(state IN ('ready','approved','stale')),
 policy_revision bigint NOT NULL CHECK(policy_revision>0),
 snapshot_digest text NOT NULL CHECK(snapshot_digest ~ '^sha256:[0-9a-f]{64}$'),
 summary jsonb NOT NULL CHECK(jsonb_typeof(summary)='array'),
 created_by text NOT NULL,
 created_at timestamptz NOT NULL,
 expires_at timestamptz NOT NULL CHECK(expires_at>created_at),
 approved_by text,
 approved_at timestamptz,
 approval_rationale text CHECK(approval_rationale IS NULL OR octet_length(approval_rationale) BETWEEN 1 AND 8192),
 approval_idempotency_key text CHECK(approval_idempotency_key IS NULL OR octet_length(approval_idempotency_key) BETWEEN 1 AND 256),
 approval_binding_digest text CHECK(approval_binding_digest IS NULL OR approval_binding_digest ~ '^sha256:[0-9a-f]{64}$'),
 CONSTRAINT app_retention_previews_workspace_id_key UNIQUE(workspace_id,id),
 CONSTRAINT app_retention_previews_approval_key UNIQUE(workspace_id,approval_idempotency_key),
 FOREIGN KEY(workspace_id,created_by) REFERENCES {{memberships}}(workspace_id,user_id),
 FOREIGN KEY(workspace_id,approved_by) REFERENCES {{memberships}}(workspace_id,user_id),
 CHECK((state='approved')=(approved_by IS NOT NULL)),
 CHECK((approved_by IS NULL)=(approved_at IS NULL)),
 CHECK((approved_at IS NULL)=(approval_rationale IS NULL)),
 CHECK((approval_rationale IS NULL)=(approval_idempotency_key IS NULL)),
 CHECK((approval_idempotency_key IS NULL)=(approval_binding_digest IS NULL))
);
CREATE INDEX app_retention_previews_workspace_created
 ON {{retention_previews}}(workspace_id,created_at,id);

CREATE TABLE {{retention_preview_items}} (
 preview_id text NOT NULL,
 workspace_id text NOT NULL,
 ordinal integer NOT NULL CHECK(ordinal BETWEEN 0 AND 199),
 class text NOT NULL CHECK(class IN ('hot-history','archived-evidence','raw-report','audit')),
 resource_kind text NOT NULL CHECK(resource_kind IN ('import','observation','correlation-event')),
 resource_id text NOT NULL CHECK(resource_id ~ '^[0-9a-f]{32}$'),
 action text NOT NULL CHECK(action IN ('archive-history','expire-archive','expire-raw-report','archive-audit')),
 observed_at timestamptz NOT NULL,
 size_bytes bigint NOT NULL CHECK(size_bytes>=0),
 protected_reasons jsonb NOT NULL CHECK(jsonb_typeof(protected_reasons)='array'),
 CONSTRAINT app_retention_preview_items_pkey PRIMARY KEY(workspace_id,preview_id,ordinal),
 CONSTRAINT app_retention_preview_items_resource_key UNIQUE(workspace_id,preview_id,class,resource_kind,resource_id),
 FOREIGN KEY(workspace_id,preview_id) REFERENCES {{retention_previews}}(workspace_id,id) ON DELETE CASCADE
);
CREATE INDEX app_retention_preview_items_preview
 ON {{retention_preview_items}}(workspace_id,preview_id,ordinal);
`
