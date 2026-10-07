//go:build integration

package acceptance

import (
	"bytes"
	"testing"
	"time"
)

func correlationDecision(owner *string, workflow, disposition string, expires *time.Time) object {
	return object{
		"ownerId":               owner,
		"workflowState":         workflow,
		"disposition":           disposition,
		"acceptedRiskExpiresAt": expires,
	}
}

func TestM06_ReversibleMergeAndSplitPreserveVariantsDecisionsAndEvidence(t *testing.T) {
	h := newHarness(t, true)
	analyst, viewer := h.addUser(h.admin, "analyst"), h.addUser(h.admin, "viewer")
	asset := h.asset(h.admin, "Cross-source merge target", nil)

	firstInput := h.input(asset.ID, "sarif", fixture(t, "sarif.json"))
	firstInput["sourceId"], firstInput["scanId"] = "merge-source-a", "merge-scan-a"
	firstRun := h.finish(h.upload(firstInput).ID, "succeeded")
	firstWork := h.work(h.admin, "")
	equal(t, "first source issue count", len(firstWork), 1)
	firstID := firstWork[0].ID

	secondReport := sarif(t, func(_ object, result object) {
		result["guid"] = "22222222-2222-4222-8222-222222222222"
		result["level"] = "error"
		result["message"] = object{"text": "Second scanner description for the same reviewed issue."}
	})
	secondInput := h.input(asset.ID, "sarif", secondReport)
	secondInput["sourceId"], secondInput["scanId"] = "merge-source-b", "merge-scan-b"
	secondInput["sourceScanAt"], secondInput["collectedAt"] = sourceTime.Add(time.Hour), sourceTime.Add(2*time.Hour)
	secondRun := h.finish(h.upload(secondInput).ID, "succeeded")
	work := h.work(h.admin, "")
	equal(t, "two source-specific issues before review", len(work), 2)
	secondID := work[0].ID
	if secondID == firstID {
		secondID = work[1].ID
	}

	firstExpiry := h.services.cfg.Now().Add(24 * time.Hour)
	h.json(h.admin, "PATCH", "/api/v1/findings/"+firstID, object{
		"ownerId": h.admin.user.ID, "workflowState": "open",
		"disposition": "accepted-risk", "acceptedRiskExpiresAt": firstExpiry,
	}, 200)
	h.json(h.admin, "POST", "/api/v1/findings/"+firstID+"/notes",
		object{"text": "Primary source decision history."}, 201)
	h.json(h.admin, "PATCH", "/api/v1/findings/"+secondID, object{
		"ownerId": analyst.user.ID, "workflowState": "in-progress", "disposition": "none",
	}, 200)
	h.json(h.admin, "POST", "/api/v1/findings/"+secondID+"/notes",
		object{"text": "Secondary source decision history."}, 201)

	firstEvidence := h.request(h.admin, "GET", "/api/v1/imports/"+firstRun.ID+"/evidence", nil, 200).Body.Bytes()
	secondEvidence := h.request(h.admin, "GET", "/api/v1/imports/"+secondRun.ID+"/evidence", nil, 200).Body.Bytes()

	preview := h.json(viewer, "POST", "/api/v1/findings/"+firstID+"/merge-previews",
		object{"otherFindingId": secondID}, 200).MergePreview
	equal(t, "preview primary finding", preview.Primary.FindingID, firstID)
	equal(t, "preview secondary finding", preview.Other.FindingID, secondID)
	equal(t, "preview observations", preview.Primary.ObservationCount+preview.Other.ObservationCount, 2)
	equal(t, "preview notes", preview.Primary.NoteCount+preview.Other.NoteCount, 2)
	if len(preview.Conflicts) < 3 {
		t.Fatal("merge preview did not expose owner, workflow and disposition conflicts")
	}
	equal(t, "preview is read-only", len(h.work(h.admin, "")), 2)

	mergeInput := object{
		"otherFindingId":          secondID,
		"primaryDecisionRevision": preview.Primary.DecisionRevision,
		"primaryEvidenceRevision": preview.Primary.EvidenceRevision,
		"otherDecisionRevision":   preview.Other.DecisionRevision,
		"otherEvidenceRevision":   preview.Other.EvidenceRevision,
		"decision":                correlationDecision(&analyst.user.ID, "in-progress", "accepted-risk", &firstExpiry),
		"rationale":               "Two independently retained source variants describe the same reviewed issue.",
		"idempotencyKey":          "merge-reviewed-cross-source",
	}
	h.denied(viewer, "POST", "/api/v1/findings/"+firstID+"/merges", mergeInput, 403, "forbidden")
	foreign := h.addWorkspace()
	h.denied(foreign, "POST", "/api/v1/findings/"+firstID+"/merges", mergeInput, 404, "not-found")

	h.json(h.admin, "POST", "/api/v1/findings/"+secondID+"/notes",
		object{"text": "Concurrent note invalidates the earlier preview."}, 201)
	h.denied(h.admin, "POST", "/api/v1/findings/"+firstID+"/merges", mergeInput, 409, "conflict")

	preview = h.json(h.admin, "POST", "/api/v1/findings/"+firstID+"/merge-previews",
		object{"otherFindingId": secondID}, 200).MergePreview
	mergeInput["primaryDecisionRevision"] = preview.Primary.DecisionRevision
	mergeInput["primaryEvidenceRevision"] = preview.Primary.EvidenceRevision
	mergeInput["otherDecisionRevision"] = preview.Other.DecisionRevision
	mergeInput["otherEvidenceRevision"] = preview.Other.EvidenceRevision
	created := h.json(h.admin, "POST", "/api/v1/findings/"+firstID+"/merges", mergeInput, 201).Correlation
	equal(t, "active correlation state", created.State, "active")
	equal(t, "merge member count", len(created.Members), 2)
	equal(t, "merge event count", len(created.Events), 1)
	equal(t, "merge event type", created.Events[0].Type, "merge")
	equal(t, "one user-facing issue after merge", len(h.work(h.admin, "")), 1)

	replayed := h.json(h.admin, "POST", "/api/v1/findings/"+firstID+"/merges", mergeInput, 200).Correlation
	equal(t, "merge replay correlation identity", replayed.ID, created.ID)
	changedReplay := object{}
	for key, value := range mergeInput {
		changedReplay[key] = value
	}
	changedReplay["rationale"] = "Changed replay rationale."
	h.denied(h.admin, "POST", "/api/v1/findings/"+firstID+"/merges", changedReplay, 409, "conflict")

	merged := h.finding(h.admin, firstID)
	if merged.Correlation == nil || merged.Correlation.ID != created.ID {
		t.Fatal("primary finding omitted active correlation metadata")
	}
	equal(t, "merged observations retained", len(merged.Observations), 2)
	equal(t, "merged notes retained", len(merged.Notes), 3)
	equal(t, "chosen merged owner", *merged.OwnerID, analyst.user.ID)
	equal(t, "chosen merged workflow", merged.WorkflowState, "in-progress")
	equal(t, "chosen merged disposition", merged.Disposition, "accepted-risk")

	h.json(h.admin, "POST", "/api/v1/findings/"+firstID+"/notes",
		object{"text": "Post-merge note remains on the primary issue."}, 201)
	h.json(h.admin, "PATCH", "/api/v1/findings/"+firstID,
		object{"workflowState": "resolved", "disposition": "accepted-risk", "acceptedRiskExpiresAt": firstExpiry}, 200)

	split := h.json(viewer, "POST", "/api/v1/findings/"+firstID+"/split-previews",
		object{"memberFindingId": secondID}, 200).SplitPreview
	equal(t, "split preview correlation", split.Correlation.ID, created.ID)
	equal(t, "split preview member", split.Member.FindingID, secondID)

	splitInput := object{
		"memberFindingId":         secondID,
		"correlationRevision":     split.Correlation.Revision,
		"primaryDecisionRevision": split.Primary.DecisionRevision,
		"primaryEvidenceRevision": split.Primary.EvidenceRevision,
		"memberDecisionRevision":  split.Member.DecisionRevision,
		"memberEvidenceRevision":  split.Member.EvidenceRevision,
		"primaryDecision":         correlationDecision(&h.admin.user.ID, "open", "accepted-risk", &firstExpiry),
		"memberDecision":          correlationDecision(&analyst.user.ID, "in-progress", "none", nil),
		"rationale":               "Separate the retained variants after reviewing their later decision applicability.",
		"idempotencyKey":          "split-reviewed-cross-source",
	}
	h.json(h.admin, "POST", "/api/v1/findings/"+firstID+"/notes",
		object{"text": "Concurrent primary note invalidates the first split preview."}, 201)
	h.denied(h.admin, "POST", "/api/v1/findings/"+firstID+"/splits", splitInput, 409, "conflict")

	split = h.json(h.admin, "POST", "/api/v1/findings/"+firstID+"/split-previews",
		object{"memberFindingId": secondID}, 200).SplitPreview
	splitInput["correlationRevision"] = split.Correlation.Revision
	splitInput["primaryDecisionRevision"] = split.Primary.DecisionRevision
	splitInput["primaryEvidenceRevision"] = split.Primary.EvidenceRevision
	splitInput["memberDecisionRevision"] = split.Member.DecisionRevision
	splitInput["memberEvidenceRevision"] = split.Member.EvidenceRevision
	separated := h.json(h.admin, "POST", "/api/v1/findings/"+firstID+"/splits", splitInput, 201).Correlation
	equal(t, "split correlation state", separated.State, "split")
	equal(t, "split event count", len(separated.Events), 2)
	equal(t, "split event type", separated.Events[1].Type, "split")
	equal(t, "two issues visible after split", len(h.work(h.admin, "")), 2)
	equal(t, "split replay correlation identity",
		h.json(h.admin, "POST", "/api/v1/findings/"+firstID+"/splits", splitInput, 200).Correlation.ID, separated.ID)

	primary := h.finding(h.admin, firstID)
	member := h.finding(h.admin, secondID)
	equal(t, "primary observations after split", len(primary.Observations), 1)
	equal(t, "member observations after split", len(member.Observations), 1)
	equal(t, "primary retained all own and later notes", len(primary.Notes), 3)
	equal(t, "member retained only its own notes", len(member.Notes), 2)
	equal(t, "primary restored explicit owner", *primary.OwnerID, h.admin.user.ID)
	equal(t, "member restored explicit owner", *member.OwnerID, analyst.user.ID)
	equal(t, "member restored workflow", member.WorkflowState, "in-progress")
	equal(t, "member restored disposition", member.Disposition, "none")

	if !bytes.Equal(h.request(h.admin, "GET", "/api/v1/imports/"+firstRun.ID+"/evidence", nil, 200).Body.Bytes(), firstEvidence) ||
		!bytes.Equal(h.request(h.admin, "GET", "/api/v1/imports/"+secondRun.ID+"/evidence", nil, 200).Body.Bytes(), secondEvidence) {
		t.Fatal("merge or split changed immutable source evidence")
	}
	h.restart()
	equal(t, "split survives reopen", len(h.work(h.admin, "")), 2)
	equal(t, "primary history survives reopen", len(h.finding(h.admin, firstID).Notes), 3)
	equal(t, "member history survives reopen", len(h.finding(h.admin, secondID).Notes), 2)
}
