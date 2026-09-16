package app

const schemaV2 = `
CREATE TABLE {{report_snapshots}} (
 id text PRIMARY KEY, workspace_id text NOT NULL REFERENCES {{workspaces}}(id),
 name text NOT NULL CHECK (octet_length(name) BETWEEN 1 AND 256),
 requested_by text NOT NULL,
 freshness_days integer NOT NULL CHECK (freshness_days BETWEEN 1 AND 365),
 created_at timestamptz NOT NULL, completed_at timestamptz,
 state text NOT NULL DEFAULT 'queued' CHECK (state IN ('queued','processing','succeeded','failed')),
 report jsonb,
 failure_code text, failure_message text,
 worker_id text, fence bigint NOT NULL DEFAULT 0 CHECK (fence>=0),
 attempts integer NOT NULL DEFAULT 0 CHECK (attempts>=0),
 lease_until timestamptz, available_at timestamptz NOT NULL DEFAULT clock_timestamp(),
 UNIQUE(workspace_id,id),
 FOREIGN KEY(workspace_id,requested_by) REFERENCES {{memberships}}(workspace_id,user_id),
 CHECK ((state='succeeded')=(report IS NOT NULL)),
 CHECK ((state='succeeded')=(completed_at IS NOT NULL)),
 CHECK ((state='processing')=(worker_id IS NOT NULL AND lease_until IS NOT NULL)),
 CHECK (state='processing' OR (worker_id IS NULL AND lease_until IS NULL)),
 CHECK (report IS NULL OR jsonb_typeof(report)='object'),
 CHECK ((failure_code IS NULL)=(failure_message IS NULL)),
 CHECK (state<>'failed' OR failure_code IS NOT NULL)
);
CREATE INDEX app_report_snapshots_claim_idx ON {{report_snapshots}}(available_at,id)
 WHERE state IN ('queued','processing');
CREATE INDEX app_imports_reporting_idx ON {{imports}}
 (workspace_id,asset_id,source_id,scope_id,scope_revision,scope_branch,source_scan_at)
 WHERE state='succeeded' AND source_status='succeeded' AND scan_kind='full' AND completeness='complete';
`
