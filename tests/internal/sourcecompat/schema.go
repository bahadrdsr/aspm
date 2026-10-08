//go:build integration

package sourcecompat

import (
	"encoding/json"
	"fmt"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"testing"
)

const LegacyProfileCheck = `CHECK (profile = 'github-cloud-app'::text)`
const ProfileCheck = `CHECK (profile = ANY (ARRAY['github-cloud-app'::text, 'ado-services-build-artifacts'::text]))`
const LegacyKindCheck = `CHECK (kind = ANY (ARRAY['repository'::text, 'finding'::text]))`
const KindCheck = `CHECK (kind = ANY (ARRAY['repository'::text, 'finding'::text, 'pipeline'::text, 'artifact'::text, 'report'::text]))`
const ConnectionTargetCheck = `CHECK (profile = 'github-cloud-app'::text AND azure_devops_target IS NULL OR profile = 'ado-services-build-artifacts'::text AND repository = ''::text AND azure_devops_target IS NOT NULL AND jsonb_typeof(azure_devops_target) = 'object'::text)`
const CollectionTargetCheck = `CHECK (profile = 'github-cloud-app'::text AND azure_devops_target IS NULL AND azure_devops_selection IS NULL OR profile = 'ado-services-build-artifacts'::text AND repository = ''::text AND azure_devops_target IS NOT NULL AND jsonb_typeof(azure_devops_target) = 'object'::text AND azure_devops_selection IS NOT NULL AND jsonb_typeof(azure_devops_selection) = 'object'::text)`
const LegacyWorkflowStateCheck = `CHECK (workflow_state = ANY (ARRAY['open'::text, 'in-progress'::text, 'resolved'::text]))`
const WorkflowStateCheck = `CHECK (workflow_state = ANY (ARRAY['open'::text, 'in-progress'::text, 'pending-retest'::text, 'resolved'::text]))`
const LegacyDispositionCheck = `CHECK (disposition = ANY (ARRAY['none'::text, 'accepted-risk'::text]))`
const DispositionCheck = `CHECK (disposition = ANY (ARRAY['none'::text, 'accepted-risk'::text, 'suppressed'::text, 'false-positive'::text]))`

func clone(value map[string][]string) map[string][]string {
	result := make(map[string][]string, len(value))
	for key, rows := range value {
		result[key] = slices.Clone(rows)
	}
	return result
}

func replace(rows []string, name, old, next string) (string, error) {
	separator, matches := "", 0
	for i, row := range rows {
		for _, sep := range []string{"|", "|c|"} {
			if row == name+sep+old {
				rows[i], separator = name+sep+next, sep
				matches++
			}
		}
	}
	if matches != 1 {
		return "", fmt.Errorf("V12: exact named CHECK %s missing or ambiguous", name)
	}
	return separator, nil
}

func sortConstraints(rows []string) {
	slices.SortFunc(rows, func(a, b string) int {
		left, _, _ := strings.Cut(a, "|")
		right, _, _ := strings.Cut(b, "|")
		return strings.Compare(left, right)
	})
}

func expected(before map[string][]string) (map[string][]string, error) {
	result := clone(before)
	for _, table := range []string{"source_connections", "source_collections", "source_repository_assets", "source_collection_records"} {
		columns, constraints := table+"/columns", table+"/constraints"
		old, present := before[columns]
		if !present {
			continue
		}
		if len(old) == 0 {
			return nil, fmt.Errorf("V12: missing observed %s columns", table)
		}
		suffix := ""
		switch strings.Count(old[0], "|") {
		case 3:
		case 5:
			suffix = "||"
		default:
			return nil, fmt.Errorf("V12: unsupported catalog representation")
		}
		name, prior, next := "app_"+table+"_profile_check", LegacyProfileCheck, ProfileCheck
		if table == "source_collection_records" {
			name, prior, next = "app_"+table+"_kind_check", LegacyKindCheck, KindCheck
		}
		separator, err := replace(result[constraints], name, prior, next)
		if err != nil {
			return nil, err
		}
		if table == "source_connections" || table == "source_collections" {
			result[columns] = append(result[columns], "azure_devops_target|jsonb|false|"+suffix)
			target := ConnectionTargetCheck
			if table == "source_collections" {
				result[columns] = append(result[columns], "azure_devops_selection|jsonb|false|"+suffix)
				target = CollectionTargetCheck
			}
			result[constraints] = append(result[constraints], "app_"+table+"_profile_target_check"+separator+target)
		}
		sortConstraints(result[constraints])
	}
	return result, nil
}

// ProjectV12 checks the entire expected catalog before projecting only V12.
func ProjectV12(t testing.TB, before, current map[string][]string) map[string][]string {
	t.Helper()
	want, err := expected(before)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, current) {
		t.Fatal("V12: missing required or unapproved catalog delta")
	}
	result := clone(current)
	for _, table := range []string{"source_connections", "source_collections", "source_repository_assets", "source_collection_records"} {
		for _, kind := range []string{"columns", "constraints"} {
			key := table + "/" + kind
			if prior, present := before[key]; present {
				result[key] = slices.Clone(prior)
			}
		}
	}
	return result
}

func constraintSeparator(rows []string, definition string) string {
	typed := false
	for _, row := range rows {
		parts := strings.SplitN(row, "|", 3)
		if len(parts) == 3 && len(parts[1]) == 1 {
			typed = true
			break
		}
	}
	if strings.HasPrefix(definition, "NOT NULL ") {
		if typed {
			return "|n|"
		}
	}
	if typed {
		return "|c|"
	}
	return "|"
}

func expectedV13(before map[string][]string) (map[string][]string, error) {
	result := clone(before)
	columns, present := before["findings/columns"]
	if !present {
		return result, nil
	}
	if len(columns) == 0 {
		return nil, fmt.Errorf("V13: missing observed findings columns")
	}
	suffix := ""
	switch strings.Count(columns[0], "|") {
	case 3:
	case 5:
		suffix = "||"
	default:
		return nil, fmt.Errorf("V13: unsupported catalog representation")
	}
	result["findings/columns"] = append(result["findings/columns"],
		"decision_revision|bigint|true|1"+suffix,
		"evidence_revision|bigint|true|1"+suffix)
	for _, value := range []struct{ name, definition string }{
		{"app_findings_decision_revision_check", "CHECK (decision_revision > 0)"},
		{"app_findings_decision_revision_not_null", "NOT NULL decision_revision"},
		{"app_findings_evidence_revision_check", "CHECK (evidence_revision > 0)"},
		{"app_findings_evidence_revision_not_null", "NOT NULL evidence_revision"},
	} {
		result["findings/constraints"] = append(result["findings/constraints"],
			value.name+constraintSeparator(result["findings/constraints"], value.definition)+value.definition)
	}
	sortConstraints(result["findings/constraints"])
	return result, nil
}

// ProjectV13 checks the exact correlation/lifecycle delta on legacy tables.
func ProjectV13(t testing.TB, before, current map[string][]string) map[string][]string {
	t.Helper()
	want, err := expectedV13(before)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(want, current) {
		t.Fatal("V13: missing required or unapproved legacy-table catalog delta")
	}
	result := clone(current)
	for _, kind := range []string{"columns", "constraints"} {
		key := "findings/" + kind
		if prior, present := before[key]; present {
			result[key] = slices.Clone(prior)
		}
	}
	return result
}

// ProjectCurrent composes the exact additive deltas for current observers.
func ProjectCurrent(t testing.TB, before, current map[string][]string) map[string][]string {
	t.Helper()
	v12, err := expected(before)
	if err != nil {
		t.Fatal(err)
	}
	v13, err := expectedV13(v12)
	if err != nil {
		t.Fatal(err)
	}
	projected := ProjectV21(t, v13, current)
	projected = ProjectV20(t, v13, projected)
	projected = ProjectV19(t, v13, projected)
	projected = ProjectV17(t, v13, projected)
	projected = ProjectV16(t, v13, projected)
	projected = ProjectV15(t, v13, projected)
	projected = ProjectV14(t, v13, projected)
	return ProjectV12(t, before, ProjectV13(t, v12, projected))
}

var v13Relations = []string{
	"app_finding_correlation_events|r",
	"app_finding_correlation_events_group|i",
	"app_finding_correlation_events_group_sequence_key|i",
	"app_finding_correlation_events_idempotency_key|i",
	"app_finding_correlation_events_pkey|i",
	"app_finding_correlation_events_workspace_id_key|i",
	"app_finding_correlation_members|r",
	"app_finding_correlation_members_active_finding|i",
	"app_finding_correlation_members_group|i",
	"app_finding_correlation_members_group_finding_key|i",
	"app_finding_correlation_members_group_ordinal_key|i",
	"app_finding_correlation_members_pkey|i",
	"app_finding_correlations|r",
	"app_finding_correlations_pkey|i",
	"app_finding_correlations_workspace_id_key|i",
}

var v14Relations = []string{
	"app_assessment_previews_retention_observation_idx|i",
	"app_finding_correlation_events_retention_idx|i",
	"app_imports_retention_idx|i",
	"app_observations_retention_run_idx|i",
	"app_retention_holds|r",
	"app_retention_holds_active_resource|i",
	"app_retention_holds_pkey|i",
	"app_retention_holds_workspace_created|i",
	"app_retention_holds_workspace_id_key|i",
	"app_retention_policies|r",
	"app_retention_policies_pkey|i",
	"app_retention_preview_items|r",
	"app_retention_preview_items_pkey|i",
	"app_retention_preview_items_preview|i",
	"app_retention_preview_items_resource_key|i",
	"app_retention_previews|r",
	"app_retention_previews_approval_key|i",
	"app_retention_previews_pkey|i",
	"app_retention_previews_workspace_created|i",
	"app_retention_previews_workspace_id_key|i",
}

var v15Relations = []string{
	"app_finding_correlation_events_retention_state_idx|i",
	"app_observations_retention_state_idx|i",
	"app_retention_run_items|r",
	"app_retention_run_items_pkey|i",
	"app_retention_run_items_resource_key|i",
	"app_retention_run_items_run_ordinal_key|i",
	"app_retention_run_items_run_state_idx|i",
	"app_retention_run_items_workspace_id_key|i",
	"app_retention_runs|r",
	"app_retention_runs_claim_idx|i",
	"app_retention_runs_idempotency_key|i",
	"app_retention_runs_pkey|i",
	"app_retention_runs_workspace_created_idx|i",
	"app_retention_runs_workspace_id_key|i",
}

var v16Relations = []string{
	"app_findings_correlation_candidate_idx|i",
}

var v17Relations = []string{
	"app_findings_meaningful_change_idx|i",
}

var v18Relations = []string{
	"app_archive_publications|r",
	"app_archive_publications_object_key|i",
	"app_archive_publications_orphan_idx|i",
	"app_archive_publications_pkey|i",
	"app_archive_publications_workspace_id_key|i",
}

var v19Relations = []string{
	"app_finding_decision_events|r",
	"app_finding_decision_events_finding_idx|i",
	"app_finding_decision_events_finding_revision_key|i",
	"app_finding_decision_events_pkey|i",
	"app_finding_decision_events_workspace_id_key|i",
}

var v20Relations = []string{
	"app_finding_disposition_approvals|r",
	"app_finding_disposition_approvals_finding_idx|i",
	"app_finding_disposition_approvals_finding_revision_key|i",
	"app_finding_disposition_approvals_pkey|i",
	"app_finding_disposition_approvals_workspace_id_key|i",
}

var v21Relations = []string{
	"app_finding_change_events|r",
	"app_finding_change_events_claim_idx|i",
	"app_finding_change_events_finding_revision_key|i",
	"app_finding_change_events_pkey|i",
	"app_finding_change_events_workspace_id_key|i",
	"app_finding_deliveries_policy_effect_key|i",
	"app_jira_finding_effects|r",
	"app_jira_finding_effects_delivery_key|i",
	"app_jira_finding_effects_pkey|i",
	"app_notification_policies|r",
	"app_notification_policies_pkey|i",
	"app_notification_policies_workspace_id_key|i",
	"app_notification_policy_events|r",
	"app_notification_policy_events_pkey|i",
	"app_notification_policy_events_policy_finding_revision_key|i",
	"app_notification_policy_events_policy_idx|i",
	"app_notification_policy_events_workspace_id_key|i",
	"app_notification_policy_revisions|r",
	"app_notification_policy_revisions_pkey|i",
	"app_notification_policy_revisions_policy_revision_key|i",
	"app_notification_policy_revisions_workspace_epoch_key|i",
	"app_notification_policy_revisions_workspace_id_key|i",
}

var v21Tables = []string{
	"notification_policies",
	"notification_policy_revisions",
	"finding_change_events",
	"notification_policy_events",
	"jira_finding_effects",
}

// V21Tables returns the exact additive V21 table set for catalog observers.
func V21Tables() []string { return slices.Clone(v21Tables) }

var v14LegacyIndexes = []struct {
	table, name, definition string
}{
	{"imports", "app_imports_retention_idx",
		"app_imports USING btree (workspace_id, imported_at, id) WHERE (state = ANY (ARRAY['succeeded'::text, 'failed'::text]))"},
	{"observations", "app_observations_retention_run_idx",
		"app_observations USING btree (workspace_id, run_id, id)"},
	{"assessment_previews", "app_assessment_previews_retention_observation_idx",
		"app_assessment_previews USING btree (workspace_id, observation_id)"},
}

var observedSchemaName = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)

func exactV14Index(row, name, definition string) bool {
	prefix := name + "|CREATE INDEX " + name + " ON "
	if !strings.HasPrefix(row, prefix) {
		return false
	}
	schema, actual, present := strings.Cut(strings.TrimPrefix(row, prefix), ".")
	return present && observedSchemaName.MatchString(schema) && actual == definition
}

// ProjectV14 validates and projects only the additive indexes on legacy tables.
func ProjectV14(t testing.TB, before, current map[string][]string) map[string][]string {
	t.Helper()
	result := clone(current)
	for _, spec := range v14LegacyIndexes {
		key := spec.table + "/indexes"
		prior, present := before[key]
		if !present {
			continue
		}
		rows, present := current[key]
		if !present || len(rows) != len(prior)+1 {
			t.Fatalf("V14: %s index set has a missing or unapproved delta", spec.table)
		}
		remaining := make([]string, 0, len(prior))
		matches := 0
		for _, row := range rows {
			if strings.HasPrefix(row, spec.name+"|") {
				if !exactV14Index(row, spec.name, spec.definition) {
					t.Fatalf("V14: %s definition changed", spec.name)
				}
				matches++
				continue
			}
			remaining = append(remaining, row)
		}
		if matches != 1 || !reflect.DeepEqual(remaining, prior) {
			t.Fatalf("V14: %s index set has a missing or unapproved delta", spec.table)
		}
		result[key] = slices.Clone(prior)
	}
	return result
}

func v15ColumnSuffix(columns []string) (string, error) {
	if len(columns) == 0 {
		return "", fmt.Errorf("V15: missing observed columns")
	}
	switch strings.Count(columns[0], "|") {
	case 3:
		return "", nil
	case 5:
		return "||", nil
	default:
		return "", fmt.Errorf("V15: unsupported catalog representation")
	}
}

func replaceV15Column(rows []string, name, old, next string) error {
	matches := 0
	for index, row := range rows {
		if row == name+old {
			rows[index] = name + next
			matches++
		}
	}
	if matches != 1 {
		return fmt.Errorf("V15: exact %s column missing or ambiguous", name)
	}
	return nil
}

func removeV15Constraint(rows []string, name string) ([]string, error) {
	result := make([]string, 0, len(rows))
	matches := 0
	for _, row := range rows {
		if strings.HasPrefix(row, name+"|") {
			matches++
			continue
		}
		result = append(result, row)
	}
	if matches != 1 {
		return nil, fmt.Errorf("V15: exact %s constraint missing or ambiguous", name)
	}
	return result, nil
}

func expectedV15Table(before map[string][]string, table string) ([]string, []string, error) {
	columns, present := before[table+"/columns"]
	if !present {
		return nil, nil, nil
	}
	suffix, err := v15ColumnSuffix(columns)
	if err != nil {
		return nil, nil, err
	}
	columns = slices.Clone(columns)
	constraints := slices.Clone(before[table+"/constraints"])
	addConstraint := func(name, definition string) {
		constraints = append(constraints, name+constraintSeparator(constraints, definition)+definition)
	}
	switch table {
	case "imports":
		columns = append(columns,
			"evidence_availability|text|true|'available'::text"+suffix,
			"evidence_revision|bigint|true|1"+suffix,
			"retention_transition|text|false|"+suffix,
			"evidence_expired_at|timestamp with time zone|false|"+suffix)
		for _, value := range []struct{ name, definition string }{
			{"app_imports_evidence_availability_check", "CHECK (evidence_availability = ANY (ARRAY['available'::text, 'archived'::text, 'expired'::text, 'missing'::text, 'corrupt'::text]))"},
			{"app_imports_evidence_availability_not_null", "NOT NULL evidence_availability"},
			{"app_imports_evidence_expiry_check", "CHECK ((evidence_availability = 'expired'::text) = (evidence_expired_at IS NOT NULL))"},
			{"app_imports_evidence_revision_check", "CHECK (evidence_revision > 0)"},
			{"app_imports_evidence_revision_not_null", "NOT NULL evidence_revision"},
			{"app_imports_retention_transition_check", "CHECK (retention_transition = 'expiring'::text)"},
		} {
			addConstraint(value.name, value.definition)
		}
	case "observations":
		if err := replaceV15Column(columns, "data", "|jsonb|true|"+suffix, "|jsonb|false|"+suffix); err != nil {
			return nil, nil, err
		}
		columns = append(columns,
			"evidence_availability|text|true|'available'::text"+suffix,
			"evidence_revision|bigint|true|1"+suffix,
			"summary|jsonb|false|"+suffix,
			"archive_key|text|false|"+suffix,
			"archive_digest|text|false|"+suffix,
			"archive_size|bigint|false|"+suffix,
			"archived_at|timestamp with time zone|false|"+suffix,
			"evidence_expired_at|timestamp with time zone|false|"+suffix,
			"retention_transition|text|false|"+suffix)
		constraints, err = removeV15Constraint(constraints, "app_observations_data_not_null")
		if err != nil {
			return nil, nil, err
		}
		for _, value := range []struct{ name, definition string }{
			{"app_observations_archive_digest_check", "CHECK (archive_digest IS NULL OR archive_digest ~ '^sha256:[0-9a-f]{64}$'::text)"},
			{"app_observations_archive_reference_check", "CHECK (archive_key IS NULL AND archive_digest IS NULL AND archive_size IS NULL AND archived_at IS NULL OR archive_key IS NOT NULL AND archive_digest IS NOT NULL AND archive_size IS NOT NULL AND archived_at IS NOT NULL)"},
			{"app_observations_archive_size_check", "CHECK (archive_size IS NULL OR archive_size >= 0)"},
			{"app_observations_evidence_availability_check", "CHECK (evidence_availability = ANY (ARRAY['available'::text, 'archived'::text, 'expired'::text, 'missing'::text, 'corrupt'::text]))"},
			{"app_observations_evidence_availability_not_null", "NOT NULL evidence_availability"},
			{"app_observations_evidence_content_check", "CHECK (evidence_availability = 'available'::text AND data IS NOT NULL AND summary IS NULL OR evidence_availability <> 'available'::text AND data IS NULL AND summary IS NOT NULL)"},
			{"app_observations_evidence_expiry_check", "CHECK ((evidence_availability = 'expired'::text) = (evidence_expired_at IS NOT NULL))"},
			{"app_observations_evidence_revision_check", "CHECK (evidence_revision > 0)"},
			{"app_observations_evidence_revision_not_null", "NOT NULL evidence_revision"},
			{"app_observations_retention_transition_check", "CHECK (retention_transition = 'expiring'::text)"},
		} {
			addConstraint(value.name, value.definition)
		}
	default:
		return nil, nil, fmt.Errorf("V15: unsupported legacy table")
	}
	sortConstraints(constraints)
	return columns, constraints, nil
}

// ProjectV15 validates and projects the exact legacy-table availability delta.
func ProjectV15(t testing.TB, before, current map[string][]string) map[string][]string {
	t.Helper()
	result := clone(current)
	for _, table := range []string{"imports", "observations"} {
		columns, constraints, err := expectedV15Table(before, table)
		if err != nil {
			t.Fatal(err)
		}
		if columns == nil {
			continue
		}
		if !reflect.DeepEqual(current[table+"/columns"], columns) ||
			!reflect.DeepEqual(current[table+"/constraints"], constraints) {
			t.Fatalf("V15: %s catalog has a missing or unapproved delta", table)
		}
		result[table+"/columns"] = slices.Clone(before[table+"/columns"])
		result[table+"/constraints"] = slices.Clone(before[table+"/constraints"])
	}
	key := "observations/indexes"
	if prior, present := before[key]; present {
		rows := current[key]
		remaining := make([]string, 0, len(rows))
		matches := 0
		for _, row := range rows {
			if strings.HasPrefix(row, "app_observations_retention_state_idx|") {
				if !exactV14Index(row, "app_observations_retention_state_idx",
					"app_observations USING btree (workspace_id, evidence_availability, id)") {
					t.Fatal("V15: observation availability index definition changed")
				}
				matches++
				continue
			}
			remaining = append(remaining, row)
		}
		if matches != 1 {
			t.Fatal("V15: observation availability index missing or ambiguous")
		}
		result[key] = remaining
		_ = prior
	}
	return result
}

// ProjectV16 validates and projects the exact bounded-candidate finding delta.
func ProjectV16(t testing.TB, before, current map[string][]string) map[string][]string {
	t.Helper()
	result := clone(current)
	columns, present := before["findings/columns"]
	if !present {
		return result
	}
	suffix, err := v15ColumnSuffix(columns)
	if err != nil {
		t.Fatal(err)
	}
	wantColumns := append(slices.Clone(columns),
		"candidate_uri|text|true|''::text"+suffix,
		"candidate_line|integer|true|0"+suffix)
	wantConstraints := slices.Clone(before["findings/constraints"])
	for _, value := range []struct{ name, definition string }{
		{"app_findings_candidate_line_check", "CHECK (candidate_line >= 0 AND candidate_line <= 2147483647)"},
		{"app_findings_candidate_line_not_null", "NOT NULL candidate_line"},
		{"app_findings_candidate_location_check", "CHECK (candidate_uri = ''::text AND candidate_line = 0 OR candidate_uri <> ''::text AND candidate_line > 0)"},
		{"app_findings_candidate_uri_check", "CHECK (octet_length(candidate_uri) <= 8192)"},
		{"app_findings_candidate_uri_not_null", "NOT NULL candidate_uri"},
	} {
		wantConstraints = append(wantConstraints,
			value.name+constraintSeparator(wantConstraints, value.definition)+value.definition)
	}
	sortConstraints(wantConstraints)
	if !reflect.DeepEqual(current["findings/columns"], wantColumns) ||
		!reflect.DeepEqual(current["findings/constraints"], wantConstraints) {
		t.Fatal("V16: findings catalog has a missing or unapproved delta")
	}
	result["findings/columns"] = slices.Clone(columns)
	result["findings/constraints"] = slices.Clone(before["findings/constraints"])
	key := "findings/indexes"
	if prior, present := before[key]; present {
		rows := current[key]
		remaining := make([]string, 0, len(rows))
		matches := 0
		for _, row := range rows {
			if strings.HasPrefix(row, "app_findings_correlation_candidate_idx|") {
				if !exactV14Index(row, "app_findings_correlation_candidate_idx",
					"app_findings USING btree (workspace_id, asset_id, scope_branch, candidate_uri, candidate_line, id) WHERE ((candidate_uri <> ''::text) AND (candidate_line > 0))") {
					t.Fatal("V16: candidate index definition changed")
				}
				matches++
				continue
			}
			remaining = append(remaining, row)
		}
		if matches != 1 || !reflect.DeepEqual(remaining, prior) {
			t.Fatal("V16: candidate index missing or ambiguous")
		}
		result[key] = slices.Clone(prior)
	}
	return result
}

func exactV21Index(row, name, definition string) bool {
	prefix := name + "|CREATE UNIQUE INDEX " + name + " ON "
	if !strings.HasPrefix(row, prefix) {
		return false
	}
	actual := strings.TrimPrefix(row, prefix)
	if actual == definition {
		return true
	}
	schema, qualified, present := strings.Cut(actual, ".")
	return present && observedSchemaName.MatchString(schema) && qualified == definition
}

func appendObservedV21NotNull(before, current []string, name, column string) []string {
	definition := "NOT NULL " + column
	separator := constraintSeparator(before, definition)
	row := name + separator + definition
	if separator == "|n|" || slices.Contains(current, row) {
		return append(before, row)
	}
	return before
}

// ProjectV21 validates and projects the exact automatic-policy delta on legacy tables.
func ProjectV21(t testing.TB, before, current map[string][]string) map[string][]string {
	t.Helper()
	result := clone(current)
	for _, table := range []string{"workspaces", "finding_deliveries"} {
		columns, present := before[table+"/columns"]
		if !present {
			continue
		}
		suffix, err := v15ColumnSuffix(columns)
		if err != nil {
			t.Fatal(err)
		}
		wantColumns := slices.Clone(columns)
		wantConstraints := slices.Clone(before[table+"/constraints"])
		if table == "workspaces" {
			wantColumns = append(wantColumns, "notification_policy_epoch|bigint|true|0"+suffix)
			definition := "CHECK (notification_policy_epoch >= 0)"
			wantConstraints = append(wantConstraints,
				"app_workspaces_notification_policy_epoch_check"+
					constraintSeparator(wantConstraints, definition)+definition)
			wantConstraints = appendObservedV21NotNull(wantConstraints, current[table+"/constraints"],
				"app_workspaces_notification_policy_epoch_not_null", "notification_policy_epoch")
		} else {
			wantColumns = append(wantColumns,
				"trigger_kind|text|true|'manual'::text"+suffix,
				"policy_id|text|false|"+suffix,
				"policy_revision|bigint|false|"+suffix,
				"finding_change_revision|bigint|false|"+suffix)
			wantConstraints = appendObservedV21NotNull(wantConstraints, current[table+"/constraints"],
				"app_finding_deliveries_trigger_kind_not_null", "trigger_kind")
			for _, value := range []struct{ name, definition string }{
				{"app_finding_deliveries_policy_binding_check", "CHECK (trigger_kind = 'manual'::text AND policy_id IS NULL AND policy_revision IS NULL AND finding_change_revision IS NULL OR trigger_kind = 'notification-policy'::text AND policy_id IS NOT NULL AND policy_revision IS NOT NULL AND policy_revision > 0 AND finding_change_revision IS NOT NULL AND finding_change_revision > 0)"},
				{"app_finding_deliveries_trigger_kind_check", "CHECK (trigger_kind = ANY (ARRAY['manual'::text, 'notification-policy'::text]))"},
			} {
				wantConstraints = append(wantConstraints,
					value.name+constraintSeparator(wantConstraints, value.definition)+value.definition)
			}
		}
		sortConstraints(wantConstraints)
		if !reflect.DeepEqual(current[table+"/columns"], wantColumns) ||
			!reflect.DeepEqual(current[table+"/constraints"], wantConstraints) {
			t.Fatalf("V21: %s catalog has a missing or unapproved delta", table)
		}
		result[table+"/columns"] = slices.Clone(columns)
		result[table+"/constraints"] = slices.Clone(before[table+"/constraints"])
	}
	key := "finding_deliveries/indexes"
	if prior, present := before[key]; present {
		rows := current[key]
		remaining := make([]string, 0, len(rows))
		matches := 0
		for _, row := range rows {
			if strings.HasPrefix(row, "app_finding_deliveries_policy_effect_key|") {
				if !exactV21Index(row, "app_finding_deliveries_policy_effect_key",
					"app_finding_deliveries USING btree (workspace_id, policy_id, finding_id, finding_change_revision) WHERE (trigger_kind = 'notification-policy'::text)") {
					t.Fatal("V21: policy delivery effect index definition changed")
				}
				matches++
				continue
			}
			remaining = append(remaining, row)
		}
		if matches != 1 || !reflect.DeepEqual(remaining, prior) {
			t.Fatal("V21: policy delivery effect index is missing or ambiguous")
		}
		result[key] = slices.Clone(prior)
	}
	return result
}

// ProjectV20 validates and projects the exact disposition constraint change.
func ProjectV20(t testing.TB, before, current map[string][]string) map[string][]string {
	t.Helper()
	result := clone(current)
	prior, present := before["findings/constraints"]
	if !present {
		return result
	}
	foundPrior := false
	for _, row := range prior {
		if strings.HasPrefix(row, "app_findings_disposition_check|") {
			foundPrior = true
			break
		}
	}
	if !foundPrior {
		return result
	}
	rows, present := current["findings/constraints"]
	if !present {
		t.Fatal("V20: findings constraints are missing")
	}
	rows = slices.Clone(rows)
	separator, err := replace(rows, "app_findings_disposition_check", DispositionCheck, LegacyDispositionCheck)
	if err != nil || separator == "" {
		t.Fatal("V20: exact disposition constraint replacement is missing or ambiguous")
	}
	sortConstraints(rows)
	result["findings/constraints"] = rows
	return result
}

// ProjectV19 validates and projects the exact pending-retest constraint change.
func ProjectV19(t testing.TB, before, current map[string][]string) map[string][]string {
	t.Helper()
	result := clone(current)
	prior, present := before["findings/constraints"]
	if !present {
		return result
	}
	foundPrior := false
	for _, row := range prior {
		if strings.HasPrefix(row, "app_findings_workflow_state_check|") {
			foundPrior = true
			break
		}
	}
	if !foundPrior {
		return result
	}
	rows, present := current["findings/constraints"]
	if !present {
		t.Fatal("V19: findings constraints are missing")
	}
	rows = slices.Clone(rows)
	separator, err := replace(rows, "app_findings_workflow_state_check", WorkflowStateCheck, LegacyWorkflowStateCheck)
	if err != nil || separator == "" {
		t.Fatal("V19: exact workflow-state constraint replacement is missing or ambiguous")
	}
	sortConstraints(rows)
	result["findings/constraints"] = rows
	return result
}

// ProjectV17 validates and projects the exact finding lifecycle delta.
func ProjectV17(t testing.TB, before, current map[string][]string) map[string][]string {
	t.Helper()
	result := clone(current)
	columns, present := current["findings/columns"]
	if !present {
		return result
	}
	suffix, err := v15ColumnSuffix(columns)
	if err != nil {
		t.Fatal(err)
	}
	addedColumns := []string{
		"content_digest|text|true|''::text" + suffix,
		"change_kind|text|true|'unchanged'::text" + suffix,
		"change_at|timestamp with time zone|false|" + suffix,
		"change_run_id|text|true|''::text" + suffix,
		"change_revision|bigint|true|1" + suffix,
	}
	if len(columns) < len(addedColumns) ||
		!reflect.DeepEqual(columns[len(columns)-len(addedColumns):], addedColumns) {
		t.Fatal("V17: findings columns have a missing or unapproved delta")
	}
	result["findings/columns"] = slices.Clone(columns[:len(columns)-len(addedColumns)])
	constraints := slices.Clone(current["findings/constraints"])
	for _, value := range []struct{ name, definition string }{
		{"app_findings_change_kind_check", "CHECK (change_kind = ANY (ARRAY['new'::text, 'changed'::text, 'unchanged'::text, 'reopened'::text, 'inferred-resolved'::text]))"},
		{"app_findings_change_kind_not_null", "NOT NULL change_kind"},
		{"app_findings_change_revision_check", "CHECK (change_revision > 0)"},
		{"app_findings_change_revision_not_null", "NOT NULL change_revision"},
		{"app_findings_change_run_id_check", "CHECK (change_run_id = ''::text OR change_run_id ~ '^[0-9a-f]{32}$'::text)"},
		{"app_findings_change_run_id_not_null", "NOT NULL change_run_id"},
		{"app_findings_content_digest_check", "CHECK (content_digest = ''::text OR content_digest ~ '^sha256:[0-9a-f]{64}$'::text)"},
		{"app_findings_content_digest_not_null", "NOT NULL content_digest"},
	} {
		expected := value.name + constraintSeparator(constraints, value.definition) + value.definition
		matches := 0
		remaining := make([]string, 0, len(constraints))
		for _, row := range constraints {
			if row == expected {
				matches++
				continue
			}
			remaining = append(remaining, row)
		}
		if matches != 1 {
			t.Fatalf("V17: exact %s constraint missing or ambiguous", value.name)
		}
		constraints = remaining
	}
	result["findings/constraints"] = constraints
	key := "findings/indexes"
	if _, present := before[key]; present {
		rows := current[key]
		remaining := make([]string, 0, len(rows))
		matches := 0
		for _, row := range rows {
			if strings.HasPrefix(row, "app_findings_meaningful_change_idx|") {
				if !exactV14Index(row, "app_findings_meaningful_change_idx",
					"app_findings USING btree (workspace_id, change_kind, id) WHERE (change_kind = ANY (ARRAY['new'::text, 'changed'::text, 'reopened'::text]))") {
					t.Fatal("V17: meaningful change index definition changed")
				}
				matches++
				continue
			}
			remaining = append(remaining, row)
		}
		if matches != 1 {
			t.Fatal("V17: meaningful change index missing or ambiguous")
		}
		result[key] = remaining
	}
	return result
}

func relationDifference(left, right []string) []string {
	present := make(map[string]struct{}, len(right))
	for _, value := range right {
		present[value] = struct{}{}
	}
	var result []string
	for _, value := range left {
		if _, ok := present[value]; !ok {
			result = append(result, value)
		}
	}
	return result
}

// ProjectRelationsV13 validates the exact new table/index relation set.
func ProjectRelationsV13(t testing.TB, before, current []string) []string {
	t.Helper()
	want := append(slices.Clone(before), v13Relations...)
	missing, unexpected := relationDifference(want, current), relationDifference(current, want)
	if len(want) != len(current) || len(missing) != 0 || len(unexpected) != 0 {
		t.Fatalf("V13: relation/index set contains a missing or unapproved delta; missing=%v unexpected=%v",
			missing, unexpected)
	}
	return slices.Clone(before)
}

// ProjectRelationsV14 validates the complete current V13/V14 table and index set.
func ProjectRelationsV14(t testing.TB, before, current []string) []string {
	t.Helper()
	want := append(slices.Clone(before), v13Relations...)
	want = append(want, v14Relations...)
	missing, unexpected := relationDifference(want, current), relationDifference(current, want)
	if len(want) != len(current) || len(missing) != 0 || len(unexpected) != 0 {
		t.Fatalf("V14: relation/index set contains a missing or unapproved delta; missing=%v unexpected=%v",
			missing, unexpected)
	}
	return slices.Clone(before)
}

// ProjectRelationsV15 validates the complete current V13-V15 table and index set.
func ProjectRelationsV15(t testing.TB, before, current []string) []string {
	t.Helper()
	want := append(slices.Clone(before), v13Relations...)
	want = append(want, v14Relations...)
	want = append(want, v15Relations...)
	missing, unexpected := relationDifference(want, current), relationDifference(current, want)
	if len(want) != len(current) || len(missing) != 0 || len(unexpected) != 0 {
		t.Fatalf("V15: relation/index set contains a missing or unapproved delta; missing=%v unexpected=%v",
			missing, unexpected)
	}
	return slices.Clone(before)
}

// ProjectRelationsV16 validates the complete current V13-V16 relation set.
func ProjectRelationsV16(t testing.TB, before, current []string) []string {
	t.Helper()
	want := append(slices.Clone(before), v13Relations...)
	want = append(want, v14Relations...)
	want = append(want, v15Relations...)
	want = append(want, v16Relations...)
	missing, unexpected := relationDifference(want, current), relationDifference(current, want)
	if len(want) != len(current) || len(missing) != 0 || len(unexpected) != 0 {
		t.Fatalf("V16: relation/index set contains a missing or unapproved delta; missing=%v unexpected=%v",
			missing, unexpected)
	}
	return slices.Clone(before)
}

// ProjectRelationsV17 validates the complete current V13-V17 relation set.
func ProjectRelationsV17(t testing.TB, before, current []string) []string {
	t.Helper()
	want := append(slices.Clone(before), v13Relations...)
	want = append(want, v14Relations...)
	want = append(want, v15Relations...)
	want = append(want, v16Relations...)
	want = append(want, v17Relations...)
	missing, unexpected := relationDifference(want, current), relationDifference(current, want)
	if len(want) != len(current) || len(missing) != 0 || len(unexpected) != 0 {
		t.Fatalf("V17: relation/index set contains a missing or unapproved delta; missing=%v unexpected=%v",
			missing, unexpected)
	}
	return slices.Clone(before)
}

// ProjectRelationsV18 validates the complete current V13-V18 relation set.
func ProjectRelationsV18(t testing.TB, before, current []string) []string {
	t.Helper()
	want := append(slices.Clone(before), v13Relations...)
	want = append(want, v14Relations...)
	want = append(want, v15Relations...)
	want = append(want, v16Relations...)
	want = append(want, v17Relations...)
	want = append(want, v18Relations...)
	missing, unexpected := relationDifference(want, current), relationDifference(current, want)
	if len(want) != len(current) || len(missing) != 0 || len(unexpected) != 0 {
		t.Fatalf("V18: relation/index set contains a missing or unapproved delta; missing=%v unexpected=%v",
			missing, unexpected)
	}
	return slices.Clone(before)
}

// ProjectRelationsV19 validates the complete current V13-V19 relation set.
func ProjectRelationsV19(t testing.TB, before, current []string) []string {
	t.Helper()
	want := append(slices.Clone(before), v13Relations...)
	want = append(want, v14Relations...)
	want = append(want, v15Relations...)
	want = append(want, v16Relations...)
	want = append(want, v17Relations...)
	want = append(want, v18Relations...)
	want = append(want, v19Relations...)
	missing, unexpected := relationDifference(want, current), relationDifference(current, want)
	if len(want) != len(current) || len(missing) != 0 || len(unexpected) != 0 {
		t.Fatalf("V19: relation/index set contains a missing or unapproved delta; missing=%v unexpected=%v",
			missing, unexpected)
	}
	return slices.Clone(before)
}

// ProjectRelationsV20 validates the complete current V13-V20 relation set.
func ProjectRelationsV20(t testing.TB, before, current []string) []string {
	t.Helper()
	want := append(slices.Clone(before), v13Relations...)
	want = append(want, v14Relations...)
	want = append(want, v15Relations...)
	want = append(want, v16Relations...)
	want = append(want, v17Relations...)
	want = append(want, v18Relations...)
	want = append(want, v19Relations...)
	want = append(want, v20Relations...)
	missing, unexpected := relationDifference(want, current), relationDifference(current, want)
	if len(want) != len(current) || len(missing) != 0 || len(unexpected) != 0 {
		t.Fatalf("V20: relation/index set contains a missing or unapproved delta; missing=%v unexpected=%v",
			missing, unexpected)
	}
	return slices.Clone(before)
}

// ProjectRelationsV21 validates the complete current V13-V21 relation set.
func ProjectRelationsV21(t testing.TB, before, current []string) []string {
	t.Helper()
	want := append(slices.Clone(before), v13Relations...)
	want = append(want, v14Relations...)
	want = append(want, v15Relations...)
	want = append(want, v16Relations...)
	want = append(want, v17Relations...)
	want = append(want, v18Relations...)
	want = append(want, v19Relations...)
	want = append(want, v20Relations...)
	want = append(want, v21Relations...)
	missing, unexpected := relationDifference(want, current), relationDifference(current, want)
	if len(want) != len(current) || len(missing) != 0 || len(unexpected) != 0 {
		t.Fatalf("V21: relation/index set contains a missing or unapproved delta; missing=%v unexpected=%v",
			missing, unexpected)
	}
	return slices.Clone(before)
}

func canonicalRows(t testing.TB, rows []string, additions []string) []string {
	t.Helper()
	result := make([]string, 0, len(rows))
	for _, row := range rows {
		var value map[string]json.RawMessage
		if json.Unmarshal([]byte(row), &value) != nil || value == nil {
			t.Fatal("V12: invalid complete observed business row")
		}
		for _, name := range additions {
			if _, present := value[name]; present {
				t.Fatal("V12: historical row already contained an added column")
			}
			value[name] = json.RawMessage("null")
		}
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal("V12: cannot compare complete observed business rows")
		}
		result = append(result, string(data))
	}
	slices.Sort(result)
	return result
}

// ProjectRowsV12 first compares every field, including exactly the new nulls.
func ProjectRowsV12(t testing.TB, before, current map[string][]string) map[string][]string {
	t.Helper()
	if len(before) != len(current) {
		t.Fatal("V12: business table set changed")
	}
	for table, rows := range before {
		additions := []string{}
		if table == "source_connections" || table == "source_collections" {
			additions = append(additions, "azure_devops_target")
		}
		if table == "source_collections" {
			additions = append(additions, "azure_devops_selection")
		}
		actual, present := current[table]
		if !present || !reflect.DeepEqual(canonicalRows(t, rows, additions), canonicalRows(t, actual, nil)) {
			t.Fatalf("V12: complete historical business rows changed in %s", table)
		}
	}
	return clone(before)
}

func canonicalRowsWithValues(t testing.TB, rows []string, additions map[string]json.RawMessage) []string {
	t.Helper()
	result := make([]string, 0, len(rows))
	for _, row := range rows {
		var value map[string]json.RawMessage
		if json.Unmarshal([]byte(row), &value) != nil || value == nil {
			t.Fatal("V13: invalid complete observed business row")
		}
		for name, raw := range additions {
			if _, present := value[name]; present {
				t.Fatal("V13: historical row already contained an added column")
			}
			value[name] = raw
		}
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal("V13: cannot compare complete observed business rows")
		}
		result = append(result, string(data))
	}
	slices.Sort(result)
	return result
}

// ProjectRowsCurrent compares all historical fields plus exact additive defaults.
func ProjectRowsCurrent(t testing.TB, before, current map[string][]string) map[string][]string {
	t.Helper()
	if len(before) != len(current) {
		t.Fatal("V21: business table set changed")
	}
	for table, rows := range before {
		additions := map[string]json.RawMessage{}
		if table == "source_connections" || table == "source_collections" {
			additions["azure_devops_target"] = json.RawMessage("null")
		}
		if table == "source_collections" {
			additions["azure_devops_selection"] = json.RawMessage("null")
		}
		if table == "workspaces" {
			additions["notification_policy_epoch"] = json.RawMessage("0")
		}
		if table == "finding_deliveries" {
			additions["trigger_kind"] = json.RawMessage(`"manual"`)
			for _, name := range []string{"policy_id", "policy_revision", "finding_change_revision"} {
				additions[name] = json.RawMessage("null")
			}
		}
		if table == "findings" {
			additions["decision_revision"] = json.RawMessage("1")
			additions["evidence_revision"] = json.RawMessage("1")
			additions["candidate_uri"] = json.RawMessage(`""`)
			additions["candidate_line"] = json.RawMessage("0")
			additions["content_digest"] = json.RawMessage(`""`)
			additions["change_kind"] = json.RawMessage(`"unchanged"`)
			additions["change_at"] = json.RawMessage("null")
			additions["change_run_id"] = json.RawMessage(`""`)
			additions["change_revision"] = json.RawMessage("1")
		}
		if table == "imports" {
			additions["evidence_availability"] = json.RawMessage(`"available"`)
			additions["evidence_revision"] = json.RawMessage("1")
			additions["retention_transition"] = json.RawMessage("null")
			additions["evidence_expired_at"] = json.RawMessage("null")
		}
		if table == "observations" {
			additions["evidence_availability"] = json.RawMessage(`"available"`)
			additions["evidence_revision"] = json.RawMessage("1")
			for _, name := range []string{"summary", "archive_key", "archive_digest", "archive_size",
				"archived_at", "evidence_expired_at", "retention_transition"} {
				additions[name] = json.RawMessage("null")
			}
		}
		actual, present := current[table]
		if !present || !reflect.DeepEqual(canonicalRowsWithValues(t, rows, additions),
			canonicalRowsWithValues(t, actual, nil)) {
			t.Fatalf("V21: complete historical business rows changed in %s", table)
		}
	}
	return clone(before)
}
