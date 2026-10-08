//go:build integration

package sourcecompat

import (
	"reflect"
	"testing"
)

var exactV21Catalog = map[string][]string{
	"notification_policies/columns": {
		"id|text|true|",
		"workspace_id|text|true|",
		"name|text|true|",
		"connection_id|text|true|",
		"connection_profile|text|true|",
		"connection_revision|bigint|true|",
		"enabled|boolean|true|",
		"change_kinds|text[]|true|",
		"minimum_severity|text|true|",
		"revision|bigint|true|1",
		"epoch|bigint|true|",
		"approved_by|text|true|",
		"rationale|text|true|",
		"created_at|timestamp with time zone|true|",
		"updated_at|timestamp with time zone|true|",
	},
	"notification_policies/constraints": {
		"app_notification_policies_change_kinds_count_check|CHECK (cardinality(change_kinds) >= 1 AND cardinality(change_kinds) <= 3)",
		"app_notification_policies_change_kinds_values_check|CHECK (change_kinds <@ ARRAY['new'::text, 'changed'::text, 'reopened'::text])",
		"app_notification_policies_connection_profile_check|CHECK (connection_profile = ANY (ARRAY['slack-workspace-bot'::text, 'teams-workflows-channel'::text, 'jira-cloud-v3'::text]))",
		"app_notification_policies_connection_revision_check|CHECK (connection_revision > 0)",
		"app_notification_policies_epoch_check|CHECK (epoch > 0)",
		"app_notification_policies_id_check|CHECK (id ~ '^[0-9a-f]{32}$'::text)",
		"app_notification_policies_minimum_severity_check|CHECK (minimum_severity = ANY (ARRAY['critical'::text, 'high'::text, 'medium'::text, 'low'::text, 'info'::text]))",
		"app_notification_policies_name_check|CHECK (octet_length(name) >= 1 AND octet_length(name) <= 256)",
		"app_notification_policies_pkey|PRIMARY KEY (id)",
		"app_notification_policies_rationale_check|CHECK (octet_length(rationale) >= 1 AND octet_length(rationale) <= 8192)",
		"app_notification_policies_revision_check|CHECK (revision > 0)",
		"app_notification_policies_workspace_approver_fkey|FOREIGN KEY (workspace_id, approved_by) REFERENCES app_memberships(workspace_id, user_id)",
		"app_notification_policies_workspace_connection_fkey|FOREIGN KEY (workspace_id, connection_id) REFERENCES app_integration_connections(workspace_id, id)",
		"app_notification_policies_workspace_id_key|UNIQUE (workspace_id, id)",
	},
	"notification_policies/indexes": {
		"app_notification_policies_pkey|CREATE UNIQUE INDEX app_notification_policies_pkey ON app_notification_policies USING btree (id)",
		"app_notification_policies_workspace_id_key|CREATE UNIQUE INDEX app_notification_policies_workspace_id_key ON app_notification_policies USING btree (workspace_id, id)",
	},
	"notification_policy_revisions/columns": {
		"id|text|true|",
		"workspace_id|text|true|",
		"policy_id|text|true|",
		"name|text|true|",
		"connection_id|text|true|",
		"connection_profile|text|true|",
		"connection_revision|bigint|true|",
		"enabled|boolean|true|",
		"change_kinds|text[]|true|",
		"minimum_severity|text|true|",
		"revision|bigint|true|",
		"epoch|bigint|true|",
		"actor_id|text|true|",
		"actor_name|text|true|",
		"rationale|text|true|",
		"created_at|timestamp with time zone|true|",
	},
	"notification_policy_revisions/constraints": {
		"app_notification_policy_revisions_actor_name_check|CHECK (octet_length(actor_name) >= 1 AND octet_length(actor_name) <= 256)",
		"app_notification_policy_revisions_change_kinds_count_check|CHECK (cardinality(change_kinds) >= 1 AND cardinality(change_kinds) <= 3)",
		"app_notification_policy_revisions_change_kinds_values_check|CHECK (change_kinds <@ ARRAY['new'::text, 'changed'::text, 'reopened'::text])",
		"app_notification_policy_revisions_connection_profile_check|CHECK (connection_profile = ANY (ARRAY['slack-workspace-bot'::text, 'teams-workflows-channel'::text, 'jira-cloud-v3'::text]))",
		"app_notification_policy_revisions_connection_revision_check|CHECK (connection_revision > 0)",
		"app_notification_policy_revisions_epoch_check|CHECK (epoch > 0)",
		"app_notification_policy_revisions_id_check|CHECK (id ~ '^[0-9a-f]{32}$'::text)",
		"app_notification_policy_revisions_minimum_severity_check|CHECK (minimum_severity = ANY (ARRAY['critical'::text, 'high'::text, 'medium'::text, 'low'::text, 'info'::text]))",
		"app_notification_policy_revisions_name_check|CHECK (octet_length(name) >= 1 AND octet_length(name) <= 256)",
		"app_notification_policy_revisions_pkey|PRIMARY KEY (id)",
		"app_notification_policy_revisions_policy_fkey|FOREIGN KEY (workspace_id, policy_id) REFERENCES app_notification_policies(workspace_id, id)",
		"app_notification_policy_revisions_policy_revision_key|UNIQUE (workspace_id, policy_id, revision)",
		"app_notification_policy_revisions_rationale_check|CHECK (octet_length(rationale) >= 1 AND octet_length(rationale) <= 8192)",
		"app_notification_policy_revisions_revision_check|CHECK (revision > 0)",
		"app_notification_policy_revisions_workspace_actor_fkey|FOREIGN KEY (workspace_id, actor_id) REFERENCES app_memberships(workspace_id, user_id)",
		"app_notification_policy_revisions_workspace_epoch_key|UNIQUE (workspace_id, epoch)",
		"app_notification_policy_revisions_workspace_id_key|UNIQUE (workspace_id, id)",
	},
	"notification_policy_revisions/indexes": {
		"app_notification_policy_revisions_pkey|CREATE UNIQUE INDEX app_notification_policy_revisions_pkey ON app_notification_policy_revisions USING btree (id)",
		"app_notification_policy_revisions_policy_revision_key|CREATE UNIQUE INDEX app_notification_policy_revisions_policy_revision_key ON app_notification_policy_revisions USING btree (workspace_id, policy_id, revision)",
		"app_notification_policy_revisions_workspace_epoch_key|CREATE UNIQUE INDEX app_notification_policy_revisions_workspace_epoch_key ON app_notification_policy_revisions USING btree (workspace_id, epoch)",
		"app_notification_policy_revisions_workspace_id_key|CREATE UNIQUE INDEX app_notification_policy_revisions_workspace_id_key ON app_notification_policy_revisions USING btree (workspace_id, id)",
	},
	"finding_change_events/columns": {
		"id|text|true|",
		"workspace_id|text|true|",
		"finding_id|text|true|",
		"policy_epoch|bigint|true|",
		"title|text|true|",
		"severity|text|true|",
		"asset_name|text|true|",
		"change_kind|text|true|",
		"change_revision|bigint|true|",
		"change_at|timestamp with time zone|true|",
		"state|text|true|'pending'::text",
		"worker_id|text|false|",
		"fence|bigint|true|0",
		"lease_until|timestamp with time zone|false|",
	},
	"finding_change_events/constraints": {
		"app_finding_change_events_asset_name_check|CHECK (octet_length(asset_name) >= 1 AND octet_length(asset_name) <= 256)",
		"app_finding_change_events_change_kind_check|CHECK (change_kind = ANY (ARRAY['new'::text, 'changed'::text, 'reopened'::text]))",
		"app_finding_change_events_change_revision_check|CHECK (change_revision > 0)",
		"app_finding_change_events_finding_fkey|FOREIGN KEY (workspace_id, finding_id) REFERENCES app_findings(workspace_id, id) ON DELETE CASCADE",
		"app_finding_change_events_finding_revision_key|UNIQUE (workspace_id, finding_id, change_revision)",
		"app_finding_change_events_id_check|CHECK (id ~ '^[0-9a-f]{32}$'::text)",
		"app_finding_change_events_pkey|PRIMARY KEY (id)",
		"app_finding_change_events_policy_epoch_check|CHECK (policy_epoch > 0)",
		"app_finding_change_events_severity_check|CHECK (severity = ANY (ARRAY['critical'::text, 'high'::text, 'medium'::text, 'low'::text, 'info'::text]))",
		"app_finding_change_events_state_check|CHECK (state = ANY (ARRAY['pending'::text, 'processing'::text, 'evaluated'::text]))",
		"app_finding_change_events_state_lease_check|CHECK (state = 'pending'::text AND worker_id IS NULL AND fence = 0 AND lease_until IS NULL OR state = 'processing'::text AND worker_id IS NOT NULL AND fence > 0 AND lease_until IS NOT NULL OR state = 'evaluated'::text AND worker_id IS NULL AND lease_until IS NULL)",
		"app_finding_change_events_title_check|CHECK (octet_length(title) >= 1 AND octet_length(title) <= 1024)",
		"app_finding_change_events_workspace_id_key|UNIQUE (workspace_id, id)",
	},
	"finding_change_events/indexes": {
		"app_finding_change_events_claim_idx|CREATE INDEX app_finding_change_events_claim_idx ON app_finding_change_events USING btree (change_at, id) WHERE (state = ANY (ARRAY['pending'::text, 'processing'::text]))",
		"app_finding_change_events_finding_revision_key|CREATE UNIQUE INDEX app_finding_change_events_finding_revision_key ON app_finding_change_events USING btree (workspace_id, finding_id, change_revision)",
		"app_finding_change_events_pkey|CREATE UNIQUE INDEX app_finding_change_events_pkey ON app_finding_change_events USING btree (id)",
		"app_finding_change_events_workspace_id_key|CREATE UNIQUE INDEX app_finding_change_events_workspace_id_key ON app_finding_change_events USING btree (workspace_id, id)",
	},
	"notification_policy_events/columns": {
		"id|text|true|",
		"workspace_id|text|true|",
		"policy_id|text|true|",
		"policy_revision|bigint|true|",
		"finding_id|text|true|",
		"finding_change_revision|bigint|true|",
		"outcome|text|true|",
		"delivery_id|text|false|",
		"created_at|timestamp with time zone|true|",
	},
	"notification_policy_events/constraints": {
		"app_notification_policy_events_delivery_fkey|FOREIGN KEY (workspace_id, delivery_id) REFERENCES app_finding_deliveries(workspace_id, id)",
		"app_notification_policy_events_delivery_semantics_check|CHECK ((outcome = ANY (ARRAY['queued'::text, 'duplicate-ticket'::text])) AND delivery_id IS NOT NULL OR (outcome = ANY (ARRAY['connection-stale'::text, 'invalid-payload'::text])) AND delivery_id IS NULL)",
		"app_notification_policy_events_finding_change_revision_check|CHECK (finding_change_revision > 0)",
		"app_notification_policy_events_finding_fkey|FOREIGN KEY (workspace_id, finding_id) REFERENCES app_findings(workspace_id, id) ON DELETE CASCADE",
		"app_notification_policy_events_id_check|CHECK (id ~ '^[0-9a-f]{32}$'::text)",
		"app_notification_policy_events_outcome_check|CHECK (outcome = ANY (ARRAY['queued'::text, 'duplicate-ticket'::text, 'connection-stale'::text, 'invalid-payload'::text]))",
		"app_notification_policy_events_pkey|PRIMARY KEY (id)",
		"app_notification_policy_events_policy_finding_revision_key|UNIQUE (workspace_id, policy_id, finding_id, finding_change_revision)",
		"app_notification_policy_events_policy_fkey|FOREIGN KEY (workspace_id, policy_id) REFERENCES app_notification_policies(workspace_id, id)",
		"app_notification_policy_events_policy_revision_check|CHECK (policy_revision > 0)",
		"app_notification_policy_events_workspace_id_key|UNIQUE (workspace_id, id)",
	},
	"notification_policy_events/indexes": {
		"app_notification_policy_events_pkey|CREATE UNIQUE INDEX app_notification_policy_events_pkey ON app_notification_policy_events USING btree (id)",
		"app_notification_policy_events_policy_finding_revision_key|CREATE UNIQUE INDEX app_notification_policy_events_policy_finding_revision_key ON app_notification_policy_events USING btree (workspace_id, policy_id, finding_id, finding_change_revision)",
		"app_notification_policy_events_policy_idx|CREATE INDEX app_notification_policy_events_policy_idx ON app_notification_policy_events USING btree (workspace_id, policy_id, id)",
		"app_notification_policy_events_workspace_id_key|CREATE UNIQUE INDEX app_notification_policy_events_workspace_id_key ON app_notification_policy_events USING btree (workspace_id, id)",
	},
	"jira_finding_effects/columns": {
		"workspace_id|text|true|",
		"connection_id|text|true|",
		"finding_id|text|true|",
		"delivery_id|text|true|",
		"created_at|timestamp with time zone|true|",
	},
	"jira_finding_effects/constraints": {
		"app_jira_finding_effects_connection_fkey|FOREIGN KEY (workspace_id, connection_id) REFERENCES app_integration_connections(workspace_id, id)",
		"app_jira_finding_effects_delivery_fkey|FOREIGN KEY (workspace_id, delivery_id) REFERENCES app_finding_deliveries(workspace_id, id)",
		"app_jira_finding_effects_delivery_key|UNIQUE (workspace_id, delivery_id)",
		"app_jira_finding_effects_finding_fkey|FOREIGN KEY (workspace_id, finding_id) REFERENCES app_findings(workspace_id, id) ON DELETE CASCADE",
		"app_jira_finding_effects_pkey|PRIMARY KEY (workspace_id, connection_id, finding_id)",
	},
	"jira_finding_effects/indexes": {
		"app_jira_finding_effects_delivery_key|CREATE UNIQUE INDEX app_jira_finding_effects_delivery_key ON app_jira_finding_effects USING btree (workspace_id, delivery_id)",
		"app_jira_finding_effects_pkey|CREATE UNIQUE INDEX app_jira_finding_effects_pkey ON app_jira_finding_effects USING btree (workspace_id, connection_id, finding_id)",
	},
}

// ExpectedV21Catalog returns an isolated copy for focused diagnostic comparisons.
func ExpectedV21Catalog() map[string][]string { return clone(exactV21Catalog) }

// ValidateV21Catalog requires the complete exact bounded V21 table catalog.
func ValidateV21Catalog(t testing.TB, current map[string][]string) {
	t.Helper()
	if !reflect.DeepEqual(current, exactV21Catalog) {
		t.Fatal("V21: new table columns, constraints or indexes contain a missing or unapproved delta")
	}
}
