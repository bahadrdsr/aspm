package app

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/jackc/pgx/v5"
	"golang.org/x/oauth2"
)

const (
	oidcCallbackPath = "/api/v1/auth/oidc/callback"
	oidcFlowCookie   = "__Host-aspm_oidc"
	oidcFlowTTL      = 5 * time.Minute
)

func (a *Application) oidcCallbackURL(r *http.Request) (string, error) {
	origin := a.config.PublicOrigin
	if origin == "" {
		if r.TLS == nil {
			return "", errForbidden
		}
		origin = "https://" + r.Host
	}
	u, err := url.Parse(origin)
	if err != nil || !validOIDCURL(origin, false) || u.Path != "" {
		return "", errForbidden
	}
	return origin + oidcCallbackPath, nil
}

func oidcCookie(value string, expires time.Time, maxAge int) *http.Cookie {
	return &http.Cookie{Name: oidcFlowCookie, Value: value, Path: "/", Secure: true, HttpOnly: true,
		SameSite: http.SameSiteLaxMode, Expires: expires, MaxAge: maxAge}
}

func validOIDCOpaque(value string) bool {
	if len(value) != 43 {
		return false
	}
	decoded, err := base64.RawURLEncoding.DecodeString(value)
	return err == nil && len(decoded) == 32
}

func (a *Application) startOIDC(w http.ResponseWriter, r *http.Request) error {
	if a.oidc == nil {
		return errNotFound
	}
	if !a.oidc.valid {
		return errOIDCUnavailable
	}
	if r.URL.RawQuery != "" {
		return errInvalid
	}
	redirect, err := a.oidcCallbackURL(r)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(r.Context(), oidcRequestTime)
	defer cancel()
	allowed, err := a.loginAllowed(r.WithContext(ctx))
	if err != nil {
		return err
	}
	if !allowed {
		return errUnauthorized
	}
	var exists bool
	if err = a.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM `+a.table("workspaces")+` WHERE id=$1)`,
		a.oidc.config.WorkspaceID).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return errOIDCUnavailable
	}
	provider, err := a.oidc.discover(ctx)
	if err != nil {
		return err
	}
	state, nonce, verifier := randomToken(), randomToken(), oauth2.GenerateVerifier()
	stateHash, browserHash, nonceHash := sha256.Sum256([]byte(state)), sha256.Sum256([]byte(verifier)), sha256.Sum256([]byte(nonce))
	now := a.config.Now().UTC()
	// The verifier stays solely in a protected, host-only browser cookie. The
	// durable flow stores only its hash, so reopening needs no ephemeral key.
	if _, err = a.pool.Exec(ctx, `WITH expired AS (
		SELECT state_hash FROM `+a.table("oidc_flows")+` WHERE expires_at<=$1
		ORDER BY expires_at FOR UPDATE SKIP LOCKED LIMIT 100)
		DELETE FROM `+a.table("oidc_flows")+` flow USING expired e WHERE flow.state_hash=e.state_hash`, now); err != nil {
		return err
	}
	if _, err = a.pool.Exec(ctx, `INSERT INTO `+a.table("oidc_flows")+`
		(state_hash,browser_hash,nonce_hash,issuer,client_id,workspace_id,redirect_uri,created_at,expires_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, stateHash[:], browserHash[:], nonceHash[:],
		a.oidc.config.Issuer, a.oidc.config.ClientID, a.oidc.config.WorkspaceID, redirect, now, now.Add(oidcFlowTTL)); err != nil {
		return err
	}
	oauth := a.oidc.oauthConfig(provider, redirect)
	destination := oauth.AuthCodeURL(state, oidc.Nonce(nonce), oauth2.S256ChallengeOption(verifier),
		oauth2.SetAuthURLParam("response_mode", "query"))
	http.SetCookie(w, oidcCookie(verifier, now.Add(oidcFlowTTL), int(oidcFlowTTL.Seconds())))
	http.Redirect(w, r, destination, http.StatusFound)
	return nil
}

func (s *oidcAuth) oauthConfig(provider *discoveredOIDC, redirect string) oauth2.Config {
	return oauth2.Config{
		ClientID: s.config.ClientID, ClientSecret: s.config.ClientSecret,
		RedirectURL: redirect, Endpoint: provider.endpoint,
		Scopes: []string{oidc.ScopeOpenID, "email", "profile"},
	}
}

func (a *Application) oidcCallback(w http.ResponseWriter, r *http.Request) error {
	if a.oidc == nil {
		return errNotFound
	}
	http.SetCookie(w, oidcCookie("", time.Unix(1, 0).UTC(), -1))
	if !a.oidc.valid {
		return errOIDCUnavailable
	}
	redirect, err := a.oidcCallbackURL(r)
	if err != nil {
		return err
	}
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil || len(r.URL.RawQuery) > 8192 {
		return errInvalid
	}
	for key, values := range query {
		switch key {
		case "state", "code", "iss", "session_state", "error", "error_description", "error_uri":
		default:
			return errInvalid
		}
		if len(values) != 1 {
			return errInvalid
		}
	}
	state := query.Get("state")
	cookies := r.CookiesNamed(oidcFlowCookie)
	if !validOIDCOpaque(state) || len(cookies) != 1 || !validOIDCOpaque(cookies[0].Value) {
		return errUnauthorized
	}
	if query.Has("iss") && query.Get("iss") != a.oidc.config.Issuer {
		return errUnauthorized
	}
	ctx, cancel := context.WithTimeout(r.Context(), oidcRequestTime)
	defer cancel()
	stateHash, browserHash := sha256.Sum256([]byte(state)), sha256.Sum256([]byte(cookies[0].Value))
	var nonceHash []byte
	// Consume before network access, including failed exchanges. Two callbacks
	// can never exchange or authenticate the same durable browser-bound flow.
	err = a.pool.QueryRow(ctx, `DELETE FROM `+a.table("oidc_flows")+`
		WHERE state_hash=$1 AND browser_hash=$2 AND issuer=$3 AND client_id=$4 AND workspace_id=$5
		AND redirect_uri=$6 AND expires_at>$7 RETURNING nonce_hash`,
		stateHash[:], browserHash[:], a.oidc.config.Issuer, a.oidc.config.ClientID,
		a.oidc.config.WorkspaceID, redirect, a.config.Now().UTC()).Scan(&nonceHash)
	if errors.Is(err, pgx.ErrNoRows) {
		return errUnauthorized
	}
	if err != nil {
		return err
	}
	code := query.Get("code")
	if query.Has("error") || !validText(code, 4096) || strings.ContainsAny(code, "\r\n") {
		return errUnauthorized
	}
	provider, err := a.oidc.discover(ctx)
	if err != nil {
		return err
	}
	oauth := a.oidc.oauthConfig(provider, redirect)
	token, err := oauth.Exchange(oidc.ClientContext(ctx, a.oidc.client), code, oauth2.VerifierOption(cookies[0].Value))
	if err != nil {
		var rejected *oauth2.RetrieveError
		if errors.As(err, &rejected) && rejected.Response != nil && rejected.Response.StatusCode >= 400 && rejected.Response.StatusCode < 500 {
			return errUnauthorized
		}
		return errOIDCUnavailable
	}
	raw, present := token.Extra("id_token").(string)
	if !present || len(raw) > 64<<10 {
		return errUnauthorized
	}
	identity, err := provider.verifier.Verify(ctx, raw)
	if err != nil || identity.Issuer != a.oidc.config.Issuer || !validText(identity.Subject, 255) {
		return errUnauthorized
	}
	actualNonce := sha256.Sum256([]byte(identity.Nonce))
	if identity.Nonce == "" || subtle.ConstantTimeCompare(nonceHash, actualNonce[:]) != 1 {
		return errUnauthorized
	}
	var profile oidcProfile
	if identity.Claims(&profile) != nil || !profile.EmailVerified ||
		(profile.AuthorizedParty != "" && profile.AuthorizedParty != a.oidc.config.ClientID) ||
		(len(identity.Audience) > 1 && profile.AuthorizedParty != a.oidc.config.ClientID) {
		return errUnauthorized
	}
	if err = profile.validate(); err != nil {
		return err
	}
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	user, err := a.oidcUser(ctx, tx, identity.Subject, profile)
	if err != nil {
		return err
	}
	session, err := a.issueSession(ctx, tx, r, user.ID)
	if err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	http.SetCookie(w, session.cookie)
	http.Redirect(w, r, "/", http.StatusSeeOther)
	return nil
}
