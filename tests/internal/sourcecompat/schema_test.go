//go:build integration

package sourcecompat

import (
	"reflect"
	"slices"
	"testing"
)

func observedV11Catalog() map[string][]string {
	return map[string][]string{
		"source_connections/columns": {"id|text|false|"},
		"source_connections/constraints": {
			"app_source_connections_profile_check|" + LegacyProfileCheck,
		},
		"source_collections/columns": {"id|text|false|"},
		"source_collections/constraints": {
			"app_source_collections_profile_check|" + LegacyProfileCheck,
		},
		"source_repository_assets/columns": {"workspace_id|text|false|"},
		"source_repository_assets/constraints": {
			"app_source_repository_assets_profile_check|" + LegacyProfileCheck,
		},
		"source_collection_records/columns": {"id|text|false|"},
		"source_collection_records/constraints": {
			"app_source_collection_records_kind_check|" + LegacyKindCheck,
		},
	}
}

func TestProjectV12AcceptsOnlyTheExactAdditiveCatalog(t *testing.T) {
	before := observedV11Catalog()
	current, err := expected(before)
	if err != nil {
		t.Fatal(err)
	}
	if got := ProjectV12(t, before, current); !reflect.DeepEqual(got, before) {
		t.Fatal("exact V12 projection did not restore the observed V11 catalog")
	}
	if !reflect.DeepEqual(current["source_connections/columns"],
		[]string{"id|text|false|", "azure_devops_target|jsonb|false|"}) {
		t.Fatal("connection target column was not appended exactly")
	}
	if !reflect.DeepEqual(current["source_collections/columns"],
		[]string{"id|text|false|", "azure_devops_target|jsonb|false|", "azure_devops_selection|jsonb|false|"}) {
		t.Fatal("collection target/selection columns were not appended in order")
	}
}

func TestExpectedV12RejectsMissingOrAmbiguousObservedControls(t *testing.T) {
	for _, mutate := range []func(map[string][]string){
		func(value map[string][]string) {
			value["source_connections/constraints"] = nil
		},
		func(value map[string][]string) {
			value["source_collections/constraints"] = append(value["source_collections/constraints"],
				"app_source_collections_profile_check|"+LegacyProfileCheck)
		},
		func(value map[string][]string) {
			value["source_repository_assets/columns"] = []string{"unsupported"}
		},
	} {
		value := observedV11Catalog()
		mutate(value)
		if _, err := expected(value); err == nil {
			t.Fatal("invalid observed catalog was accepted")
		}
	}
}

func TestProjectRowsV12PreservesCompleteHistoricalRows(t *testing.T) {
	before := map[string][]string{
		"source_connections": {`{"id":"source","profile":"github-cloud-app"}`},
		"source_collections": {`{"id":"collection","profile":"github-cloud-app"}`},
		"assets":             {`{"id":"asset"}`},
	}
	current := map[string][]string{
		"source_connections": {`{"azure_devops_target":null,"id":"source","profile":"github-cloud-app"}`},
		"source_collections": {`{"azure_devops_selection":null,"azure_devops_target":null,"id":"collection","profile":"github-cloud-app"}`},
		"assets":             {`{"id":"asset"}`},
	}
	if got := ProjectRowsV12(t, before, current); !reflect.DeepEqual(got, before) {
		t.Fatal("complete historical rows were not projected without loss")
	}
}

func TestProjectCurrentAcceptsExactV12AndV13Composition(t *testing.T) {
	before := observedV11Catalog()
	before["findings/columns"] = []string{"id|text|true|"}
	before["findings/constraints"] = []string{
		"app_findings_pkey|PRIMARY KEY (id)",
		"app_findings_disposition_check|" + LegacyDispositionCheck,
		"app_findings_workflow_state_check|" + LegacyWorkflowStateCheck,
	}
	v12, err := expected(before)
	if err != nil {
		t.Fatal(err)
	}
	current, err := expectedV13(v12)
	if err != nil {
		t.Fatal(err)
	}
	current["findings/columns"] = append(current["findings/columns"],
		"candidate_uri|text|true|''::text",
		"candidate_line|integer|true|0")
	current["findings/constraints"] = append(current["findings/constraints"],
		"app_findings_candidate_line_check|CHECK (candidate_line >= 0 AND candidate_line <= 2147483647)",
		"app_findings_candidate_line_not_null|NOT NULL candidate_line",
		"app_findings_candidate_location_check|CHECK (candidate_uri = ''::text AND candidate_line = 0 OR candidate_uri <> ''::text AND candidate_line > 0)",
		"app_findings_candidate_uri_check|CHECK (octet_length(candidate_uri) <= 8192)",
		"app_findings_candidate_uri_not_null|NOT NULL candidate_uri")
	current["findings/columns"] = append(current["findings/columns"],
		"content_digest|text|true|''::text",
		"change_kind|text|true|'unchanged'::text",
		"change_at|timestamp with time zone|false|",
		"change_run_id|text|true|''::text",
		"change_revision|bigint|true|1")
	current["findings/constraints"] = append(current["findings/constraints"],
		"app_findings_change_kind_check|CHECK (change_kind = ANY (ARRAY['new'::text, 'changed'::text, 'unchanged'::text, 'reopened'::text, 'inferred-resolved'::text]))",
		"app_findings_change_kind_not_null|NOT NULL change_kind",
		"app_findings_change_revision_check|CHECK (change_revision > 0)",
		"app_findings_change_revision_not_null|NOT NULL change_revision",
		"app_findings_change_run_id_check|CHECK (change_run_id = ''::text OR change_run_id ~ '^[0-9a-f]{32}$'::text)",
		"app_findings_change_run_id_not_null|NOT NULL change_run_id",
		"app_findings_content_digest_check|CHECK (content_digest = ''::text OR content_digest ~ '^sha256:[0-9a-f]{64}$'::text)",
		"app_findings_content_digest_not_null|NOT NULL content_digest")
	for i, row := range current["findings/constraints"] {
		if row == "app_findings_disposition_check|"+LegacyDispositionCheck {
			current["findings/constraints"][i] = "app_findings_disposition_check|" + DispositionCheck
		}
		if row == "app_findings_workflow_state_check|"+LegacyWorkflowStateCheck {
			current["findings/constraints"][i] = "app_findings_workflow_state_check|" + WorkflowStateCheck
		}
	}
	sortConstraints(current["findings/constraints"])
	if got := ProjectCurrent(t, before, current); !reflect.DeepEqual(got, before) {
		t.Fatal("current projection did not restore the observed legacy catalog")
	}
}

func TestExpectedV13RejectsMissingOrAmbiguousFindingCatalog(t *testing.T) {
	for _, value := range []map[string][]string{
		{"findings/columns": {}, "findings/constraints": {"app_findings_pkey|PRIMARY KEY (id)"}},
		{"findings/columns": {"unsupported"}, "findings/constraints": {"app_findings_pkey|PRIMARY KEY (id)"}},
	} {
		if _, err := expectedV13(value); err == nil {
			t.Fatal("invalid observed V13 finding catalog was accepted")
		}
	}
}

func TestValidateV21CatalogAcceptsOnlyTheDeclaredNewTables(t *testing.T) {
	current := clone(exactV21Catalog)
	ValidateV21Catalog(t, current)
	if !reflect.DeepEqual(V21Tables(), []string{
		"notification_policies", "notification_policy_revisions", "finding_change_events",
		"notification_policy_events", "jira_finding_effects",
	}) {
		t.Fatal("V21 table enumeration changed")
	}
}

func TestValidateV22CatalogWidensOnlyNotificationConnectionProfiles(t *testing.T) {
	current := clone(exactV22Catalog)
	ValidateV22Catalog(t, current)
	if !reflect.DeepEqual(V22Tables(), V21Tables()) {
		t.Fatal("V22 unexpectedly added or removed a notification-policy relation")
	}
	for _, table := range []string{"notification_policies", "notification_policy_revisions"} {
		key := table + "/constraints"
		if slices.Contains(current[key],
			"app_"+table+"_connection_profile_check|"+V21NotificationProfileCheck) ||
			!slices.Contains(current[key],
				"app_"+table+"_connection_profile_check|"+V22NotificationProfileCheck) {
			t.Fatalf("V22 %s profile CHECK was not widened exactly", table)
		}
	}
}

func TestProjectV22AcceptsOnlyExactWebhookColumnsAndChecks(t *testing.T) {
	before := map[string][]string{
		"integration_connections/columns": {
			"id|text|true|", "jira_target|jsonb|false|", "teams_target|jsonb|false|",
		},
		"integration_connections/constraints": {
			"app_integration_connections_profile_check|" + V21DeliveryProfileCheck,
			"app_integration_connections_profile_target_check|" + V21DeliveryTargetCheck,
		},
		"finding_deliveries/columns": {
			"id|text|true|", "jira_target|jsonb|false|", "create_attempted_at|timestamp with time zone|false|",
			"teams_target|jsonb|false|", "trigger_kind|text|true|'manual'::text",
			"policy_id|text|false|", "policy_revision|bigint|false|", "finding_change_revision|bigint|false|",
		},
		"finding_deliveries/constraints": {
			"app_finding_deliveries_profile_check|" + V21DeliveryProfileCheck,
			"app_finding_deliveries_profile_target_check|" + V21DeliveryTargetCheck,
		},
		"notification_policies/constraints": {
			"app_notification_policies_connection_profile_check|" + V21NotificationProfileCheck,
		},
		"notification_policy_revisions/constraints": {
			"app_notification_policy_revisions_connection_profile_check|" + V21NotificationProfileCheck,
		},
	}
	current := clone(before)
	for _, table := range []string{"integration_connections", "finding_deliveries"} {
		current[table+"/columns"] = append(current[table+"/columns"], "webhook_target|jsonb|false|")
		current[table+"/constraints"] = []string{
			"app_" + table + "_profile_check|" + V22DeliveryProfileCheck,
			"app_" + table + "_profile_target_check|" + V22DeliveryTargetCheck,
		}
	}
	for _, table := range []string{"notification_policies", "notification_policy_revisions"} {
		current[table+"/constraints"] = []string{
			"app_" + table + "_connection_profile_check|" + V22NotificationProfileCheck,
		}
	}
	if got := ProjectV22(t, before, current); !reflect.DeepEqual(got, before) {
		t.Fatal("exact V22 projection did not restore the V21 catalog")
	}
}

func TestProjectV21AcceptsOnlyExactLegacyNotificationPolicyDelta(t *testing.T) {
	before := map[string][]string{
		"workspaces/columns":             {"id|text|true|"},
		"workspaces/constraints":         {"app_workspaces_pkey|PRIMARY KEY (id)"},
		"finding_deliveries/columns":     {"id|text|true|"},
		"finding_deliveries/constraints": {"app_finding_deliveries_pkey|PRIMARY KEY (id)"},
		"finding_deliveries/indexes":     {"app_finding_deliveries_pkey|unchanged"},
	}
	current := clone(before)
	current["workspaces/columns"] = append(current["workspaces/columns"],
		"notification_policy_epoch|bigint|true|0")
	current["workspaces/constraints"] = append(current["workspaces/constraints"],
		"app_workspaces_notification_policy_epoch_check|CHECK (notification_policy_epoch >= 0)")
	sortConstraints(current["workspaces/constraints"])
	current["finding_deliveries/columns"] = append(current["finding_deliveries/columns"],
		"trigger_kind|text|true|'manual'::text", "policy_id|text|false|",
		"policy_revision|bigint|false|", "finding_change_revision|bigint|false|")
	current["finding_deliveries/constraints"] = append(current["finding_deliveries/constraints"],
		"app_finding_deliveries_policy_binding_check|CHECK (trigger_kind = 'manual'::text AND policy_id IS NULL AND policy_revision IS NULL AND finding_change_revision IS NULL OR trigger_kind = 'notification-policy'::text AND policy_id IS NOT NULL AND policy_revision IS NOT NULL AND policy_revision > 0 AND finding_change_revision IS NOT NULL AND finding_change_revision > 0)",
		"app_finding_deliveries_trigger_kind_check|CHECK (trigger_kind = ANY (ARRAY['manual'::text, 'notification-policy'::text]))")
	sortConstraints(current["finding_deliveries/constraints"])
	current["finding_deliveries/indexes"] = append(current["finding_deliveries/indexes"],
		"app_finding_deliveries_policy_effect_key|CREATE UNIQUE INDEX app_finding_deliveries_policy_effect_key ON acceptance_schema.app_finding_deliveries USING btree (workspace_id, policy_id, finding_id, finding_change_revision) WHERE (trigger_kind = 'notification-policy'::text)")
	if got := ProjectV21(t, before, current); !reflect.DeepEqual(got, before) {
		t.Fatal("exact qualified V21 legacy-table projection did not restore the prior catalog")
	}
	stripped := clone(current)
	stripped["finding_deliveries/indexes"][1] =
		"app_finding_deliveries_policy_effect_key|CREATE UNIQUE INDEX app_finding_deliveries_policy_effect_key ON app_finding_deliveries USING btree (workspace_id, policy_id, finding_id, finding_change_revision) WHERE (trigger_kind = 'notification-policy'::text)"
	if got := ProjectV21(t, before, stripped); !reflect.DeepEqual(got, before) {
		t.Fatal("exact schema-stripped V21 legacy-table projection did not restore the prior catalog")
	}
}

func TestProjectV21ProjectsOnlyExactPG18TypedNotNullAdditions(t *testing.T) {
	before := map[string][]string{
		"workspaces/columns": {"id|text|true|||"},
		"workspaces/constraints": {
			"app_workspaces_id_not_null|n|NOT NULL id",
			"app_workspaces_pkey|p|PRIMARY KEY (id)",
		},
		"finding_deliveries/columns": {"id|text|true|||"},
		"finding_deliveries/constraints": {
			"app_finding_deliveries_id_not_null|n|NOT NULL id",
			"app_finding_deliveries_pkey|p|PRIMARY KEY (id)",
		},
		"finding_deliveries/indexes": {"app_finding_deliveries_pkey|unchanged"},
	}
	current := clone(before)
	current["workspaces/columns"] = append(current["workspaces/columns"],
		"notification_policy_epoch|bigint|true|0||")
	current["workspaces/constraints"] = append(current["workspaces/constraints"],
		"app_workspaces_notification_policy_epoch_check|c|CHECK (notification_policy_epoch >= 0)",
		"app_workspaces_notification_policy_epoch_not_null|n|NOT NULL notification_policy_epoch")
	sortConstraints(current["workspaces/constraints"])
	current["finding_deliveries/columns"] = append(current["finding_deliveries/columns"],
		"trigger_kind|text|true|'manual'::text||", "policy_id|text|false|||",
		"policy_revision|bigint|false|||", "finding_change_revision|bigint|false|||")
	current["finding_deliveries/constraints"] = append(current["finding_deliveries/constraints"],
		"app_finding_deliveries_policy_binding_check|c|CHECK (trigger_kind = 'manual'::text AND policy_id IS NULL AND policy_revision IS NULL AND finding_change_revision IS NULL OR trigger_kind = 'notification-policy'::text AND policy_id IS NOT NULL AND policy_revision IS NOT NULL AND policy_revision > 0 AND finding_change_revision IS NOT NULL AND finding_change_revision > 0)",
		"app_finding_deliveries_trigger_kind_check|c|CHECK (trigger_kind = ANY (ARRAY['manual'::text, 'notification-policy'::text]))",
		"app_finding_deliveries_trigger_kind_not_null|n|NOT NULL trigger_kind")
	sortConstraints(current["finding_deliveries/constraints"])
	current["finding_deliveries/indexes"] = append(current["finding_deliveries/indexes"],
		"app_finding_deliveries_policy_effect_key|CREATE UNIQUE INDEX app_finding_deliveries_policy_effect_key ON app_finding_deliveries USING btree (workspace_id, policy_id, finding_id, finding_change_revision) WHERE (trigger_kind = 'notification-policy'::text)")
	if got := ProjectV21(t, before, current); !reflect.DeepEqual(got, before) {
		t.Fatal("exact PG18 typed NOT NULL projection did not preserve the prior catalog")
	}
}

func TestProjectRelationsV13AcceptsOnlyExactCorrelationRelations(t *testing.T) {
	before := []string{"app_findings|r", "app_findings_pkey|i"}
	current := append(slices.Clone(before), v13Relations...)
	slices.Sort(current)
	slices.Reverse(current)
	if got := ProjectRelationsV13(t, before, current); !reflect.DeepEqual(got, before) {
		t.Fatal("exact V13 relation projection did not restore the prior relation set")
	}
}

func TestProjectRelationsV14AcceptsOnlyExactRetentionRelations(t *testing.T) {
	before := []string{"app_findings|r", "app_findings_pkey|i"}
	current := append(slices.Clone(before), v13Relations...)
	current = append(current, v14Relations...)
	slices.Sort(current)
	slices.Reverse(current)
	if got := ProjectRelationsV14(t, before, current); !reflect.DeepEqual(got, before) {
		t.Fatal("exact V14 relation projection did not restore the prior relation set")
	}
}

func TestProjectRelationsV15AcceptsOnlyExactExecutionRelations(t *testing.T) {
	before := []string{"app_findings|r", "app_findings_pkey|i"}
	current := append(slices.Clone(before), v13Relations...)
	current = append(current, v14Relations...)
	current = append(current, v15Relations...)
	slices.Sort(current)
	slices.Reverse(current)
	if got := ProjectRelationsV15(t, before, current); !reflect.DeepEqual(got, before) {
		t.Fatal("exact V15 relation projection did not restore the prior relation set")
	}
}

func TestProjectRelationsV16AcceptsOnlyExactCandidateIndex(t *testing.T) {
	before := []string{"app_findings|r", "app_findings_pkey|i"}
	current := append(slices.Clone(before), v13Relations...)
	current = append(current, v14Relations...)
	current = append(current, v15Relations...)
	current = append(current, v16Relations...)
	slices.Sort(current)
	slices.Reverse(current)
	if got := ProjectRelationsV16(t, before, current); !reflect.DeepEqual(got, before) {
		t.Fatal("exact V16 relation projection did not restore the prior relation set")
	}
}

func TestProjectRelationsV17AcceptsOnlyExactLifecycleIndex(t *testing.T) {
	before := []string{"app_findings|r", "app_findings_pkey|i"}
	current := append(slices.Clone(before), v13Relations...)
	current = append(current, v14Relations...)
	current = append(current, v15Relations...)
	current = append(current, v16Relations...)
	current = append(current, v17Relations...)
	slices.Sort(current)
	slices.Reverse(current)
	if got := ProjectRelationsV17(t, before, current); !reflect.DeepEqual(got, before) {
		t.Fatal("exact V17 relation projection did not restore the prior relation set")
	}
}

func TestProjectRelationsV18AcceptsOnlyExactPublicationRelations(t *testing.T) {
	before := []string{"app_findings|r", "app_findings_pkey|i"}
	current := append(slices.Clone(before), v13Relations...)
	current = append(current, v14Relations...)
	current = append(current, v15Relations...)
	current = append(current, v16Relations...)
	current = append(current, v17Relations...)
	current = append(current, v18Relations...)
	slices.Sort(current)
	slices.Reverse(current)
	if got := ProjectRelationsV18(t, before, current); !reflect.DeepEqual(got, before) {
		t.Fatal("exact V18 relation projection did not restore the prior relation set")
	}
}

func TestProjectRelationsV19AcceptsOnlyExactDecisionHistoryRelations(t *testing.T) {
	before := []string{"app_findings|r", "app_findings_pkey|i"}
	current := append(slices.Clone(before), v13Relations...)
	current = append(current, v14Relations...)
	current = append(current, v15Relations...)
	current = append(current, v16Relations...)
	current = append(current, v17Relations...)
	current = append(current, v18Relations...)
	current = append(current, v19Relations...)
	slices.Sort(current)
	slices.Reverse(current)
	if got := ProjectRelationsV19(t, before, current); !reflect.DeepEqual(got, before) {
		t.Fatal("exact V19 relation projection did not restore the prior relation set")
	}
}

func TestProjectRelationsV20AcceptsOnlyExactDispositionApprovalRelations(t *testing.T) {
	before := []string{"app_findings|r", "app_findings_pkey|i"}
	current := append(slices.Clone(before), v13Relations...)
	current = append(current, v14Relations...)
	current = append(current, v15Relations...)
	current = append(current, v16Relations...)
	current = append(current, v17Relations...)
	current = append(current, v18Relations...)
	current = append(current, v19Relations...)
	current = append(current, v20Relations...)
	slices.Sort(current)
	slices.Reverse(current)
	if got := ProjectRelationsV20(t, before, current); !reflect.DeepEqual(got, before) {
		t.Fatal("exact V20 relation projection did not restore the prior relation set")
	}
}

func TestProjectRelationsV21AcceptsOnlyExactNotificationPolicyRelations(t *testing.T) {
	before := []string{"app_findings|r", "app_findings_pkey|i"}
	current := append(slices.Clone(before), v13Relations...)
	current = append(current, v14Relations...)
	current = append(current, v15Relations...)
	current = append(current, v16Relations...)
	current = append(current, v17Relations...)
	current = append(current, v18Relations...)
	current = append(current, v19Relations...)
	current = append(current, v20Relations...)
	current = append(current, v21Relations...)
	slices.Sort(current)
	slices.Reverse(current)
	if got := ProjectRelationsV21(t, before, current); !reflect.DeepEqual(got, before) {
		t.Fatal("exact V21 relation projection did not restore the prior relation set")
	}
}

func TestProjectRelationsV22RequiresNoNewRelations(t *testing.T) {
	before := []string{"app_findings|r", "app_findings_pkey|i"}
	current := append(slices.Clone(before), v13Relations...)
	current = append(current, v14Relations...)
	current = append(current, v15Relations...)
	current = append(current, v16Relations...)
	current = append(current, v17Relations...)
	current = append(current, v18Relations...)
	current = append(current, v19Relations...)
	current = append(current, v20Relations...)
	current = append(current, v21Relations...)
	slices.Sort(current)
	slices.Reverse(current)
	if got := ProjectRelationsV22(t, before, current); !reflect.DeepEqual(got, before) {
		t.Fatal("V22 relation projection did not preserve the prior relation set")
	}
}

func TestProjectV14AcceptsOnlyExactLegacyRetentionIndexes(t *testing.T) {
	before := map[string][]string{
		"imports/indexes":             {"app_imports_pkey|unchanged"},
		"observations/indexes":        {"app_observations_pkey|unchanged"},
		"assessment_previews/indexes": {"app_assessment_previews_pkey|unchanged"},
	}
	current := clone(before)
	for _, spec := range v14LegacyIndexes {
		key := spec.table + "/indexes"
		current[key] = append(current[key], spec.name+"|CREATE INDEX "+spec.name+
			" ON acceptance_schema."+spec.definition)
		slices.Sort(current[key])
	}
	if got := ProjectV14(t, before, current); !reflect.DeepEqual(got, before) {
		t.Fatal("exact V14 index projection did not restore the prior catalog")
	}
}

func TestProjectRowsCurrentAddsOnlyExactV12AndV13Defaults(t *testing.T) {
	before := map[string][]string{
		"source_connections":      {`{"id":"source","profile":"github-cloud-app"}`},
		"source_collections":      {`{"id":"collection","profile":"github-cloud-app"}`},
		"workspaces":              {`{"id":"workspace"}`},
		"integration_connections": {`{"id":"connection","profile":"slack-workspace-bot"}`},
		"finding_deliveries":      {`{"id":"delivery"}`},
		"findings":                {`{"id":"finding","workflow_state":"open"}`},
	}
	current := map[string][]string{
		"source_connections":      {`{"azure_devops_target":null,"id":"source","profile":"github-cloud-app"}`},
		"source_collections":      {`{"azure_devops_selection":null,"azure_devops_target":null,"id":"collection","profile":"github-cloud-app"}`},
		"workspaces":              {`{"id":"workspace","notification_policy_epoch":0}`},
		"integration_connections": {`{"id":"connection","profile":"slack-workspace-bot","webhook_target":null}`},
		"finding_deliveries":      {`{"finding_change_revision":null,"id":"delivery","policy_id":null,"policy_revision":null,"trigger_kind":"manual","webhook_target":null}`},
		"findings":                {`{"candidate_line":0,"candidate_uri":"","change_at":null,"change_kind":"unchanged","change_revision":1,"change_run_id":"","content_digest":"","decision_revision":1,"evidence_revision":1,"id":"finding","workflow_state":"open"}`},
	}
	if got := ProjectRowsCurrent(t, before, current); !reflect.DeepEqual(got, before) {
		t.Fatal("complete current rows were not projected without loss")
	}
}
