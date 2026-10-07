package app

const schemaV15 = `
ALTER TABLE {{imports}}
 ADD COLUMN evidence_availability text NOT NULL DEFAULT 'available'
  CHECK(evidence_availability IN ('available','archived','expired','missing','corrupt')),
 ADD COLUMN evidence_revision bigint NOT NULL DEFAULT 1 CHECK(evidence_revision>0),
 ADD COLUMN retention_transition text CHECK(retention_transition IN ('expiring')),
 ADD COLUMN evidence_expired_at timestamptz,
 ADD CONSTRAINT app_imports_evidence_expiry_check
  CHECK((evidence_availability='expired')=(evidence_expired_at IS NOT NULL));

ALTER TABLE {{observations}} ALTER COLUMN data DROP NOT NULL;
ALTER TABLE {{observations}}
 ADD COLUMN evidence_availability text NOT NULL DEFAULT 'available'
  CHECK(evidence_availability IN ('available','archived','expired','missing','corrupt')),
 ADD COLUMN evidence_revision bigint NOT NULL DEFAULT 1 CHECK(evidence_revision>0),
 ADD COLUMN summary jsonb,
 ADD COLUMN archive_key text,
 ADD COLUMN archive_digest text CHECK(archive_digest IS NULL OR archive_digest ~ '^sha256:[0-9a-f]{64}$'),
 ADD COLUMN archive_size bigint CHECK(archive_size IS NULL OR archive_size>=0),
 ADD COLUMN archived_at timestamptz,
 ADD COLUMN evidence_expired_at timestamptz,
 ADD COLUMN retention_transition text CHECK(retention_transition IN ('expiring')),
 ADD CONSTRAINT app_observations_evidence_content_check CHECK(
  (evidence_availability='available' AND data IS NOT NULL AND summary IS NULL)
  OR (evidence_availability<>'available' AND data IS NULL AND summary IS NOT NULL)),
 ADD CONSTRAINT app_observations_archive_reference_check CHECK(
  (archive_key IS NULL AND archive_digest IS NULL AND archive_size IS NULL AND archived_at IS NULL)
  OR (archive_key IS NOT NULL AND archive_digest IS NOT NULL AND archive_size IS NOT NULL AND archived_at IS NOT NULL)),
 ADD CONSTRAINT app_observations_evidence_expiry_check
  CHECK((evidence_availability='expired')=(evidence_expired_at IS NOT NULL));
CREATE INDEX app_observations_retention_state_idx
 ON {{observations}}(workspace_id,evidence_availability,id);

ALTER TABLE {{finding_correlation_events}}
 ADD COLUMN detail_availability text NOT NULL DEFAULT 'available'
  CHECK(detail_availability IN ('available','archived','expired','missing','corrupt')),
 ADD COLUMN detail_revision bigint NOT NULL DEFAULT 1 CHECK(detail_revision>0),
 ADD COLUMN archive_key text,
 ADD COLUMN archive_digest text CHECK(archive_digest IS NULL OR archive_digest ~ '^sha256:[0-9a-f]{64}$'),
 ADD COLUMN archive_size bigint CHECK(archive_size IS NULL OR archive_size>=0),
 ADD COLUMN archived_at timestamptz,
 ADD COLUMN detail_expired_at timestamptz,
 ADD COLUMN retention_transition text CHECK(retention_transition IN ('expiring')),
 ADD CONSTRAINT app_finding_correlation_events_archive_reference_check CHECK(
  (archive_key IS NULL AND archive_digest IS NULL AND archive_size IS NULL AND archived_at IS NULL)
  OR (archive_key IS NOT NULL AND archive_digest IS NOT NULL AND archive_size IS NOT NULL AND archived_at IS NOT NULL)),
 ADD CONSTRAINT app_finding_correlation_events_detail_expiry_check
  CHECK((detail_availability='expired')=(detail_expired_at IS NOT NULL));
CREATE INDEX app_finding_correlation_events_retention_state_idx
 ON {{finding_correlation_events}}(workspace_id,detail_availability,id);

CREATE TABLE {{retention_runs}} (
 id text PRIMARY KEY CHECK(id ~ '^[0-9a-f]{32}$'),
 workspace_id text NOT NULL REFERENCES {{workspaces}}(id),
 operation text NOT NULL CHECK(operation IN ('apply-preview','restore-observation')),
 preview_id text,
 target_kind text,
 target_id text,
 state text NOT NULL DEFAULT 'queued' CHECK(state IN ('queued','processing','succeeded','partial','failed')),
 requested_by text NOT NULL,
 rationale text NOT NULL CHECK(octet_length(rationale) BETWEEN 1 AND 8192),
 idempotency_key text NOT NULL CHECK(octet_length(idempotency_key) BETWEEN 1 AND 256),
 binding_digest text NOT NULL CHECK(binding_digest ~ '^sha256:[0-9a-f]{64}$'),
 created_at timestamptz NOT NULL,
 completed_at timestamptz,
 available_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 worker_id text,
 fence bigint NOT NULL DEFAULT 0 CHECK(fence>=0),
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts>=0),
 lease_until timestamptz,
 failure_code text,
 failure_message text,
 CONSTRAINT app_retention_runs_workspace_id_key UNIQUE(workspace_id,id),
 CONSTRAINT app_retention_runs_idempotency_key UNIQUE(workspace_id,idempotency_key),
 FOREIGN KEY(workspace_id,requested_by) REFERENCES {{memberships}}(workspace_id,user_id),
 FOREIGN KEY(workspace_id,preview_id) REFERENCES {{retention_previews}}(workspace_id,id),
 CHECK((operation='apply-preview' AND preview_id IS NOT NULL AND target_kind IS NULL AND target_id IS NULL)
  OR (operation='restore-observation' AND preview_id IS NULL AND target_kind='observation'
   AND target_id ~ '^[0-9a-f]{32}$')),
 CHECK((state='processing' AND worker_id IS NOT NULL AND lease_until IS NOT NULL)
  OR (state<>'processing' AND worker_id IS NULL AND lease_until IS NULL)),
 CHECK(state NOT IN ('succeeded','partial','failed') OR completed_at IS NOT NULL)
);
CREATE INDEX app_retention_runs_claim_idx ON {{retention_runs}}(available_at,id)
 WHERE state IN ('queued','processing');
CREATE INDEX app_retention_runs_workspace_created_idx
 ON {{retention_runs}}(workspace_id,created_at,id);

CREATE TABLE {{retention_run_items}} (
 id text PRIMARY KEY CHECK(id ~ '^[0-9a-f]{32}$'),
 workspace_id text NOT NULL,
 run_id text NOT NULL,
 ordinal integer NOT NULL CHECK(ordinal BETWEEN 0 AND 199),
 class text NOT NULL CHECK(class IN ('hot-history','archived-evidence','raw-report','audit')),
 resource_kind text NOT NULL CHECK(resource_kind IN ('import','observation','correlation-event')),
 resource_id text NOT NULL CHECK(resource_id ~ '^[0-9a-f]{32}$'),
 action text NOT NULL CHECK(action IN ('archive-history','expire-archive','expire-raw-report','archive-audit','restore-archive')),
 state text NOT NULL DEFAULT 'queued'
  CHECK(state IN ('queued','processing','succeeded','protected','missing','corrupt','failed')),
 protected_reasons jsonb NOT NULL DEFAULT '[]' CHECK(jsonb_typeof(protected_reasons)='array'),
 outcome text NOT NULL DEFAULT '',
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts>=0),
 failure_code text,
 failure_message text,
 started_at timestamptz,
 completed_at timestamptz,
 CONSTRAINT app_retention_run_items_workspace_id_key UNIQUE(workspace_id,id),
 CONSTRAINT app_retention_run_items_run_ordinal_key UNIQUE(workspace_id,run_id,ordinal),
 CONSTRAINT app_retention_run_items_resource_key UNIQUE(workspace_id,run_id,resource_kind,resource_id,action),
 FOREIGN KEY(workspace_id,run_id) REFERENCES {{retention_runs}}(workspace_id,id) ON DELETE CASCADE,
 CHECK(state IN ('queued','processing') OR completed_at IS NOT NULL)
);
CREATE INDEX app_retention_run_items_run_state_idx
 ON {{retention_run_items}}(workspace_id,run_id,state,ordinal);
`
