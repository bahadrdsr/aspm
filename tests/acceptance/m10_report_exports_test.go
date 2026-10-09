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
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bahadrdsr/aspm/tests/internal/sourcecompat"
	"github.com/jackc/pgx/v5"
)

const (
	reportExportsPath             = "/api/v1/reports/exports"
	reportExportGenerationFailure = "report-export-generation-failed"
	reportExportFormulaName       = " =SUM(1,2) quarterly posture"
)

var reportExportNow = time.Date(2026, 10, 9, 2, 3, 4, 123456789, time.UTC)

type reportExportFailure struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

type reportExportReceipt struct {
	ID           string               `json:"id"`
	WorkspaceID  string               `json:"workspaceId"`
	SnapshotID   string               `json:"snapshotId"`
	SnapshotName string               `json:"snapshotName"`
	RequestedBy  string               `json:"requestedBy"`
	Format       string               `json:"format"`
	State        string               `json:"state"`
	CreatedAt    time.Time            `json:"createdAt"`
	CompletedAt  *time.Time           `json:"completedAt"`
	Failure      *reportExportFailure `json:"failure"`
	Digest       *string              `json:"digest"`
	SizeBytes    *int64               `json:"sizeBytes"`
	Filename     *string              `json:"filename"`
}

type reportExportEnvelope struct {
	APIVersion string              `json:"apiVersion"`
	DataOrigin string              `json:"dataOrigin"`
	Export     reportExportReceipt `json:"export"`
}

type reportExportPage struct {
	APIVersion string                `json:"apiVersion"`
	DataOrigin string                `json:"dataOrigin"`
	Items      []reportExportReceipt `json:"items"`
	Total      int                   `json:"total"`
	NextCursor *string               `json:"nextCursor"`
}

type reportExportDBRow struct {
	ID, WorkspaceID, SnapshotID, RequestedBy, Format, IdempotencyKey, State string
	CreatedAt, AvailableAt                                                  time.Time
	CompletedAt, LeaseUntil                                                 *time.Time
	Content                                                                 []byte
	ContentDigest, FailureCode, FailureMessage, WorkerID                    *string
	ContentSize                                                             *int64
	Fence                                                                   int64
	Attempts                                                                int
}

func reportExportTable(h *harness) string {
	return pgx.Identifier{h.services.cfg.Schema, "app_report_exports"}.Sanitize()
}

func exportJSONKeys(t *testing.T, raw []byte, label string, want ...string) map[string]json.RawMessage {
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

func assertReportExportConsistency(t *testing.T, item reportExportReceipt) {
	t.Helper()
	if len(item.ID) != 32 || len(item.WorkspaceID) != 32 || len(item.SnapshotID) != 32 ||
		len(item.RequestedBy) != 32 || item.SnapshotName == "" ||
		(item.Format != "json" && item.Format != "csv") {
		t.Fatal("report export identity, binding or format is invalid")
	}
	wantFilename := "aspm-report-" + item.SnapshotID + "." + item.Format
	switch item.State {
	case "queued", "processing":
		if item.CompletedAt != nil || item.Failure != nil || item.Digest != nil ||
			item.SizeBytes != nil || item.Filename != nil {
			t.Fatal("nonterminal report export exposed terminal metadata")
		}
	case "failed":
		if item.CompletedAt == nil || item.Failure == nil || item.Failure.Code == "" ||
			item.Failure.Message == "" || item.Failure.Retryable || item.Digest != nil ||
			item.SizeBytes != nil || item.Filename != nil {
			t.Fatal("failed report export did not expose only its terminal diagnostic")
		}
	case "succeeded":
		if item.CompletedAt == nil || item.Failure != nil || item.Digest == nil ||
			!strings.HasPrefix(*item.Digest, "sha256:") || len(*item.Digest) != 71 ||
			item.SizeBytes == nil || *item.SizeBytes < 0 || *item.SizeBytes > 256<<10 ||
			item.Filename == nil || *item.Filename != wantFilename {
			t.Fatal("succeeded report export omitted exact artifact metadata")
		}
	default:
		t.Fatalf("unknown report export state %q", item.State)
	}
}

func decodeReportExport(t *testing.T, body []byte) reportExportEnvelope {
	t.Helper()
	envelope := exportJSONKeys(t, body, "report export envelope", "apiVersion", "dataOrigin", "export")
	item := exportJSONKeys(t, envelope["export"], "report export",
		"id", "workspaceId", "snapshotId", "snapshotName", "requestedBy", "format", "state",
		"createdAt", "completedAt", "failure", "digest", "sizeBytes", "filename")
	if !bytes.Equal(bytes.TrimSpace(item["failure"]), []byte("null")) {
		exportJSONKeys(t, item["failure"], "report export failure", "code", "message", "retryable")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var result reportExportEnvelope
	ok(t, "strictly decode report export response", decoder.Decode(&result))
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		t.Fatal("report export response contains trailing JSON")
	}
	if result.APIVersion != apiVersion || result.DataOrigin != "live" {
		t.Fatal("report export response changed API version or live data origin")
	}
	assertReportExportConsistency(t, result.Export)
	return result
}

func decodeReportExportPage(t *testing.T, body []byte) reportExportPage {
	t.Helper()
	envelope := exportJSONKeys(t, body, "report export page",
		"apiVersion", "dataOrigin", "items", "total", "nextCursor")
	var items []json.RawMessage
	ok(t, "decode report export page items", json.Unmarshal(envelope["items"], &items))
	for index, raw := range items {
		value := exportJSONKeys(t, raw, fmt.Sprintf("report export page item %d", index),
			"id", "workspaceId", "snapshotId", "snapshotName", "requestedBy", "format", "state",
			"createdAt", "completedAt", "failure", "digest", "sizeBytes", "filename")
		if !bytes.Equal(bytes.TrimSpace(value["failure"]), []byte("null")) {
			exportJSONKeys(t, value["failure"], "report export page failure", "code", "message", "retryable")
		}
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var result reportExportPage
	ok(t, "strictly decode report export page", decoder.Decode(&result))
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		t.Fatal("report export page contains trailing JSON")
	}
	if result.APIVersion != apiVersion || result.DataOrigin != "live" || result.Items == nil {
		t.Fatal("report export page changed API version, live origin or array shape")
	}
	seen := map[string]bool{}
	last := ""
	for _, item := range result.Items {
		assertReportExportConsistency(t, item)
		if item.ID <= last || seen[item.ID] {
			t.Fatal("report export page is not strictly ordered by unique lower-case ID")
		}
		seen[item.ID], last = true, item.ID
	}
	return result
}

func createReportExport(t *testing.T, h *harness, who actor, snapshotID, format, key string,
	want int) (*reportExportEnvelope, []byte) {
	t.Helper()
	response := h.request(who, http.MethodPost, reportExportsPath, encode(t, object{
		"snapshotId": snapshotID, "format": format, "idempotencyKey": key,
	}), want)
	raw := append([]byte(nil), response.Body.Bytes()...)
	if want >= 400 {
		h.decode(response)
		return nil, raw
	}
	result := decodeReportExport(t, raw)
	return &result, raw
}

func getReportExport(t *testing.T, h *harness, who actor, id string, want int) *reportExportEnvelope {
	t.Helper()
	response := h.request(who, http.MethodGet, reportExportsPath+"/"+id, nil, want)
	if want >= 400 {
		h.decode(response)
		return nil
	}
	result := decodeReportExport(t, response.Body.Bytes())
	return &result
}

func readReportExportRow(t *testing.T, h *harness, id string) reportExportDBRow {
	t.Helper()
	var row reportExportDBRow
	ok(t, "read durable report export row", h.services.db.QueryRow(h.services.ctx, `SELECT
		id,workspace_id,snapshot_id,requested_by,format,idempotency_key,state,created_at,completed_at,
		content,content_digest,content_size,failure_code,failure_message,worker_id,fence,attempts,
		lease_until,available_at
		FROM `+reportExportTable(h)+` WHERE id=$1`, id).Scan(
		&row.ID, &row.WorkspaceID, &row.SnapshotID, &row.RequestedBy, &row.Format, &row.IdempotencyKey,
		&row.State, &row.CreatedAt, &row.CompletedAt, &row.Content, &row.ContentDigest, &row.ContentSize,
		&row.FailureCode, &row.FailureMessage, &row.WorkerID, &row.Fence, &row.Attempts,
		&row.LeaseUntil, &row.AvailableAt,
	))
	return row
}

func reportExportRows(t *testing.T, h *harness) []reportExportDBRow {
	t.Helper()
	rows, err := h.services.db.Query(h.services.ctx, `SELECT
		id,workspace_id,snapshot_id,requested_by,format,idempotency_key,state,created_at,completed_at,
		content,content_digest,content_size,failure_code,failure_message,worker_id,fence,attempts,
		lease_until,available_at
		FROM `+reportExportTable(h)+` ORDER BY workspace_id,id`)
	ok(t, "read durable report export rows", err)
	defer rows.Close()
	var result []reportExportDBRow
	for rows.Next() {
		var row reportExportDBRow
		ok(t, "scan durable report export row", rows.Scan(
			&row.ID, &row.WorkspaceID, &row.SnapshotID, &row.RequestedBy, &row.Format, &row.IdempotencyKey,
			&row.State, &row.CreatedAt, &row.CompletedAt, &row.Content, &row.ContentDigest, &row.ContentSize,
			&row.FailureCode, &row.FailureMessage, &row.WorkerID, &row.Fence, &row.Attempts,
			&row.LeaseUntil, &row.AvailableAt,
		))
		result = append(result, row)
	}
	ok(t, "finish durable report export row read", rows.Err())
	return result
}

func reportExportFixture(workspace string) historicalSavedReport {
	return historicalSavedReport{
		WorkspaceID: workspace,
		AsOf:        reportExportNow,
		Totals: historicalTrendTotals{
			Assets: 11, Findings: 12, OpenFindings: 9, AcceptedRisk: 2,
			ExpiredAcceptedRisk: 1, Suppressed: 3, ExpiredSuppression: 1,
			FalsePositive: 4, InferredResolved: 5, VerifiedResolved: 0,
		},
		BySeverity: historicalTrendSeverity{Critical: 1, High: 2, Medium: 3, Low: 4, Info: 2},
		Coverage: historicalTrendCoverage{
			ScannedAssets: 8, UnscannedAssets: 3, StaleAssets: 2,
			UnknownFreshnessAssets: 1, FreshnessWindowDays: 14,
		},
		FreshnessWindow: historicalTrendWindow{
			From: reportExportNow.Add(-14 * 24 * time.Hour), To: reportExportNow, Days: 14,
		},
		Verification: historicalTrendVerification{
			State: "not-run", Reason: "Independent verified-resolution results are not integrated with canonical findings.",
		},
	}
}

type expectedReportExportJSON struct {
	APIVersion string `json:"apiVersion"`
	Export     struct {
		SnapshotID  string                `json:"snapshotId"`
		Name        string                `json:"name"`
		CompletedAt time.Time             `json:"completedAt"`
		Report      historicalSavedReport `json:"report"`
	} `json:"export"`
}

func expectedReportExportJSONBytes(t *testing.T, snapshotID, name string,
	completedAt time.Time, report historicalSavedReport) []byte {
	t.Helper()
	var document expectedReportExportJSON
	document.APIVersion = apiVersion
	document.Export.SnapshotID = snapshotID
	document.Export.Name = name
	document.Export.CompletedAt = completedAt
	document.Export.Report = report
	data, err := json.Marshal(document)
	ok(t, "encode independent exact report export JSON", err)
	return append(data, '\n')
}

func expectedCSVField(value string) string {
	if strings.ContainsAny(value, "\",\r\n") {
		return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
	}
	return value
}

func expectedReportExportCSVBytes(snapshotID, name string, completedAt time.Time,
	report historicalSavedReport) []byte {
	safeName := name
	trimmed := strings.TrimLeft(name, " \r\n\t")
	if strings.HasPrefix(name, "\t") || strings.HasPrefix(name, "\r") ||
		(trimmed != "" && strings.ContainsRune("=+-@", rune(trimmed[0]))) {
		safeName = "'" + name
	}
	rows := [][3]string{
		{"section", "metric", "value"},
		{"snapshot", "id", snapshotID},
		{"snapshot", "name", safeName},
		{"snapshot", "completedAt", completedAt.UTC().Format(time.RFC3339Nano)},
		{"report", "workspaceId", report.WorkspaceID},
		{"report", "asOf", report.AsOf.UTC().Format(time.RFC3339Nano)},
		{"totals", "assets", strconv.FormatInt(report.Totals.Assets, 10)},
		{"totals", "findings", strconv.FormatInt(report.Totals.Findings, 10)},
		{"totals", "openFindings", strconv.FormatInt(report.Totals.OpenFindings, 10)},
		{"totals", "acceptedRisk", strconv.FormatInt(report.Totals.AcceptedRisk, 10)},
		{"totals", "expiredAcceptedRisk", strconv.FormatInt(report.Totals.ExpiredAcceptedRisk, 10)},
		{"totals", "suppressed", strconv.FormatInt(report.Totals.Suppressed, 10)},
		{"totals", "expiredSuppression", strconv.FormatInt(report.Totals.ExpiredSuppression, 10)},
		{"totals", "falsePositive", strconv.FormatInt(report.Totals.FalsePositive, 10)},
		{"totals", "inferredResolved", strconv.FormatInt(report.Totals.InferredResolved, 10)},
		{"totals", "verifiedResolved", strconv.FormatInt(report.Totals.VerifiedResolved, 10)},
		{"severity", "critical", strconv.FormatInt(report.BySeverity.Critical, 10)},
		{"severity", "high", strconv.FormatInt(report.BySeverity.High, 10)},
		{"severity", "medium", strconv.FormatInt(report.BySeverity.Medium, 10)},
		{"severity", "low", strconv.FormatInt(report.BySeverity.Low, 10)},
		{"severity", "info", strconv.FormatInt(report.BySeverity.Info, 10)},
		{"coverage", "scannedAssets", strconv.FormatInt(report.Coverage.ScannedAssets, 10)},
		{"coverage", "unscannedAssets", strconv.FormatInt(report.Coverage.UnscannedAssets, 10)},
		{"coverage", "staleAssets", strconv.FormatInt(report.Coverage.StaleAssets, 10)},
		{"coverage", "unknownFreshnessAssets", strconv.FormatInt(report.Coverage.UnknownFreshnessAssets, 10)},
		{"coverage", "freshnessWindowDays", strconv.Itoa(report.Coverage.FreshnessWindowDays)},
		{"freshnessWindow", "from", report.FreshnessWindow.From.UTC().Format(time.RFC3339Nano)},
		{"freshnessWindow", "to", report.FreshnessWindow.To.UTC().Format(time.RFC3339Nano)},
		{"freshnessWindow", "days", strconv.Itoa(report.FreshnessWindow.Days)},
		{"verification", "state", report.Verification.State},
		{"verification", "reason", report.Verification.Reason},
	}
	var result strings.Builder
	for _, row := range rows {
		result.WriteString(expectedCSVField(row[0]))
		result.WriteByte(',')
		result.WriteString(expectedCSVField(row[1]))
		result.WriteByte(',')
		result.WriteString(expectedCSVField(row[2]))
		result.WriteString("\r\n")
	}
	return []byte(result.String())
}

func assertReportExportContent(t *testing.T, responseBody []byte, header http.Header,
	item reportExportReceipt, want []byte) {
	t.Helper()
	contentType := "application/json; charset=utf-8"
	if item.Format == "csv" {
		contentType = "text/csv; charset=utf-8"
	}
	if header.Get("Content-Type") != contentType ||
		header.Get("Content-Disposition") != `attachment; filename="`+*item.Filename+`"` ||
		header.Get("X-ASPM-Content-Digest") != *item.Digest ||
		header.Get("X-ASPM-Content-Length") != strconv.FormatInt(*item.SizeBytes, 10) {
		t.Fatal("report export content headers changed or omitted exact artifact metadata")
	}
	if !bytes.Equal(responseBody, want) {
		t.Fatal("report export content bytes changed")
	}
	sum := sha256.Sum256(want)
	if *item.Digest != fmt.Sprintf("sha256:%x", sum) || *item.SizeBytes != int64(len(want)) {
		t.Fatal("report export metadata does not describe the exact returned bytes")
	}
}

func TestM10_ReportExportsV26MigrationIsExactAdditiveAndEmpty(t *testing.T) {
	h, storage := newHistoricalTrendHarness(t, nil, true)
	completed := reportExportNow
	report := reportExportFixture(h.admin.workspace)
	snapshot := historicalSnapshotSeed{
		ID: strings.Repeat("6", 32), WorkspaceID: h.admin.workspace, RequestedBy: h.admin.user.ID,
		Name: "V26 preserved immutable snapshot", State: "succeeded", CompletedAt: &completed, Report: &report,
	}
	seedHistoricalSnapshot(t, h, snapshot)
	snapshotBefore := append([]byte(nil), h.request(h.admin, http.MethodGet,
		"/api/v1/reports/snapshots/"+snapshot.ID, nil, http.StatusOK).Body.Bytes()...)

	wantLedger := make([]int, 27)
	for index := range wantLedger {
		wantLedger[index] = index + 1
	}
	if ledger := historicalSchemaVersions(t, h); !reflect.DeepEqual(ledger, wantLedger) {
		t.Fatalf("report export migration ledger got %v, want exact V27 %v", ledger, wantLedger)
	}
	current := notificationDefinitions(t, h, sourcecompat.CurrentTables())
	sourcecompat.ValidateCurrentCatalog(t, current)
	var defaults int
	ok(t, "count V26 default report export rows",
		h.services.db.QueryRow(h.services.ctx, `SELECT count(*) FROM `+reportExportTable(h)).Scan(&defaults))
	if defaults != 0 {
		t.Fatal("V26 migration invented default report export jobs or artifacts")
	}

	ok(t, "close current application before exact V25 downgrade", h.app.Close())
	h.app = Application{}
	_, err := h.services.db.Exec(h.services.ctx, `DROP TABLE `+
		verificationTable(h, "verification_jobs")+`, `+
		verificationTable(h, "verification_approvals")+`, `+
		verificationTable(h, "verification_evidence")+`;
		DELETE FROM `+pgx.Identifier{h.services.cfg.Schema, "app_schema_versions"}.Sanitize()+` WHERE version=27;
		DROP TABLE `+reportExportTable(h)+`;
		DELETE FROM `+pgx.Identifier{h.services.cfg.Schema, "app_schema_versions"}.Sanitize()+` WHERE version=26`)
	ok(t, "remove only V27 verification and V26 report export storage", err)
	v25Catalog := notificationDefinitions(t, h, sourcecompat.V25CurrentTables())
	sourcecompat.ValidateV25CurrentCatalog(t, v25Catalog)
	storage.arm()

	h.open()
	if storage.calls.Load() != 0 {
		t.Fatal("V26/V27 migrations performed raw or archive object-store I/O")
	}
	after := notificationDefinitions(t, h, sourcecompat.CurrentTables())
	sourcecompat.ValidateCurrentCatalog(t, after)
	v26 := sourcecompat.ProjectV27Current(t, after)
	if projected := sourcecompat.ProjectV26Current(t, v26); !reflect.DeepEqual(projected, v25Catalog) {
		t.Fatal("V26 current catalog did not project to the exact observed V25 catalog")
	}
	if !bytes.Equal(snapshotBefore, h.request(h.admin, http.MethodGet,
		"/api/v1/reports/snapshots/"+snapshot.ID, nil, http.StatusOK).Body.Bytes()) {
		t.Fatal("V26 migration changed an existing immutable report snapshot")
	}
	if rows := reportExportRows(t, h); len(rows) != 0 {
		t.Fatal("V26 upgrade created report export rows")
	}
	beforeReopen := notificationDefinitions(t, h, sourcecompat.CurrentTables())
	h.restart()
	if !reflect.DeepEqual(historicalSchemaVersions(t, h), wantLedger) ||
		!reflect.DeepEqual(notificationDefinitions(t, h, sourcecompat.CurrentTables()), beforeReopen) ||
		len(reportExportRows(t, h)) != 0 {
		t.Fatal("V27 reopen repeated migration or changed the exact empty export catalog")
	}
}

func TestM10_ReportExportCreationReplayConflictAndAuthority(t *testing.T) {
	h, storage := newHistoricalTrendHarness(t, nil, true)
	analyst := h.addUser(h.admin, "analyst")
	viewer := h.addUser(h.admin, "viewer")
	foreign := h.addWorkspace()
	foreignViewer := h.addUser(foreign, "viewer")
	completed := reportExportNow
	report := reportExportFixture(h.admin.workspace)
	success := historicalSnapshotSeed{
		ID: strings.Repeat("1", 32), WorkspaceID: h.admin.workspace, RequestedBy: h.admin.user.ID,
		Name: "Succeeded immutable export source", State: "succeeded", CompletedAt: &completed, Report: &report,
	}
	second := success
	second.ID, second.Name = strings.Repeat("2", 32), "Second succeeded immutable export source"
	seedHistoricalSnapshot(t, h, success)
	seedHistoricalSnapshot(t, h, second)
	for index, state := range []string{"queued", "processing", "failed"} {
		seedHistoricalSnapshot(t, h, historicalSnapshotSeed{
			ID: fmt.Sprintf("%032x", index+10), WorkspaceID: h.admin.workspace,
			RequestedBy: h.admin.user.ID, Name: "Non-succeeded export source " + state, State: state,
		})
	}
	foreignReport := reportExportFixture(foreign.workspace)
	foreignSnapshot := historicalSnapshotSeed{
		ID: strings.Repeat("f", 32), WorkspaceID: foreign.workspace, RequestedBy: foreign.user.ID,
		Name: "Foreign succeeded immutable export source", State: "succeeded",
		CompletedAt: &completed, Report: &foreignReport,
	}
	seedHistoricalSnapshot(t, h, foreignSnapshot)
	before := historicalState(t, h)
	storage.arm()

	h.denied(viewer, http.MethodPost, reportExportsPath, object{
		"snapshotId": success.ID, "format": "json", "idempotencyKey": "viewer-denied",
	}, http.StatusForbidden, "forbidden")
	h.denied(actor{}, http.MethodPost, reportExportsPath, object{
		"snapshotId": success.ID, "format": "json", "idempotencyKey": "anonymous-denied",
	}, http.StatusUnauthorized, "unauthorized")
	for _, body := range []object{
		{"snapshotId": strings.Repeat("A", 32), "format": "json", "idempotencyKey": "upper-id"},
		{"snapshotId": success.ID, "format": "JSON", "idempotencyKey": "bad-format"},
		{"snapshotId": success.ID, "format": "json", "idempotencyKey": " "},
		{"snapshotId": success.ID, "format": "json", "idempotencyKey": "nul\u0000key"},
		{"snapshotId": success.ID, "format": "json", "idempotencyKey": strings.Repeat("é", 129)},
		{"snapshotId": success.ID, "format": "json", "idempotencyKey": "unknown-key", "extra": true},
		{"snapshotId": success.ID, "format": "json"},
	} {
		h.denied(h.admin, http.MethodPost, reportExportsPath, body, http.StatusBadRequest, "invalid-input")
	}
	for index := range 3 {
		h.denied(h.admin, http.MethodPost, reportExportsPath, object{
			"snapshotId": fmt.Sprintf("%032x", index+10), "format": "json",
			"idempotencyKey": "state-conflict-" + strconv.Itoa(index),
		}, http.StatusConflict, "conflict")
	}
	h.denied(h.admin, http.MethodPost, reportExportsPath, object{
		"snapshotId": strings.Repeat("9", 32), "format": "json", "idempotencyKey": "missing-source",
	}, http.StatusNotFound, "not-found")
	h.denied(h.admin, http.MethodPost, reportExportsPath, object{
		"snapshotId": foreignSnapshot.ID, "format": "json", "idempotencyKey": "foreign-source",
	}, http.StatusNotFound, "not-found")

	const key = "same-workspace-idempotent-export"
	first, firstRaw := createReportExport(t, h, analyst, success.ID, "json", key, http.StatusAccepted)
	if first.Export.State != "queued" || first.Export.RequestedBy != analyst.user.ID {
		t.Fatal("first export request did not persist an ordinary queued job for its requester")
	}
	row := readReportExportRow(t, h, first.Export.ID)
	if row.State != "queued" || row.SnapshotID != success.ID || row.RequestedBy != analyst.user.ID ||
		row.Format != "json" || row.IdempotencyKey != key || row.Attempts != 0 || row.Fence != 0 ||
		row.CompletedAt != nil || row.Content != nil || row.ContentDigest != nil || row.ContentSize != nil ||
		row.FailureCode != nil || row.FailureMessage != nil || row.WorkerID != nil || row.LeaseUntil != nil {
		t.Fatal("accepted report export request committed anything beyond the durable queued job")
	}
	replayed, replayRaw := createReportExport(t, h, h.admin, success.ID, "json", key, http.StatusAccepted)
	if !reflect.DeepEqual(replayed.Export, first.Export) || !bytes.Equal(firstRaw, replayRaw) {
		t.Fatal("exact queued idempotency replay did not return the original export receipt")
	}
	if replayed.Export.RequestedBy != analyst.user.ID {
		t.Fatal("idempotency replay rewrote the original requester")
	}
	for _, changed := range []object{
		{"snapshotId": second.ID, "format": "json", "idempotencyKey": key},
		{"snapshotId": success.ID, "format": "csv", "idempotencyKey": key},
	} {
		h.denied(h.admin, http.MethodPost, reportExportsPath, changed, http.StatusConflict, "conflict")
	}
	foreignCreated, _ := createReportExport(t, h, foreign, foreignSnapshot.ID, "json", key, http.StatusAccepted)
	if foreignCreated.Export.WorkspaceID != foreign.workspace || foreignCreated.Export.ID == first.Export.ID {
		t.Fatal("workspace-scoped idempotency did not isolate the foreign export")
	}
	h.denied(foreignViewer, http.MethodGet, reportExportsPath+"/"+first.Export.ID, nil,
		http.StatusNotFound, "not-found")
	if len(reportExportRows(t, h)) != 2 {
		t.Fatal("invalid, denied or conflicting export requests changed durable job cardinality")
	}
	if after := historicalState(t, h); !reflect.DeepEqual(after, before) {
		t.Fatal("report export creation changed snapshots, findings, decisions, providers, retention, archives or schema state")
	}
	if storage.calls.Load() != 0 {
		t.Fatal("report export creation or replay performed raw/archive object-store I/O")
	}
}

func TestM10_ReportWorkerDrainsSnapshotsAndExactJSONCSVExports(t *testing.T) {
	h, storage := newHistoricalTrendHarness(t, nil, true)
	viewer := h.addUser(h.admin, "viewer")
	report := reportExportFixture(h.admin.workspace)
	snapshot := historicalSnapshotSeed{
		ID: strings.Repeat("3", 32), WorkspaceID: h.admin.workspace, RequestedBy: h.admin.user.ID,
		Name: reportExportFormulaName, State: "succeeded", CompletedAt: &reportExportNow, Report: &report,
	}
	seedHistoricalSnapshot(t, h, snapshot)
	snapshotBefore := append([]byte(nil), h.request(h.admin, http.MethodGet,
		"/api/v1/reports/snapshots/"+snapshot.ID, nil, http.StatusOK).Body.Bytes()...)
	var savedJSON string
	ok(t, "read immutable source report JSON", h.services.db.QueryRow(h.services.ctx,
		`SELECT report::text FROM `+historicalSnapshotTable(h)+` WHERE id=$1`, snapshot.ID).Scan(&savedJSON))

	queuedSnapshotResponse := h.request(h.admin, http.MethodPost, "/api/v1/reports/snapshots",
		encode(t, object{"name": "Snapshot queue drained beside exports", "freshnessDays": 7}), http.StatusAccepted)
	var queuedSnapshot struct {
		Snapshot reportSnapshot
	}
	ok(t, "decode adjacent queued snapshot", json.Unmarshal(queuedSnapshotResponse.Body.Bytes(), &queuedSnapshot))
	jsonJob, _ := createReportExport(t, h, h.admin, snapshot.ID, "json",
		"exact-json-export", http.StatusAccepted)
	csvJob, _ := createReportExport(t, h, h.admin, snapshot.ID, "csv",
		"exact-csv-export", http.StatusAccepted)
	countsBefore := historicalState(t, h).Counts
	storage.arm()

	ok(t, "drain snapshot and report export queues", h.app.ProcessReports(h.services.ctx))
	if storage.calls.Load() != 0 {
		t.Fatal("database-only report worker performed raw/archive object-store I/O")
	}
	queuedSnapshotDetail := h.request(h.admin, http.MethodGet,
		"/api/v1/reports/snapshots/"+queuedSnapshot.Snapshot.ID, nil, http.StatusOK)
	ok(t, "decode adjacent completed snapshot", json.Unmarshal(queuedSnapshotDetail.Body.Bytes(), &queuedSnapshot))
	if queuedSnapshot.Snapshot.State != "succeeded" || queuedSnapshot.Snapshot.Report == nil {
		t.Fatal("ProcessReports returned before draining its pre-existing snapshot queue")
	}

	jsonDone := getReportExport(t, h, viewer, jsonJob.Export.ID, http.StatusOK)
	csvDone := getReportExport(t, h, viewer, csvJob.Export.ID, http.StatusOK)
	if jsonDone.Export.State != "succeeded" || csvDone.Export.State != "succeeded" {
		t.Fatal("ProcessReports returned before draining queued report exports")
	}
	wantJSON := expectedReportExportJSONBytes(t, snapshot.ID, snapshot.Name, reportExportNow, report)
	wantCSV := expectedReportExportCSVBytes(snapshot.ID, snapshot.Name, reportExportNow, report)
	if bytes.Contains(bytes.ReplaceAll(wantCSV, []byte("\r\n"), nil), []byte("\n")) ||
		!bytes.Contains(wantCSV, []byte(`snapshot,name,"' =SUM(1,2) quarterly posture"`+"\r\n")) {
		t.Fatal("independent CSV oracle is not exact CRLF or formula-safe")
	}
	jsonContent := h.request(viewer, http.MethodGet,
		reportExportsPath+"/"+jsonDone.Export.ID+"/content", nil, http.StatusOK)
	assertReportExportContent(t, jsonContent.Body.Bytes(), jsonContent.Header(), jsonDone.Export, wantJSON)
	csvContent := h.request(viewer, http.MethodGet,
		reportExportsPath+"/"+csvDone.Export.ID+"/content", nil, http.StatusOK)
	assertReportExportContent(t, csvContent.Body.Bytes(), csvContent.Header(), csvDone.Export, wantCSV)

	for _, item := range []reportExportReceipt{jsonDone.Export, csvDone.Export} {
		row := readReportExportRow(t, h, item.ID)
		if row.State != "succeeded" || row.Attempts != 1 || row.Fence != 1 ||
			row.WorkerID != nil || row.LeaseUntil != nil || row.FailureCode != nil ||
			row.CompletedAt == nil || !bytes.Equal(row.Content, map[string][]byte{"json": wantJSON, "csv": wantCSV}[item.Format]) ||
			row.ContentDigest == nil || *row.ContentDigest != *item.Digest ||
			row.ContentSize == nil || *row.ContentSize != *item.SizeBytes {
			t.Fatal("report worker did not atomically publish exact content and terminal metadata under one live fence")
		}
	}
	if !bytes.Equal(snapshotBefore, h.request(h.admin, http.MethodGet,
		"/api/v1/reports/snapshots/"+snapshot.ID, nil, http.StatusOK).Body.Bytes()) {
		t.Fatal("report export generation changed the selected immutable snapshot response")
	}
	var afterJSON string
	ok(t, "reread immutable source report JSON", h.services.db.QueryRow(h.services.ctx,
		`SELECT report::text FROM `+historicalSnapshotTable(h)+` WHERE id=$1`, snapshot.ID).Scan(&afterJSON))
	if afterJSON != savedJSON {
		t.Fatal("report export generation recomputed or rewrote saved report JSON")
	}
	if countsAfter := historicalState(t, h).Counts; !reflect.DeepEqual(countsAfter, countsBefore) {
		t.Fatal("report worker export generation wrote findings, providers, retention or archive state")
	}

	replay, _ := createReportExport(t, h, h.admin, snapshot.ID, "json",
		"exact-json-export", http.StatusOK)
	if !reflect.DeepEqual(replay.Export, jsonDone.Export) {
		t.Fatal("terminal idempotency replay did not return the exact succeeded export")
	}
	page := decodeReportExportPage(t, h.request(viewer, http.MethodGet,
		reportExportsPath+"?limit=100", nil, http.StatusOK).Body.Bytes())
	if page.Total != 2 || len(page.Items) != 2 || page.NextCursor != nil {
		t.Fatal("viewer could not read exact completed report export history")
	}
	ok(t, "empty combined report queues are safe to reprocess", h.app.ProcessReports(h.services.ctx))
	rowsBeforeReopen := reportExportRows(t, h)
	h.restart()
	if !reflect.DeepEqual(reportExportRows(t, h), rowsBeforeReopen) {
		t.Fatal("report export artifacts or worker metadata did not persist across reopen")
	}
	jsonAfterReopen := h.request(viewer, http.MethodGet,
		reportExportsPath+"/"+jsonDone.Export.ID+"/content", nil, http.StatusOK)
	assertReportExportContent(t, jsonAfterReopen.Body.Bytes(), jsonAfterReopen.Header(), jsonDone.Export, wantJSON)
	if storage.calls.Load() != 0 {
		t.Fatal("report export reads or reopen performed raw/archive object-store I/O")
	}
}

type reportExportListMarker struct{}

type reportExportListTracer struct {
	gate          *securityGate
	once          sync.Once
	armed         atomic.Bool
	inTransaction atomic.Bool
	begins        atomic.Int32
	commits       atomic.Int32
	selects       atomic.Int32
	writes        atomic.Int32
	locks         atomic.Int32
	readOnly      atomic.Bool
}

func newReportExportListTracer() *reportExportListTracer {
	return &reportExportListTracer{gate: newSecurityGate()}
}

func (t *reportExportListTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn,
	data pgx.TraceQueryStartData) context.Context {
	if !t.armed.Load() {
		return ctx
	}
	role, _ := ctx.Value(securityTraceRole{}).(string)
	if role != "report-export-list" {
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
	if strings.Contains(sql, "app_report_exports") && strings.Contains(sql, "select") {
		t.selects.Add(1)
		if strings.Contains(sql, "count(") {
			return context.WithValue(ctx, reportExportListMarker{}, true)
		}
	}
	if strings.HasPrefix(sql, "commit") {
		t.commits.Add(1)
		t.inTransaction.Store(false)
	}
	return ctx
}

func (t *reportExportListTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn,
	data pgx.TraceQueryEndData) {
	if data.Err == nil && ctx.Value(reportExportListMarker{}) == true {
		t.once.Do(func() { _ = t.gate.block(ctx) })
	}
}

func seedQueuedReportExport(t *testing.T, h *harness, id, snapshotID, key string, createdAt time.Time) {
	t.Helper()
	_, err := h.services.db.Exec(h.services.ctx, `INSERT INTO `+reportExportTable(h)+`
		(id,workspace_id,snapshot_id,requested_by,format,idempotency_key,created_at)
		VALUES($1,$2,$3,$4,'json',$5,$6)`,
		id, h.admin.workspace, snapshotID, h.admin.user.ID, key, createdAt)
	ok(t, "seed bounded queued report export", err)
}

func TestM10_ReportExportHistoryUsesNativePagingAndOneRepeatableRead(t *testing.T) {
	tracer := newReportExportListTracer()
	h, storage := newHistoricalTrendHarness(t, tracer, true)
	viewer := h.addUser(h.admin, "viewer")
	report := reportExportFixture(h.admin.workspace)
	snapshot := historicalSnapshotSeed{
		ID: strings.Repeat("4", 32), WorkspaceID: h.admin.workspace, RequestedBy: h.admin.user.ID,
		Name: "Paged export source", State: "succeeded", CompletedAt: &reportExportNow, Report: &report,
	}
	seedHistoricalSnapshot(t, h, snapshot)
	for index := 1; index <= 101; index++ {
		seedQueuedReportExport(t, h, fmt.Sprintf("%032x", index), snapshot.ID,
			fmt.Sprintf("paged-export-%03d", index), reportExportNow.Add(time.Duration(index)*time.Second))
	}
	storage.arm()

	first := decodeReportExportPage(t, h.request(viewer, http.MethodGet,
		reportExportsPath+"?limit=100", nil, http.StatusOK).Body.Bytes())
	if first.Total != 101 || len(first.Items) != 100 || first.NextCursor == nil ||
		*first.NextCursor != fmt.Sprintf("%032x", 100) ||
		first.Items[0].ID != fmt.Sprintf("%032x", 1) ||
		first.Items[99].ID != fmt.Sprintf("%032x", 100) {
		t.Fatal("report export history did not return the exact first native page")
	}
	second := decodeReportExportPage(t, h.request(viewer, http.MethodGet,
		reportExportsPath+"?limit=100&cursor="+*first.NextCursor, nil, http.StatusOK).Body.Bytes())
	if second.Total != 101 || len(second.Items) != 1 || second.NextCursor != nil ||
		second.Items[0].ID != fmt.Sprintf("%032x", 101) {
		t.Fatal("report export history did not honor the exact native cursor")
	}
	for _, path := range []string{
		reportExportsPath + "?limit=",
		reportExportsPath + "?limit=0",
		reportExportsPath + "?limit=101",
		reportExportsPath + "?limit=1.0",
		reportExportsPath + "?limit=1&limit=2",
		reportExportsPath + "?cursor=",
		reportExportsPath + "?cursor=" + strings.Repeat("A", 32),
		reportExportsPath + "?cursor=abc",
		reportExportsPath + "?page=2",
	} {
		h.denied(viewer, http.MethodGet, path, nil, http.StatusBadRequest, "invalid-input")
	}
	h.denied(viewer, http.MethodPost, reportExportsPath+"/"+first.Items[0].ID,
		object{}, http.StatusMethodNotAllowed, "method-not-allowed")

	tracer.armed.Store(true)
	inflight := securityRequest(h, viewer, http.MethodGet, reportExportsPath+"?limit=100",
		nil, 15*time.Second, "report-export-list")
	select {
	case <-tracer.gate.entered:
	case <-inflight.done:
		t.Fatalf("report export list returned status %d before its count query completed", inflight.result.Code)
	case <-time.After(5 * time.Second):
		t.Fatal("report export list did not reach its bounded count query")
	}
	lateID := strings.Repeat("f", 32)
	seedQueuedReportExport(t, h, lateID, snapshot.ID, "late-repeatable-read-export",
		reportExportNow.Add(10*time.Minute))
	tracer.gate.open()
	response := inflight.wait(t)
	if response.Code != http.StatusOK {
		t.Fatalf("stable report export page returned status %d", response.Code)
	}
	stable := decodeReportExportPage(t, response.Body.Bytes())
	if stable.Total != 101 || len(stable.Items) != 100 {
		t.Fatal("report export total/page mixed snapshots across a concurrent committed insert")
	}
	for _, item := range stable.Items {
		if item.ID == lateID {
			t.Fatal("in-flight report export page observed a row committed after its count")
		}
	}
	later := decodeReportExportPage(t, h.request(viewer, http.MethodGet,
		reportExportsPath+"?limit=100&cursor="+fmt.Sprintf("%032x", 101), nil, http.StatusOK).Body.Bytes())
	if later.Total != 102 || len(later.Items) != 1 || later.Items[0].ID != lateID {
		t.Fatal("later report export transaction did not observe the committed row")
	}
	if tracer.begins.Load() != 1 || tracer.commits.Load() != 1 || tracer.selects.Load() != 2 ||
		!tracer.readOnly.Load() || tracer.writes.Load() != 0 || tracer.locks.Load() != 0 {
		t.Fatalf("report export list did not use one read-only repeatable-read transaction: begin=%d commit=%d selects=%d writes=%d locks=%d readOnly=%t",
			tracer.begins.Load(), tracer.commits.Load(), tracer.selects.Load(),
			tracer.writes.Load(), tracer.locks.Load(), tracer.readOnly.Load())
	}
	if storage.calls.Load() != 0 {
		t.Fatal("report export history or detail reads performed object-store I/O")
	}
}

type reportExportWorkerMarker struct{ index int }

type reportExportWorkerTracer struct {
	gates   []*securityGate
	matches atomic.Int32
}

func newReportExportWorkerTracer(count int) *reportExportWorkerTracer {
	result := &reportExportWorkerTracer{gates: make([]*securityGate, count)}
	for index := range result.gates {
		result.gates[index] = newSecurityGate()
	}
	return result
}

func (t *reportExportWorkerTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn,
	data pgx.TraceQueryStartData) context.Context {
	role, _ := ctx.Value(securityTraceRole{}).(string)
	if role != "report-export-worker" {
		return ctx
	}
	sql := strings.ToLower(strings.Join(strings.Fields(data.SQL), " "))
	if strings.Contains(sql, "select") && strings.Contains(sql, "app_report_snapshots") {
		index := int(t.matches.Add(1) - 1)
		if index < len(t.gates) {
			return context.WithValue(ctx, reportExportWorkerMarker{}, reportExportWorkerMarker{index: index})
		}
	}
	return ctx
}

func (t *reportExportWorkerTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn,
	data pgx.TraceQueryEndData) {
	marker, present := ctx.Value(reportExportWorkerMarker{}).(reportExportWorkerMarker)
	if present && data.Err == nil {
		_ = t.gates[marker.index].block(ctx)
	}
}

func runReportWorker(t *testing.T, h *harness, tracerRole string) (*securityWorker, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithCancel(h.services.ctx)
	ctx = context.WithValue(ctx, securityTraceRole{}, tracerRole)
	result := &securityWorker{done: make(chan struct{}), cancel: cancel}
	go func() {
		defer close(result.done)
		result.err = h.app.ProcessReports(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-result.done:
		case <-time.After(5 * time.Second):
			t.Error("report worker did not stop after cancellation")
		}
	})
	return result, cancel
}

func waitReportExportState(t *testing.T, h *harness, id, want string) reportExportDBRow {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		row := readReportExportRow(t, h, id)
		if row.State == want {
			return row
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("report export %s did not reach %s", id, want)
	return reportExportDBRow{}
}

func TestM10_ReportExportWorkerCancellationFencingAndAttemptLimit(t *testing.T) {
	t.Run("cancellation and stale fence", func(t *testing.T) {
		tracer := newReportExportWorkerTracer(2)
		h, storage := newHistoricalTrendHarness(t, tracer, true)
		for _, gate := range tracer.gates {
			t.Cleanup(gate.open)
		}
		report := reportExportFixture(h.admin.workspace)
		snapshot := historicalSnapshotSeed{
			ID: strings.Repeat("5", 32), WorkspaceID: h.admin.workspace, RequestedBy: h.admin.user.ID,
			Name: "Cancellation fenced export source", State: "succeeded",
			CompletedAt: &reportExportNow, Report: &report,
		}
		seedHistoricalSnapshot(t, h, snapshot)
		job, _ := createReportExport(t, h, h.admin, snapshot.ID, "json",
			"cancelled-then-retried-export", http.StatusAccepted)
		storage.arm()

		first, cancelFirst := runReportWorker(t, h, "report-export-worker")
		select {
		case <-tracer.gates[0].entered:
		case <-first.done:
			t.Fatalf("first report export worker returned early: %v", first.err)
		case <-time.After(5 * time.Second):
			t.Fatal("first report export worker did not reach immutable snapshot generation")
		}
		firstProcessing := waitReportExportState(t, h, job.Export.ID, "processing")
		if firstProcessing.WorkerID == nil || firstProcessing.Fence != 1 || firstProcessing.Attempts != 1 ||
			firstProcessing.LeaseUntil == nil {
			t.Fatal("first report export claim did not establish its live lease and fence")
		}
		h.denied(h.admin, http.MethodGet, reportExportsPath+"/"+job.Export.ID+"/content", nil,
			http.StatusConflict, "conflict")
		cancelFirst()
		select {
		case <-first.done:
		case <-time.After(5 * time.Second):
			t.Fatal("cancelled report export worker did not return")
		}
		if !errors.Is(first.err, context.Canceled) {
			t.Fatalf("cancelled report export worker returned %v, want context cancellation", first.err)
		}
		requeued := waitReportExportState(t, h, job.Export.ID, "queued")
		if requeued.WorkerID != nil || requeued.LeaseUntil != nil || requeued.Attempts != 1 ||
			requeued.Fence != 1 || requeued.Content != nil || requeued.FailureCode != nil {
			t.Fatal("cancelled report export attempt was not safely requeued without an artifact")
		}

		second, _ := runReportWorker(t, h, "report-export-worker")
		select {
		case <-tracer.gates[1].entered:
		case <-second.done:
			t.Fatalf("second report export worker returned early: %v", second.err)
		case <-time.After(5 * time.Second):
			t.Fatal("second report export worker did not reach immutable snapshot generation")
		}
		secondProcessing := waitReportExportState(t, h, job.Export.ID, "processing")
		if secondProcessing.WorkerID == nil || *secondProcessing.WorkerID == *firstProcessing.WorkerID ||
			secondProcessing.Fence != 2 || secondProcessing.Attempts != 2 {
			t.Fatal("reclaimed report export did not advance to a distinct worker and fence")
		}
		fake := []byte("stale worker must not publish\n")
		fakeDigest := fmt.Sprintf("sha256:%x", sha256.Sum256(fake))
		stale := make(chan struct {
			rows int64
			err  error
		}, 1)
		go func() {
			result, err := h.services.db.Exec(h.services.ctx, `UPDATE `+reportExportTable(h)+`
				SET state='succeeded',completed_at=clock_timestamp(),content=$4,content_digest=$5,content_size=$6,
					worker_id=NULL,lease_until=NULL
				WHERE id=$1 AND worker_id=$2 AND fence=$3 AND state='processing' AND lease_until>clock_timestamp()`,
				job.Export.ID, *firstProcessing.WorkerID, firstProcessing.Fence, fake, fakeDigest, len(fake))
			stale <- struct {
				rows int64
				err  error
			}{rows: result.RowsAffected(), err: err}
		}()
		tracer.gates[1].open()
		select {
		case <-second.done:
		case <-time.After(5 * time.Second):
			t.Fatal("released report export worker did not finish")
		}
		ok(t, "finish reclaimed report export", second.err)
		var staleResult struct {
			rows int64
			err  error
		}
		select {
		case staleResult = <-stale:
		case <-time.After(5 * time.Second):
			t.Fatal("stale report export publication did not return after current commit")
		}
		ok(t, "attempt stale report export publication", staleResult.err)
		if staleResult.rows != 0 {
			t.Fatal("stale report export fence published an artifact")
		}
		done := readReportExportRow(t, h, job.Export.ID)
		want := expectedReportExportJSONBytes(t, snapshot.ID, snapshot.Name, reportExportNow, report)
		if done.State != "succeeded" || done.Fence != 2 || done.Attempts != 2 ||
			!bytes.Equal(done.Content, want) || bytes.Equal(done.Content, fake) {
			t.Fatal("only the current report export fence should publish exact content")
		}
		if storage.calls.Load() != 0 {
			t.Fatal("cancelled or reclaimed report export worker performed object-store I/O")
		}
	})

	t.Run("three failed generations become terminal", func(t *testing.T) {
		h, storage := newHistoricalTrendHarness(t, nil, true)
		report := reportExportFixture(h.admin.workspace)
		snapshot := historicalSnapshotSeed{
			ID: strings.Repeat("7", 32), WorkspaceID: h.admin.workspace, RequestedBy: h.admin.user.ID,
			Name: "Malformed saved report generation control", State: "succeeded",
			CompletedAt: &reportExportNow, Report: &report,
		}
		seedHistoricalSnapshot(t, h, snapshot)
		_, err := h.services.db.Exec(h.services.ctx, `UPDATE `+historicalSnapshotTable(h)+`
			SET report='{"workspaceId":7}'::jsonb WHERE id=$1`, snapshot.ID)
		ok(t, "seed malformed immutable report object", err)
		var malformedBefore string
		ok(t, "read malformed immutable report object", h.services.db.QueryRow(h.services.ctx,
			`SELECT report::text FROM `+historicalSnapshotTable(h)+` WHERE id=$1`, snapshot.ID).Scan(&malformedBefore))
		job, _ := createReportExport(t, h, h.admin, snapshot.ID, "json",
			"terminal-generation-failure", http.StatusAccepted)
		storage.arm()
		ctx, cancel := context.WithTimeout(h.services.ctx, 15*time.Second)
		defer cancel()
		ok(t, "drain three failed report export attempts", h.app.ProcessReports(ctx))
		failed := readReportExportRow(t, h, job.Export.ID)
		if failed.State != "failed" || failed.Attempts != 3 || failed.Fence != 3 ||
			failed.CompletedAt == nil || failed.FailureCode == nil ||
			*failed.FailureCode != reportExportGenerationFailure ||
			failed.FailureMessage == nil || strings.TrimSpace(*failed.FailureMessage) == "" ||
			failed.Content != nil || failed.ContentDigest != nil || failed.ContentSize != nil ||
			failed.WorkerID != nil || failed.LeaseUntil != nil {
			t.Fatal("three export generation failures did not become one terminal diagnostic without content")
		}
		detail := getReportExport(t, h, h.admin, job.Export.ID, http.StatusOK)
		if detail.Export.State != "failed" || detail.Export.Failure == nil ||
			detail.Export.Failure.Code != reportExportGenerationFailure {
			t.Fatal("terminal export generation failure is not honestly represented by the API")
		}
		h.denied(h.admin, http.MethodGet, reportExportsPath+"/"+job.Export.ID+"/content", nil,
			http.StatusConflict, "conflict")
		var malformedAfter string
		ok(t, "reread malformed immutable report object", h.services.db.QueryRow(h.services.ctx,
			`SELECT report::text FROM `+historicalSnapshotTable(h)+` WHERE id=$1`, snapshot.ID).Scan(&malformedAfter))
		if malformedAfter != malformedBefore {
			t.Fatal("failed export generation rewrote its immutable source snapshot")
		}
		if storage.calls.Load() != 0 {
			t.Fatal("failed report export generation performed object-store I/O")
		}
	})
}

func TestM10_ReportExportContentStateAuthorityAndCorruption(t *testing.T) {
	h, storage := newHistoricalTrendHarness(t, nil, true)
	viewer := h.addUser(h.admin, "viewer")
	foreign := h.addWorkspace()
	report := reportExportFixture(h.admin.workspace)
	snapshot := historicalSnapshotSeed{
		ID: strings.Repeat("8", 32), WorkspaceID: h.admin.workspace, RequestedBy: h.admin.user.ID,
		Name: "Content integrity export source", State: "succeeded",
		CompletedAt: &reportExportNow, Report: &report,
	}
	seedHistoricalSnapshot(t, h, snapshot)
	storage.arm()
	queued, _ := createReportExport(t, h, h.admin, snapshot.ID, "json",
		"queued-content-conflict", http.StatusAccepted)
	h.denied(viewer, http.MethodGet, reportExportsPath+"/"+queued.Export.ID+"/content", nil,
		http.StatusConflict, "conflict")
	ok(t, "process content integrity export", h.app.ProcessReports(h.services.ctx))
	done := getReportExport(t, h, viewer, queued.Export.ID, http.StatusOK)
	want := expectedReportExportJSONBytes(t, snapshot.ID, snapshot.Name, reportExportNow, report)
	content := h.request(viewer, http.MethodGet,
		reportExportsPath+"/"+done.Export.ID+"/content", nil, http.StatusOK)
	assertReportExportContent(t, content.Body.Bytes(), content.Header(), done.Export, want)
	h.denied(foreign, http.MethodGet, reportExportsPath+"/"+done.Export.ID+"/content", nil,
		http.StatusNotFound, "not-found")
	h.denied(viewer, http.MethodPost, reportExportsPath+"/"+done.Export.ID+"/content", object{},
		http.StatusMethodNotAllowed, "method-not-allowed")
	h.denied(viewer, http.MethodHead, reportExportsPath+"/"+done.Export.ID+"/content", nil,
		http.StatusMethodNotAllowed, "method-not-allowed")

	_, err := h.services.db.Exec(h.services.ctx, `UPDATE `+reportExportTable(h)+`
		SET content=set_byte(content,0,(get_byte(content,0)+1)%256) WHERE id=$1`, done.Export.ID)
	ok(t, "corrupt one persisted report export byte without changing its length", err)
	corrupt := h.request(viewer, http.MethodGet,
		reportExportsPath+"/"+done.Export.ID+"/content", nil, http.StatusUnprocessableEntity)
	failure := h.decode(corrupt)
	if failure.Error == nil || failure.Error.Code != "evidence-corrupt" ||
		bytes.Equal(corrupt.Body.Bytes(), want) ||
		corrupt.Header().Get("X-ASPM-Content-Digest") != "" ||
		corrupt.Header().Get("X-ASPM-Content-Length") != "" ||
		corrupt.Header().Get("Content-Disposition") != "" {
		t.Fatal("corrupt report export content did not fail closed before artifact headers or bytes")
	}
	metadata := getReportExport(t, h, viewer, done.Export.ID, http.StatusOK)
	if !reflect.DeepEqual(metadata.Export, done.Export) {
		t.Fatal("corruption detection rewrote the durable export receipt")
	}
	if storage.calls.Load() != 0 {
		t.Fatal("report export content retrieval performed object-store I/O")
	}
}
