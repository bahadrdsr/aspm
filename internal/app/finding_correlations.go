package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

type correlationDB interface {
	queryRower
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

type correlationFinding struct {
	FindingCorrelationMember
	AssetID       string
	ScopeBranch   string
	CandidateURI  string
	CandidateLine int
}

type findingDecisionInput struct {
	OwnerID               optional[string]    `json:"ownerId"`
	WorkflowState         string              `json:"workflowState"`
	Disposition           string              `json:"disposition"`
	AcceptedRiskExpiresAt optional[time.Time] `json:"acceptedRiskExpiresAt"`
}

func (input findingDecisionInput) decision() (FindingDecision, bool) {
	if !input.OwnerID.Set || !input.AcceptedRiskExpiresAt.Set ||
		(input.WorkflowState != "open" && input.WorkflowState != "in-progress" && input.WorkflowState != "resolved") ||
		(input.Disposition != "none" && input.Disposition != "accepted-risk") {
		return FindingDecision{}, false
	}
	decision := FindingDecision{
		OwnerID: input.OwnerID.Value, WorkflowState: input.WorkflowState,
		Disposition: input.Disposition, AcceptedRiskExpiresAt: input.AcceptedRiskExpiresAt.Value,
	}
	if decision.AcceptedRiskExpiresAt != nil &&
		(decision.AcceptedRiskExpiresAt.IsZero() || decision.Disposition != "accepted-risk") {
		return FindingDecision{}, false
	}
	if decision.Disposition == "none" && decision.AcceptedRiskExpiresAt != nil {
		return FindingDecision{}, false
	}
	return decision, true
}

func correlationDigest(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	return sum[:], nil
}

func sameDecision(left, right FindingDecision) bool {
	if (left.OwnerID == nil) != (right.OwnerID == nil) ||
		left.OwnerID != nil && *left.OwnerID != *right.OwnerID ||
		left.WorkflowState != right.WorkflowState || left.Disposition != right.Disposition ||
		(left.AcceptedRiskExpiresAt == nil) != (right.AcceptedRiskExpiresAt == nil) {
		return false
	}
	return left.AcceptedRiskExpiresAt == nil || left.AcceptedRiskExpiresAt.Equal(*right.AcceptedRiskExpiresAt)
}

func correlationConflicts(left, right FindingDecision) []string {
	result := []string{}
	if (left.OwnerID == nil) != (right.OwnerID == nil) ||
		left.OwnerID != nil && *left.OwnerID != *right.OwnerID {
		result = append(result, "ownerId")
	}
	if left.WorkflowState != right.WorkflowState {
		result = append(result, "workflowState")
	}
	if left.Disposition != right.Disposition {
		result = append(result, "disposition")
	}
	if !sameDecision(FindingDecision{AcceptedRiskExpiresAt: left.AcceptedRiskExpiresAt},
		FindingDecision{AcceptedRiskExpiresAt: right.AcceptedRiskExpiresAt}) {
		result = append(result, "acceptedRiskExpiresAt")
	}
	return result
}

func scanCorrelationFinding(row pgx.Row) (correlationFinding, error) {
	var record correlationFinding
	err := row.Scan(&record.FindingID, &record.AssetID, &record.SourceID, &record.ScopeBranch,
		&record.CandidateURI, &record.CandidateLine, &record.Title, &record.Severity,
		&record.Decision.OwnerID, &record.Decision.WorkflowState,
		&record.Decision.Disposition, &record.Decision.AcceptedRiskExpiresAt,
		&record.DecisionRevision, &record.EvidenceRevision, &record.ObservationCount, &record.NoteCount)
	record.OriginalDecision = record.Decision
	record.Active = true
	return record, err
}

func (a *Application) readCorrelationFinding(ctx context.Context, db queryRower, workspace, id string, lock bool) (correlationFinding, error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE OF f"
	}
	return scanCorrelationFinding(db.QueryRow(ctx, `SELECT f.id,f.asset_id,f.source_id,f.scope_branch,
		f.candidate_uri,f.candidate_line,f.title,f.severity,
		f.owner_id,f.workflow_state,f.disposition,f.accepted_risk_expires_at,
		f.decision_revision,f.evidence_revision,
		(SELECT count(*) FROM `+a.table("observations")+` o WHERE o.workspace_id=f.workspace_id AND o.finding_id=f.id),
		(SELECT count(*) FROM `+a.table("notes")+` n WHERE n.workspace_id=f.workspace_id AND n.finding_id=f.id)
		FROM `+a.table("findings")+` f WHERE f.workspace_id=$1 AND f.id=$2`+suffix,
		workspace, id))
}

func (a *Application) activeCorrelationMeta(ctx context.Context, db queryRower,
	workspace, finding string) (string, string, int64, error) {
	var id, primary string
	var revision int64
	err := db.QueryRow(ctx, `SELECT c.id,c.primary_finding_id,c.revision
		FROM `+a.table("finding_correlation_members")+` m
		JOIN `+a.table("finding_correlations")+` c
		ON c.workspace_id=m.workspace_id AND c.id=m.correlation_id AND c.state='active'
		WHERE m.workspace_id=$1 AND m.finding_id=$2 AND m.released_at IS NULL`,
		workspace, finding).Scan(&id, &primary, &revision)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", 0, nil
	}
	return id, primary, revision, err
}

func (a *Application) activeCorrelationID(ctx context.Context, db queryRower, workspace, finding string) (string, error) {
	id, _, _, err := a.activeCorrelationMeta(ctx, db, workspace, finding)
	return id, err
}

func (a *Application) correlationPreview(ctx context.Context, db correlationDB, workspace, primaryID, otherID string) (FindingMergePreview, error) {
	var preview FindingMergePreview
	if primaryID == otherID {
		return preview, errInvalid
	}
	primary, err := a.readCorrelationFinding(ctx, db, workspace, primaryID, false)
	if err != nil {
		return preview, err
	}
	other, err := a.readCorrelationFinding(ctx, db, workspace, otherID, false)
	if err != nil {
		return preview, err
	}
	if primary.AssetID != other.AssetID || primary.SourceID == other.SourceID {
		return preview, errInvalid
	}
	primaryGroup, groupPrimary, _, err := a.activeCorrelationMeta(ctx, db, workspace, primaryID)
	if err != nil {
		return preview, err
	}
	otherGroup, _, _, err := a.activeCorrelationMeta(ctx, db, workspace, otherID)
	if err != nil {
		return preview, err
	}
	if otherGroup != "" || primaryGroup != "" && groupPrimary != primaryID {
		return preview, errConflict
	}
	if primaryGroup != "" {
		correlation, err := a.loadCorrelation(ctx, db, workspace, primaryGroup)
		if err != nil {
			return preview, err
		}
		for _, member := range correlation.Members {
			if member.FindingID == otherID || member.Active && member.SourceID == other.SourceID {
				return preview, errConflict
			}
		}
		preview.Correlation = &correlation
	}
	preview.Primary, preview.Other = primary.FindingCorrelationMember, other.FindingCorrelationMember
	preview.Conflicts = correlationConflicts(primary.Decision, other.Decision)
	return preview, nil
}

func (a *Application) listCorrelationCandidates(w http.ResponseWriter, r *http.Request,
	workspace, primaryID string) error {
	limit, cursor, err := pageParameters(r)
	if err != nil {
		return err
	}
	if limit > 100 {
		return errInvalid
	}
	primary, err := a.readCorrelationFinding(r.Context(), a.pool, workspace, primaryID, false)
	if err != nil {
		return err
	}
	groupID, groupPrimary, _, err := a.activeCorrelationMeta(r.Context(), a.pool, workspace, primaryID)
	if err != nil {
		return err
	}
	if groupID != "" && groupPrimary != primaryID {
		return errConflict
	}
	items := []FindingCorrelationCandidate{}
	if primary.CandidateURI == "" || primary.CandidateLine == 0 {
		writeJSON(w, http.StatusOK, map[string]any{
			"correlationCandidates": map[string]any{"items": items, "nextCursor": nil},
		})
		return nil
	}
	rows, err := a.pool.Query(r.Context(), `SELECT f.id,f.asset_id,f.source_id,f.scope_branch,f.candidate_uri,f.candidate_line,
		f.title,f.severity,f.owner_id,f.workflow_state,f.disposition,f.accepted_risk_expires_at,
		f.decision_revision,f.evidence_revision,
		(SELECT count(*) FROM `+a.table("observations")+` o
		 WHERE o.workspace_id=f.workspace_id AND o.finding_id=f.id),
		(SELECT count(*) FROM `+a.table("notes")+` n
		 WHERE n.workspace_id=f.workspace_id AND n.finding_id=f.id)
		FROM `+a.table("findings")+` f
		WHERE f.workspace_id=$1 AND f.asset_id=$2 AND f.scope_branch=$3
		AND f.candidate_uri=$4 AND f.candidate_line=$5
		AND f.id<>$6 AND f.id>$7 AND f.source_id<>$8
		AND NOT EXISTS(SELECT 1 FROM `+a.table("finding_correlation_members")+` active_member
		 JOIN `+a.table("finding_correlations")+` active_group
		 ON active_group.workspace_id=active_member.workspace_id
		  AND active_group.id=active_member.correlation_id AND active_group.state='active'
		 WHERE active_member.workspace_id=f.workspace_id AND active_member.finding_id=f.id
		  AND active_member.released_at IS NULL)
		AND ($9='' OR NOT EXISTS(SELECT 1 FROM `+a.table("finding_correlation_members")+` prior_member
		 WHERE prior_member.workspace_id=f.workspace_id AND prior_member.correlation_id=$9
		  AND prior_member.finding_id=f.id))
		AND ($9='' OR NOT EXISTS(SELECT 1 FROM `+a.table("finding_correlation_members")+` group_member
		 JOIN `+a.table("findings")+` grouped
		 ON grouped.workspace_id=group_member.workspace_id AND grouped.id=group_member.finding_id
		 WHERE group_member.workspace_id=f.workspace_id AND group_member.correlation_id=$9
		  AND group_member.released_at IS NULL AND grouped.source_id=f.source_id))
		ORDER BY f.id LIMIT $10`,
		workspace, primary.AssetID, primary.ScopeBranch, primary.CandidateURI, primary.CandidateLine,
		primaryID, cursor, primary.SourceID, groupID, limit+1)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		record, err := scanCorrelationFinding(rows)
		if err != nil {
			return err
		}
		items = append(items, FindingCorrelationCandidate{
			Member: record.FindingCorrelationMember,
			Match: FindingCorrelationMatch{
				Kind: "exact-location", Branch: record.ScopeBranch,
				URI: record.CandidateURI, Line: record.CandidateLine,
			},
		})
	}
	if err = rows.Err(); err != nil {
		return err
	}
	var next *string
	if len(items) > limit {
		value := items[limit-1].Member.FindingID
		next = &value
		items = items[:limit]
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"correlationCandidates": map[string]any{"items": items, "nextCursor": next},
	})
	return nil
}

func (a *Application) previewFindingMerge(w http.ResponseWriter, r *http.Request, workspace, primary string) error {
	var input struct {
		OtherFindingID string `json:"otherFindingId"`
	}
	if err := a.decode(w, r, &input, 16<<10); err != nil {
		return err
	}
	if !validID(input.OtherFindingID) {
		return errInvalid
	}
	preview, err := a.correlationPreview(r.Context(), a.pool, workspace, primary, input.OtherFindingID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"mergePreview": preview})
	return nil
}

func (a *Application) lockCorrelationFindings(ctx context.Context, tx pgx.Tx, workspace, primaryID, otherID string) (correlationFinding, correlationFinding, error) {
	ids := []string{primaryID, otherID}
	slices.Sort(ids)
	first, err := a.readCorrelationFinding(ctx, tx, workspace, ids[0], true)
	if err != nil {
		return correlationFinding{}, correlationFinding{}, err
	}
	second, err := a.readCorrelationFinding(ctx, tx, workspace, ids[1], true)
	if err != nil {
		return correlationFinding{}, correlationFinding{}, err
	}
	if first.FindingID == primaryID {
		return first, second, nil
	}
	return second, first, nil
}

func (a *Application) applyFindingDecision(ctx context.Context, tx pgx.Tx, workspace string,
	record correlationFinding, decision FindingDecision) error {
	if err := a.checkOwner(ctx, tx, workspace, decision.OwnerID); err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `UPDATE `+a.table("findings")+`
		SET owner_id=$4,workflow_state=$5,disposition=$6,accepted_risk_expires_at=$7,
			decision_revision=decision_revision+1
		WHERE workspace_id=$1 AND id=$2 AND decision_revision=$3 AND evidence_revision=$8`,
		workspace, record.FindingID, record.DecisionRevision, decision.OwnerID,
		decision.WorkflowState, decision.Disposition, decision.AcceptedRiskExpiresAt, record.EvidenceRevision)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return errConflict
	}
	return nil
}

func (a *Application) correlationReplay(ctx context.Context, db queryRower, workspace, key, eventType string,
	digest []byte) (string, bool, error) {
	var correlation, storedType string
	var stored []byte
	err := db.QueryRow(ctx, `SELECT correlation_id,event_type,binding_digest
		FROM `+a.table("finding_correlation_events")+` WHERE workspace_id=$1 AND idempotency_key=$2`,
		workspace, key).Scan(&correlation, &storedType, &stored)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if storedType != eventType || !bytes.Equal(stored, digest) {
		return "", false, errConflict
	}
	return correlation, true, nil
}

func (a *Application) loadCorrelation(ctx context.Context, db correlationDB, workspace, id string) (FindingCorrelation, error) {
	var correlation FindingCorrelation
	err := db.QueryRow(ctx, `SELECT id,workspace_id,primary_finding_id,state,revision
		FROM `+a.table("finding_correlations")+` WHERE workspace_id=$1 AND id=$2`,
		workspace, id).Scan(&correlation.ID, &correlation.WorkspaceID, &correlation.PrimaryFindingID,
		&correlation.State, &correlation.Revision)
	if err != nil {
		return correlation, err
	}
	rows, err := db.Query(ctx, `SELECT f.id,f.source_id,f.title,f.severity,
		f.owner_id,f.workflow_state,f.disposition,f.accepted_risk_expires_at,
		f.decision_revision,f.evidence_revision,
		(SELECT count(*) FROM `+a.table("observations")+` o WHERE o.workspace_id=f.workspace_id AND o.finding_id=f.id),
		(SELECT count(*) FROM `+a.table("notes")+` n WHERE n.workspace_id=f.workspace_id AND n.finding_id=f.id),
		m.original_decision,m.released_at IS NULL
		FROM `+a.table("finding_correlation_members")+` m
		JOIN `+a.table("findings")+` f ON f.workspace_id=m.workspace_id AND f.id=m.finding_id
		WHERE m.workspace_id=$1 AND m.correlation_id=$2 ORDER BY m.ordinal`,
		workspace, id)
	if err != nil {
		return correlation, err
	}
	for rows.Next() {
		var member FindingCorrelationMember
		var original []byte
		if err = rows.Scan(&member.FindingID, &member.SourceID, &member.Title, &member.Severity,
			&member.Decision.OwnerID, &member.Decision.WorkflowState, &member.Decision.Disposition,
			&member.Decision.AcceptedRiskExpiresAt, &member.DecisionRevision, &member.EvidenceRevision,
			&member.ObservationCount, &member.NoteCount, &original, &member.Active); err != nil {
			rows.Close()
			return correlation, err
		}
		if err = json.Unmarshal(original, &member.OriginalDecision); err != nil {
			rows.Close()
			return correlation, err
		}
		correlation.Members = append(correlation.Members, member)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return correlation, err
	}
	rows, err = db.Query(ctx, `SELECT id,event_type,actor_id,rationale,created_at,detail_availability
		FROM `+a.table("finding_correlation_events")+`
		WHERE workspace_id=$1 AND correlation_id=$2 ORDER BY sequence`, workspace, id)
	if err != nil {
		return correlation, err
	}
	defer rows.Close()
	for rows.Next() {
		var event FindingCorrelationEvent
		if err = rows.Scan(&event.ID, &event.Type, &event.ActorID, &event.Rationale, &event.CreatedAt,
			&event.DetailAvailability); err != nil {
			return correlation, err
		}
		correlation.Events = append(correlation.Events, event)
	}
	return correlation, rows.Err()
}

func (a *Application) activeCorrelationForPrimary(ctx context.Context, db correlationDB,
	workspace, finding string) (*FindingCorrelation, error) {
	var id string
	err := db.QueryRow(ctx, `SELECT id FROM `+a.table("finding_correlations")+`
		WHERE workspace_id=$1 AND primary_finding_id=$2 AND state='active'`, workspace, finding).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	correlation, err := a.loadCorrelation(ctx, db, workspace, id)
	return &correlation, err
}

func correlationState(primary, other correlationFinding, decision FindingDecision) map[string]any {
	return map[string]any{
		"primaryFindingId": primary.FindingID,
		"otherFindingId":   other.FindingID,
		"primaryDecision":  primary.Decision,
		"otherDecision":    other.Decision,
		"selectedDecision": decision,
	}
}

func correlationActiveMemberIDs(correlation FindingCorrelation) []string {
	ids := []string{}
	for _, member := range correlation.Members {
		if member.Active {
			ids = append(ids, member.FindingID)
		}
	}
	return ids
}

func (a *Application) mergeFindings(w http.ResponseWriter, r *http.Request, workspace string,
	session authenticatedSession, primaryID string) error {
	var input struct {
		OtherFindingID          string               `json:"otherFindingId"`
		CorrelationRevision     int64                `json:"correlationRevision"`
		PrimaryDecisionRevision int64                `json:"primaryDecisionRevision"`
		PrimaryEvidenceRevision int64                `json:"primaryEvidenceRevision"`
		OtherDecisionRevision   int64                `json:"otherDecisionRevision"`
		OtherEvidenceRevision   int64                `json:"otherEvidenceRevision"`
		Decision                findingDecisionInput `json:"decision"`
		Rationale               string               `json:"rationale"`
		IdempotencyKey          string               `json:"idempotencyKey"`
	}
	if err := a.decode(w, r, &input, 32<<10); err != nil {
		return err
	}
	decision, valid := input.Decision.decision()
	if !valid || !validID(input.OtherFindingID) || input.OtherFindingID == primaryID ||
		input.CorrelationRevision < 0 ||
		input.PrimaryDecisionRevision < 1 || input.PrimaryEvidenceRevision < 1 ||
		input.OtherDecisionRevision < 1 || input.OtherEvidenceRevision < 1 ||
		!validText(input.Rationale, 8192) || !validText(input.IdempotencyKey, 256) {
		return errInvalid
	}
	binding := struct {
		Workspace, Actor, Primary, Other, Rationale, Key      string
		CorrelationRevision, PrimaryDecision, PrimaryEvidence int64
		OtherDecision, OtherEvidence                          int64
		Decision                                              FindingDecision
	}{workspace, session.User.ID, primaryID, input.OtherFindingID, input.Rationale, input.IdempotencyKey,
		input.CorrelationRevision, input.PrimaryDecisionRevision, input.PrimaryEvidenceRevision,
		input.OtherDecisionRevision, input.OtherEvidenceRevision, decision}
	digest, err := correlationDigest(binding)
	if err != nil {
		return err
	}
	tx, err := a.pool.Begin(r.Context())
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`,
		"aspm/finding-correlation/"+workspace+"/"+input.IdempotencyKey); err != nil {
		return err
	}
	if correlationID, replay, err := a.correlationReplay(r.Context(), tx, workspace,
		input.IdempotencyKey, "merge", digest); err != nil {
		return err
	} else if replay {
		correlation, err := a.loadCorrelation(r.Context(), tx, workspace, correlationID)
		if err != nil {
			return err
		}
		if err = tx.Commit(r.Context()); err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, map[string]any{"correlation": correlation})
		return nil
	}
	role, err := a.sessionRole(r.Context(), tx, session, workspace)
	if err != nil {
		return err
	}
	if !canWrite(Workspace{Role: role}) {
		return errForbidden
	}
	primary, other, err := a.lockCorrelationFindings(r.Context(), tx, workspace, primaryID, input.OtherFindingID)
	if err != nil {
		return err
	}
	if primary.AssetID != other.AssetID || primary.SourceID == other.SourceID ||
		primary.DecisionRevision != input.PrimaryDecisionRevision ||
		primary.EvidenceRevision != input.PrimaryEvidenceRevision ||
		other.DecisionRevision != input.OtherDecisionRevision ||
		other.EvidenceRevision != input.OtherEvidenceRevision {
		return errConflict
	}
	primaryGroup, groupPrimary, _, err := a.activeCorrelationMeta(r.Context(), tx, workspace, primaryID)
	if err != nil {
		return err
	}
	otherGroup, _, _, err := a.activeCorrelationMeta(r.Context(), tx, workspace, input.OtherFindingID)
	if err != nil {
		return err
	}
	if otherGroup != "" || primaryGroup != "" && groupPrimary != primaryID {
		return errConflict
	}
	if err = a.applyFindingDecision(r.Context(), tx, workspace, primary, decision); err != nil {
		return err
	}
	now, correlationID := a.config.Now().UTC(), primaryGroup
	sequence := int64(1)
	var beforeState any
	if correlationID == "" {
		if input.CorrelationRevision != 0 {
			return errConflict
		}
		correlationID = newID()
		if _, err = tx.Exec(r.Context(), `INSERT INTO `+a.table("finding_correlations")+`
			(id,workspace_id,primary_finding_id,state,revision,created_by,created_at,updated_at)
			VALUES($1,$2,$3,'active',1,$4,$5,$5)`,
			correlationID, workspace, primaryID, session.User.ID, now); err != nil {
			return err
		}
		for ordinal, member := range []correlationFinding{primary, other} {
			original, err := json.Marshal(member.Decision)
			if err != nil {
				return err
			}
			if _, err = tx.Exec(r.Context(), `INSERT INTO `+a.table("finding_correlation_members")+`
				(id,workspace_id,correlation_id,finding_id,ordinal,original_decision,added_at,added_by)
				VALUES($1,$2,$3,$4,$5,$6,$7,$8)`,
				newID(), workspace, correlationID, member.FindingID, ordinal, original, now, session.User.ID); err != nil {
				return err
			}
		}
		beforeState = correlationState(primary, other, decision)
	} else {
		var revision int64
		if err = tx.QueryRow(r.Context(), `SELECT revision FROM `+a.table("finding_correlations")+`
			WHERE workspace_id=$1 AND id=$2 AND state='active' FOR UPDATE`,
			workspace, correlationID).Scan(&revision); err != nil {
			return err
		}
		if input.CorrelationRevision != revision {
			return errConflict
		}
		current, err := a.loadCorrelation(r.Context(), tx, workspace, correlationID)
		if err != nil {
			return err
		}
		ordinal := 0
		for _, member := range current.Members {
			ordinal++
			if member.FindingID == other.FindingID || member.Active && member.SourceID == other.SourceID {
				return errConflict
			}
		}
		if ordinal > 63 {
			return errTooLarge
		}
		original, err := json.Marshal(other.Decision)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(r.Context(), `INSERT INTO `+a.table("finding_correlation_members")+`
			(id,workspace_id,correlation_id,finding_id,ordinal,original_decision,added_at,added_by)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8)`,
			newID(), workspace, correlationID, other.FindingID, ordinal, original, now, session.User.ID); err != nil {
			return err
		}
		result, err := tx.Exec(r.Context(), `UPDATE `+a.table("finding_correlations")+`
			SET revision=revision+1,updated_at=$4
			WHERE workspace_id=$1 AND id=$2 AND revision=$3 AND state='active'`,
			workspace, correlationID, revision, now)
		if err != nil {
			return err
		}
		if result.RowsAffected() != 1 {
			return errConflict
		}
		sequence = revision + 1
		beforeState = map[string]any{
			"state": "active", "primaryFindingId": primaryID,
			"memberFindingIds": correlationActiveMemberIDs(current),
			"addedFindingId":   other.FindingID, "selectedDecision": decision,
		}
	}
	before, err := json.Marshal(beforeState)
	if err != nil {
		return err
	}
	current, err := a.loadCorrelation(r.Context(), tx, workspace, correlationID)
	if err != nil {
		return err
	}
	after, err := json.Marshal(map[string]any{
		"state": "active", "primaryFindingId": primaryID,
		"memberFindingIds": correlationActiveMemberIDs(current), "decision": decision,
	})
	if err != nil {
		return err
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO `+a.table("finding_correlation_events")+`
		(id,workspace_id,correlation_id,sequence,event_type,actor_id,rationale,idempotency_key,
		 binding_digest,before_state,after_state,created_at)
		VALUES($1,$2,$3,$4,'merge',$5,$6,$7,$8,$9,$10,$11)`,
		newID(), workspace, correlationID, sequence, session.User.ID, input.Rationale, input.IdempotencyKey,
		digest, before, after, now); err != nil {
		return err
	}
	correlation, err := a.loadCorrelation(r.Context(), tx, workspace, correlationID)
	if err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]any{"correlation": correlation})
	return nil
}

func (a *Application) activeSplitPreview(ctx context.Context, db correlationDB,
	workspace, primaryID, memberID string) (FindingSplitPreview, error) {
	var preview FindingSplitPreview
	var correlationID string
	err := db.QueryRow(ctx, `SELECT c.id FROM `+a.table("finding_correlations")+` c
		JOIN `+a.table("finding_correlation_members")+` m
		ON m.workspace_id=c.workspace_id AND m.correlation_id=c.id AND m.released_at IS NULL
		WHERE c.workspace_id=$1 AND c.primary_finding_id=$2 AND c.state='active' AND m.finding_id=$3`,
		workspace, primaryID, memberID).Scan(&correlationID)
	if err != nil {
		return preview, err
	}
	preview.Correlation, err = a.loadCorrelation(ctx, db, workspace, correlationID)
	if err != nil {
		return preview, err
	}
	if len(correlationActiveMemberIDs(preview.Correlation)) < 2 {
		return preview, errConflict
	}
	for _, member := range preview.Correlation.Members {
		if !member.Active {
			continue
		}
		switch member.FindingID {
		case primaryID:
			preview.Primary = member
		case memberID:
			preview.Member = member
		}
	}
	if preview.Primary.FindingID == "" || preview.Member.FindingID == "" {
		return preview, errNotFound
	}
	return preview, nil
}

func (a *Application) previewFindingSplit(w http.ResponseWriter, r *http.Request, workspace, primary string) error {
	var input struct {
		MemberFindingID string `json:"memberFindingId"`
	}
	if err := a.decode(w, r, &input, 16<<10); err != nil {
		return err
	}
	if !validID(input.MemberFindingID) || input.MemberFindingID == primary {
		return errInvalid
	}
	preview, err := a.activeSplitPreview(r.Context(), a.pool, workspace, primary, input.MemberFindingID)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"splitPreview": preview})
	return nil
}

func (a *Application) splitFinding(w http.ResponseWriter, r *http.Request, workspace string,
	session authenticatedSession, primaryID string) error {
	var input struct {
		MemberFindingID         string               `json:"memberFindingId"`
		CorrelationRevision     int64                `json:"correlationRevision"`
		PrimaryDecisionRevision int64                `json:"primaryDecisionRevision"`
		PrimaryEvidenceRevision int64                `json:"primaryEvidenceRevision"`
		MemberDecisionRevision  int64                `json:"memberDecisionRevision"`
		MemberEvidenceRevision  int64                `json:"memberEvidenceRevision"`
		PrimaryDecision         findingDecisionInput `json:"primaryDecision"`
		MemberDecision          findingDecisionInput `json:"memberDecision"`
		Rationale               string               `json:"rationale"`
		IdempotencyKey          string               `json:"idempotencyKey"`
	}
	if err := a.decode(w, r, &input, 32<<10); err != nil {
		return err
	}
	primaryDecision, primaryValid := input.PrimaryDecision.decision()
	memberDecision, memberValid := input.MemberDecision.decision()
	if !primaryValid || !memberValid || !validID(input.MemberFindingID) || input.MemberFindingID == primaryID ||
		input.CorrelationRevision < 1 || input.PrimaryDecisionRevision < 1 || input.PrimaryEvidenceRevision < 1 ||
		input.MemberDecisionRevision < 1 || input.MemberEvidenceRevision < 1 ||
		!validText(input.Rationale, 8192) || !validText(input.IdempotencyKey, 256) {
		return errInvalid
	}
	binding := struct {
		Workspace, Actor, Primary, Member, Rationale, Key             string
		Correlation, PrimaryDecisionRevision, PrimaryEvidenceRevision int64
		MemberDecisionRevision, MemberEvidenceRevision                int64
		PrimaryDecision, MemberDecision                               FindingDecision
	}{workspace, session.User.ID, primaryID, input.MemberFindingID, input.Rationale, input.IdempotencyKey,
		input.CorrelationRevision, input.PrimaryDecisionRevision, input.PrimaryEvidenceRevision,
		input.MemberDecisionRevision, input.MemberEvidenceRevision, primaryDecision, memberDecision}
	digest, err := correlationDigest(binding)
	if err != nil {
		return err
	}
	tx, err := a.pool.Begin(r.Context())
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`,
		"aspm/finding-correlation/"+workspace+"/"+input.IdempotencyKey); err != nil {
		return err
	}
	if correlationID, replay, err := a.correlationReplay(r.Context(), tx, workspace,
		input.IdempotencyKey, "split", digest); err != nil {
		return err
	} else if replay {
		correlation, err := a.loadCorrelation(r.Context(), tx, workspace, correlationID)
		if err != nil {
			return err
		}
		if err = tx.Commit(r.Context()); err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, map[string]any{"correlation": correlation})
		return nil
	}
	role, err := a.sessionRole(r.Context(), tx, session, workspace)
	if err != nil {
		return err
	}
	if !canWrite(Workspace{Role: role}) {
		return errForbidden
	}
	primary, member, err := a.lockCorrelationFindings(r.Context(), tx, workspace, primaryID, input.MemberFindingID)
	if err != nil {
		return err
	}
	var correlationID string
	var revision int64
	err = tx.QueryRow(r.Context(), `SELECT id,revision FROM `+a.table("finding_correlations")+`
		WHERE workspace_id=$1 AND primary_finding_id=$2 AND state='active' FOR UPDATE`,
		workspace, primaryID).Scan(&correlationID, &revision)
	if err != nil {
		return err
	}
	if revision != input.CorrelationRevision {
		return errConflict
	}
	var members int
	if err = tx.QueryRow(r.Context(), `SELECT count(*) FROM `+a.table("finding_correlation_members")+`
		WHERE workspace_id=$1 AND correlation_id=$2 AND released_at IS NULL`,
		workspace, correlationID).Scan(&members); err != nil {
		return err
	}
	if members < 2 {
		return errConflict
	}
	var memberPresent bool
	if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM `+a.table("finding_correlation_members")+`
		WHERE workspace_id=$1 AND correlation_id=$2 AND finding_id=$3 AND released_at IS NULL)`,
		workspace, correlationID, input.MemberFindingID).Scan(&memberPresent); err != nil {
		return err
	}
	if !memberPresent {
		return errNotFound
	}
	if primary.DecisionRevision != input.PrimaryDecisionRevision ||
		primary.EvidenceRevision != input.PrimaryEvidenceRevision ||
		member.DecisionRevision != input.MemberDecisionRevision ||
		member.EvidenceRevision != input.MemberEvidenceRevision {
		return errConflict
	}
	if err = a.applyFindingDecision(r.Context(), tx, workspace, primary, primaryDecision); err != nil {
		return err
	}
	if err = a.applyFindingDecision(r.Context(), tx, workspace, member, memberDecision); err != nil {
		return err
	}
	current, err := a.loadCorrelation(r.Context(), tx, workspace, correlationID)
	if err != nil {
		return err
	}
	beforeIDs := correlationActiveMemberIDs(current)
	now := a.config.Now().UTC()
	terminal := members == 2
	var result pgconn.CommandTag
	if terminal {
		result, err = tx.Exec(r.Context(), `UPDATE `+a.table("finding_correlation_members")+`
			SET released_at=$3,released_by=$4
			WHERE workspace_id=$1 AND correlation_id=$2 AND released_at IS NULL`,
			workspace, correlationID, now, session.User.ID)
	} else {
		result, err = tx.Exec(r.Context(), `UPDATE `+a.table("finding_correlation_members")+`
			SET released_at=$4,released_by=$5
			WHERE workspace_id=$1 AND correlation_id=$2 AND finding_id=$3 AND released_at IS NULL`,
			workspace, correlationID, input.MemberFindingID, now, session.User.ID)
	}
	if err != nil {
		return err
	}
	expectedReleased := int64(1)
	if terminal {
		expectedReleased = 2
	}
	if result.RowsAffected() != expectedReleased {
		return errConflict
	}
	state := "active"
	afterIDs := slices.DeleteFunc(slices.Clone(beforeIDs), func(id string) bool {
		return id == input.MemberFindingID
	})
	if terminal {
		state, afterIDs = "split", []string{}
	}
	result, err = tx.Exec(r.Context(), `UPDATE `+a.table("finding_correlations")+`
		SET state=$5,revision=revision+1,updated_at=$4
		WHERE workspace_id=$1 AND id=$2 AND revision=$3 AND state='active'`,
		workspace, correlationID, revision, now, state)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return errConflict
	}
	before, err := json.Marshal(map[string]any{
		"state": "active", "primaryFindingId": primaryID,
		"memberFindingIds": beforeIDs,
	})
	if err != nil {
		return err
	}
	after, err := json.Marshal(map[string]any{
		"state": state, "primaryFindingId": primaryID, "memberFindingId": input.MemberFindingID,
		"memberFindingIds": afterIDs,
		"primaryDecision":  primaryDecision, "memberDecision": memberDecision,
	})
	if err != nil {
		return err
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO `+a.table("finding_correlation_events")+`
		(id,workspace_id,correlation_id,sequence,event_type,actor_id,rationale,idempotency_key,
		 binding_digest,before_state,after_state,created_at)
		VALUES($1,$2,$3,$4,'split',$5,$6,$7,$8,$9,$10,$11)`,
		newID(), workspace, correlationID, revision+1, session.User.ID, input.Rationale,
		input.IdempotencyKey, digest, before, after, now); err != nil {
		return err
	}
	correlation, err := a.loadCorrelation(r.Context(), tx, workspace, correlationID)
	if err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]any{"correlation": correlation})
	return nil
}
