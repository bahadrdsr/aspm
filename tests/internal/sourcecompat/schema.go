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
