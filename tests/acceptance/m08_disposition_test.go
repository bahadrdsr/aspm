//go:build integration

package acceptance

import (
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestM08_ScopedDispositionApprovalsAreExplicitImmutableAndScanIndependent(t *testing.T) {
	h := newHarness(t, true)
	input, _, first := h.seed()
	expires := h.services.cfg.Now().Add(10 * time.Minute)
	h.denied(h.admin, "PATCH", "/api/v1/findings/"+first.ID, object{
		"disposition": "suppressed", "dispositionScope": "source", "suppressionExpiresAt": expires,
	}, 400, "invalid-input")
	suppressed := h.json(h.admin, "PATCH", "/api/v1/findings/"+first.ID, object{
		"disposition": "suppressed", "dispositionScope": "source", "suppressionExpiresAt": expires,
		"rationale": "Suppress this synthetic source result until the bounded expiry.",
	}, 200).Finding
	equal(t, "suppressed disposition", suppressed.Disposition, "suppressed")
	if suppressed.AcceptedRiskExpiresAt != nil || suppressed.DispositionApproval == nil {
		t.Fatal("suppression reused accepted-risk expiry or omitted its approval")
	}
	approval := suppressed.DispositionApproval
	equal(t, "suppression approval disposition", approval.Disposition, "suppressed")
	equal(t, "suppression scope kind", approval.ScopeKind, "source")
	equal(t, "suppression scope value", approval.ScopeValue, first.Observations[0].SourceID)
	equal(t, "suppression approval actor", approval.ActorID, h.admin.user.ID)
	equal(t, "suppression approval actor name", approval.ActorName, h.admin.user.Name)
	equal(t, "suppression approval revision", approval.DecisionRevision, suppressed.DecisionRevision)
	equal(t, "suppression rationale", approval.Rationale,
		"Suppress this synthetic source result until the bounded expiry.")
	sameTime(t, "suppression expiry", approval.ExpiresAt, &expires)
	if approval.ID == "" || approval.FindingID != first.ID || approval.CreatedAt.IsZero() || approval.Expired {
		t.Fatal("suppression approval omitted immutable identity, finding, time, or current expiry state")
	}
	suppressedReport := overview(t, h, h.admin)
	equal(t, "suppressed posture total", suppressedReport.Totals["suppressed"], 1)
	equal(t, "current suppression posture total", suppressedReport.Totals["expiredSuppression"], 0)

	newTime := sourceTime.Add(24 * time.Hour)
	input["scanId"], input["sourceScanAt"], input["collectedAt"] = "suppressed-scan", newTime, newTime.Add(time.Hour)
	h.finish(h.upload(input).ID, "succeeded")
	afterScan := h.finding(h.admin, first.ID)
	equal(t, "scan preserved suppression", afterScan.Disposition, "suppressed")
	equal(t, "scan preserved approval identity", afterScan.DispositionApproval.ID, approval.ID)
	equal(t, "scan created no human decision event", len(afterScan.DecisionEvents), 1)
	h.clock.Add(int64(11 * time.Minute))
	expiredSuppression := h.finding(h.admin, first.ID)
	if expiredSuppression.DispositionApproval == nil || !expiredSuppression.DispositionApproval.Expired {
		t.Fatal("suppression expiry must be computed without rewriting the approval")
	}
	equal(t, "expired suppression posture total", overview(t, h, h.admin).Totals["expiredSuppression"], 1)

	h.denied(h.admin, "PATCH", "/api/v1/findings/"+first.ID, object{
		"disposition": "false-positive", "dispositionScope": "asset",
		"rationale": "Invalid scope for a false-positive decision.",
	}, 400, "invalid-input")
	falsePositive := h.json(h.admin, "PATCH", "/api/v1/findings/"+first.ID, object{
		"disposition": "false-positive", "dispositionScope": "finding",
		"rationale": "Reviewed synthetic evidence does not represent this canonical issue.",
	}, 200).Finding
	equal(t, "false-positive disposition", falsePositive.Disposition, "false-positive")
	if falsePositive.DispositionApproval == nil ||
		falsePositive.DispositionApproval.ID == approval.ID ||
		falsePositive.DispositionApproval.ScopeKind != "finding" ||
		falsePositive.DispositionApproval.ScopeValue != first.ID ||
		falsePositive.DispositionApproval.ExpiresAt != nil {
		t.Fatal("false-positive approval scope, identity, or expiry is invalid")
	}
	falsePositiveReport := overview(t, h, h.admin)
	equal(t, "false-positive posture total", falsePositiveReport.Totals["falsePositive"], 1)
	equal(t, "suppressed posture cleared", falsePositiveReport.Totals["suppressed"], 0)
	h.denied(h.admin, "PATCH", "/api/v1/findings/"+first.ID,
		object{"disposition": "none"}, 400, "invalid-input")
	cleared := h.json(h.admin, "PATCH", "/api/v1/findings/"+first.ID, object{
		"disposition": "none", "rationale": "Clear the reviewed synthetic disposition.",
	}, 200).Finding
	equal(t, "cleared disposition", cleared.Disposition, "none")
	if cleared.DispositionApproval != nil || len(cleared.DecisionEvents) != 3 {
		t.Fatal("clearing a disposition removed immutable history or retained an active approval")
	}
	var count int
	err := h.services.db.QueryRow(h.services.ctx, `SELECT count(*) FROM `+
		pgx.Identifier{h.services.cfg.Schema, "app_finding_disposition_approvals"}.Sanitize()+
		` WHERE workspace_id=$1 AND finding_id=$2`, h.admin.workspace, first.ID).Scan(&count)
	ok(t, "count immutable disposition approvals", err)
	equal(t, "immutable approval count", count, 2)
}
