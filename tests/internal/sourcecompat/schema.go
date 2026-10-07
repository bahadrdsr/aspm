//go:build integration

package sourcecompat

import (
	"encoding/json"
	"fmt"
	"reflect"
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

func constraintSeparator(rows []string) string {
	for _, row := range rows {
		if strings.Contains(row, "|c|") {
			return "|c|"
		}
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
	separator := constraintSeparator(result["findings/constraints"])
	for _, value := range []struct{ name, definition string }{
		{"app_findings_decision_revision_check", "CHECK (decision_revision > 0)"},
		{"app_findings_decision_revision_not_null", "NOT NULL decision_revision"},
		{"app_findings_evidence_revision_check", "CHECK (evidence_revision > 0)"},
		{"app_findings_evidence_revision_not_null", "NOT NULL evidence_revision"},
	} {
		result["findings/constraints"] = append(result["findings/constraints"],
			value.name+separator+value.definition)
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

// ProjectCurrent composes the exact V12 and V13 deltas for current observers.
func ProjectCurrent(t testing.TB, before, current map[string][]string) map[string][]string {
	t.Helper()
	v12, err := expected(before)
	if err != nil {
		t.Fatal(err)
	}
	return ProjectV12(t, before, ProjectV13(t, v12, current))
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

// ProjectRowsCurrent compares all historical fields plus exact V12/V13 defaults.
func ProjectRowsCurrent(t testing.TB, before, current map[string][]string) map[string][]string {
	t.Helper()
	if len(before) != len(current) {
		t.Fatal("V13: business table set changed")
	}
	for table, rows := range before {
		additions := map[string]json.RawMessage{}
		if table == "source_connections" || table == "source_collections" {
			additions["azure_devops_target"] = json.RawMessage("null")
		}
		if table == "source_collections" {
			additions["azure_devops_selection"] = json.RawMessage("null")
		}
		if table == "findings" {
			additions["decision_revision"] = json.RawMessage("1")
			additions["evidence_revision"] = json.RawMessage("1")
		}
		actual, present := current[table]
		if !present || !reflect.DeepEqual(canonicalRowsWithValues(t, rows, additions),
			canonicalRowsWithValues(t, actual, nil)) {
			t.Fatalf("V13: complete historical business rows changed in %s", table)
		}
	}
	return clone(before)
}
