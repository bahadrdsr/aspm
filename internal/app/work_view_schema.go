package app

const schemaV9 = `
CREATE TABLE {{work_views}} (
 id text PRIMARY KEY,
 workspace_id text NOT NULL REFERENCES {{workspaces}}(id),
 user_id text NOT NULL REFERENCES {{users}}(id),
 name text NOT NULL CHECK(octet_length(name) BETWEEN 1 AND 256),
 query text NOT NULL CHECK(octet_length(query)<=512),
 sort text NOT NULL CHECK(sort IN ('source-order','severity','title')),
 revision bigint NOT NULL DEFAULT 1 CHECK(revision>0),
 created_at timestamptz NOT NULL, updated_at timestamptz NOT NULL,
 CHECK(updated_at>=created_at)
);
CREATE INDEX app_work_views_owner_idx ON {{work_views}}(workspace_id,user_id,id);
`
