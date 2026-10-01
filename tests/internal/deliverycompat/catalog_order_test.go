//go:build integration

package deliverycompat

import (
	"reflect"
	"testing"
)

func TestConstraintRowsFollowCatalogNameOrder(t *testing.T) {
	for _, detailed := range []bool{false, true} {
		suffix, separator := "", "|"
		if detailed {
			suffix, separator = "||", "|c|"
		}
		before := legacySnapshot(detailed)
		for _, table := range []string{"integration_connections", "finding_deliveries"} {
			key, prefix := table+"/constraints", "app_"+table
			before[key] = append([]string{
				prefix + "_check" + separator + "CHECK (id <> ''::text)",
				prefix + "_check1" + separator + "CHECK (channel <> ''::text)",
			}, before[key]...)
		}
		current := clone(before)
		for _, table := range []string{"integration_connections", "finding_deliveries"} {
			columns, constraints, prefix := table+"/columns", table+"/constraints", "app_"+table
			current[columns] = append(current[columns], "jira_target|jsonb|false|"+suffix)
			if table == "finding_deliveries" {
				current[columns] = append(current[columns], "create_attempted_at|timestamp with time zone|false|"+suffix)
			}
			current[constraints] = []string{
				before[constraints][0],
				before[constraints][1],
				prefix + "_profile_check" + separator + ProfileCheck,
				prefix + "_profile_target_check" + separator + TargetCheck,
				before[constraints][3],
			}
		}
		observed := clone(current)
		projected, err := project(before, current)
		if err != nil {
			t.Fatalf("exact approved catalog in constraint-name order was rejected: %v", err)
		}
		if !reflect.DeepEqual(projected, before) || !reflect.DeepEqual(current, observed) {
			t.Fatal("projection must preserve complete original rows and observed input order")
		}
		constraints := "finding_deliveries/constraints"
		swapped := clone(current)
		swapped[constraints][0], swapped[constraints][1] = swapped[constraints][1], swapped[constraints][0]
		if _, err := project(before, swapped); err == nil {
			t.Fatal("incorrect observation order was accepted")
		}
		altered := clone(current)
		altered[constraints][0] += " NOT VALID"
		if _, err := project(before, altered); err == nil {
			t.Fatal("changing a prefix-related legacy constraint was accepted")
		}
	}
}
