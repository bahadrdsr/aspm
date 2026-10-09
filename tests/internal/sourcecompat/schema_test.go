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
		"change_revision|bigint|true|1",
		"first_observed_at|timestamp with time zone|true|")
	current["findings/indexes"] = []string{
		v25FindingIndexName + "|CREATE INDEX " + v25FindingIndexName + " ON " + v25FindingIndexDefinition,
	}
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

func TestProjectV23AcceptsOnlyExactHistoryRetentionChecksAndIndexes(t *testing.T) {
	before := map[string][]string{
		"retention_holds/columns": {"id|text|true|", "resource_kind|text|true|"},
		"retention_holds/constraints": {
			"app_retention_holds_pkey|PRIMARY KEY (id)",
			"app_retention_holds_resource_kind_check|" + V22RetentionHoldResourceKindCheck,
		},
		"retention_holds/indexes":         {"app_retention_holds_pkey|unchanged"},
		"retention_preview_items/columns": {"preview_id|text|true|", "resource_kind|text|true|"},
		"retention_preview_items/constraints": {
			"app_retention_preview_items_pkey|PRIMARY KEY (preview_id)",
			"app_retention_preview_items_resource_kind_check|" + V22RetentionPreviewResourceKindCheck,
		},
		"retention_preview_items/indexes": {"app_retention_preview_items_pkey|unchanged"},
	}
	for _, spec := range v23IndexSpecs {
		before[spec.table+"/columns"] = []string{"id|text|true|"}
		before[spec.table+"/constraints"] = []string{"app_" + spec.table + "_pkey|PRIMARY KEY (id)"}
		before[spec.table+"/indexes"] = []string{"app_" + spec.table + "_pkey|unchanged"}
	}
	current := clone(before)
	current["retention_holds/constraints"][1] =
		"app_retention_holds_resource_kind_check|" + V23RetentionHoldResourceKindCheck
	current["retention_preview_items/constraints"][1] =
		"app_retention_preview_items_resource_kind_check|" + V23RetentionPreviewResourceKindCheck
	for _, spec := range v23IndexSpecs {
		key := spec.table + "/indexes"
		current[key] = append(current[key], spec.name+"|CREATE INDEX "+spec.name+
			" ON acceptance_schema."+spec.definition)
		slices.Sort(current[key])
	}
	ValidateV23Catalog(t, before, current)
	if got := ProjectV23(t, before, current); !reflect.DeepEqual(got, before) {
		t.Fatal("exact V23 projection did not restore the V22 catalog")
	}
	if !reflect.DeepEqual(V23Tables(), []string{
		"retention_holds", "retention_preview_items", "finding_decision_events",
		"notification_policy_revisions", "finding_change_events", "notification_policy_events",
	}) {
		t.Fatal("V23 catalog table enumeration changed")
	}
	if slices.Contains(V23Tables(), "retention_run_items") {
		t.Fatal("V23 must not widen or otherwise catalog retention run items")
	}
}

func TestProjectV23RejectsUnapprovedHistoryRetentionDeltas(t *testing.T) {
	before := map[string][]string{
		"retention_holds/columns": {"id|text|true|", "resource_kind|text|true|"},
		"retention_holds/constraints": {
			"app_retention_holds_resource_kind_check|" + V22RetentionHoldResourceKindCheck,
		},
		"retention_holds/indexes":           {"app_retention_holds_pkey|unchanged"},
		"finding_change_events/columns":     {"id|text|true|"},
		"finding_change_events/constraints": {"app_finding_change_events_pkey|PRIMARY KEY (id)"},
		"finding_change_events/indexes":     {"app_finding_change_events_pkey|unchanged"},
	}
	valid := clone(before)
	valid["retention_holds/constraints"][0] =
		"app_retention_holds_resource_kind_check|" + V23RetentionHoldResourceKindCheck
	spec := v23IndexSpecs[2]
	valid["finding_change_events/indexes"] = append(valid["finding_change_events/indexes"],
		spec.name+"|CREATE INDEX "+spec.name+" ON acceptance_schema."+spec.definition)
	for _, mutate := range []func(map[string][]string){
		func(value map[string][]string) {
			value["retention_holds/columns"] = append(value["retention_holds/columns"], "archive_key|text|false|")
		},
		func(value map[string][]string) {
			value["retention_holds/constraints"][0] =
				"app_retention_holds_resource_kind_check|" + V22RetentionHoldResourceKindCheck
		},
		func(value map[string][]string) {
			value["finding_change_events/indexes"][1] = spec.name + "|CREATE INDEX " + spec.name +
				" ON acceptance_schema.app_finding_change_events USING btree (change_at, id)"
		},
		func(value map[string][]string) {
			value["finding_change_events/indexes"] = append(value["finding_change_events/indexes"],
				"app_finding_change_events_unapproved_idx|unexpected")
		},
	} {
		current := clone(valid)
		mutate(current)
		if _, err := projectV23(before, current); err == nil {
			t.Fatal("V23 accepted an unapproved history-retention catalog delta")
		}
	}
}

func TestValidateCurrentCatalogComposesV22AndV23Exactly(t *testing.T) {
	current := clone(exactV22Catalog)
	for key, rows := range exactV22HistoryRetentionCatalog {
		current[key] = slices.Clone(rows)
	}
	for index, row := range current["retention_holds/constraints"] {
		if row == "app_retention_holds_resource_kind_check|"+V22RetentionHoldResourceKindCheck {
			current["retention_holds/constraints"][index] =
				"app_retention_holds_resource_kind_check|" + V23RetentionHoldResourceKindCheck
		}
	}
	for index, row := range current["retention_preview_items/constraints"] {
		if row == "app_retention_preview_items_resource_kind_check|"+V22RetentionPreviewResourceKindCheck {
			current["retention_preview_items/constraints"][index] =
				"app_retention_preview_items_resource_kind_check|" + V23RetentionPreviewResourceKindCheck
		}
	}
	for _, spec := range v23IndexSpecs {
		key := spec.table + "/indexes"
		current[key] = append(current[key], spec.name+"|CREATE INDEX "+spec.name+
			" ON "+spec.definition)
		slices.Sort(current[key])
	}
	expected := ExpectedV23Catalog()
	if !reflect.DeepEqual(current, expected) {
		for key, rows := range expected {
			if !reflect.DeepEqual(current[key], rows) {
				t.Fatalf("synthetic V23 catalog drifted at %s: got=%q want=%q", key, current[key], rows)
			}
		}
		t.Fatal("synthetic V23 current catalog fixture drifted from the exact expected catalog")
	}
	ValidateV23CurrentCatalog(t, current)
	prior := ProjectV23Current(t, current)
	if !reflect.DeepEqual(catalogSubset(prior, v21Tables), exactV22Catalog) {
		t.Fatal("V23 current catalog did not project to the exact V22 notification-policy catalog")
	}
}

func TestProjectV24AcceptsOnlyExactHistoryArchiveColumnsAndChecks(t *testing.T) {
	before := clone(exactV23CurrentCatalogForV24)
	current, err := expectedV24Catalog(before)
	if err != nil {
		t.Fatal(err)
	}
	ValidateV24Catalog(t, before, current)
	if got := ProjectV24(t, before, current); !reflect.DeepEqual(got, before) {
		t.Fatal("exact V24 projection did not restore the V23 catalog")
	}
	wantTail := []string{
		"detail_availability|text|true|'available'::text",
		"detail_revision|bigint|true|1",
		"archive_key|text|false|",
		"archive_digest|text|false|",
		"archive_size|bigint|false|",
		"archived_at|timestamp with time zone|false|",
	}
	for _, table := range v24HistoryTables {
		columns := current[table+"/columns"]
		if len(columns) < len(wantTail) ||
			!reflect.DeepEqual(columns[len(columns)-len(wantTail):], wantTail) {
			t.Fatalf("V24 %s archive columns were missing or reordered", table)
		}
		if !reflect.DeepEqual(current[table+"/indexes"], before[table+"/indexes"]) {
			t.Fatalf("V24 added an unapproved %s index", table)
		}
		for _, value := range []struct{ name, definition string }{
			{"app_" + table + "_archive_reference_check", V24ArchiveReferenceCheck},
			{"app_" + table + "_detail_availability_check", V24DetailAvailabilityCheck},
			{"app_" + table + "_detail_revision_check", V24DetailRevisionCheck},
		} {
			if !slices.Contains(current[table+"/constraints"], value.name+"|"+value.definition) {
				t.Fatalf("V24 %s exact constraint %s is missing", table, value.name)
			}
		}
	}
	if !slices.Contains(current["retention_run_items/constraints"],
		"app_retention_run_items_resource_kind_check|"+V24RetentionRunItemResourceKindCheck) {
		t.Fatal("V24 did not widen the retention run item resource kinds exactly")
	}
	if !slices.Contains(current["archive_publications/constraints"],
		"app_archive_publications_resource_kind_check|"+V24ArchivePublicationResourceKindCheck) {
		t.Fatal("V24 did not widen the archive publication resource kinds exactly")
	}
	if !reflect.DeepEqual(V24Tables(), []string{
		"finding_decision_events", "notification_policy_revisions",
		"finding_change_events", "notification_policy_events",
		"retention_run_items", "archive_publications",
	}) {
		t.Fatal("V24 touched-table enumeration changed")
	}
}

func TestProjectV24RejectsUnapprovedHistoryArchiveDeltas(t *testing.T) {
	before := clone(exactV23CurrentCatalogForV24)
	valid, err := expectedV24Catalog(before)
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(map[string][]string){
		func(value map[string][]string) {
			columns := value["finding_decision_events/columns"]
			value["finding_decision_events/columns"] = append(
				slices.Clone(columns[:len(columns)-1]), "unapproved|text|false|")
		},
		func(value map[string][]string) {
			rows := value["notification_policy_revisions/constraints"]
			for index, row := range rows {
				if row == "app_notification_policy_revisions_archive_reference_check|"+V24ArchiveReferenceCheck {
					rows[index] = "app_notification_policy_revisions_archive_reference_check|CHECK (archive_key IS NULL)"
				}
			}
		},
		func(value map[string][]string) {
			value["finding_change_events/indexes"] = append(value["finding_change_events/indexes"],
				"app_finding_change_events_archive_idx|unexpected")
		},
		func(value map[string][]string) {
			rows := value["retention_run_items/constraints"]
			for index, row := range rows {
				if row == "app_retention_run_items_resource_kind_check|"+V24RetentionRunItemResourceKindCheck {
					rows[index] = "app_retention_run_items_resource_kind_check|" +
						V23RetentionRunItemResourceKindCheck
				}
			}
		},
		func(value map[string][]string) {
			value["archive_publications/columns"] = append(value["archive_publications/columns"],
				"unapproved|text|false|")
		},
	} {
		current := clone(valid)
		mutate(current)
		if _, err := projectV24(before, current); err == nil {
			t.Fatal("V24 accepted an unapproved history archive catalog delta")
		}
	}
}

func TestValidateV24CurrentCatalogComposesV23AndV24Exactly(t *testing.T) {
	current := ExpectedV24Catalog()
	ValidateV24CurrentCatalog(t, current)
	prior := ProjectV24Current(t, current)
	if !reflect.DeepEqual(prior, exactV23CurrentCatalogForV24) {
		t.Fatal("V24 current catalog did not project to the exact expanded V23 catalog")
	}
	v23 := map[string][]string{}
	for key := range exactV23Catalog {
		v23[key] = slices.Clone(prior[key])
	}
	ValidateV23CurrentCatalog(t, v23)
}

func TestProjectV25AcceptsOnlyExactSLAStorageAndFindingAgeDelta(t *testing.T) {
	before := ExpectedV24Catalog()
	before["findings/columns"] = []string{"id|text|true|", "imported_at|timestamp with time zone|true|"}
	before["findings/constraints"] = []string{"app_findings_pkey|PRIMARY KEY (id)"}
	before["findings/indexes"] = []string{
		"app_findings_pkey|CREATE UNIQUE INDEX app_findings_pkey ON app_findings USING btree (id)",
	}
	current, err := expectedV25Catalog(before)
	if err != nil {
		t.Fatal(err)
	}
	ValidateV25Catalog(t, before, current)
	if got := ProjectV25(t, before, current); !reflect.DeepEqual(got, before) {
		t.Fatal("exact V25 projection did not restore the V24 catalog")
	}
	if current["findings/columns"][len(current["findings/columns"])-1] !=
		"first_observed_at|timestamp with time zone|true|" {
		t.Fatal("V25 first_observed_at was missing, reordered or given a default")
	}
	if !slices.Contains(current["findings/indexes"],
		v25FindingIndexName+"|CREATE INDEX "+v25FindingIndexName+" ON "+v25FindingIndexDefinition) {
		t.Fatal("V25 exact current unresolved-finding candidate index is missing")
	}
	if !reflect.DeepEqual(V25Tables(), []string{
		"findings", "report_sla_policies", "report_sla_policy_revisions",
	}) {
		t.Fatal("V25 touched-table enumeration changed")
	}
	for key, rows := range exactV25NewTableCatalog {
		if !reflect.DeepEqual(current[key], rows) {
			t.Fatalf("V25 exact SLA catalog drifted at %s", key)
		}
	}
}

func TestProjectV25RejectsMissingOrUnapprovedSLADeltas(t *testing.T) {
	before := ExpectedV24Catalog()
	before["findings/columns"] = []string{"id|text|true|", "imported_at|timestamp with time zone|true|"}
	before["findings/constraints"] = []string{"app_findings_pkey|PRIMARY KEY (id)"}
	before["findings/indexes"] = []string{
		"app_findings_pkey|CREATE UNIQUE INDEX app_findings_pkey ON app_findings USING btree (id)",
	}
	valid, err := expectedV25Catalog(before)
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(map[string][]string){
		func(value map[string][]string) {
			value["findings/columns"][len(value["findings/columns"])-1] =
				"first_observed_at|timestamp with time zone|true|clock_timestamp()"
		},
		func(value map[string][]string) {
			value["findings/indexes"] = append(value["findings/indexes"],
				"app_findings_unapproved_sla_idx|unexpected")
		},
		func(value map[string][]string) {
			value["report_sla_policies/columns"][1] = "critical_days|integer|true|14"
		},
		func(value map[string][]string) {
			value["report_sla_policy_revisions/constraints"] =
				value["report_sla_policy_revisions/constraints"][:len(value["report_sla_policy_revisions/constraints"])-1]
		},
		func(value map[string][]string) {
			value["report_sla_policy_revisions/indexes"] = append(
				value["report_sla_policy_revisions/indexes"], "app_report_sla_policy_revisions_unapproved|unexpected")
		},
	} {
		current := clone(valid)
		mutate(current)
		projected, err := projectV25(before, current)
		if err == nil && reflect.DeepEqual(projected, before) {
			t.Fatal("V25 accepted a missing or unapproved SLA catalog delta")
		}
	}
}

func TestValidateV25CurrentCatalogComposesV24AndV25Exactly(t *testing.T) {
	before := ExpectedV24Catalog()
	before["findings/columns"] = []string{"id|text|true|", "imported_at|timestamp with time zone|true|"}
	before["findings/constraints"] = []string{"app_findings_pkey|PRIMARY KEY (id)"}
	before["findings/indexes"] = []string{
		"app_findings_pkey|CREATE UNIQUE INDEX app_findings_pkey ON app_findings USING btree (id)",
	}
	current, err := expectedV25Catalog(before)
	if err != nil {
		t.Fatal(err)
	}
	ValidateV25CurrentCatalog(t, current)
	prior := ProjectV25Current(t, current)
	if !reflect.DeepEqual(prior, before) {
		t.Fatal("V25 current catalog did not project to the exact supplied V24 shape")
	}
}

func TestProjectV26AcceptsOnlyExactBoundedReportExportStorage(t *testing.T) {
	before := map[string][]string{
		"report_snapshots/columns":     {"id|text|true|"},
		"report_snapshots/constraints": {"app_report_snapshots_pkey|PRIMARY KEY (id)"},
		"report_snapshots/indexes": {
			"app_report_snapshots_pkey|CREATE UNIQUE INDEX app_report_snapshots_pkey ON app_report_snapshots USING btree (id)",
		},
	}
	current, err := expectedV26Catalog(before)
	if err != nil {
		t.Fatal(err)
	}
	ValidateV26Catalog(t, before, current)
	if got := ProjectV26(t, before, current); !reflect.DeepEqual(got, before) {
		t.Fatal("exact V26 projection did not restore the V25 catalog")
	}
	if !reflect.DeepEqual(V26Tables(), []string{"report_exports"}) {
		t.Fatal("V26 touched-table enumeration changed")
	}
	for key, rows := range exactV26NewTableCatalog {
		if !reflect.DeepEqual(current[key], rows) {
			t.Fatalf("V26 exact report export catalog drifted at %s", key)
		}
	}
}

func TestProjectV26RejectsMissingOrUnapprovedExportDeltas(t *testing.T) {
	before := map[string][]string{
		"report_snapshots/columns":     {"id|text|true|"},
		"report_snapshots/constraints": {"app_report_snapshots_pkey|PRIMARY KEY (id)"},
		"report_snapshots/indexes": {
			"app_report_snapshots_pkey|CREATE UNIQUE INDEX app_report_snapshots_pkey ON app_report_snapshots USING btree (id)",
		},
	}
	valid, err := expectedV26Catalog(before)
	if err != nil {
		t.Fatal(err)
	}
	missing := clone(valid)
	for _, kind := range []string{"columns", "constraints", "indexes"} {
		delete(missing, "report_exports/"+kind)
	}
	if err := validateV26Catalog(before, missing); err == nil {
		t.Fatal("V26 accepted a missing report export catalog")
	}
	for _, mutate := range []func(map[string][]string){
		func(value map[string][]string) {
			value["report_exports/columns"][10] = "content_digest|text|false|'sha256:'::text"
		},
		func(value map[string][]string) {
			value["report_exports/constraints"] =
				value["report_exports/constraints"][:len(value["report_exports/constraints"])-1]
		},
		func(value map[string][]string) {
			value["report_exports/indexes"][0] =
				"app_report_exports_claim_idx|CREATE INDEX app_report_exports_claim_idx ON app_report_exports USING btree (id)"
		},
		func(value map[string][]string) {
			value["report_exports/indexes"] = append(value["report_exports/indexes"],
				"app_report_exports_unapproved|unexpected")
		},
	} {
		current := clone(valid)
		mutate(current)
		projected, err := projectV26(before, current)
		if err == nil && reflect.DeepEqual(projected, before) {
			t.Fatal("V26 accepted a missing or unapproved report export catalog delta")
		}
	}
}

func TestValidateCurrentCatalogComposesV25AndV26Exactly(t *testing.T) {
	v24 := ExpectedV24Catalog()
	v24["findings/columns"] = []string{"id|text|true|", "imported_at|timestamp with time zone|true|"}
	v24["findings/constraints"] = []string{"app_findings_pkey|PRIMARY KEY (id)"}
	v24["findings/indexes"] = []string{
		"app_findings_pkey|CREATE UNIQUE INDEX app_findings_pkey ON app_findings USING btree (id)",
	}
	v25, err := expectedV25Catalog(v24)
	if err != nil {
		t.Fatal(err)
	}
	current, err := expectedV26Catalog(v25)
	if err != nil {
		t.Fatal(err)
	}
	ValidateCurrentCatalog(t, current)
	if prior := ProjectV26Current(t, current); !reflect.DeepEqual(prior, v25) {
		t.Fatal("V26 current catalog did not project to the exact supplied V25 shape")
	}
}

func TestV25ObservedAnchorsUseMinimumReachableImportAndFallback(t *testing.T) {
	before := map[string][]string{
		"imports": {
			`{"run_id":"run-later","imported_at":"2026-10-02T12:00:00Z"}`,
			`{"run_id":"run-first","imported_at":"2026-09-01T08:30:00Z"}`,
		},
		"observations": {
			`{"finding_id":"finding-observed","run_id":"run-later"}`,
			`{"finding_id":"finding-observed","run_id":"run-first"}`,
		},
		"findings": {
			`{"id":"finding-observed","imported_at":"2026-10-03T00:00:00Z"}`,
			`{"id":"finding-fallback","imported_at":"2026-08-15T10:11:12Z"}`,
		},
	}
	anchors := v25ObservedAnchors(t, before)
	if string(anchors["finding-observed"]) != `"2026-09-01T08:30:00Z"` ||
		string(anchors["finding-fallback"]) != `"2026-08-15T10:11:12Z"` {
		t.Fatalf("V25 first-observed projection chose the wrong reachable minimum or fallback: %v", anchors)
	}
}

func catalogSubset(source map[string][]string, tables []string) map[string][]string {
	result := map[string][]string{}
	for _, table := range tables {
		for _, kind := range []string{"columns", "constraints", "indexes"} {
			key := table + "/" + kind
			result[key] = slices.Clone(source[key])
		}
	}
	return result
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

func TestProjectRelationsV23AcceptsOnlyFourCandidateIndexes(t *testing.T) {
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
	current = append(current, v23Relations...)
	slices.Sort(current)
	slices.Reverse(current)
	if got := ProjectRelationsV23(t, before, current); !reflect.DeepEqual(got, before) {
		t.Fatal("V23 relation projection did not preserve the V22 relation set")
	}
}

func TestProjectRelationsV24AddsNoRelationOrIndex(t *testing.T) {
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
	current = append(current, v23Relations...)
	slices.Sort(current)
	slices.Reverse(current)
	if got := ProjectRelationsV24(t, before, current); !reflect.DeepEqual(got, before) {
		t.Fatal("V24 relation projection did not preserve the V23 relation set")
	}
}

func TestProjectRelationsV25AcceptsOnlySLAStorageAndCandidateIndex(t *testing.T) {
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
	current = append(current, v23Relations...)
	current = append(current, v25Relations...)
	slices.Sort(current)
	slices.Reverse(current)
	if got := ProjectRelationsV25(t, before, current); !reflect.DeepEqual(got, before) {
		t.Fatal("V25 relation projection did not preserve the exact V24 relation set")
	}
}

func TestProjectRelationsV26AcceptsOnlyReportExportStorage(t *testing.T) {
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
	current = append(current, v23Relations...)
	current = append(current, v25Relations...)
	current = append(current, v26Relations...)
	slices.Sort(current)
	slices.Reverse(current)
	if got := ProjectRelationsV26(t, before, current); !reflect.DeepEqual(got, before) {
		t.Fatal("V26 relation projection did not preserve the exact V25 relation set")
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

func TestProjectRowsCurrentAddsExactDefaultsAndV25FirstObserved(t *testing.T) {
	before := map[string][]string{
		"source_connections":      {`{"id":"source","profile":"github-cloud-app"}`},
		"source_collections":      {`{"id":"collection","profile":"github-cloud-app"}`},
		"workspaces":              {`{"id":"workspace"}`},
		"integration_connections": {`{"id":"connection","profile":"slack-workspace-bot"}`},
		"finding_deliveries":      {`{"id":"delivery"}`},
		"findings":                {`{"id":"finding","imported_at":"2026-09-01T09:00:00Z","workflow_state":"open"}`},
	}
	current := map[string][]string{
		"source_connections":      {`{"azure_devops_target":null,"id":"source","profile":"github-cloud-app"}`},
		"source_collections":      {`{"azure_devops_selection":null,"azure_devops_target":null,"id":"collection","profile":"github-cloud-app"}`},
		"workspaces":              {`{"id":"workspace","notification_policy_epoch":0}`},
		"integration_connections": {`{"id":"connection","profile":"slack-workspace-bot","webhook_target":null}`},
		"finding_deliveries":      {`{"finding_change_revision":null,"id":"delivery","policy_id":null,"policy_revision":null,"trigger_kind":"manual","webhook_target":null}`},
		"findings":                {`{"candidate_line":0,"candidate_uri":"","change_at":null,"change_kind":"unchanged","change_revision":1,"change_run_id":"","content_digest":"","decision_revision":1,"evidence_revision":1,"first_observed_at":"2026-09-01T09:00:00Z","id":"finding","imported_at":"2026-09-01T09:00:00Z","workflow_state":"open"}`},
	}
	if got := ProjectRowsCurrent(t, before, current); !reflect.DeepEqual(got, before) {
		t.Fatal("complete current rows were not projected without loss")
	}
}
