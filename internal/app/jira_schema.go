package app

const schemaV10 = `
ALTER TABLE {{integration_connections}} ADD COLUMN jira_target jsonb;
ALTER TABLE {{finding_deliveries}} ADD COLUMN jira_target jsonb;
ALTER TABLE {{finding_deliveries}} ADD COLUMN create_attempted_at timestamptz;
ALTER TABLE {{integration_connections}}
 DROP CONSTRAINT app_integration_connections_profile_check,
 ADD CONSTRAINT app_integration_connections_profile_check
 CHECK (profile IN ('slack-workspace-bot','jira-cloud-v3')),
 ADD CONSTRAINT app_integration_connections_profile_target_check
 CHECK ((profile='slack-workspace-bot' AND jira_target IS NULL AND channel ~ '^[CG][A-Z0-9]{2,127}$')
 OR (profile='jira-cloud-v3' AND jira_target IS NOT NULL AND jsonb_typeof(jira_target)='object' AND channel=''));
ALTER TABLE {{finding_deliveries}}
 DROP CONSTRAINT app_finding_deliveries_profile_check,
 ADD CONSTRAINT app_finding_deliveries_profile_check
 CHECK (profile IN ('slack-workspace-bot','jira-cloud-v3')),
 ADD CONSTRAINT app_finding_deliveries_profile_target_check
 CHECK ((profile='slack-workspace-bot' AND jira_target IS NULL AND channel ~ '^[CG][A-Z0-9]{2,127}$')
 OR (profile='jira-cloud-v3' AND jira_target IS NOT NULL AND jsonb_typeof(jira_target)='object' AND channel=''));
`
