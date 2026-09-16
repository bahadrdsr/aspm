package app

const schemaV4 = `
ALTER TABLE {{imports}} ADD COLUMN submitted_by text REFERENCES {{users}}(id);
CREATE UNIQUE INDEX app_imports_source_scan_identity ON {{imports}}(workspace_id,source_id,scan_id);
CREATE INDEX app_imports_pending_actor ON {{imports}}(workspace_id,submitted_by)
 WHERE state IN ('queued','processing');
`
