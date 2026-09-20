package app

import (
	"net/http"

	"github.com/jackc/pgx/v5"
)

type aiProfileInput struct {
	Name             optional[string] `json:"name"`
	Family           optional[string] `json:"family"`
	Endpoint         optional[string] `json:"endpoint"`
	Model            optional[string] `json:"model"`
	Deployment       optional[string] `json:"deployment"`
	Enabled          optional[bool]   `json:"enabled"`
	StructuredOutput optional[bool]   `json:"structuredOutput"`
	APIKey           optional[string] `json:"apiKey"`
}

func applyAIProfileInput(profile AIProfile, input aiProfileInput) (AIProfile, bool, error) {
	next := profile
	for _, field := range []struct {
		input  optional[string]
		target *string
	}{
		{input.Name, &next.Name}, {input.Family, &next.Family},
		{input.Endpoint, &next.Endpoint}, {input.Model, &next.Model},
		{input.Deployment, &next.Deployment},
	} {
		if field.input.Set {
			if field.input.Value == nil {
				return AIProfile{}, false, errInvalid
			}
			*field.target = *field.input.Value
		}
	}
	for _, field := range []struct {
		input  optional[bool]
		target *bool
	}{
		{input.Enabled, &next.Enabled}, {input.StructuredOutput, &next.StructuredOutput},
	} {
		if field.input.Set {
			if field.input.Value == nil {
				return AIProfile{}, false, errInvalid
			}
			*field.target = *field.input.Value
		}
	}
	replacement := input.APIKey.Set && input.APIKey.Value != nil
	if input.APIKey.Set {
		if replacement {
			if !validIntegrationToken(*input.APIKey.Value) {
				return AIProfile{}, false, errInvalid
			}
		} else if next.Family != "local" {
			return AIProfile{}, false, errInvalid
		}
		next.CredentialConfigured = replacement
	}
	if !validAIProfile(next) {
		return AIProfile{}, false, errInvalid
	}
	// A supplied nonempty key is an action, not a secret-equality comparison.
	return next, next != profile || replacement, nil
}

func (a *Application) createAIProfile(w http.ResponseWriter, r *http.Request, workspace string, session authenticatedSession) error {
	var input aiProfileInput
	if err := a.decode(w, r, &input, aiBodyLimit); err != nil {
		return err
	}
	if !input.Name.Set || !input.Family.Set || !input.Endpoint.Set || !input.Model.Set ||
		!input.Enabled.Set || !input.StructuredOutput.Set {
		return errInvalid
	}
	profile, _, err := applyAIProfileInput(AIProfile{ID: newID(), WorkspaceID: workspace}, input)
	if err != nil {
		return err
	}
	tx, err := a.pool.Begin(r.Context())
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err = a.authorizeAIAdmin(r.Context(), tx, workspace, session); err != nil {
		return err
	}
	var ciphertext []byte
	if input.APIKey.Value != nil {
		ciphertext, err = sealCredential(a.integrationCredentials, aiCredentialAAD(workspace, profile.ID), *input.APIKey.Value)
		if err != nil {
			return err
		}
	}
	record, err := scanAIProfile(tx.QueryRow(r.Context(), `INSERT INTO `+a.table("ai_profiles")+`
		(id,workspace_id,name,family,endpoint,model,deployment,enabled,structured_output,
		 credential_ciphertext,created_at,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11) RETURNING `+aiProfileColumns,
		profile.ID, workspace, profile.Name, profile.Family, profile.Endpoint, profile.Model,
		profile.Deployment, profile.Enabled, profile.StructuredOutput, ciphertext, a.config.Now().UTC()))
	if err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]any{"profile": record.AIProfile})
	return nil
}

func (a *Application) updateAIProfile(w http.ResponseWriter, r *http.Request, workspace string, session authenticatedSession, id string) error {
	var input aiProfileInput
	if err := a.decode(w, r, &input, aiBodyLimit); err != nil {
		return err
	}
	tx, err := a.pool.Begin(r.Context())
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err = a.authorizeAIAdmin(r.Context(), tx, workspace, session); err != nil {
		return err
	}
	record, err := scanAIProfile(tx.QueryRow(r.Context(), `SELECT `+aiProfileColumns+
		` FROM `+a.table("ai_profiles")+` WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, workspace, id))
	if err != nil {
		return err
	}
	profile, changed, err := applyAIProfileInput(record.AIProfile, input)
	if err != nil {
		return err
	}
	if changed {
		var ciphertext []byte
		if input.APIKey.Set && input.APIKey.Value != nil {
			ciphertext, err = sealCredential(a.integrationCredentials, aiCredentialAAD(workspace, id), *input.APIKey.Value)
			if err != nil {
				return err
			}
		}
		record, err = scanAIProfile(tx.QueryRow(r.Context(), `UPDATE `+a.table("ai_profiles")+`
			SET name=$3,family=$4,endpoint=$5,model=$6,deployment=$7,enabled=$8,structured_output=$9,
				credential_ciphertext=CASE WHEN $10::boolean THEN $11::bytea ELSE credential_ciphertext END,
				revision=revision+1,updated_at=$12
			WHERE workspace_id=$1 AND id=$2 RETURNING `+aiProfileColumns,
			workspace, id, profile.Name, profile.Family, profile.Endpoint, profile.Model,
			profile.Deployment, profile.Enabled, profile.StructuredOutput, input.APIKey.Set,
			ciphertext, a.config.Now().UTC()))
		if err != nil {
			return err
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"profile": record.AIProfile})
	return nil
}

func (a *Application) getAIProfile(w http.ResponseWriter, r *http.Request, workspace, id string) error {
	record, err := scanAIProfile(a.pool.QueryRow(r.Context(), `SELECT `+aiProfileColumns+
		` FROM `+a.table("ai_profiles")+` WHERE workspace_id=$1 AND id=$2`, workspace, id))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"profile": record.AIProfile})
	return nil
}

func (a *Application) listAIProfiles(w http.ResponseWriter, r *http.Request, workspace string) error {
	limit, cursor, err := pageParameters(r)
	if err != nil {
		return err
	}
	tx, err := a.pool.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer rollback(tx)
	var total int64
	if err = tx.QueryRow(r.Context(), `SELECT count(*) FROM `+a.table("ai_profiles")+`
		WHERE workspace_id=$1`, workspace).Scan(&total); err != nil {
		return err
	}
	rows, err := tx.Query(r.Context(), `SELECT `+aiProfileColumns+` FROM `+a.table("ai_profiles")+`
		WHERE workspace_id=$1 AND id>$2 ORDER BY id LIMIT $3`, workspace, cursor, limit+1)
	if err != nil {
		return err
	}
	items := []AIProfile{}
	for rows.Next() {
		record, scanErr := scanAIProfile(rows)
		if scanErr != nil {
			rows.Close()
			return scanErr
		}
		items = append(items, record.AIProfile)
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
