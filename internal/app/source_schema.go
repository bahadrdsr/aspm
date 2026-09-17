package app

const schemaV6 = `
CREATE TABLE {{source_connections}} (
 id text PRIMARY KEY,
 workspace_id text NOT NULL REFERENCES {{workspaces}}(id),
 profile text NOT NULL CHECK(profile='github-cloud-app'),
 name text NOT NULL, repository text NOT NULL, enabled boolean NOT NULL,
 credential_ciphertext bytea NOT NULL CHECK(octet_length(credential_ciphertext) BETWEEN 30 AND 16413),
 revision bigint NOT NULL DEFAULT 1 CHECK(revision>0),
 repository_id text,
 created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL,
 UNIQUE(workspace_id,id)
);
CREATE INDEX app_source_connections_workspace_idx ON {{source_connections}}(workspace_id,id);
CREATE TABLE {{source_collections}} (
 id text PRIMARY KEY,
 workspace_id text NOT NULL REFERENCES {{workspaces}}(id),
 source_id text NOT NULL, profile text NOT NULL CHECK(profile='github-cloud-app'),
 connection_revision bigint NOT NULL CHECK(connection_revision>0),
 repository text NOT NULL, requested_by text NOT NULL REFERENCES {{users}}(id),
 idempotency_key text NOT NULL, binding_digest bytea NOT NULL CHECK(octet_length(binding_digest)=32),
 state text NOT NULL DEFAULT 'queued' CHECK(state IN ('queued','collecting','succeeded','partial','blocked','failed')),
 complete boolean NOT NULL DEFAULT false,
 asset_id text, repository_id text,
 record_count integer NOT NULL DEFAULT 0 CHECK(record_count BETWEEN 0 AND 6401),
 gaps jsonb NOT NULL DEFAULT '[]',
 created_at timestamptz NOT NULL, collected_at timestamptz, completed_at timestamptz,
 failure jsonb, worker_id text, fence bigint NOT NULL DEFAULT 0 CHECK(fence>=0), lease_until timestamptz,
 UNIQUE(workspace_id,id), UNIQUE(workspace_id,idempotency_key),
 FOREIGN KEY(workspace_id,source_id) REFERENCES {{source_connections}}(workspace_id,id),
 CHECK(state<>'collecting' OR (worker_id IS NOT NULL AND fence>0 AND lease_until IS NOT NULL)),
 CHECK(NOT complete OR state='succeeded')
);
CREATE INDEX app_source_collections_claim_idx ON {{source_collections}}(created_at,id)
 WHERE state IN ('queued','collecting');
CREATE INDEX app_source_collections_source_idx ON {{source_collections}}(workspace_id,source_id,id);
CREATE TABLE {{source_repository_assets}} (
 workspace_id text NOT NULL, profile text NOT NULL CHECK(profile='github-cloud-app'),
 repository_id text NOT NULL, asset_id text NOT NULL,
 PRIMARY KEY(workspace_id,profile,repository_id),
 UNIQUE(workspace_id,profile,asset_id),
 FOREIGN KEY(workspace_id,asset_id) REFERENCES {{assets}}(workspace_id,id) ON DELETE CASCADE
);
CREATE TABLE {{source_collection_records}} (
 id text PRIMARY KEY, workspace_id text NOT NULL, collection_id text NOT NULL,
 ordinal integer NOT NULL CHECK(ordinal BETWEEN 0 AND 6400),
 kind text NOT NULL CHECK(kind IN ('repository','finding')),
 external_id text NOT NULL, parent_id text NOT NULL, native_run_id text NOT NULL,
 state text NOT NULL, severity text NOT NULL, location text NOT NULL, raw_url text NOT NULL,
 source_scan_at timestamptz, source_updated_at timestamptz,
 evidence jsonb NOT NULL,
 UNIQUE(collection_id,ordinal), UNIQUE(collection_id,kind,external_id),
 FOREIGN KEY(workspace_id,collection_id) REFERENCES {{source_collections}}(workspace_id,id)
);
CREATE INDEX app_source_collection_records_page_idx ON {{source_collection_records}}(workspace_id,collection_id,id);
`
