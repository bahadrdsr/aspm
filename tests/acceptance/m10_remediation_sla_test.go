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
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bahadrdsr/aspm/tests/internal/sourcecompat"
	"github.com/jackc/pgx/v5"
)

const (
	slaPolicyPath   = "/api/v1/reports/sla-policy"
	slaSummaryPath  = "/api/v1/reports/sla"
	slaFindingsPath = "/api/v1/reports/sla-findings"

	slaVerificationReason        = "Remediation SLA status is a time-to-workflow target; it does not verify safety, resolution, or risk acceptance."
	slaInitialDefaultRationale   = "Initial default remediation targets."
	slaInitialWorkspaceRationale = "Initial workspace remediation targets."
)

var slaNow = time.Date(2026, 10, 8, 17, 1, 51, 0, time.UTC)

type slaPolicy struct {
	WorkspaceID    string    `json:"workspaceId"`
	CriticalDays   int       `json:"criticalDays"`
	HighDays       int       `json:"highDays"`
	MediumDays     int       `json:"mediumDays"`
	LowDays        int       `json:"lowDays"`
	InfoDays       int       `json:"infoDays"`
	Revision       int64     `json:"revision"`
	ApprovedBy     *string   `json:"approvedBy"`
	ApprovedByName *string   `json:"approvedByName"`
	Rationale      string    `json:"rationale"`
	CreatedAt      time.Time `json:"createdAt"`
	UpdatedAt      time.Time `json:"updatedAt"`
}

type slaPolicyEnvelope struct {
	APIVersion string    `json:"apiVersion"`
	DataOrigin string    `json:"dataOrigin"`
	Policy     slaPolicy `json:"policy"`
}

type slaTotals struct {
	Tracked      int64 `json:"tracked"`
	WithinTarget int64 `json:"withinTarget"`
	Breached     int64 `json:"breached"`
}

type slaSeverityCount struct {
	Tracked  int64 `json:"tracked"`
	Breached int64 `json:"breached"`
}

type slaVerification struct {
	State  string `json:"state"`
	Reason string `json:"reason"`
}

type slaSummary struct {
	WorkspaceID  string                      `json:"workspaceId"`
	AsOf         time.Time                   `json:"asOf"`
	Policy       slaPolicy                   `json:"policy"`
	Totals       slaTotals                   `json:"totals"`
	BySeverity   map[string]slaSeverityCount `json:"bySeverity"`
	Verification slaVerification             `json:"verification"`
}

type slaSummaryEnvelope struct {
	APIVersion string     `json:"apiVersion"`
	DataOrigin string     `json:"dataOrigin"`
	SLA        slaSummary `json:"sla"`
}

type slaFindingItem struct {
	FindingID       string    `json:"findingId"`
	Title           string    `json:"title"`
	AssetID         string    `json:"assetId"`
	AssetName       string    `json:"assetName"`
	Severity        string    `json:"severity"`
	OwnerID         *string   `json:"ownerId"`
	OwnerName       *string   `json:"ownerName"`
	WorkflowState   string    `json:"workflowState"`
	Disposition     string    `json:"disposition"`
	SourceState     string    `json:"sourceState"`
	FirstObservedAt time.Time `json:"firstObservedAt"`
	DueAt           time.Time `json:"dueAt"`
	TargetDays      int       `json:"targetDays"`
	Status          string    `json:"status"`
	OverdueSeconds  int64     `json:"overdueSeconds"`
}

type slaFindingEnvelope struct {
	APIVersion string           `json:"apiVersion"`
	DataOrigin string           `json:"dataOrigin"`
	Items      []slaFindingItem `json:"items"`
	Total      int64            `json:"total"`
	NextCursor *string          `json:"nextCursor"`
}

type slaPolicyRow struct {
	WorkspaceID, ApprovedBy, ApprovedByName, Rationale    string
	CriticalDays, HighDays, MediumDays, LowDays, InfoDays int
	Revision                                              int64
	CreatedAt, UpdatedAt                                  time.Time
}

type slaRevisionRow struct {
	slaPolicyRow
}

type slaClock struct {
	h     *harness
	armed atomic.Bool
	calls atomic.Int32
}

func (c *slaClock) now() time.Time {
	if c.armed.Load() {
		c.calls.Add(1)
	}
	return time.Unix(0, c.h.clock.Load()).UTC()
}

func (c *slaClock) start() {
	c.calls.Store(0)
	c.armed.Store(true)
}

func (c *slaClock) stop(t *testing.T, label string) {
	t.Helper()
	c.armed.Store(false)
	if calls := c.calls.Load(); calls != 2 {
		t.Fatalf("%s must capture server Now once after authentication: calls=%d", label, calls)
	}
}

func newSLAHarness(t *testing.T, tracer pgx.QueryTracer, storage bool) (*harness, *historicalStorageTripwire, *slaClock) {
	t.Helper()
	requireApplication(t)
	h := &harness{t: t, services: ownedServices(t), password: secret(t)}
	h.clock.Store(slaNow.UnixNano())
	clock := &slaClock{h: h}
	h.services.cfg.Now = clock.now
	h.services.cfg.BootstrapToken = secret(t)
	h.services.cfg.LogOutput = io.Discard
	h.services.cfg.QueryTracer = tracer
	var tripwire *historicalStorageTripwire
	if storage {
		tripwire = newHistoricalStorageTripwire(t, h.services)
	}
	h.open()
	t.Cleanup(func() {
		if h.app.Close != nil {
			ok(t, "close remediation SLA application", h.app.Close())
		}
	})
	h.enroll()
	return h, tripwire, clock
}

func slaTable(h *harness, name string) string {
	return pgx.Identifier{h.services.cfg.Schema, "app_" + name}.Sanitize()
}

func slaJSONKeys(t *testing.T, raw []byte, label string, want ...string) map[string]json.RawMessage {
	t.Helper()
	var value map[string]json.RawMessage
	ok(t, "decode "+label+" object", json.Unmarshal(raw, &value))
	got := make([]string, 0, len(value))
	for key := range value {
		got = append(got, key)
	}
	slices.Sort(got)
	expected := slices.Clone(want)
	slices.Sort(expected)
	if !reflect.DeepEqual(got, expected) {
		t.Fatalf("%s keys got %v, want exactly %v", label, got, expected)
	}
	return value
}

func decodeSLAPolicy(t *testing.T, body []byte) slaPolicyEnvelope {
	t.Helper()
	envelope := slaJSONKeys(t, body, "SLA policy envelope", "apiVersion", "dataOrigin", "policy")
	slaJSONKeys(t, envelope["policy"], "SLA policy",
		"workspaceId", "criticalDays", "highDays", "mediumDays", "lowDays", "infoDays",
		"revision", "approvedBy", "approvedByName", "rationale", "createdAt", "updatedAt")
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var result slaPolicyEnvelope
	ok(t, "strictly decode SLA policy", decoder.Decode(&result))
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		t.Fatal("SLA policy response contains trailing JSON")
	}
	if result.APIVersion != apiVersion || result.DataOrigin != "live" {
		t.Fatal("SLA policy envelope changed API version or live origin")
	}
	assertSLAPolicy(t, result.Policy)
	return result
}

func decodeSLASummary(t *testing.T, body []byte) slaSummaryEnvelope {
	t.Helper()
	envelope := slaJSONKeys(t, body, "SLA summary envelope", "apiVersion", "dataOrigin", "sla")
	summary := slaJSONKeys(t, envelope["sla"], "SLA summary",
		"workspaceId", "asOf", "policy", "totals", "bySeverity", "verification")
	slaJSONKeys(t, summary["policy"], "SLA summary policy",
		"workspaceId", "criticalDays", "highDays", "mediumDays", "lowDays", "infoDays",
		"revision", "approvedBy", "approvedByName", "rationale", "createdAt", "updatedAt")
	slaJSONKeys(t, summary["totals"], "SLA totals", "tracked", "withinTarget", "breached")
	severity := slaJSONKeys(t, summary["bySeverity"], "SLA severity groups",
		"critical", "high", "medium", "low", "info")
	for _, key := range []string{"critical", "high", "medium", "low", "info"} {
		slaJSONKeys(t, severity[key], "SLA "+key+" counts", "tracked", "breached")
	}
	slaJSONKeys(t, summary["verification"], "SLA verification", "state", "reason")
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var result slaSummaryEnvelope
	ok(t, "strictly decode SLA summary", decoder.Decode(&result))
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		t.Fatal("SLA summary response contains trailing JSON")
	}
	if result.APIVersion != apiVersion || result.DataOrigin != "live" {
		t.Fatal("SLA summary envelope changed API version or live origin")
	}
	assertSLAPolicy(t, result.SLA.Policy)
	if result.SLA.Verification.State != "not-run" ||
		result.SLA.Verification.Reason != slaVerificationReason {
		t.Fatal("SLA summary changed the fixed verification limitation")
	}
	if len(result.SLA.BySeverity) != 5 ||
		result.SLA.Totals.Tracked != result.SLA.Totals.WithinTarget+result.SLA.Totals.Breached {
		t.Fatal("SLA summary totals or exact severity keys are inconsistent")
	}
	var tracked, breached int64
	for _, key := range []string{"critical", "high", "medium", "low", "info"} {
		counts, present := result.SLA.BySeverity[key]
		if !present || counts.Breached > counts.Tracked {
			t.Fatal("SLA severity aggregation is incomplete or impossible")
		}
		tracked += counts.Tracked
		breached += counts.Breached
	}
	if tracked != result.SLA.Totals.Tracked || breached != result.SLA.Totals.Breached {
		t.Fatal("SLA severity aggregation does not equal whole totals")
	}
	return result
}

func decodeSLAFindingPage(t *testing.T, body []byte) slaFindingEnvelope {
	t.Helper()
	envelope := slaJSONKeys(t, body, "SLA finding envelope",
		"apiVersion", "dataOrigin", "items", "total", "nextCursor")
	var rawItems []json.RawMessage
	ok(t, "decode SLA finding item array", json.Unmarshal(envelope["items"], &rawItems))
	for index, raw := range rawItems {
		slaJSONKeys(t, raw, fmt.Sprintf("SLA finding item %d", index),
			"findingId", "title", "assetId", "assetName", "severity", "ownerId", "ownerName",
			"workflowState", "disposition", "sourceState", "firstObservedAt", "dueAt",
			"targetDays", "status", "overdueSeconds")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var result slaFindingEnvelope
	ok(t, "strictly decode SLA finding page", decoder.Decode(&result))
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		t.Fatal("SLA finding response contains trailing JSON")
	}
	if result.APIVersion != apiVersion || result.DataOrigin != "live" || result.Items == nil {
		t.Fatal("SLA finding page changed API version, live origin or array shape")
	}
	return result
}

func assertSLAPolicy(t *testing.T, policy slaPolicy) {
	t.Helper()
	targets := []int{policy.CriticalDays, policy.HighDays, policy.MediumDays, policy.LowDays, policy.InfoDays}
	for index, target := range targets {
		if target < 1 || target > 3650 || index > 0 && targets[index-1] > target {
			t.Fatal("SLA policy target bounds or ordering are invalid")
		}
	}
	if policy.WorkspaceID == "" || policy.Revision < 1 || strings.TrimSpace(policy.Rationale) == "" ||
		len([]byte(policy.Rationale)) > 8192 || strings.ContainsRune(policy.Rationale, 0) ||
		policy.CreatedAt.IsZero() || policy.UpdatedAt.Before(policy.CreatedAt) ||
		(policy.ApprovedBy == nil) != (policy.ApprovedByName == nil) {
		t.Fatal("SLA policy omitted durable revision, actor, rationale or timestamps")
	}
}

func getSLAPolicy(t *testing.T, h *harness, who actor) (*httptestResponse, slaPolicyEnvelope) {
	t.Helper()
	response := h.request(who, "GET", slaPolicyPath, nil, http.StatusOK)
	return &httptestResponse{code: response.Code, body: append([]byte(nil), response.Body.Bytes()...)},
		decodeSLAPolicy(t, response.Body.Bytes())
}

type httptestResponse struct {
	code int
	body []byte
}

func requestSLASummary(t *testing.T, h *harness, clock *slaClock, who actor, body []byte) (*httptestResponse, slaSummaryEnvelope) {
	t.Helper()
	clock.start()
	response := h.request(who, "GET", slaSummaryPath, body, 0)
	clock.stop(t, "SLA summary")
	if response.Code != http.StatusOK {
		t.Fatalf("GET %s returned %d, want 200", slaSummaryPath, response.Code)
	}
	return &httptestResponse{code: response.Code, body: append([]byte(nil), response.Body.Bytes()...)},
		decodeSLASummary(t, response.Body.Bytes())
}

func requestSLAPage(t *testing.T, h *harness, clock *slaClock, who actor, path string, body []byte) (*httptestResponse, slaFindingEnvelope) {
	t.Helper()
	clock.start()
	response := h.request(who, "GET", path, body, 0)
	clock.stop(t, "SLA finding drill-down")
	if response.Code != http.StatusOK {
		t.Fatalf("GET %s returned %d, want 200", path, response.Code)
	}
	return &httptestResponse{code: response.Code, body: append([]byte(nil), response.Body.Bytes()...)},
		decodeSLAFindingPage(t, response.Body.Bytes())
}

func slaPolicyRowForWorkspace(t *testing.T, h *harness, workspace string) slaPolicyRow {
	t.Helper()
	var row slaPolicyRow
	ok(t, "read current SLA policy row", h.services.db.QueryRow(h.services.ctx, `SELECT
		workspace_id,critical_days,high_days,medium_days,low_days,info_days,revision,
		COALESCE(approved_by,''),COALESCE(approved_by_name,''),rationale,created_at,updated_at
		FROM `+slaTable(h, "report_sla_policies")+` WHERE workspace_id=$1`, workspace).Scan(
		&row.WorkspaceID, &row.CriticalDays, &row.HighDays, &row.MediumDays, &row.LowDays,
		&row.InfoDays, &row.Revision, &row.ApprovedBy, &row.ApprovedByName, &row.Rationale,
		&row.CreatedAt, &row.UpdatedAt,
	))
	return row
}

func slaRevisionRows(t *testing.T, h *harness, workspace string) []slaRevisionRow {
	t.Helper()
	rows, err := h.services.db.Query(h.services.ctx, `SELECT
		workspace_id,critical_days,high_days,medium_days,low_days,info_days,revision,
		COALESCE(approved_by,''),COALESCE(approved_by_name,''),rationale,created_at,created_at
		FROM `+slaTable(h, "report_sla_policy_revisions")+`
		WHERE workspace_id=$1 ORDER BY revision`, workspace)
	ok(t, "read immutable SLA policy revisions", err)
	defer rows.Close()
	var result []slaRevisionRow
	for rows.Next() {
		var row slaRevisionRow
		ok(t, "scan immutable SLA policy revision", rows.Scan(
			&row.WorkspaceID, &row.CriticalDays, &row.HighDays, &row.MediumDays, &row.LowDays,
			&row.InfoDays, &row.Revision, &row.ApprovedBy, &row.ApprovedByName, &row.Rationale,
			&row.CreatedAt, &row.UpdatedAt,
		))
		result = append(result, row)
	}
	ok(t, "finish immutable SLA policy revision read", rows.Err())
	return result
}

func slaFirstObserved(t *testing.T, h *harness, findingID string) time.Time {
	t.Helper()
	var value time.Time
	ok(t, "read stored finding first_observed_at", h.services.db.QueryRow(h.services.ctx,
		`SELECT first_observed_at FROM `+slaTable(h, "findings")+`
		WHERE workspace_id=$1 AND id=$2`, h.admin.workspace, findingID).Scan(&value))
	return value.UTC()
}

func slaDatabaseRows(t *testing.T, h *harness, tables ...string) map[string][]string {
	t.Helper()
	result := make(map[string][]string, len(tables))
	for _, table := range tables {
		expression := "to_jsonb(v)"
		if table == "findings" {
			expression = "(to_jsonb(v)-'first_observed_at')"
		}
		result[table] = notificationCatalogRows(t, h, `SELECT `+expression+`::text FROM `+
			slaTable(h, table)+` AS v ORDER BY to_jsonb(v)::text`)
	}
	return result
}

func slaLedger(t *testing.T, h *harness) []string {
	t.Helper()
	return notificationCatalogRows(t, h, `SELECT ledger.version::text FROM `+
		slaTable(h, "schema_versions")+` AS ledger ORDER BY ledger.version`)
}

func TestM10_RemediationSLAV25MigrationBackfillsAgeDefaultsAndExactCatalog(t *testing.T) {
	h, storage, _ := newSLAHarness(t, nil, true)
	firstAccepted := slaNow.Add(-420 * 24 * time.Hour)
	h.clock.Store(firstAccepted.UnixNano())
	firstInput, firstRun, first := h.seed()
	firstAnchor := slaFirstObserved(t, h, first.ID)
	if !firstAnchor.Equal(firstRun.ImportedAt) {
		t.Fatal("new finding did not anchor first_observed_at to the first accepted import")
	}

	h.clock.Store(slaNow.Add(-300 * 24 * time.Hour).UnixNano())
	firstInput["scanId"] = "sla-migration-rescan"
	firstInput["sourceScanAt"] = sourceTime.Add(-90 * 24 * time.Hour)
	firstInput["collectedAt"] = sourceTime.Add(4 * time.Hour)
	firstInput["report"] = string(sarif(t, func(_ object, result object) {
		result["level"] = "error"
		result["message"] = object{"text": "Changed migration control retains its original age."}
	}))
	h.finish(h.upload(firstInput).ID, "succeeded")
	if !slaFirstObserved(t, h, first.ID).Equal(firstAnchor) {
		t.Fatal("changed observation moved first_observed_at before migration downgrade")
	}
	h.json(h.admin, "PATCH", "/api/v1/findings/"+first.ID, object{
		"workflowState": "in-progress", "disposition": "accepted-risk",
		"acceptedRiskExpiresAt": slaNow.Add(24 * time.Hour),
		"rationale":             "Preserve a complete pre-V25 decision and approval during migration.",
	}, 200)
	h.json(h.admin, "POST", "/api/v1/findings/"+first.ID+"/notes",
		object{"text": "Preserve this pre-V25 note and its finding identity."}, 201)

	fallbackAsset := h.asset(h.admin, "SLA migration fallback repository", nil)
	fallbackInput := h.input(fallbackAsset.ID, "sarif", sarif(t, func(_ object, result object) {
		result["guid"] = "25252525-aaaa-4252-8252-aaaaaaaaaaaa"
		result["message"] = object{"text": "Finding without a retained observation/import uses its finding import time."}
	}))
	fallbackInput["sourceId"], fallbackInput["scanId"] = "sla-fallback", "sla-fallback-1"
	h.clock.Store(slaNow.Add(-200 * 24 * time.Hour).UnixNano())
	fallbackRun := h.finish(h.upload(fallbackInput).ID, "succeeded")
	var fallbackID string
	ok(t, "read fallback finding identity", h.services.db.QueryRow(h.services.ctx, `SELECT id FROM `+
		slaTable(h, "findings")+` WHERE workspace_id=$1 AND asset_id=$2`,
		h.admin.workspace, fallbackAsset.ID).Scan(&fallbackID))
	var fallbackImportedAt time.Time
	ok(t, "read fallback finding imported_at", h.services.db.QueryRow(h.services.ctx, `SELECT imported_at FROM `+
		slaTable(h, "findings")+` WHERE workspace_id=$1 AND id=$2`,
		h.admin.workspace, fallbackID).Scan(&fallbackImportedAt))
	_, err := h.services.db.Exec(h.services.ctx, `DELETE FROM `+slaTable(h, "observations")+`
		WHERE workspace_id=$1 AND finding_id=$2`, h.admin.workspace, fallbackID)
	ok(t, "remove only the fallback finding observation fixture", err)
	_, err = h.services.db.Exec(h.services.ctx, `DELETE FROM `+slaTable(h, "imports")+`
		WHERE workspace_id=$1 AND id=$2`, h.admin.workspace, fallbackRun.ID)
	ok(t, "remove only the fallback finding import fixture", err)

	h.clock.Store(slaNow.Add(-150 * 24 * time.Hour).UnixNano())
	secondary := v25AddFinding(t, h, asset{ID: first.AssetID}, "sla-migration-secondary")
	v25Merge(t, h, first.ID, secondary)
	completedAt := slaNow.Add(-30 * 24 * time.Hour)
	saved := historicalReport(h.admin.workspace, completedAt, 1)
	seedHistoricalSnapshot(t, h, historicalSnapshotSeed{
		ID: strings.Repeat("7", 32), WorkspaceID: h.admin.workspace, RequestedBy: h.admin.user.ID,
		Name: "V25 migration preserved snapshot", State: "succeeded", CompletedAt: &completedAt, Report: &saved,
	})

	var reachableMinimum time.Time
	ok(t, "read minimum reachable import time", h.services.db.QueryRow(h.services.ctx, `SELECT min(i.imported_at)
		FROM `+slaTable(h, "observations")+` o JOIN `+slaTable(h, "imports")+` i
		 ON i.workspace_id=o.workspace_id AND i.run_id=o.run_id
		WHERE o.workspace_id=$1 AND o.finding_id=$2`, h.admin.workspace, first.ID).Scan(&reachableMinimum))
	preservedTables := []string{
		"workspaces", "memberships", "assets", "imports", "findings", "observations", "notes",
		"finding_decision_events", "finding_disposition_approvals", "finding_correlations",
		"finding_correlation_members", "finding_correlation_events", "report_snapshots",
	}
	preservedBefore := slaDatabaseRows(t, h, preservedTables...)

	ok(t, "close V26 application before exact V24 downgrade", h.app.Close())
	h.app = Application{}
	tx, err := h.services.db.Begin(h.services.ctx)
	ok(t, "begin exact V26 downgrade fixture", err)
	defer tx.Rollback(h.services.ctx)
	_, err = tx.Exec(h.services.ctx, `DROP TABLE `+
		slaTable(h, "report_exports")+`;
		DELETE FROM `+slaTable(h, "schema_versions")+` WHERE version=26;
		DROP TABLE `+
		slaTable(h, "report_sla_policy_revisions")+`, `+slaTable(h, "report_sla_policies")+`;
		DROP INDEX `+pgx.Identifier{h.services.cfg.Schema, "app_findings_sla_candidate_idx"}.Sanitize()+`;
		ALTER TABLE `+slaTable(h, "findings")+` DROP COLUMN first_observed_at;
		DELETE FROM `+slaTable(h, "schema_versions")+` WHERE version=25`)
	ok(t, "remove only the V25 remediation SLA schema", err)
	ok(t, "commit exact V24 downgrade fixture", tx.Commit(h.services.ctx))
	v24Names := append(sourcecompat.V24CurrentTables(), "findings")
	v24Catalog := notificationDefinitions(t, h, v24Names)
	storage.arm()

	h.open()
	wantLedger := make([]string, 26)
	for index := range wantLedger {
		wantLedger[index] = fmt.Sprint(index + 1)
	}
	if !reflect.DeepEqual(slaLedger(t, h), wantLedger) {
		t.Fatalf("V26 current migration ledger got %v, want %v", slaLedger(t, h), wantLedger)
	}
	currentCatalog := notificationDefinitions(t, h, sourcecompat.CurrentTables())
	sourcecompat.ValidateCurrentCatalog(t, currentCatalog)
	projected := sourcecompat.ProjectV25Current(t, sourcecompat.ProjectV26Current(t, currentCatalog))
	if !reflect.DeepEqual(projected, v24Catalog) {
		differences := v21CatalogDifferences(projected, v24Catalog)
		t.Fatalf("V25 current catalog did not project to the exact observed V24 catalog:\n%s",
			strings.Join(differences, "\n"))
	}
	if !slaFirstObserved(t, h, first.ID).Equal(reachableMinimum.UTC()) {
		t.Fatal("V25 migration did not use the minimum import reachable through finding observations")
	}
	if !slaFirstObserved(t, h, fallbackID).Equal(fallbackImportedAt.UTC()) {
		t.Fatal("V25 migration did not use finding imported_at only when no observation/import remained")
	}
	if after := slaDatabaseRows(t, h, preservedTables...); !reflect.DeepEqual(after, preservedBefore) {
		t.Fatal("V25 migration changed an existing finding field, observation, decision, approval, correlation, snapshot or object reference")
	}
	existingPolicy := slaPolicyRowForWorkspace(t, h, h.admin.workspace)
	existingRevisions := slaRevisionRows(t, h, h.admin.workspace)
	if existingPolicy.CriticalDays != 7 || existingPolicy.HighDays != 30 ||
		existingPolicy.MediumDays != 90 || existingPolicy.LowDays != 180 ||
		existingPolicy.InfoDays != 365 || existingPolicy.Revision != 1 ||
		existingPolicy.ApprovedBy != "" || existingPolicy.ApprovedByName != "" ||
		existingPolicy.Rationale != slaInitialDefaultRationale ||
		len(existingRevisions) != 1 ||
		existingRevisions[0].slaPolicyRow != existingPolicy {
		t.Fatal("existing workspace did not receive one exact system default current row and immutable revision")
	}
	if storage.calls.Load() != 0 {
		t.Fatal("V25 migration read or wrote raw/archive object storage")
	}

	newWorkspace := h.addWorkspace()
	created := decodeSLAPolicy(t,
		h.request(newWorkspace, "GET", slaPolicyPath, nil, http.StatusOK).Body.Bytes()).Policy
	if created.WorkspaceID != newWorkspace.workspace || created.Revision != 1 ||
		created.ApprovedBy == nil || *created.ApprovedBy != h.admin.user.ID ||
		created.ApprovedByName == nil || *created.ApprovedByName != h.admin.user.Name ||
		created.Rationale != slaInitialWorkspaceRationale ||
		len(slaRevisionRows(t, h, newWorkspace.workspace)) != 1 {
		t.Fatal("new workspace did not receive its actor-attributed SLA policy in the creation transaction")
	}

	beforeReopenCatalog := notificationDefinitions(t, h, sourcecompat.CurrentTables())
	beforeReopenExisting := slaRevisionRows(t, h, h.admin.workspace)
	beforeReopenCreated := slaRevisionRows(t, h, newWorkspace.workspace)
	h.restart()
	if !reflect.DeepEqual(slaLedger(t, h), wantLedger) ||
		!reflect.DeepEqual(notificationDefinitions(t, h, sourcecompat.CurrentTables()), beforeReopenCatalog) ||
		!reflect.DeepEqual(slaRevisionRows(t, h, h.admin.workspace), beforeReopenExisting) ||
		!reflect.DeepEqual(slaRevisionRows(t, h, newWorkspace.workspace), beforeReopenCreated) {
		t.Fatal("V26 reopen repeated migration or changed current/immutable SLA policy rows")
	}
}

func slaPolicyBody(policy slaPolicy, rationale string) object {
	return object{
		"revision":     policy.Revision,
		"criticalDays": policy.CriticalDays,
		"highDays":     policy.HighDays,
		"mediumDays":   policy.MediumDays,
		"lowDays":      policy.LowDays,
		"infoDays":     policy.InfoDays,
		"rationale":    rationale,
	}
}

func slaSideEffectCounts(t *testing.T, h *harness) map[string]int {
	t.Helper()
	tables := []string{
		"findings", "observations", "finding_decision_events", "finding_disposition_approvals",
		"report_snapshots", "finding_change_events", "notification_policy_events",
		"finding_deliveries", "retention_previews", "retention_runs", "archive_publications",
	}
	result := make(map[string]int, len(tables))
	for _, table := range tables {
		var count int
		ok(t, "count SLA side-effect control "+table, h.services.db.QueryRow(h.services.ctx,
			`SELECT count(*) FROM `+slaTable(h, table)+` WHERE workspace_id=$1`,
			h.admin.workspace).Scan(&count))
		result[table] = count
	}
	return result
}

func TestM10_RemediationSLAPolicyIsPersistentStrictImmutableAndAdminOnly(t *testing.T) {
	h, storage, _ := newSLAHarness(t, nil, true)
	analyst := h.addUser(h.admin, "analyst")
	viewer := h.addUser(h.admin, "viewer")
	input, _, finding := h.seed()
	completedAt := slaNow.Add(-time.Hour)
	saved := historicalReport(h.admin.workspace, completedAt, 0)
	seedHistoricalSnapshot(t, h, historicalSnapshotSeed{
		ID: strings.Repeat("6", 32), WorkspaceID: h.admin.workspace, RequestedBy: h.admin.user.ID,
		Name: "SLA policy immutable snapshot control", State: "succeeded",
		CompletedAt: &completedAt, Report: &saved,
	})
	beforeEffects := slaSideEffectCounts(t, h)
	beforeFinding := h.finding(h.admin, finding.ID)
	beforeSnapshot := append([]byte(nil),
		h.request(h.admin, "GET", "/api/v1/reports/snapshots/"+strings.Repeat("6", 32), nil, 200).Body.Bytes()...)
	storage.arm()

	adminResponse := h.request(h.admin, "GET", slaPolicyPath, nil, http.StatusOK)
	viewerResponse := h.request(viewer, "GET", slaPolicyPath,
		encode(t, object{"workspaceId": strings.Repeat("f", 32)}), http.StatusOK)
	if !bytes.Equal(adminResponse.Body.Bytes(), viewerResponse.Body.Bytes()) {
		t.Fatal("viewer and admin SLA policy responses differ")
	}
	initial := decodeSLAPolicy(t, adminResponse.Body.Bytes()).Policy
	if initial.WorkspaceID != h.admin.workspace ||
		initial.CriticalDays != 7 || initial.HighDays != 30 || initial.MediumDays != 90 ||
		initial.LowDays != 180 || initial.InfoDays != 365 || initial.Revision != 1 ||
		initial.ApprovedBy == nil || *initial.ApprovedBy != h.admin.user.ID ||
		initial.ApprovedByName == nil || *initial.ApprovedByName != h.admin.user.Name ||
		initial.Rationale != slaInitialWorkspaceRationale ||
		len(slaRevisionRows(t, h, h.admin.workspace)) != 1 {
		t.Fatal("new workspace SLA policy did not use the exact actor-attributed starter values")
	}
	h.denied(analyst, "PATCH", slaPolicyPath, slaPolicyBody(initial, "Analyst write is denied."), 403, "forbidden")
	h.denied(viewer, "PATCH", slaPolicyPath, slaPolicyBody(initial, "Viewer write is denied."), 403, "forbidden")

	invalid := []object{
		{"revision": initial.Revision, "rationale": "Rationale-only shape is incomplete."},
		{"revision": initial.Revision, "criticalDays": 7, "highDays": 30, "mediumDays": 90,
			"lowDays": 180, "infoDays": 365, "rationale": "Extra key is rejected.", "forecast": true},
		{"revision": initial.Revision, "criticalDays": 0, "highDays": 30, "mediumDays": 90,
			"lowDays": 180, "infoDays": 365, "rationale": "Out of range."},
		{"revision": initial.Revision, "criticalDays": 31, "highDays": 30, "mediumDays": 90,
			"lowDays": 180, "infoDays": 365, "rationale": "Wrong order."},
		{"revision": initial.Revision, "criticalDays": 7.5, "highDays": 30, "mediumDays": 90,
			"lowDays": 180, "infoDays": 365, "rationale": "Noninteger target."},
		{"revision": initial.Revision, "criticalDays": 7, "highDays": 30, "mediumDays": 90,
			"lowDays": 180, "infoDays": 365, "rationale": " \t\r\n "},
		{"revision": initial.Revision, "criticalDays": 7, "highDays": 30, "mediumDays": 90,
			"lowDays": 180, "infoDays": 365, "rationale": "NUL\u0000rejected"},
		{"revision": initial.Revision, "criticalDays": 7, "highDays": 30, "mediumDays": 90,
			"lowDays": 180, "infoDays": 365, "rationale": strings.Repeat("é", 4097)},
	}
	for index, body := range invalid {
		h.denied(h.admin, "PATCH", slaPolicyPath, body, 400, "invalid-input")
		if rows := slaRevisionRows(t, h, h.admin.workspace); len(rows) != 1 {
			t.Fatalf("invalid SLA policy request %d appended a revision", index)
		}
	}

	h.denied(h.admin, "PATCH", slaPolicyPath+"?workspaceId="+strings.Repeat("f", 32),
		slaPolicyBody(initial, "Query cannot choose workspace."), 400, "invalid-input")
	h.denied(h.admin, "POST", slaPolicyPath, object{}, 405, "method-not-allowed")

	noOp := slaPolicyBody(initial, initial.Rationale)
	h.denied(h.admin, "PATCH", slaPolicyPath, noOp, 409, "conflict")
	if !reflect.DeepEqual(slaRevisionRows(t, h, h.admin.workspace),
		[]slaRevisionRow{{slaPolicyRow: slaPolicyRowForWorkspace(t, h, h.admin.workspace)}}) {
		t.Fatal("exact SLA policy no-op changed current or immutable revision state")
	}

	update := slaPolicyBody(initial, "Approve shorter synthetic remediation targets after explicit review.")
	update["criticalDays"], update["highDays"], update["mediumDays"] = 5, 20, 60
	update["lowDays"], update["infoDays"] = 120, 240
	updateAt := slaNow.Add(time.Minute)
	h.clock.Store(updateAt.UnixNano())
	response := h.request(h.admin, "PATCH", slaPolicyPath, encode(t, update), http.StatusOK)
	updated := decodeSLAPolicy(t, response.Body.Bytes()).Policy
	if updated.WorkspaceID != h.admin.workspace || updated.Revision != 2 ||
		updated.CriticalDays != 5 || updated.HighDays != 20 || updated.MediumDays != 60 ||
		updated.LowDays != 120 || updated.InfoDays != 240 ||
		updated.ApprovedBy == nil || *updated.ApprovedBy != h.admin.user.ID ||
		updated.ApprovedByName == nil || *updated.ApprovedByName != h.admin.user.Name ||
		updated.Rationale != update["rationale"] || !updated.CreatedAt.Equal(initial.CreatedAt) ||
		!updated.UpdatedAt.Equal(updateAt) {
		t.Fatal("real SLA policy update did not return the exact canonical committed snapshot")
	}
	revisions := slaRevisionRows(t, h, h.admin.workspace)
	if len(revisions) != 2 || revisions[0].Revision != 1 || revisions[1].Revision != 2 ||
		revisions[0].Rationale != slaInitialWorkspaceRationale ||
		revisions[1].Rationale != update["rationale"] ||
		revisions[1].CriticalDays != 5 || revisions[1].HighDays != 20 ||
		revisions[1].MediumDays != 60 || revisions[1].LowDays != 120 ||
		revisions[1].InfoDays != 240 {
		t.Fatal("real SLA policy update did not append one immutable complete revision")
	}
	if got := decodeSLAPolicy(t,
		h.request(h.admin, "GET", slaPolicyPath, nil, http.StatusOK).Body.Bytes()).Policy; !reflect.DeepEqual(got, updated) {
		t.Fatal("GET SLA policy did not return the committed PATCH acknowledgement")
	}
	stale := slaPolicyBody(initial, "Stale revision must not mutate policy.")
	stale["criticalDays"] = 4
	h.denied(h.admin, "PATCH", slaPolicyPath, stale, 409, "conflict")
	current := slaPolicyRowForWorkspace(t, h, h.admin.workspace)
	if !reflect.DeepEqual(slaRevisionRows(t, h, h.admin.workspace), revisions) ||
		current.Revision != 2 || current.CriticalDays != 5 || current.HighDays != 20 ||
		current.MediumDays != 60 || current.LowDays != 120 || current.InfoDays != 240 ||
		current.Rationale != update["rationale"] || !current.CreatedAt.Equal(initial.CreatedAt) ||
		!current.UpdatedAt.Equal(updateAt) {
		t.Fatal("stale SLA policy conflict changed current or immutable history")
	}

	if storage.calls.Load() != 0 {
		t.Fatal("SLA policy GET/PATCH performed raw or archive object-store I/O")
	}
	if !reflect.DeepEqual(slaSideEffectCounts(t, h), beforeEffects) ||
		!reflect.DeepEqual(h.finding(h.admin, finding.ID), beforeFinding) ||
		!bytes.Equal(h.request(h.admin, "GET", "/api/v1/reports/snapshots/"+strings.Repeat("6", 32), nil, 200).Body.Bytes(), beforeSnapshot) {
		t.Fatal("SLA policy read/update changed findings, decisions, notifications, workers or snapshots")
	}
	input["scanId"] = "sla-policy-post-update-control"
	input["sourceScanAt"] = sourceTime.Add(48 * time.Hour)
	input["collectedAt"] = sourceTime.Add(49 * time.Hour)
	h.finish(h.upload(input).ID, "succeeded")
	if slaPolicyRowForWorkspace(t, h, h.admin.workspace).Revision != 2 ||
		len(slaRevisionRows(t, h, h.admin.workspace)) != 2 {
		t.Fatal("ordinary scan changed current or immutable SLA policy")
	}
}

type slaFindingSeed struct {
	ID                string
	Asset             asset
	Title             string
	Severity          string
	Owner             *actor
	WorkflowState     string
	Disposition       string
	SourceState       string
	FirstObservedAt   time.Time
	AcceptedRiskUntil *time.Time
}

func seedSLAFinding(t *testing.T, h *harness, seed slaFindingSeed) slaFindingSeed {
	t.Helper()
	var ownerID any
	if seed.Owner != nil {
		ownerID = seed.Owner.user.ID
	}
	if seed.WorkflowState == "" {
		seed.WorkflowState = "open"
	}
	if seed.Disposition == "" {
		seed.Disposition = "none"
	}
	if seed.SourceState == "" {
		seed.SourceState = "observed"
	}
	if seed.Title == "" {
		seed.Title = "Synthetic SLA finding " + seed.ID
	}
	_, err := h.services.db.Exec(h.services.ctx, `INSERT INTO `+slaTable(h, "findings")+`
		(id,workspace_id,asset_id,source_id,scope_id,scope_revision,scope_branch,identity_key,
		 title,description,remediation,severity,evidence_text,source_label,source_scan_at,
		 collected_at,imported_at,first_observed_at,source_freshness_at,source_state,owner_id,
		 workflow_state,disposition,accepted_risk_expires_at)
		VALUES($1,$2,$3,$4,$5,'1','refs/heads/main',$6,$7,'Synthetic SLA description.',
		 'Synthetic SLA remediation.',$8,'Synthetic SLA evidence.','synthetic-sla',$9,
		 $10,$11,$12,$9,$13,$14,$15,$16,$17)`,
		seed.ID, h.admin.workspace, seed.Asset.ID, "sla-source-"+seed.ID,
		"sla-scope-"+seed.ID, "sla-identity-"+seed.ID, seed.Title, seed.Severity,
		seed.FirstObservedAt.Add(time.Hour), seed.FirstObservedAt.Add(2*time.Hour),
		seed.FirstObservedAt.Add(3*time.Hour), seed.FirstObservedAt, seed.SourceState,
		ownerID, seed.WorkflowState, seed.Disposition, seed.AcceptedRiskUntil)
	ok(t, "seed current SLA finding", err)
	return seed
}

func seedActiveSLACorrelation(t *testing.T, h *harness, primary, secondary string) string {
	t.Helper()
	id := strings.Repeat("c", 31) + "1"
	_, err := h.services.db.Exec(h.services.ctx, `INSERT INTO `+slaTable(h, "finding_correlations")+`
		(id,workspace_id,primary_finding_id,state,revision,created_by,created_at,updated_at)
		VALUES($1,$2,$3,'active',1,$4,$5,$5)`,
		id, h.admin.workspace, primary, h.admin.user.ID, slaNow.Add(-time.Hour))
	ok(t, "seed active SLA correlation", err)
	for ordinal, findingID := range []string{primary, secondary} {
		memberID := fmt.Sprintf("%032x", 500+ordinal)
		_, err = h.services.db.Exec(h.services.ctx, `INSERT INTO `+
			slaTable(h, "finding_correlation_members")+`
			(id,workspace_id,correlation_id,finding_id,ordinal,original_decision,added_at,added_by)
			VALUES($1,$2,$3,$4,$5,'{}'::jsonb,$6,$7)`,
			memberID, h.admin.workspace, id, findingID, ordinal,
			slaNow.Add(-time.Hour), h.admin.user.ID)
		ok(t, "seed active SLA correlation member", err)
	}
	return id
}

func slaTarget(policy slaPolicy, severity string) int {
	switch severity {
	case "critical":
		return policy.CriticalDays
	case "high":
		return policy.HighDays
	case "medium":
		return policy.MediumDays
	case "low":
		return policy.LowDays
	case "info":
		return policy.InfoDays
	default:
		return 0
	}
}

func expectedSLAItem(seed slaFindingSeed, policy slaPolicy, effective time.Time) slaFindingItem {
	if seed.WorkflowState == "" {
		seed.WorkflowState = "open"
	}
	if seed.Disposition == "" {
		seed.Disposition = "none"
	}
	if seed.SourceState == "" {
		seed.SourceState = "observed"
	}
	target := slaTarget(policy, seed.Severity)
	due := effective.Add(time.Duration(target) * 24 * time.Hour)
	status := "within-target"
	var overdue int64
	if due.Before(slaNow) {
		status = "breached"
		overdue = int64(slaNow.Sub(due) / time.Second)
	}
	var ownerID, ownerName *string
	if seed.Owner != nil {
		id, name := seed.Owner.user.ID, seed.Owner.user.Name
		ownerID, ownerName = &id, &name
	}
	return slaFindingItem{
		FindingID: seed.ID, Title: seed.Title, AssetID: seed.Asset.ID, AssetName: seed.Asset.Name,
		Severity: seed.Severity, OwnerID: ownerID, OwnerName: ownerName,
		WorkflowState: seed.WorkflowState, Disposition: seed.Disposition, SourceState: seed.SourceState,
		FirstObservedAt: effective, DueAt: due, TargetDays: target, Status: status,
		OverdueSeconds: overdue,
	}
}

func slaValidID(value string) bool {
	if len(value) != 32 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func assertSLAPage(t *testing.T, got slaFindingEnvelope, status string,
	want []slaFindingItem, total int64, cursor *string) {
	t.Helper()
	if got.Total != total || !reflect.DeepEqual(got.NextCursor, cursor) ||
		!reflect.DeepEqual(got.Items, want) {
		t.Fatalf("SLA finding page got %#v, want items=%#v total=%d cursor=%v",
			got, want, total, cursor)
	}
	last := ""
	for _, item := range got.Items {
		if !slaValidID(item.FindingID) || !slaValidID(item.AssetID) ||
			item.FindingID <= last || item.Status != status ||
			item.TargetDays < 1 || item.TargetDays > 3650 ||
			!item.DueAt.Equal(item.FirstObservedAt.Add(time.Duration(item.TargetDays)*24*time.Hour)) {
			t.Fatal("SLA finding identity, order, status or due arithmetic is invalid")
		}
		wantStatus := "within-target"
		var overdue int64
		if item.DueAt.Before(slaNow) {
			wantStatus = "breached"
			overdue = int64(slaNow.Sub(item.DueAt) / time.Second)
		}
		if item.Status != wantStatus || item.OverdueSeconds != overdue ||
			item.OwnerID == nil != (item.OwnerName == nil) {
			t.Fatal("SLA finding overdue arithmetic or owner snapshot is invalid")
		}
		last = item.FindingID
	}
	if cursor != nil && (len(got.Items) == 0 || *cursor != got.Items[len(got.Items)-1].FindingID) {
		t.Fatal("SLA finding nextCursor is not the last returned finding ID")
	}
}

func TestM10_RemediationSLASummaryAndDrilldownUseCurrentHumanWorkflowOnly(t *testing.T) {
	h, storage, clock := newSLAHarness(t, nil, true)
	analyst := h.addUser(h.admin, "analyst")
	viewer := h.addUser(h.admin, "viewer")
	assetOne := h.asset(h.admin, "SLA current repository one", &h.admin.user.ID)
	assetTwo := h.asset(h.admin, "SLA current repository two", &analyst.user.ID)
	expiredRisk := slaNow.Add(-time.Hour)
	seeds := []slaFindingSeed{
		{ID: fmt.Sprintf("%032x", 1), Asset: assetOne, Severity: "critical", Owner: &h.admin,
			WorkflowState: "open", FirstObservedAt: slaNow.Add(-7 * 24 * time.Hour)},
		{ID: fmt.Sprintf("%032x", 2), Asset: assetOne, Severity: "critical", Owner: &analyst,
			WorkflowState: "open", FirstObservedAt: slaNow.Add(-7*24*time.Hour - time.Second)},
		{ID: fmt.Sprintf("%032x", 3), Asset: assetOne, Severity: "high",
			WorkflowState: "in-progress", FirstObservedAt: slaNow.Add(-24 * time.Hour)},
		{ID: fmt.Sprintf("%032x", 4), Asset: assetTwo, Severity: "medium",
			WorkflowState: "pending-retest", FirstObservedAt: slaNow.Add(-91 * 24 * time.Hour)},
		{ID: fmt.Sprintf("%032x", 5), Asset: assetTwo, Severity: "low",
			WorkflowState: "resolved", FirstObservedAt: slaNow.Add(-500 * 24 * time.Hour)},
		{ID: fmt.Sprintf("%032x", 6), Asset: assetTwo, Severity: "info", Owner: &analyst,
			WorkflowState: "open", Disposition: "accepted-risk",
			AcceptedRiskUntil: &expiredRisk, FirstObservedAt: slaNow.Add(-366 * 24 * time.Hour)},
		{ID: fmt.Sprintf("%032x", 7), Asset: assetTwo, Severity: "high",
			WorkflowState: "open", Disposition: "suppressed",
			FirstObservedAt: slaNow.Add(-31 * 24 * time.Hour)},
		{ID: fmt.Sprintf("%032x", 8), Asset: assetOne, Severity: "medium",
			WorkflowState: "open", Disposition: "false-positive",
			FirstObservedAt: slaNow.Add(-91 * 24 * time.Hour)},
		{ID: fmt.Sprintf("%032x", 9), Asset: assetOne, Severity: "low",
			WorkflowState: "open", SourceState: "inferred-resolved",
			FirstObservedAt: slaNow.Add(-181 * 24 * time.Hour)},
		{ID: fmt.Sprintf("%032x", 10), Asset: assetTwo, Severity: "high",
			WorkflowState: "open", SourceState: "stale",
			FirstObservedAt: slaNow.Add(-31 * 24 * time.Hour)},
		{ID: fmt.Sprintf("%032x", 11), Asset: assetOne, Severity: "medium",
			WorkflowState: "open", SourceState: "unknown",
			FirstObservedAt: slaNow.Add(-24 * time.Hour)},
		{ID: fmt.Sprintf("%032x", 12), Asset: assetTwo, Severity: "info",
			WorkflowState: "open", FirstObservedAt: slaNow.Add(-10 * 24 * time.Hour)},
		{ID: fmt.Sprintf("%032x", 13), Asset: assetTwo, Severity: "critical",
			WorkflowState: "open", FirstObservedAt: slaNow.Add(-400 * 24 * time.Hour)},
		{ID: fmt.Sprintf("%032x", 14), Asset: assetOne, Severity: "low",
			WorkflowState: "open", FirstObservedAt: slaNow.Add(-100 * 24 * time.Hour)},
	}
	for index := range seeds {
		seeds[index].Title = fmt.Sprintf("Synthetic SLA current finding %02d", index+1)
		seedSLAFinding(t, h, seeds[index])
	}
	seedActiveSLACorrelation(t, h, seeds[11].ID, seeds[12].ID)
	policy := decodeSLAPolicy(t,
		h.request(h.admin, "GET", slaPolicyPath, nil, http.StatusOK).Body.Bytes()).Policy

	expected := make([]slaFindingItem, 0, len(seeds))
	for index, seed := range seeds {
		if seed.WorkflowState == "resolved" || index == 12 {
			continue
		}
		effective := seed.FirstObservedAt
		if index == 11 {
			effective = seeds[12].FirstObservedAt
		}
		expected = append(expected, expectedSLAItem(seed, policy, effective))
	}
	slices.SortFunc(expected, func(left, right slaFindingItem) int {
		return strings.Compare(left.FindingID, right.FindingID)
	})
	var expectedTotals slaTotals
	expectedBySeverity := map[string]slaSeverityCount{
		"critical": {}, "high": {}, "medium": {}, "low": {}, "info": {},
	}
	breachedItems, withinItems := []slaFindingItem{}, []slaFindingItem{}
	for _, item := range expected {
		expectedTotals.Tracked++
		counts := expectedBySeverity[item.Severity]
		counts.Tracked++
		if item.Status == "breached" {
			expectedTotals.Breached++
			counts.Breached++
			breachedItems = append(breachedItems, item)
		} else {
			expectedTotals.WithinTarget++
			withinItems = append(withinItems, item)
		}
		expectedBySeverity[item.Severity] = counts
	}
	stateBefore := slaSideEffectCounts(t, h)
	snapshotBefore := slaDatabaseRows(t, h, "report_snapshots")
	storage.arm()

	adminResponse, summary := requestSLASummary(t, h, clock, h.admin,
		encode(t, object{"workspaceId": strings.Repeat("f", 32)}))
	viewerResponse, viewerSummary := requestSLASummary(t, h, clock, viewer, nil)
	if !bytes.Equal(adminResponse.body, viewerResponse.body) ||
		!reflect.DeepEqual(summary, viewerSummary) {
		t.Fatal("viewer and admin SLA summaries differ")
	}
	if summary.SLA.WorkspaceID != h.admin.workspace || !summary.SLA.AsOf.Equal(slaNow) ||
		!reflect.DeepEqual(summary.SLA.Policy, policy) ||
		!reflect.DeepEqual(summary.SLA.Totals, expectedTotals) ||
		!reflect.DeepEqual(summary.SLA.BySeverity, expectedBySeverity) {
		t.Fatalf("SLA summary did not project exact current membership: %#v", summary.SLA)
	}
	boundary := expectedSLAItem(seeds[0], policy, seeds[0].FirstObservedAt)
	if boundary.Status != "within-target" || !boundary.DueAt.Equal(slaNow) || boundary.OverdueSeconds != 0 {
		t.Fatal("exact SLA due boundary was not within-target")
	}
	if summary.SLA.Totals.Breached == 0 || summary.SLA.Totals.WithinTarget == 0 {
		t.Fatal("SLA fixture did not exercise both current statuses")
	}

	breachedAdminResponse, breached := requestSLAPage(t, h, clock, h.admin,
		slaFindingsPath+"?status=breached&limit=100", nil)
	breachedViewerResponse, breachedViewer := requestSLAPage(t, h, clock, viewer,
		slaFindingsPath+"?status=breached&limit=100",
		encode(t, object{"workspaceId": strings.Repeat("f", 32)}))
	if !bytes.Equal(breachedAdminResponse.body, breachedViewerResponse.body) ||
		!reflect.DeepEqual(breached, breachedViewer) {
		t.Fatal("viewer and admin breached finding responses differ")
	}
	assertSLAPage(t, breached, "breached", breachedItems, expectedTotals.Breached, nil)
	_, within := requestSLAPage(t, h, clock, analyst,
		slaFindingsPath+"?status=within-target&limit=100", nil)
	assertSLAPage(t, within, "within-target", withinItems, expectedTotals.WithinTarget, nil)
	if breached.Total != summary.SLA.Totals.Breached ||
		within.Total != summary.SLA.Totals.WithinTarget {
		t.Fatal("SLA summary status counts do not equal drill-down whole-filter totals")
	}
	for _, item := range append(slices.Clone(breached.Items), within.Items...) {
		if item.FindingID == seeds[12].ID || item.FindingID == seeds[4].ID {
			t.Fatal("SLA drill-down exposed a hidden active-correlation member or resolved human workflow")
		}
		if item.FindingID == seeds[11].ID &&
			!item.FirstObservedAt.Equal(seeds[12].FirstObservedAt) {
			t.Fatal("visible correlation primary did not use the minimum active member age")
		}
	}
	if storage.calls.Load() != 0 ||
		!reflect.DeepEqual(slaSideEffectCounts(t, h), stateBefore) ||
		!reflect.DeepEqual(slaDatabaseRows(t, h, "report_snapshots"), snapshotBefore) {
		t.Fatal("SLA summary or drill-down performed storage, worker, snapshot or durable write side effects")
	}
}

func findSLAItem(t *testing.T, page slaFindingEnvelope, id string) slaFindingItem {
	t.Helper()
	for _, item := range page.Items {
		if item.FindingID == id {
			return item
		}
	}
	t.Fatalf("SLA finding page omitted %s", id)
	return slaFindingItem{}
}

func TestM10_RemediationSLAFirstObservedIsStableAndCorrelationAgeIsEffectiveOnly(t *testing.T) {
	h, _, clock := newSLAHarness(t, nil, false)
	a := h.asset(h.admin, "SLA stable-age repository", &h.admin.user.ID)
	firstAccepted := slaNow.Add(-400 * 24 * time.Hour)
	h.clock.Store(firstAccepted.UnixNano())
	input := h.input(a.ID, "sarif", fixture(t, "sarif.json"))
	input["sourceId"], input["scanId"] = "sla-stable-old", "sla-stable-old-1"
	input["sourceScanAt"], input["collectedAt"] = firstAccepted.Add(-2*time.Hour), firstAccepted.Add(-time.Hour)
	firstRun := h.finish(h.upload(input).ID, "succeeded")
	var oldID string
	ok(t, "read old SLA finding identity", h.services.db.QueryRow(h.services.ctx, `SELECT id FROM `+
		slaTable(h, "findings")+` WHERE workspace_id=$1 AND source_id=$2`,
		h.admin.workspace, "sla-stable-old").Scan(&oldID))
	oldAnchor := slaFirstObserved(t, h, oldID)
	if !oldAnchor.Equal(firstRun.ImportedAt) {
		t.Fatal("new finding age did not equal its first accepted import")
	}

	h.finish(h.upload(input).ID, "succeeded")
	if !slaFirstObserved(t, h, oldID).Equal(oldAnchor) {
		t.Fatal("identical reimport moved first_observed_at")
	}
	h.clock.Store(slaNow.Add(-300 * 24 * time.Hour).UnixNano())
	input["scanId"] = "sla-stable-old-2"
	input["sourceScanAt"] = firstAccepted.Add(-30 * 24 * time.Hour)
	input["collectedAt"] = firstAccepted.Add(-29 * 24 * time.Hour)
	input["report"] = string(sarif(t, func(_ object, result object) {
		result["level"] = "error"
		result["message"] = object{"text": "Changed observation must retain the first accepted age."}
	}))
	h.finish(h.upload(input).ID, "succeeded")
	if !slaFirstObserved(t, h, oldID).Equal(oldAnchor) {
		t.Fatal("changed observation or source-time change moved first_observed_at")
	}
	h.json(h.admin, "PATCH", "/api/v1/findings/"+oldID, object{
		"ownerId": h.admin.user.ID, "workflowState": "pending-retest",
		"disposition": "accepted-risk", "acceptedRiskExpiresAt": slaNow.Add(24 * time.Hour),
		"rationale": "Human workflow and disposition must not reset remediation age.",
	}, 200)
	if !slaFirstObserved(t, h, oldID).Equal(oldAnchor) {
		t.Fatal("workflow or disposition edit moved first_observed_at")
	}

	h.clock.Store(slaNow.Add(-200 * 24 * time.Hour).UnixNano())
	input["scanId"] = "sla-stable-old-absence"
	input["sourceScanAt"] = slaNow.Add(-201 * 24 * time.Hour)
	input["collectedAt"] = slaNow.Add(-200*24*time.Hour - time.Hour)
	input["report"] = string(sarif(t, func(run, _ object) { run["results"] = []any{} }))
	h.finish(h.upload(input).ID, "succeeded")
	if h.finding(h.admin, oldID).SourceState != "inferred-resolved" ||
		!slaFirstObserved(t, h, oldID).Equal(oldAnchor) {
		t.Fatal("source-inferred resolution changed human age or failed to remain independent")
	}
	h.clock.Store(slaNow.Add(-100 * 24 * time.Hour).UnixNano())
	input["scanId"] = "sla-stable-old-reopened"
	input["sourceScanAt"] = slaNow.Add(-101 * 24 * time.Hour)
	input["collectedAt"] = slaNow.Add(-100*24*time.Hour - time.Hour)
	input["report"] = string(fixture(t, "sarif.json"))
	h.finish(h.upload(input).ID, "succeeded")
	if !slaFirstObserved(t, h, oldID).Equal(oldAnchor) {
		t.Fatal("reopened observation or ordinary rescan moved first_observed_at")
	}

	recentAccepted := slaNow.Add(-10 * 24 * time.Hour)
	h.clock.Store(recentAccepted.UnixNano())
	recentInput := h.input(a.ID, "sarif", sarif(t, func(_ object, result object) {
		result["guid"] = "25252525-bbbb-4252-8252-bbbbbbbbbbbb"
		result["level"] = "note"
		result["message"] = object{"text": "Recent visible primary for effective SLA age."}
	}))
	recentInput["sourceId"], recentInput["scanId"] = "sla-stable-recent", "sla-stable-recent-1"
	recentInput["sourceScanAt"], recentInput["collectedAt"] =
		recentAccepted.Add(-2*time.Hour), recentAccepted.Add(-time.Hour)
	recentRun := h.finish(h.upload(recentInput).ID, "succeeded")
	var recentID string
	ok(t, "read recent SLA finding identity", h.services.db.QueryRow(h.services.ctx, `SELECT id FROM `+
		slaTable(h, "findings")+` WHERE workspace_id=$1 AND source_id=$2`,
		h.admin.workspace, "sla-stable-recent").Scan(&recentID))
	recentAnchor := slaFirstObserved(t, h, recentID)
	if !recentAnchor.Equal(recentRun.ImportedAt) || recentAnchor.Equal(oldAnchor) {
		t.Fatal("split-capable findings did not retain independent original anchors")
	}
	h.clock.Store(slaNow.UnixNano())
	v25Merge(t, h, recentID, oldID)
	_, active := requestSLAPage(t, h, clock, h.admin,
		slaFindingsPath+"?status=breached&limit=100", nil)
	primary := findSLAItem(t, active, recentID)
	if !primary.FirstObservedAt.Equal(oldAnchor) || primary.Severity != "info" ||
		primary.TargetDays != 365 || primary.Status != "breached" {
		t.Fatal("active correlation primary did not use the minimum stored active-member age at read time")
	}
	for _, item := range active.Items {
		if item.FindingID == oldID {
			t.Fatal("active correlation secondary was tracked as a separate SLA item")
		}
	}
	if !slaFirstObserved(t, h, recentID).Equal(recentAnchor) ||
		!slaFirstObserved(t, h, oldID).Equal(oldAnchor) {
		t.Fatal("effective correlation age destructively rewrote a stored finding anchor")
	}

	split := h.json(h.admin, "POST", "/api/v1/findings/"+recentID+"/split-previews",
		object{"memberFindingId": oldID}, 200).SplitPreview
	splitInput := object{
		"memberFindingId":         oldID,
		"correlationRevision":     split.Correlation.Revision,
		"primaryDecisionRevision": split.Primary.DecisionRevision,
		"primaryEvidenceRevision": split.Primary.EvidenceRevision,
		"memberDecisionRevision":  split.Member.DecisionRevision,
		"memberEvidenceRevision":  split.Member.EvidenceRevision,
		"primaryDecision":         correlationDecision(&h.admin.user.ID, "open", "none", nil),
		"memberDecision":          correlationDecision(&h.admin.user.ID, "open", "none", nil),
		"rationale":               "Release the retained source member without rewriting either remediation age.",
		"idempotencyKey":          "sla-stable-age-split",
	}
	h.json(h.admin, "POST", "/api/v1/findings/"+recentID+"/splits", splitInput, 201)
	if !slaFirstObserved(t, h, recentID).Equal(recentAnchor) ||
		!slaFirstObserved(t, h, oldID).Equal(oldAnchor) {
		t.Fatal("split rewrote one of the original remediation anchors")
	}
	_, within := requestSLAPage(t, h, clock, h.admin,
		slaFindingsPath+"?status=within-target&limit=100", nil)
	releasedPrimary := findSLAItem(t, within, recentID)
	if !releasedPrimary.FirstObservedAt.Equal(recentAnchor) || releasedPrimary.Status != "within-target" {
		t.Fatal("released member continued contributing to the former primary effective age")
	}
	_, breached := requestSLAPage(t, h, clock, h.admin,
		slaFindingsPath+"?status=breached&limit=100", nil)
	if !findSLAItem(t, breached, oldID).FirstObservedAt.Equal(oldAnchor) {
		t.Fatal("split member did not retain and expose its own original anchor")
	}

	_, err := h.services.db.Exec(h.services.ctx, `UPDATE `+slaTable(h, "findings")+`
		SET severity='critical' WHERE workspace_id=$1 AND id=$2`, h.admin.workspace, recentID)
	ok(t, "change current severity without changing age", err)
	_, severityMoved := requestSLAPage(t, h, clock, h.admin,
		slaFindingsPath+"?status=breached&limit=100", nil)
	moved := findSLAItem(t, severityMoved, recentID)
	if moved.TargetDays != 7 || !moved.FirstObservedAt.Equal(recentAnchor) {
		t.Fatal("current severity did not move SLA status solely through the current target")
	}
	_, err = h.services.db.Exec(h.services.ctx, `UPDATE `+slaTable(h, "findings")+`
		SET severity='info' WHERE workspace_id=$1 AND id=$2`, h.admin.workspace, recentID)
	ok(t, "restore current SLA severity", err)
	currentPolicy := decodeSLAPolicy(t,
		h.request(h.admin, "GET", slaPolicyPath, nil, 200).Body.Bytes()).Policy
	policyUpdate := slaPolicyBody(currentPolicy, "Shorten the ordered synthetic targets after review.")
	for _, key := range []string{"criticalDays", "highDays", "mediumDays", "lowDays", "infoDays"} {
		policyUpdate[key] = 5
	}
	h.request(h.admin, "PATCH", slaPolicyPath, encode(t, policyUpdate), 200)
	_, policyMoved := requestSLAPage(t, h, clock, h.admin,
		slaFindingsPath+"?status=breached&limit=100", nil)
	if item := findSLAItem(t, policyMoved, recentID); item.TargetDays != 5 ||
		!item.FirstObservedAt.Equal(recentAnchor) {
		t.Fatal("current policy did not move status without changing firstObservedAt")
	}
}

type slaTraceMarker struct{}

type slaReadTracer struct {
	role          string
	gate          *securityGate
	once          sync.Once
	armed         atomic.Bool
	inTransaction atomic.Bool
	begins        atomic.Int32
	commits       atomic.Int32
	rollbacks     atomic.Int32
	selects       atomic.Int32
	writes        atomic.Int32
	locks         atomic.Int32
	readOnly      atomic.Bool
}

func newSLAReadTracer(role string) *slaReadTracer {
	return &slaReadTracer{role: role, gate: newSecurityGate()}
}

func (t *slaReadTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if !t.armed.Load() {
		return ctx
	}
	role, _ := ctx.Value(securityTraceRole{}).(string)
	if role != t.role {
		return ctx
	}
	sql := strings.ToLower(strings.Join(strings.Fields(data.SQL), " "))
	if strings.HasPrefix(sql, "begin") && strings.Contains(sql, "repeatable read") {
		t.begins.Add(1)
		t.inTransaction.Store(true)
		if strings.Contains(sql, "read only") {
			t.readOnly.Store(true)
		}
	}
	if t.inTransaction.Load() {
		if strings.Contains(sql, " insert ") || strings.HasPrefix(sql, "insert ") ||
			strings.Contains(sql, " update ") || strings.HasPrefix(sql, "update ") ||
			strings.Contains(sql, " delete ") || strings.HasPrefix(sql, "delete ") {
			t.writes.Add(1)
		}
		if strings.Contains(sql, "for update") || strings.Contains(sql, "for share") {
			t.locks.Add(1)
		}
	}
	if strings.Contains(sql, "app_report_sla_policies") ||
		strings.Contains(sql, "app_findings") {
		t.selects.Add(1)
		return context.WithValue(ctx, slaTraceMarker{}, true)
	}
	if strings.HasPrefix(sql, "commit") {
		t.commits.Add(1)
		t.inTransaction.Store(false)
	}
	if strings.HasPrefix(sql, "rollback") {
		t.rollbacks.Add(1)
		t.inTransaction.Store(false)
	}
	return ctx
}

func (t *slaReadTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	if data.Err == nil && ctx.Value(slaTraceMarker{}) == true {
		t.once.Do(func() { _ = t.gate.block(ctx) })
	}
}

func assertSLAReadTrace(t *testing.T, tracer *slaReadTracer, label string) {
	t.Helper()
	if tracer.begins.Load() != 1 || !tracer.readOnly.Load() ||
		tracer.commits.Load() != 1 || tracer.rollbacks.Load() != 0 ||
		tracer.selects.Load() < 1 || tracer.writes.Load() != 0 || tracer.locks.Load() != 0 {
		t.Fatalf("%s was not one read-only repeatable-read snapshot: begins=%d commits=%d rollbacks=%d selects=%d writes=%d locks=%d readOnly=%t",
			label, tracer.begins.Load(), tracer.commits.Load(), tracer.rollbacks.Load(),
			tracer.selects.Load(), tracer.writes.Load(), tracer.locks.Load(), tracer.readOnly.Load())
	}
}

func TestM10_RemediationSLAReadsUseOneStableRepeatableReadSnapshot(t *testing.T) {
	t.Run("summary policy and findings", func(t *testing.T) {
		tracer := newSLAReadTracer("sla-summary-read")
		h, _, clock := newSLAHarness(t, tracer, false)
		a := h.asset(h.admin, "SLA stable summary repository", nil)
		seed := seedSLAFinding(t, h, slaFindingSeed{
			ID: strings.Repeat("1", 32), Asset: a, Severity: "critical",
			WorkflowState: "open", FirstObservedAt: slaNow.Add(-8 * 24 * time.Hour),
		})
		tracer.armed.Store(true)
		clock.start()
		future := securityRequest(h, h.admin, "GET", slaSummaryPath, nil,
			15*time.Second, "sla-summary-read")
		select {
		case <-tracer.gate.entered:
		case <-future.done:
			t.Fatalf("SLA summary returned status %d before a real policy/finding query", future.result.Code)
		case <-time.After(5 * time.Second):
			t.Fatal("SLA summary did not reach its bounded policy/finding query")
		}
		tx, err := h.services.db.Begin(h.services.ctx)
		ok(t, "begin concurrent SLA summary change", err)
		_, err = tx.Exec(h.services.ctx, `UPDATE `+slaTable(h, "report_sla_policies")+`
			SET critical_days=30,high_days=30,medium_days=30,low_days=30,info_days=30,
			 revision=revision+1,approved_by=$2,approved_by_name=$3,
			 rationale='Concurrent SLA summary policy.',updated_at=$4
			WHERE workspace_id=$1`, h.admin.workspace, h.admin.user.ID, h.admin.user.Name, slaNow)
		ok(t, "commit concurrent SLA policy current row", err)
		_, err = tx.Exec(h.services.ctx, `INSERT INTO `+slaTable(h, "report_sla_policy_revisions")+`
			(workspace_id,revision,critical_days,high_days,medium_days,low_days,info_days,
			 approved_by,approved_by_name,rationale,created_at)
			VALUES($1,2,30,30,30,30,30,$2,$3,'Concurrent SLA summary policy.',$4)`,
			h.admin.workspace, h.admin.user.ID, h.admin.user.Name, slaNow)
		ok(t, "append concurrent SLA policy revision", err)
		_, err = tx.Exec(h.services.ctx, `UPDATE `+slaTable(h, "findings")+`
			SET workflow_state='resolved',severity='info' WHERE workspace_id=$1 AND id=$2`,
			h.admin.workspace, seed.ID)
		ok(t, "commit concurrent SLA finding change", err)
		ok(t, "commit concurrent SLA summary transaction", tx.Commit(h.services.ctx))
		tracer.gate.open()
		response := future.wait(t)
		clock.stop(t, "stable SLA summary")
		if response.Code != http.StatusOK {
			t.Fatalf("stable SLA summary returned status %d", response.Code)
		}
		result := decodeSLASummary(t, response.Body.Bytes())
		if result.SLA.Policy.Revision != 1 ||
			result.SLA.Totals != (slaTotals{Tracked: 1, Breached: 1}) {
			t.Fatal("SLA summary mixed concurrent policy/finding commits into its transaction snapshot")
		}
		assertSLAReadTrace(t, tracer, "SLA summary")

		tracer.armed.Store(false)
		_, later := requestSLASummary(t, h, clock, h.admin, nil)
		if later.SLA.Policy.Revision != 2 ||
			later.SLA.Totals != (slaTotals{}) {
			t.Fatal("later SLA summary did not observe committed policy and resolved workflow changes")
		}
	})

	t.Run("drill-down total and page", func(t *testing.T) {
		tracer := newSLAReadTracer("sla-findings-read")
		h, _, clock := newSLAHarness(t, tracer, false)
		a := h.asset(h.admin, "SLA stable page repository", nil)
		first := seedSLAFinding(t, h, slaFindingSeed{
			ID: fmt.Sprintf("%032x", 1), Asset: a, Severity: "critical",
			WorkflowState: "open", FirstObservedAt: slaNow.Add(-8 * 24 * time.Hour),
		})
		seedSLAFinding(t, h, slaFindingSeed{
			ID: fmt.Sprintf("%032x", 2), Asset: a, Severity: "critical",
			WorkflowState: "open", FirstObservedAt: slaNow.Add(-9 * 24 * time.Hour),
		})
		tracer.armed.Store(true)
		clock.start()
		future := securityRequest(h, h.admin, "GET",
			slaFindingsPath+"?status=breached&limit=1", nil,
			15*time.Second, "sla-findings-read")
		select {
		case <-tracer.gate.entered:
		case <-future.done:
			t.Fatalf("SLA finding page returned status %d before a real policy/finding query", future.result.Code)
		case <-time.After(5 * time.Second):
			t.Fatal("SLA finding page did not reach its bounded policy/finding query")
		}
		_, err := h.services.db.Exec(h.services.ctx, `UPDATE `+slaTable(h, "findings")+`
			SET workflow_state='resolved' WHERE workspace_id=$1 AND id=$2`,
			h.admin.workspace, first.ID)
		ok(t, "commit concurrent SLA page removal", err)
		seedSLAFinding(t, h, slaFindingSeed{
			ID: fmt.Sprintf("%032x", 3), Asset: a, Severity: "critical",
			WorkflowState: "open", FirstObservedAt: slaNow.Add(-10 * 24 * time.Hour),
		})
		tracer.gate.open()
		response := future.wait(t)
		clock.stop(t, "stable SLA finding page")
		if response.Code != http.StatusOK {
			t.Fatalf("stable SLA finding page returned status %d", response.Code)
		}
		result := decodeSLAFindingPage(t, response.Body.Bytes())
		if result.Total != 2 || len(result.Items) != 1 ||
			result.Items[0].FindingID != fmt.Sprintf("%032x", 1) ||
			result.NextCursor == nil || *result.NextCursor != fmt.Sprintf("%032x", 1) {
			t.Fatal("SLA finding read mixed concurrent changes between whole total and page")
		}
		assertSLAReadTrace(t, tracer, "SLA finding drill-down")

		tracer.armed.Store(false)
		_, later := requestSLAPage(t, h, clock, h.admin,
			slaFindingsPath+"?status=breached&limit=1", nil)
		if later.Total != 2 || len(later.Items) != 1 ||
			later.Items[0].FindingID != fmt.Sprintf("%032x", 2) {
			t.Fatal("later SLA finding page did not observe committed removal and addition")
		}
	})
}

func TestM10_RemediationSLADrilldownValidatesAuthorityAndNativePaging(t *testing.T) {
	t.Run("paging and query contract", func(t *testing.T) {
		h, _, clock := newSLAHarness(t, nil, false)
		asset := h.asset(h.admin, "SLA paged findings repository", nil)
		for index := 1; index <= 101; index++ {
			seedSLAFinding(t, h, slaFindingSeed{
				ID: fmt.Sprintf("%032x", index), Asset: asset,
				Title:    fmt.Sprintf("Synthetic breached SLA page finding %03d", index),
				Severity: "critical", WorkflowState: "open",
				FirstObservedAt: slaNow.Add(-8 * 24 * time.Hour),
			})
		}
		_, summary := requestSLASummary(t, h, clock, h.admin, nil)
		if summary.SLA.Totals != (slaTotals{Tracked: 101, Breached: 101}) {
			t.Fatalf("paged SLA fixture summary got %#v", summary.SLA.Totals)
		}
		_, first := requestSLAPage(t, h, clock, h.admin,
			slaFindingsPath+"?status=breached", nil)
		if len(first.Items) != 100 || first.Total != 101 || first.NextCursor == nil ||
			*first.NextCursor != fmt.Sprintf("%032x", 100) ||
			first.Items[0].FindingID != fmt.Sprintf("%032x", 1) ||
			first.Items[99].FindingID != fmt.Sprintf("%032x", 100) {
			t.Fatal("SLA default page did not return the first exact 100-row native page")
		}
		_, second := requestSLAPage(t, h, clock, h.admin,
			slaFindingsPath+"?status=breached&cursor="+*first.NextCursor, nil)
		if len(second.Items) != 1 || second.Total != 101 || second.NextCursor != nil ||
			second.Items[0].FindingID != fmt.Sprintf("%032x", 101) {
			t.Fatal("SLA cursor-exclusive continuation did not return the remaining exact row")
		}
		_, one := requestSLAPage(t, h, clock, h.admin,
			slaFindingsPath+"?status=breached&limit=1", nil)
		if len(one.Items) != 1 || one.NextCursor == nil ||
			*one.NextCursor != one.Items[0].FindingID {
			t.Fatal("SLA explicit one-row page did not return its last-row cursor")
		}
		_, pastEnd := requestSLAPage(t, h, clock, h.admin,
			slaFindingsPath+"?status=breached&cursor="+strings.Repeat("f", 32), nil)
		if len(pastEnd.Items) != 0 || pastEnd.Total != 101 || pastEnd.NextCursor != nil {
			t.Fatal("SLA cursor after the final row changed whole-filter total or fabricated a cursor")
		}
		_, empty := requestSLAPage(t, h, clock, h.admin,
			slaFindingsPath+"?status=within-target&limit=100", nil)
		if len(empty.Items) != 0 || empty.Total != 0 || empty.NextCursor != nil {
			t.Fatal("authorized empty SLA status must be an empty array with exact zero total")
		}

		for _, path := range []string{
			slaFindingsPath,
			slaFindingsPath + "?status=",
			slaFindingsPath + "?status=Breached",
			slaFindingsPath + "?status=unknown",
			slaFindingsPath + "?status=breached&status=within-target",
			slaFindingsPath + "?status=breached&limit=",
			slaFindingsPath + "?status=breached&limit=0",
			slaFindingsPath + "?status=breached&limit=101",
			slaFindingsPath + "?status=breached&limit=-1",
			slaFindingsPath + "?status=breached&limit=1.0",
			slaFindingsPath + "?status=breached&limit=1e2",
			slaFindingsPath + "?status=breached&limit=%2B1",
			slaFindingsPath + "?status=breached&limit=1&limit=2",
			slaFindingsPath + "?status=breached&cursor=",
			slaFindingsPath + "?status=breached&cursor=abc",
			slaFindingsPath + "?status=breached&cursor=" + strings.Repeat("A", 32),
			slaFindingsPath + "?status=breached&cursor=" + fmt.Sprintf("%032x", 1) +
				"&cursor=" + fmt.Sprintf("%032x", 2),
			slaFindingsPath + "?status=breached&workspaceId=" + h.admin.workspace,
			slaFindingsPath + "?Status=breached",
			slaFindingsPath + "?status=breached&page=1",
		} {
			h.denied(h.admin, "GET", path, nil, 400, "invalid-input")
		}
		for _, path := range []string{
			slaSummaryPath + "?workspaceId=" + h.admin.workspace,
			slaSummaryPath + "?status=breached",
			slaSummaryPath + "?status=",
			slaPolicyPath + "?workspaceId=" + h.admin.workspace,
		} {
			h.denied(h.admin, "GET", path, nil, 400, "invalid-input")
		}
		for _, method := range []string{"POST", "PATCH", "DELETE"} {
			h.denied(h.admin, method, slaSummaryPath, object{}, 405, "method-not-allowed")
			h.denied(h.admin, method, slaFindingsPath+"?status=breached", object{}, 405, "method-not-allowed")
		}
	})

	t.Run("authentication and selected membership", func(t *testing.T) {
		h, _, _ := newSLAHarness(t, nil, false)
		summaryPath := slaSummaryPath
		findingsPath := slaFindingsPath + "?status=breached"
		h.denied(actor{}, "GET", slaPolicyPath, nil, 401, "unauthorized")
		h.denied(actor{}, "GET", summaryPath, nil, 401, "unauthorized")
		h.denied(actor{}, "GET", findingsPath, nil, 401, "unauthorized")

		expired := h.addUser(h.admin, "viewer")
		expiredHash := sha256.Sum256([]byte(expired.cookie.Value))
		_, err := h.services.db.Exec(h.services.ctx, `UPDATE `+slaTable(h, "sessions")+`
			SET expires_at=$2 WHERE token_hash=$1`, expiredHash[:], slaNow)
		ok(t, "expire SLA session", err)
		h.denied(expired, "GET", summaryPath, nil, 401, "unauthorized")

		revoked := h.addUser(h.admin, "viewer")
		revokedHash := sha256.Sum256([]byte(revoked.cookie.Value))
		_, err = h.services.db.Exec(h.services.ctx, `UPDATE `+slaTable(h, "sessions")+`
			SET revoked_at=$2 WHERE token_hash=$1`, revokedHash[:], slaNow)
		ok(t, "revoke SLA session", err)
		h.denied(revoked, "GET", findingsPath, nil, 401, "unauthorized")

		unavailable := h.addUser(h.admin, "viewer")
		_, err = h.services.db.Exec(h.services.ctx, `DELETE FROM `+slaTable(h, "memberships")+`
			WHERE workspace_id=$1 AND user_id=$2`, unavailable.workspace, unavailable.user.ID)
		ok(t, "remove SLA membership", err)
		h.denied(unavailable, "GET", slaPolicyPath, nil, 403, "forbidden")
		h.denied(unavailable, "GET", summaryPath, nil, 403, "forbidden")
		h.denied(unavailable, "GET", findingsPath, nil, 403, "forbidden")

		foreign := h.addUser(h.addWorkspace(), "viewer")
		forged := foreign
		forged.workspace = h.admin.workspace
		h.denied(forged, "GET", slaPolicyPath, nil, 403, "forbidden")
		h.denied(forged, "GET", summaryPath, nil, 403, "forbidden")
		h.denied(forged, "GET", findingsPath, nil, 403, "forbidden")
	})
}
