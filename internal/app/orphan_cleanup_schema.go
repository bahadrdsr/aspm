package app

const schemaV18 = `
CREATE TABLE {{archive_publications}} (
 id text PRIMARY KEY CHECK(id ~ '^[0-9a-f]{32}$'),
 workspace_id text NOT NULL REFERENCES {{workspaces}}(id),
 object_key text NOT NULL CHECK(octet_length(object_key) BETWEEN 1 AND 4096),
 resource_kind text NOT NULL CHECK(resource_kind IN ('observation','correlation-event')),
 resource_id text NOT NULL CHECK(resource_id ~ '^[0-9a-f]{32}$'),
 object_digest text NOT NULL CHECK(object_digest ~ '^sha256:[0-9a-f]{64}$'),
 object_size bigint NOT NULL CHECK(object_size>=0),
 state text NOT NULL CHECK(state IN ('publishing','referenced','orphan','deleted')),
 revision bigint NOT NULL DEFAULT 1 CHECK(revision>0),
 created_at timestamptz NOT NULL,
 updated_at timestamptz NOT NULL,
 referenced_at timestamptz,
 deleted_at timestamptz,
 CONSTRAINT app_archive_publications_workspace_id_key UNIQUE(workspace_id,id),
 CONSTRAINT app_archive_publications_object_key UNIQUE(workspace_id,object_key),
 CHECK((state='referenced')=(referenced_at IS NOT NULL)),
 CHECK((state='deleted')=(deleted_at IS NOT NULL))
);
CREATE INDEX app_archive_publications_orphan_idx
 ON {{archive_publications}}(workspace_id,state,updated_at,id)
 WHERE state IN ('publishing','orphan');

ALTER TABLE {{retention_preview_items}}
 DROP CONSTRAINT app_retention_preview_items_class_check,
 DROP CONSTRAINT app_retention_preview_items_resource_kind_check,
 DROP CONSTRAINT app_retention_preview_items_action_check,
 ADD COLUMN object_key text,
 ADD COLUMN object_digest text,
 ADD COLUMN object_revision bigint,
 ADD CONSTRAINT app_retention_preview_items_class_check
  CHECK(class IN ('hot-history','archived-evidence','raw-report','audit','orphan-archive')),
 ADD CONSTRAINT app_retention_preview_items_resource_kind_check
  CHECK(resource_kind IN ('import','observation','correlation-event','archive-object')),
 ADD CONSTRAINT app_retention_preview_items_action_check
  CHECK(action IN ('archive-history','expire-archive','expire-raw-report','archive-audit','delete-orphan')),
 ADD CONSTRAINT app_retention_preview_items_object_check CHECK(
  (resource_kind='archive-object' AND object_key IS NOT NULL AND
   object_digest ~ '^sha256:[0-9a-f]{64}$' AND object_revision>0)
  OR (resource_kind<>'archive-object' AND object_key IS NULL AND object_digest IS NULL AND object_revision IS NULL));

ALTER TABLE {{retention_run_items}}
 DROP CONSTRAINT app_retention_run_items_class_check,
 DROP CONSTRAINT app_retention_run_items_resource_kind_check,
 DROP CONSTRAINT app_retention_run_items_action_check,
 ADD COLUMN object_key text,
 ADD COLUMN object_digest text,
 ADD COLUMN object_revision bigint,
 ADD CONSTRAINT app_retention_run_items_class_check
  CHECK(class IN ('hot-history','archived-evidence','raw-report','audit','orphan-archive')),
 ADD CONSTRAINT app_retention_run_items_resource_kind_check
  CHECK(resource_kind IN ('import','observation','correlation-event','archive-object')),
 ADD CONSTRAINT app_retention_run_items_action_check
  CHECK(action IN ('archive-history','expire-archive','expire-raw-report','archive-audit','restore-archive','delete-orphan')),
 ADD CONSTRAINT app_retention_run_items_object_check CHECK(
  (resource_kind='archive-object' AND object_key IS NOT NULL AND
   object_digest ~ '^sha256:[0-9a-f]{64}$' AND object_revision>0)
  OR (resource_kind<>'archive-object' AND object_key IS NULL AND object_digest IS NULL AND object_revision IS NULL));
`
