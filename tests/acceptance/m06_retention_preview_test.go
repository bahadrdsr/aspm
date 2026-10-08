//go:build integration

package acceptance

import (
	"bytes"
	"encoding/json"
	"slices"
	"testing"
	"time"
)

func retentionFindingForRun(t *testing.T, h *harness, runID string) finding {
	t.Helper()
	for _, item := range h.work(h.admin, "") {
		current := h.finding(h.admin, item.ID)
		for _, observation := range current.Observations {
			if observation.RunID == runID {
				return current
			}
		}
	}
	t.Fatalf("no finding retained run %s", runID)
	return finding{}
}

func retentionSummary(t *testing.T, preview retentionPreview, class string) retentionClassSummary {
	t.Helper()
	for _, summary := range preview.Summaries {
		if summary.Class == class {
			return summary
		}
	}
	t.Fatalf("retention preview omitted %s summary", class)
	return retentionClassSummary{}
}

func retentionItem(t *testing.T, preview retentionPreview, class, resourceID string) retentionPreviewItem {
	t.Helper()
	for _, item := range preview.Items {
		if item.Class == class && item.ResourceID == resourceID {
			return item
		}
	}
	t.Fatalf("retention preview omitted %s resource %s", class, resourceID)
	return retentionPreviewItem{}
}

func TestM06_RetentionPreviewSeparatesPoliciesHoldsAndStaleApproval(t *testing.T) {
	h := newHarness(t, true)
	viewer := h.addUser(h.admin, "viewer")
	foreign := h.addWorkspace()

	policy := h.json(h.admin, "GET", "/api/v1/retention/policy", nil, 200).RetentionPolicy
	equal(t, "default retention policy", []int{
		policy.HotHistoryDays, policy.RawReportDays, policy.ArchivedEvidenceDays, policy.AuditDays,
	}, []int{90, 180, 365, 730})
	equal(t, "initial policy revision", policy.Revision, int64(1))
	foreignPolicy := h.json(foreign, "GET", "/api/v1/retention/policy", nil, 200).RetentionPolicy
	equal(t, "new workspace retention defaults", []int{
		foreignPolicy.HotHistoryDays, foreignPolicy.RawReportDays,
		foreignPolicy.ArchivedEvidenceDays, foreignPolicy.AuditDays,
	}, []int{90, 180, 365, 730})
	h.denied(viewer, "PATCH", "/api/v1/retention/policy", object{
		"revision": 1, "hotHistoryDays": 30, "rawReportDays": 60,
		"archivedEvidenceDays": 120, "auditDays": 240,
	}, 403, "forbidden")
	h.denied(h.admin, "PATCH", "/api/v1/retention/policy", object{
		"revision": 1, "hotHistoryDays": 60, "rawReportDays": 30,
		"archivedEvidenceDays": 120, "auditDays": 240,
	}, 400, "invalid-input")
	policy = h.json(h.admin, "PATCH", "/api/v1/retention/policy", object{
		"revision": 1, "hotHistoryDays": 30, "rawReportDays": 60,
		"archivedEvidenceDays": 120, "auditDays": 240,
	}, 200).RetentionPolicy
	equal(t, "updated policy revision", policy.Revision, int64(2))

	asset := h.asset(h.admin, "Retention preview repository", nil)
	upload := func(source, scan string, report []byte) imported {
		input := h.input(asset.ID, "sarif", report)
		input["sourceId"], input["scanId"] = source, scan
		return h.finish(h.upload(input).ID, "succeeded")
	}
	eligible := upload("retention-eligible", "retention-eligible-scan", fixture(t, "sarif.json"))
	held := upload("retention-held", "retention-held-scan", fixture(t, "sarif.json"))
	decision := upload("retention-decision", "retention-decision-scan", fixture(t, "sarif.json"))
	sharedReport := sarif(t, func(run, result object) {
		var second object
		ok(t, "clone shared-reference result", json.Unmarshal(encode(t, result), &second))
		second["guid"] = "33333333-3333-4333-8333-333333333333"
		second["message"] = object{"text": "Second result sharing the same raw report object."}
		run["results"] = append(run["results"].([]any), second)
	})
	shared := upload("retention-shared", "retention-shared-scan", sharedReport)

	eligibleFinding := retentionFindingForRun(t, h, eligible.RunID)
	heldFinding := retentionFindingForRun(t, h, held.RunID)
	decisionFinding := retentionFindingForRun(t, h, decision.RunID)
	h.json(h.admin, "PATCH", "/api/v1/findings/"+decisionFinding.ID,
		object{"workflowState": "in-progress"}, 200)
	h.json(h.admin, "POST", "/api/v1/findings/"+decisionFinding.ID+"/notes",
		object{"text": "Active human decision protects source evidence from retention."}, 201)

	mergePreview := h.json(h.admin, "POST", "/api/v1/findings/"+eligibleFinding.ID+"/merge-previews",
		object{"otherFindingId": heldFinding.ID}, 200).MergePreview
	correlation := h.json(h.admin, "POST", "/api/v1/findings/"+eligibleFinding.ID+"/merges", object{
		"otherFindingId":          heldFinding.ID,
		"primaryDecisionRevision": mergePreview.Primary.DecisionRevision,
		"primaryEvidenceRevision": mergePreview.Primary.EvidenceRevision,
		"otherDecisionRevision":   mergePreview.Other.DecisionRevision,
		"otherEvidenceRevision":   mergePreview.Other.EvidenceRevision,
		"decision":                correlationDecision(nil, "open", "none", nil),
		"rationale":               "Create owned split audit history for retention preview.",
		"idempotencyKey":          "retention-preview-merge",
	}, 201).Correlation
	splitPreview := h.json(h.admin, "POST", "/api/v1/findings/"+eligibleFinding.ID+"/split-previews",
		object{"memberFindingId": heldFinding.ID}, 200).SplitPreview
	h.json(h.admin, "POST", "/api/v1/findings/"+eligibleFinding.ID+"/splits", object{
		"memberFindingId": heldFinding.ID, "correlationRevision": correlation.Revision,
		"primaryDecisionRevision": splitPreview.Primary.DecisionRevision,
		"primaryEvidenceRevision": splitPreview.Primary.EvidenceRevision,
		"memberDecisionRevision":  splitPreview.Member.DecisionRevision,
		"memberEvidenceRevision":  splitPreview.Member.EvidenceRevision,
		"primaryDecision":         correlationDecision(nil, "open", "none", nil),
		"memberDecision":          correlationDecision(nil, "open", "none", nil),
		"rationale":               "Release both variants while preserving auditable history.",
		"idempotencyKey":          "retention-preview-split",
	}, 201)

	legalHold := h.json(h.admin, "POST", "/api/v1/retention/holds", object{
		"resourceKind": "observation", "resourceId": heldFinding.Observations[0].ID,
		"reason": "Synthetic legal hold propagated to its raw report.",
	}, 201).RetentionHold
	equal(t, "new hold revision", legalHold.Revision, int64(1))
	h.denied(h.admin, "POST", "/api/v1/retention/holds", object{
		"resourceKind": "unsupported", "resourceId": eligible.ID, "reason": "Invalid kind.",
	}, 400, "invalid-input")
	h.denied(viewer, "POST", "/api/v1/retention/holds", object{
		"resourceKind": "import", "resourceId": eligible.ID, "reason": "Viewer cannot create holds.",
	}, 403, "forbidden")
	viewerHolds := h.json(viewer, "GET", "/api/v1/retention/holds", nil, 200).RetentionHolds
	if len(viewerHolds) != 1 || viewerHolds[0] != legalHold {
		t.Fatal("viewer could not read exact workspace-scoped hold history")
	}

	beforeEvidence := map[string][]byte{}
	for _, record := range []imported{eligible, held, decision, shared} {
		beforeEvidence[record.ID] = bytes.Clone(h.request(h.admin, "GET",
			"/api/v1/imports/"+record.ID+"/evidence", nil, 200).Body.Bytes())
	}
	beforeWork := h.work(h.admin, "")
	beforeDecision := h.finding(h.admin, decisionFinding.ID)

	h.clock.Add(int64(250 * 24 * time.Hour))
	h.admin = h.login(h.admin.user.Email, h.password, h.admin.workspace)
	viewer = h.addUser(h.admin, "viewer")
	foreign = h.login(h.admin.user.Email, h.password, foreign.workspace)

	preview := h.json(viewer, "POST", "/api/v1/retention/previews", object{}, 201).RetentionPreview
	equal(t, "preview state", preview.State, "ready")
	equal(t, "preview policy revision", preview.PolicyRevision, policy.Revision)
	if preview.ID == "" || preview.Revision != 1 || preview.SnapshotDigest == "" ||
		!preview.ExpiresAt.After(preview.CreatedAt) {
		t.Fatal("retention preview omitted durable binding metadata")
	}
	hot := retentionSummary(t, preview, "hot-history")
	raw := retentionSummary(t, preview, "raw-report")
	archive := retentionSummary(t, preview, "archived-evidence")
	audit := retentionSummary(t, preview, "audit")
	equal(t, "hot-history action and policy", []any{hot.Action, hot.RetainDays}, []any{"archive-history", 30})
	equal(t, "raw-report action and policy", []any{raw.Action, raw.RetainDays}, []any{"expire-raw-report", 60})
	equal(t, "archived-evidence action and policy", []any{archive.Action, archive.RetainDays}, []any{"expire-archive", 120})
	equal(t, "audit action and policy", []any{audit.Action, audit.RetainDays}, []any{"archive-audit", 240})
	if hot.TotalCount != 5 || hot.EligibleCount != 3 || hot.ProtectedCount != 2 ||
		raw.TotalCount != 4 || raw.EligibleCount != 1 || raw.ProtectedCount != 3 ||
		archive.TotalCount != 0 || audit.TotalCount != 3 || audit.EligibleCount != 3 ||
		audit.ProtectedCount != 0 {
		t.Fatalf("unexpected retention cohort summary: hot=%+v raw=%+v archive=%+v audit=%+v",
			hot, raw, archive, audit)
	}
	if len(beforeDecision.DecisionEvents) != 1 {
		t.Fatal("V23 compatibility fixture did not retain one decision-history event")
	}
	decisionEvent := beforeDecision.DecisionEvents[0]
	decisionItem := retentionItem(t, preview, "audit", decisionEvent.ID)
	if decisionItem.ResourceKind != "finding-decision-event" ||
		decisionItem.SizeBytes != int64(len(v23DecisionArchiveBytes(t, h, decisionEvent.ID))) ||
		len(decisionItem.ProtectedReasons) != 0 {
		t.Fatal("V23 changed the exact eligible decision-history audit candidate")
	}
	equal(t, "held raw report reason",
		retentionItem(t, preview, "raw-report", held.ID).ProtectedReasons, []string{"legal-hold"})
	equal(t, "active decision raw report reason",
		retentionItem(t, preview, "raw-report", decision.ID).ProtectedReasons, []string{"active-decision"})
	equal(t, "shared raw report reason",
		retentionItem(t, preview, "raw-report", shared.ID).ProtectedReasons, []string{"shared-observation-references"})
	equal(t, "eligible raw report remains unprotected",
		retentionItem(t, preview, "raw-report", eligible.ID).ProtectedReasons, []string{})
	h.denied(foreign, "GET", "/api/v1/retention/previews/"+preview.ID, nil, 404, "not-found")

	for _, record := range []imported{eligible, held, decision, shared} {
		after := h.request(h.admin, "GET", "/api/v1/imports/"+record.ID+"/evidence", nil, 200).Body.Bytes()
		if !bytes.Equal(after, beforeEvidence[record.ID]) {
			t.Fatalf("preview changed raw evidence for import %s", record.ID)
		}
	}
	equal(t, "preview did not change Work membership", h.work(h.admin, ""), beforeWork)
	equal(t, "preview did not change active decisions", h.finding(h.admin, decisionFinding.ID), beforeDecision)

	newHold := h.json(h.admin, "POST", "/api/v1/retention/holds", object{
		"resourceKind": "import", "resourceId": eligible.ID, "reason": "Created after preview.",
	}, 201).RetentionHold
	h.denied(h.admin, "POST", "/api/v1/retention/previews/"+preview.ID+"/approvals", object{
		"revision": preview.Revision, "snapshotDigest": preview.SnapshotDigest,
		"rationale": "This stale preview must not be approved.", "idempotencyKey": "stale-hold-preview",
	}, 409, "conflict")
	stale := h.json(h.admin, "GET", "/api/v1/retention/previews/"+preview.ID, nil, 200).RetentionPreview
	equal(t, "stale preview is durably labeled", stale.State, "stale")

	h.json(h.admin, "POST", "/api/v1/retention/holds/"+newHold.ID+"/releases", object{
		"revision": newHold.Revision, "rationale": "Remove the post-preview synthetic hold.",
	}, 200)
	second := h.json(h.admin, "POST", "/api/v1/retention/previews", object{}, 201).RetentionPreview
	policy = h.json(h.admin, "PATCH", "/api/v1/retention/policy", object{
		"revision": policy.Revision, "hotHistoryDays": 31, "rawReportDays": 61,
		"archivedEvidenceDays": 121, "auditDays": 241,
	}, 200).RetentionPolicy
	h.denied(h.admin, "POST", "/api/v1/retention/previews/"+second.ID+"/approvals", object{
		"revision": second.Revision, "snapshotDigest": second.SnapshotDigest,
		"rationale": "Policy changes must invalidate the old preview.", "idempotencyKey": "stale-policy-preview",
	}, 409, "conflict")

	current := h.json(h.admin, "POST", "/api/v1/retention/previews", object{}, 201).RetentionPreview
	approval := object{
		"revision": current.Revision, "snapshotDigest": current.SnapshotDigest,
		"rationale":      "Approve this exact non-destructive retention preview.",
		"idempotencyKey": "approve-current-retention-preview",
	}
	approved := h.json(h.admin, "POST", "/api/v1/retention/previews/"+current.ID+"/approvals",
		approval, 201).RetentionPreview
	equal(t, "approved preview state", approved.State, "approved")
	equal(t, "approval increments revision", approved.Revision, int64(2))
	if approved.ApprovedBy == nil || *approved.ApprovedBy != h.admin.user.ID ||
		approved.ApprovalRationale == nil || *approved.ApprovalRationale == "" {
		t.Fatal("approved preview omitted actor or rationale")
	}
	replayed := h.json(h.admin, "POST", "/api/v1/retention/previews/"+current.ID+"/approvals",
		approval, 200).RetentionPreview
	equal(t, "exact approval replay", replayed, approved)
	changed := object{}
	for key, value := range approval {
		changed[key] = value
	}
	changed["rationale"] = "Changed replay must conflict."
	h.denied(h.admin, "POST", "/api/v1/retention/previews/"+current.ID+"/approvals",
		changed, 409, "conflict")
	h.denied(viewer, "POST", "/api/v1/retention/previews/"+current.ID+"/approvals",
		approval, 403, "forbidden")

	expired := h.json(h.admin, "POST", "/api/v1/retention/previews", object{}, 201).RetentionPreview
	h.clock.Add(int64(16 * time.Minute))
	h.denied(h.admin, "POST", "/api/v1/retention/previews/"+expired.ID+"/approvals", object{
		"revision": expired.Revision, "snapshotDigest": expired.SnapshotDigest,
		"rationale": "An expired preview must not be approved.", "idempotencyKey": "expired-retention-preview",
	}, 409, "conflict")
	equal(t, "expired preview is durably labeled stale",
		h.json(h.admin, "GET", "/api/v1/retention/previews/"+expired.ID, nil, 200).RetentionPreview.State, "stale")

	holds := h.json(h.admin, "GET", "/api/v1/retention/holds", nil, 200).RetentionHolds
	if len(holds) != 2 || !slices.ContainsFunc(holds, func(hold retentionHold) bool {
		return hold.ID == legalHold.ID && hold.ReleasedAt == nil
	}) || !slices.ContainsFunc(holds, func(hold retentionHold) bool {
		return hold.ID == newHold.ID && hold.ReleasedAt != nil
	}) {
		t.Fatal("hold history did not preserve active and released records")
	}
	for _, record := range []imported{eligible, held, decision, shared} {
		after := h.request(h.admin, "GET", "/api/v1/imports/"+record.ID+"/evidence", nil, 200).Body.Bytes()
		if !bytes.Equal(after, beforeEvidence[record.ID]) {
			t.Fatalf("approval changed raw evidence for import %s", record.ID)
		}
	}
	equal(t, "approval did not change Work membership", h.work(h.admin, ""), beforeWork)
	equal(t, "approval did not change active decisions", h.finding(h.admin, decisionFinding.ID), beforeDecision)
}
