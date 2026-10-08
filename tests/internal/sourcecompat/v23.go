//go:build integration

package sourcecompat

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

const V22RetentionHoldResourceKindCheck = `CHECK (resource_kind = ANY (ARRAY['import'::text, 'observation'::text, 'correlation-event'::text]))`
const V23RetentionHoldResourceKindCheck = `CHECK (resource_kind = ANY (ARRAY['import'::text, 'observation'::text, 'correlation-event'::text, 'finding-decision-event'::text, 'notification-policy-revision'::text, 'finding-change-event'::text, 'notification-policy-event'::text]))`
const V22RetentionPreviewResourceKindCheck = `CHECK (resource_kind = ANY (ARRAY['import'::text, 'observation'::text, 'correlation-event'::text, 'archive-object'::text]))`
const V23RetentionPreviewResourceKindCheck = `CHECK (resource_kind = ANY (ARRAY['import'::text, 'observation'::text, 'correlation-event'::text, 'archive-object'::text, 'finding-decision-event'::text, 'notification-policy-revision'::text, 'finding-change-event'::text, 'notification-policy-event'::text]))`

type v23IndexSpec struct {
	table, name, definition string
}

var v23IndexSpecs = []v23IndexSpec{
	{
		table: "finding_decision_events",
		name:  "app_finding_decision_events_retention_idx",
		definition: "app_finding_decision_events USING btree " +
			"(workspace_id, created_at, id)",
	},
	{
		table: "notification_policy_revisions",
		name:  "app_notification_policy_revisions_retention_idx",
		definition: "app_notification_policy_revisions USING btree " +
			"(workspace_id, created_at, id)",
	},
	{
		table: "finding_change_events",
		name:  "app_finding_change_events_retention_idx",
		definition: "app_finding_change_events USING btree " +
			"(workspace_id, change_at, id)",
	},
	{
		table: "notification_policy_events",
		name:  "app_notification_policy_events_retention_idx",
		definition: "app_notification_policy_events USING btree " +
			"(workspace_id, created_at, id)",
	},
}

var v23Tables = []string{
	"retention_holds",
	"retention_preview_items",
	"finding_decision_events",
	"notification_policy_revisions",
	"finding_change_events",
	"notification_policy_events",
}

var v23Relations = []string{
	"app_finding_change_events_retention_idx|i",
	"app_finding_decision_events_retention_idx|i",
	"app_notification_policy_events_retention_idx|i",
	"app_notification_policy_revisions_retention_idx|i",
}

var exactV23Catalog = func() map[string][]string {
	result := clone(exactV22Catalog)
	for key, rows := range exactV22HistoryRetentionCatalog {
		result[key] = slices.Clone(rows)
	}
	for _, value := range []struct {
		table, prior, current string
	}{
		{"retention_holds", V22RetentionHoldResourceKindCheck, V23RetentionHoldResourceKindCheck},
		{"retention_preview_items", V22RetentionPreviewResourceKindCheck, V23RetentionPreviewResourceKindCheck},
	} {
		key := value.table + "/constraints"
		rows := slices.Clone(result[key])
		if err := replaceV23Constraint(rows, "app_"+value.table+"_resource_kind_check",
			value.prior, value.current); err != nil {
			panic("V23 sourcecompat resource-kind calibration failed")
		}
		result[key] = rows
	}
	for _, spec := range v23IndexSpecs {
		key := spec.table + "/indexes"
		result[key] = append(result[key],
			spec.name+"|CREATE INDEX "+spec.name+" ON "+spec.definition)
		slices.Sort(result[key])
	}
	return result
}()

func V23Tables() []string { return slices.Clone(v23Tables) }

func CurrentTables() []string {
	result := slices.Clone(v23Tables)
	for _, table := range v21Tables {
		if !slices.Contains(result, table) {
			result = append(result, table)
		}
	}
	return result
}

func ExpectedV23Catalog() map[string][]string { return clone(exactV23Catalog) }

func V23IndexNames() []string {
	result := make([]string, 0, len(v23IndexSpecs))
	for _, spec := range v23IndexSpecs {
		result = append(result, spec.name)
	}
	return result
}

func ProjectV23Current(t testing.TB, current map[string][]string) map[string][]string {
	t.Helper()
	wantKeys := len(CurrentTables()) * 3
	if len(current) != wantKeys {
		t.Fatalf("V23: current catalog key count=%d, want %d", len(current), wantKeys)
	}
	prior := clone(current)
	for _, value := range []struct {
		table, prior, current string
	}{
		{"retention_holds", V22RetentionHoldResourceKindCheck, V23RetentionHoldResourceKindCheck},
		{"retention_preview_items", V22RetentionPreviewResourceKindCheck, V23RetentionPreviewResourceKindCheck},
	} {
		key := value.table + "/constraints"
		if err := replaceV23Constraint(prior[key], "app_"+value.table+"_resource_kind_check",
			value.current, value.prior); err != nil {
			t.Fatal(err)
		}
	}
	for _, spec := range v23IndexSpecs {
		key := spec.table + "/indexes"
		kept := make([]string, 0, len(prior[key])-1)
		matches := 0
		for _, row := range prior[key] {
			if strings.HasPrefix(row, spec.name+"|") {
				if !exactV23Index(row, spec) {
					t.Fatalf("V23: %s definition changed", spec.name)
				}
				matches++
				continue
			}
			kept = append(kept, row)
		}
		if matches != 1 {
			t.Fatalf("V23: %s is missing or ambiguous", spec.name)
		}
		prior[key] = kept
	}
	if projected, err := projectV23(prior, current); err != nil {
		t.Fatal(err)
	} else if !reflect.DeepEqual(projected, prior) {
		t.Fatal("V23: current catalog did not project to its exact V22 shape")
	}
	return prior
}

func ValidateCurrentCatalog(t testing.TB, current map[string][]string) {
	t.Helper()
	if !reflect.DeepEqual(current, exactV23Catalog) {
		t.Fatal("V23: current catalog contains a missing or unapproved delta")
	}
	prior := ProjectV23Current(t, current)
	history := map[string][]string{}
	for key := range exactV22HistoryRetentionCatalog {
		history[key] = slices.Clone(prior[key])
	}
	if !reflect.DeepEqual(history, exactV22HistoryRetentionCatalog) {
		t.Fatal("V23: retention and decision-history catalog changed outside the exact delta")
	}
	v22 := map[string][]string{}
	for _, table := range v21Tables {
		for _, kind := range []string{"columns", "constraints", "indexes"} {
			key := table + "/" + kind
			v22[key] = slices.Clone(prior[key])
		}
	}
	ValidateV22Catalog(t, v22)
}

func replaceV23Constraint(rows []string, name, current, prior string) error {
	matches := 0
	for index, row := range rows {
		for _, separator := range []string{"|", "|c|"} {
			if row == name+separator+current {
				rows[index] = name + separator + prior
				matches++
			}
		}
	}
	if matches != 1 {
		return fmt.Errorf("V23: exact named CHECK %s is missing or ambiguous", name)
	}
	return nil
}

func exactV23Index(row string, spec v23IndexSpec) bool {
	prefix := spec.name + "|CREATE INDEX " + spec.name + " ON "
	if !strings.HasPrefix(row, prefix) {
		return false
	}
	actual := strings.TrimPrefix(row, prefix)
	if actual == spec.definition {
		return true
	}
	schema, qualified, present := strings.Cut(actual, ".")
	return present && observedSchemaName.MatchString(schema) && qualified == spec.definition
}

func projectV23(before, current map[string][]string) (map[string][]string, error) {
	result := clone(current)
	for _, value := range []struct {
		table, prior, current string
	}{
		{"retention_holds", V22RetentionHoldResourceKindCheck, V23RetentionHoldResourceKindCheck},
		{"retention_preview_items", V22RetentionPreviewResourceKindCheck, V23RetentionPreviewResourceKindCheck},
	} {
		constraintsKey := value.table + "/constraints"
		if _, present := before[constraintsKey]; !present {
			continue
		}
		for _, kind := range []string{"columns", "indexes"} {
			key := value.table + "/" + kind
			if prior, present := before[key]; present && !reflect.DeepEqual(current[key], prior) {
				return nil, fmt.Errorf("V23: %s %s contains an unapproved delta", value.table, kind)
			}
		}
		rows := slices.Clone(current[constraintsKey])
		if err := replaceV23Constraint(rows, "app_"+value.table+"_resource_kind_check",
			value.current, value.prior); err != nil {
			return nil, err
		}
		if !reflect.DeepEqual(rows, before[constraintsKey]) {
			return nil, fmt.Errorf("V23: %s constraints contain a missing or unapproved delta", value.table)
		}
		result[constraintsKey] = slices.Clone(before[constraintsKey])
	}
	for _, spec := range v23IndexSpecs {
		indexesKey := spec.table + "/indexes"
		prior, present := before[indexesKey]
		if !present {
			continue
		}
		for _, kind := range []string{"columns", "constraints"} {
			key := spec.table + "/" + kind
			if old, present := before[key]; present && !reflect.DeepEqual(current[key], old) {
				return nil, fmt.Errorf("V23: %s %s contains an unapproved delta", spec.table, kind)
			}
		}
		remaining := make([]string, 0, len(prior))
		matches := 0
		for _, row := range current[indexesKey] {
			if strings.HasPrefix(row, spec.name+"|") {
				if !exactV23Index(row, spec) {
					return nil, fmt.Errorf("V23: %s definition changed", spec.name)
				}
				matches++
				continue
			}
			remaining = append(remaining, row)
		}
		if matches != 1 || !reflect.DeepEqual(remaining, prior) {
			return nil, fmt.Errorf("V23: %s is missing, duplicated or accompanied by another index", spec.name)
		}
		result[indexesKey] = slices.Clone(prior)
	}
	return result, nil
}

// ProjectV23 validates and projects only the bounded history-retention preview delta.
func ProjectV23(t testing.TB, before, current map[string][]string) map[string][]string {
	t.Helper()
	result, err := projectV23(before, current)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// ValidateV23Catalog requires the exact supplied V22 catalog plus only the V23 delta.
func ValidateV23Catalog(t testing.TB, before, current map[string][]string) {
	t.Helper()
	if got := ProjectV23(t, before, current); !reflect.DeepEqual(got, before) {
		t.Fatal("V23: projected catalog did not restore the exact V22 catalog")
	}
}

// ProjectRelationsV23 permits only the four bounded candidate indexes after V22.
func ProjectRelationsV23(t testing.TB, before, current []string) []string {
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
	missing, unexpected := relationDifference(want, current), relationDifference(current, want)
	if len(want) != len(current) || len(missing) != 0 || len(unexpected) != 0 {
		t.Fatalf("V23: relation/index set contains a missing or unapproved delta; missing=%v unexpected=%v",
			missing, unexpected)
	}
	return slices.Clone(before)
}
