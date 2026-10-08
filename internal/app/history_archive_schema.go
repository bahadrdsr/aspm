package app

const schemaV24 = `
ALTER TABLE {{finding_decision_events}}
 ADD COLUMN detail_availability text NOT NULL DEFAULT 'available',
 ADD COLUMN detail_revision bigint NOT NULL DEFAULT 1,
 ADD COLUMN archive_key text,
 ADD COLUMN archive_digest text,
 ADD COLUMN archive_size bigint,
 ADD COLUMN archived_at timestamptz,
 ADD CONSTRAINT app_finding_decision_events_detail_availability_check
  CHECK(detail_availability IN ('available','archived','missing','corrupt')),
 ADD CONSTRAINT app_finding_decision_events_detail_revision_check CHECK(detail_revision>0),
 ADD CONSTRAINT app_finding_decision_events_archive_reference_check CHECK(
  (archive_key IS NULL AND archive_digest IS NULL AND archive_size IS NULL AND archived_at IS NULL)
  OR (archive_key IS NOT NULL AND archive_digest IS NOT NULL AND archive_size IS NOT NULL AND archived_at IS NOT NULL));

ALTER TABLE {{notification_policy_revisions}}
 ADD COLUMN detail_availability text NOT NULL DEFAULT 'available',
 ADD COLUMN detail_revision bigint NOT NULL DEFAULT 1,
 ADD COLUMN archive_key text,
 ADD COLUMN archive_digest text,
 ADD COLUMN archive_size bigint,
 ADD COLUMN archived_at timestamptz,
 ADD CONSTRAINT app_notification_policy_revisions_detail_availability_check
  CHECK(detail_availability IN ('available','archived','missing','corrupt')),
 ADD CONSTRAINT app_notification_policy_revisions_detail_revision_check CHECK(detail_revision>0),
 ADD CONSTRAINT app_notification_policy_revisions_archive_reference_check CHECK(
  (archive_key IS NULL AND archive_digest IS NULL AND archive_size IS NULL AND archived_at IS NULL)
  OR (archive_key IS NOT NULL AND archive_digest IS NOT NULL AND archive_size IS NOT NULL AND archived_at IS NOT NULL));

ALTER TABLE {{finding_change_events}}
 ADD COLUMN detail_availability text NOT NULL DEFAULT 'available',
 ADD COLUMN detail_revision bigint NOT NULL DEFAULT 1,
 ADD COLUMN archive_key text,
 ADD COLUMN archive_digest text,
 ADD COLUMN archive_size bigint,
 ADD COLUMN archived_at timestamptz,
 ADD CONSTRAINT app_finding_change_events_detail_availability_check
  CHECK(detail_availability IN ('available','archived','missing','corrupt')),
 ADD CONSTRAINT app_finding_change_events_detail_revision_check CHECK(detail_revision>0),
 ADD CONSTRAINT app_finding_change_events_archive_reference_check CHECK(
  (archive_key IS NULL AND archive_digest IS NULL AND archive_size IS NULL AND archived_at IS NULL)
  OR (archive_key IS NOT NULL AND archive_digest IS NOT NULL AND archive_size IS NOT NULL AND archived_at IS NOT NULL));

ALTER TABLE {{notification_policy_events}}
 ADD COLUMN detail_availability text NOT NULL DEFAULT 'available',
 ADD COLUMN detail_revision bigint NOT NULL DEFAULT 1,
 ADD COLUMN archive_key text,
 ADD COLUMN archive_digest text,
 ADD COLUMN archive_size bigint,
 ADD COLUMN archived_at timestamptz,
 ADD CONSTRAINT app_notification_policy_events_detail_availability_check
  CHECK(detail_availability IN ('available','archived','missing','corrupt')),
 ADD CONSTRAINT app_notification_policy_events_detail_revision_check CHECK(detail_revision>0),
 ADD CONSTRAINT app_notification_policy_events_archive_reference_check CHECK(
  (archive_key IS NULL AND archive_digest IS NULL AND archive_size IS NULL AND archived_at IS NULL)
  OR (archive_key IS NOT NULL AND archive_digest IS NOT NULL AND archive_size IS NOT NULL AND archived_at IS NOT NULL));

ALTER TABLE {{retention_run_items}}
 DROP CONSTRAINT app_retention_run_items_resource_kind_check,
 ADD CONSTRAINT app_retention_run_items_resource_kind_check
 CHECK(resource_kind IN (
  'import','observation','correlation-event','archive-object',
  'finding-decision-event','notification-policy-revision',
  'finding-change-event','notification-policy-event'
 ));

ALTER TABLE {{archive_publications}}
 DROP CONSTRAINT app_archive_publications_resource_kind_check,
 ADD CONSTRAINT app_archive_publications_resource_kind_check
 CHECK(resource_kind IN (
  'observation','correlation-event','finding-decision-event',
  'notification-policy-revision','finding-change-event','notification-policy-event'
 ));
`
