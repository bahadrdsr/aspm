package app

const schemaV17 = `
ALTER TABLE {{findings}}
 ADD COLUMN content_digest text NOT NULL DEFAULT ''
  CHECK(content_digest='' OR content_digest ~ '^sha256:[0-9a-f]{64}$'),
 ADD COLUMN change_kind text NOT NULL DEFAULT 'unchanged'
  CHECK(change_kind IN ('new','changed','unchanged','reopened','inferred-resolved')),
 ADD COLUMN change_at timestamptz,
 ADD COLUMN change_run_id text NOT NULL DEFAULT ''
  CHECK(change_run_id='' OR change_run_id ~ '^[0-9a-f]{32}$'),
 ADD COLUMN change_revision bigint NOT NULL DEFAULT 1 CHECK(change_revision>0);

CREATE INDEX app_findings_meaningful_change_idx
 ON {{findings}}(workspace_id,change_kind,id)
 WHERE change_kind IN ('new','changed','reopened');
`
