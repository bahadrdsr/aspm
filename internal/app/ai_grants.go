package app

import (
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"
)

func (a *Application) createAIGrant(w http.ResponseWriter, r *http.Request, workspace string, session authenticatedSession) error {
	var input struct {
		ProfileID       string `json:"profileId"`
		ProfileRevision string `json:"profileRevision"`
		PolicyRevision  string `json:"policyRevision"`
		Destination     string `json:"destination"`
		Task            string `json:"task"`
		DataClass       string `json:"dataClass"`
		ExpiresAt       string `json:"expiresAt"`
	}
	if err := a.decode(w, r, &input, aiBodyLimit); err != nil {
		return err
	}
	if !validID(input.ProfileID) || !validAIText(input.ProfileRevision, 256) ||
		!validAIText(input.PolicyRevision, 256) || !validAIText(input.Destination, aiEndpointLimit) ||
		input.Task != aiTask || input.DataClass != aiDataClass || len(input.ExpiresAt) > 64 {
		return errInvalid
	}
	expiry, err := time.Parse(time.RFC3339Nano, input.ExpiresAt)
	if err != nil {
		return errInvalid
	}
	// PostgreSQL stores microseconds. Never round an approval's expiry up.
	expiry = expiry.UTC().Truncate(time.Microsecond)
	tx, err := a.pool.Begin(r.Context())
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err = a.authorizeAIAdmin(r.Context(), tx, workspace, session); err != nil {
		return err
	}
	profile, err := scanAIProfile(tx.QueryRow(r.Context(), `SELECT `+aiProfileColumns+
		` FROM `+a.table("ai_profiles")+` WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, workspace, input.ProfileID))
	if errors.Is(err, pgx.ErrNoRows) {
		return errConflict
	}
	if err != nil {
		return err
	}
	policy, err := scanAIPolicy(tx.QueryRow(r.Context(), `SELECT `+aiPolicyColumns+
		` FROM `+a.table("ai_policies")+` WHERE workspace_id=$1 FOR UPDATE`, workspace), workspace)
	if err != nil {
		return err
	}
	now := a.config.Now().UTC()
	if !expiry.After(now) {
		return errInvalid
	}
	if policy.Mode != "approved-hosted" || !profile.Enabled || !profile.StructuredOutput ||
		!validAIProfile(profile.AIProfile) || profile.Revision != input.ProfileRevision ||
		policy.Revision != input.PolicyRevision || profile.Endpoint != input.Destination {
		return errConflict
	}
	grant, err := scanAIGrant(tx.QueryRow(r.Context(), `INSERT INTO `+a.table("ai_egress_grants")+`
		(id,workspace_id,profile_id,profile_revision,policy_revision,destination,task,data_class,
		 expires_at,created_at,granted_by)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) RETURNING `+aiGrantColumns,
		newID(), workspace, profile.ID, profile.Revision, policy.Revision, profile.Endpoint,
		aiTask, aiDataClass, expiry, now, session.User.ID))
	if err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	writeJSON(w, http.StatusCreated, map[string]any{"grant": grant})
	return nil
}

func (a *Application) revokeAIGrant(w http.ResponseWriter, r *http.Request, workspace string, session authenticatedSession, id string) error {
	var input struct{}
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
	grant, err := scanAIGrant(tx.QueryRow(r.Context(), `SELECT `+aiGrantColumns+
		` FROM `+a.table("ai_egress_grants")+` WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, workspace, id))
	if err != nil {
		return err
	}
	if grant.RevokedAt == nil {
		grant, err = scanAIGrant(tx.QueryRow(r.Context(), `UPDATE `+a.table("ai_egress_grants")+`
			SET revoked_at=$3,revoked_by=$4 WHERE workspace_id=$1 AND id=$2 RETURNING `+aiGrantColumns,
			workspace, id, a.config.Now().UTC(), session.User.ID))
		if err != nil {
			return err
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"grant": grant})
	return nil
}

func (a *Application) getAIGrant(w http.ResponseWriter, r *http.Request, workspace, id string) error {
	grant, err := scanAIGrant(a.pool.QueryRow(r.Context(), `SELECT `+aiGrantColumns+
		` FROM `+a.table("ai_egress_grants")+` WHERE workspace_id=$1 AND id=$2`, workspace, id))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"grant": grant})
	return nil
}

func (a *Application) listAIGrants(w http.ResponseWriter, r *http.Request, workspace string) error {
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
	if err = tx.QueryRow(r.Context(), `SELECT count(*) FROM `+a.table("ai_egress_grants")+`
		WHERE workspace_id=$1`, workspace).Scan(&total); err != nil {
		return err
	}
	rows, err := tx.Query(r.Context(), `SELECT `+aiGrantColumns+` FROM `+a.table("ai_egress_grants")+`
		WHERE workspace_id=$1 AND id>$2 ORDER BY id LIMIT $3`, workspace, cursor, limit+1)
	if err != nil {
		return err
	}
	items := []AIEgressGrant{}
	for rows.Next() {
		grant, scanErr := scanAIGrant(rows)
		if scanErr != nil {
			rows.Close()
			return scanErr
		}
		items = append(items, grant)
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
