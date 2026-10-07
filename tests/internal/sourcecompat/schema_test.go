//go:build integration

package sourcecompat

import (
	"reflect"
	"testing"
)

func observedV11Catalog() map[string][]string {
	return map[string][]string{
		"source_connections/columns": {"id|text|false|"},
		"source_connections/constraints": {
			"app_source_connections_profile_check|" + LegacyProfileCheck,
		},
		"source_collections/columns": {"id|text|false|"},
		"source_collections/constraints": {
			"app_source_collections_profile_check|" + LegacyProfileCheck,
		},
		"source_repository_assets/columns": {"workspace_id|text|false|"},
		"source_repository_assets/constraints": {
			"app_source_repository_assets_profile_check|" + LegacyProfileCheck,
		},
		"source_collection_records/columns": {"id|text|false|"},
		"source_collection_records/constraints": {
			"app_source_collection_records_kind_check|" + LegacyKindCheck,
		},
	}
}

func TestProjectV12AcceptsOnlyTheExactAdditiveCatalog(t *testing.T) {
	before := observedV11Catalog()
	current, err := expected(before)
	if err != nil {
		t.Fatal(err)
	}
	if got := ProjectV12(t, before, current); !reflect.DeepEqual(got, before) {
		t.Fatal("exact V12 projection did not restore the observed V11 catalog")
	}
	if !reflect.DeepEqual(current["source_connections/columns"],
		[]string{"id|text|false|", "azure_devops_target|jsonb|false|"}) {
		t.Fatal("connection target column was not appended exactly")
	}
	if !reflect.DeepEqual(current["source_collections/columns"],
		[]string{"id|text|false|", "azure_devops_target|jsonb|false|", "azure_devops_selection|jsonb|false|"}) {
		t.Fatal("collection target/selection columns were not appended in order")
	}
}

func TestExpectedV12RejectsMissingOrAmbiguousObservedControls(t *testing.T) {
	for _, mutate := range []func(map[string][]string){
		func(value map[string][]string) {
			value["source_connections/constraints"] = nil
		},
		func(value map[string][]string) {
			value["source_collections/constraints"] = append(value["source_collections/constraints"],
				"app_source_collections_profile_check|"+LegacyProfileCheck)
		},
		func(value map[string][]string) {
			value["source_repository_assets/columns"] = []string{"unsupported"}
		},
	} {
		value := observedV11Catalog()
		mutate(value)
		if _, err := expected(value); err == nil {
			t.Fatal("invalid observed catalog was accepted")
		}
	}
}

func TestProjectRowsV12PreservesCompleteHistoricalRows(t *testing.T) {
	before := map[string][]string{
		"source_connections": {`{"id":"source","profile":"github-cloud-app"}`},
		"source_collections": {`{"id":"collection","profile":"github-cloud-app"}`},
		"assets":             {`{"id":"asset"}`},
	}
	current := map[string][]string{
		"source_connections": {`{"azure_devops_target":null,"id":"source","profile":"github-cloud-app"}`},
		"source_collections": {`{"azure_devops_selection":null,"azure_devops_target":null,"id":"collection","profile":"github-cloud-app"}`},
		"assets":             {`{"id":"asset"}`},
	}
	if got := ProjectRowsV12(t, before, current); !reflect.DeepEqual(got, before) {
		t.Fatal("complete historical rows were not projected without loss")
	}
}
