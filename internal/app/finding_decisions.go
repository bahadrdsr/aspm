package app

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	maxBulkFindings  = 100
	decisionPageSize = 100
	maxJSONSafeInt   = int64(9007199254740991)
)

type findingDecisionPatch struct {
	OwnerID               optional[string]    `json:"ownerId"`
	WorkflowState         optional[string]    `json:"workflowState"`
	Disposition           optional[string]    `json:"disposition"`
	AcceptedRiskExpiresAt optional[time.Time] `json:"acceptedRiskExpiresAt"`
	DispositionScope      optional[string]    `json:"dispositionScope"`
	SuppressionExpiresAt  optional[time.Time] `json:"suppressionExpiresAt"`
	Rationale             string              `json:"rationale"`
	ExpectedRevision      *int64              `json:"-"`
	RejectNoChange        bool                `json:"-"`
}

type storedFindingDecision struct {
	FindingDecision
	FindingID, AssetID, SourceID, ScopeID, ScopeRevision, ScopeBranch string
	Revision                                                          int64
}

func validWorkflowState(value string) bool {
	return value == "open" || value == "in-progress" || value == "pending-retest" || value == "resolved"
}

func sameString(left, right *string) bool {
	return (left == nil && right == nil) || (left != nil && right != nil && *left == *right)
}

func sameTimePointer(left, right *time.Time) bool {
	return (left == nil && right == nil) || (left != nil && right != nil && left.Equal(*right))
}

func decisionChanges(before, after FindingDecision) []string {
	result := make([]string, 0, 7)
	if !sameString(before.OwnerID, after.OwnerID) {
		result = append(result, "ownerId")
	}
	if before.WorkflowState != after.WorkflowState {
		result = append(result, "workflowState")
	}
	if before.Disposition != after.Disposition {
		result = append(result, "disposition")
	}
	if !sameTimePointer(before.AcceptedRiskExpiresAt, after.AcceptedRiskExpiresAt) {
		result = append(result, "acceptedRiskExpiresAt")
	}
	if before.DispositionScope != after.DispositionScope {
		result = append(result, "dispositionScope")
	}
	if !sameTimePointer(before.SuppressionExpiresAt, after.SuppressionExpiresAt) {
		result = append(result, "suppressionExpiresAt")
	}
	if before.DispositionRationale != after.DispositionRationale {
		result = append(result, "dispositionRationale")
	}
	return result
}

func (a *Application) applyFindingDecisionPatch(ctx context.Context, tx pgx.Tx, workspace, id, actor, action string,
	patch findingDecisionPatch) (storedFindingDecision, error) {
	var before storedFindingDecision
	err := tx.QueryRow(ctx, `SELECT f.owner_id,f.workflow_state,f.disposition,f.accepted_risk_expires_at,
		CASE WHEN f.disposition='none' THEN '' ELSE COALESCE(approval.scope_kind,'finding') END,
		CASE WHEN f.disposition='suppressed' THEN approval.expires_at END,
		COALESCE(approval.rationale,''),f.id,f.asset_id,f.source_id,f.scope_id,f.scope_revision,f.scope_branch,
		f.decision_revision
		FROM `+a.table("findings")+` f
		LEFT JOIN LATERAL (
			SELECT scope_kind,expires_at,rationale FROM `+a.table("finding_disposition_approvals")+` a
			WHERE a.workspace_id=f.workspace_id AND a.finding_id=f.id
			AND a.disposition=f.disposition AND a.decision_revision<=f.decision_revision
			ORDER BY a.decision_revision DESC LIMIT 1
		) approval ON true
		WHERE f.workspace_id=$1 AND f.id=$2 AND `+a.workVisible()+` FOR UPDATE OF f`,
		workspace, id).Scan(&before.OwnerID, &before.WorkflowState, &before.Disposition,
		&before.AcceptedRiskExpiresAt, &before.DispositionScope, &before.SuppressionExpiresAt,
		&before.DispositionRationale, &before.FindingID, &before.AssetID, &before.SourceID,
		&before.ScopeID, &before.ScopeRevision, &before.ScopeBranch, &before.Revision)
	if err != nil {
		return storedFindingDecision{}, err
	}
	if patch.ExpectedRevision != nil && before.Revision != *patch.ExpectedRevision {
		return storedFindingDecision{}, errConflict
	}
	after := before
	if patch.OwnerID.Set {
		after.OwnerID = patch.OwnerID.Value
	}
	if err = a.checkOwner(ctx, tx, workspace, after.OwnerID); err != nil {
		return storedFindingDecision{}, err
	}
	if patch.WorkflowState.Set {
		if patch.WorkflowState.Value == nil {
			return storedFindingDecision{}, errInvalid
		}
		after.WorkflowState = *patch.WorkflowState.Value
	}
	if !validWorkflowState(after.WorkflowState) {
		return storedFindingDecision{}, errInvalid
	}
	dispositionTouched := patch.Disposition.Set || patch.AcceptedRiskExpiresAt.Set ||
		patch.DispositionScope.Set || patch.SuppressionExpiresAt.Set
	dispositionChanged := false
	if patch.Disposition.Set {
		if patch.Disposition.Value == nil {
			return storedFindingDecision{}, errInvalid
		}
		dispositionChanged = after.Disposition != *patch.Disposition.Value
		after.Disposition = *patch.Disposition.Value
	}
	if patch.AcceptedRiskExpiresAt.Set {
		after.AcceptedRiskExpiresAt = patch.AcceptedRiskExpiresAt.Value
	}
	if patch.DispositionScope.Set {
		if patch.DispositionScope.Value == nil {
			return storedFindingDecision{}, errInvalid
		}
		after.DispositionScope = *patch.DispositionScope.Value
	}
	if patch.SuppressionExpiresAt.Set {
		after.SuppressionExpiresAt = patch.SuppressionExpiresAt.Value
	}
	if dispositionChanged {
		switch after.Disposition {
		case "none":
			after.AcceptedRiskExpiresAt, after.SuppressionExpiresAt = nil, nil
			after.DispositionScope, after.DispositionRationale = "", ""
		case "accepted-risk":
			after.SuppressionExpiresAt = nil
			if !patch.DispositionScope.Set {
				after.DispositionScope = "finding"
			}
		case "suppressed":
			after.AcceptedRiskExpiresAt = nil
			if !patch.DispositionScope.Set {
				return storedFindingDecision{}, errInvalid
			}
		case "false-positive":
			after.AcceptedRiskExpiresAt, after.SuppressionExpiresAt = nil, nil
			if !patch.DispositionScope.Set {
				after.DispositionScope = "finding"
			}
		}
	}
	if !validDisposition(after.Disposition) {
		return storedFindingDecision{}, errInvalid
	}
	if dispositionTouched {
		if !validText(patch.Rationale, 8192) {
			return storedFindingDecision{}, errInvalid
		}
		if after.Disposition == "none" {
			after.DispositionScope, after.DispositionRationale = "", ""
			after.AcceptedRiskExpiresAt, after.SuppressionExpiresAt = nil, nil
		} else {
			after.DispositionRationale = patch.Rationale
		}
	} else if patch.Rationale != "" && !validText(patch.Rationale, 8192) {
		return storedFindingDecision{}, errInvalid
	}
	switch after.Disposition {
	case "none":
		if after.AcceptedRiskExpiresAt != nil || after.SuppressionExpiresAt != nil ||
			after.DispositionScope != "" || after.DispositionRationale != "" {
			return storedFindingDecision{}, errInvalid
		}
	case "accepted-risk":
		if after.DispositionScope != "finding" || after.SuppressionExpiresAt != nil ||
			after.AcceptedRiskExpiresAt != nil && after.AcceptedRiskExpiresAt.IsZero() {
			return storedFindingDecision{}, errInvalid
		}
	case "suppressed":
		if !validDispositionScope(after.DispositionScope) || after.AcceptedRiskExpiresAt != nil ||
			after.SuppressionExpiresAt == nil || after.SuppressionExpiresAt.IsZero() ||
			!after.SuppressionExpiresAt.After(a.config.Now()) {
			return storedFindingDecision{}, errInvalid
		}
	case "false-positive":
		if after.DispositionScope != "finding" ||
			after.AcceptedRiskExpiresAt != nil || after.SuppressionExpiresAt != nil {
			return storedFindingDecision{}, errInvalid
		}
	}
	changes := decisionChanges(before.FindingDecision, after.FindingDecision)
	if patch.RejectNoChange && len(changes) == 0 {
		return storedFindingDecision{}, errConflict
	}
	after.Revision = before.Revision + 1
	if dispositionTouched && after.Disposition != "none" {
		if err = a.insertDispositionApproval(ctx, tx, workspace, actor, after, patch.Rationale); err != nil {
			return storedFindingDecision{}, err
		}
	}
	if _, err = tx.Exec(ctx, `UPDATE `+a.table("findings")+`
		SET owner_id=$3,workflow_state=$4,disposition=$5,accepted_risk_expires_at=$6,decision_revision=$7
		WHERE workspace_id=$1 AND id=$2`, workspace, id, after.OwnerID, after.WorkflowState,
		after.Disposition, after.AcceptedRiskExpiresAt, after.Revision); err != nil {
		return storedFindingDecision{}, err
	}
	beforeJSON, err := json.Marshal(before.FindingDecision)
	if err != nil {
		return storedFindingDecision{}, err
	}
	afterJSON, err := json.Marshal(after.FindingDecision)
	if err != nil {
		return storedFindingDecision{}, err
	}
	changedJSON, err := json.Marshal(changes)
	if err != nil {
		return storedFindingDecision{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO `+a.table("finding_decision_events")+`
		(id,workspace_id,finding_id,decision_revision,actor_id,action,rationale,changed_fields,
		 before_state,after_state,created_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8::jsonb,$9::jsonb,$10::jsonb,$11)`,
		newID(), workspace, id, after.Revision, actor, action, patch.Rationale,
		string(changedJSON), string(beforeJSON), string(afterJSON), a.config.Now().UTC()); err != nil {
		return storedFindingDecision{}, err
	}
	return after, nil
}

func (a *Application) bulkPatchFindings(w http.ResponseWriter, r *http.Request, workspace, actor string) error {
	var input struct {
		FindingIDs            []string               `json:"findingIds"`
		OwnerID               optional[string]       `json:"ownerId"`
		WorkflowState         optional[string]       `json:"workflowState"`
		DecisionRevisions     map[string]json.Number `json:"decisionRevisions"`
		Disposition           optional[string]       `json:"disposition"`
		AcceptedRiskExpiresAt optional[time.Time]    `json:"acceptedRiskExpiresAt"`
		Rationale             string                 `json:"rationale"`
	}
	if err := a.decode(w, r, &input, 64<<10); err != nil {
		return err
	}
	riskMode := input.Disposition.Set || input.AcceptedRiskExpiresAt.Set || input.DecisionRevisions != nil
	if len(input.FindingIDs) == 0 || len(input.FindingIDs) > maxBulkFindings ||
		!validText(input.Rationale, 8192) ||
		riskMode && (input.OwnerID.Set || input.WorkflowState.Set ||
			!input.Disposition.Set || input.Disposition.Value == nil ||
			*input.Disposition.Value != "accepted-risk" ||
			!input.AcceptedRiskExpiresAt.Set || input.DecisionRevisions == nil) ||
		!riskMode && (!input.OwnerID.Set && !input.WorkflowState.Set ||
			input.Disposition.Set || input.AcceptedRiskExpiresAt.Set || input.DecisionRevisions != nil) {
		return errInvalid
	}
	ids := append([]string(nil), input.FindingIDs...)
	sort.Strings(ids)
	for index, id := range ids {
		if !validID(id) || (index > 0 && ids[index-1] == id) {
			return errInvalid
		}
	}
	revisions := map[string]int64{}
	if riskMode {
		if len(input.DecisionRevisions) != len(ids) ||
			input.AcceptedRiskExpiresAt.Value != nil &&
				!input.AcceptedRiskExpiresAt.Value.After(a.config.Now()) {
			return errInvalid
		}
		for _, id := range ids {
			value, present := input.DecisionRevisions[id]
			if !present {
				return errInvalid
			}
			revision, parseErr := strconv.ParseInt(string(value), 10, 64)
			if parseErr != nil || revision < 1 || revision > maxJSONSafeInt {
				return errInvalid
			}
			revisions[id] = revision
		}
		for id := range input.DecisionRevisions {
			if _, present := revisions[id]; !present {
				return errInvalid
			}
		}
	}
	tx, err := a.pool.Begin(r.Context())
	if err != nil {
		return err
	}
	defer rollback(tx)
	for _, id := range ids {
		patch := findingDecisionPatch{
			OwnerID: input.OwnerID, WorkflowState: input.WorkflowState, Rationale: input.Rationale,
		}
		if riskMode {
			scope := "finding"
			expected := revisions[id]
			patch.Disposition = input.Disposition
			patch.AcceptedRiskExpiresAt = input.AcceptedRiskExpiresAt
			patch.DispositionScope = optional[string]{Set: true, Value: &scope}
			patch.ExpectedRevision = &expected
			patch.RejectNoChange = true
		}
		if _, err = a.applyFindingDecisionPatch(r.Context(), tx, workspace, id, actor, "bulk-update", patch); err != nil {
			return err
		}
	}
	rows, err := tx.Query(r.Context(), `SELECT `+workColumns+a.workFrom()+`
		WHERE f.workspace_id=$1 AND f.id=ANY($2) AND `+a.workVisible()+` ORDER BY f.id`, workspace, ids)
	if err != nil {
		return err
	}
	items := make([]WorkItem, 0, len(ids))
	for rows.Next() {
		item, scanErr := scanWork(rows, a.config.Now())
		if scanErr != nil {
			rows.Close()
			return scanErr
		}
		items = append(items, item)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(items) != len(ids) {
		return pgx.ErrNoRows
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"dataOrigin": "live", "items": items, "total": len(items), "nextCursor": nil,
	})
	return nil
}

func decisionCursor(value string) (int64, error) {
	if value == "" {
		return 0, nil
	}
	if len(value) != 20 || strings.Trim(value, "0123456789") != "" {
		return 0, errInvalid
	}
	revision, err := strconv.ParseInt(value, 10, 64)
	if err != nil || revision < 0 {
		return 0, errInvalid
	}
	return revision, nil
}

func formatDecisionCursor(revision int64) string {
	return fmt.Sprintf("%020d", revision)
}

func (a *Application) readFindingDecisionEvents(ctx context.Context, workspace, findingID string,
	cursor int64) ([]FindingDecisionEvent, *string, error) {
	rows, err := a.pool.Query(ctx, `SELECT e.id,e.decision_revision,e.actor_id,u.name,
		before_owner.name,after_owner.name,e.action,e.rationale,e.changed_fields,e.before_state,e.after_state,e.created_at
		FROM `+a.table("finding_decision_events")+` e
		JOIN `+a.table("users")+` u ON u.id=e.actor_id
		LEFT JOIN `+a.table("users")+` before_owner ON before_owner.id=e.before_state->>'ownerId'
		LEFT JOIN `+a.table("users")+` after_owner ON after_owner.id=e.after_state->>'ownerId'
		WHERE e.workspace_id=$1 AND e.finding_id=$2 AND e.decision_revision>$3
		ORDER BY e.decision_revision LIMIT $4`, workspace, findingID, cursor, decisionPageSize+1)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()
	events := make([]FindingDecisionEvent, 0, decisionPageSize+1)
	for rows.Next() {
		var event FindingDecisionEvent
		var changed, before, after []byte
		if err = rows.Scan(&event.ID, &event.DecisionRevision, &event.ActorID, &event.ActorName,
			&event.BeforeOwnerName, &event.AfterOwnerName, &event.Action, &event.Rationale,
			&changed, &before, &after, &event.CreatedAt); err != nil {
			return nil, nil, err
		}
		if json.Unmarshal(changed, &event.ChangedFields) != nil ||
			json.Unmarshal(before, &event.Before) != nil || json.Unmarshal(after, &event.After) != nil {
			return nil, nil, errInvalid
		}
		if event.ChangedFields == nil {
			event.ChangedFields = []string{}
		}
		events = append(events, event)
	}
	if err = rows.Err(); err != nil {
		return nil, nil, err
	}
	var next *string
	if len(events) > decisionPageSize {
		events = events[:decisionPageSize]
		value := formatDecisionCursor(events[decisionPageSize-1].DecisionRevision)
		next = &value
	}
	return events, next, nil
}
