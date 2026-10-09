//go:build integration

package acceptance

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5"
)

const v25MaxSafeInteger = int64(9007199254740991)

type v25WorkItem struct {
	workItem
	DecisionRevision      int64
	Disposition           string
	AcceptedRiskExpiresAt *time.Time
	RiskAcceptanceExpired bool
}

type v25MutationSnapshot struct {
	Findings       []string
	TableCounts    map[string]int
	SchemaVersions []string
}

func v25Report(t *testing.T, count int) []byte {
	t.Helper()
	return sarif(t, func(run, result object) {
		results := []any{result}
		for index := 1; index < count; index++ {
			var next object
			ok(t, "clone V25 report result", json.Unmarshal(encode(t, result), &next))
			next["guid"] = fmt.Sprintf("25252525-2525-4252-8252-%012d", index)
			next["message"] = object{"text": fmt.Sprintf("V25 synthetic selected finding %d.", index+1)}
			results = append(results, next)
		}
		run["results"] = results
	})
}

func v25SeedFindings(t *testing.T, h *harness, count int, source string) (asset, object, []string) {
	t.Helper()
	a := h.asset(h.admin, "V25 bounded bulk risk repository "+source, &h.admin.user.ID)
	input := h.input(a.ID, "sarif", v25Report(t, count))
	input["sourceId"], input["scanId"] = source, source+"-scan-1"
	h.finish(h.upload(input).ID, "succeeded")
	work := h.work(h.admin, "")
	ids := make([]string, 0, count)
	for _, item := range work {
		if item.AssetName == a.Name {
			ids = append(ids, item.ID)
		}
	}
	if len(ids) != count {
		t.Fatalf("V25 fixture seeded %d findings, want %d", len(ids), count)
	}
	sort.Strings(ids)
	return a, input, ids
}

func v25AddFinding(t *testing.T, h *harness, a asset, source string) string {
	t.Helper()
	input := h.input(a.ID, "sarif", v25Report(t, 1))
	input["sourceId"], input["scanId"] = source, source+"-scan-1"
	input["sourceScanAt"], input["collectedAt"] = sourceTime.Add(time.Hour), sourceTime.Add(2*time.Hour)
	h.finish(h.upload(input).ID, "succeeded")
	var id string
	ok(t, "read V25 distinct-source finding", h.services.db.QueryRow(h.services.ctx, `SELECT id FROM `+
		pgx.Identifier{h.services.cfg.Schema, "app_findings"}.Sanitize()+
		` WHERE workspace_id=$1 AND asset_id=$2 AND source_id=$3`,
		h.admin.workspace, a.ID, source).Scan(&id))
	return id
}

func v25Decision(f finding) findingDecision {
	decision := findingDecision{
		OwnerID:               f.OwnerID,
		WorkflowState:         f.WorkflowState,
		Disposition:           f.Disposition,
		AcceptedRiskExpiresAt: f.AcceptedRiskExpiresAt,
	}
	if f.DispositionApproval != nil {
		decision.DispositionScope = f.DispositionApproval.ScopeKind
		decision.DispositionRationale = f.DispositionApproval.Rationale
		if f.Disposition == "suppressed" {
			decision.SuppressionExpiresAt = f.DispositionApproval.ExpiresAt
		}
	}
	return decision
}

func v25SameString(left, right *string) bool {
	return (left == nil && right == nil) || (left != nil && right != nil && *left == *right)
}

func v25SameCorrelationMembership(left, right *findingCorrelation) bool {
	if left == nil || right == nil {
		return left == nil && right == nil
	}
	if left.ID != right.ID || left.WorkspaceID != right.WorkspaceID ||
		left.PrimaryFindingID != right.PrimaryFindingID || left.State != right.State ||
		left.Revision != right.Revision || !reflect.DeepEqual(left.Events, right.Events) ||
		len(left.Members) != len(right.Members) {
		return false
	}
	for index := range left.Members {
		if left.Members[index].FindingID != right.Members[index].FindingID ||
			left.Members[index].SourceID != right.Members[index].SourceID ||
			left.Members[index].Active != right.Members[index].Active {
			return false
		}
	}
	return true
}

func v25Versions(t *testing.T, h *harness) []string {
	t.Helper()
	rows, err := h.services.db.Query(h.services.ctx, `SELECT ledger.version::text FROM `+
		pgx.Identifier{h.services.cfg.Schema, "app_schema_versions"}.Sanitize()+
		` AS ledger ORDER BY ledger.version`)
	ok(t, "read V25 unchanged migration ledger", err)
	defer rows.Close()
	var result []string
	for rows.Next() {
		var value string
		ok(t, "scan V25 migration ledger", rows.Scan(&value))
		result = append(result, value)
	}
	ok(t, "finish V25 migration ledger", rows.Err())
	return result
}

func v25HistoryIDs(t *testing.T, h *harness, table string) []string {
	t.Helper()
	rows, err := h.services.db.Query(h.services.ctx, `SELECT id FROM `+
		pgx.Identifier{h.services.cfg.Schema, "app_" + table}.Sanitize()+
		` WHERE workspace_id=$1 ORDER BY id`, h.admin.workspace)
	ok(t, "read V25 "+table+" identities", err)
	defer rows.Close()
	var result []string
	for rows.Next() {
		var id string
		ok(t, "scan V25 "+table+" identity", rows.Scan(&id))
		result = append(result, id)
	}
	ok(t, "finish V25 "+table+" identities", rows.Err())
	return result
}

func v25Revision(t *testing.T, h *harness, workspace, id string) int64 {
	t.Helper()
	var revision int64
	ok(t, "read V25 finding decision revision", h.services.db.QueryRow(h.services.ctx, `SELECT decision_revision FROM `+
		pgx.Identifier{h.services.cfg.Schema, "app_findings"}.Sanitize()+
		` WHERE workspace_id=$1 AND id=$2`, workspace, id).Scan(&revision))
	return revision
}

func v25ObjectKeys(t *testing.T, h *harness) []string {
	t.Helper()
	var result []string
	for _, prefix := range []string{h.services.cfg.Storage.Prefix, h.services.cfg.ArchiveStorage.Prefix} {
		pages := s3.NewListObjectsV2Paginator(h.services.s3, &s3.ListObjectsV2Input{
			Bucket: aws.String(h.services.cfg.Storage.Bucket), Prefix: aws.String(prefix), MaxKeys: aws.Int32(100),
		})
		for pages.HasMorePages() {
			page, err := pages.NextPage(h.services.ctx)
			ok(t, "list V25 owned object keys", err)
			for _, item := range page.Contents {
				result = append(result, aws.ToString(item.Key))
			}
		}
	}
	sort.Strings(result)
	return result
}

func v25Snapshot(t *testing.T, h *harness) v25MutationSnapshot {
	t.Helper()
	rows, err := h.services.db.Query(h.services.ctx, `SELECT id,COALESCE(owner_id,''),workflow_state,
		disposition,COALESCE(accepted_risk_expires_at::text,''),decision_revision
		FROM `+pgx.Identifier{h.services.cfg.Schema, "app_findings"}.Sanitize()+
		` WHERE workspace_id=$1 ORDER BY id`, h.admin.workspace)
	ok(t, "read V25 atomic finding snapshot", err)
	var findings []string
	for rows.Next() {
		var id, owner, workflow, disposition, expiry string
		var revision int64
		ok(t, "scan V25 atomic finding snapshot",
			rows.Scan(&id, &owner, &workflow, &disposition, &expiry, &revision))
		findings = append(findings, fmt.Sprintf("%s|%s|%s|%s|%s|%d",
			id, owner, workflow, disposition, expiry, revision))
	}
	ok(t, "finish V25 atomic finding snapshot", rows.Err())
	rows.Close()

	tables := []string{
		"finding_disposition_approvals", "finding_decision_events", "report_snapshots",
		"notification_policies", "notification_policy_revisions", "finding_change_events", "notification_policy_events",
		"finding_deliveries", "retention_previews", "retention_runs", "archive_publications",
	}
	counts := make(map[string]int, len(tables))
	for _, table := range tables {
		var count int
		ok(t, "count V25 "+table, h.services.db.QueryRow(h.services.ctx, `SELECT count(*) FROM `+
			pgx.Identifier{h.services.cfg.Schema, "app_" + table}.Sanitize()+
			` WHERE workspace_id=$1`, h.admin.workspace).Scan(&count))
		counts[table] = count
	}
	return v25MutationSnapshot{Findings: findings, TableCounts: counts, SchemaVersions: v25Versions(t, h)}
}

func v25AssertAtomic(t *testing.T, h *harness, before v25MutationSnapshot) {
	t.Helper()
	if after := v25Snapshot(t, h); !reflect.DeepEqual(after, before) {
		t.Fatal("V25 rejected request changed finding, history, report, policy, delivery, retention, archive, or schema state")
	}
}

func v25BulkBody(ids []string, revisions map[string]int64, expiry any, rationale string) object {
	return object{
		"findingIds": ids, "decisionRevisions": revisions, "disposition": "accepted-risk",
		"acceptedRiskExpiresAt": expiry, "rationale": rationale,
	}
}

func v25Merge(t *testing.T, h *harness, primary, secondary string) {
	t.Helper()
	preview := h.json(h.admin, "POST", "/api/v1/findings/"+primary+"/merge-previews",
		object{"otherFindingId": secondary}, 200).MergePreview
	h.json(h.admin, "POST", "/api/v1/findings/"+primary+"/merges", object{
		"otherFindingId":          secondary,
		"primaryDecisionRevision": preview.Primary.DecisionRevision,
		"primaryEvidenceRevision": preview.Primary.EvidenceRevision,
		"otherDecisionRevision":   preview.Other.DecisionRevision,
		"otherEvidenceRevision":   preview.Other.EvidenceRevision,
		"decision":                correlationDecision(&h.admin.user.ID, "open", "none", nil),
		"rationale":               "Keep the V25 primary visible while retaining its correlated source member.",
		"idempotencyKey":          "v25-bulk-risk-correlation",
	}, 201)
}

func TestM08_V25BulkAcceptedRiskCreatesIndependentImmutableHistory(t *testing.T) {
	h, objectStore := newV23HistoryHarness(t)
	analyst := h.addUser(h.admin, "analyst")
	a, input, ids := v25SeedFindings(t, h, 4, "v25-bulk-risk")
	secondary := v25AddFinding(t, h, a, "v25-bulk-risk-secondary")
	now := h.services.cfg.Now()

	priorRiskExpiry := now.Add(12 * time.Hour)
	h.json(h.admin, "PATCH", "/api/v1/findings/"+ids[0], object{
		"ownerId": analyst.user.ID, "workflowState": "in-progress",
		"disposition": "accepted-risk", "acceptedRiskExpiresAt": priorRiskExpiry,
		"rationale": "Prior finding-scoped accepted risk remains immutable history.",
	}, 200)
	priorSuppressionExpiry := now.Add(18 * time.Hour)
	h.json(h.admin, "PATCH", "/api/v1/findings/"+ids[1], object{
		"workflowState": "pending-retest", "disposition": "suppressed",
		"dispositionScope": "source", "suppressionExpiresAt": priorSuppressionExpiry,
		"rationale": "Prior source suppression remains immutable history.",
	}, 200)
	h.json(h.admin, "PATCH", "/api/v1/findings/"+ids[2], object{
		"workflowState": "resolved", "disposition": "false-positive",
		"dispositionScope": "finding",
		"rationale":        "Prior false-positive decision remains immutable history.",
	}, 200)
	v25Merge(t, h, ids[3], secondary)
	for _, id := range ids {
		h.json(h.admin, "POST", "/api/v1/findings/"+id+"/notes",
			object{"text": "Preserve this V25 analyst note across the bulk decision."}, 201)
	}

	selected := append([]string(nil), ids...)
	before := make(map[string]finding, len(selected))
	revisions := make(map[string]int64, len(selected))
	for _, id := range selected {
		before[id] = h.finding(h.admin, id)
		revisions[id] = before[id].DecisionRevision
	}
	if before[ids[3]].Correlation == nil {
		t.Fatal("V25 fixture did not retain a visible correlated primary")
	}
	priorApprovals := v25HistoryIDs(t, h, "finding_disposition_approvals")
	priorEvents := v25HistoryIDs(t, h, "finding_decision_events")
	beforeReport := overview(t, h, h.admin)
	expectedAccepted := beforeReport.Totals["acceptedRisk"]
	for _, id := range selected {
		if before[id].Disposition != "accepted-risk" {
			expectedAccepted++
		}
	}
	wantVersions := make([]string, 26)
	for index := range wantVersions {
		wantVersions[index] = fmt.Sprint(index + 1)
	}
	if versions := v25Versions(t, h); !reflect.DeepEqual(versions, wantVersions) {
		t.Fatalf("bulk accepted-risk migration ledger got %v, want exact V26 %v", versions, wantVersions)
	}

	requestOrder := []string{selected[3], selected[1], selected[2], selected[0]}
	expiry := now.Add(48 * time.Hour)
	rationale := "Approve the selected synthetic findings as accepted risk after one bounded review."
	body := v25BulkBody(requestOrder, revisions, expiry, rationale)
	objectStore.arm()
	response := h.json(analyst, "PATCH", "/api/v1/findings", body, 200)
	if objectStore.calls.Load() != 0 {
		t.Fatal("V25 bulk accepted risk performed object-store I/O")
	}
	equal(t, "V25 bulk response total", response.Total, len(selected))
	equal(t, "V25 bulk response cursor", response.NextCursor, (*string)(nil))
	bulkItems := items[v25WorkItem](t, response)
	equal(t, "V25 bulk response item count", len(bulkItems), len(selected))
	responseIDs := make([]string, len(bulkItems))
	for index, item := range bulkItems {
		responseIDs[index] = item.ID
		if item.DecisionRevision != before[item.ID].DecisionRevision+1 ||
			item.Disposition != "accepted-risk" || item.RiskAcceptanceExpired {
			t.Fatalf("V25 canonical Work item omitted committed decision metadata: %#v", item)
		}
		sameTime(t, "V25 canonical Work expiry", item.AcceptedRiskExpiresAt, &expiry)
	}
	equal(t, "V25 response sorted by finding ID", responseIDs, selected)

	expectedChanges := map[string][]string{
		ids[0]: {"acceptedRiskExpiresAt", "dispositionRationale"},
		ids[1]: {"disposition", "acceptedRiskExpiresAt", "dispositionScope", "suppressionExpiresAt", "dispositionRationale"},
		ids[2]: {"disposition", "acceptedRiskExpiresAt", "dispositionRationale"},
		ids[3]: {"disposition", "acceptedRiskExpiresAt", "dispositionScope", "dispositionRationale"},
	}
	approvalIDs := map[string]bool{}
	eventIDs := map[string]bool{}
	scopeValues := map[string]bool{}
	afterFirst := make(map[string]finding, len(selected))
	for _, id := range selected {
		got := h.finding(h.admin, id)
		afterFirst[id] = got
		old := before[id]
		if got.DecisionRevision != old.DecisionRevision+1 || got.Disposition != "accepted-risk" ||
			got.DispositionApproval == nil || got.RiskAcceptanceExpired {
			t.Fatalf("V25 did not apply one accepted-risk decision to %s", id)
		}
		if !reflect.DeepEqual(got.workItem, old.workItem) {
			t.Fatalf("V25 bulk decision changed established Work fields for %s:\nbefore=%+v\nafter=%+v",
				id, old.workItem, got.workItem)
		}
		if !v25SameString(got.OwnerID, old.OwnerID) ||
			got.SourceState != old.SourceState || !reflect.DeepEqual(got.SourceFreshnessAt, old.SourceFreshnessAt) ||
			!reflect.DeepEqual(got.Evidence, old.Evidence) || !reflect.DeepEqual(got.Notes, old.Notes) ||
			!reflect.DeepEqual(got.Observations, old.Observations) ||
			!v25SameCorrelationMembership(got.Correlation, old.Correlation) ||
			got.VerifiedResolution != old.VerifiedResolution {
			t.Fatalf("V25 bulk decision changed owner, workflow, source, evidence, notes, observations, correlation, or verification for %s", id)
		}
		sameTime(t, "V25 finding accepted-risk expiry", got.AcceptedRiskExpiresAt, &expiry)
		approval := got.DispositionApproval
		if approval.ID == "" || approvalIDs[approval.ID] || approval.FindingID != id ||
			approval.DecisionRevision != got.DecisionRevision || approval.ActorID != analyst.user.ID ||
			approval.ActorName != analyst.user.Name || approval.Disposition != "accepted-risk" ||
			approval.ScopeKind != "finding" || approval.ScopeValue != id || scopeValues[approval.ScopeValue] ||
			approval.Rationale != rationale || !approval.CreatedAt.Equal(now) || approval.Expired {
			t.Fatalf("V25 approval is not an independent immutable finding-scoped record: %#v", approval)
		}
		sameTime(t, "V25 approval expiry", approval.ExpiresAt, &expiry)
		approvalIDs[approval.ID], scopeValues[approval.ScopeValue] = true, true

		if len(got.DecisionEvents) != len(old.DecisionEvents)+1 {
			t.Fatalf("V25 finding %s did not append exactly one decision event", id)
		}
		event := got.DecisionEvents[len(got.DecisionEvents)-1]
		wantAfter := findingDecision{
			OwnerID: old.OwnerID, WorkflowState: old.WorkflowState, Disposition: "accepted-risk",
			AcceptedRiskExpiresAt: &expiry, DispositionScope: "finding", DispositionRationale: rationale,
		}
		if event.ID == "" || eventIDs[event.ID] || event.DecisionRevision != got.DecisionRevision ||
			event.ActorID != analyst.user.ID ||
			event.ActorName != analyst.user.Name || event.Action != "bulk-update" ||
			event.Rationale != rationale || !event.CreatedAt.Equal(now) ||
			!reflect.DeepEqual(event.ChangedFields, expectedChanges[id]) ||
			!reflect.DeepEqual(event.Before, v25Decision(old)) ||
			!reflect.DeepEqual(event.After, wantAfter) {
			t.Fatalf("V25 decision event lost exact actor, changes, or before/after state: %#v", event)
		}
		eventIDs[event.ID] = true
	}
	afterApprovals := v25HistoryIDs(t, h, "finding_disposition_approvals")
	afterEvents := v25HistoryIDs(t, h, "finding_decision_events")
	if len(afterApprovals) != len(priorApprovals)+len(selected) ||
		len(afterEvents) != len(priorEvents)+len(selected) {
		t.Fatal("V25 did not append exactly one approval and decision event per selected finding")
	}
	for _, id := range priorApprovals {
		if !contains(afterApprovals, id) {
			t.Fatal("V25 rewrote or deleted a prior disposition approval")
		}
	}
	for _, id := range priorEvents {
		if !contains(afterEvents, id) {
			t.Fatal("V25 rewrote or deleted a prior decision event")
		}
	}
	equal(t, "V25 report accepted-risk count", overview(t, h, h.admin).Totals["acceptedRisk"], expectedAccepted)

	workResponse := h.json(h.admin, "GET", "/api/v1/work", nil, 200)
	workItems := items[v25WorkItem](t, workResponse)
	acceptedVisible := 0
	for _, item := range workItems {
		if item.Disposition == "accepted-risk" {
			acceptedVisible++
		}
	}
	equal(t, "V25 Work accepted-risk count", acceptedVisible, len(selected))

	currentRevisions := make(map[string]int64, len(selected))
	for _, id := range selected {
		currentRevisions[id] = afterFirst[id].DecisionRevision
	}
	noChange := v25Snapshot(t, h)
	objectStore.arm()
	h.denied(analyst, "PATCH", "/api/v1/findings",
		v25BulkBody(requestOrder, currentRevisions, expiry, rationale), 409, "conflict")
	if objectStore.calls.Load() != 0 {
		t.Fatal("V25 exact no-change conflict performed object-store I/O")
	}
	v25AssertAtomic(t, h, noChange)

	reviewedRationale := "Renew the selected accepted-risk decisions after reviewing the unchanged source facts."
	objectStore.arm()
	renewed := h.json(analyst, "PATCH", "/api/v1/findings",
		v25BulkBody(requestOrder, currentRevisions, nil, reviewedRationale), 200)
	if objectStore.calls.Load() != 0 {
		t.Fatal("V25 rationale-only reviewed decision performed object-store I/O")
	}
	renewedItems := items[v25WorkItem](t, renewed)
	renewedIDs := make([]string, len(renewedItems))
	latestApprovalIDs := make(map[string]string, len(selected))
	latestRevisions := make(map[string]int64, len(selected))
	latestEventIDs := make([]string, 0, len(selected))
	for index, item := range renewedItems {
		renewedIDs[index] = item.ID
	}
	equal(t, "V25 renewed response sorted by finding ID", renewedIDs, selected)
	for _, id := range selected {
		got := h.finding(h.admin, id)
		latestRevisions[id] = got.DecisionRevision
		if got.DecisionRevision != afterFirst[id].DecisionRevision+1 || got.DispositionApproval == nil ||
			got.DispositionApproval.ID == afterFirst[id].DispositionApproval.ID ||
			got.DispositionApproval.Rationale != reviewedRationale ||
			got.AcceptedRiskExpiresAt != nil || got.DispositionApproval.ExpiresAt != nil ||
			len(got.DecisionEvents) != len(afterFirst[id].DecisionEvents)+1 {
			t.Fatalf("V25 changed-rationale review did not create fresh immutable history for %s", id)
		}
		event := got.DecisionEvents[len(got.DecisionEvents)-1]
		if !reflect.DeepEqual(event.ChangedFields, []string{"acceptedRiskExpiresAt", "dispositionRationale"}) ||
			event.Action != "bulk-update" || event.Rationale != reviewedRationale {
			t.Fatalf("V25 changed-rationale event was not exact: %#v", event)
		}
		latestApprovalIDs[id] = got.DispositionApproval.ID
		latestEventIDs = append(latestEventIDs, event.ID)
	}

	approvalCountBeforeScan := len(v25HistoryIDs(t, h, "finding_disposition_approvals"))
	eventCountBeforeScan := len(v25HistoryIDs(t, h, "finding_decision_events"))
	input["scanId"] = "v25-bulk-risk-scan-2"
	input["sourceScanAt"] = sourceTime.Add(72 * time.Hour)
	input["collectedAt"] = sourceTime.Add(73 * time.Hour)
	h.finish(h.upload(input).ID, "succeeded")
	for _, id := range selected {
		got := h.finding(h.admin, id)
		if got.DecisionRevision != latestRevisions[id] || got.Disposition != "accepted-risk" ||
			got.DispositionApproval == nil || got.DispositionApproval.ID != latestApprovalIDs[id] {
			t.Fatalf("V25 source scan rewrote the human decision for %s", id)
		}
	}
	if len(v25HistoryIDs(t, h, "finding_disposition_approvals")) != approvalCountBeforeScan ||
		len(v25HistoryIDs(t, h, "finding_decision_events")) != eventCountBeforeScan {
		t.Fatal("V25 source scan created or removed approval or decision history")
	}
	equal(t, "V25 scan preserved accepted-risk report count",
		overview(t, h, h.admin).Totals["acceptedRisk"], expectedAccepted)

	aged := now.Add(-731 * 24 * time.Hour)
	_, err := h.services.db.Exec(h.services.ctx, `UPDATE `+
		pgx.Identifier{h.services.cfg.Schema, "app_finding_decision_events"}.Sanitize()+
		` SET created_at=$3 WHERE workspace_id=$1 AND id=ANY($2)`,
		h.admin.workspace, latestEventIDs, aged)
	ok(t, "age V25 decision events into ordinary V23/V24 audit candidates", err)
	preview := h.json(h.admin, "POST", "/api/v1/retention/previews", object{}, 201).RetentionPreview
	for _, id := range latestEventIDs {
		item := v23RetentionItem(t, preview, "finding-decision-event", id)
		if item.Class != "audit" || item.Action != "archive-audit" || len(item.ProtectedReasons) != 0 {
			t.Fatalf("V25 decision event did not use ordinary V23/V24 retention semantics: %#v", item)
		}
	}
	equal(t, "bulk accepted-risk final migration ledger remains V26", v25Versions(t, h), wantVersions)
}

func TestM08_V25BulkAcceptedRiskRejectsInvalidInvisibleAndStaleSetsAtomically(t *testing.T) {
	h := newHarness(t, true)
	a, _, ids := v25SeedFindings(t, h, 2, "v25-validation")
	hidden := v25AddFinding(t, h, a, "v25-validation-secondary")
	v25Merge(t, h, ids[0], hidden)
	visible := []string{ids[0], ids[1]}

	deletedAsset, _, deletedIDs := v25SeedFindings(t, h, 1, "v25-deleted")
	deletedID := deletedIDs[0]
	deletedRevision := v25Revision(t, h, h.admin.workspace, deletedID)
	h.request(h.admin, "DELETE", "/api/v1/assets/"+deletedAsset.ID, nil, http.StatusNoContent)

	foreign := h.addWorkspace()
	foreignAsset := h.asset(foreign, "V25 foreign risk repository", nil)
	foreignInput := h.input(foreignAsset.ID, "sarif", v25Report(t, 1))
	foreignInput["sourceId"], foreignInput["scanId"] = "v25-foreign", "v25-foreign-scan"
	h.finishAs(foreign, h.uploadAs(foreign, foreignInput).ID, "succeeded")
	foreignFinding := h.work(foreign, "")[0]
	foreignRevision := v25Revision(t, h, foreign.workspace, foreignFinding.ID)

	revisions := map[string]int64{}
	for _, id := range visible {
		revisions[id] = v25Revision(t, h, h.admin.workspace, id)
	}
	hiddenRevision := v25Revision(t, h, h.admin.workspace, hidden)
	future := h.services.cfg.Now().Add(time.Hour)
	rationale := "Reject invalid V25 selected-set inputs without any partial mutation."
	viewer := h.addUser(h.admin, "viewer")

	objectsBefore := v25ObjectKeys(t, h)
	before := v25Snapshot(t, h)
	h.denied(viewer, "PATCH", "/api/v1/findings",
		v25BulkBody(visible, revisions, future, rationale), 403, "forbidden")
	v25AssertAtomic(t, h, before)

	invalid := []struct {
		name string
		body object
	}{
		{"duplicate finding", v25BulkBody([]string{visible[0], visible[0]},
			map[string]int64{visible[0]: revisions[visible[0]]}, future, rationale)},
		{"uppercase finding", v25BulkBody([]string{strings.ToUpper(visible[0])},
			map[string]int64{strings.ToUpper(visible[0]): revisions[visible[0]]}, future, rationale)},
		{"missing revision key", v25BulkBody(visible,
			map[string]int64{visible[0]: revisions[visible[0]]}, future, rationale)},
		{"extra revision key", v25BulkBody(visible, map[string]int64{
			visible[0]: revisions[visible[0]], visible[1]: revisions[visible[1]],
			strings.Repeat("e", 32): 1,
		}, future, rationale)},
		{"zero revision", v25BulkBody(visible,
			map[string]int64{visible[0]: 0, visible[1]: revisions[visible[1]]}, future, rationale)},
		{"negative revision", v25BulkBody(visible,
			map[string]int64{visible[0]: -1, visible[1]: revisions[visible[1]]}, future, rationale)},
		{"wrong disposition", func() object {
			value := v25BulkBody(visible, revisions, future, rationale)
			value["disposition"] = "suppressed"
			return value
		}()},
		{"missing explicit expiry", object{
			"findingIds": visible, "decisionRevisions": revisions,
			"disposition": "accepted-risk", "rationale": rationale,
		}},
		{"expiry equal to Now", v25BulkBody(visible, revisions, h.services.cfg.Now(), rationale)},
		{"past expiry", v25BulkBody(visible, revisions, h.services.cfg.Now().Add(-time.Second), rationale)},
		{"malformed expiry", v25BulkBody(visible, revisions, "2026-10-08 12:00:00", rationale)},
		{"blank rationale", v25BulkBody(visible, revisions, future, " \n\t ")},
		{"NUL rationale", v25BulkBody(visible, revisions, future, "review\000decision")},
		{"overlong UTF-8 rationale", v25BulkBody(visible, revisions, future, strings.Repeat("é", 4097))},
		{"unexpected field", func() object {
			value := v25BulkBody(visible, revisions, future, rationale)
			value["approvalId"] = strings.Repeat("a", 32)
			return value
		}()},
		{"mixed owner and risk", func() object {
			value := v25BulkBody(visible, revisions, future, rationale)
			value["ownerId"] = h.admin.user.ID
			return value
		}()},
		{"mixed workflow and risk", func() object {
			value := v25BulkBody(visible, revisions, future, rationale)
			value["workflowState"] = "resolved"
			return value
		}()},
		{"risk fields on owner request", object{
			"findingIds": visible, "ownerId": h.admin.user.ID, "rationale": rationale,
			"decisionRevisions": revisions,
		}},
	}
	tooMany := make([]string, 101)
	tooManyRevisions := make(map[string]int64, 101)
	for index := range tooMany {
		tooMany[index] = fmt.Sprintf("%032x", 0x250000+index)
		tooManyRevisions[tooMany[index]] = 1
	}
	invalid = append(invalid, struct {
		name string
		body object
	}{"over limit", v25BulkBody(tooMany, tooManyRevisions, future, rationale)})
	for _, test := range invalid {
		t.Log("V25 invalid atomic case:", test.name)
		state := v25Snapshot(t, h)
		h.denied(h.admin, "PATCH", "/api/v1/findings", test.body, 400, "invalid-input")
		v25AssertAtomic(t, h, state)
	}

	for _, raw := range []string{
		fmt.Sprintf(`{"findingIds":["%s"],"decisionRevisions":{"%s":1.5},"disposition":"accepted-risk","acceptedRiskExpiresAt":"%s","rationale":"fractional revision"}`,
			visible[0], visible[0], future.Format(time.RFC3339)),
		fmt.Sprintf(`{"findingIds":["%s"],"decisionRevisions":{"%s":%d},"disposition":"accepted-risk","acceptedRiskExpiresAt":"%s","rationale":"unsafe revision"}`,
			visible[0], visible[0], v25MaxSafeInteger+1, future.Format(time.RFC3339)),
	} {
		state := v25Snapshot(t, h)
		reply := h.decode(h.request(h.admin, "PATCH", "/api/v1/findings", []byte(raw), 400))
		if reply.Error == nil || reply.Error.Code != "invalid-input" {
			t.Fatal("V25 non-safe decision revision did not return invalid-input")
		}
		v25AssertAtomic(t, h, state)
	}

	missing := strings.Repeat("f", 32)
	notFound := []struct {
		name      string
		ids       []string
		revisions map[string]int64
	}{
		{"foreign", []string{visible[0], foreignFinding.ID}, map[string]int64{
			visible[0]: revisions[visible[0]], foreignFinding.ID: foreignRevision,
		}},
		{"secondary hidden", []string{visible[0], hidden}, map[string]int64{
			visible[0]: revisions[visible[0]], hidden: hiddenRevision,
		}},
		{"deleted", []string{visible[0], deletedID}, map[string]int64{
			visible[0]: revisions[visible[0]], deletedID: deletedRevision,
		}},
		{"missing", []string{visible[0], missing}, map[string]int64{
			visible[0]: revisions[visible[0]], missing: 1,
		}},
	}
	for _, test := range notFound {
		t.Log("V25 invisible atomic case:", test.name)
		state := v25Snapshot(t, h)
		h.denied(h.admin, "PATCH", "/api/v1/findings",
			v25BulkBody(test.ids, test.revisions, future, rationale), 404, "not-found")
		v25AssertAtomic(t, h, state)
	}

	stale := map[string]int64{visible[0]: revisions[visible[0]], visible[1]: revisions[visible[1]]}
	h.json(h.admin, "PATCH", "/api/v1/findings/"+visible[1], object{
		"workflowState": "in-progress", "rationale": "Advance one selected finding after the bounded review.",
	}, 200)
	state := v25Snapshot(t, h)
	h.denied(h.admin, "PATCH", "/api/v1/findings",
		v25BulkBody(visible, stale, future, rationale), 409, "conflict")
	v25AssertAtomic(t, h, state)

	if after := v25ObjectKeys(t, h); !reflect.DeepEqual(after, objectsBefore) {
		t.Fatal("V25 rejected bulk requests changed raw or archive objects")
	}
}

func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
