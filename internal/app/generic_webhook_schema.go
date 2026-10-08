package app

const schemaV22 = `
ALTER TABLE {{integration_connections}} ADD COLUMN webhook_target jsonb;
ALTER TABLE {{finding_deliveries}} ADD COLUMN webhook_target jsonb;

ALTER TABLE {{integration_connections}}
 DROP CONSTRAINT app_integration_connections_profile_check,
 DROP CONSTRAINT app_integration_connections_profile_target_check,
 ADD CONSTRAINT app_integration_connections_profile_check
 CHECK (profile IN ('slack-workspace-bot','jira-cloud-v3','teams-workflows-channel','generic-webhook-v1')),
 ADD CONSTRAINT app_integration_connections_profile_target_check
 CHECK ((profile='slack-workspace-bot' AND jira_target IS NULL AND teams_target IS NULL AND webhook_target IS NULL
         AND channel ~ '^[CG][A-Z0-9]{2,127}$')
 OR (profile='jira-cloud-v3' AND jira_target IS NOT NULL AND jsonb_typeof(jira_target)='object'
     AND teams_target IS NULL AND webhook_target IS NULL AND channel='')
 OR (profile='teams-workflows-channel' AND jira_target IS NULL AND teams_target IS NOT NULL
     AND jsonb_typeof(teams_target)='object' AND webhook_target IS NULL AND channel='')
 OR (profile='generic-webhook-v1' AND jira_target IS NULL AND teams_target IS NULL
     AND webhook_target IS NOT NULL AND jsonb_typeof(webhook_target)='object' AND channel=''));

ALTER TABLE {{finding_deliveries}}
 DROP CONSTRAINT app_finding_deliveries_profile_check,
 DROP CONSTRAINT app_finding_deliveries_profile_target_check,
 ADD CONSTRAINT app_finding_deliveries_profile_check
 CHECK (profile IN ('slack-workspace-bot','jira-cloud-v3','teams-workflows-channel','generic-webhook-v1')),
 ADD CONSTRAINT app_finding_deliveries_profile_target_check
 CHECK ((profile='slack-workspace-bot' AND jira_target IS NULL AND teams_target IS NULL AND webhook_target IS NULL
         AND channel ~ '^[CG][A-Z0-9]{2,127}$')
 OR (profile='jira-cloud-v3' AND jira_target IS NOT NULL AND jsonb_typeof(jira_target)='object'
     AND teams_target IS NULL AND webhook_target IS NULL AND channel='')
 OR (profile='teams-workflows-channel' AND jira_target IS NULL AND teams_target IS NOT NULL
     AND jsonb_typeof(teams_target)='object' AND webhook_target IS NULL AND channel='')
 OR (profile='generic-webhook-v1' AND jira_target IS NULL AND teams_target IS NULL
     AND webhook_target IS NOT NULL AND jsonb_typeof(webhook_target)='object' AND channel=''));

ALTER TABLE {{notification_policies}}
 DROP CONSTRAINT app_notification_policies_connection_profile_check,
 ADD CONSTRAINT app_notification_policies_connection_profile_check
 CHECK(connection_profile IN ('slack-workspace-bot','teams-workflows-channel','jira-cloud-v3','generic-webhook-v1'));

ALTER TABLE {{notification_policy_revisions}}
 DROP CONSTRAINT app_notification_policy_revisions_connection_profile_check,
 ADD CONSTRAINT app_notification_policy_revisions_connection_profile_check
 CHECK(connection_profile IN ('slack-workspace-bot','teams-workflows-channel','jira-cloud-v3','generic-webhook-v1'));
`
