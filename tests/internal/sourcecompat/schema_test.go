//go:build integration

package sourcecompat

import (
	"reflect"
	"slices"
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

func TestProjectCurrentAcceptsExactV12AndV13Composition(t *testing.T) {
	before := observedV11Catalog()
	before["findings/columns"] = []string{"id|text|true|"}
	before["findings/constraints"] = []string{"app_findings_pkey|PRIMARY KEY (id)"}
	v12, err := expected(before)
	if err != nil {
		t.Fatal(err)
	}
	current, err := expectedV13(v12)
	if err != nil {
		t.Fatal(err)
	}
	if got := ProjectCurrent(t, before, current); !reflect.DeepEqual(got, before) {
		t.Fatal("current V12/V13 projection did not restore the observed legacy catalog")
	}
}

func TestExpectedV13RejectsMissingOrAmbiguousFindingCatalog(t *testing.T) {
	for _, value := range []map[string][]string{
		{"findings/columns": {}, "findings/constraints": {"app_findings_pkey|PRIMARY KEY (id)"}},
		{"findings/columns": {"unsupported"}, "findings/constraints": {"app_findings_pkey|PRIMARY KEY (id)"}},
	} {
		if _, err := expectedV13(value); err == nil {
			t.Fatal("invalid observed V13 finding catalog was accepted")
		}
	}
}

func TestProjectRelationsV13AcceptsOnlyExactCorrelationRelations(t *testing.T) {
	before := []string{"app_findings|r", "app_findings_pkey|i"}
	current := append(slices.Clone(before), v13Relations...)
	slices.Sort(current)
	slices.Reverse(current)
	if got := ProjectRelationsV13(t, before, current); !reflect.DeepEqual(got, before) {
		t.Fatal("exact V13 relation projection did not restore the prior relation set")
	}
}

func TestProjectRelationsV14AcceptsOnlyExactRetentionRelations(t *testing.T) {
	before := []string{"app_findings|r", "app_findings_pkey|i"}
	current := append(slices.Clone(before), v13Relations...)
	current = append(current, v14Relations...)
	slices.Sort(current)
	slices.Reverse(current)
	if got := ProjectRelationsV14(t, before, current); !reflect.DeepEqual(got, before) {
		t.Fatal("exact V14 relation projection did not restore the prior relation set")
	}
}

func TestProjectRelationsV15AcceptsOnlyExactExecutionRelations(t *testing.T) {
	before := []string{"app_findings|r", "app_findings_pkey|i"}
	current := append(slices.Clone(before), v13Relations...)
	current = append(current, v14Relations...)
	current = append(current, v15Relations...)
	slices.Sort(current)
	slices.Reverse(current)
	if got := ProjectRelationsV15(t, before, current); !reflect.DeepEqual(got, before) {
		t.Fatal("exact V15 relation projection did not restore the prior relation set")
	}
}

func TestProjectV14AcceptsOnlyExactLegacyRetentionIndexes(t *testing.T) {
	before := map[string][]string{
		"imports/indexes":             {"app_imports_pkey|unchanged"},
		"observations/indexes":        {"app_observations_pkey|unchanged"},
		"assessment_previews/indexes": {"app_assessment_previews_pkey|unchanged"},
	}
	current := clone(before)
	for _, spec := range v14LegacyIndexes {
		key := spec.table + "/indexes"
		current[key] = append(current[key], spec.name+"|CREATE INDEX "+spec.name+
			" ON acceptance_schema."+spec.definition)
		slices.Sort(current[key])
	}
	if got := ProjectV14(t, before, current); !reflect.DeepEqual(got, before) {
		t.Fatal("exact V14 index projection did not restore the prior catalog")
	}
}

func TestProjectRowsCurrentAddsOnlyExactV12AndV13Defaults(t *testing.T) {
	before := map[string][]string{
		"source_connections": {`{"id":"source","profile":"github-cloud-app"}`},
		"source_collections": {`{"id":"collection","profile":"github-cloud-app"}`},
		"findings":           {`{"id":"finding","workflow_state":"open"}`},
	}
	current := map[string][]string{
		"source_connections": {`{"azure_devops_target":null,"id":"source","profile":"github-cloud-app"}`},
		"source_collections": {`{"azure_devops_selection":null,"azure_devops_target":null,"id":"collection","profile":"github-cloud-app"}`},
		"findings":           {`{"decision_revision":1,"evidence_revision":1,"id":"finding","workflow_state":"open"}`},
	}
	if got := ProjectRowsCurrent(t, before, current); !reflect.DeepEqual(got, before) {
		t.Fatal("complete current rows were not projected without loss")
	}
}
