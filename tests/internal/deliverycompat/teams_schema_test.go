//go:build integration && teams_workflows

package deliverycompat

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestTeamsV11ExactSchemaDelta(t *testing.T) {
	const approvedProfile = `CHECK (profile = ANY (ARRAY['slack-workspace-bot'::text, 'jira-cloud-v3'::text, 'teams-workflows-channel'::text]))`
	const approvedTarget = `CHECK (profile = 'slack-workspace-bot'::text AND jira_target IS NULL AND teams_target IS NULL AND channel ~ '^[CG][A-Z0-9]{2,127}$'::text OR profile = 'jira-cloud-v3'::text AND jira_target IS NOT NULL AND jsonb_typeof(jira_target) = 'object'::text AND teams_target IS NULL AND channel = ''::text OR profile = 'teams-workflows-channel'::text AND jira_target IS NULL AND teams_target IS NOT NULL AND jsonb_typeof(teams_target) = 'object'::text AND channel = ''::text)`
	for _, detailed := range []bool{false, true} {
		suffix, separator := "", "|"
		if detailed {
			suffix, separator = "||", "|c|"
		}
		before, current := map[string][]string{}, map[string][]string{}
		for _, table := range []string{"integration_connections", "finding_deliveries"} {
			prefix := "app_" + table
			before[table+"/columns"] = []string{"id|text|true|" + suffix, "jira_target|jsonb|false|" + suffix}
			if table == "finding_deliveries" {
				before[table+"/columns"] = append(before[table+"/columns"], "create_attempted_at|timestamp with time zone|false|"+suffix)
			}
			before[table+"/constraints"] = []string{
				prefix + "_check" + separator + "CHECK (id <> ''::text)",
				prefix + "_check1" + separator + "CHECK (id <> 'other'::text)",
				prefix + "_profile_check" + separator + ProfileCheck,
				prefix + "_profile_target_check" + separator + TargetCheck,
			}
			before[table+"/indexes"] = []string{prefix + "_pkey|unchanged index"}
			current[table+"/columns"] = append(slices.Clone(before[table+"/columns"]), "teams_target|jsonb|false|"+suffix)
			current[table+"/constraints"] = []string{
				before[table+"/constraints"][0], before[table+"/constraints"][1],
				prefix + "_profile_check" + separator + approvedProfile,
				prefix + "_profile_target_check" + separator + approvedTarget,
			}
			current[table+"/indexes"] = slices.Clone(before[table+"/indexes"])
		}
		savedBefore, savedCurrent := clone(before), clone(current)
		got, err := projectTeams(before, current)
		if err != nil || !reflect.DeepEqual(got, before) || !reflect.DeepEqual(before, savedBefore) ||
			!reflect.DeepEqual(current, savedCurrent) {
			t.Fatal("literal V11 delta failed or mutated an observed catalog")
		}
		got["integration_connections/columns"][0] = "must not alias"
		if !reflect.DeepEqual(current, savedCurrent) {
			t.Fatal("V11 projection aliases the observed catalog")
		}
		columns, constraints := "finding_deliveries/columns", "finding_deliveries/constraints"
		for label, mutate := range map[string]func(map[string][]string){
			"missing-column": func(v map[string][]string) { v[columns] = v[columns][:len(v[columns])-1] },
			"wrong-column":   func(v map[string][]string) { v[columns][3] = "teams_target|text|false|" + suffix },
			"default":        func(v map[string][]string) { v[columns][3] += "'{}'::jsonb" },
			"old-marker":     func(v map[string][]string) { v[columns][2] = "create_attempted_at|text|false|" + suffix },
			"extra-profile": func(v map[string][]string) {
				v[constraints][2] = strings.Replace(v[constraints][2], "'teams-workflows-channel'::text", "'other-profile'::text", 1)
			},
			"weakened-target": func(v map[string][]string) { v[constraints][3] = "app_finding_deliveries_profile_target_check" + separator + "CHECK (true)" },
			"invalid-check":   func(v map[string][]string) { v[constraints][3] += " NOT VALID" },
			"wrong-order": func(v map[string][]string) {
				v[constraints][0], v[constraints][1] = v[constraints][1], v[constraints][0]
			},
			"extra-relation": func(v map[string][]string) { v["new/columns"] = []string{"id|text|true|"} },
			"old-index":      func(v map[string][]string) { v["integration_connections/indexes"][0] += " changed" },
		} {
			altered := clone(current)
			mutate(altered)
			if _, err := projectTeams(before, altered); err == nil {
				t.Fatalf("%s: an unapproved schema delta was projected away", label)
			}
		}
		if _, err := projectTeams(before, before); err == nil {
			t.Fatal("unchanged V10 was accepted as the required V11")
		}
	}
}
