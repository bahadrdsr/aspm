//go:build integration

package acceptance

import (
	"bytes"
	"testing"
	"time"
)

func TestM06_IdenticalRunIsIdempotentAndConflictingReplayIsRejected(t *testing.T) {
	h := newHarness(t, true)
	input, firstRun, first := h.seed()
	equal(t, "initial observation", len(first.Observations), 1)
	if first.Observations[0].ID == "" {
		t.Fatal("observation needs its own stable identity")
	}
	input["collectedAt"] = sourceTime.Add(24 * time.Hour)
	replay := h.finish(h.upload(input).ID, "succeeded")
	equal(t, "reimport is not another scan", replay.RunID, firstRun.RunID)
	after := h.finding(h.admin, first.ID)
	equal(t, "reimport does not create another issue", len(h.work(h.admin, "")), 1)
	equal(t, "reimport does not create another observation", len(after.Observations), 1)
	equal(t, "observation identity retained", after.Observations[0].ID, first.Observations[0].ID)
	sameTime(t, "repeat scan time", after.SourceScanAt, &sourceTime)
	sameTime(t, "repeat freshness", after.SourceFreshnessAt, &sourceTime)
	input["report"] = string(sarif(t, func(_ object, result object) { result["level"] = "error" }))
	h.denied(h.admin, "POST", "/api/v1/imports", input, 409, "conflict")
	original := h.request(h.admin, "GET", "/api/v1/imports/"+firstRun.ID+"/evidence", nil, 200)
	if !bytes.Equal(original.Body.Bytes(), fixture(t, "sarif.json")) {
		t.Fatal("conflicting replay overwrote immutable evidence")
	}
	equal(t, "conflict preserved normalized severity", h.finding(h.admin, first.ID).Severity, first.Severity)
}

func TestM06_NewScanPreservesDecisionsAndExposesRiskExpiry(t *testing.T) {
	h := newHarness(t, true)
	input, firstRun, first := h.seed()
	assignee := h.addUser(h.admin, "analyst")
	expires := h.services.cfg.Now().Add(5 * time.Minute)
	h.json(h.admin, "PATCH", "/api/v1/findings/"+first.ID, object{"ownerId": assignee.user.ID, "workflowState": "in-progress",
		"disposition": "accepted-risk", "acceptedRiskExpiresAt": expires}, 200)
	h.json(h.admin, "POST", "/api/v1/findings/"+first.ID+"/notes", object{"text": "Synthetic decision retained across scans."}, 201)
	newTime := sourceTime.Add(24 * time.Hour)
	input["scanId"], input["sourceScanAt"], input["collectedAt"] = "scan-2", newTime, newTime.Add(time.Hour)
	input["report"] = string(sarif(t, func(_ object, result object) {
		location := result["locations"].([]any)[0].(map[string]any)["physicalLocation"].(map[string]any)
		location["region"] = object{"startLine": 19}
		result["message"] = object{"text": "Updated synthetic observation at the moved location."}
	}))
	secondRun := h.finish(h.upload(input).ID, "succeeded")
	if secondRun.RunID == firstRun.RunID {
		t.Fatal("a genuinely distinct scan reused the previous run")
	}
	equal(t, "moved stable finding has one canonical issue", len(h.work(h.admin, "")), 1)
	f := h.finding(h.admin, first.ID)
	equal(t, "two logical per-run observations", len(f.Observations), 2)
	byRun := map[string]observation{}
	for _, o := range f.Observations {
		byRun[o.RunID] = o
	}
	equal(t, "old source location retained", byRun[firstRun.RunID].SourceLocation.Line, 12)
	equal(t, "new source location retained", byRun[secondRun.RunID].SourceLocation.Line, 19)
	if byRun[firstRun.RunID].ID == byRun[secondRun.RunID].ID {
		t.Fatal("new scan must retain a distinct observation identity")
	}
	equal(t, "human workflow preserved", f.WorkflowState, "in-progress")
	equal(t, "human disposition preserved", f.Disposition, "accepted-risk")
	equal(t, "source observation state", f.SourceState, "observed")
	sameTime(t, "new scan freshness", f.SourceFreshnessAt, &newTime)
	sameTime(t, "accepted risk expiry retained", f.AcceptedRiskExpiresAt, &expires)
	if f.OwnerID == nil || *f.OwnerID != assignee.user.ID || len(f.Notes) != 1 || f.RiskAcceptanceExpired {
		t.Fatal("reconciliation lost assignment, note or current risk acceptance")
	}
	equal(t, "note text retained", f.Notes[0].Text, "Synthetic decision retained across scans.")
	h.clock.Add(int64(6 * time.Minute))
	expired := h.finding(h.admin, first.ID)
	if !expired.RiskAcceptanceExpired {
		t.Fatal("expired risk acceptance must be visibly expired using the controlled clock")
	}
	equal(t, "expiry does not silently rewrite a human decision", expired.Disposition, "accepted-risk")
	sameTime(t, "expired decision keeps its expiry", expired.AcceptedRiskExpiresAt, &expires)
}

func TestM06_IncomparableAbsenceCannotInferClosure(t *testing.T) {
	h := newHarness(t, true)
	_, _, first := h.seed()
	empty := sarif(t, func(run, _ object) { run["results"] = []any{} })
	for _, scenario := range []struct {
		name   string
		change func(object)
	}{
		{"failed", func(v object) { v["sourceStatus"], v["completeness"] = "failed", "unknown" }},
		{"partial", func(v object) { v["completeness"] = "partial" }},
		{"different-branch", func(v object) { v["scope"] = scope{"owned-repository", "1", "refs/heads/other"} }},
		{"different-source", func(v object) { v["sourceId"] = "other-source" }},
		{"different-scope-revision", func(v object) { v["scope"] = scope{"owned-repository", "2", "refs/heads/main"} }},
		{"stale", func(v object) { v["sourceScanAt"] = sourceTime.Add(-time.Hour) }},
		{"unknown-time", func(v object) { v["sourceScanAt"] = nil }},
		{"delta", func(v object) { v["scanKind"] = "delta" }},
	} {
		next := h.input(first.AssetID, "sarif", empty)
		next["scanId"], next["sourceScanAt"] = "absence-"+scenario.name, sourceTime.Add(24*time.Hour)
		next["collectedAt"] = sourceTime.Add(25 * time.Hour)
		scenario.change(next)
		run := h.finish(h.upload(next).ID, "succeeded")
		if scenario.name == "unknown-time" && run.SourceScanAt != nil {
			t.Fatal("unknown scan time was replaced with collection/import time")
		}
		f := h.finding(h.admin, first.ID)
		if f.SourceState != "observed" && f.SourceState != "unknown" && f.SourceState != "stale" {
			t.Fatalf("%s absence inferred resolution without comparable coverage", scenario.name)
		}
		equal(t, scenario.name+" preserves workflow", f.WorkflowState, "open")
		equal(t, scenario.name+" cannot verify resolution", f.VerifiedResolution, false)
		sameTime(t, scenario.name+" preserves comparable freshness", f.SourceFreshnessAt, &sourceTime)
		equal(t, scenario.name+" cannot invent observations", len(f.Observations), 1)
	}
	equal(t, "absence did not hide the issue", len(h.work(h.admin, "")), 1)
}

func TestM06_CompleteAbsenceIsScannerInferenceNotVerifiedResolution(t *testing.T) {
	h := newHarness(t, true)
	input, firstRun, first := h.seed()
	newTime := sourceTime.Add(24 * time.Hour)
	input["scanId"], input["sourceScanAt"], input["collectedAt"] = "scan-2", newTime, newTime.Add(time.Hour)
	input["report"] = string(sarif(t, func(run, _ object) { run["results"] = []any{} }))
	emptyRun := h.finish(h.upload(input).ID, "succeeded")
	equal(t, "empty scan has no detection observations", emptyRun.ObservationCount, 0)
	if emptyRun.RunID == firstRun.RunID {
		t.Fatal("complete empty scan must retain distinct run provenance")
	}
	f := h.finding(h.admin, first.ID)
	equal(t, "comparable absence infers source resolution", f.SourceState, "inferred-resolved")
	equal(t, "scanner inference is not independent verification", f.VerifiedResolution, false)
	equal(t, "scanner inference did not run verification", f.Evidence.VerificationState, "not-run")
	equal(t, "human workflow remains independent", f.WorkflowState, "open")
	equal(t, "no invented false-positive disposition", f.Disposition, "none")
	sameTime(t, "comparable complete scan advances freshness", f.SourceFreshnessAt, &newTime)
	equal(t, "resolved source retains original observation", len(f.Observations), 1)
	equal(t, "source resolution alone does not hide human work", len(h.work(h.admin, "")), 1)
	later := newTime.Add(24 * time.Hour)
	input["scanId"], input["sourceScanAt"], input["collectedAt"] = "scan-3", later, later.Add(time.Hour)
	input["report"] = string(fixture(t, "sarif.json"))
	h.finish(h.upload(input).ID, "succeeded")
	f = h.finding(h.admin, first.ID)
	equal(t, "reappearance reuses the canonical issue", len(h.work(h.admin, "")), 1)
	equal(t, "reappearance is observed", f.SourceState, "observed")
	equal(t, "reappearance adds a real observation", len(f.Observations), 2)
	sameTime(t, "reappearance freshness", f.SourceFreshnessAt, &later)
}
