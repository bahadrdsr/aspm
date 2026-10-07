//go:build integration

package acceptance

import (
	"context"
	"testing"
	"time"
)

func observationForRun(t *testing.T, finding finding, runID string) observation {
	t.Helper()
	for _, item := range finding.Observations {
		if item.RunID == runID {
			return item
		}
	}
	t.Fatalf("finding %s omitted observation for run %s", finding.ID, runID)
	return observation{}
}

func TestM06_ChangeLifecycleKeepsMeaningfulWorkSeparateFromScanChurn(t *testing.T) {
	h := newHarness(t, true)
	assignee := h.addUser(h.admin, "analyst")
	asset := h.asset(h.admin, "Lifecycle classification repository", nil)
	baseReport := fixture(t, "sarif.json")
	input := h.input(asset.ID, "sarif", baseReport)
	input["sourceId"], input["scanId"] = "lifecycle-source", "lifecycle-scan-1"
	firstReceipt := h.upload(input)
	ctx, cancel := context.WithTimeout(h.services.ctx, 20*time.Second)
	ok(t, "process first lifecycle import", h.app.ProcessImports(ctx))
	cancel()
	firstRun := h.json(h.admin, "GET", "/api/v1/imports/"+firstReceipt.ID, nil, 200).Import
	if firstRun.State != "succeeded" {
		t.Fatalf("first lifecycle import state=%s failure=%+v", firstRun.State, firstRun.Failure)
	}
	first := retentionFindingForRun(t, h, firstRun.RunID)
	equal(t, "first finding change", first.ChangeKind, "new")
	equal(t, "first finding change revision", first.ChangeRevision, int64(1))
	equal(t, "first observation change", observationForRun(t, first, firstRun.RunID).ChangeKind, "new")
	equal(t, "new finding is meaningful Work", h.json(h.admin, "GET",
		"/api/v1/work?change=meaningful", nil, 200).Total, 1)

	h.json(h.admin, "PATCH", "/api/v1/findings/"+first.ID, object{
		"ownerId": assignee.user.ID, "workflowState": "in-progress",
	}, 200)
	h.json(h.admin, "POST", "/api/v1/findings/"+first.ID+"/notes",
		object{"text": "Human lifecycle decision must survive scan churn."}, 201)

	scan := func(id string, observed time.Time, report []byte, changes object) imported {
		next := h.input(asset.ID, "sarif", report)
		next["sourceId"], next["scanId"] = "lifecycle-source", id
		next["sourceScanAt"], next["collectedAt"] = observed, observed.Add(time.Minute)
		for key, value := range changes {
			next[key] = value
		}
		return h.finish(h.upload(next).ID, "succeeded")
	}

	unchangedTime := sourceTime.Add(24 * time.Hour)
	unchangedRun := scan("lifecycle-scan-2", unchangedTime, baseReport, object{})
	current := h.finding(h.admin, first.ID)
	equal(t, "same content is unchanged", current.ChangeKind, "unchanged")
	equal(t, "unchanged advances lifecycle revision", current.ChangeRevision, int64(2))
	equal(t, "unchanged observation classification",
		observationForRun(t, current, unchangedRun.RunID).ChangeKind, "unchanged")
	equal(t, "unchanged scan creates no meaningful Work", h.json(h.admin, "GET",
		"/api/v1/work?change=meaningful", nil, 200).Total, 0)

	changedReport := sarif(t, func(_ object, result object) {
		result["message"] = object{"text": "Changed normalized lifecycle evidence."}
	})
	changedTime := unchangedTime.Add(24 * time.Hour)
	changedRun := scan("lifecycle-scan-3", changedTime, changedReport, object{})
	current = h.finding(h.admin, first.ID)
	equal(t, "changed content classification", current.ChangeKind, "changed")
	equal(t, "changed lifecycle revision", current.ChangeRevision, int64(3))
	equal(t, "changed observation classification",
		observationForRun(t, current, changedRun.RunID).ChangeKind, "changed")
	equal(t, "changed finding is meaningful Work", h.json(h.admin, "GET",
		"/api/v1/work?change=meaningful", nil, 200).Total, 1)

	resolvedTime := changedTime.Add(24 * time.Hour)
	emptyReport := sarif(t, func(run, _ object) { run["results"] = []any{} })
	scan("lifecycle-scan-4", resolvedTime, emptyReport, object{})
	current = h.finding(h.admin, first.ID)
	equal(t, "complete absence lifecycle", current.ChangeKind, "inferred-resolved")
	equal(t, "absence lifecycle revision", current.ChangeRevision, int64(4))
	equal(t, "inferred resolution is not fresh analyst Work", h.json(h.admin, "GET",
		"/api/v1/work?change=meaningful", nil, 200).Total, 0)
	equal(t, "absence keeps human workflow", current.WorkflowState, "in-progress")
	equal(t, "absence keeps human owner", *current.OwnerID, assignee.user.ID)

	reopenedTime := resolvedTime.Add(24 * time.Hour)
	reopenedRun := scan("lifecycle-scan-5", reopenedTime, changedReport, object{})
	current = h.finding(h.admin, first.ID)
	equal(t, "reappearance classification", current.ChangeKind, "reopened")
	equal(t, "reappearance lifecycle revision", current.ChangeRevision, int64(5))
	equal(t, "reopened observation classification",
		observationForRun(t, current, reopenedRun.RunID).ChangeKind, "reopened")
	equal(t, "reopened finding is meaningful Work", h.json(h.admin, "GET",
		"/api/v1/work?change=meaningful", nil, 200).Total, 1)

	historicalReport := sarif(t, func(_ object, result object) {
		result["message"] = object{"text": "Older changed evidence must stay historical."}
	})
	historicalRun := scan("lifecycle-scan-older", changedTime.Add(time.Hour), historicalReport, object{})
	current = h.finding(h.admin, first.ID)
	equal(t, "out-of-order scan does not replace current change", current.ChangeKind, "reopened")
	equal(t, "out-of-order scan does not advance lifecycle revision", current.ChangeRevision, int64(5))
	historical := observationForRun(t, current, historicalRun.RunID)
	equal(t, "out-of-order observation classification", historical.ChangeKind, "historical")
	equal(t, "out-of-order observation reason", historical.ChangeReasons, []string{"out-of-order"})

	partialTime := reopenedTime.Add(24 * time.Hour)
	partialRun := scan("lifecycle-scan-partial", partialTime, changedReport, object{"completeness": "partial"})
	current = h.finding(h.admin, first.ID)
	equal(t, "partial positive observation can be unchanged", current.ChangeKind, "unchanged")
	equal(t, "partial observation reason",
		observationForRun(t, current, partialRun.RunID).ChangeReasons, []string{"partial-scan"})
	equal(t, "partial unchanged scan is not meaningful Work", h.json(h.admin, "GET",
		"/api/v1/work?change=meaningful", nil, 200).Total, 0)

	deltaTime := partialTime.Add(24 * time.Hour)
	deltaRun := scan("lifecycle-scan-delta", deltaTime, changedReport, object{"scanKind": "delta"})
	current = h.finding(h.admin, first.ID)
	equal(t, "delta positive observation can be unchanged", current.ChangeKind, "unchanged")
	equal(t, "delta observation reason",
		observationForRun(t, current, deltaRun.RunID).ChangeReasons, []string{"delta-scan"})

	failedTime := deltaTime.Add(24 * time.Hour)
	failedRun := scan("lifecycle-scan-failed", failedTime, historicalReport,
		object{"sourceStatus": "failed", "completeness": "unknown"})
	current = h.finding(h.admin, first.ID)
	equal(t, "failed scan does not replace current lifecycle", current.ChangeKind, "unchanged")
	equal(t, "failed scan does not advance lifecycle revision", current.ChangeRevision, int64(7))
	failed := observationForRun(t, current, failedRun.RunID)
	equal(t, "failed scan observation classification", failed.ChangeKind, "non-authoritative")
	equal(t, "failed scan observation reasons", failed.ChangeReasons,
		[]string{"source-status-failed", "unknown-completeness"})

	branchTime := failedTime.Add(24 * time.Hour)
	branchRun := scan("lifecycle-scan-branch", branchTime, changedReport,
		object{"scope": scope{"owned-repository", "1", "refs/heads/feature"}})
	branchFinding := retentionFindingForRun(t, h, branchRun.RunID)
	if branchFinding.ID == first.ID {
		t.Fatal("changed scope reused the existing source-specific finding")
	}
	equal(t, "changed scope starts a new lifecycle", branchFinding.ChangeKind, "new")
	equal(t, "only changed-scope new finding is meaningful", h.json(h.admin, "GET",
		"/api/v1/work?change=meaningful", nil, 200).Total, 1)

	current = h.finding(h.admin, first.ID)
	equal(t, "scan churn preserves human owner", *current.OwnerID, assignee.user.ID)
	equal(t, "scan churn preserves human workflow", current.WorkflowState, "in-progress")
	equal(t, "scan churn preserves note", current.Notes[0].Text,
		"Human lifecycle decision must survive scan churn.")
	equal(t, "all positive observations remain in history", len(current.Observations), 8)
}
