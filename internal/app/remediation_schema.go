package app

const schemaV5 = `
CREATE TABLE {{integration_connections}} (
 id text PRIMARY KEY,
 workspace_id text NOT NULL REFERENCES {{workspaces}}(id),
 profile text NOT NULL CHECK(profile='slack-workspace-bot'),
 name text NOT NULL, channel text NOT NULL,
 credential_ciphertext bytea NOT NULL CHECK(octet_length(credential_ciphertext) BETWEEN 30 AND 16413),
 enabled boolean NOT NULL,
 revision bigint NOT NULL DEFAULT 1 CHECK(revision>0),
 created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL,
 UNIQUE(workspace_id,id)
);
CREATE INDEX app_integration_connections_workspace_idx ON {{integration_connections}}(workspace_id,id);
CREATE TABLE {{finding_deliveries}} (
 id text PRIMARY KEY,
 workspace_id text NOT NULL REFERENCES {{workspaces}}(id),
 finding_id text NOT NULL, connection_id text NOT NULL,
 connection_revision bigint NOT NULL CHECK(connection_revision>0),
 profile text NOT NULL CHECK(profile='slack-workspace-bot'), channel text NOT NULL,
 requested_by text NOT NULL REFERENCES {{users}}(id),
 idempotency_key text NOT NULL,
 binding_digest bytea NOT NULL CHECK(octet_length(binding_digest)=32),
 approval_ref text NOT NULL, payload jsonb NOT NULL,
 state text NOT NULL DEFAULT 'queued'
   CHECK(state IN ('queued','dispatching','confirmed','accepted','blocked','failed','rate-limited','uncertain')),
 created_at timestamptz NOT NULL,
 dispatch_started_at timestamptz, completed_at timestamptz,
 worker_id text, fence bigint NOT NULL DEFAULT 0 CHECK(fence>=0), lease_until timestamptz,
 receipt jsonb, failure jsonb,
 UNIQUE(workspace_id,idempotency_key),
 UNIQUE(workspace_id,id),
 FOREIGN KEY(workspace_id,connection_id) REFERENCES {{integration_connections}}(workspace_id,id),
 CHECK(state<>'dispatching' OR (worker_id IS NOT NULL AND fence>0 AND lease_until IS NOT NULL AND dispatch_started_at IS NOT NULL)),
 CHECK(state<>'queued' OR (dispatch_started_at IS NULL AND completed_at IS NULL AND receipt IS NULL AND failure IS NULL))
);
CREATE INDEX app_finding_deliveries_claim_idx ON {{finding_deliveries}}(created_at,id)
 WHERE state IN ('queued','dispatching');
CREATE INDEX app_finding_deliveries_finding_idx ON {{finding_deliveries}}(workspace_id,finding_id,id);
`
