package app

const schemaV8 = `
CREATE TABLE {{assessment_quotas}} (
 singleton boolean PRIMARY KEY DEFAULT true CHECK(singleton),
 scope text NOT NULL UNIQUE CHECK(octet_length(scope) BETWEEN 1 AND 128 AND btrim(scope)<>''),
 max_concurrent integer CHECK(max_concurrent BETWEEN 1 AND 16),
 requests_per_window integer CHECK(requests_per_window BETWEEN 1 AND 1000),
 request_window_us bigint CHECK(request_window_us BETWEEN 1000000 AND 3600000000),
 worker_limits jsonb CHECK(jsonb_typeof(worker_limits)='object'),
 CHECK((max_concurrent IS NULL AND requests_per_window IS NULL AND request_window_us IS NULL AND worker_limits IS NULL)
    OR (max_concurrent IS NOT NULL AND requests_per_window IS NOT NULL AND request_window_us IS NOT NULL AND worker_limits IS NOT NULL))
);
CREATE TABLE {{assessment_previews}} (
 id text PRIMARY KEY CHECK(id ~ '^[0-9a-f]{32}$'),
 workspace_id text NOT NULL REFERENCES {{workspaces}}(id),
 finding_id text NOT NULL CHECK(finding_id ~ '^[0-9a-f]{32}$'),
 observation_id text NOT NULL CHECK(observation_id ~ '^[0-9a-f]{32}$'),
 source_evidence_digest text NOT NULL CHECK(source_evidence_digest ~ '^sha256:[0-9a-f]{64}$'),
 requested_by text NOT NULL REFERENCES {{users}}(id),
 profile_id text NOT NULL,
 profile_revision text NOT NULL CHECK(profile_revision ~ '^[1-9][0-9]{0,18}$'),
 policy_revision text NOT NULL CHECK(policy_revision ~ '^[1-9][0-9]{0,18}$'),
 grant_id text NOT NULL CHECK(grant_id='' OR grant_id ~ '^[0-9a-f]{32}$'),
 destination text NOT NULL CHECK(octet_length(destination) BETWEEN 1 AND 16384),
 family text NOT NULL CHECK(family IN ('openai','azure-foundry','anthropic','local')),
 model text NOT NULL CHECK(octet_length(model) BETWEEN 1 AND 256),
 deployment text NOT NULL CHECK(octet_length(deployment)<=256),
 task text NOT NULL CHECK(task='finding-validity'),
 data_class text NOT NULL CHECK(data_class='finding-evidence'),
 prompt_revision text NOT NULL CHECK(prompt_revision='finding-validity-reviewed-context/v1'),
 context_ref text NOT NULL CHECK(context_ref='reviewed-context:'||id),
 context_digest text NOT NULL CHECK(context_digest ~ '^sha256:[0-9a-f]{64}$'),
 context_origin text NOT NULL CHECK(context_origin='user-reviewed-derived'),
 context text NOT NULL CHECK(octet_length(context) BETWEEN 1 AND 32768 AND btrim(context)<>''),
 created_at timestamptz NOT NULL, expires_at timestamptz NOT NULL,
 UNIQUE(workspace_id,finding_id,requested_by,id),
 FOREIGN KEY(workspace_id,profile_id) REFERENCES {{ai_profiles}}(workspace_id,id),
 CHECK(expires_at>created_at AND expires_at<=created_at+interval '5 minutes')
);
CREATE INDEX app_assessment_previews_finding_idx
 ON {{assessment_previews}}(workspace_id,finding_id,id);
CREATE TABLE {{assessment_jobs}} (
 id text PRIMARY KEY CHECK(id ~ '^[0-9a-f]{32}$'),
 workspace_id text NOT NULL REFERENCES {{workspaces}}(id),
 finding_id text NOT NULL,
 requested_by text NOT NULL,
 preview_id text NOT NULL,
 idempotency_key text NOT NULL CHECK(octet_length(idempotency_key) BETWEEN 1 AND 128 AND btrim(idempotency_key)<>''),
 scope text NOT NULL REFERENCES {{assessment_quotas}}(scope),
 consent_expires_at timestamptz NOT NULL,
 state text NOT NULL DEFAULT 'queued'
   CHECK(state IN ('queued','dispatching','succeeded','failed','cancelled','invalidated','uncertain')),
 attempts integer NOT NULL DEFAULT 0 CHECK(attempts BETWEEN 0 AND 1),
 dispatch_state text NOT NULL DEFAULT 'not-started'
   CHECK(dispatch_state IN ('not-started','possibly-sent','response-received')),
 created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 dispatch_started_at timestamptz, completed_at timestamptz,
 worker_id text CHECK(octet_length(worker_id) BETWEEN 1 AND 128),
 fence bigint NOT NULL DEFAULT 0 CHECK(fence>=0), lease_until timestamptz,
 io_owner_id text CHECK(octet_length(io_owner_id) BETWEEN 1 AND 128),
 io_token text CHECK(io_token ~ '^[0-9a-f]{32}$'),
 io_deadline timestamptz, io_released_at timestamptz,
 result jsonb CHECK(jsonb_typeof(result)='object' AND octet_length(result::text)<=65536),
 failure jsonb CHECK(jsonb_typeof(failure)='object' AND octet_length(failure::text)<=8192),
 usage jsonb NOT NULL DEFAULT '{"known":false,"inputTokens":0,"outputTokens":0,"cachedInputTokens":0,"cacheWriteTokens":0}'
   CHECK(jsonb_typeof(usage)='object' AND octet_length(usage::text)<=1024),
 request_id text NOT NULL DEFAULT '' CHECK(octet_length(request_id)<=512),
 returned_model text NOT NULL DEFAULT '' CHECK(octet_length(returned_model)<=512),
 stop_reason text NOT NULL DEFAULT '' CHECK(octet_length(stop_reason)<=512),
 retry_after_millis bigint NOT NULL DEFAULT 0 CHECK(retry_after_millis BETWEEN 0 AND 86400000),
 UNIQUE(workspace_id,idempotency_key), UNIQUE(workspace_id,id),
 FOREIGN KEY(workspace_id,finding_id,requested_by,preview_id)
   REFERENCES {{assessment_previews}}(workspace_id,finding_id,requested_by,id),
 CHECK((attempts=0)=(dispatch_started_at IS NULL)),
 CHECK((attempts=0 AND io_owner_id IS NULL AND io_token IS NULL AND io_deadline IS NULL AND io_released_at IS NULL)
    OR (attempts=1 AND io_owner_id IS NOT NULL AND io_token IS NOT NULL AND io_deadline IS NOT NULL)),
 CHECK(io_deadline IS NULL OR (io_deadline>dispatch_started_at AND io_deadline<=dispatch_started_at+interval '30 seconds')),
 CHECK(io_released_at IS NULL OR io_released_at>=dispatch_started_at),
 CHECK((attempts=0)=(dispatch_state='not-started')),
 CHECK((state='succeeded')=(result IS NOT NULL)),
 CHECK(state<>'succeeded' OR (attempts=1 AND dispatch_state='response-received' AND failure IS NULL)),
 CHECK((state IN ('queued','dispatching'))=(completed_at IS NULL)),
 CHECK(state NOT IN ('queued','dispatching','succeeded') OR failure IS NULL),
 CHECK(state IN ('queued','dispatching','succeeded') OR failure IS NOT NULL),
 CHECK(state<>'queued' OR attempts=0),
 CHECK(state<>'dispatching' OR (attempts=1 AND worker_id IS NOT NULL AND fence>0 AND lease_until IS NOT NULL)),
 CHECK(state='dispatching' OR (worker_id IS NULL AND lease_until IS NULL))
);
CREATE INDEX app_assessment_jobs_claim_idx ON {{assessment_jobs}}(scope,created_at,id)
 WHERE state='queued';
CREATE INDEX app_assessment_jobs_active_idx ON {{assessment_jobs}}(scope,lease_until,id)
 WHERE state='dispatching';
CREATE INDEX app_assessment_jobs_io_idx ON {{assessment_jobs}}(scope,io_deadline,id)
 WHERE io_token IS NOT NULL AND io_released_at IS NULL;
CREATE INDEX app_assessment_jobs_window_idx ON {{assessment_jobs}}(scope,dispatch_started_at)
 WHERE dispatch_started_at IS NOT NULL;
CREATE INDEX app_assessment_jobs_finding_idx ON {{assessment_jobs}}(workspace_id,finding_id,id);
`
