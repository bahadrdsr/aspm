package app

import "net/http"

func (a *Application) getAIPolicy(w http.ResponseWriter, r *http.Request, workspace string) error {
	policy, err := scanAIPolicy(a.pool.QueryRow(r.Context(), `SELECT `+aiPolicyColumns+
		` FROM `+a.table("ai_policies")+` WHERE workspace_id=$1`, workspace), workspace)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"policy": policy})
	return nil
}

func (a *Application) updateAIPolicy(w http.ResponseWriter, r *http.Request, workspace string, session authenticatedSession) error {
	var input struct {
		Mode string `json:"mode"`
	}
	if err := a.decode(w, r, &input, aiBodyLimit); err != nil {
		return err
	}
	if !validAIPolicyMode(input.Mode) {
		return errInvalid
	}
	tx, err := a.pool.Begin(r.Context())
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err = a.authorizeAIAdmin(r.Context(), tx, workspace, session); err != nil {
		return err
	}
	policy, err := scanAIPolicy(tx.QueryRow(r.Context(), `SELECT `+aiPolicyColumns+
		` FROM `+a.table("ai_policies")+` WHERE workspace_id=$1 FOR UPDATE`, workspace), workspace)
	if err != nil {
		return err
	}
	if policy.Mode != input.Mode {
		policy, err = scanAIPolicy(tx.QueryRow(r.Context(), `INSERT INTO `+a.table("ai_policies")+` AS p
			(workspace_id,mode,updated_by,updated_at) VALUES($1,$2,$3,$4)
			ON CONFLICT(workspace_id) DO UPDATE
				SET mode=EXCLUDED.mode,revision=p.revision+1,
					updated_by=EXCLUDED.updated_by,updated_at=EXCLUDED.updated_at
			RETURNING `+aiPolicyColumns, workspace, input.Mode, session.User.ID, a.config.Now().UTC()), workspace)
		if err != nil {
			return err
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"policy": policy})
	return nil
}
