//go:build integration

package acceptance

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"
)

type securitySnapshot struct {
	finding  finding
	evidence []byte
	importID string
}

func securityRemember(h *harness, f finding, run imported) securitySnapshot {
	h.t.Helper()
	return securitySnapshot{finding: f, importID: run.ID,
		evidence: bytes.Clone(h.request(h.admin, "GET", "/api/v1/imports/"+run.ID+"/evidence", nil, 200).Body.Bytes())}
}

func securityUnchanged(h *harness, before securitySnapshot) {
	h.t.Helper()
	if !reflect.DeepEqual(h.finding(h.admin, before.finding.ID), before.finding) {
		h.t.Error("denied/revoked operation changed prior finding ownership, workflow, notes or provenance")
	}
	after := h.request(h.admin, "GET", "/api/v1/imports/"+before.importID+"/evidence", nil, 200)
	if !bytes.Equal(after.Body.Bytes(), before.evidence) {
		h.t.Error("denied/revoked operation changed previously authorized evidence")
	}
}

func securityUpload(h *harness, a actor, input object) imported {
	h.t.Helper()
	r := h.json(a, "POST", "/api/v1/imports", input, 202).Import
	if r.ID == "" || r.RunID == "" || r.State != "queued" {
		h.t.Fatal("new authenticated import did not identify durable queued work")
	}
	return r
}

func TestSecurityM04_DeniedWritesCannotChangeExistingFindingOrEvidence(t *testing.T) {
	h := newHarness(t, true)
	input, run, original := h.seed()
	viewer := h.addUser(h.admin, "viewer")
	foreign := h.addUser(h.addWorkspace(), "admin")
	h.json(h.admin, "PATCH", "/api/v1/findings/"+original.ID,
		object{"ownerId": h.admin.user.ID, "workflowState": "in-progress"}, 200)
	h.json(h.admin, "POST", "/api/v1/findings/"+original.ID+"/notes", object{"text": "Existing independent decision."}, 201)
	before := securityRemember(h, h.finding(h.admin, original.ID), run)
	for _, a := range []actor{viewer, foreign} {
		status, code := 403, "forbidden"
		if a.workspace != h.admin.workspace {
			status, code = 404, "not-found"
		}
		h.denied(a, "PATCH", "/api/v1/findings/"+original.ID,
			object{"ownerId": a.user.ID, "workflowState": "resolved"}, status, code)
		h.denied(a, "POST", "/api/v1/findings/"+original.ID+"/notes", object{"text": "Denied change."}, status, code)
		h.denied(a, "POST", "/api/v1/imports", input, status, code)
	}
	h.denied(foreign, "GET", "/api/v1/findings/"+original.ID, nil, 404, "not-found")
	h.denied(foreign, "GET", "/api/v1/imports/"+run.ID+"/evidence", nil, 404, "not-found")
	h.request(viewer, "GET", "/api/v1/imports/"+run.ID+"/evidence", nil, 200)
	h.finish(run.ID, "succeeded")
	securityUnchanged(h, before)
	equal(t, "denied work did not create another finding", len(h.work(h.admin, "")), 1)
}

func TestSecurityM05_RawReportCannotChooseRoutingOwnershipOrWorkflow(t *testing.T) {
	h := newHarness(t, true)
	input, priorRun, prior := h.seed()
	analyst := h.addUser(h.admin, "analyst")
	other := h.addUser(h.addWorkspace(), "admin")
	foreignAsset := h.asset(other, "Foreign source cannot choose this asset", &other.user.ID)
	expires := h.services.cfg.Now().Add(time.Hour)
	h.json(h.admin, "PATCH", "/api/v1/findings/"+prior.ID, object{
		"ownerId": h.admin.user.ID, "workflowState": "in-progress",
		"disposition": "accepted-risk", "acceptedRiskExpiresAt": expires,
	}, 200)
	h.json(h.admin, "POST", "/api/v1/findings/"+prior.ID+"/notes", object{"text": "Human-owned decision."}, 201)
	before := h.finding(h.admin, prior.ID)
	authority := object{
		"workspaceId": other.workspace, "assetId": foreignAsset.ID, "ownerId": other.user.ID,
		"sourceId": "raw-report-must-not-route", "scanId": "raw-report-must-not-bind",
		"scope": scope{"raw-scope", "999", "refs/heads/raw"}, "workflowState": "resolved",
		"disposition": "none", "verifiedResolution": true, "verificationState": "verified",
		"requesterId": h.admin.user.ID, "role": "admin",
	}
	raw := sarif(t, func(run, result object) {
		run["properties"] = authority
		properties := result["properties"].(map[string]any)
		for key, value := range authority {
			properties[key] = value
			result[key] = value
		}
	})
	input["report"], input["scanId"] = string(raw), "trusted-wrapper-scan-2"
	input["sourceScanAt"] = sourceTime.Add(24 * time.Hour)
	done := h.finish(securityUpload(h, analyst, input).ID, "succeeded")
	equal(t, "authenticated wrapper source", done.SourceID, "acceptance-source")
	equal(t, "authenticated wrapper scan", done.ScanID, "trusted-wrapper-scan-2")
	equal(t, "authenticated wrapper scope", done.Scope, scope{"owned-repository", "1", "refs/heads/main"})
	after := h.finding(h.admin, prior.ID)
	equal(t, "canonical identity retained", len(h.work(h.admin, "")), 1)
	equal(t, "foreign workspace stayed empty", len(h.work(other, "")), 0)
	equal(t, "asset binding not taken from report", after.AssetID, before.AssetID)
	equal(t, "workspace binding not taken from report", after.WorkspaceID, before.WorkspaceID)
	equal(t, "human owner unchanged", after.OwnerID, before.OwnerID)
	equal(t, "human workflow unchanged", after.WorkflowState, before.WorkflowState)
	equal(t, "human disposition unchanged", after.Disposition, before.Disposition)
	equal(t, "human notes unchanged", after.Notes, before.Notes)
	sameTime(t, "human expiry unchanged", after.AcceptedRiskExpiresAt, before.AcceptedRiskExpiresAt)
	equal(t, "raw report cannot grant verification", after.Evidence.VerificationState, "not-run")
	equal(t, "raw report cannot verify resolution", after.VerifiedResolution, false)
	equal(t, "one new observation, not a replacement", len(after.Observations), len(before.Observations)+1)
	for _, observation := range before.Observations {
		found := false
		for _, current := range after.Observations {
			found = found || reflect.DeepEqual(current, observation)
		}
		if !found {
			t.Fatal("raw authority fields rewrote prior observation provenance")
		}
	}
	if !bytes.Equal(h.request(h.admin, "GET", "/api/v1/imports/"+done.ID+"/evidence", nil, 200).Body.Bytes(), raw) {
		t.Fatal("untrusted original report was not retained exactly as evidence")
	}
	if !bytes.Equal(h.request(h.admin, "GET", "/api/v1/imports/"+priorRun.ID+"/evidence", nil, 200).Body.Bytes(), fixture(t, "sarif.json")) {
		t.Fatal("later raw report overwrote prior immutable evidence")
	}
}

func TestSecurityM06_ExistingSourceScanCannotBeReboundToAnotherAssetOrScope(t *testing.T) {
	h := newHarness(t, true)
	input, run, f := h.seed()
	before := securityRemember(h, f, run)
	second := h.asset(h.admin, "Second legitimate owned asset", nil)
	for _, changed := range []object{
		{"assetId": second.ID},
		{"scope": scope{"another-scope", "1", "refs/heads/main"}},
		{"scope": scope{"owned-repository", "1", "refs/heads/other"}},
	} {
		var candidate object
		ok(t, "copy request fixture", json.Unmarshal(encode(t, input), &candidate))
		for key, value := range changed {
			candidate[key] = value
		}
		rr := h.request(h.admin, "POST", "/api/v1/imports", encode(t, candidate), 0)
		if rr.Code != 409 {
			t.Errorf("existing (workspace, source, scan) identity accepted a changed asset/scope: status %d", rr.Code)
		} else {
			equal(t, "immutable source-scan conflict", h.decode(rr).Error.Code, "conflict")
		}
	}
	h.finish(run.ID, "succeeded")
	securityUnchanged(h, before)
	if len(h.work(h.admin, "")) != 1 {
		t.Error("rebinding attempts created new work from an existing source scan identity")
	}
	input["scanId"], input["scope"] = "legitimate-new-branch-scan", scope{"owned-repository", "1", "refs/heads/other"}
	newRun := h.finish(securityUpload(h, h.admin, input).ID, "succeeded")
	if newRun.RunID == run.RunID {
		t.Fatal("a legitimate new scan/branch did not retain distinct run identity")
	}
	equal(t, "legitimate new branch retained", newRun.Scope.Branch, "refs/heads/other")
}

func TestSecurityM06_HistoricalIntakeIsValidButReplayExpiresFromServerAcceptance(t *testing.T) {
	h := newHarness(t, true)
	a := h.asset(h.admin, "Historical source", nil)
	input := h.input(a.ID, "sarif", fixture(t, "sarif.json"))
	historical := time.Date(2001, 2, 3, 4, 5, 6, 0, time.UTC)
	input["sourceScanAt"], input["collectedAt"] = historical, historical.Add(time.Hour)
	accepted := h.services.cfg.Now()
	first := h.finish(securityUpload(h, h.admin, input).ID, "succeeded")
	sameTime(t, "historical source timestamp remains valid", first.SourceScanAt, &historical)
	sameTime(t, "server acceptance is not source time", &first.ImportedAt, &accepted)
	work := h.work(h.admin, "")
	equal(t, "historical intake creates real work", len(work), 1)
	before := securityRemember(h, h.finding(h.admin, work[0].ID), first)
	h.clock.Store(accepted.Add(23 * time.Hour).UnixNano())
	h.admin = h.login(h.admin.user.Email, h.password, h.admin.workspace)
	replay := h.finish(h.upload(input).ID, "succeeded")
	equal(t, "within-window replay keeps its original run", replay.RunID, first.RunID)
	securityUnchanged(h, before)
	h.clock.Store(accepted.Add(24*time.Hour + time.Second).UnixNano())
	h.admin = h.login(h.admin.user.Email, h.password, h.admin.workspace)
	rr := h.request(h.admin, "POST", "/api/v1/imports", encode(t, input), 0)
	if rr.Code != 409 {
		t.Errorf("same scan replay remained accepted after its server-acceptance window: status %d", rr.Code)
	} else {
		equal(t, "bounded replay rejection", h.decode(rr).Error.Code, "replay-expired")
	}
	securityUnchanged(h, before)
	input["scanId"] = "another-genuinely-distinct-historical-scan"
	distinct := h.finish(securityUpload(h, h.admin, input).ID, "succeeded")
	if distinct.RunID == first.RunID {
		t.Fatal("new historical scans must not be rejected or aliased merely because their source time is old")
	}
}

func TestSecurityM06_LatePartialOrDeltaPositiveHonorsStoredCompleteCoverage(t *testing.T) {
	for _, kind := range []string{"partial", "delta"} {
		t.Run(kind, func(t *testing.T) {
			h := newHarness(t, true)
			newer := sourceTime.Add(24 * time.Hour)
			for _, emptyFirst := range []bool{false, true} {
				a := h.asset(h.admin, fmt.Sprintf("%s empty-first=%t", kind, emptyFirst), nil)
				positive := h.input(a.ID, "sarif", fixture(t, "sarif.json"))
				positive["sourceId"] = a.ID
				if kind == "partial" {
					positive["completeness"] = "partial"
				} else {
					positive["scanKind"] = "delta"
				}
				empty := h.input(a.ID, "sarif", sarif(t, func(run, _ object) { run["results"] = []any{} }))
				empty["sourceId"], empty["scanId"], empty["sourceScanAt"] = a.ID, "newer-complete-absence", newer
				order := []object{positive, empty}
				if emptyFirst {
					order = []object{empty, positive}
				}
				for _, input := range order {
					h.finish(securityUpload(h, h.admin, input).ID, "succeeded")
				}
				items := h.work(h.admin, a.Name)
				equal(t, "one canonical finding for this scope", len(items), 1)
				f := h.finding(h.admin, items[0].ID)
				if f.SourceState != "inferred-resolved" || f.SourceFreshnessAt == nil || !f.SourceFreshnessAt.Equal(newer) {
					t.Errorf("%s empty-first=%t: final source state/freshness depends on processing order", kind, emptyFirst)
				}
				equal(t, "human workflow remains open", f.WorkflowState, "open")
				equal(t, "absence is not independent verification", f.VerifiedResolution, false)
				equal(t, "positive observation remains available", len(f.Observations), 1)
				sameTime(t, "original positive source time preserved", f.Observations[0].SourceScanAt, &sourceTime)
			}
		})
	}
}

func TestSecurityM07_ZAPObservationGrowthIsLinearAndPreservesInstanceFields(t *testing.T) {
	h := newHarness(t, true)
	previousSize := 0
	for _, count := range []int{8, 16, 32} {
		var report object
		ok(t, "decode owned ZAP fixture", json.Unmarshal(fixture(t, "zap.json"), &report))
		alert := report["site"].([]any)[0].(map[string]any)["alerts"].([]any)[0].(map[string]any)
		instances := make([]any, 0, count)
		for i := 0; i < count; i++ {
			instances = append(instances, object{"uri": fmt.Sprintf("https://fixture.example.invalid/growth-%d/item-%02d", count, i),
				"method": "GET", "evidence": fmt.Sprintf("instance-evidence-%02d", i), "otherinfo": fmt.Sprintf("instance-context-%02d", i)})
		}
		alert["instances"], alert["count"] = instances, fmt.Sprint(count)
		raw := encode(t, report)
		a := h.asset(h.admin, fmt.Sprintf("ZAP growth %d fixture", count), nil)
		input := h.input(a.ID, "zap", raw)
		input["sourceId"] = a.ID
		run := h.finish(securityUpload(h, h.admin, input).ID, "succeeded")
		var observations []observation
		for _, item := range h.work(h.admin, a.Name) {
			for _, o := range h.finding(h.admin, item.ID).Observations {
				if o.RunID == run.RunID {
					observations = append(observations, o)
				}
			}
		}
		equal(t, "one observation per ZAP instance", len(observations), count)
		for _, o := range observations {
			equal(t, "shared confidence preserved", o.Unmapped["confidence"], any("2"))
			equal(t, "shared context preserved", o.Unmapped["otherinfo"], any("Keep the ZAP source note."))
			index := strings.TrimPrefix(o.SourceLocation.URI, fmt.Sprintf("https://fixture.example.invalid/growth-%d/item-", count))
			data := string(encode(t, o))
			if !strings.Contains(data, "instance-evidence-"+index) || !strings.Contains(data, "instance-context-"+index) {
				t.Error("ZAP normalization lost per-instance evidence or context")
			}
		}
		size := len(encode(t, observations))
		if previousSize != 0 && size*2 > previousSize*5 {
			t.Errorf("doubling ZAP instances grew serialized observations beyond 2.5x: %d -> %d bytes", previousSize, size)
		}
		previousSize = size
		if !bytes.Equal(h.request(h.admin, "GET", "/api/v1/imports/"+run.ID+"/evidence", nil, 200).Body.Bytes(), raw) {
			t.Fatal("growth normalization changed exact original source evidence")
		}
	}
}
