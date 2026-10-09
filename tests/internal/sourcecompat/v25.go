//go:build integration

package sourcecompat

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

const V25SLATargetsCheck = `CHECK (critical_days >= 1 AND critical_days <= 3650 AND high_days >= 1 AND high_days <= 3650 AND medium_days >= 1 AND medium_days <= 3650 AND low_days >= 1 AND low_days <= 3650 AND info_days >= 1 AND info_days <= 3650 AND critical_days <= high_days AND high_days <= medium_days AND medium_days <= low_days AND low_days <= info_days)`
const V25SLAActorCheck = `CHECK ((approved_by IS NULL) = (approved_by_name IS NULL))`
const V25SLARationaleCheck = `CHECK (octet_length(rationale) >= 1 AND octet_length(rationale) <= 8192 AND btrim(rationale) <> ''::text)`

const v25FindingIndexName = "app_findings_sla_candidate_idx"
const v25FindingIndexDefinition = `app_findings USING btree (workspace_id, workflow_state, severity, first_observed_at, id) WHERE (workflow_state <> 'resolved'::text)`

var v25Tables = []string{
	"findings",
	"report_sla_policies",
	"report_sla_policy_revisions",
}

var v25Relations = []string{
	"app_findings_sla_candidate_idx|i",
	"app_report_sla_policies|r",
	"app_report_sla_policies_pkey|i",
	"app_report_sla_policy_revisions|r",
	"app_report_sla_policy_revisions_pkey|i",
}

var exactV25NewTableCatalog = map[string][]string{
	"report_sla_policies/columns": {
		"workspace_id|text|true|",
		"critical_days|integer|true|7",
		"high_days|integer|true|30",
		"medium_days|integer|true|90",
		"low_days|integer|true|180",
		"info_days|integer|true|365",
		"revision|bigint|true|1",
		"approved_by|text|false|",
		"approved_by_name|text|false|",
		"rationale|text|true|",
		"created_at|timestamp with time zone|true|",
		"updated_at|timestamp with time zone|true|",
	},
	"report_sla_policies/constraints": {
		"app_report_sla_policies_actor_check|" + V25SLAActorCheck,
		"app_report_sla_policies_approved_by_fkey|FOREIGN KEY (approved_by) REFERENCES app_users(id)",
		"app_report_sla_policies_pkey|PRIMARY KEY (workspace_id)",
		"app_report_sla_policies_rationale_check|" + V25SLARationaleCheck,
		"app_report_sla_policies_revision_check|CHECK (revision > 0)",
		"app_report_sla_policies_targets_check|" + V25SLATargetsCheck,
		"app_report_sla_policies_workspace_id_fkey|FOREIGN KEY (workspace_id) REFERENCES app_workspaces(id) ON DELETE CASCADE",
	},
	"report_sla_policies/indexes": {
		"app_report_sla_policies_pkey|CREATE UNIQUE INDEX app_report_sla_policies_pkey ON app_report_sla_policies USING btree (workspace_id)",
	},
	"report_sla_policy_revisions/columns": {
		"workspace_id|text|true|",
		"revision|bigint|true|",
		"critical_days|integer|true|",
		"high_days|integer|true|",
		"medium_days|integer|true|",
		"low_days|integer|true|",
		"info_days|integer|true|",
		"approved_by|text|false|",
		"approved_by_name|text|false|",
		"rationale|text|true|",
		"created_at|timestamp with time zone|true|",
	},
	"report_sla_policy_revisions/constraints": {
		"app_report_sla_policy_revisions_actor_check|" + V25SLAActorCheck,
		"app_report_sla_policy_revisions_approved_by_fkey|FOREIGN KEY (approved_by) REFERENCES app_users(id)",
		"app_report_sla_policy_revisions_pkey|PRIMARY KEY (workspace_id, revision)",
		"app_report_sla_policy_revisions_rationale_check|" + V25SLARationaleCheck,
		"app_report_sla_policy_revisions_revision_check|CHECK (revision > 0)",
		"app_report_sla_policy_revisions_targets_check|" + V25SLATargetsCheck,
		"app_report_sla_policy_revisions_workspace_id_fkey|FOREIGN KEY (workspace_id) REFERENCES app_workspaces(id) ON DELETE CASCADE",
	},
	"report_sla_policy_revisions/indexes": {
		"app_report_sla_policy_revisions_pkey|CREATE UNIQUE INDEX app_report_sla_policy_revisions_pkey ON app_report_sla_policy_revisions USING btree (workspace_id, revision)",
	},
}

func V25Tables() []string { return slices.Clone(v25Tables) }

func V25CurrentTables() []string {
	result := V24CurrentTables()
	for _, table := range v25Tables {
		if !slices.Contains(result, table) {
			result = append(result, table)
		}
	}
	return result
}

func exactV25Index(row, name, definition string) bool {
	prefix := name + "|CREATE INDEX " + name + " ON "
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

func projectedV24Findings(current map[string][]string) (map[string][]string, error) {
	result := map[string][]string{}
	columns := current["findings/columns"]
	if len(columns) == 0 {
		return nil, fmt.Errorf("V25: findings columns are missing")
	}
	suffix, err := v24ColumnSuffix(columns)
	if err != nil {
		return nil, err
	}
	added := "first_observed_at|timestamp with time zone|true|" + suffix
	if columns[len(columns)-1] != added {
		return nil, fmt.Errorf("V25: first_observed_at is missing, reordered or has an unapproved default")
	}
	result["findings/columns"] = slices.Clone(columns[:len(columns)-1])
	constraints := current["findings/constraints"]
	keptConstraints := make([]string, 0, len(constraints))
	notNullMatches := 0
	for _, row := range constraints {
		if row == "app_findings_first_observed_at_not_null|NOT NULL first_observed_at" ||
			row == "app_findings_first_observed_at_not_null|n|NOT NULL first_observed_at" {
			notNullMatches++
			continue
		}
		keptConstraints = append(keptConstraints, row)
	}
	if notNullMatches > 1 {
		return nil, fmt.Errorf("V25: first_observed_at NOT NULL constraint is ambiguous")
	}
	result["findings/constraints"] = keptConstraints
	indexes := current["findings/indexes"]
	kept := make([]string, 0, len(indexes)-1)
	matches := 0
	for _, row := range indexes {
		if strings.HasPrefix(row, v25FindingIndexName+"|") {
			if !exactV25Index(row, v25FindingIndexName, v25FindingIndexDefinition) {
				return nil, fmt.Errorf("V25: SLA candidate index definition changed")
			}
			matches++
			continue
		}
		kept = append(kept, row)
	}
	if matches != 1 {
		return nil, fmt.Errorf("V25: SLA candidate index is missing or ambiguous")
	}
	result["findings/indexes"] = kept
	return result, nil
}

func expectedV25Catalog(before map[string][]string) (map[string][]string, error) {
	result := clone(before)
	if columns, present := before["findings/columns"]; present {
		suffix, err := v24ColumnSuffix(columns)
		if err != nil {
			return nil, err
		}
		result["findings/columns"] = append(slices.Clone(columns),
			"first_observed_at|timestamp with time zone|true|"+suffix)
		result["findings/constraints"] = slices.Clone(before["findings/constraints"])
		result["findings/indexes"] = append(slices.Clone(before["findings/indexes"]),
			v25FindingIndexName+"|CREATE INDEX "+v25FindingIndexName+" ON "+v25FindingIndexDefinition)
		slices.Sort(result["findings/indexes"])
	}
	for key, rows := range exactV25NewTableCatalog {
		if _, present := before[key]; present {
			return nil, fmt.Errorf("V25: prior catalog already contains %s", key)
		}
		result[key] = slices.Clone(rows)
	}
	return result, nil
}

func projectV25(before, current map[string][]string) (map[string][]string, error) {
	result := clone(current)
	if _, present := before["findings/columns"]; present {
		projected, err := projectedV24Findings(current)
		if err != nil {
			return nil, err
		}
		for _, kind := range []string{"columns", "constraints", "indexes"} {
			key := "findings/" + kind
			if _, present := before[key]; !present && len(projected[key]) == 0 {
				delete(result, key)
			} else {
				result[key] = slices.Clone(projected[key])
			}
		}
	}
	for _, table := range []string{"report_sla_policies", "report_sla_policy_revisions"} {
		presentKinds := 0
		for _, kind := range []string{"columns", "constraints", "indexes"} {
			if _, present := current[table+"/"+kind]; present {
				presentKinds++
			}
		}
		if presentKinds == 0 {
			continue
		}
		if presentKinds != 3 {
			return nil, fmt.Errorf("V25: %s catalog is incomplete", table)
		}
		for _, kind := range []string{"columns", "constraints", "indexes"} {
			key := table + "/" + kind
			if _, present := before[key]; present {
				return nil, fmt.Errorf("V25: prior catalog already contains %s", key)
			}
			if !reflect.DeepEqual(current[key], exactV25NewTableCatalog[key]) {
				return nil, fmt.Errorf("V25: %s %s contains a missing or unapproved delta", table, kind)
			}
			delete(result, key)
		}
	}
	return result, nil
}

// ProjectV25 validates and projects only persistent remediation SLA storage.
func ProjectV25(t testing.TB, before, current map[string][]string) map[string][]string {
	t.Helper()
	result, err := projectV25(before, current)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// ValidateV25Catalog requires the exact supplied V24 catalog plus only V25.
func ValidateV25Catalog(t testing.TB, before, current map[string][]string) {
	t.Helper()
	if got := ProjectV25(t, before, current); !reflect.DeepEqual(got, before) {
		t.Fatal("V25: projected catalog did not restore the exact V24 catalog")
	}
}

func ProjectV25Current(t testing.TB, current map[string][]string) map[string][]string {
	t.Helper()
	wantKeys := len(V25CurrentTables()) * 3
	if len(current) != wantKeys {
		t.Fatalf("V25: current catalog key count=%d, want %d", len(current), wantKeys)
	}
	before := clone(exactV24Catalog)
	findings, err := projectedV24Findings(current)
	if err != nil {
		t.Fatal(err)
	}
	for key, rows := range findings {
		before[key] = slices.Clone(rows)
	}
	projected, err := projectV25(before, current)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(projected, before) {
		t.Fatal("V25: current catalog did not project to its exact V24 shape")
	}
	v24 := map[string][]string{}
	for key := range exactV24Catalog {
		v24[key] = slices.Clone(projected[key])
	}
	ValidateV24CurrentCatalog(t, v24)
	return projected
}

func ValidateV25CurrentCatalog(t testing.TB, current map[string][]string) {
	t.Helper()
	ProjectV25Current(t, current)
}

// ProjectRelationsV25 permits only the SLA tables and finding candidate index after V24.
func ProjectRelationsV25(t testing.TB, before, current []string) []string {
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
	want = append(want, v23Relations...)
	want = append(want, v25Relations...)
	missing, unexpected := relationDifference(want, current), relationDifference(current, want)
	if len(want) != len(current) || len(missing) != 0 || len(unexpected) != 0 {
		t.Fatalf("V25: relation/index set contains a missing or unapproved delta; missing=%v unexpected=%v",
			missing, unexpected)
	}
	return slices.Clone(before)
}
