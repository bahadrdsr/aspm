package app

const schemaV3 = `
ALTER TABLE {{users}} ALTER COLUMN password_hash DROP NOT NULL;
ALTER TABLE {{users}} ADD COLUMN auth_method text NOT NULL DEFAULT 'local'
 CHECK (auth_method IN ('local','oidc'));
ALTER TABLE {{users}} ADD CONSTRAINT app_users_auth_credential_check
 CHECK ((auth_method='local' AND password_hash IS NOT NULL) OR (auth_method='oidc' AND password_hash IS NULL));
CREATE TABLE {{oidc_identities}} (
 issuer text COLLATE "C" NOT NULL, subject text COLLATE "C" NOT NULL,
 user_id text NOT NULL UNIQUE REFERENCES {{users}}(id),
 created_at timestamptz NOT NULL,
 PRIMARY KEY(issuer,subject),
 CHECK (octet_length(issuer) BETWEEN 1 AND 1024),
 CHECK (octet_length(subject) BETWEEN 1 AND 255)
);
CREATE TABLE {{oidc_flows}} (
 state_hash bytea PRIMARY KEY CHECK (octet_length(state_hash)=32),
 browser_hash bytea NOT NULL CHECK (octet_length(browser_hash)=32),
 nonce_hash bytea NOT NULL CHECK (octet_length(nonce_hash)=32),
 issuer text COLLATE "C" NOT NULL, client_id text COLLATE "C" NOT NULL,
 workspace_id text NOT NULL REFERENCES {{workspaces}}(id),
 redirect_uri text COLLATE "C" NOT NULL, created_at timestamptz NOT NULL, expires_at timestamptz NOT NULL,
 CHECK (expires_at>created_at)
);
CREATE INDEX app_oidc_flows_expiry_idx ON {{oidc_flows}}(expires_at);
`
