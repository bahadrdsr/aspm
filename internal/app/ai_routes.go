package app

import (
	"context"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
)

func (a *Application) routeAIConfiguration(w http.ResponseWriter, r *http.Request, membership Workspace, session authenticatedSession) error {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/ai/")
	switch path {
	case "profiles":
		if err := requireMethod(w, r, http.MethodGet, http.MethodPost); err != nil {
			return err
		}
		if r.Method == http.MethodGet {
			return a.listAIProfiles(w, r, membership.ID)
		}
		if membership.Role != "admin" {
			return errForbidden
		}
		return a.createAIProfile(w, r, membership.ID, session)
	case "policy":
		if err := requireMethod(w, r, http.MethodGet, http.MethodPatch); err != nil {
			return err
		}
		if r.Method == http.MethodGet {
			return a.getAIPolicy(w, r, membership.ID)
		}
		if membership.Role != "admin" {
			return errForbidden
		}
		return a.updateAIPolicy(w, r, membership.ID, session)
	case "grants":
		if err := requireMethod(w, r, http.MethodGet, http.MethodPost); err != nil {
			return err
		}
		if r.Method == http.MethodGet {
			return a.listAIGrants(w, r, membership.ID)
		}
		if membership.Role != "admin" {
			return errForbidden
		}
		return a.createAIGrant(w, r, membership.ID, session)
	}
	parts := strings.Split(path, "/")
	if len(parts) < 2 || !validID(parts[1]) {
		return errNotFound
	}
	if len(parts) == 2 && parts[0] == "profiles" {
		if err := requireMethod(w, r, http.MethodGet, http.MethodPatch); err != nil {
			return err
		}
		if r.Method == http.MethodGet {
			return a.getAIProfile(w, r, membership.ID, parts[1])
		}
		if membership.Role != "admin" {
			return errForbidden
		}
		return a.updateAIProfile(w, r, membership.ID, session, parts[1])
	}
	if parts[0] == "grants" {
		if len(parts) == 2 {
			if err := requireMethod(w, r, http.MethodGet); err != nil {
				return err
			}
			return a.getAIGrant(w, r, membership.ID, parts[1])
		}
		if len(parts) == 3 && parts[2] == "revoke" {
			if err := requireMethod(w, r, http.MethodPost); err != nil {
				return err
			}
			if membership.Role != "admin" {
				return errForbidden
			}
			return a.revokeAIGrant(w, r, membership.ID, session, parts[1])
		}
	}
	return errNotFound
}

func (a *Application) authorizeAIAdmin(ctx context.Context, tx pgx.Tx, workspace string, session authenticatedSession) error {
	// Workspace first matches membership changes and also serializes the
	// initially absent policy row. Configuration locks follow profile, policy,
	// then grant whenever a transaction needs more than one of those rows.
	var id string
	if err := tx.QueryRow(ctx, `SELECT id FROM `+a.table("workspaces")+` WHERE id=$1 FOR UPDATE`,
		workspace).Scan(&id); err != nil {
		return err
	}
	role, err := a.sessionRole(ctx, tx, session, workspace)
	if err != nil {
		return err
	}
	if role != "admin" {
		return errForbidden
	}
	return nil
}
