package app

const schemaV23 = `
ALTER TABLE {{retention_holds}}
 DROP CONSTRAINT app_retention_holds_resource_kind_check,
 ADD CONSTRAINT app_retention_holds_resource_kind_check
 CHECK(resource_kind IN (
  'import','observation','correlation-event',
  'finding-decision-event','notification-policy-revision',
  'finding-change-event','notification-policy-event'
 ));

ALTER TABLE {{retention_preview_items}}
 DROP CONSTRAINT app_retention_preview_items_resource_kind_check,
 ADD CONSTRAINT app_retention_preview_items_resource_kind_check
 CHECK(resource_kind IN (
  'import','observation','correlation-event','archive-object',
  'finding-decision-event','notification-policy-revision',
  'finding-change-event','notification-policy-event'
 ));

CREATE INDEX app_finding_decision_events_retention_idx
 ON {{finding_decision_events}}(workspace_id,created_at,id);
CREATE INDEX app_notification_policy_revisions_retention_idx
 ON {{notification_policy_revisions}}(workspace_id,created_at,id);
CREATE INDEX app_finding_change_events_retention_idx
 ON {{finding_change_events}}(workspace_id,change_at,id);
CREATE INDEX app_notification_policy_events_retention_idx
 ON {{notification_policy_events}}(workspace_id,created_at,id);
`
