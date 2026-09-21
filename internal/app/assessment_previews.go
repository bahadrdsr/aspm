package app

import (
	"context"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

func (a *Application) authorizeAssessmentWriter(ctx context.Context, tx pgx.Tx, workspace string, session authenticatedSession) error {
	var id string
	if err := tx.QueryRow(ctx, `SELECT id FROM `+a.table("workspaces")+`
		WHERE id=$1 FOR UPDATE`, workspace).Scan(&id); err != nil {
		return err
	}
	role, err := a.sessionRole(ctx, tx, session, workspace)
	if err != nil {
		return err
	}
	if role != "admin" && role != "analyst" {
		return errForbidden
	}
	return nil
}

func (a *Application) createAssessmentPreview(w http.ResponseWriter, r *http.Request, workspace string, session authenticatedSession, finding string) error {
	if a.config.AssessmentScope == "" {
		return errUnavailable
	}
	var input struct {
		ObservationID string                `json:"observationId"`
		ProfileID     string                `json:"profileId"`
		GrantID       *string               `json:"grantId"`
		Context       assessmentContextText `json:"context"`
		Reviewed      bool                  `json:"reviewed"`
	}
	if err := a.decode(w, r, &input, 256<<10); err != nil {
		return err
	}
	text := string(input.Context)
	if len(text) > assessmentContextLimit {
		return errTooLarge
	}
	if !input.Reviewed || !validAssessmentContext(text) || !validID(input.ObservationID) ||
		!validID(input.ProfileID) || input.GrantID == nil || *input.GrantID != "" && !validID(*input.GrantID) {
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
	var sourceDigest string
	if err = tx.QueryRow(r.Context(), `SELECT COALESCE(o.data->>'evidenceDigest','')
		FROM `+a.table("observations")+` o JOIN `+a.table("findings")+` f
			ON f.workspace_id=o.workspace_id AND f.id=o.finding_id
		WHERE o.workspace_id=$1 AND o.finding_id=$2 AND o.id=$3 FOR KEY SHARE OF f,o`,
		workspace, finding, input.ObservationID).Scan(&sourceDigest); err != nil {
		return err
	}
	resolved, err := a.readAIConfiguration(r.Context(), tx, a.integrationCredentials, AIConfigurationRequest{
		WorkspaceID: workspace, ActorID: session.User.ID, ProfileID: input.ProfileID,
		GrantID: *input.GrantID, Task: aiTask, DataClass: aiDataClass,
	}, true)
	if err != nil {
		return assessmentAPIError(err)
	}
	p, policy := resolved.configuration.Profile, resolved.configuration.Policy
	preview := AssessmentPreview{ID: newID(), AssessmentBinding: AssessmentBinding{
		WorkspaceID: workspace, FindingID: finding, ObservationID: input.ObservationID,
		SourceEvidenceDigest: sourceDigest, RequestedBy: session.User.ID,
		ProfileID: p.ID, ProfileRevision: p.Revision, PolicyRevision: policy.Revision, GrantID: policy.ApprovalRef,
		Destination: p.Endpoint, Family: p.Family, Model: p.Model, Deployment: p.Deployment,
		Task: aiTask, DataClass: aiDataClass, PromptRevision: assessmentPromptRevision,
		ContextDigest: reportDigest([]byte(text)), ContextOrigin: assessmentContextOrigin, Context: text,
	}}
	preview.ContextRef = "reviewed-context:" + preview.ID
	if assessmentBindingHasSecret(preview.AssessmentBinding, []string{p.APIKey}) {
		return errInvalid
	}
	if err = tx.QueryRow(r.Context(), `SELECT clock_timestamp()`).Scan(&preview.CreatedAt); err != nil {
		return err
	}
	preview.ExpiresAt = assessmentExpiry(preview.CreatedAt.Add(5*time.Minute), resolved)
	valid, err := a.assessmentTimeValid(r.Context(), tx, preview.ExpiresAt)
	if err != nil {
		return err
	}
	if !valid {
		return errConflict
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO `+a.table("assessment_previews")+`
		(id,workspace_id,finding_id,observation_id,source_evidence_digest,requested_by,profile_id,
		 profile_revision,policy_revision,grant_id,destination,family,model,deployment,task,data_class,
		 prompt_revision,context_ref,context_digest,context_origin,context,created_at,expires_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23)`,
		preview.ID, workspace, finding, preview.ObservationID, preview.SourceEvidenceDigest, preview.RequestedBy,
		preview.ProfileID, preview.ProfileRevision, preview.PolicyRevision, preview.GrantID,
		preview.Destination, preview.Family, preview.Model, preview.Deployment, preview.Task, preview.DataClass,
		preview.PromptRevision, preview.ContextRef, preview.ContextDigest, preview.ContextOrigin, preview.Context,
		preview.CreatedAt, preview.ExpiresAt); err != nil {
		return err
	}
	if err = a.finalizeAssessmentConsent(r.Context(), tx, workspace, session, preview.ExpiresAt); err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]any{"preview": preview})
	return nil
}

func (a *Application) finalizeAssessmentConsent(ctx context.Context, tx pgx.Tx, workspace string, session authenticatedSession, expires time.Time) error {
	role, err := a.sessionRole(ctx, tx, session, workspace)
	if err != nil {
		return err
	}
	if role != "admin" && role != "analyst" {
		return errForbidden
	}
	valid, err := a.assessmentTimeValid(ctx, tx, expires)
	if err != nil {
		return err
	}
	if !valid {
		return errConflict
	}
	return ctx.Err()
}
