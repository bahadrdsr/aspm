//go:build integration

package acceptance

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bahadrdsr/aspm/tests/internal/sourcecompat"
	"github.com/jackc/pgx/v5"
)

const (
	verificationMethod       = "deterministic-evidence"
	verificationSchema       = "aspm.synthetic-fixture/v1"
	verificationEnvironment  = "synthetic-m12-environment"
	verificationScope        = "synthetic-m12-scope-revision-1"
	verificationFixtureLimit = 64 << 10
)

type verificationFailure struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

type verificationEvidenceReceipt struct {
	ID                      string    `json:"id"`
	WorkspaceID             string    `json:"workspaceId"`
	FindingID               string    `json:"findingId"`
	SubmittedBy             string    `json:"submittedBy"`
	Method                  string    `json:"method"`
	Schema                  string    `json:"schema"`
	EnvironmentID           string    `json:"environmentId"`
	ScopeRevision           string    `json:"scopeRevision"`
	FindingEvidenceRevision int64     `json:"findingEvidenceRevision"`
	Digest                  string    `json:"digest"`
	SizeBytes               int64     `json:"sizeBytes"`
	CreatedAt               time.Time `json:"createdAt"`
}

type verificationApprovalReceipt struct {
	ID                      string     `json:"id"`
	WorkspaceID             string     `json:"workspaceId"`
	FindingID               string     `json:"findingId"`
	EvidenceID              string     `json:"evidenceId"`
	ApprovedBy              string     `json:"approvedBy"`
	Method                  string     `json:"method"`
	EnvironmentID           string     `json:"environmentId"`
	ScopeRevision           string     `json:"scopeRevision"`
	FindingEvidenceRevision int64      `json:"findingEvidenceRevision"`
	EvidenceDigest          string     `json:"evidenceDigest"`
	Rationale               string     `json:"rationale"`
	CreatedAt               time.Time  `json:"createdAt"`
	ExpiresAt               time.Time  `json:"expiresAt"`
	RevokedAt               *time.Time `json:"revokedAt"`
	RevokedBy               *string    `json:"revokedBy"`
	RevocationRationale     *string    `json:"revocationRationale"`
	Current                 bool       `json:"current"`
}

type verificationResultReceipt struct {
	Method         string `json:"method"`
	EnvironmentID  string `json:"environmentId"`
	ScopeRevision  string `json:"scopeRevision"`
	EvidenceID     string `json:"evidenceId"`
	EvidenceDigest string `json:"evidenceDigest"`
	Outcome        string `json:"outcome"`
	CloseFinding   bool   `json:"closeFinding"`
	FalsePositive  bool   `json:"falsePositive"`
}

type verificationJobReceipt struct {
	ID                      string                     `json:"id"`
	WorkspaceID             string                     `json:"workspaceId"`
	FindingID               string                     `json:"findingId"`
	ApprovalID              string                     `json:"approvalId"`
	EvidenceID              string                     `json:"evidenceId"`
	RequestedBy             string                     `json:"requestedBy"`
	Method                  string                     `json:"method"`
	EnvironmentID           string                     `json:"environmentId"`
	ScopeRevision           string                     `json:"scopeRevision"`
	FindingEvidenceRevision int64                      `json:"findingEvidenceRevision"`
	EvidenceDigest          string                     `json:"evidenceDigest"`
	State                   string                     `json:"state"`
	CreatedAt               time.Time                  `json:"createdAt"`
	CompletedAt             *time.Time                 `json:"completedAt"`
	Failure                 *verificationFailure       `json:"failure"`
	Result                  *verificationResultReceipt `json:"result"`
}

type verificationEvidenceEnvelope struct {
	APIVersion string                      `json:"apiVersion"`
	DataOrigin string                      `json:"dataOrigin"`
	Evidence   verificationEvidenceReceipt `json:"evidence"`
}

type verificationApprovalEnvelope struct {
	APIVersion string                      `json:"apiVersion"`
	DataOrigin string                      `json:"dataOrigin"`
	Approval   verificationApprovalReceipt `json:"approval"`
}

type verificationJobEnvelope struct {
	APIVersion   string                 `json:"apiVersion"`
	DataOrigin   string                 `json:"dataOrigin"`
	Verification verificationJobReceipt `json:"verification"`
}

type verificationEvidencePage struct {
	APIVersion string                        `json:"apiVersion"`
	DataOrigin string                        `json:"dataOrigin"`
	Items      []verificationEvidenceReceipt `json:"items"`
	Total      int                           `json:"total"`
	NextCursor *string                       `json:"nextCursor"`
}

type verificationApprovalPage struct {
	APIVersion string                        `json:"apiVersion"`
	DataOrigin string                        `json:"dataOrigin"`
	Items      []verificationApprovalReceipt `json:"items"`
	Total      int                           `json:"total"`
	NextCursor *string                       `json:"nextCursor"`
}

type verificationJobPage struct {
	APIVersion string                   `json:"apiVersion"`
	DataOrigin string                   `json:"dataOrigin"`
	Items      []verificationJobReceipt `json:"items"`
	Total      int                      `json:"total"`
	NextCursor *string                  `json:"nextCursor"`
}

type verificationEvidenceRow struct {
	ID, WorkspaceID, FindingID, SubmittedBy, Method, Schema string
	EnvironmentID, ScopeRevision, Digest                    string
	FindingEvidenceRevision, SizeBytes                      int64
	Content                                                 []byte
	CreatedAt                                               time.Time
}

type verificationApprovalRow struct {
	ID, WorkspaceID, FindingID, EvidenceID, ApprovedBy, Method string
	EnvironmentID, ScopeRevision, EvidenceDigest, Rationale    string
	FindingEvidenceRevision                                    int64
	CreatedAt, ExpiresAt                                       time.Time
	RevokedAt                                                  *time.Time
	RevokedBy, RevocationRationale                             *string
}

type verificationJobRow struct {
	ID, WorkspaceID, FindingID, ApprovalID, EvidenceID, RequestedBy string
	Method, EnvironmentID, ScopeRevision, EvidenceDigest            string
	IdempotencyKey, State                                           string
	FindingEvidenceRevision, Fence                                  int64
	Attempts                                                        int
	Outcome, FailureCode, FailureMessage, WorkerID                  *string
	CloseFinding, FalsePositive                                     bool
	CreatedAt, AvailableAt                                          time.Time
	CompletedAt, LeaseUntil                                         *time.Time
}

func verificationBase(findingID string) string {
	return "/api/v1/findings/" + findingID + "/verification"
}

func verificationTable(h *harness, name string) string {
	return pgx.Identifier{h.services.cfg.Schema, "app_" + name}.Sanitize()
}

func verificationJSONKeys(t *testing.T, raw []byte, label string, want ...string) map[string]json.RawMessage {
	t.Helper()
	var value map[string]json.RawMessage
	ok(t, "decode "+label+" object", json.Unmarshal(raw, &value))
	if len(value) != len(want) {
		t.Fatalf("%s keys got %v, want exactly %v", label, value, want)
	}
	for _, key := range want {
		if _, present := value[key]; !present {
			t.Fatalf("%s omitted required key %q", label, key)
		}
	}
	return value
}

func strictVerificationDecode[T any](t *testing.T, raw []byte, target *T, label string) {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	ok(t, "strictly decode "+label, decoder.Decode(target))
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		t.Fatalf("%s response contains trailing JSON", label)
	}
}

func assertVerificationIdentity(t *testing.T, id, label string) {
	t.Helper()
	if len(id) != 32 || strings.ToLower(id) != id {
		t.Fatalf("%s is not an exact lower-case 32-hex identifier", label)
	}
	for _, value := range id {
		if !strings.ContainsRune("0123456789abcdef", value) {
			t.Fatalf("%s is not an exact lower-case 32-hex identifier", label)
		}
	}
}

func assertVerificationEvidence(t *testing.T, item verificationEvidenceReceipt) {
	t.Helper()
	assertVerificationIdentity(t, item.ID, "verification evidence ID")
	assertVerificationIdentity(t, item.WorkspaceID, "verification evidence workspace ID")
	assertVerificationIdentity(t, item.FindingID, "verification evidence finding ID")
	assertVerificationIdentity(t, item.SubmittedBy, "verification evidence submitter ID")
	if item.Method != verificationMethod || item.Schema != verificationSchema ||
		item.EnvironmentID == "" || item.ScopeRevision == "" ||
		item.FindingEvidenceRevision < 1 ||
		!strings.HasPrefix(item.Digest, "sha256:") || len(item.Digest) != 71 ||
		item.SizeBytes < 1 || item.SizeBytes > verificationFixtureLimit || item.CreatedAt.IsZero() {
		t.Fatal("verification evidence receipt lost its exact immutable binding or bound")
	}
}

func assertVerificationApproval(t *testing.T, item verificationApprovalReceipt) {
	t.Helper()
	for label, id := range map[string]string{
		"approval ID": item.ID, "approval workspace ID": item.WorkspaceID,
		"approval finding ID": item.FindingID, "approval evidence ID": item.EvidenceID,
		"approval actor ID": item.ApprovedBy,
	} {
		assertVerificationIdentity(t, id, label)
	}
	if item.Method != verificationMethod || item.EnvironmentID == "" ||
		item.ScopeRevision == "" || item.FindingEvidenceRevision < 1 ||
		!strings.HasPrefix(item.EvidenceDigest, "sha256:") || len(item.EvidenceDigest) != 71 ||
		strings.TrimSpace(item.Rationale) == "" || item.CreatedAt.IsZero() ||
		!item.ExpiresAt.After(item.CreatedAt) ||
		(item.RevokedAt == nil) != (item.RevokedBy == nil) ||
		(item.RevokedAt == nil) != (item.RevocationRationale == nil) ||
		item.Current && item.RevokedAt != nil {
		t.Fatal("verification approval receipt lost its bounded authority or revocation semantics")
	}
}

func assertVerificationJob(t *testing.T, item verificationJobReceipt) {
	t.Helper()
	for label, id := range map[string]string{
		"verification job ID": item.ID, "verification workspace ID": item.WorkspaceID,
		"verification finding ID": item.FindingID, "verification approval ID": item.ApprovalID,
		"verification evidence ID": item.EvidenceID, "verification requester ID": item.RequestedBy,
	} {
		assertVerificationIdentity(t, id, label)
	}
	if item.Method != verificationMethod || item.EnvironmentID == "" ||
		item.ScopeRevision == "" || item.FindingEvidenceRevision < 1 ||
		!strings.HasPrefix(item.EvidenceDigest, "sha256:") || len(item.EvidenceDigest) != 71 ||
		item.CreatedAt.IsZero() {
		t.Fatal("verification job lost its immutable approved binding")
	}
	switch item.State {
	case "queued", "processing":
		if item.CompletedAt != nil || item.Failure != nil || item.Result != nil {
			t.Fatal("nonterminal verification job exposed terminal data")
		}
	case "succeeded":
		if item.CompletedAt == nil || item.Failure != nil || item.Result == nil ||
			item.Result.Method != item.Method || item.Result.EnvironmentID != item.EnvironmentID ||
			item.Result.ScopeRevision != item.ScopeRevision ||
			item.Result.EvidenceID != item.EvidenceID ||
			item.Result.EvidenceDigest != item.EvidenceDigest ||
			(item.Result.Outcome != "reproduced" && item.Result.Outcome != "not-reproduced") ||
			item.Result.CloseFinding || item.Result.FalsePositive {
			t.Fatal("succeeded verification job exposed an unsafe or mismatched result")
		}
	case "blocked", "failed", "cancelled":
		if item.CompletedAt == nil || item.Failure == nil ||
			item.Failure.Code == "" || item.Failure.Message == "" ||
			item.Failure.Retryable || item.Result != nil {
			t.Fatal("non-success terminal verification job did not expose one bounded diagnostic")
		}
	default:
		t.Fatalf("unknown deterministic verification state %q", item.State)
	}
}

func decodeVerificationEvidence(t *testing.T, raw []byte) verificationEvidenceEnvelope {
	t.Helper()
	envelope := verificationJSONKeys(t, raw, "verification evidence envelope",
		"apiVersion", "dataOrigin", "evidence")
	verificationJSONKeys(t, envelope["evidence"], "verification evidence",
		"id", "workspaceId", "findingId", "submittedBy", "method", "schema",
		"environmentId", "scopeRevision", "findingEvidenceRevision", "digest", "sizeBytes", "createdAt")
	var result verificationEvidenceEnvelope
	strictVerificationDecode(t, raw, &result, "verification evidence")
	if result.APIVersion != apiVersion || result.DataOrigin != "live" {
		t.Fatal("verification evidence changed API version or live data origin")
	}
	assertVerificationEvidence(t, result.Evidence)
	return result
}

func decodeVerificationApproval(t *testing.T, raw []byte) verificationApprovalEnvelope {
	t.Helper()
	envelope := verificationJSONKeys(t, raw, "verification approval envelope",
		"apiVersion", "dataOrigin", "approval")
	verificationJSONKeys(t, envelope["approval"], "verification approval",
		"id", "workspaceId", "findingId", "evidenceId", "approvedBy", "method",
		"environmentId", "scopeRevision", "findingEvidenceRevision", "evidenceDigest",
		"rationale", "createdAt", "expiresAt", "revokedAt", "revokedBy",
		"revocationRationale", "current")
	var result verificationApprovalEnvelope
	strictVerificationDecode(t, raw, &result, "verification approval")
	if result.APIVersion != apiVersion || result.DataOrigin != "live" {
		t.Fatal("verification approval changed API version or live data origin")
	}
	assertVerificationApproval(t, result.Approval)
	return result
}

func decodeVerificationJob(t *testing.T, raw []byte) verificationJobEnvelope {
	t.Helper()
	envelope := verificationJSONKeys(t, raw, "verification job envelope",
		"apiVersion", "dataOrigin", "verification")
	item := verificationJSONKeys(t, envelope["verification"], "verification job",
		"id", "workspaceId", "findingId", "approvalId", "evidenceId", "requestedBy",
		"method", "environmentId", "scopeRevision", "findingEvidenceRevision",
		"evidenceDigest", "state", "createdAt", "completedAt", "failure", "result")
	if !bytes.Equal(bytes.TrimSpace(item["failure"]), []byte("null")) {
		verificationJSONKeys(t, item["failure"], "verification failure",
			"code", "message", "retryable")
	}
	if !bytes.Equal(bytes.TrimSpace(item["result"]), []byte("null")) {
		verificationJSONKeys(t, item["result"], "verification result",
			"method", "environmentId", "scopeRevision", "evidenceId", "evidenceDigest",
			"outcome", "closeFinding", "falsePositive")
	}
	var result verificationJobEnvelope
	strictVerificationDecode(t, raw, &result, "verification job")
	if result.APIVersion != apiVersion || result.DataOrigin != "live" {
		t.Fatal("verification job changed API version or live data origin")
	}
	assertVerificationJob(t, result.Verification)
	return result
}

func canonicalVerificationFixture(t *testing.T, environment string, condition bool) []byte {
	t.Helper()
	value := struct {
		Schema        string `json:"schema"`
		EnvironmentID string `json:"environmentId"`
		Condition     bool   `json:"condition"`
	}{verificationSchema, environment, condition}
	data, err := json.Marshal(value)
	ok(t, "encode canonical deterministic fixture", err)
	return data
}

func submitVerificationEvidence(t *testing.T, h *harness, who actor, findingID string,
	environment, scope string, condition bool, want int) *verificationEvidenceEnvelope {
	t.Helper()
	response := h.request(who, http.MethodPost, verificationBase(findingID)+"/evidence", encode(t, object{
		"method": verificationMethod, "environmentId": environment, "scopeRevision": scope,
		"fixture": object{"schema": verificationSchema, "environmentId": environment, "condition": condition},
	}), want)
	if want >= 400 {
		h.decode(response)
		return nil
	}
	result := decodeVerificationEvidence(t, response.Body.Bytes())
	return &result
}

func approveVerificationEvidence(t *testing.T, h *harness, who actor, findingID, evidenceID,
	rationale string, expiresAt time.Time, want int) *verificationApprovalEnvelope {
	t.Helper()
	response := h.request(who, http.MethodPost, verificationBase(findingID)+"/approvals", encode(t, object{
		"evidenceId": evidenceID, "rationale": rationale, "expiresAt": expiresAt,
	}), want)
	if want >= 400 {
		h.decode(response)
		return nil
	}
	result := decodeVerificationApproval(t, response.Body.Bytes())
	return &result
}

func queueVerification(t *testing.T, h *harness, who actor, findingID, approvalID, key string,
	want int) (*verificationJobEnvelope, []byte) {
	t.Helper()
	response := h.request(who, http.MethodPost, verificationBase(findingID)+"/jobs", encode(t, object{
		"approvalId": approvalID, "idempotencyKey": key,
	}), want)
	raw := append([]byte(nil), response.Body.Bytes()...)
	if want >= 400 {
		h.decode(response)
		return nil, raw
	}
	result := decodeVerificationJob(t, raw)
	return &result, raw
}

func getVerification(t *testing.T, h *harness, who actor, findingID, jobID string,
	want int) *verificationJobEnvelope {
	t.Helper()
	response := h.request(who, http.MethodGet,
		verificationBase(findingID)+"/jobs/"+jobID, nil, want)
	if want >= 400 {
		h.decode(response)
		return nil
	}
	result := decodeVerificationJob(t, response.Body.Bytes())
	return &result
}

func readVerificationEvidenceRow(t *testing.T, h *harness, id string) verificationEvidenceRow {
	t.Helper()
	var row verificationEvidenceRow
	ok(t, "read durable verification evidence", h.services.db.QueryRow(h.services.ctx, `SELECT
		id,workspace_id,finding_id,submitted_by,method,fixture_schema,environment_id,scope_revision,
		finding_evidence_revision,content,content_digest,content_size,created_at
		FROM `+verificationTable(h, "verification_evidence")+` WHERE id=$1`, id).Scan(
		&row.ID, &row.WorkspaceID, &row.FindingID, &row.SubmittedBy, &row.Method, &row.Schema,
		&row.EnvironmentID, &row.ScopeRevision, &row.FindingEvidenceRevision, &row.Content,
		&row.Digest, &row.SizeBytes, &row.CreatedAt,
	))
	return row
}

func readVerificationApprovalRow(t *testing.T, h *harness, id string) verificationApprovalRow {
	t.Helper()
	var row verificationApprovalRow
	ok(t, "read durable verification approval", h.services.db.QueryRow(h.services.ctx, `SELECT
		id,workspace_id,finding_id,evidence_id,approved_by,method,environment_id,scope_revision,
		finding_evidence_revision,evidence_digest,rationale,created_at,expires_at,
		revoked_at,revoked_by,revocation_rationale
		FROM `+verificationTable(h, "verification_approvals")+` WHERE id=$1`, id).Scan(
		&row.ID, &row.WorkspaceID, &row.FindingID, &row.EvidenceID, &row.ApprovedBy, &row.Method,
		&row.EnvironmentID, &row.ScopeRevision, &row.FindingEvidenceRevision, &row.EvidenceDigest,
		&row.Rationale, &row.CreatedAt, &row.ExpiresAt, &row.RevokedAt, &row.RevokedBy,
		&row.RevocationRationale,
	))
	return row
}

func readVerificationJobRow(t *testing.T, h *harness, id string) verificationJobRow {
	t.Helper()
	var row verificationJobRow
	ok(t, "read durable verification job", h.services.db.QueryRow(h.services.ctx, `SELECT
		id,workspace_id,finding_id,approval_id,evidence_id,requested_by,method,environment_id,
		scope_revision,finding_evidence_revision,evidence_digest,idempotency_key,state,outcome,
		close_finding,false_positive,failure_code,failure_message,created_at,completed_at,
		worker_id,fence,attempts,lease_until,available_at
		FROM `+verificationTable(h, "verification_jobs")+` WHERE id=$1`, id).Scan(
		&row.ID, &row.WorkspaceID, &row.FindingID, &row.ApprovalID, &row.EvidenceID,
		&row.RequestedBy, &row.Method, &row.EnvironmentID, &row.ScopeRevision,
		&row.FindingEvidenceRevision, &row.EvidenceDigest, &row.IdempotencyKey, &row.State,
		&row.Outcome, &row.CloseFinding, &row.FalsePositive, &row.FailureCode,
		&row.FailureMessage, &row.CreatedAt, &row.CompletedAt, &row.WorkerID, &row.Fence,
		&row.Attempts, &row.LeaseUntil, &row.AvailableAt,
	))
	return row
}

func verificationTableCounts(t *testing.T, h *harness) map[string]int {
	t.Helper()
	result := map[string]int{}
	for _, table := range sourcecompat.V27Tables() {
		var count int
		ok(t, "count "+table, h.services.db.QueryRow(h.services.ctx,
			`SELECT count(*) FROM `+verificationTable(h, table)).Scan(&count))
		result[table] = count
	}
	return result
}

func TestM12_V27MigrationIsExactAdditiveEmptyAndDatabaseOnly(t *testing.T) {
	h, storage := newHistoricalTrendHarness(t, nil, true)
	_, _, seeded := h.seed()
	findingBefore := h.finding(h.admin, seeded.ID)
	stateBefore := historicalState(t, h)

	wantLedger := make([]int, 27)
	for index := range wantLedger {
		wantLedger[index] = index + 1
	}
	if ledger := historicalSchemaVersions(t, h); !reflect.DeepEqual(ledger, wantLedger) {
		t.Fatalf("deterministic verification migration ledger got %v, want exact V27 %v", ledger, wantLedger)
	}
	current := notificationDefinitions(t, h, sourcecompat.CurrentTables())
	sourcecompat.ValidateCurrentCatalog(t, current)
	if counts := verificationTableCounts(t, h); !reflect.DeepEqual(counts, map[string]int{
		"verification_evidence": 0, "verification_approvals": 0, "verification_jobs": 0,
	}) {
		t.Fatalf("V27 migration invented verification evidence, approvals or jobs: %v", counts)
	}

	ok(t, "close V27 application before exact V26 downgrade", h.app.Close())
	h.app = Application{}
	_, err := h.services.db.Exec(h.services.ctx, `DROP TABLE `+
		verificationTable(h, "verification_jobs")+`, `+
		verificationTable(h, "verification_approvals")+`, `+
		verificationTable(h, "verification_evidence")+`;
		DELETE FROM `+verificationTable(h, "schema_versions")+` WHERE version=27`)
	ok(t, "remove only V27 deterministic verification storage", err)
	v26Catalog := notificationDefinitions(t, h, sourcecompat.V26CurrentTables())
	sourcecompat.ValidateV26CurrentCatalog(t, v26Catalog)
	storage.arm()

	h.open()
	if storage.calls.Load() != 0 {
		t.Fatal("V27 migration performed raw/archive object-store or provider I/O")
	}
	after := notificationDefinitions(t, h, sourcecompat.CurrentTables())
	sourcecompat.ValidateCurrentCatalog(t, after)
	if projected := sourcecompat.ProjectV27Current(t, after); !reflect.DeepEqual(projected, v26Catalog) {
		t.Fatal("V27 current catalog did not project to the exact observed V26 catalog")
	}
	if !reflect.DeepEqual(historicalSchemaVersions(t, h), wantLedger) {
		t.Fatal("V27 migration did not restore the exact 1..27 ledger")
	}
	if !reflect.DeepEqual(verificationTableCounts(t, h), map[string]int{
		"verification_evidence": 0, "verification_approvals": 0, "verification_jobs": 0,
	}) {
		t.Fatal("V27 upgrade created default verification rows")
	}
	if !reflect.DeepEqual(h.finding(h.admin, seeded.ID), findingBefore) {
		t.Fatal("V27 migration changed an existing finding")
	}
	stateAfter := historicalState(t, h)
	if !reflect.DeepEqual(stateAfter.Counts, stateBefore.Counts) ||
		!reflect.DeepEqual(stateAfter.Snapshots, stateBefore.Snapshots) {
		t.Fatal("V27 migration changed prior findings, decisions, reports, retention or delivery rows")
	}
	beforeReopen := notificationDefinitions(t, h, sourcecompat.CurrentTables())
	h.restart()
	if !reflect.DeepEqual(historicalSchemaVersions(t, h), wantLedger) ||
		!reflect.DeepEqual(notificationDefinitions(t, h, sourcecompat.CurrentTables()), beforeReopen) ||
		!reflect.DeepEqual(verificationTableCounts(t, h), map[string]int{
			"verification_evidence": 0, "verification_approvals": 0, "verification_jobs": 0,
		}) {
		t.Fatal("V27 reopen repeated migration or changed the exact empty verification catalog")
	}
}

func TestM12_EvidenceApprovalQueueAuthorityCanonicalBytesAndReplay(t *testing.T) {
	h, storage := newHistoricalTrendHarness(t, nil, true)
	_, _, seeded := h.seed()
	analyst := h.addUser(h.admin, "analyst")
	viewer := h.addUser(h.admin, "viewer")
	foreign := h.addWorkspace()
	findingBefore := h.finding(h.admin, seeded.ID)
	countsBefore := historicalState(t, h).Counts
	storage.arm()

	base := verificationBase(seeded.ID)
	h.denied(actor{}, http.MethodPost, base+"/evidence", object{}, http.StatusUnauthorized, "unauthorized")
	h.denied(viewer, http.MethodPost, base+"/evidence", object{
		"method": verificationMethod, "environmentId": verificationEnvironment,
		"scopeRevision": verificationScope,
		"fixture":       object{"schema": verificationSchema, "environmentId": verificationEnvironment, "condition": true},
	}, http.StatusForbidden, "forbidden")
	h.denied(foreign, http.MethodPost, base+"/evidence", object{
		"method": verificationMethod, "environmentId": verificationEnvironment,
		"scopeRevision": verificationScope,
		"fixture":       object{"schema": verificationSchema, "environmentId": verificationEnvironment, "condition": true},
	}, http.StatusNotFound, "not-found")
	for _, body := range []object{
		{"method": "browser-exploitation", "environmentId": verificationEnvironment, "scopeRevision": verificationScope,
			"fixture": object{"schema": verificationSchema, "environmentId": verificationEnvironment, "condition": true}},
		{"method": verificationMethod, "environmentId": verificationEnvironment, "scopeRevision": verificationScope,
			"fixture": object{"schema": "aspm.synthetic-fixture/v2", "environmentId": verificationEnvironment, "condition": true}},
		{"method": verificationMethod, "environmentId": verificationEnvironment, "scopeRevision": verificationScope,
			"fixture": object{"schema": verificationSchema, "environmentId": "different-environment", "condition": true}},
		{"method": verificationMethod, "environmentId": verificationEnvironment, "scopeRevision": verificationScope,
			"fixture": object{"schema": verificationSchema, "environmentId": verificationEnvironment}},
		{"method": verificationMethod, "environmentId": verificationEnvironment, "scopeRevision": "nul\u0000scope",
			"fixture": object{"schema": verificationSchema, "environmentId": verificationEnvironment, "condition": true}},
		{"method": verificationMethod, "environmentId": strings.Repeat("é", 129), "scopeRevision": verificationScope,
			"fixture": object{"schema": verificationSchema, "environmentId": strings.Repeat("é", 129), "condition": true}},
		{"method": verificationMethod, "environmentId": verificationEnvironment, "scopeRevision": verificationScope,
			"fixture": object{"schema": verificationSchema, "environmentId": verificationEnvironment, "condition": true, "targetUrl": "https://synthetic.invalid"}},
		{"method": verificationMethod, "environmentId": verificationEnvironment, "scopeRevision": verificationScope,
			"fixture": object{"schema": verificationSchema, "environmentId": verificationEnvironment, "condition": true},
			"command": "not permitted"},
	} {
		h.denied(analyst, http.MethodPost, base+"/evidence", body, http.StatusBadRequest, "invalid-input")
	}
	oversized := object{
		"method": verificationMethod, "environmentId": verificationEnvironment,
		"scopeRevision": verificationScope,
		"fixture": object{"schema": verificationSchema, "environmentId": verificationEnvironment,
			"condition": true, "padding": strings.Repeat("x", verificationFixtureLimit)},
	}
	h.denied(analyst, http.MethodPost, base+"/evidence", oversized, http.StatusRequestEntityTooLarge, "too-large")

	evidence := submitVerificationEvidence(t, h, analyst, seeded.ID,
		verificationEnvironment, verificationScope, true, http.StatusCreated).Evidence
	if evidence.WorkspaceID != h.admin.workspace || evidence.FindingID != seeded.ID ||
		evidence.SubmittedBy != analyst.user.ID || evidence.FindingEvidenceRevision != findingBefore.EvidenceRevision {
		t.Fatal("evidence intake did not bind the current finding, workspace, actor and evidence revision")
	}
	canonical := canonicalVerificationFixture(t, verificationEnvironment, true)
	row := readVerificationEvidenceRow(t, h, evidence.ID)
	sum := sha256.Sum256(canonical)
	if row.ID != evidence.ID || row.WorkspaceID != h.admin.workspace || row.FindingID != seeded.ID ||
		row.SubmittedBy != analyst.user.ID || row.Method != verificationMethod ||
		row.Schema != verificationSchema || row.EnvironmentID != verificationEnvironment ||
		row.ScopeRevision != verificationScope || row.FindingEvidenceRevision != findingBefore.EvidenceRevision ||
		!bytes.Equal(row.Content, canonical) || row.Digest != fmt.Sprintf("sha256:%x", sum) ||
		row.Digest != evidence.Digest || row.SizeBytes != int64(len(canonical)) ||
		row.SizeBytes != evidence.SizeBytes {
		t.Fatal("evidence intake did not store exact canonical immutable UTF-8 fixture bytes, digest and size")
	}
	if bytes.Contains(canonical, []byte(`"scopeRevision"`)) {
		t.Fatal("the exact internal synthetic fixture unexpectedly contains the external scope binding")
	}

	expires := h.services.cfg.Now().Add(time.Hour)
	h.denied(analyst, http.MethodPost, base+"/approvals", object{
		"evidenceId": evidence.ID, "rationale": "Analyst cannot approve.", "expiresAt": expires,
	}, http.StatusForbidden, "forbidden")
	h.denied(viewer, http.MethodPost, base+"/approvals", object{
		"evidenceId": evidence.ID, "rationale": "Viewer cannot approve.", "expiresAt": expires,
	}, http.StatusForbidden, "forbidden")
	for _, body := range []object{
		{"evidenceId": evidence.ID, "rationale": " ", "expiresAt": expires},
		{"evidenceId": evidence.ID, "rationale": "nul\u0000rationale", "expiresAt": expires},
		{"evidenceId": evidence.ID, "rationale": strings.Repeat("é", 4097), "expiresAt": expires},
		{"evidenceId": evidence.ID, "rationale": "Expired approval.", "expiresAt": h.services.cfg.Now()},
		{"evidenceId": evidence.ID, "rationale": "Unbounded approval.", "expiresAt": h.services.cfg.Now().Add(24*time.Hour + time.Nanosecond)},
		{"evidenceId": strings.Repeat("A", 32), "rationale": "Upper-case ID.", "expiresAt": expires},
		{"evidenceId": evidence.ID, "rationale": "Unknown field.", "expiresAt": expires, "approved": true},
	} {
		h.denied(h.admin, http.MethodPost, base+"/approvals", body, http.StatusBadRequest, "invalid-input")
	}
	const rationale = "Approve only the bounded synthetic fixture condition for deterministic verification."
	approval := approveVerificationEvidence(t, h, h.admin, seeded.ID, evidence.ID,
		rationale, expires, http.StatusCreated).Approval
	if approval.WorkspaceID != h.admin.workspace || approval.FindingID != seeded.ID ||
		approval.EvidenceID != evidence.ID || approval.ApprovedBy != h.admin.user.ID ||
		approval.Method != verificationMethod || approval.EnvironmentID != verificationEnvironment ||
		approval.ScopeRevision != verificationScope ||
		approval.FindingEvidenceRevision != findingBefore.EvidenceRevision ||
		approval.EvidenceDigest != evidence.Digest || approval.Rationale != rationale ||
		!approval.ExpiresAt.Equal(expires) || !approval.Current {
		t.Fatal("admin approval did not retain the exact evidence and current-finding authority")
	}
	approvalRow := readVerificationApprovalRow(t, h, approval.ID)
	if approvalRow.EvidenceID != evidence.ID || approvalRow.ApprovedBy != h.admin.user.ID ||
		approvalRow.EvidenceDigest != evidence.Digest || approvalRow.Rationale != rationale ||
		approvalRow.RevokedAt != nil || approvalRow.RevokedBy != nil ||
		approvalRow.RevocationRationale != nil {
		t.Fatal("approval row was not immutable and unrevoked after creation")
	}

	h.denied(viewer, http.MethodPost, base+"/jobs", object{
		"approvalId": approval.ID, "idempotencyKey": "viewer-denied",
	}, http.StatusForbidden, "forbidden")
	for _, body := range []object{
		{"approvalId": strings.Repeat("A", 32), "idempotencyKey": "upper-approval"},
		{"approvalId": approval.ID, "idempotencyKey": " "},
		{"approvalId": approval.ID, "idempotencyKey": "nul\u0000key"},
		{"approvalId": approval.ID, "idempotencyKey": strings.Repeat("é", 129)},
		{"approvalId": approval.ID, "idempotencyKey": "unknown-field", "execute": true},
		{"approvalId": approval.ID},
	} {
		h.denied(analyst, http.MethodPost, base+"/jobs", body, http.StatusBadRequest, "invalid-input")
	}
	const key = "m12-workspace-idempotent-verification"
	first, firstRaw := queueVerification(t, h, analyst, seeded.ID, approval.ID, key, http.StatusAccepted)
	if first.Verification.State != "queued" || first.Verification.RequestedBy != analyst.user.ID {
		t.Fatal("first verification acceptance did not persist only an ordinary queued job")
	}
	jobRow := readVerificationJobRow(t, h, first.Verification.ID)
	if jobRow.State != "queued" || jobRow.ApprovalID != approval.ID ||
		jobRow.EvidenceID != evidence.ID || jobRow.RequestedBy != analyst.user.ID ||
		jobRow.IdempotencyKey != key || jobRow.Attempts != 0 || jobRow.Fence != 0 ||
		jobRow.CompletedAt != nil || jobRow.Outcome != nil || jobRow.FailureCode != nil ||
		jobRow.FailureMessage != nil || jobRow.WorkerID != nil || jobRow.LeaseUntil != nil ||
		jobRow.CloseFinding || jobRow.FalsePositive {
		t.Fatal("accepted verification request committed anything beyond the durable queued job")
	}
	replay, replayRaw := queueVerification(t, h, h.admin, seeded.ID, approval.ID, key, http.StatusOK)
	if !reflect.DeepEqual(replay.Verification, first.Verification) ||
		!bytes.Equal(firstRaw, replayRaw) || replay.Verification.RequestedBy != analyst.user.ID {
		t.Fatal("exact verification idempotency replay did not return the original receipt and requester")
	}
	secondApproval := approveVerificationEvidence(t, h, h.admin, seeded.ID, evidence.ID,
		"Second bounded approval for changed-binding conflict.", expires, http.StatusCreated).Approval
	h.denied(h.admin, http.MethodPost, base+"/jobs", object{
		"approvalId": secondApproval.ID, "idempotencyKey": key,
	}, http.StatusConflict, "conflict")

	if !reflect.DeepEqual(h.finding(h.admin, seeded.ID), findingBefore) ||
		!reflect.DeepEqual(historicalState(t, h).Counts, countsBefore) {
		t.Fatal("evidence intake, approval or queue changed finding workflow, disposition, source, AI, report or retention state")
	}
	if storage.calls.Load() != 0 {
		t.Fatal("verification intake, approval, queue or replay performed object-store, provider or tool I/O")
	}
}

func decodeVerificationEvidencePage(t *testing.T, raw []byte) verificationEvidencePage {
	t.Helper()
	envelope := verificationJSONKeys(t, raw, "verification evidence page",
		"apiVersion", "dataOrigin", "items", "total", "nextCursor")
	var items []json.RawMessage
	ok(t, "decode verification evidence page items", json.Unmarshal(envelope["items"], &items))
	for _, item := range items {
		verificationJSONKeys(t, item, "verification evidence page item",
			"id", "workspaceId", "findingId", "submittedBy", "method", "schema",
			"environmentId", "scopeRevision", "findingEvidenceRevision", "digest", "sizeBytes", "createdAt")
	}
	var result verificationEvidencePage
	strictVerificationDecode(t, raw, &result, "verification evidence page")
	if result.APIVersion != apiVersion || result.DataOrigin != "live" || result.Items == nil {
		t.Fatal("verification evidence page changed its version, origin or array shape")
	}
	for _, item := range result.Items {
		assertVerificationEvidence(t, item)
	}
	return result
}

func decodeVerificationApprovalPage(t *testing.T, raw []byte) verificationApprovalPage {
	t.Helper()
	envelope := verificationJSONKeys(t, raw, "verification approval page",
		"apiVersion", "dataOrigin", "items", "total", "nextCursor")
	var items []json.RawMessage
	ok(t, "decode verification approval page items", json.Unmarshal(envelope["items"], &items))
	for _, item := range items {
		verificationJSONKeys(t, item, "verification approval page item",
			"id", "workspaceId", "findingId", "evidenceId", "approvedBy", "method",
			"environmentId", "scopeRevision", "findingEvidenceRevision", "evidenceDigest",
			"rationale", "createdAt", "expiresAt", "revokedAt", "revokedBy",
			"revocationRationale", "current")
	}
	var result verificationApprovalPage
	strictVerificationDecode(t, raw, &result, "verification approval page")
	if result.APIVersion != apiVersion || result.DataOrigin != "live" || result.Items == nil {
		t.Fatal("verification approval page changed its version, origin or array shape")
	}
	for _, item := range result.Items {
		assertVerificationApproval(t, item)
	}
	return result
}

func decodeVerificationJobPage(t *testing.T, raw []byte) verificationJobPage {
	t.Helper()
	envelope := verificationJSONKeys(t, raw, "verification job page",
		"apiVersion", "dataOrigin", "items", "total", "nextCursor")
	var items []json.RawMessage
	ok(t, "decode verification job page items", json.Unmarshal(envelope["items"], &items))
	for _, item := range items {
		value := verificationJSONKeys(t, item, "verification job page item",
			"id", "workspaceId", "findingId", "approvalId", "evidenceId", "requestedBy",
			"method", "environmentId", "scopeRevision", "findingEvidenceRevision",
			"evidenceDigest", "state", "createdAt", "completedAt", "failure", "result")
		if !bytes.Equal(bytes.TrimSpace(value["failure"]), []byte("null")) {
			verificationJSONKeys(t, value["failure"], "verification page failure",
				"code", "message", "retryable")
		}
		if !bytes.Equal(bytes.TrimSpace(value["result"]), []byte("null")) {
			verificationJSONKeys(t, value["result"], "verification page result",
				"method", "environmentId", "scopeRevision", "evidenceId", "evidenceDigest",
				"outcome", "closeFinding", "falsePositive")
		}
	}
	var result verificationJobPage
	strictVerificationDecode(t, raw, &result, "verification job page")
	if result.APIVersion != apiVersion || result.DataOrigin != "live" || result.Items == nil {
		t.Fatal("verification job page changed its version, origin or array shape")
	}
	last := ""
	for _, item := range result.Items {
		assertVerificationJob(t, item)
		if item.ID <= last {
			t.Fatal("verification job page is not strictly ordered by lower-case ID")
		}
		last = item.ID
	}
	return result
}

func seedVerificationBinding(t *testing.T, h *harness, who actor, finding finding,
	condition bool, suffix string) (verificationEvidenceReceipt, verificationApprovalReceipt, verificationJobReceipt) {
	t.Helper()
	evidence := submitVerificationEvidence(t, h, who, finding.ID,
		verificationEnvironment+"-"+suffix, verificationScope+"-"+suffix,
		condition, http.StatusCreated).Evidence
	approval := approveVerificationEvidence(t, h, h.admin, finding.ID, evidence.ID,
		"Approve bounded synthetic verification binding "+suffix+".",
		h.services.cfg.Now().Add(time.Hour), http.StatusCreated).Approval
	job, _ := queueVerification(t, h, who, finding.ID, approval.ID,
		"m12-verification-"+suffix, http.StatusAccepted)
	return evidence, approval, job.Verification
}

func TestM12_VerificationHistoryDetailPagingRevocationAndNoFixtureDisclosure(t *testing.T) {
	h, storage := newHistoricalTrendHarness(t, nil, true)
	_, _, seeded := h.seed()
	viewer := h.addUser(h.admin, "viewer")
	evidence, approval, job := seedVerificationBinding(t, h, h.admin, seeded, true, "history")
	storage.arm()

	evidencePage := decodeVerificationEvidencePage(t, h.request(viewer, http.MethodGet,
		verificationBase(seeded.ID)+"/evidence?limit=100", nil, http.StatusOK).Body.Bytes())
	approvalPage := decodeVerificationApprovalPage(t, h.request(viewer, http.MethodGet,
		verificationBase(seeded.ID)+"/approvals?limit=100", nil, http.StatusOK).Body.Bytes())
	jobPage := decodeVerificationJobPage(t, h.request(viewer, http.MethodGet,
		verificationBase(seeded.ID)+"/jobs?limit=100", nil, http.StatusOK).Body.Bytes())
	if evidencePage.Total != 1 || len(evidencePage.Items) != 1 || evidencePage.NextCursor != nil ||
		evidencePage.Items[0].ID != evidence.ID ||
		approvalPage.Total != 1 || len(approvalPage.Items) != 1 || approvalPage.NextCursor != nil ||
		approvalPage.Items[0].ID != approval.ID ||
		jobPage.Total != 1 || len(jobPage.Items) != 1 || jobPage.NextCursor != nil ||
		jobPage.Items[0].ID != job.ID {
		t.Fatal("viewer could not read the exact bounded native verification history")
	}
	canonical := canonicalVerificationFixture(t, evidence.EnvironmentID, true)
	for _, raw := range [][]byte{
		h.request(viewer, http.MethodGet, verificationBase(seeded.ID)+"/evidence?limit=100", nil, http.StatusOK).Body.Bytes(),
		h.request(viewer, http.MethodGet, verificationBase(seeded.ID)+"/approvals?limit=100", nil, http.StatusOK).Body.Bytes(),
		h.request(viewer, http.MethodGet, verificationBase(seeded.ID)+"/jobs?limit=100", nil, http.StatusOK).Body.Bytes(),
		h.request(viewer, http.MethodGet, verificationBase(seeded.ID)+"/jobs/"+job.ID, nil, http.StatusOK).Body.Bytes(),
	} {
		if bytes.Contains(raw, canonical) || bytes.Contains(raw, []byte(`"condition":true`)) {
			t.Fatal("verification history or detail disclosed raw synthetic fixture bytes")
		}
	}
	for _, path := range []string{
		verificationBase(seeded.ID) + "/evidence?limit=0",
		verificationBase(seeded.ID) + "/evidence?cursor=",
		verificationBase(seeded.ID) + "/approvals?limit=101",
		verificationBase(seeded.ID) + "/jobs?limit=1.0",
		verificationBase(seeded.ID) + "/jobs?cursor=" + strings.Repeat("A", 32),
		verificationBase(seeded.ID) + "/jobs?page=2",
	} {
		h.denied(viewer, http.MethodGet, path, nil, http.StatusBadRequest, "invalid-input")
	}
	h.denied(viewer, http.MethodPatch, verificationBase(seeded.ID)+"/evidence/"+evidence.ID,
		object{}, http.StatusMethodNotAllowed, "method-not-allowed")
	h.denied(viewer, http.MethodDelete, verificationBase(seeded.ID)+"/approvals/"+approval.ID,
		nil, http.StatusMethodNotAllowed, "method-not-allowed")
	h.denied(viewer, http.MethodPost, verificationBase(seeded.ID)+"/approvals/"+approval.ID+"/revoke",
		object{"rationale": "Viewer cannot revoke."}, http.StatusForbidden, "forbidden")

	const revokeReason = "Revoke synthetic verification authority and cancel every active bound job."
	revokedResponse := h.request(h.admin, http.MethodPost,
		verificationBase(seeded.ID)+"/approvals/"+approval.ID+"/revoke",
		encode(t, object{"rationale": revokeReason}), http.StatusOK)
	revoked := decodeVerificationApproval(t, revokedResponse.Body.Bytes()).Approval
	if revoked.Current || revoked.RevokedAt == nil || revoked.RevokedBy == nil ||
		*revoked.RevokedBy != h.admin.user.ID || revoked.RevocationRationale == nil ||
		*revoked.RevocationRationale != revokeReason {
		t.Fatal("approval revocation did not retain immutable history and current denial")
	}
	cancelled := getVerification(t, h, viewer, seeded.ID, job.ID, http.StatusOK).Verification
	if cancelled.State != "cancelled" || cancelled.Failure == nil ||
		cancelled.Failure.Code != "approval-revoked" || cancelled.Result != nil {
		t.Fatal("approval revocation did not actively cancel queued verification without publishing success")
	}
	row := readVerificationApprovalRow(t, h, approval.ID)
	if row.RevokedAt == nil || row.RevokedBy == nil || *row.RevokedBy != h.admin.user.ID ||
		row.RevocationRationale == nil || *row.RevocationRationale != revokeReason {
		t.Fatal("approval revocation row did not preserve actor, reason and history")
	}
	h.denied(h.admin, http.MethodPost,
		verificationBase(seeded.ID)+"/approvals/"+approval.ID+"/revoke",
		object{"rationale": revokeReason}, http.StatusConflict, "conflict")
	h.denied(h.admin, http.MethodPost, verificationBase(seeded.ID)+"/jobs", object{
		"approvalId": approval.ID, "idempotencyKey": "revoked-approval-denied",
	}, http.StatusConflict, "conflict")
	if storage.calls.Load() != 0 {
		t.Fatal("verification history, detail or revocation performed object-store, provider or tool I/O")
	}
}

type verificationTraceRole struct{}
type verificationReadMarker struct{}

type verificationGate struct {
	entered chan struct{}
	release chan struct{}
	enter   sync.Once
	open    sync.Once
}

func newVerificationGate() *verificationGate {
	return &verificationGate{entered: make(chan struct{}), release: make(chan struct{})}
}

func (g *verificationGate) block(ctx context.Context) error {
	g.enter.Do(func() { close(g.entered) })
	select {
	case <-g.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (g *verificationGate) unblock() {
	g.open.Do(func() { close(g.release) })
}

type verificationWorkerTracer struct {
	gate *verificationGate
	once sync.Once
	hits atomic.Int32
}

func newVerificationWorkerTracer() *verificationWorkerTracer {
	return &verificationWorkerTracer{gate: newVerificationGate()}
}

func (t *verificationWorkerTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn,
	data pgx.TraceQueryStartData) context.Context {
	role, _ := ctx.Value(verificationTraceRole{}).(string)
	sql := strings.ToLower(strings.Join(strings.Fields(data.SQL), " "))
	if role == "verification-worker" && strings.Contains(sql, "select") &&
		strings.Contains(sql, "app_verification_evidence") &&
		strings.Contains(sql, "content") {
		t.hits.Add(1)
		return context.WithValue(ctx, verificationReadMarker{}, true)
	}
	return ctx
}

func (t *verificationWorkerTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn,
	data pgx.TraceQueryEndData) {
	if data.Err == nil && ctx.Value(verificationReadMarker{}) == true {
		t.once.Do(func() { _ = t.gate.block(ctx) })
	}
}

func openVerificationWorker(t *testing.T, h *harness, tracer pgx.QueryTracer) VerificationWorker {
	t.Helper()
	requireVerificationWorker(t)
	worker, err := Production.OpenVerificationWorker(h.services.ctx, VerificationWorkerConfig{
		DatabaseURL: h.services.cfg.DatabaseURL, Schema: h.services.cfg.Schema,
		ApplicationName: h.services.cfg.ApplicationName + "-verification",
		MaxConnections:  2, WorkerID: strings.Repeat("d", 32),
		LeaseDuration: 2 * time.Second, AuthorizationInterval: 25 * time.Millisecond,
		MaxFixtureBytes: verificationFixtureLimit, Now: h.services.cfg.Now,
		LogOutput: io.Discard, QueryTracer: tracer,
	})
	ok(t, "open database-only deterministic verification worker", err)
	if worker.ProcessNext == nil || worker.Ping == nil || worker.Close == nil {
		t.Fatal("verification worker binding omitted ProcessNext, Ping or Close")
	}
	ok(t, "ping deterministic verification worker", worker.Ping(h.services.ctx))
	t.Cleanup(func() { ok(t, "close deterministic verification worker", worker.Close()) })
	return worker
}

func processVerification(t *testing.T, h *harness, worker VerificationWorker) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(h.services.ctx, 5*time.Second)
	defer cancel()
	worked, err := worker.ProcessNext(ctx)
	ok(t, "process one deterministic verification job", err)
	return worked
}

func TestM12_VerificationWorkerPublishesOnlyBoundedSyntheticOutcomes(t *testing.T) {
	h, storage := newHistoricalTrendHarness(t, nil, true)
	_, _, seeded := h.seed()
	analyst := h.addUser(h.admin, "analyst")
	findingBefore := h.finding(h.admin, seeded.ID)
	countsBefore := historicalState(t, h).Counts
	_, _, reproducedJob := seedVerificationBinding(t, h, analyst, seeded, true, "reproduced")
	storage.arm()
	worker := openVerificationWorker(t, h, nil)

	if !processVerification(t, h, worker) {
		t.Fatal("verification worker did not claim the queued synthetic fixture")
	}
	reproduced := getVerification(t, h, analyst, seeded.ID, reproducedJob.ID, http.StatusOK).Verification
	if reproduced.State != "succeeded" || reproduced.Result == nil ||
		reproduced.Result.Outcome != "reproduced" ||
		reproduced.Result.CloseFinding || reproduced.Result.FalsePositive {
		t.Fatal("true synthetic fixture did not produce the exact safe reproduced result")
	}
	row := readVerificationJobRow(t, h, reproduced.ID)
	if row.State != "succeeded" || row.Attempts != 1 || row.Fence < 1 ||
		row.Outcome == nil || *row.Outcome != "reproduced" ||
		row.CompletedAt == nil || row.WorkerID != nil || row.LeaseUntil != nil ||
		row.FailureCode != nil || row.CloseFinding || row.FalsePositive {
		t.Fatal("verification success was not committed atomically under the live worker fence")
	}
	succeededBefore := row
	if processVerification(t, h, worker) {
		t.Fatal("verification worker retried a succeeded job")
	}
	if !reflect.DeepEqual(readVerificationJobRow(t, h, reproduced.ID), succeededBefore) {
		t.Fatal("empty worker pass changed a succeeded verification job")
	}

	_, _, absentJob := seedVerificationBinding(t, h, analyst, seeded, false, "not-reproduced")
	if !processVerification(t, h, worker) {
		t.Fatal("verification worker did not claim the second synthetic fixture")
	}
	absent := getVerification(t, h, analyst, seeded.ID, absentJob.ID, http.StatusOK).Verification
	if absent.State != "succeeded" || absent.Result == nil ||
		absent.Result.Outcome != "not-reproduced" ||
		absent.Result.CloseFinding || absent.Result.FalsePositive {
		t.Fatal("false synthetic fixture did not produce the exact safe not-reproduced result")
	}
	if !reflect.DeepEqual(h.finding(h.admin, seeded.ID), findingBefore) ||
		!reflect.DeepEqual(historicalState(t, h).Counts, countsBefore) {
		t.Fatal("verification outcome mutated finding workflow, disposition, false-positive, source, AI or safety state")
	}
	if storage.calls.Load() != 0 {
		t.Fatal("database-only verification worker performed storage, network, provider or tool I/O")
	}
}

func TestM12_VerificationRevocationExpiryAndBindingChangesFenceSuccess(t *testing.T) {
	t.Run("revocation actively cancels processing and fences publication", func(t *testing.T) {
		tracer := newVerificationWorkerTracer()
		h, storage := newHistoricalTrendHarness(t, nil, true)
		_, _, seeded := h.seed()
		_, approval, job := seedVerificationBinding(t, h, h.admin, seeded, true, "revoke-processing")
		storage.arm()
		worker := openVerificationWorker(t, h, tracer)
		ctx, cancel := context.WithTimeout(
			context.WithValue(h.services.ctx, verificationTraceRole{}, "verification-worker"), 10*time.Second)
		defer cancel()
		done := make(chan struct{})
		var worked bool
		var workerErr error
		go func() {
			defer close(done)
			worked, workerErr = worker.ProcessNext(ctx)
		}()
		select {
		case <-tracer.gate.entered:
		case <-done:
			t.Fatalf("verification worker returned before the held evidence read: worked=%t err=%v", worked, workerErr)
		case <-time.After(5 * time.Second):
			t.Fatal("verification worker did not reach the held database evidence read")
		}
		processing := readVerificationJobRow(t, h, job.ID)
		if processing.State != "processing" || processing.WorkerID == nil ||
			processing.Fence < 1 || processing.Attempts != 1 || processing.LeaseUntil == nil {
			t.Fatal("verification claim did not establish one bounded processing lease and fence")
		}
		h.request(h.admin, http.MethodPost,
			verificationBase(seeded.ID)+"/approvals/"+approval.ID+"/revoke",
			encode(t, object{"rationale": "Stop the held synthetic fixture before result publication."}),
			http.StatusOK)
		cancelled := readVerificationJobRow(t, h, job.ID)
		if cancelled.State != "cancelled" || cancelled.CompletedAt == nil ||
			cancelled.FailureCode == nil || *cancelled.FailureCode != "approval-revoked" ||
			cancelled.Outcome != nil || cancelled.Fence <= processing.Fence {
			t.Fatal("revocation did not atomically cancel and fence the in-flight verification")
		}
		tracer.gate.unblock()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("revoked verification worker did not stop after its held read was released")
		}
		if workerErr != nil && !errors.Is(workerErr, context.Canceled) {
			t.Fatalf("revoked stale worker returned an unexpected error: %v", workerErr)
		}
		after := readVerificationJobRow(t, h, job.ID)
		if after.State != "cancelled" || after.Outcome != nil || after.Fence != cancelled.Fence {
			t.Fatal("stale verification fence published success after approval revocation")
		}
		if tracer.hits.Load() != 1 || storage.calls.Load() != 0 {
			t.Fatal("revoked verification retried evidence or performed non-database I/O")
		}
	})

	t.Run("expiry and changed evidence revision block queued work", func(t *testing.T) {
		h, storage := newHistoricalTrendHarness(t, nil, true)
		_, _, seeded := h.seed()
		evidence := submitVerificationEvidence(t, h, h.admin, seeded.ID,
			verificationEnvironment+"-expiry", verificationScope+"-expiry",
			true, http.StatusCreated).Evidence
		approval := approveVerificationEvidence(t, h, h.admin, seeded.ID, evidence.ID,
			"Short current approval for expiry blocking.",
			h.services.cfg.Now().Add(time.Minute), http.StatusCreated).Approval
		expiring, _ := queueVerification(t, h, h.admin, seeded.ID, approval.ID,
			"m12-expiring-approval", http.StatusAccepted)
		h.clock.Add(int64(2 * time.Minute))
		storage.arm()
		worker := openVerificationWorker(t, h, nil)
		if !processVerification(t, h, worker) {
			t.Fatal("worker did not settle expired queued verification")
		}
		expired := getVerification(t, h, h.admin, seeded.ID,
			expiring.Verification.ID, http.StatusOK).Verification
		if expired.State != "blocked" || expired.Failure == nil ||
			expired.Failure.Code != "approval-expired" || expired.Result != nil {
			t.Fatal("expired queued approval was not blocked without a result")
		}

		_, approval2, changedJob := seedVerificationBinding(t, h, h.admin, seeded, true, "changed-binding")
		_, err := h.services.db.Exec(h.services.ctx, `UPDATE `+verificationTable(h, "findings")+`
				SET evidence_revision=evidence_revision+1 WHERE workspace_id=$1 AND id=$2`,
			h.admin.workspace, seeded.ID)
		ok(t, "advance current finding evidence revision", err)
		if !processVerification(t, h, worker) {
			t.Fatal("worker did not settle changed-binding queued verification")
		}
		changed := getVerification(t, h, h.admin, seeded.ID, changedJob.ID, http.StatusOK).Verification
		if changed.State != "blocked" || changed.Failure == nil ||
			changed.Failure.Code != "binding-changed" || changed.Result != nil {
			t.Fatal("changed current finding binding was not blocked without success")
		}
		approvalPage := decodeVerificationApprovalPage(t, h.request(h.admin, http.MethodGet,
			verificationBase(seeded.ID)+"/approvals?limit=100", nil, http.StatusOK).Body.Bytes())
		var current *bool
		for _, item := range approvalPage.Items {
			if item.ID == approval2.ID {
				value := item.Current
				current = &value
			}
		}
		if current == nil || *current {
			t.Fatal("approval history did not expose changed finding evidence revision as non-current")
		}
		if storage.calls.Load() != 0 {
			t.Fatal("expired or stale verification performed non-database I/O")
		}
	})
}

func TestM12_VerificationHistoryUsesBoundedNativePaging(t *testing.T) {
	h, storage := newHistoricalTrendHarness(t, nil, true)
	_, _, seeded := h.seed()
	viewer := h.addUser(h.admin, "viewer")
	evidence := submitVerificationEvidence(t, h, h.admin, seeded.ID,
		verificationEnvironment+"-paging", verificationScope+"-paging",
		true, http.StatusCreated).Evidence
	approval := approveVerificationEvidence(t, h, h.admin, seeded.ID, evidence.ID,
		"Approve one bounded native paging fixture.",
		h.services.cfg.Now().Add(time.Hour), http.StatusCreated).Approval
	tx, err := h.services.db.Begin(h.services.ctx)
	ok(t, "begin verification paging seed", err)
	defer tx.Rollback(h.services.ctx)
	for index := 1; index <= 101; index++ {
		id := fmt.Sprintf("%032x", index)
		_, err = tx.Exec(h.services.ctx, `INSERT INTO `+verificationTable(h, "verification_jobs")+`
				(id,workspace_id,finding_id,approval_id,evidence_id,requested_by,method,
				 environment_id,scope_revision,finding_evidence_revision,evidence_digest,
				 idempotency_key,created_at)
				VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
			id, h.admin.workspace, seeded.ID, approval.ID, evidence.ID, h.admin.user.ID,
			verificationMethod, evidence.EnvironmentID, evidence.ScopeRevision,
			evidence.FindingEvidenceRevision, evidence.Digest,
			fmt.Sprintf("m12-paged-verification-%03d", index),
			h.services.cfg.Now().Add(time.Duration(index)*time.Nanosecond))
		ok(t, "seed bounded verification history row", err)
	}
	ok(t, "commit verification paging seed", tx.Commit(h.services.ctx))
	storage.arm()

	first := decodeVerificationJobPage(t, h.request(viewer, http.MethodGet,
		verificationBase(seeded.ID)+"/jobs?limit=100", nil, http.StatusOK).Body.Bytes())
	if first.Total != 101 || len(first.Items) != 100 ||
		first.NextCursor == nil || *first.NextCursor != fmt.Sprintf("%032x", 100) ||
		first.Items[0].ID != fmt.Sprintf("%032x", 1) ||
		first.Items[99].ID != fmt.Sprintf("%032x", 100) {
		t.Fatal("verification history did not return the exact first native page")
	}
	second := decodeVerificationJobPage(t, h.request(viewer, http.MethodGet,
		verificationBase(seeded.ID)+"/jobs?limit=100&cursor="+*first.NextCursor,
		nil, http.StatusOK).Body.Bytes())
	if second.Total != 101 || len(second.Items) != 1 || second.NextCursor != nil ||
		second.Items[0].ID != fmt.Sprintf("%032x", 101) {
		t.Fatal("verification history did not honor the exact exclusive native cursor")
	}
	if storage.calls.Load() != 0 {
		t.Fatal("verification paging performed object-store, provider or tool I/O")
	}
}

func insertMalformedVerification(t *testing.T, h *harness, finding finding,
	content []byte, suffix string) verificationJobReceipt {
	t.Helper()
	sum := fmt.Sprintf("sha256:%x", sha256.Sum256(content))
	evidenceID := "a" + strings.Repeat(suffix, 31)
	approvalID := "b" + strings.Repeat(suffix, 31)
	jobID := "c" + strings.Repeat(suffix, 31)
	environment := "synthetic-malformed-environment-" + suffix
	scope := "synthetic-malformed-scope-" + suffix
	created := h.services.cfg.Now()
	tx, err := h.services.db.Begin(h.services.ctx)
	ok(t, "begin malformed verification seed", err)
	defer tx.Rollback(h.services.ctx)
	_, err = tx.Exec(h.services.ctx, `INSERT INTO `+verificationTable(h, "verification_evidence")+`
			(id,workspace_id,finding_id,submitted_by,method,fixture_schema,environment_id,
			 scope_revision,finding_evidence_revision,content,content_digest,content_size,created_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		evidenceID, h.admin.workspace, finding.ID, h.admin.user.ID, verificationMethod,
		verificationSchema, environment, scope, finding.EvidenceRevision, content, sum,
		len(content), created)
	ok(t, "insert malformed bounded verification evidence", err)
	_, err = tx.Exec(h.services.ctx, `INSERT INTO `+verificationTable(h, "verification_approvals")+`
			(id,workspace_id,finding_id,evidence_id,approved_by,method,environment_id,
			 scope_revision,finding_evidence_revision,evidence_digest,rationale,created_at,expires_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		approvalID, h.admin.workspace, finding.ID, evidenceID, h.admin.user.ID,
		verificationMethod, environment, scope, finding.EvidenceRevision, sum,
		"Approve one malformed database boundary fixture for terminal worker handling.",
		created, created.Add(time.Hour))
	ok(t, "insert malformed verification approval", err)
	_, err = tx.Exec(h.services.ctx, `INSERT INTO `+verificationTable(h, "verification_jobs")+`
			(id,workspace_id,finding_id,approval_id,evidence_id,requested_by,method,
			 environment_id,scope_revision,finding_evidence_revision,evidence_digest,
			 idempotency_key,created_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		jobID, h.admin.workspace, finding.ID, approvalID, evidenceID, h.admin.user.ID,
		verificationMethod, environment, scope, finding.EvidenceRevision, sum,
		"m12-malformed-"+suffix, created)
	ok(t, "insert malformed verification job", err)
	ok(t, "commit malformed verification seed", tx.Commit(h.services.ctx))
	return verificationJobReceipt{
		ID: jobID, WorkspaceID: h.admin.workspace, FindingID: finding.ID,
		ApprovalID: approvalID, EvidenceID: evidenceID, RequestedBy: h.admin.user.ID,
		Method: verificationMethod, EnvironmentID: environment, ScopeRevision: scope,
		FindingEvidenceRevision: finding.EvidenceRevision, EvidenceDigest: sum,
		State: "queued", CreatedAt: created,
	}
}

func TestM12_VerificationWorkerFormatIntegrityReclaimAndAttemptLimitsAreTerminal(t *testing.T) {
	h, storage := newHistoricalTrendHarness(t, nil, true)
	_, _, seeded := h.seed()
	worker := openVerificationWorker(t, h, nil)
	storage.arm()

	malformedBytes := []byte(`{"schema":"aspm.synthetic-fixture/v1","environmentId":"synthetic-malformed-environment-1","condition":"not-a-boolean"}`)
	malformed := insertMalformedVerification(t, h, seeded, malformedBytes, "1")
	if !processVerification(t, h, worker) {
		t.Fatal("worker did not settle malformed synthetic fixture")
	}
	formatFailure := getVerification(t, h, h.admin, seeded.ID, malformed.ID, http.StatusOK).Verification
	if formatFailure.State != "failed" || formatFailure.Failure == nil ||
		formatFailure.Failure.Code != "fixture-format" || formatFailure.Result != nil ||
		readVerificationJobRow(t, h, malformed.ID).Attempts != 1 {
		t.Fatal("fixture format error was not terminal and explicit on its first attempt")
	}

	_, _, corruptJob := seedVerificationBinding(t, h, h.admin, seeded, true, "integrity")
	_, err := h.services.db.Exec(h.services.ctx, `UPDATE `+verificationTable(h, "verification_evidence")+`
			SET content=set_byte(content,0,(get_byte(content,0)+1)%256) WHERE id=$1`,
		corruptJob.EvidenceID)
	ok(t, "corrupt one immutable fixture byte without changing declared digest or size", err)
	if !processVerification(t, h, worker) {
		t.Fatal("worker did not settle corrupt synthetic fixture")
	}
	integrity := getVerification(t, h, h.admin, seeded.ID, corruptJob.ID, http.StatusOK).Verification
	if integrity.State != "failed" || integrity.Failure == nil ||
		integrity.Failure.Code != "evidence-integrity" || integrity.Result != nil ||
		readVerificationJobRow(t, h, corruptJob.ID).Attempts != 1 {
		t.Fatal("fixture integrity error was not terminal and explicit on its first attempt")
	}
	if processVerification(t, h, worker) {
		t.Fatal("worker retried terminal format or integrity failures")
	}

	_, _, reclaimJob := seedVerificationBinding(t, h, h.admin, seeded, true, "reclaim")
	_, err = h.services.db.Exec(h.services.ctx, `UPDATE `+verificationTable(h, "verification_jobs")+`
			SET state='processing',worker_id=$2,fence=1,attempts=1,
				lease_until=clock_timestamp()-interval '1 second'
			WHERE id=$1`, reclaimJob.ID, strings.Repeat("e", 32))
	ok(t, "seed one expired verification lease for reclaim", err)
	if !processVerification(t, h, worker) {
		t.Fatal("worker did not reclaim the expired verification lease")
	}
	reclaimed := readVerificationJobRow(t, h, reclaimJob.ID)
	if reclaimed.State != "succeeded" || reclaimed.Attempts != 2 ||
		reclaimed.Fence < 2 || reclaimed.Outcome == nil ||
		*reclaimed.Outcome != "reproduced" || reclaimed.WorkerID != nil ||
		reclaimed.LeaseUntil != nil {
		t.Fatal("verification reclaim did not advance attempt/fence and publish only under the current lease")
	}

	_, _, exhaustedJob := seedVerificationBinding(t, h, h.admin, seeded, true, "attempt-limit")
	_, err = h.services.db.Exec(h.services.ctx, `UPDATE `+verificationTable(h, "verification_jobs")+`
			SET state='processing',worker_id=$2,fence=3,attempts=3,
				lease_until=clock_timestamp()-interval '1 second'
			WHERE id=$1`, exhaustedJob.ID, strings.Repeat("f", 32))
	ok(t, "seed exhausted verification lease", err)
	if !processVerification(t, h, worker) {
		t.Fatal("worker did not settle the exhausted verification lease")
	}
	exhausted := getVerification(t, h, h.admin, seeded.ID, exhaustedJob.ID, http.StatusOK).Verification
	if exhausted.State != "failed" || exhausted.Failure == nil ||
		exhausted.Failure.Code != "attempt-limit" || exhausted.Result != nil {
		t.Fatal("verification attempt limit did not become one terminal explicit failure")
	}
	exhaustedRow := readVerificationJobRow(t, h, exhaustedJob.ID)
	if exhaustedRow.Attempts != 3 || exhaustedRow.Outcome != nil ||
		exhaustedRow.WorkerID != nil || exhaustedRow.LeaseUntil != nil {
		t.Fatal("attempt-limit settlement retried or retained a live lease")
	}
	if processVerification(t, h, worker) {
		t.Fatal("worker retried a job after terminal attempt exhaustion")
	}
	if storage.calls.Load() != 0 {
		t.Fatal("format, integrity, reclaim or attempt-limit handling performed non-database I/O")
	}
}
