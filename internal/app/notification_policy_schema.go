package app

const schemaV21 = `
ALTER TABLE {{workspaces}}
 ADD COLUMN notification_policy_epoch bigint NOT NULL DEFAULT 0,
 ADD CONSTRAINT app_workspaces_notification_policy_epoch_check
  CHECK(notification_policy_epoch>=0);

ALTER TABLE {{finding_deliveries}}
 ADD COLUMN trigger_kind text NOT NULL DEFAULT 'manual',
 ADD COLUMN policy_id text,
 ADD COLUMN policy_revision bigint,
 ADD COLUMN finding_change_revision bigint,
 ADD CONSTRAINT app_finding_deliveries_trigger_kind_check
  CHECK(trigger_kind IN ('manual','notification-policy')),
 ADD CONSTRAINT app_finding_deliveries_policy_binding_check CHECK(
  (trigger_kind='manual' AND policy_id IS NULL AND policy_revision IS NULL AND finding_change_revision IS NULL) OR
  (trigger_kind='notification-policy' AND policy_id IS NOT NULL AND policy_revision IS NOT NULL
   AND policy_revision>0 AND finding_change_revision IS NOT NULL AND finding_change_revision>0));
CREATE UNIQUE INDEX app_finding_deliveries_policy_effect_key
 ON {{finding_deliveries}}(workspace_id,policy_id,finding_id,finding_change_revision)
 WHERE trigger_kind='notification-policy';

CREATE TABLE {{notification_policies}} (
 id text PRIMARY KEY CHECK(id ~ '^[0-9a-f]{32}$'),
 workspace_id text NOT NULL,
 name text NOT NULL CHECK(octet_length(name) BETWEEN 1 AND 256),
 connection_id text NOT NULL,
 connection_profile text NOT NULL
  CHECK(connection_profile IN ('slack-workspace-bot','teams-workflows-channel','jira-cloud-v3')),
 connection_revision bigint NOT NULL CHECK(connection_revision>0),
 enabled boolean NOT NULL,
 change_kinds text[] NOT NULL,
 minimum_severity text NOT NULL CHECK(minimum_severity IN ('critical','high','medium','low','info')),
 revision bigint NOT NULL DEFAULT 1 CHECK(revision>0),
 epoch bigint NOT NULL CHECK(epoch>0),
 approved_by text NOT NULL,
 rationale text NOT NULL CHECK(octet_length(rationale) BETWEEN 1 AND 8192),
 created_at timestamptz NOT NULL,
 updated_at timestamptz NOT NULL,
 CONSTRAINT app_notification_policies_workspace_id_key UNIQUE(workspace_id,id),
 CONSTRAINT app_notification_policies_change_kinds_count_check
  CHECK(cardinality(change_kinds)>=1 AND cardinality(change_kinds)<=3),
 CONSTRAINT app_notification_policies_change_kinds_values_check
  CHECK(change_kinds <@ ARRAY['new','changed','reopened']::text[]),
 CONSTRAINT app_notification_policies_workspace_connection_fkey
  FOREIGN KEY(workspace_id,connection_id) REFERENCES {{integration_connections}}(workspace_id,id),
 CONSTRAINT app_notification_policies_workspace_approver_fkey
  FOREIGN KEY(workspace_id,approved_by) REFERENCES {{memberships}}(workspace_id,user_id)
);

CREATE TABLE {{notification_policy_revisions}} (
 id text PRIMARY KEY CHECK(id ~ '^[0-9a-f]{32}$'),
 workspace_id text NOT NULL,
 policy_id text NOT NULL,
 name text NOT NULL CHECK(octet_length(name) BETWEEN 1 AND 256),
 connection_id text NOT NULL,
 connection_profile text NOT NULL
  CHECK(connection_profile IN ('slack-workspace-bot','teams-workflows-channel','jira-cloud-v3')),
 connection_revision bigint NOT NULL CHECK(connection_revision>0),
 enabled boolean NOT NULL,
 change_kinds text[] NOT NULL,
 minimum_severity text NOT NULL CHECK(minimum_severity IN ('critical','high','medium','low','info')),
 revision bigint NOT NULL CHECK(revision>0),
 epoch bigint NOT NULL CHECK(epoch>0),
 actor_id text NOT NULL,
 actor_name text NOT NULL CHECK(octet_length(actor_name) BETWEEN 1 AND 256),
 rationale text NOT NULL CHECK(octet_length(rationale) BETWEEN 1 AND 8192),
 created_at timestamptz NOT NULL,
 CONSTRAINT app_notification_policy_revisions_workspace_id_key UNIQUE(workspace_id,id),
 CONSTRAINT app_notification_policy_revisions_policy_revision_key UNIQUE(workspace_id,policy_id,revision),
 CONSTRAINT app_notification_policy_revisions_workspace_epoch_key UNIQUE(workspace_id,epoch),
 CONSTRAINT app_notification_policy_revisions_change_kinds_count_check
  CHECK(cardinality(change_kinds)>=1 AND cardinality(change_kinds)<=3),
 CONSTRAINT app_notification_policy_revisions_change_kinds_values_check
  CHECK(change_kinds <@ ARRAY['new','changed','reopened']::text[]),
 CONSTRAINT app_notification_policy_revisions_policy_fkey
  FOREIGN KEY(workspace_id,policy_id) REFERENCES {{notification_policies}}(workspace_id,id),
 CONSTRAINT app_notification_policy_revisions_workspace_actor_fkey
  FOREIGN KEY(workspace_id,actor_id) REFERENCES {{memberships}}(workspace_id,user_id)
);

CREATE TABLE {{finding_change_events}} (
 id text PRIMARY KEY CHECK(id ~ '^[0-9a-f]{32}$'),
 workspace_id text NOT NULL,
 finding_id text NOT NULL,
 policy_epoch bigint NOT NULL CHECK(policy_epoch>0),
 title text NOT NULL CHECK(octet_length(title) BETWEEN 1 AND 1024),
 severity text NOT NULL CHECK(severity IN ('critical','high','medium','low','info')),
 asset_name text NOT NULL CHECK(octet_length(asset_name) BETWEEN 1 AND 256),
 change_kind text NOT NULL CHECK(change_kind IN ('new','changed','reopened')),
 change_revision bigint NOT NULL CHECK(change_revision>0),
 change_at timestamptz NOT NULL,
 state text NOT NULL DEFAULT 'pending' CHECK(state IN ('pending','processing','evaluated')),
 worker_id text,
 fence bigint NOT NULL DEFAULT 0,
 lease_until timestamptz,
 CONSTRAINT app_finding_change_events_workspace_id_key UNIQUE(workspace_id,id),
 CONSTRAINT app_finding_change_events_finding_revision_key UNIQUE(workspace_id,finding_id,change_revision),
 CONSTRAINT app_finding_change_events_finding_fkey
  FOREIGN KEY(workspace_id,finding_id) REFERENCES {{findings}}(workspace_id,id) ON DELETE CASCADE,
 CONSTRAINT app_finding_change_events_state_lease_check CHECK(
  (state='pending' AND worker_id IS NULL AND fence=0 AND lease_until IS NULL) OR
  (state='processing' AND worker_id IS NOT NULL AND fence>0 AND lease_until IS NOT NULL) OR
  (state='evaluated' AND worker_id IS NULL AND lease_until IS NULL))
);
CREATE INDEX app_finding_change_events_claim_idx
 ON {{finding_change_events}}(change_at,id) WHERE state IN ('pending','processing');

CREATE TABLE {{notification_policy_events}} (
 id text PRIMARY KEY CHECK(id ~ '^[0-9a-f]{32}$'),
 workspace_id text NOT NULL,
 policy_id text NOT NULL,
 policy_revision bigint NOT NULL CHECK(policy_revision>0),
 finding_id text NOT NULL,
 finding_change_revision bigint NOT NULL CHECK(finding_change_revision>0),
 outcome text NOT NULL CHECK(outcome IN ('queued','duplicate-ticket','connection-stale','invalid-payload')),
 delivery_id text,
 created_at timestamptz NOT NULL,
 CONSTRAINT app_notification_policy_events_workspace_id_key UNIQUE(workspace_id,id),
 CONSTRAINT app_notification_policy_events_policy_finding_revision_key
  UNIQUE(workspace_id,policy_id,finding_id,finding_change_revision),
 CONSTRAINT app_notification_policy_events_delivery_semantics_check CHECK(
  outcome = ANY(ARRAY['queued','duplicate-ticket']::text[]) AND delivery_id IS NOT NULL OR
  outcome = ANY(ARRAY['connection-stale','invalid-payload']::text[]) AND delivery_id IS NULL),
 CONSTRAINT app_notification_policy_events_policy_fkey
  FOREIGN KEY(workspace_id,policy_id) REFERENCES {{notification_policies}}(workspace_id,id),
 CONSTRAINT app_notification_policy_events_finding_fkey
  FOREIGN KEY(workspace_id,finding_id) REFERENCES {{findings}}(workspace_id,id) ON DELETE CASCADE,
 CONSTRAINT app_notification_policy_events_delivery_fkey
  FOREIGN KEY(workspace_id,delivery_id) REFERENCES {{finding_deliveries}}(workspace_id,id)
);
CREATE INDEX app_notification_policy_events_policy_idx
 ON {{notification_policy_events}}(workspace_id,policy_id,id);

CREATE TABLE {{jira_finding_effects}} (
 workspace_id text NOT NULL,
 connection_id text NOT NULL,
 finding_id text NOT NULL,
 delivery_id text NOT NULL,
 created_at timestamptz NOT NULL,
 PRIMARY KEY(workspace_id,connection_id,finding_id),
 CONSTRAINT app_jira_finding_effects_delivery_key UNIQUE(workspace_id,delivery_id),
 CONSTRAINT app_jira_finding_effects_connection_fkey
  FOREIGN KEY(workspace_id,connection_id) REFERENCES {{integration_connections}}(workspace_id,id),
 CONSTRAINT app_jira_finding_effects_finding_fkey
  FOREIGN KEY(workspace_id,finding_id) REFERENCES {{findings}}(workspace_id,id) ON DELETE CASCADE,
 CONSTRAINT app_jira_finding_effects_delivery_fkey
  FOREIGN KEY(workspace_id,delivery_id) REFERENCES {{finding_deliveries}}(workspace_id,id)
);

INSERT INTO {{jira_finding_effects}}(workspace_id,connection_id,finding_id,delivery_id,created_at)
SELECT DISTINCT ON (workspace_id,connection_id,finding_id)
 workspace_id,connection_id,finding_id,id,created_at
FROM {{finding_deliveries}}
WHERE profile='jira-cloud-v3'
ORDER BY workspace_id,connection_id,finding_id,created_at,id;
`
