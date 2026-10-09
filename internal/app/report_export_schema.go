package app

const schemaV26 = `
CREATE TABLE {{report_exports}} (
 id text NOT NULL,
 workspace_id text NOT NULL,
 snapshot_id text NOT NULL,
 requested_by text NOT NULL,
 format text NOT NULL,
 idempotency_key text NOT NULL,
 state text NOT NULL DEFAULT 'queued',
 created_at timestamptz NOT NULL,
 completed_at timestamptz,
 content bytea,
 content_digest text,
 content_size bigint,
 failure_code text,
 failure_message text,
 worker_id text,
 fence bigint NOT NULL DEFAULT 0,
 attempts integer NOT NULL DEFAULT 0,
 lease_until timestamptz,
 available_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CONSTRAINT app_report_exports_pkey PRIMARY KEY(id),
 CONSTRAINT app_report_exports_workspace_id_key UNIQUE(workspace_id,id),
 CONSTRAINT app_report_exports_workspace_idempotency_key UNIQUE(workspace_id,idempotency_key),
 CONSTRAINT app_report_exports_workspace_id_fkey
  FOREIGN KEY(workspace_id) REFERENCES {{workspaces}}(id) ON DELETE CASCADE,
 CONSTRAINT app_report_exports_workspace_snapshot_fkey
  FOREIGN KEY(workspace_id,snapshot_id) REFERENCES {{report_snapshots}}(workspace_id,id),
 CONSTRAINT app_report_exports_workspace_requester_fkey
  FOREIGN KEY(workspace_id,requested_by) REFERENCES {{memberships}}(workspace_id,user_id),
 CONSTRAINT app_report_exports_id_check CHECK(id ~ '^[0-9a-f]{32}$'),
 CONSTRAINT app_report_exports_workspace_id_check CHECK(workspace_id ~ '^[0-9a-f]{32}$'),
 CONSTRAINT app_report_exports_snapshot_id_check CHECK(snapshot_id ~ '^[0-9a-f]{32}$'),
 CONSTRAINT app_report_exports_requested_by_check CHECK(requested_by ~ '^[0-9a-f]{32}$'),
 CONSTRAINT app_report_exports_worker_id_check
  CHECK(worker_id IS NULL OR worker_id ~ '^[0-9a-f]{32}$'),
 CONSTRAINT app_report_exports_idempotency_key_check CHECK(
  octet_length(idempotency_key)>=1 AND octet_length(idempotency_key)<=256 AND
  btrim(idempotency_key)<>''),
 CONSTRAINT app_report_exports_format_check CHECK(format IN ('json','csv')),
 CONSTRAINT app_report_exports_state_check CHECK(state IN ('queued','processing','succeeded','failed')),
 CONSTRAINT app_report_exports_content_digest_check
  CHECK(content_digest IS NULL OR content_digest ~ '^sha256:[0-9a-f]{64}$'),
 CONSTRAINT app_report_exports_content_check
  CHECK(content IS NULL OR octet_length(content)<=262144),
 CONSTRAINT app_report_exports_content_size_check
  CHECK(content_size IS NULL OR content_size>=0 AND content_size<=262144),
 CONSTRAINT app_report_exports_failure_code_check CHECK(
  failure_code IS NULL OR octet_length(failure_code)>=1 AND
  octet_length(failure_code)<=128 AND btrim(failure_code)<>''),
 CONSTRAINT app_report_exports_failure_message_check CHECK(
  failure_message IS NULL OR octet_length(failure_message)>=1 AND
  octet_length(failure_message)<=1024 AND btrim(failure_message)<>''),
 CONSTRAINT app_report_exports_state_artifact_check CHECK(
  state='succeeded' AND content IS NOT NULL AND content_digest IS NOT NULL AND
  content_size IS NOT NULL AND content_size=octet_length(content) OR
  state<>'succeeded' AND content IS NULL AND content_digest IS NULL AND content_size IS NULL),
 CONSTRAINT app_report_exports_state_completion_check
  CHECK((state IN ('succeeded','failed'))=(completed_at IS NOT NULL)),
 CONSTRAINT app_report_exports_state_failure_check
  CHECK((state='failed')=(failure_code IS NOT NULL AND failure_message IS NOT NULL)),
 CONSTRAINT app_report_exports_state_lease_check CHECK(
  state='processing' AND worker_id IS NOT NULL AND fence>0 AND lease_until IS NOT NULL OR
  state<>'processing' AND worker_id IS NULL AND lease_until IS NULL),
 CONSTRAINT app_report_exports_timestamps_check
  CHECK(completed_at IS NULL OR completed_at>=created_at),
 CONSTRAINT app_report_exports_fence_check CHECK(fence>=0),
 CONSTRAINT app_report_exports_attempts_check CHECK(attempts>=0 AND attempts<=3)
);

CREATE INDEX app_report_exports_claim_idx ON {{report_exports}}(available_at,id)
 WHERE state IN ('queued','processing');
`
