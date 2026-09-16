//go:build integration

package acceptance

import (
	"encoding/json"
	"testing"
	"time"
)

type postureReport struct {
	WorkspaceID string
	AsOf        time.Time
	Totals      map[string]int
	BySeverity  map[string]int
	Coverage    struct {
		ScannedAssets, UnscannedAssets, StaleAssets, UnknownFreshnessAssets int
		FreshnessWindowDays                                                 int
	}
}

type reportSnapshot struct {
	ID, Name, State string
	Report          *postureReport
}

func overview(t *testing.T, h *harness, who actor) postureReport {
	t.Helper()
	response := h.request(who, "GET", "/api/v1/reports/overview?freshnessDays=7", nil, 200)
	var body struct {
		APIVersion, DataOrigin string
		Report                 postureReport
	}
	ok(t, "decode posture report", json.Unmarshal(response.Body.Bytes(), &body))
	equal(t, "report version", body.APIVersion, apiVersion)
	equal(t, "report is persisted application data", body.DataOrigin, "live")
	equal(t, "report workspace", body.Report.WorkspaceID, who.workspace)
	sameTime(t, "report clock", &body.Report.AsOf, pointerTime(h.services.cfg.Now()))
	return body.Report
}

func pointerTime(value time.Time) *time.Time { return &value }

func TestM10_PostureSeparatesHumanWorkSourceResolutionAndCoverage(t *testing.T) {
	h := newHarness(t, true)
	input, _, first := h.seed()
	h.asset(h.admin, "Never assessed repository", nil)
	initial := overview(t, h, h.admin)
	for key, want := range map[string]int{"assets": 2, "findings": 1, "openFindings": 1, "acceptedRisk": 0, "expiredAcceptedRisk": 0, "inferredResolved": 0, "verifiedResolved": 0} {
		equal(t, "initial total "+key, initial.Totals[key], want)
	}
	equal(t, "source severity aggregation", initial.BySeverity["medium"], 1)
	equal(t, "complete assessed asset", initial.Coverage.ScannedAssets, 1)
	equal(t, "unscanned asset remains visible", initial.Coverage.UnscannedAssets, 1)
	equal(t, "old source scan is stale", initial.Coverage.StaleAssets, 1)
	equal(t, "declared freshness window", initial.Coverage.FreshnessWindowDays, 7)
	expiry := h.services.cfg.Now().Add(time.Minute)
	h.json(h.admin, "PATCH", "/api/v1/findings/"+first.ID, object{
		"disposition": "accepted-risk", "acceptedRiskExpiresAt": expiry,
	}, 200)
	h.clock.Add(int64(2 * time.Minute))
	newer := sourceTime.Add(24 * time.Hour)
	input["scanId"], input["sourceScanAt"], input["collectedAt"] = "reporting-absence", newer, newer.Add(time.Hour)
	input["report"] = string(sarif(t, func(run, _ object) { run["results"] = []any{} }))
	h.finish(h.upload(input).ID, "succeeded")
	current := overview(t, h, h.admin)
	for key, want := range map[string]int{"findings": 1, "openFindings": 1, "acceptedRisk": 1, "expiredAcceptedRisk": 1, "inferredResolved": 1, "verifiedResolved": 0} {
		equal(t, "source inference/expiry total "+key, current.Totals[key], want)
	}
	equal(t, "inference did not close human work", h.finding(h.admin, first.ID).WorkflowState, "open")
}

func TestM10_ReportsRemainWorkspaceAuthorized(t *testing.T) {
	h := newHarness(t, true)
	h.seed()
	viewer := h.addUser(h.admin, "viewer")
	equal(t, "viewer can inspect its own report", overview(t, h, viewer).Totals["findings"], 1)
	other := h.addUser(h.addWorkspace(), "admin")
	equal(t, "foreign findings not aggregated", overview(t, h, other).Totals["findings"], 0)
	equal(t, "foreign assets not aggregated", overview(t, h, other).Totals["assets"], 0)
	h.denied(actor{}, "GET", "/api/v1/reports/overview", nil, 401, "unauthorized")
	forged := other
	forged.workspace = h.admin.workspace
	h.denied(forged, "GET", "/api/v1/reports/overview", nil, 403, "forbidden")
	h.denied(h.admin, "GET", "/api/v1/reports/overview?freshnessDays=0", nil, 400, "invalid-input")
	h.denied(h.admin, "GET", "/api/v1/reports/overview?freshnessDays=999999", nil, 400, "invalid-input")
}

func TestM10_SnapshotsAreQueuedDurableImmutableAndAuthorized(t *testing.T) {
	h := newHarness(t, true)
	h.seed()
	viewer := h.addUser(h.admin, "viewer")
	h.denied(viewer, "POST", "/api/v1/reports/snapshots", object{"name": "Denied snapshot"}, 403, "forbidden")
	response := h.request(h.admin, "POST", "/api/v1/reports/snapshots", encode(t, object{"name": "Before inventory growth", "freshnessDays": 7}), 202)
	var body struct {
		APIVersion string
		Snapshot   reportSnapshot
	}
	ok(t, "decode queued snapshot", json.Unmarshal(response.Body.Bytes(), &body))
	equal(t, "queued snapshot API version", body.APIVersion, apiVersion)
	if body.Snapshot.ID == "" || body.Snapshot.State != "queued" || body.Snapshot.Report != nil {
		t.Fatal("snapshot submission must queue real work, not invent an inline completed report")
	}
	id := body.Snapshot.ID
	h.restart()
	response = h.request(h.admin, "GET", "/api/v1/reports/snapshots/"+id, nil, 200)
	ok(t, "decode reopened snapshot", json.Unmarshal(response.Body.Bytes(), &body))
	equal(t, "snapshot queue persisted", body.Snapshot.State, "queued")
	if h.app.ProcessReports == nil {
		t.Fatal("M10 production report-worker binding is missing")
	}
	ok(t, "process real report jobs", h.app.ProcessReports(h.services.ctx))
	response = h.request(viewer, "GET", "/api/v1/reports/snapshots/"+id, nil, 200)
	ok(t, "decode completed snapshot", json.Unmarshal(response.Body.Bytes(), &body))
	if body.Snapshot.State != "succeeded" || body.Snapshot.Report == nil {
		t.Fatal("report worker did not persist a completed snapshot")
	}
	saved := body.Snapshot
	equal(t, "snapshot original asset count", saved.Report.Totals["assets"], 1)
	h.asset(h.admin, "Later inventory", nil)
	equal(t, "current report changed", overview(t, h, h.admin).Totals["assets"], 2)
	ok(t, "empty report queue is safe to reprocess", h.app.ProcessReports(h.services.ctx))
	response = h.request(h.admin, "GET", "/api/v1/reports/snapshots/"+id, nil, 200)
	ok(t, "decode unchanged snapshot", json.Unmarshal(response.Body.Bytes(), &body))
	equal(t, "completed snapshot is immutable", body.Snapshot, saved)
	other := h.addUser(h.addWorkspace(), "admin")
	h.denied(other, "GET", "/api/v1/reports/snapshots/"+id, nil, 404, "not-found")
	h.denied(actor{}, "GET", "/api/v1/reports/snapshots/"+id, nil, 401, "unauthorized")
}
