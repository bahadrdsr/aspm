//go:build integration

package sourcecompat

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

const V26ExportIDCheck = `CHECK (id ~ '^[0-9a-f]{32}$'::text)`
const V26ExportWorkspaceIDCheck = `CHECK (workspace_id ~ '^[0-9a-f]{32}$'::text)`
const V26ExportSnapshotIDCheck = `CHECK (snapshot_id ~ '^[0-9a-f]{32}$'::text)`
const V26ExportRequesterCheck = `CHECK (requested_by ~ '^[0-9a-f]{32}$'::text)`
const V26ExportWorkerCheck = `CHECK (worker_id IS NULL OR worker_id ~ '^[0-9a-f]{32}$'::text)`
const V26ExportIdempotencyCheck = `CHECK (octet_length(idempotency_key) >= 1 AND octet_length(idempotency_key) <= 256 AND btrim(idempotency_key) <> ''::text)`
const V26ExportFormatCheck = `CHECK (format = ANY (ARRAY['json'::text, 'csv'::text]))`
const V26ExportStateCheck = `CHECK (state = ANY (ARRAY['queued'::text, 'processing'::text, 'succeeded'::text, 'failed'::text]))`
const V26ExportDigestCheck = `CHECK (content_digest IS NULL OR content_digest ~ '^sha256:[0-9a-f]{64}$'::text)`
const V26ExportContentCheck = `CHECK (content IS NULL OR octet_length(content) <= 262144)`
const V26ExportContentSizeCheck = `CHECK (content_size IS NULL OR content_size >= 0 AND content_size <= 262144)`
const V26ExportFailureCodeCheck = `CHECK (failure_code IS NULL OR octet_length(failure_code) >= 1 AND octet_length(failure_code) <= 128 AND btrim(failure_code) <> ''::text)`
const V26ExportFailureMessageCheck = `CHECK (failure_message IS NULL OR octet_length(failure_message) >= 1 AND octet_length(failure_message) <= 1024 AND btrim(failure_message) <> ''::text)`
const V26ExportArtifactCheck = `CHECK (state = 'succeeded'::text AND content IS NOT NULL AND content_digest IS NOT NULL AND content_size IS NOT NULL AND content_size = octet_length(content) OR state <> 'succeeded'::text AND content IS NULL AND content_digest IS NULL AND content_size IS NULL)`
const V26ExportCompletionCheck = `CHECK ((state = ANY (ARRAY['succeeded'::text, 'failed'::text])) = (completed_at IS NOT NULL))`
const V26ExportFailureCheck = `CHECK ((state = 'failed'::text) = (failure_code IS NOT NULL AND failure_message IS NOT NULL))`
const V26ExportLeaseCheck = `CHECK (state = 'processing'::text AND worker_id IS NOT NULL AND fence > 0 AND lease_until IS NOT NULL OR state <> 'processing'::text AND worker_id IS NULL AND lease_until IS NULL)`

const v26ClaimIndexName = "app_report_exports_claim_idx"
const v26ClaimIndexDefinition = `app_report_exports USING btree (available_at, id) WHERE (state = ANY (ARRAY['queued'::text, 'processing'::text]))`

var v26Tables = []string{"report_exports"}

var v26Relations = []string{
	"app_report_exports|r",
	"app_report_exports_claim_idx|i",
	"app_report_exports_pkey|i",
	"app_report_exports_workspace_id_key|i",
	"app_report_exports_workspace_idempotency_key|i",
}

var exactV26NewTableCatalog = map[string][]string{
	"report_exports/columns": {
		"id|text|true|",
		"workspace_id|text|true|",
		"snapshot_id|text|true|",
		"requested_by|text|true|",
		"format|text|true|",
		"idempotency_key|text|true|",
		"state|text|true|'queued'::text",
		"created_at|timestamp with time zone|true|",
		"completed_at|timestamp with time zone|false|",
		"content|bytea|false|",
		"content_digest|text|false|",
		"content_size|bigint|false|",
		"failure_code|text|false|",
		"failure_message|text|false|",
		"worker_id|text|false|",
		"fence|bigint|true|0",
		"attempts|integer|true|0",
		"lease_until|timestamp with time zone|false|",
		"available_at|timestamp with time zone|true|clock_timestamp()",
	},
	"report_exports/constraints": {
		"app_report_exports_attempts_check|CHECK (attempts >= 0 AND attempts <= 3)",
		"app_report_exports_content_check|" + V26ExportContentCheck,
		"app_report_exports_content_digest_check|" + V26ExportDigestCheck,
		"app_report_exports_content_size_check|" + V26ExportContentSizeCheck,
		"app_report_exports_failure_code_check|" + V26ExportFailureCodeCheck,
		"app_report_exports_failure_message_check|" + V26ExportFailureMessageCheck,
		"app_report_exports_fence_check|CHECK (fence >= 0)",
		"app_report_exports_format_check|" + V26ExportFormatCheck,
		"app_report_exports_id_check|" + V26ExportIDCheck,
		"app_report_exports_idempotency_key_check|" + V26ExportIdempotencyCheck,
		"app_report_exports_pkey|PRIMARY KEY (id)",
		"app_report_exports_requested_by_check|" + V26ExportRequesterCheck,
		"app_report_exports_snapshot_id_check|" + V26ExportSnapshotIDCheck,
		"app_report_exports_state_artifact_check|" + V26ExportArtifactCheck,
		"app_report_exports_state_check|" + V26ExportStateCheck,
		"app_report_exports_state_completion_check|" + V26ExportCompletionCheck,
		"app_report_exports_state_failure_check|" + V26ExportFailureCheck,
		"app_report_exports_state_lease_check|" + V26ExportLeaseCheck,
		"app_report_exports_timestamps_check|CHECK (completed_at IS NULL OR completed_at >= created_at)",
		"app_report_exports_worker_id_check|" + V26ExportWorkerCheck,
		"app_report_exports_workspace_id_check|" + V26ExportWorkspaceIDCheck,
		"app_report_exports_workspace_id_fkey|FOREIGN KEY (workspace_id) REFERENCES app_workspaces(id) ON DELETE CASCADE",
		"app_report_exports_workspace_id_key|UNIQUE (workspace_id, id)",
		"app_report_exports_workspace_idempotency_key|UNIQUE (workspace_id, idempotency_key)",
		"app_report_exports_workspace_requester_fkey|FOREIGN KEY (workspace_id, requested_by) REFERENCES app_memberships(workspace_id, user_id)",
		"app_report_exports_workspace_snapshot_fkey|FOREIGN KEY (workspace_id, snapshot_id) REFERENCES app_report_snapshots(workspace_id, id)",
	},
	"report_exports/indexes": {
		"app_report_exports_claim_idx|CREATE INDEX app_report_exports_claim_idx ON " + v26ClaimIndexDefinition,
		"app_report_exports_pkey|CREATE UNIQUE INDEX app_report_exports_pkey ON app_report_exports USING btree (id)",
		"app_report_exports_workspace_id_key|CREATE UNIQUE INDEX app_report_exports_workspace_id_key ON app_report_exports USING btree (workspace_id, id)",
		"app_report_exports_workspace_idempotency_key|CREATE UNIQUE INDEX app_report_exports_workspace_idempotency_key ON app_report_exports USING btree (workspace_id, idempotency_key)",
	},
}

func V26Tables() []string { return slices.Clone(v26Tables) }

func V26CurrentTables() []string {
	result := V25CurrentTables()
	for _, table := range v26Tables {
		if !slices.Contains(result, table) {
			result = append(result, table)
		}
	}
	return result
}

func exactV26Index(row, name, definition string) bool {
	prefix := name + "|CREATE INDEX " + name + " ON "
	if !strings.HasPrefix(row, prefix) {
		return false
	}
	actual := strings.TrimPrefix(row, prefix)
	if actual == definition {
		return true
	}
	schema, qualified, present := strings.Cut(actual, ".")
	return present && observedSchemaName.MatchString(schema) && qualified == definition
}

func expectedV26Catalog(before map[string][]string) (map[string][]string, error) {
	result := clone(before)
	for key, rows := range exactV26NewTableCatalog {
		if _, present := before[key]; present {
			return nil, fmt.Errorf("V26: prior catalog already contains %s", key)
		}
		result[key] = slices.Clone(rows)
	}
	return result, nil
}

func projectV26(before, current map[string][]string) (map[string][]string, error) {
	result := clone(current)
	presentKinds := 0
	for _, kind := range []string{"columns", "constraints", "indexes"} {
		if _, present := current["report_exports/"+kind]; present {
			presentKinds++
		}
	}
	if presentKinds == 0 {
		return result, nil
	}
	if presentKinds != 3 {
		return nil, fmt.Errorf("V26: report_exports catalog is incomplete")
	}
	for _, kind := range []string{"columns", "constraints", "indexes"} {
		key := "report_exports/" + kind
		if _, present := before[key]; present {
			return nil, fmt.Errorf("V26: prior catalog already contains %s", key)
		}
		rows := current[key]
		if kind == "indexes" {
			if !reflect.DeepEqual(rows, exactV26NewTableCatalog[key]) {
				if len(rows) != len(exactV26NewTableCatalog[key]) {
					return nil, fmt.Errorf("V26: report_exports indexes contain a missing or unapproved delta")
				}
				for index, row := range rows {
					want := exactV26NewTableCatalog[key][index]
					if strings.HasPrefix(want, v26ClaimIndexName+"|") {
						if !exactV26Index(row, v26ClaimIndexName, v26ClaimIndexDefinition) {
							return nil, fmt.Errorf("V26: report export claim index definition changed")
						}
					} else if row != want {
						return nil, fmt.Errorf("V26: report_exports indexes contain a missing or unapproved delta")
					}
				}
			}
		} else if !reflect.DeepEqual(rows, exactV26NewTableCatalog[key]) {
			return nil, fmt.Errorf("V26: report_exports %s contains a missing or unapproved delta", kind)
		}
		delete(result, key)
	}
	return result, nil
}

func validateV26Catalog(before, current map[string][]string) error {
	for _, kind := range []string{"columns", "constraints", "indexes"} {
		if _, present := current["report_exports/"+kind]; !present {
			return fmt.Errorf("V26: report_exports catalog is missing")
		}
	}
	projected, err := projectV26(before, current)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(projected, before) {
		return fmt.Errorf("V26: projected catalog did not restore the exact V25 catalog")
	}
	return nil
}

// ProjectV26 validates and projects only durable report export storage.
func ProjectV26(t testing.TB, before, current map[string][]string) map[string][]string {
	t.Helper()
	result, err := projectV26(before, current)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

// ValidateV26Catalog requires the exact supplied V25 catalog plus only V26.
func ValidateV26Catalog(t testing.TB, before, current map[string][]string) {
	t.Helper()
	if err := validateV26Catalog(before, current); err != nil {
		t.Fatal(err)
	}
}

func ProjectV26Current(t testing.TB, current map[string][]string) map[string][]string {
	t.Helper()
	wantKeys := len(V26CurrentTables()) * 3
	if len(current) != wantKeys {
		t.Fatalf("V26: current catalog key count=%d, want %d", len(current), wantKeys)
	}
	before := clone(current)
	for _, kind := range []string{"columns", "constraints", "indexes"} {
		delete(before, "report_exports/"+kind)
	}
	projected, err := projectV26(before, current)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(projected, before) {
		t.Fatal("V26: current catalog did not project to its exact V25 shape")
	}
	ValidateV25CurrentCatalog(t, projected)
	return projected
}

func ValidateV26CurrentCatalog(t testing.TB, current map[string][]string) {
	t.Helper()
	ProjectV26Current(t, current)
}

// ProjectRelationsV26 permits only the report export table and its indexes after V25.
func ProjectRelationsV26(t testing.TB, before, current []string) []string {
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
	want = append(want, v25Relations...)
	want = append(want, v26Relations...)
	missing, unexpected := relationDifference(want, current), relationDifference(current, want)
	if len(want) != len(current) || len(missing) != 0 || len(unexpected) != 0 {
		t.Fatalf("V26: relation/index set contains a missing or unapproved delta; missing=%v unexpected=%v",
			missing, unexpected)
	}
	return slices.Clone(before)
}
