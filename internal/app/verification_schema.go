package app

const schemaV27 = `
CREATE TABLE {{verification_evidence}} (
 id text NOT NULL,
 workspace_id text NOT NULL,
 finding_id text NOT NULL,
 submitted_by text NOT NULL,
 method text NOT NULL,
 fixture_schema text NOT NULL,
 environment_id text NOT NULL,
 scope_revision text NOT NULL,
 finding_evidence_revision bigint NOT NULL,
 content bytea NOT NULL,
 content_digest text NOT NULL,
 content_size bigint NOT NULL,
 created_at timestamptz NOT NULL,
 CONSTRAINT app_verification_evidence_pkey PRIMARY KEY(id),
 CONSTRAINT app_verification_evidence_workspace_id_key UNIQUE(workspace_id,id),
 CONSTRAINT app_verification_evidence_binding_key UNIQUE(
  workspace_id,finding_id,id,method,environment_id,scope_revision,
  finding_evidence_revision,content_digest),
 CONSTRAINT app_verification_evidence_workspace_id_fkey
  FOREIGN KEY(workspace_id) REFERENCES {{workspaces}}(id) ON DELETE CASCADE,
 CONSTRAINT app_verification_evidence_workspace_finding_fkey
  FOREIGN KEY(workspace_id,finding_id) REFERENCES {{findings}}(workspace_id,id) ON DELETE CASCADE,
 CONSTRAINT app_verification_evidence_workspace_submitter_fkey
  FOREIGN KEY(workspace_id,submitted_by) REFERENCES {{memberships}}(workspace_id,user_id),
 CONSTRAINT app_verification_evidence_id_check CHECK(id ~ '^[0-9a-f]{32}$'),
 CONSTRAINT app_verification_evidence_workspace_id_check CHECK(workspace_id ~ '^[0-9a-f]{32}$'),
 CONSTRAINT app_verification_evidence_finding_id_check CHECK(finding_id ~ '^[0-9a-f]{32}$'),
 CONSTRAINT app_verification_evidence_submitted_by_check CHECK(submitted_by ~ '^[0-9a-f]{32}$'),
 CONSTRAINT app_verification_evidence_method_check CHECK(method='deterministic-evidence'),
 CONSTRAINT app_verification_evidence_schema_check CHECK(fixture_schema='aspm.synthetic-fixture/v1'),
 CONSTRAINT app_verification_evidence_environment_id_check CHECK(
  octet_length(environment_id)>=1 AND octet_length(environment_id)<=256 AND btrim(environment_id)<>''),
 CONSTRAINT app_verification_evidence_scope_revision_check CHECK(
  octet_length(scope_revision)>=1 AND octet_length(scope_revision)<=256 AND btrim(scope_revision)<>''),
 CONSTRAINT app_verification_evidence_finding_evidence_revision_check CHECK(finding_evidence_revision>0),
 CONSTRAINT app_verification_evidence_content_check
  CHECK(octet_length(content)>=1 AND octet_length(content)<=65536),
 CONSTRAINT app_verification_evidence_content_digest_check
  CHECK(content_digest ~ '^sha256:[0-9a-f]{64}$'),
 CONSTRAINT app_verification_evidence_content_size_check
  CHECK(content_size>=1 AND content_size<=65536 AND content_size=octet_length(content))
);

CREATE INDEX app_verification_evidence_finding_idx
 ON {{verification_evidence}}(workspace_id,finding_id,id);

CREATE TABLE {{verification_approvals}} (
 id text NOT NULL,
 workspace_id text NOT NULL,
 finding_id text NOT NULL,
 evidence_id text NOT NULL,
 approved_by text NOT NULL,
 method text NOT NULL,
 environment_id text NOT NULL,
 scope_revision text NOT NULL,
 finding_evidence_revision bigint NOT NULL,
 evidence_digest text NOT NULL,
 rationale text NOT NULL,
 created_at timestamptz NOT NULL,
 expires_at timestamptz NOT NULL,
 revoked_at timestamptz,
 revoked_by text,
 revocation_rationale text,
 CONSTRAINT app_verification_approvals_pkey PRIMARY KEY(id),
 CONSTRAINT app_verification_approvals_workspace_id_key UNIQUE(workspace_id,id),
 CONSTRAINT app_verification_approvals_binding_key UNIQUE(
  workspace_id,finding_id,id,evidence_id,method,environment_id,scope_revision,
  finding_evidence_revision,evidence_digest),
 CONSTRAINT app_verification_approvals_workspace_id_fkey
  FOREIGN KEY(workspace_id) REFERENCES {{workspaces}}(id) ON DELETE CASCADE,
 CONSTRAINT app_verification_approvals_workspace_finding_fkey
  FOREIGN KEY(workspace_id,finding_id) REFERENCES {{findings}}(workspace_id,id) ON DELETE CASCADE,
 CONSTRAINT app_verification_approvals_workspace_approver_fkey
  FOREIGN KEY(workspace_id,approved_by) REFERENCES {{memberships}}(workspace_id,user_id),
 CONSTRAINT app_verification_approvals_workspace_revoker_fkey
  FOREIGN KEY(workspace_id,revoked_by) REFERENCES {{memberships}}(workspace_id,user_id),
 CONSTRAINT app_verification_approvals_workspace_evidence_fkey
  FOREIGN KEY(workspace_id,finding_id,evidence_id,method,environment_id,scope_revision,
   finding_evidence_revision,evidence_digest)
  REFERENCES {{verification_evidence}}(workspace_id,finding_id,id,method,environment_id,
   scope_revision,finding_evidence_revision,content_digest),
 CONSTRAINT app_verification_approvals_id_check CHECK(id ~ '^[0-9a-f]{32}$'),
 CONSTRAINT app_verification_approvals_workspace_id_check CHECK(workspace_id ~ '^[0-9a-f]{32}$'),
 CONSTRAINT app_verification_approvals_finding_id_check CHECK(finding_id ~ '^[0-9a-f]{32}$'),
 CONSTRAINT app_verification_approvals_evidence_id_check CHECK(evidence_id ~ '^[0-9a-f]{32}$'),
 CONSTRAINT app_verification_approvals_approved_by_check CHECK(approved_by ~ '^[0-9a-f]{32}$'),
 CONSTRAINT app_verification_approvals_revoked_by_check
  CHECK(revoked_by IS NULL OR revoked_by ~ '^[0-9a-f]{32}$'),
 CONSTRAINT app_verification_approvals_method_check CHECK(method='deterministic-evidence'),
 CONSTRAINT app_verification_approvals_environment_id_check CHECK(
  octet_length(environment_id)>=1 AND octet_length(environment_id)<=256 AND btrim(environment_id)<>''),
 CONSTRAINT app_verification_approvals_scope_revision_check CHECK(
  octet_length(scope_revision)>=1 AND octet_length(scope_revision)<=256 AND btrim(scope_revision)<>''),
 CONSTRAINT app_verification_approvals_finding_evidence_revision_check CHECK(finding_evidence_revision>0),
 CONSTRAINT app_verification_approvals_evidence_digest_check
  CHECK(evidence_digest ~ '^sha256:[0-9a-f]{64}$'),
 CONSTRAINT app_verification_approvals_rationale_check CHECK(
  octet_length(rationale)>=1 AND octet_length(rationale)<=8192 AND btrim(rationale)<>''),
 CONSTRAINT app_verification_approvals_expires_at_check
  CHECK(expires_at>created_at AND expires_at<=created_at+interval '24 hours'),
 CONSTRAINT app_verification_approvals_revocation_rationale_check CHECK(
  revocation_rationale IS NULL OR octet_length(revocation_rationale)>=1
  AND octet_length(revocation_rationale)<=8192 AND btrim(revocation_rationale)<>''),
 CONSTRAINT app_verification_approvals_revocation_check CHECK(
  revoked_at IS NULL AND revoked_by IS NULL AND revocation_rationale IS NULL
  OR revoked_at IS NOT NULL AND revoked_by IS NOT NULL AND revocation_rationale IS NOT NULL
  AND revoked_at>=created_at)
);

CREATE INDEX app_verification_approvals_current_idx
 ON {{verification_approvals}}(workspace_id,finding_id,expires_at,id)
 WHERE revoked_at IS NULL;
CREATE INDEX app_verification_approvals_evidence_idx
 ON {{verification_approvals}}(workspace_id,evidence_id,id);

CREATE TABLE {{verification_jobs}} (
 id text NOT NULL,
 workspace_id text NOT NULL,
 finding_id text NOT NULL,
 approval_id text NOT NULL,
 evidence_id text NOT NULL,
 requested_by text NOT NULL,
 method text NOT NULL,
 environment_id text NOT NULL,
 scope_revision text NOT NULL,
 finding_evidence_revision bigint NOT NULL,
 evidence_digest text NOT NULL,
 idempotency_key text NOT NULL,
 state text NOT NULL DEFAULT 'queued',
 outcome text,
 close_finding boolean NOT NULL DEFAULT false,
 false_positive boolean NOT NULL DEFAULT false,
 failure_code text,
 failure_message text,
 created_at timestamptz NOT NULL,
 completed_at timestamptz,
 worker_id text,
 fence bigint NOT NULL DEFAULT 0,
 attempts integer NOT NULL DEFAULT 0,
 lease_until timestamptz,
 available_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 CONSTRAINT app_verification_jobs_pkey PRIMARY KEY(id),
 CONSTRAINT app_verification_jobs_workspace_id_key UNIQUE(workspace_id,id),
 CONSTRAINT app_verification_jobs_workspace_idempotency_key UNIQUE(workspace_id,idempotency_key),
 CONSTRAINT app_verification_jobs_workspace_id_fkey
  FOREIGN KEY(workspace_id) REFERENCES {{workspaces}}(id) ON DELETE CASCADE,
 CONSTRAINT app_verification_jobs_workspace_finding_fkey
  FOREIGN KEY(workspace_id,finding_id) REFERENCES {{findings}}(workspace_id,id) ON DELETE CASCADE,
 CONSTRAINT app_verification_jobs_workspace_requester_fkey
  FOREIGN KEY(workspace_id,requested_by) REFERENCES {{memberships}}(workspace_id,user_id),
 CONSTRAINT app_verification_jobs_workspace_approval_fkey
  FOREIGN KEY(workspace_id,finding_id,approval_id,evidence_id,method,environment_id,
   scope_revision,finding_evidence_revision,evidence_digest)
  REFERENCES {{verification_approvals}}(workspace_id,finding_id,id,evidence_id,method,
   environment_id,scope_revision,finding_evidence_revision,evidence_digest),
 CONSTRAINT app_verification_jobs_id_check CHECK(id ~ '^[0-9a-f]{32}$'),
 CONSTRAINT app_verification_jobs_workspace_id_check CHECK(workspace_id ~ '^[0-9a-f]{32}$'),
 CONSTRAINT app_verification_jobs_finding_id_check CHECK(finding_id ~ '^[0-9a-f]{32}$'),
 CONSTRAINT app_verification_jobs_approval_id_check CHECK(approval_id ~ '^[0-9a-f]{32}$'),
 CONSTRAINT app_verification_jobs_evidence_id_check CHECK(evidence_id ~ '^[0-9a-f]{32}$'),
 CONSTRAINT app_verification_jobs_requested_by_check CHECK(requested_by ~ '^[0-9a-f]{32}$'),
 CONSTRAINT app_verification_jobs_worker_id_check
  CHECK(worker_id IS NULL OR worker_id ~ '^[0-9a-f]{32}$'),
 CONSTRAINT app_verification_jobs_method_check CHECK(method='deterministic-evidence'),
 CONSTRAINT app_verification_jobs_environment_id_check CHECK(
  octet_length(environment_id)>=1 AND octet_length(environment_id)<=256 AND btrim(environment_id)<>''),
 CONSTRAINT app_verification_jobs_scope_revision_check CHECK(
  octet_length(scope_revision)>=1 AND octet_length(scope_revision)<=256 AND btrim(scope_revision)<>''),
 CONSTRAINT app_verification_jobs_finding_evidence_revision_check CHECK(finding_evidence_revision>0),
 CONSTRAINT app_verification_jobs_evidence_digest_check
  CHECK(evidence_digest ~ '^sha256:[0-9a-f]{64}$'),
 CONSTRAINT app_verification_jobs_idempotency_key_check CHECK(
  octet_length(idempotency_key)>=1 AND octet_length(idempotency_key)<=256 AND btrim(idempotency_key)<>''),
 CONSTRAINT app_verification_jobs_state_check
  CHECK(state IN ('queued','processing','succeeded','blocked','failed','cancelled')),
 CONSTRAINT app_verification_jobs_verification_outcome_check
  CHECK(outcome IS NULL OR outcome IN ('reproduced','not-reproduced')),
 CONSTRAINT app_verification_jobs_state_result_check CHECK((state='succeeded')=(outcome IS NOT NULL)),
 CONSTRAINT app_verification_jobs_state_completion_check CHECK(
  (state IN ('succeeded','blocked','failed','cancelled'))=(completed_at IS NOT NULL)),
 CONSTRAINT app_verification_jobs_state_failure_check CHECK(
  (state IN ('blocked','failed','cancelled'))=(failure_code IS NOT NULL AND failure_message IS NOT NULL)),
 CONSTRAINT app_verification_jobs_state_lease_check CHECK(
  state='processing' AND worker_id IS NOT NULL AND fence>0 AND attempts>0 AND lease_until IS NOT NULL
  OR state<>'processing' AND worker_id IS NULL AND lease_until IS NULL),
 CONSTRAINT app_verification_jobs_state_safety_check CHECK(NOT close_finding AND NOT false_positive),
 CONSTRAINT app_verification_jobs_failure_code_check CHECK(
  failure_code IS NULL OR octet_length(failure_code)>=1 AND octet_length(failure_code)<=128
  AND btrim(failure_code)<>''),
 CONSTRAINT app_verification_jobs_failure_message_check CHECK(
  failure_message IS NULL OR octet_length(failure_message)>=1 AND octet_length(failure_message)<=1024
  AND btrim(failure_message)<>''),
 CONSTRAINT app_verification_jobs_timestamps_check CHECK(completed_at IS NULL OR completed_at>=created_at),
 CONSTRAINT app_verification_jobs_fence_check CHECK(fence>=0),
 CONSTRAINT app_verification_jobs_attempts_check CHECK(attempts>=0 AND attempts<=3)
);

CREATE INDEX app_verification_jobs_claim_idx
 ON {{verification_jobs}}(available_at,id) WHERE state='queued';
CREATE INDEX app_verification_jobs_reclaim_idx
 ON {{verification_jobs}}(lease_until,id) WHERE state='processing';
CREATE INDEX app_verification_jobs_finding_idx
 ON {{verification_jobs}}(workspace_id,finding_id,id);
CREATE INDEX app_verification_jobs_approval_idx
 ON {{verification_jobs}}(workspace_id,approval_id,id);
`
