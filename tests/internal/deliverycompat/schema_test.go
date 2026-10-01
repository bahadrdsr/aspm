//go:build integration

package deliverycompat

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func legacySnapshot(detailed bool) map[string][]string {
	suffix, separator := "", "|"
	if detailed {
		suffix, separator = "||", "|c|"
	}
	result := map[string][]string{
		"notes/columns":     {"id|text|true|" + suffix},
		"notes/constraints": {"notes_pkey|PRIMARY KEY (id)"},
		"notes/indexes":     {"notes_pkey|unchanged index"},
	}
	for _, table := range []string{"integration_connections", "finding_deliveries"} {
		result[table+"/columns"] = []string{"id|text|true|" + suffix, "profile|text|true|" + suffix, "channel|text|true|" + suffix}
		result[table+"/constraints"] = []string{
			"app_" + table + "_profile_check" + separator + LegacyProfileCheck,
			"app_" + table + "_workspace_id_fkey|FOREIGN KEY (workspace_id) REFERENCES app_workspaces(id)",
		}
		result[table+"/indexes"] = []string{"app_" + table + "_pkey|unchanged index"}
	}
	return result
}

func TestApprovedDeltaBothExistingCatalogRepresentations(t *testing.T) {
	for _, detailed := range []bool{false, true} {
		before := legacySnapshot(detailed)
		original := clone(before)
		current, err := expected(before)
		if err != nil {
			t.Fatal(err)
		}
		projected, err := project(before, current)
		if err != nil || !reflect.DeepEqual(projected, before) || !reflect.DeepEqual(before, original) {
			t.Fatal("explicit approved additions/replacements did not preserve the exact historical snapshot")
		}
		projected["integration_connections/columns"][0] = "must not alias input"
		if current["integration_connections/columns"][0] != original["integration_connections/columns"][0] {
			t.Fatal("projection aliases the observed current snapshot")
		}
	}
}

func TestMissingWeakenedOrUnrelatedDeltaNeverProjects(t *testing.T) {
	before := legacySnapshot(false)
	valid, err := expected(before)
	if err != nil {
		t.Fatal(err)
	}
	columns, constraints := "finding_deliveries/columns", "finding_deliveries/constraints"
	mutations := map[string]func(map[string][]string){
		"missing-target-column": func(v map[string][]string) { v[columns] = slices.Delete(v[columns], 3, 4) },
		"wrong-type":            func(v map[string][]string) { v[columns][3] = "jira_target|text|false|" },
		"wrong-nullability":     func(v map[string][]string) { v[columns][3] = "jira_target|jsonb|true|" },
		"new-default":           func(v map[string][]string) { v[columns][3] += "'{}'::jsonb" },
		"wrong-order":           func(v map[string][]string) { v[columns][3], v[columns][4] = v[columns][4], v[columns][3] },
		"old-default":           func(v map[string][]string) { v[columns][0] += "'changed'" },
		"extra-column":          func(v map[string][]string) { v[columns] = append(v[columns], "extra|text|false|") },
		"extra-check":           func(v map[string][]string) { v[constraints] = append(v[constraints], "extra|CHECK (true)") },
		"missing-typed-check":   func(v map[string][]string) { v[constraints] = slices.Delete(v[constraints], 1, 2) },
		"missing-foreign-key":   func(v map[string][]string) { v[constraints] = slices.Delete(v[constraints], 2, 3) },
		"unknown-profile": func(v map[string][]string) {
			v[constraints][0] = strings.Replace(v[constraints][0], "'jira-cloud-v3'::text", "'other-profile'::text", 1)
		},
		"renamed-profile-check": func(v map[string][]string) {
			v[constraints][0] = strings.Replace(v[constraints][0], "_profile_check|", "_renamed_check|", 1)
		},
		"weakened-target-check": func(v map[string][]string) {
			v[constraints][1] = strings.Replace(v[constraints][1], TargetCheck, "CHECK (true)", 1)
		},
		"not-valid-check": func(v map[string][]string) { v[constraints][1] += " NOT VALID" },
		"changed-index":   func(v map[string][]string) { v["notes/indexes"][0] += " changed" },
		"changed-table":   func(v map[string][]string) { v["notes/columns"][0] = "id|bytea|true|" },
		"removed-table":   func(v map[string][]string) { delete(v, "notes/columns") },
		"added-table":     func(v map[string][]string) { v["new/columns"] = []string{"id|text|true|"} },
	}
	for label, change := range mutations {
		t.Run(label, func(t *testing.T) {
			altered := clone(valid)
			change(altered)
			if _, err := project(before, altered); err == nil {
				t.Fatal("unapproved or missing schema change was silently projected away")
			}
		})
	}
	if _, err := project(before, before); err == nil {
		t.Fatal("unchanged V9 passed as the positively required V10 delta")
	}
}
