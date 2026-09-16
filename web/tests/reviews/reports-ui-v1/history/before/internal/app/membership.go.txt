package app

import (
	"context"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
)

func (a *Application) sessionRole(ctx context.Context, tx pgx.Tx, session authenticatedSession, workspace string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	var userID string
	err := tx.QueryRow(ctx, `SELECT user_id FROM `+a.table("sessions")+`
		WHERE token_hash=$1 AND user_id=$2 AND revoked_at IS NULL AND expires_at>$3 FOR SHARE`,
		session.TokenHash, session.User.ID, a.config.Now().UTC()).Scan(&userID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errUnauthorized
	}
	if err != nil {
		return "", err
	}
	var role string
	err = tx.QueryRow(ctx, `SELECT role FROM `+a.table("memberships")+`
		WHERE workspace_id=$1 AND user_id=$2 FOR SHARE`, workspace, userID).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errForbidden
	}
	return role, err
}

func (a *Application) authorizeIntake(ctx context.Context, tx pgx.Tx, session authenticatedSession, workspace, assetID string) error {
	role, err := a.sessionRole(ctx, tx, session, workspace)
	if err != nil {
		return err
	}
	if role != "admin" && role != "analyst" {
		return errForbidden
	}
	var id string
	err = tx.QueryRow(ctx, `SELECT id FROM `+a.table("assets")+`
		WHERE workspace_id=$1 AND id=$2 FOR KEY SHARE`, workspace, assetID).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return errNotFound
	}
	return err
}

func (a *Application) changeUserRole(w http.ResponseWriter, r *http.Request, workspace string, session authenticatedSession, id string) error {
	var input struct {
		Role string `json:"role"`
	}
	if err := a.decode(w, r, &input, 16<<10); err != nil {
		return err
	}
	if !validRole(input.Role) {
		return errInvalid
	}
	tx, err := a.pool.Begin(r.Context())
	if err != nil {
		return err
	}
	defer rollback(tx)
	var workspaceID string
	if err = tx.QueryRow(r.Context(), `SELECT id FROM `+a.table("workspaces")+` WHERE id=$1 FOR UPDATE`,
		workspace).Scan(&workspaceID); err != nil {
		return err
	}
	role, err := a.sessionRole(r.Context(), tx, session, workspace)
	if err != nil {
		return err
	}
	if role != "admin" {
		return errForbidden
	}
	var user User
	var prior string
	if err = tx.QueryRow(r.Context(), `SELECT u.id,u.name,u.email,m.role FROM `+a.table("memberships")+` m
		JOIN `+a.table("users")+` u ON u.id=m.user_id WHERE m.workspace_id=$1 AND m.user_id=$2 FOR UPDATE OF m`,
		workspace, id).Scan(&user.ID, &user.Name, &user.Email, &prior); err != nil {
		return err
	}
	if prior == "admin" && input.Role != "admin" {
		var admins int
		if err = tx.QueryRow(r.Context(), `SELECT count(*) FROM `+a.table("memberships")+`
			WHERE workspace_id=$1 AND role='admin'`, workspace).Scan(&admins); err != nil {
			return err
		}
		if admins <= 1 {
			return errConflict
		}
	}
	if _, err = tx.Exec(r.Context(), `UPDATE `+a.table("memberships")+` SET role=$3
		WHERE workspace_id=$1 AND user_id=$2`, workspace, id, input.Role); err != nil {
		return err
	}
	if prior != "viewer" && input.Role == "viewer" {
		// Regranting later cannot resurrect an already revoked job. The active
		// reader observes this durable failure and cancels its S3 context.
		if _, err = tx.Exec(r.Context(), `UPDATE `+a.table("imports")+`
			SET state='failed',failure_code='authorization-revoked',
				failure_message='Submitting actor no longer has selected-workspace write permission',
				worker_id=NULL,lease_until=NULL,fence=fence+1
			WHERE workspace_id=$1 AND submitted_by=$2 AND state IN ('queued','processing')`, workspace, id); err != nil {
			return err
		}
	}
	if err = r.Context().Err(); err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"user": user})
	return nil
}
