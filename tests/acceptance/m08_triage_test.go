//go:build integration

package acceptance

import (
	"bytes"
	"fmt"
	"reflect"
	"testing"
	"time"
)

func TestM08_DeveloperHandoffIsScopedBoundedAndEvidenceReferenced(t *testing.T) {
	h := newHarness(t, true)
	_, _, first := h.seed()
	h.json(h.admin, "POST", "/api/v1/findings/"+first.ID+"/notes",
		object{"text": "Private analyst note must not enter the portable handoff."}, 201)
	viewer := h.addUser(h.admin, "viewer")
	response := h.request(viewer, "GET", "/api/v1/findings/"+first.ID+"/handoff", nil, 200)
	if contentType := response.Header().Get("Content-Type"); contentType != "text/plain; charset=utf-8" {
		t.Fatalf("handoff content type=%q", contentType)
	}
	body := response.Body.Bytes()
	for _, required := range [][]byte{
		[]byte("ASPM DEVELOPER HANDOFF"), []byte(first.ID), []byte(first.Title),
		[]byte(first.AssetName), []byte(first.ScopeLabel), []byte(first.WorkflowState),
		[]byte(first.SourceState), []byte(first.Observations[0].ID),
		[]byte(first.Observations[0].EvidenceDigest), []byte("UNTRUSTED SOURCE-PROVIDED TEXT"),
	} {
		if !bytes.Contains(body, required) {
			t.Fatalf("handoff omitted required bounded context %q", required)
		}
	}
	for _, forbidden := range [][]byte{
		[]byte(first.Evidence.Text), []byte("Private analyst note"), []byte("vendorNote"),
		[]byte(h.services.cfg.Storage.AccessKey), []byte(h.services.cfg.Storage.SecretKey),
	} {
		if len(forbidden) > 0 && bytes.Contains(body, forbidden) {
			t.Fatal("handoff exposed raw evidence, analyst notes, unmapped fields, or credentials")
		}
	}
	if response.Header().Get("X-ASPM-Handoff-Truncated") != "false" || len(body) > 128<<10 {
		t.Fatal("handoff must report bounded non-truncated output for the small fixture")
	}
	other := h.addWorkspace()
	h.denied(other, "GET", "/api/v1/findings/"+first.ID+"/handoff", nil, 404, "not-found")
	h.denied(h.admin, "POST", "/api/v1/findings/"+first.ID+"/handoff", object{}, 405, "method-not-allowed")
}

func TestM08_PendingRetestDecisionHistoryIsDurableAndScanIndependent(t *testing.T) {
	h := newHarness(t, true)
	input, _, first := h.seed()
	updated := h.json(h.admin, "PATCH", "/api/v1/findings/"+first.ID, object{
		"workflowState": "pending-retest",
		"rationale":     "Awaiting the next synthetic complete scan.",
	}, 200).Finding
	equal(t, "pending retest workflow", updated.WorkflowState, "pending-retest")
	equal(t, "decision revision advanced once", updated.DecisionRevision, int64(2))
	equal(t, "single decision event", len(updated.DecisionEvents), 1)
	event := updated.DecisionEvents[0]
	equal(t, "decision event revision", event.DecisionRevision, int64(2))
	equal(t, "decision event actor", event.ActorID, h.admin.user.ID)
	equal(t, "decision event actor name", event.ActorName, h.admin.user.Name)
	equal(t, "decision event action", event.Action, "update")
	equal(t, "decision event rationale", event.Rationale, "Awaiting the next synthetic complete scan.")
	if !reflect.DeepEqual(event.ChangedFields, []string{"workflowState"}) ||
		event.Before.WorkflowState != "open" || event.After.WorkflowState != "pending-retest" ||
		event.Before.OwnerID == nil || *event.Before.OwnerID != h.admin.user.ID ||
		event.After.OwnerID == nil || *event.After.OwnerID != h.admin.user.ID ||
		event.BeforeOwnerName == nil || *event.BeforeOwnerName != h.admin.user.Name ||
		event.AfterOwnerName == nil || *event.AfterOwnerName != h.admin.user.Name || event.CreatedAt.IsZero() {
		t.Fatalf("decision history lost the exact before/after workflow transition: %#v", event)
	}
	viewer := h.addUser(h.admin, "viewer")
	equal(t, "viewer can read decision history", len(h.finding(viewer, first.ID).DecisionEvents), 1)
	h.denied(viewer, "GET", "/api/v1/findings/"+first.ID+"?decisionsCursor=1", nil, 400, "invalid-input")

	newTime := sourceTime.Add(24 * time.Hour)
	input["scanId"], input["sourceScanAt"], input["collectedAt"] = "pending-retest-scan", newTime, newTime.Add(time.Hour)
	h.finish(h.upload(input).ID, "succeeded")
	afterScan := h.finding(h.admin, first.ID)
	equal(t, "scan did not rewrite pending retest", afterScan.WorkflowState, "pending-retest")
	equal(t, "scan did not invent a decision event", len(afterScan.DecisionEvents), 1)

	h.restart()
	reopened := h.finding(h.admin, first.ID)
	equal(t, "pending retest survived reopen", reopened.WorkflowState, "pending-retest")
	if !reflect.DeepEqual(reopened.DecisionEvents, afterScan.DecisionEvents) {
		t.Fatal("decision history did not survive application reopen")
	}
}

func TestM08_BulkTriageIsAtomicScopedAndAudited(t *testing.T) {
	h := newHarness(t, true)
	_, _, first := h.seed()
	secondReport := sarif(t, func(_ object, result object) {
		result["guid"] = "22222222-2222-4222-8222-222222222222"
		result["message"] = object{"text": "Second synthetic finding for bulk triage."}
	})
	secondInput := h.input(first.AssetID, "sarif", secondReport)
	secondInput["sourceId"], secondInput["scanId"] = "acceptance-source-2", "bulk-scan-2"
	secondInput["sourceScanAt"], secondInput["collectedAt"] = sourceTime.Add(time.Hour), sourceTime.Add(2*time.Hour)
	h.finish(h.upload(secondInput).ID, "succeeded")
	work := h.work(h.admin, "")
	equal(t, "two findings for bulk triage", len(work), 2)
	secondID := work[0].ID
	if secondID == first.ID {
		secondID = work[1].ID
	}
	analyst, viewer := h.addUser(h.admin, "analyst"), h.addUser(h.admin, "viewer")
	body := object{
		"findingIds":    []string{first.ID, secondID},
		"ownerId":       analyst.user.ID,
		"workflowState": "in-progress",
		"rationale":     "Assign the selected synthetic findings for coordinated review.",
	}
	h.denied(viewer, "PATCH", "/api/v1/findings", body, 403, "forbidden")
	h.denied(analyst, "PATCH", "/api/v1/findings", object{
		"findingIds": []string{first.ID, first.ID}, "workflowState": "resolved",
		"rationale": "Duplicate selection must fail.",
	}, 400, "invalid-input")
	tooMany := make([]string, 101)
	for index := range tooMany {
		tooMany[index] = fmt.Sprintf("%032x", index+1)
	}
	h.denied(analyst, "PATCH", "/api/v1/findings", object{
		"findingIds": tooMany, "workflowState": "resolved", "rationale": "Too many findings.",
	}, 400, "invalid-input")

	other := h.addWorkspace()
	otherAsset := h.asset(other, "Foreign bulk repository", nil)
	foreignInput := h.input(otherAsset.ID, "sarif", fixture(t, "sarif.json"))
	foreignInput["sourceId"], foreignInput["scanId"] = "foreign-source", "foreign-scan"
	h.finishAs(other, h.uploadAs(other, foreignInput).ID, "succeeded")
	foreign := h.work(other, "")[0]
	h.denied(analyst, "PATCH", "/api/v1/findings", object{
		"findingIds": []string{first.ID, foreign.ID}, "workflowState": "resolved",
		"rationale": "Cross-workspace selection must fail atomically.",
	}, 404, "not-found")
	equal(t, "failed bulk action preserved first workflow", h.finding(h.admin, first.ID).WorkflowState, "open")
	equal(t, "failed bulk action preserved second workflow", h.finding(h.admin, secondID).WorkflowState, "open")

	response := h.json(analyst, "PATCH", "/api/v1/findings", body, 200)
	equal(t, "bulk response count", response.Total, 2)
	equal(t, "bulk response has no cursor", response.NextCursor, (*string)(nil))
	items := items[workItem](t, response)
	equal(t, "bulk response item count", len(items), 2)
	for _, item := range items {
		equal(t, "bulk workflow", item.WorkflowState, "in-progress")
		if item.OwnerName == nil || *item.OwnerName != analyst.user.Name {
			t.Fatal("bulk assignment did not return the selected owner")
		}
		finding := h.finding(h.admin, item.ID)
		if finding.OwnerID == nil || *finding.OwnerID != analyst.user.ID ||
			finding.WorkflowState != "in-progress" || finding.DecisionRevision != 2 ||
			len(finding.DecisionEvents) != 1 {
			t.Fatal("bulk triage did not atomically update and audit the selected finding")
		}
		event := finding.DecisionEvents[0]
		if event.ActorID != analyst.user.ID || event.Action != "bulk-update" ||
			event.Rationale != "Assign the selected synthetic findings for coordinated review." ||
			!reflect.DeepEqual(event.ChangedFields, []string{"ownerId", "workflowState"}) ||
			event.Before.OwnerID == nil || *event.Before.OwnerID != h.admin.user.ID || event.After.OwnerID == nil ||
			*event.After.OwnerID != analyst.user.ID ||
			event.BeforeOwnerName == nil || *event.BeforeOwnerName != h.admin.user.Name ||
			event.AfterOwnerName == nil || *event.AfterOwnerName != analyst.user.Name {
			t.Fatalf("bulk decision event lost actor, rationale, changed fields or before/after state: %#v", event)
		}
	}
}
