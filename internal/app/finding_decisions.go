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
)

type findingDecisionPatch struct {
	OwnerID               optional[string]    `json:"ownerId"`
	WorkflowState         optional[string]    `json:"workflowState"`
	Disposition           optional[string]    `json:"disposition"`
	AcceptedRiskExpiresAt optional[time.Time] `json:"acceptedRiskExpiresAt"`
	Rationale             string              `json:"rationale"`
}

type storedFindingDecision struct {
	FindingDecision
	Revision int64
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
	result := make([]string, 0, 4)
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
	return result
}

func (a *Application) applyFindingDecisionPatch(ctx context.Context, tx pgx.Tx, workspace, id, actor, action string,
	patch findingDecisionPatch) (storedFindingDecision, error) {
	var before storedFindingDecision
	err := tx.QueryRow(ctx, `SELECT owner_id,workflow_state,disposition,accepted_risk_expires_at,decision_revision
		FROM `+a.table("findings")+` f WHERE workspace_id=$1 AND id=$2 AND `+a.workVisible()+` FOR UPDATE`,
		workspace, id).Scan(&before.OwnerID, &before.WorkflowState, &before.Disposition,
		&before.AcceptedRiskExpiresAt, &before.Revision)
	if err != nil {
		return storedFindingDecision{}, err
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
	if patch.Disposition.Set {
		if patch.Disposition.Value == nil {
			return storedFindingDecision{}, errInvalid
		}
		after.Disposition = *patch.Disposition.Value
		if after.Disposition == "none" && !patch.AcceptedRiskExpiresAt.Set {
			after.AcceptedRiskExpiresAt = nil
		}
	}
	if after.Disposition != "none" && after.Disposition != "accepted-risk" {
		return storedFindingDecision{}, errInvalid
	}
	if patch.AcceptedRiskExpiresAt.Set {
		after.AcceptedRiskExpiresAt = patch.AcceptedRiskExpiresAt.Value
	}
	if after.AcceptedRiskExpiresAt != nil &&
		(after.AcceptedRiskExpiresAt.IsZero() || after.Disposition != "accepted-risk") {
		return storedFindingDecision{}, errInvalid
	}
	if patch.Rationale != "" && !validText(patch.Rationale, 8192) {
		return storedFindingDecision{}, errInvalid
	}
	after.Revision = before.Revision + 1
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
	changedJSON, err := json.Marshal(decisionChanges(before.FindingDecision, after.FindingDecision))
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
		FindingIDs    []string         `json:"findingIds"`
		OwnerID       optional[string] `json:"ownerId"`
		WorkflowState optional[string] `json:"workflowState"`
		Rationale     string           `json:"rationale"`
	}
	if err := a.decode(w, r, &input, 64<<10); err != nil {
		return err
	}
	if len(input.FindingIDs) == 0 || len(input.FindingIDs) > maxBulkFindings ||
		(!input.OwnerID.Set && !input.WorkflowState.Set) || !validText(input.Rationale, 8192) {
		return errInvalid
	}
	ids := append([]string(nil), input.FindingIDs...)
	sort.Strings(ids)
	for index, id := range ids {
		if !validID(id) || (index > 0 && ids[index-1] == id) {
			return errInvalid
		}
	}
	patch := findingDecisionPatch{
		OwnerID: input.OwnerID, WorkflowState: input.WorkflowState, Rationale: input.Rationale,
	}
	tx, err := a.pool.Begin(r.Context())
	if err != nil {
		return err
	}
	defer rollback(tx)
	for _, id := range ids {
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
		item, scanErr := scanWork(rows)
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
