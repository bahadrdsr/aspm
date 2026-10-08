package app

import (
	"context"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

func validDisposition(value string) bool {
	return value == "none" || value == "accepted-risk" || value == "suppressed" || value == "false-positive"
}

func validDispositionScope(value string) bool {
	return value == "finding" || value == "asset" || value == "source" || value == "scope"
}

func dispositionScopeValue(decision storedFindingDecision) (string, bool) {
	switch decision.DispositionScope {
	case "finding":
		return decision.FindingID, true
	case "asset":
		return decision.AssetID, true
	case "source":
		return decision.SourceID, true
	case "scope":
		value := decision.ScopeID + " / " + decision.ScopeBranch + " (revision " + decision.ScopeRevision + ")"
		return value, value != " /  (revision )"
	default:
		return "", false
	}
}

func (a *Application) insertDispositionApproval(ctx context.Context, tx pgx.Tx, workspace, actor string,
	decision storedFindingDecision, fallbackRationale string) error {
	if decision.Disposition == "none" {
		return nil
	}
	rationale := decision.DispositionRationale
	if rationale == "" {
		rationale = fallbackRationale
	}
	if !validText(rationale, 8192) || !validDispositionScope(decision.DispositionScope) {
		return errInvalid
	}
	scopeValue, ok := dispositionScopeValue(decision)
	if !ok || strings.ContainsRune(scopeValue, 0) || len(scopeValue) > 4096 {
		return errInvalid
	}
	var expires *time.Time
	switch decision.Disposition {
	case "accepted-risk":
		if decision.DispositionScope != "finding" || decision.SuppressionExpiresAt != nil {
			return errInvalid
		}
		expires = decision.AcceptedRiskExpiresAt
	case "suppressed":
		if decision.AcceptedRiskExpiresAt != nil || decision.SuppressionExpiresAt == nil ||
			decision.SuppressionExpiresAt.IsZero() || !decision.SuppressionExpiresAt.After(a.config.Now()) {
			return errInvalid
		}
		expires = decision.SuppressionExpiresAt
	case "false-positive":
		if decision.DispositionScope != "finding" ||
			decision.AcceptedRiskExpiresAt != nil || decision.SuppressionExpiresAt != nil {
			return errInvalid
		}
	default:
		return errInvalid
	}
	_, err := tx.Exec(ctx, `INSERT INTO `+a.table("finding_disposition_approvals")+`
		(id,workspace_id,finding_id,decision_revision,actor_id,disposition,scope_kind,scope_value,
		 rationale,expires_at,created_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`,
		newID(), workspace, decision.FindingID, decision.Revision, actor, decision.Disposition,
		decision.DispositionScope, scopeValue, rationale, expires, a.config.Now().UTC())
	return err
}

func (a *Application) readDispositionApproval(ctx context.Context, workspace, finding, disposition string,
	revision int64) (*FindingDispositionApproval, error) {
	if disposition == "none" {
		return nil, nil
	}
	var approval FindingDispositionApproval
	err := a.pool.QueryRow(ctx, `SELECT a.id,a.finding_id,a.decision_revision,a.actor_id,u.name,
		a.disposition,a.scope_kind,a.scope_value,a.rationale,a.expires_at,a.created_at
		FROM `+a.table("finding_disposition_approvals")+` a
		JOIN `+a.table("users")+` u ON u.id=a.actor_id
		WHERE a.workspace_id=$1 AND a.finding_id=$2 AND a.disposition=$3 AND a.decision_revision<=$4
		ORDER BY a.decision_revision DESC LIMIT 1`,
		workspace, finding, disposition, revision).Scan(&approval.ID, &approval.FindingID,
		&approval.DecisionRevision, &approval.ActorID, &approval.ActorName, &approval.Disposition,
		&approval.ScopeKind, &approval.ScopeValue, &approval.Rationale, &approval.ExpiresAt, &approval.CreatedAt)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	approval.Expired = approval.ExpiresAt != nil && !a.config.Now().Before(*approval.ExpiresAt)
	return &approval, nil
}
