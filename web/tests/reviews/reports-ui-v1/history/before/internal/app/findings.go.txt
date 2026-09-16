package app

import (
	"encoding/json"
	"net/http"
	"time"
)

func (a *Application) findingResponse(w http.ResponseWriter, r *http.Request, workspace, id string) error {
	var f Finding
	var scope Scope
	dest := workDest(&f.WorkItem)
	dest = append(dest, &f.AssetID, &f.WorkspaceID, &scope.ID, &scope.Revision, &scope.Branch,
		&f.Description, &f.Remediation, &f.Evidence.Text, &f.Evidence.SourceLabel, &f.OwnerID,
		&f.SourceState, &f.SourceFreshnessAt, &f.Disposition, &f.AcceptedRiskExpiresAt)
	err := a.pool.QueryRow(r.Context(), `SELECT `+workColumns+`,
		f.asset_id,f.workspace_id,f.scope_id,f.scope_revision,f.scope_branch,f.description,f.remediation,
		f.evidence_text,f.source_label,f.owner_id,f.source_state,f.source_freshness_at,f.disposition,f.accepted_risk_expires_at`+
		a.workFrom()+` WHERE f.workspace_id=$1 AND f.id=$2`, workspace, id).Scan(dest...)
	if err != nil {
		return err
	}
	f.ScopeLabel = scope.ID + " / " + scope.Branch + " (revision " + scope.Revision + ")"
	f.Evidence.VerificationState = "not-run"
	f.RiskAcceptanceExpired = f.Disposition == "accepted-risk" && f.AcceptedRiskExpiresAt != nil &&
		!a.config.Now().Before(*f.AcceptedRiskExpiresAt)
	observationsCursor, notesCursor := r.URL.Query().Get("observationsCursor"), r.URL.Query().Get("notesCursor")
	if (observationsCursor != "" && !validID(observationsCursor)) || (notesCursor != "" && !validID(notesCursor)) {
		return errInvalid
	}
	f.Observations, f.Notes = []Observation{}, []Note{}
	rows, err := a.pool.Query(r.Context(), `SELECT data FROM `+a.table("observations")+`
		WHERE workspace_id=$1 AND finding_id=$2 AND id>$3 ORDER BY id LIMIT 501`, workspace, id, observationsCursor)
	if err != nil {
		return err
	}
	for rows.Next() {
		var raw []byte
		var observation Observation
		if err = rows.Scan(&raw); err != nil {
			rows.Close()
			return err
		}
		if err = json.Unmarshal(raw, &observation); err != nil {
			rows.Close()
			return err
		}
		f.Observations = append(f.Observations, observation)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if len(f.Observations) > 500 {
		f.Observations = f.Observations[:500]
		f.ObservationsNextCursor = &f.Observations[499].ID
	}
	rows, err = a.pool.Query(r.Context(), `SELECT id,text FROM `+a.table("notes")+`
		WHERE workspace_id=$1 AND finding_id=$2 AND id>$3 ORDER BY id LIMIT 501`, workspace, id, notesCursor)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var note Note
		if err = rows.Scan(&note.ID, &note.Text); err != nil {
			return err
		}
		f.Notes = append(f.Notes, note)
	}
	if err = rows.Err(); err != nil {
		return err
	}
	if len(f.Notes) > 500 {
		f.Notes = f.Notes[:500]
		f.NotesNextCursor = &f.Notes[499].ID
	}
	writeJSON(w, 200, map[string]any{"dataOrigin": "live", "finding": f})
	return nil
}

func (a *Application) patchFinding(w http.ResponseWriter, r *http.Request, workspace, id string) error {
	var input struct {
		OwnerID               optional[string]    `json:"ownerId"`
		WorkflowState         optional[string]    `json:"workflowState"`
		Disposition           optional[string]    `json:"disposition"`
		AcceptedRiskExpiresAt optional[time.Time] `json:"acceptedRiskExpiresAt"`
	}
	if err := a.decode(w, r, &input, 16<<10); err != nil {
		return err
	}
	tx, err := a.pool.Begin(r.Context())
	if err != nil {
		return err
	}
	defer rollback(tx)
	var owner *string
	var workflow, disposition string
	var expires *time.Time
	if err = tx.QueryRow(r.Context(), `SELECT owner_id,workflow_state,disposition,accepted_risk_expires_at
		FROM `+a.table("findings")+` WHERE workspace_id=$1 AND id=$2 FOR UPDATE`,
		workspace, id).Scan(&owner, &workflow, &disposition, &expires); err != nil {
		return err
	}
	if input.OwnerID.Set {
		owner = input.OwnerID.Value
	}
	if err = a.checkOwner(r.Context(), tx, workspace, owner); err != nil {
		return err
	}
	if input.WorkflowState.Set {
		if input.WorkflowState.Value == nil {
			return errInvalid
		}
		workflow = *input.WorkflowState.Value
	}
	if workflow != "open" && workflow != "in-progress" && workflow != "resolved" {
		return errInvalid
	}
	if input.Disposition.Set {
		if input.Disposition.Value == nil {
			return errInvalid
		}
		disposition = *input.Disposition.Value
		if disposition == "none" && !input.AcceptedRiskExpiresAt.Set {
			expires = nil
		}
	}
	if disposition != "none" && disposition != "accepted-risk" {
		return errInvalid
	}
	if input.AcceptedRiskExpiresAt.Set {
		expires = input.AcceptedRiskExpiresAt.Value
	}
	if expires != nil && (expires.IsZero() || disposition != "accepted-risk") {
		return errInvalid
	}
	if _, err = tx.Exec(r.Context(), `UPDATE `+a.table("findings")+`
		SET owner_id=$3,workflow_state=$4,disposition=$5,accepted_risk_expires_at=$6
		WHERE workspace_id=$1 AND id=$2`, workspace, id, owner, workflow, disposition, expires); err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	return a.findingResponse(w, r, workspace, id)
}

func (a *Application) addNote(w http.ResponseWriter, r *http.Request, workspace, author, id string) error {
	var input struct {
		Text string `json:"text"`
	}
	if err := a.decode(w, r, &input, 32<<10); err != nil {
		return err
	}
	if !validText(input.Text, 8192) {
		return errInvalid
	}
	note := Note{ID: newID(), Text: input.Text}
	result, err := a.pool.Exec(r.Context(), `INSERT INTO `+a.table("notes")+`(id,workspace_id,finding_id,author_id,text,created_at)
		SELECT $1,workspace_id,id,$4,$5,$6 FROM `+a.table("findings")+` WHERE workspace_id=$2 AND id=$3`,
		note.ID, workspace, id, author, note.Text, a.config.Now().UTC())
	if err != nil {
		return err
	}
	if result.RowsAffected() == 0 {
		return errNotFound
	}
	writeJSON(w, http.StatusCreated, map[string]any{"note": note})
	return nil
}
