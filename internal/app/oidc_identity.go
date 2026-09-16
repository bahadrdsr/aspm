package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/mail"
	"strings"

	"github.com/jackc/pgx/v5"
)

var errOIDCLinkRequired = &apiError{409, "conflict", "This identity requires explicit account linking"}

type oidcProfile struct {
	Email           string `json:"email"`
	Name            string `json:"name"`
	EmailVerified   bool   `json:"email_verified"`
	AuthorizedParty string `json:"azp"`
}

func (p *oidcProfile) validate() error {
	p.Email = strings.ToLower(strings.TrimSpace(p.Email))
	address, err := mail.ParseAddress(p.Email)
	if err != nil || !p.EmailVerified || len(p.Email) > 254 || address.Address != p.Email {
		return errUnauthorized
	}
	if p.Name == "" {
		p.Name = p.Email
	}
	if !validText(p.Name, 256) {
		return errUnauthorized
	}
	return nil
}

func (a *Application) oidcUser(ctx context.Context, tx pgx.Tx, subject string, profile oidcProfile) (User, error) {
	issuer := a.oidc.config.Issuer
	identity, _ := json.Marshal([]string{issuer, subject})
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`,
		"aspm/app/oidc/"+a.config.Schema+"/"+reportDigest(identity)); err != nil {
		return User{}, err
	}
	var user User
	var method string
	err := tx.QueryRow(ctx, `SELECT u.id,u.name,u.email,u.auth_method FROM `+a.table("oidc_identities")+` i
		JOIN `+a.table("users")+` u ON u.id=i.user_id WHERE i.issuer=$1 AND i.subject=$2`,
		issuer, subject).Scan(&user.ID, &user.Name, &user.Email, &method)
	if err == nil {
		if method != "oidc" {
			return User{}, errOIDCLinkRequired
		}
		if profile.Email != user.Email {
			var conflict bool
			if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM `+a.table("users")+` WHERE email=$1 AND id<>$2)`,
				profile.Email, user.ID).Scan(&conflict); err != nil {
				return User{}, err
			}
			if conflict {
				return User{}, errOIDCLinkRequired
			}
		}
		// A known subject retains its application profile and explicit
		// memberships. No email, role, or group claim rewrites its authority.
		return user, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return User{}, err
	}
	var emailExists bool
	if err = tx.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM `+a.table("users")+` WHERE email=$1)`,
		profile.Email).Scan(&emailExists); err != nil {
		return User{}, err
	}
	if emailExists {
		return User{}, errOIDCLinkRequired
	}
	var workspace string
	if err = tx.QueryRow(ctx, `SELECT id FROM `+a.table("workspaces")+` WHERE id=$1 FOR KEY SHARE`,
		a.oidc.config.WorkspaceID).Scan(&workspace); err != nil {
		return User{}, err
	}
	user = User{ID: newID(), Name: profile.Name, Email: profile.Email}
	now := a.config.Now().UTC()
	if _, err = tx.Exec(ctx, `INSERT INTO `+a.table("users")+`
		(id,name,email,password_hash,auth_method,created_at) VALUES($1,$2,$3,NULL,'oidc',$4)`,
		user.ID, user.Name, user.Email, now); err != nil {
		return User{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO `+a.table("oidc_identities")+`(issuer,subject,user_id,created_at)
		VALUES($1,$2,$3,$4)`, issuer, subject, user.ID, now); err != nil {
		return User{}, err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO `+a.table("memberships")+`(workspace_id,user_id,role) VALUES($1,$2,'viewer')`,
		workspace, user.ID); err != nil {
		return User{}, err
	}
	return user, nil
}
