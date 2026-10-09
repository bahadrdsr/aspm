//go:build integration

package sourcecompat

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

const V27IDCheck = `CHECK (id ~ '^[0-9a-f]{32}$'::text)`
const V27WorkspaceIDCheck = `CHECK (workspace_id ~ '^[0-9a-f]{32}$'::text)`
const V27FindingIDCheck = `CHECK (finding_id ~ '^[0-9a-f]{32}$'::text)`
const V27MemberIDCheck = `CHECK (submitted_by ~ '^[0-9a-f]{32}$'::text)`
const V27MethodCheck = `CHECK (method = 'deterministic-evidence'::text)`
const V27FixtureSchemaCheck = `CHECK (fixture_schema = 'aspm.synthetic-fixture/v1'::text)`
const V27EnvironmentCheck = `CHECK (octet_length(environment_id) >= 1 AND octet_length(environment_id) <= 256 AND btrim(environment_id) <> ''::text)`
const V27ScopeRevisionCheck = `CHECK (octet_length(scope_revision) >= 1 AND octet_length(scope_revision) <= 256 AND btrim(scope_revision) <> ''::text)`
const V27DigestCheck = `CHECK (content_digest ~ '^sha256:[0-9a-f]{64}$'::text)`
const V27EvidenceContentCheck = `CHECK (octet_length(content) >= 1 AND octet_length(content) <= 65536)`
const V27EvidenceSizeCheck = `CHECK (content_size >= 1 AND content_size <= 65536 AND content_size = octet_length(content))`
const V27RationaleCheck = `CHECK (octet_length(rationale) >= 1 AND octet_length(rationale) <= 8192 AND btrim(rationale) <> ''::text)`
const V27RevocationRationaleCheck = `CHECK (revocation_rationale IS NULL OR octet_length(revocation_rationale) >= 1 AND octet_length(revocation_rationale) <= 8192 AND btrim(revocation_rationale) <> ''::text)`
const V27ApprovalExpiryCheck = `CHECK (expires_at > created_at AND expires_at <= (created_at + '24:00:00'::interval))`
const V27ApprovalRevocationCheck = `CHECK (revoked_at IS NULL AND revoked_by IS NULL AND revocation_rationale IS NULL OR revoked_at IS NOT NULL AND revoked_by IS NOT NULL AND revocation_rationale IS NOT NULL AND revoked_at >= created_at)`
const V27JobStateCheck = `CHECK (state = ANY (ARRAY['queued'::text, 'processing'::text, 'succeeded'::text, 'blocked'::text, 'failed'::text, 'cancelled'::text]))`
const V27JobOutcomeCheck = `CHECK (outcome IS NULL OR (outcome = ANY (ARRAY['reproduced'::text, 'not-reproduced'::text])))`
const V27JobResultCheck = `CHECK ((state = 'succeeded'::text) = (outcome IS NOT NULL))`
const V27JobFailureCheck = `CHECK ((state = ANY (ARRAY['blocked'::text, 'failed'::text, 'cancelled'::text])) = (failure_code IS NOT NULL AND failure_message IS NOT NULL))`
const V27JobCompletionCheck = `CHECK ((state = ANY (ARRAY['succeeded'::text, 'blocked'::text, 'failed'::text, 'cancelled'::text])) = (completed_at IS NOT NULL))`
const V27JobLeaseCheck = `CHECK (state = 'processing'::text AND worker_id IS NOT NULL AND fence > 0 AND attempts > 0 AND lease_until IS NOT NULL OR state <> 'processing'::text AND worker_id IS NULL AND lease_until IS NULL)`
const V27JobSafetyCheck = `CHECK (NOT close_finding AND NOT false_positive)`

var v27Tables = []string{
	"verification_evidence",
	"verification_approvals",
	"verification_jobs",
}

var v27Relations = []string{
	"app_verification_approvals|r",
	"app_verification_approvals_binding_key|i",
	"app_verification_approvals_current_idx|i",
	"app_verification_approvals_evidence_idx|i",
	"app_verification_approvals_pkey|i",
	"app_verification_approvals_workspace_id_key|i",
	"app_verification_evidence|r",
	"app_verification_evidence_binding_key|i",
	"app_verification_evidence_finding_idx|i",
	"app_verification_evidence_pkey|i",
	"app_verification_evidence_workspace_id_key|i",
	"app_verification_jobs|r",
	"app_verification_jobs_approval_idx|i",
	"app_verification_jobs_claim_idx|i",
	"app_verification_jobs_finding_idx|i",
	"app_verification_jobs_pkey|i",
	"app_verification_jobs_reclaim_idx|i",
	"app_verification_jobs_workspace_id_key|i",
	"app_verification_jobs_workspace_idempotency_key|i",
}

var exactV27NewTableCatalog = map[string][]string{
	"verification_evidence/columns": {
		"id|text|true|",
		"workspace_id|text|true|",
		"finding_id|text|true|",
		"submitted_by|text|true|",
		"method|text|true|",
		"fixture_schema|text|true|",
		"environment_id|text|true|",
		"scope_revision|text|true|",
		"finding_evidence_revision|bigint|true|",
		"content|bytea|true|",
		"content_digest|text|true|",
		"content_size|bigint|true|",
		"created_at|timestamp with time zone|true|",
	},
	"verification_evidence/constraints": {
		"app_verification_evidence_binding_key|UNIQUE (workspace_id, finding_id, id, method, environment_id, scope_revision, finding_evidence_revision, content_digest)",
		"app_verification_evidence_content_check|" + V27EvidenceContentCheck,
		"app_verification_evidence_content_digest_check|" + V27DigestCheck,
		"app_verification_evidence_content_size_check|" + V27EvidenceSizeCheck,
		"app_verification_evidence_environment_id_check|" + V27EnvironmentCheck,
		"app_verification_evidence_finding_evidence_revision_check|CHECK (finding_evidence_revision > 0)",
		"app_verification_evidence_finding_id_check|" + V27FindingIDCheck,
		"app_verification_evidence_id_check|" + V27IDCheck,
		"app_verification_evidence_method_check|" + V27MethodCheck,
		"app_verification_evidence_pkey|PRIMARY KEY (id)",
		"app_verification_evidence_schema_check|" + V27FixtureSchemaCheck,
		"app_verification_evidence_scope_revision_check|" + V27ScopeRevisionCheck,
		"app_verification_evidence_submitted_by_check|" + V27MemberIDCheck,
		"app_verification_evidence_workspace_finding_fkey|FOREIGN KEY (workspace_id, finding_id) REFERENCES app_findings(workspace_id, id) ON DELETE CASCADE",
		"app_verification_evidence_workspace_id_check|" + V27WorkspaceIDCheck,
		"app_verification_evidence_workspace_id_fkey|FOREIGN KEY (workspace_id) REFERENCES app_workspaces(id) ON DELETE CASCADE",
		"app_verification_evidence_workspace_id_key|UNIQUE (workspace_id, id)",
		"app_verification_evidence_workspace_submitter_fkey|FOREIGN KEY (workspace_id, submitted_by) REFERENCES app_memberships(workspace_id, user_id)",
	},
	"verification_evidence/indexes": {
		"app_verification_evidence_binding_key|CREATE UNIQUE INDEX app_verification_evidence_binding_key ON app_verification_evidence USING btree (workspace_id, finding_id, id, method, environment_id, scope_revision, finding_evidence_revision, content_digest)",
		"app_verification_evidence_finding_idx|CREATE INDEX app_verification_evidence_finding_idx ON app_verification_evidence USING btree (workspace_id, finding_id, id)",
		"app_verification_evidence_pkey|CREATE UNIQUE INDEX app_verification_evidence_pkey ON app_verification_evidence USING btree (id)",
		"app_verification_evidence_workspace_id_key|CREATE UNIQUE INDEX app_verification_evidence_workspace_id_key ON app_verification_evidence USING btree (workspace_id, id)",
	},
	"verification_approvals/columns": {
		"id|text|true|",
		"workspace_id|text|true|",
		"finding_id|text|true|",
		"evidence_id|text|true|",
		"approved_by|text|true|",
		"method|text|true|",
		"environment_id|text|true|",
		"scope_revision|text|true|",
		"finding_evidence_revision|bigint|true|",
		"evidence_digest|text|true|",
		"rationale|text|true|",
		"created_at|timestamp with time zone|true|",
		"expires_at|timestamp with time zone|true|",
		"revoked_at|timestamp with time zone|false|",
		"revoked_by|text|false|",
		"revocation_rationale|text|false|",
	},
	"verification_approvals/constraints": {
		"app_verification_approvals_approved_by_check|CHECK (approved_by ~ '^[0-9a-f]{32}$'::text)",
		"app_verification_approvals_binding_key|UNIQUE (workspace_id, finding_id, id, evidence_id, method, environment_id, scope_revision, finding_evidence_revision, evidence_digest)",
		"app_verification_approvals_environment_id_check|" + V27EnvironmentCheck,
		"app_verification_approvals_evidence_digest_check|CHECK (evidence_digest ~ '^sha256:[0-9a-f]{64}$'::text)",
		"app_verification_approvals_evidence_id_check|CHECK (evidence_id ~ '^[0-9a-f]{32}$'::text)",
		"app_verification_approvals_expires_at_check|" + V27ApprovalExpiryCheck,
		"app_verification_approvals_finding_evidence_revision_check|CHECK (finding_evidence_revision > 0)",
		"app_verification_approvals_finding_id_check|" + V27FindingIDCheck,
		"app_verification_approvals_id_check|" + V27IDCheck,
		"app_verification_approvals_method_check|" + V27MethodCheck,
		"app_verification_approvals_pkey|PRIMARY KEY (id)",
		"app_verification_approvals_rationale_check|" + V27RationaleCheck,
		"app_verification_approvals_revocation_check|" + V27ApprovalRevocationCheck,
		"app_verification_approvals_revocation_rationale_check|" + V27RevocationRationaleCheck,
		"app_verification_approvals_revoked_by_check|CHECK (revoked_by IS NULL OR revoked_by ~ '^[0-9a-f]{32}$'::text)",
		"app_verification_approvals_scope_revision_check|" + V27ScopeRevisionCheck,
		"app_verification_approvals_workspace_approver_fkey|FOREIGN KEY (workspace_id, approved_by) REFERENCES app_memberships(workspace_id, user_id)",
		"app_verification_approvals_workspace_evidence_fkey|FOREIGN KEY (workspace_id, finding_id, evidence_id, method, environment_id, scope_revision, finding_evidence_revision, evidence_digest) REFERENCES app_verification_evidence(workspace_id, finding_id, id, method, environment_id, scope_revision, finding_evidence_revision, content_digest)",
		"app_verification_approvals_workspace_finding_fkey|FOREIGN KEY (workspace_id, finding_id) REFERENCES app_findings(workspace_id, id) ON DELETE CASCADE",
		"app_verification_approvals_workspace_id_check|" + V27WorkspaceIDCheck,
		"app_verification_approvals_workspace_id_fkey|FOREIGN KEY (workspace_id) REFERENCES app_workspaces(id) ON DELETE CASCADE",
		"app_verification_approvals_workspace_id_key|UNIQUE (workspace_id, id)",
		"app_verification_approvals_workspace_revoker_fkey|FOREIGN KEY (workspace_id, revoked_by) REFERENCES app_memberships(workspace_id, user_id)",
	},
	"verification_approvals/indexes": {
		"app_verification_approvals_binding_key|CREATE UNIQUE INDEX app_verification_approvals_binding_key ON app_verification_approvals USING btree (workspace_id, finding_id, id, evidence_id, method, environment_id, scope_revision, finding_evidence_revision, evidence_digest)",
		"app_verification_approvals_current_idx|CREATE INDEX app_verification_approvals_current_idx ON app_verification_approvals USING btree (workspace_id, finding_id, expires_at, id) WHERE (revoked_at IS NULL)",
		"app_verification_approvals_evidence_idx|CREATE INDEX app_verification_approvals_evidence_idx ON app_verification_approvals USING btree (workspace_id, evidence_id, id)",
		"app_verification_approvals_pkey|CREATE UNIQUE INDEX app_verification_approvals_pkey ON app_verification_approvals USING btree (id)",
		"app_verification_approvals_workspace_id_key|CREATE UNIQUE INDEX app_verification_approvals_workspace_id_key ON app_verification_approvals USING btree (workspace_id, id)",
	},
	"verification_jobs/columns": {
		"id|text|true|",
		"workspace_id|text|true|",
		"finding_id|text|true|",
		"approval_id|text|true|",
		"evidence_id|text|true|",
		"requested_by|text|true|",
		"method|text|true|",
		"environment_id|text|true|",
		"scope_revision|text|true|",
		"finding_evidence_revision|bigint|true|",
		"evidence_digest|text|true|",
		"idempotency_key|text|true|",
		"state|text|true|'queued'::text",
		"outcome|text|false|",
		"close_finding|boolean|true|false",
		"false_positive|boolean|true|false",
		"failure_code|text|false|",
		"failure_message|text|false|",
		"created_at|timestamp with time zone|true|",
		"completed_at|timestamp with time zone|false|",
		"worker_id|text|false|",
		"fence|bigint|true|0",
		"attempts|integer|true|0",
		"lease_until|timestamp with time zone|false|",
		"available_at|timestamp with time zone|true|clock_timestamp()",
	},
	"verification_jobs/constraints": {
		"app_verification_jobs_approval_id_check|CHECK (approval_id ~ '^[0-9a-f]{32}$'::text)",
		"app_verification_jobs_attempts_check|CHECK (attempts >= 0 AND attempts <= 3)",
		"app_verification_jobs_environment_id_check|" + V27EnvironmentCheck,
		"app_verification_jobs_evidence_digest_check|CHECK (evidence_digest ~ '^sha256:[0-9a-f]{64}$'::text)",
		"app_verification_jobs_evidence_id_check|CHECK (evidence_id ~ '^[0-9a-f]{32}$'::text)",
		"app_verification_jobs_failure_code_check|CHECK (failure_code IS NULL OR octet_length(failure_code) >= 1 AND octet_length(failure_code) <= 128 AND btrim(failure_code) <> ''::text)",
		"app_verification_jobs_failure_message_check|CHECK (failure_message IS NULL OR octet_length(failure_message) >= 1 AND octet_length(failure_message) <= 1024 AND btrim(failure_message) <> ''::text)",
		"app_verification_jobs_fence_check|CHECK (fence >= 0)",
		"app_verification_jobs_finding_evidence_revision_check|CHECK (finding_evidence_revision > 0)",
		"app_verification_jobs_finding_id_check|" + V27FindingIDCheck,
		"app_verification_jobs_id_check|" + V27IDCheck,
		"app_verification_jobs_idempotency_key_check|CHECK (octet_length(idempotency_key) >= 1 AND octet_length(idempotency_key) <= 256 AND btrim(idempotency_key) <> ''::text)",
		"app_verification_jobs_method_check|" + V27MethodCheck,
		"app_verification_jobs_pkey|PRIMARY KEY (id)",
		"app_verification_jobs_requested_by_check|CHECK (requested_by ~ '^[0-9a-f]{32}$'::text)",
		"app_verification_jobs_scope_revision_check|" + V27ScopeRevisionCheck,
		"app_verification_jobs_state_check|" + V27JobStateCheck,
		"app_verification_jobs_state_completion_check|" + V27JobCompletionCheck,
		"app_verification_jobs_state_failure_check|" + V27JobFailureCheck,
		"app_verification_jobs_state_lease_check|" + V27JobLeaseCheck,
		"app_verification_jobs_state_result_check|" + V27JobResultCheck,
		"app_verification_jobs_state_safety_check|" + V27JobSafetyCheck,
		"app_verification_jobs_timestamps_check|CHECK (completed_at IS NULL OR completed_at >= created_at)",
		"app_verification_jobs_verification_outcome_check|" + V27JobOutcomeCheck,
		"app_verification_jobs_worker_id_check|CHECK (worker_id IS NULL OR worker_id ~ '^[0-9a-f]{32}$'::text)",
		"app_verification_jobs_workspace_approval_fkey|FOREIGN KEY (workspace_id, finding_id, approval_id, evidence_id, method, environment_id, scope_revision, finding_evidence_revision, evidence_digest) REFERENCES app_verification_approvals(workspace_id, finding_id, id, evidence_id, method, environment_id, scope_revision, finding_evidence_revision, evidence_digest)",
		"app_verification_jobs_workspace_finding_fkey|FOREIGN KEY (workspace_id, finding_id) REFERENCES app_findings(workspace_id, id) ON DELETE CASCADE",
		"app_verification_jobs_workspace_id_check|" + V27WorkspaceIDCheck,
		"app_verification_jobs_workspace_id_fkey|FOREIGN KEY (workspace_id) REFERENCES app_workspaces(id) ON DELETE CASCADE",
		"app_verification_jobs_workspace_id_key|UNIQUE (workspace_id, id)",
		"app_verification_jobs_workspace_idempotency_key|UNIQUE (workspace_id, idempotency_key)",
		"app_verification_jobs_workspace_requester_fkey|FOREIGN KEY (workspace_id, requested_by) REFERENCES app_memberships(workspace_id, user_id)",
	},
	"verification_jobs/indexes": {
		"app_verification_jobs_approval_idx|CREATE INDEX app_verification_jobs_approval_idx ON app_verification_jobs USING btree (workspace_id, approval_id, id)",
		"app_verification_jobs_claim_idx|CREATE INDEX app_verification_jobs_claim_idx ON app_verification_jobs USING btree (available_at, id) WHERE (state = 'queued'::text)",
		"app_verification_jobs_finding_idx|CREATE INDEX app_verification_jobs_finding_idx ON app_verification_jobs USING btree (workspace_id, finding_id, id)",
		"app_verification_jobs_pkey|CREATE UNIQUE INDEX app_verification_jobs_pkey ON app_verification_jobs USING btree (id)",
		"app_verification_jobs_reclaim_idx|CREATE INDEX app_verification_jobs_reclaim_idx ON app_verification_jobs USING btree (lease_until, id) WHERE (state = 'processing'::text)",
		"app_verification_jobs_workspace_id_key|CREATE UNIQUE INDEX app_verification_jobs_workspace_id_key ON app_verification_jobs USING btree (workspace_id, id)",
		"app_verification_jobs_workspace_idempotency_key|CREATE UNIQUE INDEX app_verification_jobs_workspace_idempotency_key ON app_verification_jobs USING btree (workspace_id, idempotency_key)",
	},
}

func V27Tables() []string { return slices.Clone(v27Tables) }

func CurrentTables() []string {
	result := V26CurrentTables()
	for _, table := range v27Tables {
		if !slices.Contains(result, table) {
			result = append(result, table)
		}
	}
	return result
}

func exactV27Index(row, want string) bool {
	if row == want {
		return true
	}
	name, expected, present := strings.Cut(want, "|")
	if !present {
		return false
	}
	on := " ON "
	offset := strings.Index(expected, on)
	if offset < 0 {
		return false
	}
	prefix := name + "|" + expected[:offset+len(on)]
	if !strings.HasPrefix(row, prefix) {
		return false
	}
	actual := strings.TrimPrefix(row, prefix)
	expected = expected[offset+len(on):]
	schema, qualified, present := strings.Cut(actual, ".")
	return present && observedSchemaName.MatchString(schema) && qualified == expected
}

func expectedV27Catalog(before map[string][]string) (map[string][]string, error) {
	result := clone(before)
	for key, rows := range exactV27NewTableCatalog {
		if _, present := before[key]; present {
			return nil, fmt.Errorf("V27: prior catalog already contains %s", key)
		}
		result[key] = slices.Clone(rows)
	}
	return result, nil
}

func projectV27(before, current map[string][]string) (map[string][]string, error) {
	result := clone(current)
	for _, table := range v27Tables {
		presentKinds := 0
		for _, kind := range []string{"columns", "constraints", "indexes"} {
			if _, present := current[table+"/"+kind]; present {
				presentKinds++
			}
		}
		if presentKinds == 0 {
			continue
		}
		if presentKinds != 3 {
			return nil, fmt.Errorf("V27: %s catalog is incomplete", table)
		}
		for _, kind := range []string{"columns", "constraints", "indexes"} {
			key := table + "/" + kind
			if _, present := before[key]; present {
				return nil, fmt.Errorf("V27: prior catalog already contains %s", key)
			}
			rows := current[key]
			if kind == "indexes" {
				want := exactV27NewTableCatalog[key]
				if len(rows) != len(want) {
					return nil, fmt.Errorf("V27: %s indexes contain a missing or unapproved delta", table)
				}
				for index := range want {
					if !exactV27Index(rows[index], want[index]) {
						return nil, fmt.Errorf("V27: %s index definition changed", table)
					}
				}
			} else if !reflect.DeepEqual(rows, exactV27NewTableCatalog[key]) {
				return nil, fmt.Errorf("V27: %s %s contains a missing or unapproved delta", table, kind)
			}
			delete(result, key)
		}
	}
	return result, nil
}

func validateV27Catalog(before, current map[string][]string) error {
	for _, table := range v27Tables {
		for _, kind := range []string{"columns", "constraints", "indexes"} {
			if _, present := current[table+"/"+kind]; !present {
				return fmt.Errorf("V27: %s catalog is missing", table)
			}
		}
	}
	projected, err := projectV27(before, current)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(projected, before) {
		return fmt.Errorf("V27: projected catalog did not restore the exact V26 catalog")
	}
	return nil
}

func ProjectV27(t testing.TB, before, current map[string][]string) map[string][]string {
	t.Helper()
	result, err := projectV27(before, current)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func ValidateV27Catalog(t testing.TB, before, current map[string][]string) {
	t.Helper()
	if err := validateV27Catalog(before, current); err != nil {
		t.Fatal(err)
	}
}

func ProjectV27Current(t testing.TB, current map[string][]string) map[string][]string {
	t.Helper()
	wantKeys := len(CurrentTables()) * 3
	if len(current) != wantKeys {
		t.Fatalf("V27: current catalog key count=%d, want %d", len(current), wantKeys)
	}
	before := clone(current)
	for _, table := range v27Tables {
		for _, kind := range []string{"columns", "constraints", "indexes"} {
			delete(before, table+"/"+kind)
		}
	}
	projected, err := projectV27(before, current)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(projected, before) {
		t.Fatal("V27: current catalog did not project to its exact V26 shape")
	}
	ValidateV26CurrentCatalog(t, projected)
	return projected
}

func ValidateCurrentCatalog(t testing.TB, current map[string][]string) {
	t.Helper()
	ProjectV27Current(t, current)
}

func ProjectRelationsV27(t testing.TB, before, current []string) []string {
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
	want = append(want, v27Relations...)
	missing, unexpected := relationDifference(want, current), relationDifference(current, want)
	if len(want) != len(current) || len(missing) != 0 || len(unexpected) != 0 {
		t.Fatalf("V27: relation/index set contains a missing or unapproved delta; missing=%v unexpected=%v",
			missing, unexpected)
	}
	return slices.Clone(before)
}
