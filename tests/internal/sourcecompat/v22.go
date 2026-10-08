//go:build integration

package sourcecompat

import (
	"reflect"
	"slices"
	"testing"
)

const V21DeliveryProfileCheck = `CHECK (profile = ANY (ARRAY['slack-workspace-bot'::text, 'jira-cloud-v3'::text, 'teams-workflows-channel'::text]))`
const V22DeliveryProfileCheck = `CHECK (profile = ANY (ARRAY['slack-workspace-bot'::text, 'jira-cloud-v3'::text, 'teams-workflows-channel'::text, 'generic-webhook-v1'::text]))`
const V21DeliveryTargetCheck = `CHECK (profile = 'slack-workspace-bot'::text AND jira_target IS NULL AND teams_target IS NULL AND channel ~ '^[CG][A-Z0-9]{2,127}$'::text OR profile = 'jira-cloud-v3'::text AND jira_target IS NOT NULL AND jsonb_typeof(jira_target) = 'object'::text AND teams_target IS NULL AND channel = ''::text OR profile = 'teams-workflows-channel'::text AND jira_target IS NULL AND teams_target IS NOT NULL AND jsonb_typeof(teams_target) = 'object'::text AND channel = ''::text)`
const V22DeliveryTargetCheck = `CHECK (profile = 'slack-workspace-bot'::text AND jira_target IS NULL AND teams_target IS NULL AND webhook_target IS NULL AND channel ~ '^[CG][A-Z0-9]{2,127}$'::text OR profile = 'jira-cloud-v3'::text AND jira_target IS NOT NULL AND jsonb_typeof(jira_target) = 'object'::text AND teams_target IS NULL AND webhook_target IS NULL AND channel = ''::text OR profile = 'teams-workflows-channel'::text AND jira_target IS NULL AND teams_target IS NOT NULL AND jsonb_typeof(teams_target) = 'object'::text AND webhook_target IS NULL AND channel = ''::text OR profile = 'generic-webhook-v1'::text AND jira_target IS NULL AND teams_target IS NULL AND webhook_target IS NOT NULL AND jsonb_typeof(webhook_target) = 'object'::text AND channel = ''::text)`
const V21NotificationProfileCheck = `CHECK (connection_profile = ANY (ARRAY['slack-workspace-bot'::text, 'teams-workflows-channel'::text, 'jira-cloud-v3'::text]))`
const V22NotificationProfileCheck = `CHECK (connection_profile = ANY (ARRAY['slack-workspace-bot'::text, 'teams-workflows-channel'::text, 'jira-cloud-v3'::text, 'generic-webhook-v1'::text]))`

func replaceV22Constraint(rows []string, name, old, next string) bool {
	matches := 0
	for index, row := range rows {
		for _, separator := range []string{"|", "|c|"} {
			if row == name+separator+old {
				rows[index] = name + separator + next
				matches++
			}
		}
	}
	return matches == 1
}

// ProjectV22 validates and projects only the generic-webhook delta.
func ProjectV22(t testing.TB, before, current map[string][]string) map[string][]string {
	t.Helper()
	result := clone(current)
	for _, table := range []string{"integration_connections", "finding_deliveries"} {
		columnsKey, constraintsKey := table+"/columns", table+"/constraints"
		priorColumns, present := before[columnsKey]
		if !present {
			continue
		}
		currentColumns := current[columnsKey]
		if len(currentColumns) == 0 || currentColumns[len(currentColumns)-1] != "webhook_target|jsonb|false|" &&
			currentColumns[len(currentColumns)-1] != "webhook_target|jsonb|false|||" ||
			slices.Contains(priorColumns, currentColumns[len(currentColumns)-1]) {
			t.Fatalf("V22: %s webhook target column is missing, reordered or ambiguous", table)
		}
		if table == "integration_connections" && len(currentColumns) != len(priorColumns)+1 {
			t.Fatal("V22: integration connection columns contain an unapproved delta")
		}
		result[columnsKey] = slices.Clone(currentColumns[:len(currentColumns)-1])
		rows := slices.Clone(current[constraintsKey])
		if !replaceV22Constraint(rows, "app_"+table+"_profile_check",
			V22DeliveryProfileCheck, V21DeliveryProfileCheck) ||
			!replaceV22Constraint(rows, "app_"+table+"_profile_target_check",
				V22DeliveryTargetCheck, V21DeliveryTargetCheck) {
			t.Fatalf("V22: %s profile or target consistency CHECK is missing or ambiguous", table)
		}
		sortConstraints(rows)
		result[constraintsKey] = rows
	}
	for _, table := range []string{"notification_policies", "notification_policy_revisions"} {
		key := table + "/constraints"
		if _, present := before[key]; !present {
			continue
		}
		rows := slices.Clone(current[key])
		if !replaceV22Constraint(rows, "app_"+table+"_connection_profile_check",
			V22NotificationProfileCheck, V21NotificationProfileCheck) {
			t.Fatalf("V22: %s connection profile CHECK is missing or ambiguous", table)
		}
		sortConstraints(rows)
		result[key] = rows
	}
	return result
}

var exactV22Catalog = func() map[string][]string {
	result := clone(exactV21Catalog)
	for _, table := range []string{"notification_policies", "notification_policy_revisions"} {
		key := table + "/constraints"
		if !replaceV22Constraint(result[key], "app_"+table+"_connection_profile_check",
			V21NotificationProfileCheck, V22NotificationProfileCheck) {
			panic("V22 sourcecompat notification profile calibration failed")
		}
		sortConstraints(result[key])
	}
	return result
}()

// V22Tables returns the exact current notification-policy table set.
func V22Tables() []string { return slices.Clone(v21Tables) }

// ExpectedV22Catalog returns an isolated exact current catalog.
func ExpectedV22Catalog() map[string][]string { return clone(exactV22Catalog) }

// ValidateV22Catalog requires the complete V22 notification-policy catalog.
func ValidateV22Catalog(t testing.TB, current map[string][]string) {
	t.Helper()
	if !reflect.DeepEqual(current, exactV22Catalog) {
		t.Fatal("V22: notification-policy catalog contains a missing or unapproved delta")
	}
}

// ProjectRelationsV22 proves V22 adds no relation or index.
func ProjectRelationsV22(t testing.TB, before, current []string) []string {
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
		t.Fatalf("V22: relation/index set contains a missing or unapproved delta; missing=%v unexpected=%v",
			missing, unexpected)
	}
	return slices.Clone(before)
}
