package app

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
)

func (a *Application) enqueueAssessment(w http.ResponseWriter, r *http.Request, workspace string, session authenticatedSession, finding string) error {
	if a.config.AssessmentScope == "" {
		return errUnavailable
	}
	var input struct {
		PreviewID      string `json:"previewId"`
		IdempotencyKey string `json:"idempotencyKey"`
		Consent        bool   `json:"consent"`
	}
	if err := a.decode(w, r, &input, 4<<10); err != nil {
		return err
	}
	if !input.Consent || !validID(input.PreviewID) || !validAssessmentIdentity(input.IdempotencyKey) {
		return errInvalid
	}
	tx, err := a.pool.Begin(r.Context())
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err = a.authorizeAssessmentWriter(r.Context(), tx, workspace, session); err != nil {
		return err
	}
	preview, err := scanAssessmentPreview(tx.QueryRow(r.Context(), a.assessmentPreviewSelect()+`
		WHERE p.workspace_id=$1 AND p.finding_id=$2 AND p.id=$3`, workspace, finding, input.PreviewID))
	if err != nil {
		return err
	}
	var observation string
	if err = tx.QueryRow(r.Context(), `SELECT id FROM `+a.table("observations")+`
		WHERE workspace_id=$1 AND finding_id=$2 AND id=$3 FOR KEY SHARE`,
		workspace, finding, preview.ObservationID).Scan(&observation); err != nil {
		return err
	}
	// The workspace lock serializes queue bindings. A replay is still a current
	// authorization request, unlike the deliberately historical GET surface.
	existing, lookupErr := scanAssessmentJob(tx.QueryRow(r.Context(), a.assessmentJobSelect()+`
		WHERE j.workspace_id=$1 AND j.idempotency_key=$2`, workspace, input.IdempotencyKey))
	if lookupErr != nil && !errors.Is(lookupErr, pgx.ErrNoRows) {
		return lookupErr
	}
	if lookupErr == nil && (existing.PreviewID != preview.ID || existing.RequestedBy != session.User.ID ||
		existing.FindingID != finding || existing.Scope != a.config.AssessmentScope) {
		return errConflict
	}
	if preview.RequestedBy != session.User.ID {
		return errForbidden
	}
	resolved, err := a.readAIConfiguration(r.Context(), tx, a.integrationCredentials, assessmentRequest(preview.AssessmentBinding), true)
	if err != nil {
		return assessmentAPIError(err)
	}
	if !assessmentBindingCurrent(preview.AssessmentBinding, resolved) ||
		preview.ContextRef != "reviewed-context:"+preview.ID {
		return errConflict
	}
	if assessmentBindingHasSecret(preview.AssessmentBinding, []string{resolved.configuration.Profile.APIKey}) {
		return errInvalid
	}
	expires := assessmentExpiry(preview.ExpiresAt, resolved)
	if err = a.finalizeAssessmentConsent(r.Context(), tx, workspace, session, expires); err != nil {
		return err
	}
	status := http.StatusOK
	if errors.Is(lookupErr, pgx.ErrNoRows) {
		id := newID()
		if _, err = tx.Exec(r.Context(), `INSERT INTO `+a.table("assessment_jobs")+`
			(id,workspace_id,finding_id,requested_by,preview_id,idempotency_key,scope,consent_expires_at)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8)`,
			id, workspace, finding, session.User.ID, preview.ID, input.IdempotencyKey,
			a.config.AssessmentScope, preview.ExpiresAt); err != nil {
			return err
		}
		existing, err = scanAssessmentJob(tx.QueryRow(r.Context(), a.assessmentJobSelect()+`
			WHERE j.workspace_id=$1 AND j.id=$2`, workspace, id))
		if err != nil {
			return err
		}
		status = http.StatusAccepted
	}
	if err = a.finalizeAssessmentConsent(r.Context(), tx, workspace, session, expires); err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	writeJSON(w, status, map[string]any{"assessment": existing.AssessmentJob})
	return nil
}

func (a *Application) getAssessment(w http.ResponseWriter, r *http.Request, workspace, id string) error {
	job, err := scanAssessmentJob(a.pool.QueryRow(r.Context(), a.assessmentJobSelect()+`
		WHERE j.workspace_id=$1 AND j.id=$2`, workspace, id))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"assessment": job.AssessmentJob})
	return nil
}

func (a *Application) listAssessments(w http.ResponseWriter, r *http.Request, workspace, finding string) error {
	limit, cursor, err := pageParameters(r)
	if err != nil {
		return err
	}
	tx, err := a.pool.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer rollback(tx)
	var id string
	if err = tx.QueryRow(r.Context(), `SELECT id FROM `+a.table("findings")+`
		WHERE workspace_id=$1 AND id=$2`, workspace, finding).Scan(&id); err != nil {
		return err
	}
	var total int64
	if err = tx.QueryRow(r.Context(), `SELECT count(*) FROM `+a.table("assessment_jobs")+`
		WHERE workspace_id=$1 AND finding_id=$2`, workspace, finding).Scan(&total); err != nil {
		return err
	}
	rows, err := tx.Query(r.Context(), a.assessmentJobSelect()+`
		WHERE j.workspace_id=$1 AND j.finding_id=$2 AND j.id>$3 ORDER BY j.id LIMIT $4`,
		workspace, finding, cursor, limit+1)
	if err != nil {
		return err
	}
	items := []AssessmentJob{}
	for rows.Next() {
		job, scanErr := scanAssessmentJob(rows)
		if scanErr != nil {
			rows.Close()
			return scanErr
		}
		items = append(items, job.AssessmentJob)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	var next *string
	if len(items) > limit {
		items = items[:limit]
		next = &items[len(items)-1].ID
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "nextCursor": next})
	return nil
}

func (a *Application) cancelAssessment(w http.ResponseWriter, r *http.Request, workspace string, session authenticatedSession, id string) error {
	if a.config.AssessmentScope == "" {
		return errUnavailable
	}
	var input struct{}
	if err := a.decode(w, r, &input, 4<<10); err != nil {
		return err
	}
	tx, err := a.pool.Begin(r.Context())
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err = a.authorizeAssessmentWriter(r.Context(), tx, workspace, session); err != nil {
		return err
	}
	job, err := scanAssessmentJob(tx.QueryRow(r.Context(), a.assessmentJobSelect()+`
		WHERE j.workspace_id=$1 AND j.id=$2 FOR UPDATE OF j`, workspace, id))
	if err != nil {
		return err
	}
	if job.Scope != a.config.AssessmentScope {
		return errConflict
	}
	if job.State == "queued" || job.State == "dispatching" {
		failure, _ := json.Marshal(&Failure{Code: "cancelled", Message: "Assessment cancelled by a workspace writer"})
		// Cancellation fences the result, not the in-flight transport. Only the
		// attempt owner's cleanup acknowledgment can release its reservation early.
		if _, err = tx.Exec(r.Context(), `UPDATE `+a.table("assessment_jobs")+`
			SET state='cancelled',failure=$3,completed_at=clock_timestamp(),
				worker_id=NULL,lease_until=NULL,fence=fence+1
			WHERE workspace_id=$1 AND id=$2 AND state IN ('queued','dispatching')`, workspace, id, failure); err != nil {
			return err
		}
		job, err = scanAssessmentJob(tx.QueryRow(r.Context(), a.assessmentJobSelect()+`
			WHERE j.workspace_id=$1 AND j.id=$2`, workspace, id))
		if err != nil {
			return err
		}
	}
	role, err := a.sessionRole(r.Context(), tx, session, workspace)
	if err != nil {
		return err
	}
	if role != "admin" && role != "analyst" {
		return errForbidden
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"assessment": job.AssessmentJob})
	return nil
}
