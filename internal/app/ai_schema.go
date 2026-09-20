package app

const schemaV7 = `
CREATE TABLE {{ai_profiles}} (
 id text PRIMARY KEY,
 workspace_id text NOT NULL REFERENCES {{workspaces}}(id),
 name text NOT NULL CHECK(octet_length(name) BETWEEN 1 AND 256),
 family text NOT NULL CHECK(family IN ('openai','azure-foundry','anthropic','local')),
 endpoint text NOT NULL CHECK(octet_length(endpoint) BETWEEN 1 AND 16384),
 model text NOT NULL CHECK(octet_length(model) BETWEEN 1 AND 256),
 deployment text NOT NULL CHECK(octet_length(deployment)<=256),
 enabled boolean NOT NULL, structured_output boolean NOT NULL,
 credential_ciphertext bytea CHECK(octet_length(credential_ciphertext) BETWEEN 30 AND 16413),
 revision bigint NOT NULL DEFAULT 1 CHECK(revision>0),
 created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL,
 UNIQUE(workspace_id,id),
 CHECK((family='azure-foundry' AND btrim(deployment)<>'') OR
       (family<>'azure-foundry' AND deployment='')),
 CHECK(family='local' OR credential_ciphertext IS NOT NULL)
);
CREATE TABLE {{ai_policies}} (
 workspace_id text PRIMARY KEY REFERENCES {{workspaces}}(id),
 mode text NOT NULL CHECK(mode IN ('disabled','local-only','approved-hosted')),
 revision bigint NOT NULL DEFAULT 1 CHECK(revision>0),
 updated_at timestamptz NOT NULL,
 updated_by text NOT NULL REFERENCES {{users}}(id)
);
CREATE TABLE {{ai_egress_grants}} (
 id text PRIMARY KEY,
 workspace_id text NOT NULL REFERENCES {{workspaces}}(id),
 profile_id text NOT NULL,
 profile_revision text NOT NULL CHECK(profile_revision ~ '^[1-9][0-9]{0,18}$'),
 policy_revision text NOT NULL CHECK(policy_revision ~ '^[1-9][0-9]{0,18}$'),
 destination text NOT NULL CHECK(octet_length(destination) BETWEEN 1 AND 16384),
 task text NOT NULL CHECK(task='finding-validity'),
 data_class text NOT NULL CHECK(data_class='finding-evidence'),
 expires_at timestamptz NOT NULL, created_at timestamptz NOT NULL,
 granted_by text NOT NULL REFERENCES {{users}}(id),
 revoked_at timestamptz, revoked_by text REFERENCES {{users}}(id),
 UNIQUE(workspace_id,id),
 FOREIGN KEY(workspace_id,profile_id) REFERENCES {{ai_profiles}}(workspace_id,id),
 CHECK(expires_at>created_at),
 CHECK((revoked_at IS NULL)=(revoked_by IS NULL))
);
`
