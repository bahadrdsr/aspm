//go:build integration

package sourcecompat

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

const V23RetentionRunItemResourceKindCheck = `CHECK (resource_kind = ANY (ARRAY['import'::text, 'observation'::text, 'correlation-event'::text, 'archive-object'::text]))`
const V24RetentionRunItemResourceKindCheck = `CHECK (resource_kind = ANY (ARRAY['import'::text, 'observation'::text, 'correlation-event'::text, 'archive-object'::text, 'finding-decision-event'::text, 'notification-policy-revision'::text, 'finding-change-event'::text, 'notification-policy-event'::text]))`
const V23ArchivePublicationResourceKindCheck = `CHECK (resource_kind = ANY (ARRAY['observation'::text, 'correlation-event'::text]))`
const V24ArchivePublicationResourceKindCheck = `CHECK (resource_kind = ANY (ARRAY['observation'::text, 'correlation-event'::text, 'finding-decision-event'::text, 'notification-policy-revision'::text, 'finding-change-event'::text, 'notification-policy-event'::text]))`
const V24DetailAvailabilityCheck = `CHECK (detail_availability = ANY (ARRAY['available'::text, 'archived'::text, 'missing'::text, 'corrupt'::text]))`
const V24DetailRevisionCheck = `CHECK (detail_revision > 0)`
const V24ArchiveReferenceCheck = `CHECK (archive_key IS NULL AND archive_digest IS NULL AND archive_size IS NULL AND archived_at IS NULL OR archive_key IS NOT NULL AND archive_digest IS NOT NULL AND archive_size IS NOT NULL AND archived_at IS NOT NULL)`

var v24HistoryTables = []string{
	"finding_decision_events",
	"notification_policy_revisions",
	"finding_change_events",
	"notification_policy_events",
}

var v24Tables = []string{
	"finding_decision_events",
	"notification_policy_revisions",
	"finding_change_events",
	"notification_policy_events",
	"retention_run_items",
	"archive_publications",
}

var exactV23ExecutionCatalog = map[string][]string{
	"archive_publications/columns": {
		"id|text|true|",
		"workspace_id|text|true|",
		"object_key|text|true|",
		"resource_kind|text|true|",
		"resource_id|text|true|",
		"object_digest|text|true|",
		"object_size|bigint|true|",
		"state|text|true|",
		"revision|bigint|true|1",
		"created_at|timestamp with time zone|true|",
		"updated_at|timestamp with time zone|true|",
		"referenced_at|timestamp with time zone|false|",
		"deleted_at|timestamp with time zone|false|",
	},
	"archive_publications/constraints": {
		"app_archive_publications_check|CHECK ((state = 'referenced'::text) = (referenced_at IS NOT NULL))",
		"app_archive_publications_check1|CHECK ((state = 'deleted'::text) = (deleted_at IS NOT NULL))",
		"app_archive_publications_id_check|CHECK (id ~ '^[0-9a-f]{32}$'::text)",
		"app_archive_publications_object_digest_check|CHECK (object_digest ~ '^sha256:[0-9a-f]{64}$'::text)",
		"app_archive_publications_object_key|UNIQUE (workspace_id, object_key)",
		"app_archive_publications_object_key_check|CHECK (octet_length(object_key) >= 1 AND octet_length(object_key) <= 4096)",
		"app_archive_publications_object_size_check|CHECK (object_size >= 0)",
		"app_archive_publications_pkey|PRIMARY KEY (id)",
		"app_archive_publications_resource_id_check|CHECK (resource_id ~ '^[0-9a-f]{32}$'::text)",
		"app_archive_publications_resource_kind_check|" + V23ArchivePublicationResourceKindCheck,
		"app_archive_publications_revision_check|CHECK (revision > 0)",
		"app_archive_publications_state_check|CHECK (state = ANY (ARRAY['publishing'::text, 'referenced'::text, 'orphan'::text, 'deleted'::text]))",
		"app_archive_publications_workspace_id_fkey|FOREIGN KEY (workspace_id) REFERENCES app_workspaces(id)",
		"app_archive_publications_workspace_id_key|UNIQUE (workspace_id, id)",
	},
	"archive_publications/indexes": {
		"app_archive_publications_object_key|CREATE UNIQUE INDEX app_archive_publications_object_key ON app_archive_publications USING btree (workspace_id, object_key)",
		"app_archive_publications_orphan_idx|CREATE INDEX app_archive_publications_orphan_idx ON app_archive_publications USING btree (workspace_id, state, updated_at, id) WHERE (state = ANY (ARRAY['publishing'::text, 'orphan'::text]))",
		"app_archive_publications_pkey|CREATE UNIQUE INDEX app_archive_publications_pkey ON app_archive_publications USING btree (id)",
		"app_archive_publications_workspace_id_key|CREATE UNIQUE INDEX app_archive_publications_workspace_id_key ON app_archive_publications USING btree (workspace_id, id)",
	},
	"retention_run_items/columns": {
		"id|text|true|",
		"workspace_id|text|true|",
		"run_id|text|true|",
		"ordinal|integer|true|",
		"class|text|true|",
		"resource_kind|text|true|",
		"resource_id|text|true|",
		"action|text|true|",
		"state|text|true|'queued'::text",
		"protected_reasons|jsonb|true|'[]'::jsonb",
		"outcome|text|true|''::text",
		"attempts|integer|true|0",
		"failure_code|text|false|",
		"failure_message|text|false|",
		"started_at|timestamp with time zone|false|",
		"completed_at|timestamp with time zone|false|",
		"object_key|text|false|",
		"object_digest|text|false|",
		"object_revision|bigint|false|",
	},
	"retention_run_items/constraints": {
		"app_retention_run_items_action_check|CHECK (action = ANY (ARRAY['archive-history'::text, 'expire-archive'::text, 'expire-raw-report'::text, 'archive-audit'::text, 'restore-archive'::text, 'delete-orphan'::text]))",
		"app_retention_run_items_attempts_check|CHECK (attempts >= 0)",
		"app_retention_run_items_check|CHECK ((state = ANY (ARRAY['queued'::text, 'processing'::text])) OR completed_at IS NOT NULL)",
		"app_retention_run_items_class_check|CHECK (class = ANY (ARRAY['hot-history'::text, 'archived-evidence'::text, 'raw-report'::text, 'audit'::text, 'orphan-archive'::text]))",
		"app_retention_run_items_id_check|CHECK (id ~ '^[0-9a-f]{32}$'::text)",
		"app_retention_run_items_object_check|CHECK (resource_kind = 'archive-object'::text AND object_key IS NOT NULL AND object_digest ~ '^sha256:[0-9a-f]{64}$'::text AND object_revision > 0 OR resource_kind <> 'archive-object'::text AND object_key IS NULL AND object_digest IS NULL AND object_revision IS NULL)",
		"app_retention_run_items_ordinal_check|CHECK (ordinal >= 0 AND ordinal <= 199)",
		"app_retention_run_items_pkey|PRIMARY KEY (id)",
		"app_retention_run_items_protected_reasons_check|CHECK (jsonb_typeof(protected_reasons) = 'array'::text)",
		"app_retention_run_items_resource_id_check|CHECK (resource_id ~ '^[0-9a-f]{32}$'::text)",
		"app_retention_run_items_resource_key|UNIQUE (workspace_id, run_id, resource_kind, resource_id, action)",
		"app_retention_run_items_resource_kind_check|" + V23RetentionRunItemResourceKindCheck,
		"app_retention_run_items_run_ordinal_key|UNIQUE (workspace_id, run_id, ordinal)",
		"app_retention_run_items_state_check|CHECK (state = ANY (ARRAY['queued'::text, 'processing'::text, 'succeeded'::text, 'protected'::text, 'missing'::text, 'corrupt'::text, 'failed'::text]))",
		"app_retention_run_items_workspace_id_key|UNIQUE (workspace_id, id)",
		"app_retention_run_items_workspace_id_run_id_fkey|FOREIGN KEY (workspace_id, run_id) REFERENCES app_retention_runs(workspace_id, id) ON DELETE CASCADE",
	},
	"retention_run_items/indexes": {
		"app_retention_run_items_pkey|CREATE UNIQUE INDEX app_retention_run_items_pkey ON app_retention_run_items USING btree (id)",
		"app_retention_run_items_resource_key|CREATE UNIQUE INDEX app_retention_run_items_resource_key ON app_retention_run_items USING btree (workspace_id, run_id, resource_kind, resource_id, action)",
		"app_retention_run_items_run_ordinal_key|CREATE UNIQUE INDEX app_retention_run_items_run_ordinal_key ON app_retention_run_items USING btree (workspace_id, run_id, ordinal)",
		"app_retention_run_items_run_state_idx|CREATE INDEX app_retention_run_items_run_state_idx ON app_retention_run_items USING btree (workspace_id, run_id, state, ordinal)",
		"app_retention_run_items_workspace_id_key|CREATE UNIQUE INDEX app_retention_run_items_workspace_id_key ON app_retention_run_items USING btree (workspace_id, id)",
	},
}

func V24Tables() []string { return slices.Clone(v24Tables) }

func CurrentTables() []string {
	result := V23CurrentTables()
	for _, table := range []string{"retention_run_items", "archive_publications"} {
		if !slices.Contains(result, table) {
			result = append(result, table)
		}
	}
	return result
}

func v24ColumnSuffix(columns []string) (string, error) {
	if len(columns) == 0 {
		return "", fmt.Errorf("V24: missing observed columns")
	}
	switch strings.Count(columns[0], "|") {
	case 3:
		return "", nil
	case 5:
		return "||", nil
	default:
		return "", fmt.Errorf("V24: unsupported catalog representation")
	}
}

func replaceV24Constraint(rows []string, name, old, next string) error {
	matches := 0
	for index, row := range rows {
		for _, separator := range []string{"|", "|c|"} {
			if row == name+separator+old {
				rows[index] = name + separator + next
				matches++
			}
		}
	}
	if matches != 1 {
		return fmt.Errorf("V24: exact named CHECK %s is missing or ambiguous", name)
	}
	return nil
}

func expectedV24Catalog(before map[string][]string) (map[string][]string, error) {
	result := clone(before)
	for _, table := range v24HistoryTables {
		columnsKey, constraintsKey := table+"/columns", table+"/constraints"
		columns, present := before[columnsKey]
		if !present {
			continue
		}
		suffix, err := v24ColumnSuffix(columns)
		if err != nil {
			return nil, err
		}
		result[columnsKey] = append(slices.Clone(columns),
			"detail_availability|text|true|'available'::text"+suffix,
			"detail_revision|bigint|true|1"+suffix,
			"archive_key|text|false|"+suffix,
			"archive_digest|text|false|"+suffix,
			"archive_size|bigint|false|"+suffix,
			"archived_at|timestamp with time zone|false|"+suffix)
		constraints := slices.Clone(before[constraintsKey])
		for _, value := range []struct{ name, definition string }{
			{"app_" + table + "_archive_reference_check", V24ArchiveReferenceCheck},
			{"app_" + table + "_detail_availability_check", V24DetailAvailabilityCheck},
			{"app_" + table + "_detail_revision_check", V24DetailRevisionCheck},
		} {
			constraints = append(constraints,
				value.name+constraintSeparator(constraints, value.definition)+value.definition)
		}
		sortConstraints(constraints)
		result[constraintsKey] = constraints
	}
	for _, value := range []struct {
		table, old, next string
	}{
		{"retention_run_items", V23RetentionRunItemResourceKindCheck, V24RetentionRunItemResourceKindCheck},
		{"archive_publications", V23ArchivePublicationResourceKindCheck, V24ArchivePublicationResourceKindCheck},
	} {
		key := value.table + "/constraints"
		rows, present := before[key]
		if !present {
			continue
		}
		rows = slices.Clone(rows)
		if err := replaceV24Constraint(rows, "app_"+value.table+"_resource_kind_check",
			value.old, value.next); err != nil {
			return nil, err
		}
		result[key] = rows
	}
	return result, nil
}

func projectV24(before, current map[string][]string) (map[string][]string, error) {
	want, err := expectedV24Catalog(before)
	if err != nil {
		return nil, err
	}
	result := clone(current)
	for _, table := range v24HistoryTables {
		columnsKey := table + "/columns"
		if _, present := before[columnsKey]; !present {
			continue
		}
		for _, kind := range []string{"columns", "constraints", "indexes"} {
			key := table + "/" + kind
			if !reflect.DeepEqual(current[key], want[key]) {
				return nil, fmt.Errorf("V24: %s %s contains a missing or unapproved delta", table, kind)
			}
			result[key] = slices.Clone(before[key])
		}
	}
	for _, table := range []string{"retention_run_items", "archive_publications"} {
		constraintsKey := table + "/constraints"
		if _, present := before[constraintsKey]; !present {
			continue
		}
		for _, kind := range []string{"columns", "constraints", "indexes"} {
			key := table + "/" + kind
			if !reflect.DeepEqual(current[key], want[key]) {
				return nil, fmt.Errorf("V24: %s %s contains a missing or unapproved delta", table, kind)
			}
			result[key] = slices.Clone(before[key])
		}
	}
	return result, nil
}

// ProjectV24 validates and projects only the history archive execution delta.
func ProjectV24(t testing.TB, before, current map[string][]string) map[string][]string {
	t.Helper()
	result, err := projectV24(before, current)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// ValidateV24Catalog requires the exact supplied V23 catalog plus only V24.
func ValidateV24Catalog(t testing.TB, before, current map[string][]string) {
	t.Helper()
	if got := ProjectV24(t, before, current); !reflect.DeepEqual(got, before) {
		t.Fatal("V24: projected catalog did not restore the exact V23 catalog")
	}
}

var exactV23CurrentCatalogForV24 = func() map[string][]string {
	result := clone(exactV23Catalog)
	for key, rows := range exactV23ExecutionCatalog {
		result[key] = slices.Clone(rows)
	}
	return result
}()

var exactV24Catalog = func() map[string][]string {
	result, err := expectedV24Catalog(exactV23CurrentCatalogForV24)
	if err != nil {
		panic("V24 sourcecompat calibration failed")
	}
	return result
}()

func ExpectedV24Catalog() map[string][]string { return clone(exactV24Catalog) }

func ProjectV24Current(t testing.TB, current map[string][]string) map[string][]string {
	t.Helper()
	wantKeys := len(CurrentTables()) * 3
	if len(current) != wantKeys {
		t.Fatalf("V24: current catalog key count=%d, want %d", len(current), wantKeys)
	}
	if !reflect.DeepEqual(current, exactV24Catalog) {
		t.Fatal("V24: current catalog contains a missing or unapproved delta")
	}
	prior := ProjectV24(t, exactV23CurrentCatalogForV24, current)
	if !reflect.DeepEqual(prior, exactV23CurrentCatalogForV24) {
		t.Fatal("V24: current catalog did not project to its exact V23 shape")
	}
	return prior
}

func ValidateCurrentCatalog(t testing.TB, current map[string][]string) {
	t.Helper()
	prior := ProjectV24Current(t, current)
	v23 := map[string][]string{}
	for key := range exactV23Catalog {
		v23[key] = slices.Clone(prior[key])
	}
	ValidateV23CurrentCatalog(t, v23)
}

// ProjectRelationsV24 proves V24 adds no relation or index after V23.
func ProjectRelationsV24(t testing.TB, before, current []string) []string {
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
		t.Fatalf("V24: relation/index set contains a missing or unapproved delta; missing=%v unexpected=%v",
			missing, unexpected)
	}
	return slices.Clone(before)
}
