//go:build integration

package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

const historicalTrendVerificationReason = "Historical snapshot trends are not an SLA, forecast, or independent verification."

var historicalTrendNow = time.Date(2026, 10, 8, 13, 58, 54, 0, time.UTC)

type historicalTrendTotals struct {
	Assets              int64 `json:"assets"`
	Findings            int64 `json:"findings"`
	OpenFindings        int64 `json:"openFindings"`
	AcceptedRisk        int64 `json:"acceptedRisk"`
	ExpiredAcceptedRisk int64 `json:"expiredAcceptedRisk"`
	Suppressed          int64 `json:"suppressed"`
	ExpiredSuppression  int64 `json:"expiredSuppression"`
	FalsePositive       int64 `json:"falsePositive"`
	InferredResolved    int64 `json:"inferredResolved"`
	VerifiedResolved    int64 `json:"verifiedResolved"`
}

type historicalTrendSeverity struct {
	Critical int64 `json:"critical"`
	High     int64 `json:"high"`
	Medium   int64 `json:"medium"`
	Low      int64 `json:"low"`
	Info     int64 `json:"info"`
}

type historicalTrendCoverage struct {
	ScannedAssets          int64 `json:"scannedAssets"`
	UnscannedAssets        int64 `json:"unscannedAssets"`
	StaleAssets            int64 `json:"staleAssets"`
	UnknownFreshnessAssets int64 `json:"unknownFreshnessAssets"`
	FreshnessWindowDays    int   `json:"freshnessWindowDays"`
}

type historicalTrendWindow struct {
	From time.Time `json:"from"`
	To   time.Time `json:"to"`
	Days int       `json:"days"`
}

type historicalTrendVerification struct {
	State  string `json:"state"`
	Reason string `json:"reason"`
}

type historicalSavedReport struct {
	WorkspaceID     string                      `json:"workspaceId"`
	AsOf            time.Time                   `json:"asOf"`
	Totals          historicalTrendTotals       `json:"totals"`
	BySeverity      historicalTrendSeverity     `json:"bySeverity"`
	Coverage        historicalTrendCoverage     `json:"coverage"`
	FreshnessWindow historicalTrendWindow       `json:"freshnessWindow"`
	Verification    historicalTrendVerification `json:"verification"`
}

type historicalTrendPoint struct {
	SnapshotID  string                  `json:"snapshotId"`
	Name        string                  `json:"name"`
	CompletedAt time.Time               `json:"completedAt"`
	AsOf        time.Time               `json:"asOf"`
	Totals      historicalTrendTotals   `json:"totals"`
	BySeverity  historicalTrendSeverity `json:"bySeverity"`
	Coverage    historicalTrendCoverage `json:"coverage"`
}

type historicalTrendDelta struct {
	Findings               int64 `json:"findings"`
	OpenFindings           int64 `json:"openFindings"`
	AcceptedRisk           int64 `json:"acceptedRisk"`
	Suppressed             int64 `json:"suppressed"`
	FalsePositive          int64 `json:"falsePositive"`
	Critical               int64 `json:"critical"`
	High                   int64 `json:"high"`
	Medium                 int64 `json:"medium"`
	Low                    int64 `json:"low"`
	Info                   int64 `json:"info"`
	ScannedAssets          int64 `json:"scannedAssets"`
	UnscannedAssets        int64 `json:"unscannedAssets"`
	StaleAssets            int64 `json:"staleAssets"`
	UnknownFreshnessAssets int64 `json:"unknownFreshnessAssets"`
}

type historicalTrend struct {
	WorkspaceID  string                      `json:"workspaceId"`
	From         time.Time                   `json:"from"`
	To           time.Time                   `json:"to"`
	Days         int                         `json:"days"`
	Points       []historicalTrendPoint      `json:"points"`
	Delta        *historicalTrendDelta       `json:"delta"`
	Verification historicalTrendVerification `json:"verification"`
}

type historicalTrendEnvelope struct {
	APIVersion string          `json:"apiVersion"`
	DataOrigin string          `json:"dataOrigin"`
	Trend      historicalTrend `json:"trend"`
}

type historicalSnapshotSeed struct {
	ID          string
	WorkspaceID string
	RequestedBy string
	Name        string
	State       string
	CompletedAt *time.Time
	Report      *historicalSavedReport
	CreatedAt   time.Time
}

type historicalSnapshotRow struct {
	ID, WorkspaceID, RequestedBy, Name, State, Report, FailureCode, FailureMessage, WorkerID string
	FreshnessDays, Attempts                                                                  int
	Fence                                                                                    int64
	CreatedAt                                                                                time.Time
	CompletedAt, LeaseUntil, AvailableAt                                                     *time.Time
}

type historicalDatabaseState struct {
	Snapshots []historicalSnapshotRow
	Counts    map[string]int
	Versions  []int
}

func historicalReport(workspace string, asOf time.Time, variant int) historicalSavedReport {
	reports := []struct {
		totals   historicalTrendTotals
		severity historicalTrendSeverity
		coverage historicalTrendCoverage
	}{
		{
			historicalTrendTotals{Assets: 10, Findings: 10, OpenFindings: 8, AcceptedRisk: 1, Suppressed: 2, InferredResolved: 1},
			historicalTrendSeverity{Critical: 1, High: 2, Medium: 3, Low: 3, Info: 1},
			historicalTrendCoverage{ScannedAssets: 8, UnscannedAssets: 2, StaleAssets: 3, UnknownFreshnessAssets: 1, FreshnessWindowDays: 7},
		},
		{
			historicalTrendTotals{Assets: 12, Findings: 12, OpenFindings: 9, AcceptedRisk: 2, Suppressed: 1, FalsePositive: 1},
			historicalTrendSeverity{Critical: 2, High: 3, Medium: 3, Low: 3, Info: 1},
			historicalTrendCoverage{ScannedAssets: 9, UnscannedAssets: 3, StaleAssets: 2, UnknownFreshnessAssets: 1, FreshnessWindowDays: 7},
		},
		{
			historicalTrendTotals{Assets: 11, Findings: 9, OpenFindings: 6, AcceptedRisk: 1, Suppressed: 2, FalsePositive: 1},
			historicalTrendSeverity{Critical: 1, High: 1, Medium: 3, Low: 3, Info: 1},
			historicalTrendCoverage{ScannedAssets: 7, UnscannedAssets: 4, StaleAssets: 4, UnknownFreshnessAssets: 2, FreshnessWindowDays: 7},
		},
		{
			historicalTrendTotals{Assets: 9, Findings: 7, OpenFindings: 4, AcceptedRisk: 3, Suppressed: 1, FalsePositive: 1},
			historicalTrendSeverity{Critical: 2, High: 1, Medium: 1, Low: 2, Info: 1},
			historicalTrendCoverage{ScannedAssets: 5, UnscannedAssets: 4, StaleAssets: 2, UnknownFreshnessAssets: 1, FreshnessWindowDays: 7},
		},
	}
	selected := reports[variant%len(reports)]
	return historicalSavedReport{
		WorkspaceID: workspace,
		AsOf:        asOf,
		Totals:      selected.totals,
		BySeverity:  selected.severity,
		Coverage:    selected.coverage,
		FreshnessWindow: historicalTrendWindow{
			From: asOf.Add(-7 * 24 * time.Hour), To: asOf, Days: 7,
		},
		Verification: historicalTrendVerification{
			State: "not-run", Reason: "Independent verified-resolution results are not integrated with canonical findings.",
		},
	}
}

func historicalSnapshotTable(h *harness) string {
	return pgx.Identifier{h.services.cfg.Schema, "app_report_snapshots"}.Sanitize()
}

func seedHistoricalSnapshot(t *testing.T, h *harness, seed historicalSnapshotSeed) {
	t.Helper()
	var report any
	if seed.Report != nil {
		data, err := json.Marshal(seed.Report)
		ok(t, "encode historical trend snapshot report", err)
		report = string(data)
	}
	var failureCode, failureMessage, workerID any
	var leaseUntil *time.Time
	attempts := 0
	var fence int64
	switch seed.State {
	case "processing":
		workerID = "historical-trend-worker"
		lease := historicalTrendNow.Add(time.Hour)
		leaseUntil, attempts, fence = &lease, 1, 1
	case "failed":
		failureCode, failureMessage = "synthetic-failure", "Synthetic failed snapshot control."
	}
	createdAt := seed.CreatedAt
	if createdAt.IsZero() {
		createdAt = historicalTrendNow.Add(-time.Hour)
	}
	_, err := h.services.db.Exec(h.services.ctx, `INSERT INTO `+historicalSnapshotTable(h)+`
		(id,workspace_id,name,requested_by,freshness_days,created_at,completed_at,state,report,
			failure_code,failure_message,worker_id,fence,attempts,lease_until,available_at)
		VALUES($1,$2,$3,$4,7,$5,$6,$7,$8::jsonb,$9,$10,$11,$12,$13,$14,$15)`,
		seed.ID, seed.WorkspaceID, seed.Name, seed.RequestedBy, createdAt, seed.CompletedAt,
		seed.State, report, failureCode, failureMessage, workerID, fence, attempts, leaseUntil, createdAt)
	ok(t, "seed historical trend snapshot", err)
}

func historicalJSONKeys(t *testing.T, raw []byte, label string, want ...string) map[string]json.RawMessage {
	t.Helper()
	var value map[string]json.RawMessage
	ok(t, "decode "+label+" object", json.Unmarshal(raw, &value))
	got := make(map[string]bool, len(value))
	for key := range value {
		got[key] = true
	}
	if len(got) != len(want) {
		t.Fatalf("%s keys got %v, want exactly %v", label, got, want)
	}
	for _, key := range want {
		if !got[key] {
			t.Fatalf("%s omitted required key %q", label, key)
		}
	}
	return value
}

func decodeHistoricalTrend(t *testing.T, body []byte) historicalTrendEnvelope {
	t.Helper()
	envelope := historicalJSONKeys(t, body, "historical trend envelope", "apiVersion", "dataOrigin", "trend")
	trend := historicalJSONKeys(t, envelope["trend"], "historical trend", "workspaceId", "from", "to", "days", "points", "delta", "verification")
	verification := historicalJSONKeys(t, trend["verification"], "historical trend verification", "state", "reason")
	_ = verification
	var points []json.RawMessage
	ok(t, "decode historical trend point array", json.Unmarshal(trend["points"], &points))
	for index, raw := range points {
		point := historicalJSONKeys(t, raw, fmt.Sprintf("historical trend point %d", index),
			"snapshotId", "name", "completedAt", "asOf", "totals", "bySeverity", "coverage")
		historicalJSONKeys(t, point["totals"], "historical trend totals",
			"assets", "findings", "openFindings", "acceptedRisk", "expiredAcceptedRisk", "suppressed",
			"expiredSuppression", "falsePositive", "inferredResolved", "verifiedResolved")
		historicalJSONKeys(t, point["bySeverity"], "historical trend severity",
			"critical", "high", "medium", "low", "info")
		historicalJSONKeys(t, point["coverage"], "historical trend coverage",
			"scannedAssets", "unscannedAssets", "staleAssets", "unknownFreshnessAssets", "freshnessWindowDays")
	}
	if !bytes.Equal(bytes.TrimSpace(trend["delta"]), []byte("null")) {
		historicalJSONKeys(t, trend["delta"], "historical trend delta",
			"findings", "openFindings", "acceptedRisk", "suppressed", "falsePositive",
			"critical", "high", "medium", "low", "info", "scannedAssets", "unscannedAssets",
			"staleAssets", "unknownFreshnessAssets")
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var result historicalTrendEnvelope
	ok(t, "strictly decode historical trend response", decoder.Decode(&result))
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		t.Fatal("historical trend response contains trailing JSON")
	}
	return result
}

func historicalSnapshots(t *testing.T, h *harness) []historicalSnapshotRow {
	t.Helper()
	rows, err := h.services.db.Query(h.services.ctx, `SELECT id,workspace_id,requested_by,name,freshness_days,
		state,created_at,completed_at,COALESCE(report::text,''),COALESCE(failure_code,''),
		COALESCE(failure_message,''),COALESCE(worker_id,''),fence,attempts,lease_until,available_at
		FROM `+historicalSnapshotTable(h)+` ORDER BY workspace_id,id`)
	ok(t, "read historical trend snapshot rows", err)
	defer rows.Close()
	var result []historicalSnapshotRow
	for rows.Next() {
		var item historicalSnapshotRow
		var available time.Time
		ok(t, "scan historical trend snapshot row", rows.Scan(
			&item.ID, &item.WorkspaceID, &item.RequestedBy, &item.Name, &item.FreshnessDays,
			&item.State, &item.CreatedAt, &item.CompletedAt, &item.Report, &item.FailureCode,
			&item.FailureMessage, &item.WorkerID, &item.Fence, &item.Attempts, &item.LeaseUntil, &available,
		))
		item.AvailableAt = &available
		result = append(result, item)
	}
	ok(t, "finish historical trend snapshot read", rows.Err())
	return result
}

func historicalSchemaVersions(t *testing.T, h *harness) []int {
	t.Helper()
	rows, err := h.services.db.Query(h.services.ctx, `SELECT version FROM `+
		pgx.Identifier{h.services.cfg.Schema, "app_schema_versions"}.Sanitize()+` ORDER BY version`)
	ok(t, "read historical trend schema ledger", err)
	defer rows.Close()
	var result []int
	for rows.Next() {
		var version int
		ok(t, "scan historical trend schema version", rows.Scan(&version))
		result = append(result, version)
	}
	ok(t, "finish historical trend schema ledger", rows.Err())
	return result
}

func historicalState(t *testing.T, h *harness) historicalDatabaseState {
	t.Helper()
	tables := []string{
		"assets", "imports", "findings", "report_snapshots", "finding_disposition_approvals",
		"finding_decision_events", "notification_policies", "notification_policy_revisions",
		"finding_change_events", "notification_policy_events", "finding_deliveries",
		"retention_previews", "retention_runs", "archive_publications",
	}
	counts := make(map[string]int, len(tables))
	for _, table := range tables {
		var count int
		ok(t, "count historical trend "+table, h.services.db.QueryRow(h.services.ctx, `SELECT count(*) FROM `+
			pgx.Identifier{h.services.cfg.Schema, "app_" + table}.Sanitize()+` WHERE workspace_id=$1`,
			h.admin.workspace).Scan(&count))
		counts[table] = count
	}
	return historicalDatabaseState{
		Snapshots: historicalSnapshots(t, h),
		Counts:    counts,
		Versions:  historicalSchemaVersions(t, h),
	}
}

func assertHistoricalPoint(t *testing.T, got historicalTrendPoint, seed historicalSnapshotSeed) {
	t.Helper()
	if seed.Report == nil || got.SnapshotID != seed.ID || got.Name != seed.Name ||
		!got.CompletedAt.Equal(*seed.CompletedAt) || !got.AsOf.Equal(seed.Report.AsOf) ||
		!reflect.DeepEqual(got.Totals, seed.Report.Totals) ||
		!reflect.DeepEqual(got.BySeverity, seed.Report.BySeverity) ||
		!reflect.DeepEqual(got.Coverage, seed.Report.Coverage) {
		t.Fatalf("historical trend point did not preserve its exact saved snapshot projection: %#v", got)
	}
	if !got.AsOf.Equal(got.CompletedAt) {
		t.Fatal("historical trend point broke the saved report asOf/completedAt invariant")
	}
}

type historicalStorageTripwire struct {
	server *httptest.Server
	armed  atomic.Bool
	calls  atomic.Int64
}

func newHistoricalStorageTripwire(t *testing.T, services *services) *historicalStorageTripwire {
	t.Helper()
	target, err := url.Parse(services.cfg.Storage.Endpoint)
	ok(t, "parse historical trend object-store destination", err)
	tripwire := &historicalStorageTripwire{}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = transport
	proxy.ErrorLog = log.New(io.Discard, "", 0)
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) { w.WriteHeader(http.StatusBadGateway) }
	bucketPath := "/" + services.cfg.Storage.Bucket
	rawPrefix := bucketPath + "/" + services.cfg.Storage.Prefix
	archivePrefix := bucketPath + "/" + services.cfg.ArchiveStorage.Prefix
	tripwire.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tripwire.armed.Load() {
			tripwire.calls.Add(1)
		}
		if !(r.Method == http.MethodHead && r.URL.Path == bucketPath) &&
			!strings.HasPrefix(r.URL.Path, rawPrefix) && !strings.HasPrefix(r.URL.Path, archivePrefix) {
			w.WriteHeader(http.StatusForbidden)
			return
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(func() {
		tripwire.server.CloseClientConnections()
		tripwire.server.Close()
		transport.CloseIdleConnections()
	})
	services.cfg.Storage.Endpoint = tripwire.server.URL
	services.cfg.ArchiveStorage.Endpoint = tripwire.server.URL
	return tripwire
}

func (s *historicalStorageTripwire) arm() {
	s.calls.Store(0)
	s.armed.Store(true)
}

func newHistoricalTrendHarness(t *testing.T, tracer pgx.QueryTracer, storage bool) (*harness, *historicalStorageTripwire) {
	t.Helper()
	requireApplication(t)
	h := &harness{t: t, services: ownedServices(t), password: secret(t)}
	h.clock.Store(historicalTrendNow.UnixNano())
	h.services.cfg.Now = func() time.Time { return time.Unix(0, h.clock.Load()).UTC() }
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
			ok(t, "close historical trend application", h.app.Close())
		}
	})
	h.enroll()
	return h, tripwire
}

func TestM10_HistoricalTrendsProjectImmutableSnapshotsAndDeltas(t *testing.T) {
	h, storage := newHistoricalTrendHarness(t, nil, true)
	viewer := h.addUser(h.admin, "viewer")
	foreignWorkspace := h.addWorkspace()
	foreignViewer := h.addUser(foreignWorkspace, "viewer")
	from := historicalTrendNow.Add(-10 * 24 * time.Hour)
	tied := historicalTrendNow.Add(-5 * 24 * time.Hour)
	seeds := []historicalSnapshotSeed{
		{ID: strings.Repeat("1", 32), WorkspaceID: h.admin.workspace, RequestedBy: h.admin.user.ID,
			Name: "Inclusive trend window start", State: "succeeded", CompletedAt: &from},
		{ID: strings.Repeat("2", 32), WorkspaceID: h.admin.workspace, RequestedBy: h.admin.user.ID,
			Name: "First tied completion", State: "succeeded", CompletedAt: &tied},
		{ID: strings.Repeat("3", 32), WorkspaceID: h.admin.workspace, RequestedBy: h.admin.user.ID,
			Name: "Second tied completion", State: "succeeded", CompletedAt: &tied},
		{ID: strings.Repeat("4", 32), WorkspaceID: h.admin.workspace, RequestedBy: h.admin.user.ID,
			Name: "Inclusive trend window end", State: "succeeded", CompletedAt: &historicalTrendNow},
	}
	for index := range seeds {
		report := historicalReport(seeds[index].WorkspaceID, *seeds[index].CompletedAt, index)
		seeds[index].Report = &report
		seedHistoricalSnapshot(t, h, seeds[index])
	}
	oldTime, futureTime := from.Add(-time.Second), historicalTrendNow.Add(24*time.Hour)
	oldReport := historicalReport(h.admin.workspace, oldTime, 0)
	futureReport := historicalReport(h.admin.workspace, futureTime, 1)
	foreignTime := historicalTrendNow.Add(-48 * time.Hour)
	foreignReport := historicalReport(foreignWorkspace.workspace, foreignTime, 2)
	controls := []historicalSnapshotSeed{
		{ID: strings.Repeat("5", 32), WorkspaceID: h.admin.workspace, RequestedBy: h.admin.user.ID,
			Name: "Queued trend control", State: "queued"},
		{ID: strings.Repeat("6", 32), WorkspaceID: h.admin.workspace, RequestedBy: h.admin.user.ID,
			Name: "Processing trend control", State: "processing"},
		{ID: strings.Repeat("7", 32), WorkspaceID: h.admin.workspace, RequestedBy: h.admin.user.ID,
			Name: "Failed trend control", State: "failed"},
		{ID: strings.Repeat("8", 32), WorkspaceID: h.admin.workspace, RequestedBy: h.admin.user.ID,
			Name: "Older trend control", State: "succeeded", CompletedAt: &oldTime, Report: &oldReport},
		{ID: strings.Repeat("9", 32), WorkspaceID: h.admin.workspace, RequestedBy: h.admin.user.ID,
			Name: "Future trend control", State: "succeeded", CompletedAt: &futureTime, Report: &futureReport},
		{ID: strings.Repeat("a", 32), WorkspaceID: foreignWorkspace.workspace, RequestedBy: foreignWorkspace.user.ID,
			Name: "Foreign workspace trend point", State: "succeeded", CompletedAt: &foreignTime, Report: &foreignReport},
	}
	for _, control := range controls {
		seedHistoricalSnapshot(t, h, control)
	}

	historyBefore := append([]byte(nil), h.request(h.admin, "GET", "/api/v1/reports/snapshots?limit=100", nil, 200).Body.Bytes()...)
	detailBefore := append([]byte(nil), h.request(h.admin, "GET", "/api/v1/reports/snapshots/"+seeds[0].ID, nil, 200).Body.Bytes()...)
	stateBefore := historicalState(t, h)
	wantVersions := make([]int, 24)
	for index := range wantVersions {
		wantVersions[index] = index + 1
	}
	equal(t, "historical trend schema stays at V24", stateBefore.Versions, wantVersions)
	storage.arm()

	viewerResponse := h.request(viewer, "GET", "/api/v1/reports/trends?days=10", nil, 200)
	if storage.calls.Load() != 0 {
		t.Fatal("historical trend read performed raw or archive object-store I/O")
	}
	result := decodeHistoricalTrend(t, viewerResponse.Body.Bytes())
	equal(t, "historical trend API version", result.APIVersion, apiVersion)
	equal(t, "historical trend live origin", result.DataOrigin, "live")
	equal(t, "historical trend workspace", result.Trend.WorkspaceID, h.admin.workspace)
	equal(t, "historical trend days", result.Trend.Days, 10)
	if !result.Trend.From.Equal(from) || !result.Trend.To.Equal(historicalTrendNow) {
		t.Fatal("historical trend did not preserve the exact inclusive server-Now window")
	}
	if result.Trend.Verification.State != "not-run" ||
		result.Trend.Verification.Reason != historicalTrendVerificationReason {
		t.Fatal("historical trend changed the fixed not-run verification statement")
	}
	if len(result.Trend.Points) != len(seeds) {
		t.Fatalf("historical trend returned %d points, want %d exact succeeded snapshots", len(result.Trend.Points), len(seeds))
	}
	for index, seed := range seeds {
		assertHistoricalPoint(t, result.Trend.Points[index], seed)
	}
	wantDelta := &historicalTrendDelta{
		Findings: -3, OpenFindings: -4, AcceptedRisk: 2, Suppressed: -1, FalsePositive: 1,
		Critical: 1, High: -1, Medium: -2, Low: -1, Info: 0,
		ScannedAssets: -3, UnscannedAssets: 2, StaleAssets: -1, UnknownFreshnessAssets: 0,
	}
	equal(t, "historical first-to-last delta", result.Trend.Delta, wantDelta)
	if after := historicalState(t, h); !reflect.DeepEqual(after, stateBefore) {
		t.Fatal("historical trend read mutated snapshot, scan, finding, provider, retention, archive, or schema state")
	}

	adminResponse := h.request(h.admin, "GET", "/api/v1/reports/trends?days=10", nil, 200)
	if !bytes.Equal(adminResponse.Body.Bytes(), viewerResponse.Body.Bytes()) {
		t.Fatal("historical trend response changed by current workspace role")
	}
	if !bytes.Equal(historyBefore, h.request(h.admin, "GET", "/api/v1/reports/snapshots?limit=100", nil, 200).Body.Bytes()) ||
		!bytes.Equal(detailBefore, h.request(h.admin, "GET", "/api/v1/reports/snapshots/"+seeds[0].ID, nil, 200).Body.Bytes()) {
		t.Fatal("historical trend read changed existing snapshot history or detail bytes")
	}

	foreign := decodeHistoricalTrend(t,
		h.request(foreignViewer, "GET", "/api/v1/reports/trends?days=10", nil, 200).Body.Bytes())
	if foreign.Trend.WorkspaceID != foreignWorkspace.workspace || len(foreign.Trend.Points) != 1 ||
		foreign.Trend.Points[0].SnapshotID != controls[len(controls)-1].ID || foreign.Trend.Delta != nil {
		t.Fatal("historical trend did not enforce selected membership or one-point null delta")
	}
	emptyWorkspace := h.addWorkspace()
	empty := decodeHistoricalTrend(t,
		h.request(emptyWorkspace, "GET", "/api/v1/reports/trends?days=10", nil, 200).Body.Bytes())
	if empty.Trend.WorkspaceID != emptyWorkspace.workspace || empty.Trend.Points == nil ||
		len(empty.Trend.Points) != 0 || empty.Trend.Delta != nil {
		t.Fatal("authorized empty historical trends must be an empty array with null delta")
	}
	forged := foreignViewer
	forged.workspace = h.admin.workspace
	h.denied(forged, "GET", "/api/v1/reports/trends?days=10", nil, 403, "forbidden")
	h.denied(actor{}, "GET", "/api/v1/reports/trends?days=10", nil, 401, "unauthorized")
	h.denied(viewer, "POST", "/api/v1/reports/snapshots", object{"name": "Viewer cannot create from trends"}, 403, "forbidden")

	beforeOverview := overview(t, h, h.admin)
	h.asset(h.admin, "Live overview changes outside immutable trend snapshots", nil)
	afterOverview := overview(t, h, h.admin)
	if afterOverview.Totals["assets"] != beforeOverview.Totals["assets"]+1 {
		t.Fatal("live overview fixture did not change independently of saved trend points")
	}
	afterRefresh := h.request(viewer, "GET", "/api/v1/reports/trends?days=10", nil, 200)
	if !bytes.Equal(afterRefresh.Body.Bytes(), viewerResponse.Body.Bytes()) {
		t.Fatal("live overview changes rewrote immutable historical trend receipts")
	}
	if !bytes.Equal(historyBefore, h.request(h.admin, "GET", "/api/v1/reports/snapshots?limit=100", nil, 200).Body.Bytes()) ||
		!bytes.Equal(detailBefore, h.request(h.admin, "GET", "/api/v1/reports/snapshots/"+seeds[0].ID, nil, 200).Body.Bytes()) {
		t.Fatal("live overview or trend refresh changed existing snapshot bytes")
	}
	if storage.calls.Load() != 0 {
		t.Fatal("historical trends, snapshot reads, or live overview refresh performed object-store I/O")
	}
}

func TestM10_HistoricalTrendsValidateQueriesAuthorityAndWholeWindowCap(t *testing.T) {
	t.Run("query contract", func(t *testing.T) {
		h, _ := newHistoricalTrendHarness(t, nil, false)
		viewer := h.addUser(h.admin, "viewer")
		insideTime := historicalTrendNow.Add(-20 * 24 * time.Hour)
		outsideTime := historicalTrendNow.Add(-31 * 24 * time.Hour)
		insideReport := historicalReport(h.admin.workspace, insideTime, 0)
		outsideReport := historicalReport(h.admin.workspace, outsideTime, 1)
		seedHistoricalSnapshot(t, h, historicalSnapshotSeed{
			ID: strings.Repeat("b", 32), WorkspaceID: h.admin.workspace, RequestedBy: h.admin.user.ID,
			Name: "Default window point", State: "succeeded", CompletedAt: &insideTime, Report: &insideReport,
		})
		seedHistoricalSnapshot(t, h, historicalSnapshotSeed{
			ID: strings.Repeat("c", 32), WorkspaceID: h.admin.workspace, RequestedBy: h.admin.user.ID,
			Name: "Outside default window point", State: "succeeded", CompletedAt: &outsideTime, Report: &outsideReport,
		})
		before := historicalState(t, h)
		defaultResult := decodeHistoricalTrend(t,
			h.request(viewer, "GET", "/api/v1/reports/trends", nil, 200).Body.Bytes())
		if defaultResult.Trend.Days != 30 ||
			!defaultResult.Trend.From.Equal(historicalTrendNow.Add(-30*24*time.Hour)) ||
			!defaultResult.Trend.To.Equal(historicalTrendNow) ||
			len(defaultResult.Trend.Points) != 1 ||
			defaultResult.Trend.Points[0].SnapshotID != strings.Repeat("b", 32) ||
			defaultResult.Trend.Delta != nil {
			t.Fatal("omitted historical trend days did not use the exact 30-day default")
		}
		longResult := decodeHistoricalTrend(t,
			h.request(viewer, "GET", "/api/v1/reports/trends?days=365", nil, 200).Body.Bytes())
		if longResult.Trend.Days != 365 || len(longResult.Trend.Points) != 2 ||
			longResult.Trend.Points[0].SnapshotID != strings.Repeat("c", 32) ||
			longResult.Trend.Points[1].SnapshotID != strings.Repeat("b", 32) {
			t.Fatal("365-day historical trend did not include and order exact saved points")
		}
		for _, path := range []string{
			"/api/v1/reports/trends?days=",
			"/api/v1/reports/trends?days=0",
			"/api/v1/reports/trends?days=366",
			"/api/v1/reports/trends?days=-1",
			"/api/v1/reports/trends?days=1.0",
			"/api/v1/reports/trends?days=1e2",
			"/api/v1/reports/trends?days=%2B1",
			"/api/v1/reports/trends?days=1&days=2",
			"/api/v1/reports/trends?days=30&workspaceId=" + h.admin.workspace,
			"/api/v1/reports/trends?Days=30",
		} {
			h.denied(viewer, "GET", path, nil, 400, "invalid-input")
		}
		h.denied(viewer, "POST", "/api/v1/reports/trends?days=30", object{}, 405, "method-not-allowed")
		if after := historicalState(t, h); !reflect.DeepEqual(after, before) {
			t.Fatal("valid or rejected historical trend query changed durable state")
		}
	})

	t.Run("whole window cap", func(t *testing.T) {
		h, _ := newHistoricalTrendHarness(t, nil, false)
		for index := 0; index < 100; index++ {
			completed := historicalTrendNow.Add(-time.Hour).Add(time.Duration(index) * time.Second)
			report := historicalReport(h.admin.workspace, completed, 0)
			seedHistoricalSnapshot(t, h, historicalSnapshotSeed{
				ID: fmt.Sprintf("%032x", index+1), WorkspaceID: h.admin.workspace, RequestedBy: h.admin.user.ID,
				Name:  fmt.Sprintf("Bounded historical point %03d", index+1),
				State: "succeeded", CompletedAt: &completed, Report: &report,
			})
		}
		atLimit := decodeHistoricalTrend(t,
			h.request(h.admin, "GET", "/api/v1/reports/trends?days=1", nil, 200).Body.Bytes())
		if len(atLimit.Trend.Points) != 100 ||
			atLimit.Trend.Points[0].SnapshotID != fmt.Sprintf("%032x", 1) ||
			atLimit.Trend.Points[99].SnapshotID != fmt.Sprintf("%032x", 100) ||
			!reflect.DeepEqual(atLimit.Trend.Delta, &historicalTrendDelta{}) {
			t.Fatal("historical trend did not return the exact 100-point whole window")
		}
		completed := historicalTrendNow.Add(-30 * time.Minute)
		report := historicalReport(h.admin.workspace, completed, 0)
		seedHistoricalSnapshot(t, h, historicalSnapshotSeed{
			ID: fmt.Sprintf("%032x", 101), WorkspaceID: h.admin.workspace, RequestedBy: h.admin.user.ID,
			Name: "Whole window point 101", State: "succeeded", CompletedAt: &completed, Report: &report,
		})
		response := h.request(h.admin, "GET", "/api/v1/reports/trends?days=1", nil, 413)
		var failure reply
		ok(t, "decode historical trend cap rejection", json.Unmarshal(response.Body.Bytes(), &failure))
		if failure.Error == nil || failure.Error.Code != "too-large" ||
			bytes.Contains(response.Body.Bytes(), []byte(`"trend"`)) {
			t.Fatal("101st historical trend point must return only an explicit too-large error")
		}
		if len(historicalSnapshots(t, h)) != 101 {
			t.Fatal("historical trend cap read changed or truncated immutable snapshot rows")
		}
	})
}

type historicalTraceMarker struct{}

type historicalTrendTracer struct {
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

func newHistoricalTrendTracer() *historicalTrendTracer {
	return &historicalTrendTracer{gate: newSecurityGate()}
}

func (t *historicalTrendTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if !t.armed.Load() {
		return ctx
	}
	role, _ := ctx.Value(securityTraceRole{}).(string)
	if role != "historical-trend-read" {
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
	if strings.Contains(sql, "app_report_snapshots") &&
		strings.Contains(sql, "succeeded") && strings.Contains(sql, "completed_at") {
		t.selects.Add(1)
		return context.WithValue(ctx, historicalTraceMarker{}, true)
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

func (t *historicalTrendTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	if data.Err == nil && ctx.Value(historicalTraceMarker{}) == true {
		t.once.Do(func() { _ = t.gate.block(ctx) })
	}
}

func TestM10_HistoricalTrendsUseOneStableReadOnlyRepeatableReadSnapshot(t *testing.T) {
	tracer := newHistoricalTrendTracer()
	h, _ := newHistoricalTrendHarness(t, tracer, false)
	firstTime, secondTime := historicalTrendNow.Add(-4*time.Hour), historicalTrendNow.Add(-2*time.Hour)
	firstReport := historicalReport(h.admin.workspace, firstTime, 0)
	secondReport := historicalReport(h.admin.workspace, secondTime, 1)
	for _, seed := range []historicalSnapshotSeed{
		{ID: strings.Repeat("d", 32), WorkspaceID: h.admin.workspace, RequestedBy: h.admin.user.ID,
			Name: "Stable trend first point", State: "succeeded", CompletedAt: &firstTime, Report: &firstReport},
		{ID: strings.Repeat("e", 32), WorkspaceID: h.admin.workspace, RequestedBy: h.admin.user.ID,
			Name: "Stable trend second point", State: "succeeded", CompletedAt: &secondTime, Report: &secondReport},
	} {
		seedHistoricalSnapshot(t, h, seed)
	}

	tracer.armed.Store(true)
	future := securityRequest(h, h.admin, "GET", "/api/v1/reports/trends?days=1", nil, 15*time.Second, "historical-trend-read")
	select {
	case <-tracer.gate.entered:
	case <-future.done:
		t.Fatalf("historical trend endpoint returned status %d before a real snapshot query", future.result.Code)
	case <-time.After(5 * time.Second):
		t.Fatal("historical trend endpoint did not reach its bounded snapshot query")
	}
	lateTime := historicalTrendNow.Add(-time.Hour)
	lateReport := historicalReport(h.admin.workspace, lateTime, 2)
	seedHistoricalSnapshot(t, h, historicalSnapshotSeed{
		ID: strings.Repeat("f", 32), WorkspaceID: h.admin.workspace, RequestedBy: h.admin.user.ID,
		Name: "Concurrent committed trend point", State: "succeeded", CompletedAt: &lateTime, Report: &lateReport,
	})
	tracer.gate.open()
	response := future.wait(t)
	if response.Code != http.StatusOK {
		t.Fatalf("historical trend stable read returned status %d", response.Code)
	}
	result := decodeHistoricalTrend(t, response.Body.Bytes())
	if len(result.Trend.Points) != 2 ||
		result.Trend.Points[0].SnapshotID != strings.Repeat("d", 32) ||
		result.Trend.Points[1].SnapshotID != strings.Repeat("e", 32) {
		t.Fatal("historical trend admitted a snapshot committed after its transaction snapshot")
	}
	if tracer.begins.Load() != 1 || !tracer.readOnly.Load() ||
		tracer.commits.Load() != 1 || tracer.rollbacks.Load() != 0 ||
		tracer.selects.Load() < 1 || tracer.writes.Load() != 0 || tracer.locks.Load() != 0 {
		t.Fatalf("historical trend transaction trace was not one read-only repeatable-read snapshot: begins=%d commits=%d rollbacks=%d selects=%d writes=%d locks=%d readOnly=%t",
			tracer.begins.Load(), tracer.commits.Load(), tracer.rollbacks.Load(), tracer.selects.Load(),
			tracer.writes.Load(), tracer.locks.Load(), tracer.readOnly.Load())
	}

	tracer.armed.Store(false)
	next := decodeHistoricalTrend(t,
		h.request(h.admin, "GET", "/api/v1/reports/trends?days=1", nil, 200).Body.Bytes())
	if len(next.Trend.Points) != 3 || next.Trend.Points[2].SnapshotID != strings.Repeat("f", 32) {
		t.Fatal("a later historical trend transaction did not observe the concurrently committed snapshot")
	}
}
