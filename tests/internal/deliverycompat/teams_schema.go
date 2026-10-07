//go:build integration

package deliverycompat

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/bahadrdsr/aspm/tests/internal/sourcecompat"
)

const TeamsProfileCheck = `CHECK (profile = ANY (ARRAY['slack-workspace-bot'::text, 'jira-cloud-v3'::text, 'teams-workflows-channel'::text]))`
const TeamsTargetCheck = `CHECK (profile = 'slack-workspace-bot'::text AND jira_target IS NULL AND teams_target IS NULL AND channel ~ '^[CG][A-Z0-9]{2,127}$'::text OR profile = 'jira-cloud-v3'::text AND jira_target IS NOT NULL AND jsonb_typeof(jira_target) = 'object'::text AND teams_target IS NULL AND channel = ''::text OR profile = 'teams-workflows-channel'::text AND jira_target IS NULL AND teams_target IS NOT NULL AND jsonb_typeof(teams_target) = 'object'::text AND channel = ''::text)`

// ProjectV11 admits only the specified delta from an observed published V10.
func ProjectV11(t testing.TB, before, current map[string][]string) map[string][]string {
	t.Helper()
	result, err := projectTeams(before, current)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// ProjectCurrent is for approved CURRENT observers, not historical V10 controls.
func ProjectCurrent(t testing.TB, before, current map[string][]string) map[string][]string {
	t.Helper()
	v10, err := expected(before)
	if err != nil {
		t.Fatal(err)
	}
	v11, err := expectedTeams(v10)
	if err != nil {
		t.Fatal(err)
	}
	projected := ProjectV11(t, v10, sourcecompat.ProjectCurrent(t, v11, current))
	return Project(t, before, projected)
}

// ProjectCurrentV10 composes the authentic V10 -> V11 -> V12 -> V13 current delta.
func ProjectCurrentV10(t testing.TB, before, current map[string][]string) map[string][]string {
	t.Helper()
	v11, err := expectedTeams(before)
	if err != nil {
		t.Fatal(err)
	}
	return ProjectV11(t, before, sourcecompat.ProjectCurrent(t, v11, current))
}

func teamsCheck(rows []string, name, old, next string) error {
	count := 0
	for i, row := range rows {
		for _, separator := range []string{"|", "|c|"} {
			if row == name+separator+old {
				rows[i] = name + separator + next
				count++
			}
		}
	}
	if count != 1 {
		return fmt.Errorf("V11 compatibility: exact named %s CHECK missing or ambiguous", name)
	}
	return nil
}

func expectedTeams(before map[string][]string) (map[string][]string, error) {
	result := clone(before)
	for _, table := range []string{"integration_connections", "finding_deliveries"} {
		columns, constraints := table+"/columns", table+"/constraints"
		old := before[columns]
		if len(old) < 2 {
			return nil, fmt.Errorf("V11 compatibility: published %s columns missing", table)
		}
		suffix := ""
		switch strings.Count(old[0], "|") {
		case 3:
		case 5:
			suffix = "||"
		default:
			return nil, fmt.Errorf("V11 compatibility: unsupported %s catalog representation", table)
		}
		tail := []string{"jira_target|jsonb|false|" + suffix}
		if table == "finding_deliveries" {
			tail = append(tail, "create_attempted_at|timestamp with time zone|false|"+suffix)
		}
		if !reflect.DeepEqual(old[len(old)-len(tail):], tail) {
			return nil, fmt.Errorf("V11 compatibility: exact published %s V10 tail missing", table)
		}
		result[columns] = append(result[columns], "teams_target|jsonb|false|"+suffix)
		for _, change := range []struct{ name, old, next string }{
			{"_profile_check", ProfileCheck, TeamsProfileCheck},
			{"_profile_target_check", TargetCheck, TeamsTargetCheck},
		} {
			if err := teamsCheck(result[constraints], "app_"+table+change.name, change.old, change.next); err != nil {
				return nil, err
			}
		}
		sortConstraints(result[constraints])
	}
	return result, nil
}

func projectTeams(before, current map[string][]string) (map[string][]string, error) {
	want, err := expectedTeams(before)
	if err != nil {
		return nil, err
	}
	if !reflect.DeepEqual(current, want) {
		return nil, fmt.Errorf("V11 compatibility: missing required or unapproved catalog delta")
	}
	result := clone(current)
	for _, table := range []string{"integration_connections", "finding_deliveries"} {
		columns, constraints := table+"/columns", table+"/constraints"
		result[columns] = result[columns][:len(result[columns])-1]
		for _, change := range []struct{ name, old, next string }{
			{"_profile_check", TeamsProfileCheck, ProfileCheck},
			{"_profile_target_check", TeamsTargetCheck, TargetCheck},
		} {
			if err := teamsCheck(result[constraints], "app_"+table+change.name, change.old, change.next); err != nil {
				return nil, err
			}
		}
		sortConstraints(result[constraints])
	}
	return result, nil
}
