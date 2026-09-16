package app

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/mail"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"golang.org/x/crypto/argon2"
)

const passwordParameters = "m=65536,t=3,p=4"

type authenticatedSession struct {
	User      User
	ExpiresAt time.Time
	TokenHash []byte
}

func randomToken() string {
	var data [32]byte
	_, _ = rand.Read(data[:])
	return base64.RawURLEncoding.EncodeToString(data[:])
}

func newID() string {
	var data [16]byte
	_, _ = rand.Read(data[:])
	return hex.EncodeToString(data[:])
}

func validID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, c := range id {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

func (a *Application) derivePassword(ctx context.Context, password string, salt []byte) ([]byte, error) {
	select {
	case a.hashSlots <- struct{}{}:
		defer func() { <-a.hashSlots }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return argon2.IDKey([]byte(password), salt, 3, 64*1024, 4, 32), nil
}

func (a *Application) hashPassword(ctx context.Context, password string) (string, error) {
	var salt [16]byte
	_, _ = rand.Read(salt[:])
	key, err := a.derivePassword(ctx, password, salt[:])
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("$argon2id$v=%d$%s$%s$%s", argon2.Version, passwordParameters,
		base64.RawStdEncoding.EncodeToString(salt[:]), base64.RawStdEncoding.EncodeToString(key)), nil
}

func (a *Application) checkPassword(ctx context.Context, password, encoded string) bool {
	fields := strings.Split(encoded, "$")
	if len(fields) != 6 || fields[1] != "argon2id" || fields[2] != "v=19" || fields[3] != passwordParameters {
		return false
	}
	salt, e1 := base64.RawStdEncoding.DecodeString(fields[4])
	want, e2 := base64.RawStdEncoding.DecodeString(fields[5])
	if e1 != nil || e2 != nil || len(salt) != 16 || len(want) != 32 {
		return false
	}
	actual, err := a.derivePassword(ctx, password, salt)
	return err == nil && subtle.ConstantTimeCompare(want, actual) == 1
}

func validText(value string, limit int) bool {
	return strings.TrimSpace(value) != "" && len(value) <= limit && !strings.ContainsRune(value, 0)
}

func localIdentity(name, email, password string) (string, error) {
	email = strings.ToLower(strings.TrimSpace(email))
	address, err := mail.ParseAddress(email)
	if !validText(name, 256) || len(email) > 254 || err != nil || address.Address != email ||
		len([]rune(password)) < 12 || len(password) > 1024 || strings.ContainsRune(password, 0) {
		return "", errInvalid
	}
	return email, nil
}

func validRole(role string) bool {
	return role == "admin" || role == "analyst" || role == "viewer"
}

func (a *Application) bootstrap(w http.ResponseWriter, r *http.Request) error {
	given := sha256.Sum256([]byte(r.Header.Get("X-ASPM-Bootstrap-Token")))
	if !a.bootstrapEnabled || subtle.ConstantTimeCompare(a.bootstrapHash[:], given[:]) != 1 {
		return errUnauthorized
	}
	var input struct {
		WorkspaceName string `json:"workspaceName"`
		Name          string `json:"name"`
		Email         string `json:"email"`
		Password      string `json:"password"`
	}
	if err := a.decode(w, r, &input, 64<<10); err != nil {
		return err
	}
	email, err := localIdentity(input.Name, input.Email, input.Password)
	if err != nil || !validText(input.WorkspaceName, 256) {
		return errInvalid
	}
	var enrolled bool
	if err = a.pool.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM `+a.table("bootstrap")+`)`).Scan(&enrolled); err != nil {
		return err
	}
	if enrolled {
		return errConflict
	}
	passwordHash, err := a.hashPassword(r.Context(), input.Password)
	if err != nil {
		return err
	}
	tx, err := a.pool.Begin(r.Context())
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "aspm/app/bootstrap/"+a.config.Schema); err != nil {
		return err
	}
	if err = tx.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM `+a.table("bootstrap")+`)`).Scan(&enrolled); err != nil {
		return err
	}
	if enrolled {
		return errConflict
	}
	user := User{ID: newID(), Name: input.Name, Email: email}
	workspace := Workspace{ID: newID(), Name: input.WorkspaceName, Role: "admin"}
	now := a.config.Now().UTC()
	if _, err = tx.Exec(r.Context(), `INSERT INTO `+a.table("users")+`
		(id,name,email,password_hash,created_at) VALUES($1,$2,$3,$4,$5)`, user.ID, user.Name, user.Email, passwordHash, now); err != nil {
		return err
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO `+a.table("workspaces")+`(id,name,created_at) VALUES($1,$2,$3)`,
		workspace.ID, workspace.Name, now); err != nil {
		return err
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO `+a.table("memberships")+`(workspace_id,user_id,role) VALUES($1,$2,'admin')`,
		workspace.ID, user.ID); err != nil {
		return err
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO `+a.table("bootstrap")+`(singleton,user_id,workspace_id,enrolled_at) VALUES(true,$1,$2,$3)`,
		user.ID, workspace.ID, now); err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	writeJSON(w, 201, map[string]any{"user": user, "workspace": workspace})
	return nil
}

// Fixed buckets bound throttle storage even for arbitrary usernames and client
// addresses. Forwarded client IP headers cannot evade the server-side limit.
func (a *Application) loginAllowed(r *http.Request) (bool, error) {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	hash := sha256.Sum256([]byte(host))
	bucket := (int(hash[0])<<8 | int(hash[1])) % 4096
	var attempts int
	err = a.pool.QueryRow(r.Context(), `INSERT INTO `+a.table("auth_throttle")+` AS t
		(bucket,window_at,attempts) VALUES($1,$2,1) ON CONFLICT(bucket) DO UPDATE SET
		attempts=CASE WHEN t.window_at <= $2::timestamptz-interval '1 minute' THEN 1 ELSE t.attempts+1 END,
		window_at=CASE WHEN t.window_at <= $2::timestamptz-interval '1 minute' THEN $2 ELSE t.window_at END
		RETURNING attempts`, bucket, a.config.Now().UTC()).Scan(&attempts)
	return attempts <= 30, err
}

func (a *Application) login(w http.ResponseWriter, r *http.Request) error {
	var input struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := a.decode(w, r, &input, 16<<10); err != nil {
		return err
	}
	if len(input.Email) > 254 || len(input.Password) > 1024 {
		return errUnauthorized
	}
	allowed, err := a.loginAllowed(r)
	if err != nil {
		return err
	}
	if !allowed {
		return errUnauthorized
	}
	var user User
	var hash string
	err = a.pool.QueryRow(r.Context(), `SELECT id,name,email,password_hash FROM `+a.table("users")+` WHERE email=$1 AND auth_method='local'`,
		strings.ToLower(strings.TrimSpace(input.Email))).Scan(&user.ID, &user.Name, &user.Email, &hash)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	found := err == nil
	if !found {
		hash = a.dummyPassword
	}
	valid := a.checkPassword(r.Context(), input.Password, hash)
	if !found || !valid {
		return errUnauthorized
	}
	tx, err := a.pool.Begin(r.Context())
	if err != nil {
		return err
	}
	defer rollback(tx)
	session, err := a.issueSession(r.Context(), tx, r, user.ID)
	if err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	workspaces, err := a.memberships(r.Context(), user.ID)
	if err != nil {
		return err
	}
	http.SetCookie(w, session.cookie)
	writeJSON(w, 200, map[string]any{"user": user, "workspaces": workspaces, "expiresAt": session.expires})
	return nil
}

type issuedSession struct {
	cookie  *http.Cookie
	expires time.Time
}

// Every authentication method rotates into the same server-side session store.
// Only the digest is persisted; the secret is returned solely in a cookie.
func (a *Application) issueSession(ctx context.Context, tx pgx.Tx, r *http.Request, userID string) (issuedSession, error) {
	token := randomToken()
	tokenHash := sha256.Sum256([]byte(token))
	now := a.config.Now().UTC()
	expires := now.Add(a.config.SessionTTL)
	if old, err := r.Cookie("aspm_session"); err == nil {
		oldHash := sha256.Sum256([]byte(old.Value))
		if _, err = tx.Exec(ctx, `UPDATE `+a.table("sessions")+` SET revoked_at=$3
			WHERE token_hash=$1 AND user_id=$2 AND revoked_at IS NULL`, oldHash[:], userID, now); err != nil {
			return issuedSession{}, err
		}
	}
	if _, err := tx.Exec(ctx, `DELETE FROM `+a.table("sessions")+` WHERE user_id=$1 AND (expires_at<=$2 OR revoked_at IS NOT NULL)`,
		userID, now); err != nil {
		return issuedSession{}, err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO `+a.table("sessions")+`(token_hash,user_id,created_at,expires_at)
		VALUES($1,$2,$3,$4)`, tokenHash[:], userID, now, expires); err != nil {
		return issuedSession{}, err
	}
	return issuedSession{cookie: sessionCookie(token, expires, int(a.config.SessionTTL.Seconds())), expires: expires}, nil
}

func sessionCookie(value string, expires time.Time, maxAge int) *http.Cookie {
	return &http.Cookie{Name: "aspm_session", Value: value, HttpOnly: true, Secure: true,
		Path: "/", SameSite: http.SameSiteLaxMode, Expires: expires, MaxAge: maxAge}
}

func (a *Application) authenticate(r *http.Request) (authenticatedSession, error) {
	cookies := r.CookiesNamed("aspm_session")
	if len(cookies) != 1 {
		return authenticatedSession{}, errUnauthorized
	}
	cookie := cookies[0]
	raw, err := base64.RawURLEncoding.DecodeString(cookie.Value)
	if err != nil || len(raw) != 32 || len(cookie.Value) != 43 {
		return authenticatedSession{}, errUnauthorized
	}
	hash := sha256.Sum256([]byte(cookie.Value))
	session := authenticatedSession{TokenHash: hash[:]}
	err = a.pool.QueryRow(r.Context(), `SELECT u.id,u.name,u.email,s.expires_at FROM `+a.table("sessions")+` s
		JOIN `+a.table("users")+` u ON u.id=s.user_id WHERE s.token_hash=$1 AND s.revoked_at IS NULL
		AND s.expires_at>$2`, hash[:], a.config.Now().UTC()).Scan(
		&session.User.ID, &session.User.Name, &session.User.Email, &session.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return authenticatedSession{}, errUnauthorized
	}
	return session, err
}

func (a *Application) memberships(ctx context.Context, userID string) ([]Workspace, error) {
	rows, err := a.pool.Query(ctx, `SELECT w.id,w.name,m.role FROM `+a.table("memberships")+` m
		JOIN `+a.table("workspaces")+` w ON w.id=m.workspace_id WHERE m.user_id=$1 ORDER BY w.id`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []Workspace{}
	for rows.Next() {
		var workspace Workspace
		if err := rows.Scan(&workspace.ID, &workspace.Name, &workspace.Role); err != nil {
			return nil, err
		}
		items = append(items, workspace)
	}
	return items, rows.Err()
}

func (a *Application) selectWorkspace(r *http.Request, userID string) (Workspace, error) {
	selected := r.Header.Get("X-ASPM-Workspace-ID")
	if selected == "" {
		// Only a single membership can be selected implicitly.
		items, err := a.memberships(r.Context(), userID)
		if err != nil {
			return Workspace{}, err
		}
		if len(items) != 1 {
			return Workspace{}, errForbidden
		}
		return items[0], nil
	}
	var membership Workspace
	err := a.pool.QueryRow(r.Context(), `SELECT w.id,w.name,m.role FROM `+a.table("memberships")+` m
		JOIN `+a.table("workspaces")+` w ON w.id=m.workspace_id WHERE m.workspace_id=$1 AND m.user_id=$2`,
		selected, userID).Scan(&membership.ID, &membership.Name, &membership.Role)
	if errors.Is(err, pgx.ErrNoRows) {
		return Workspace{}, errForbidden
	}
	return membership, err
}

func (a *Application) sessionResponse(w http.ResponseWriter, r *http.Request, session authenticatedSession) error {
	items, err := a.memberships(r.Context(), session.User.ID)
	if err != nil {
		return err
	}
	writeJSON(w, 200, map[string]any{"user": session.User, "workspaces": items, "expiresAt": session.ExpiresAt})
	return nil
}

func (a *Application) logout(w http.ResponseWriter, r *http.Request, session authenticatedSession) error {
	if _, err := a.pool.Exec(r.Context(), `UPDATE `+a.table("sessions")+` SET revoked_at=$2 WHERE token_hash=$1`,
		session.TokenHash, a.config.Now().UTC()); err != nil {
		return err
	}
	http.SetCookie(w, sessionCookie("", time.Unix(1, 0).UTC(), -1))
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (a *Application) createWorkspace(w http.ResponseWriter, r *http.Request, user User) error {
	var input struct {
		Name string `json:"name"`
	}
	if err := a.decode(w, r, &input, 16<<10); err != nil {
		return err
	}
	if !validText(input.Name, 256) {
		return errInvalid
	}
	workspace := Workspace{ID: newID(), Name: input.Name, Role: "admin"}
	tx, err := a.pool.Begin(r.Context())
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err = tx.Exec(r.Context(), `INSERT INTO `+a.table("workspaces")+`(id,name,created_at) VALUES($1,$2,$3)`,
		workspace.ID, workspace.Name, a.config.Now().UTC()); err != nil {
		return err
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO `+a.table("memberships")+`(workspace_id,user_id,role) VALUES($1,$2,'admin')`,
		workspace.ID, user.ID); err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	writeJSON(w, 201, map[string]any{"workspace": workspace})
	return nil
}

func (a *Application) createUser(w http.ResponseWriter, r *http.Request, workspace Workspace) error {
	var input struct {
		Name     string `json:"name"`
		Email    string `json:"email"`
		Password string `json:"password"`
		Role     string `json:"role"`
	}
	if err := a.decode(w, r, &input, 16<<10); err != nil {
		return err
	}
	email, err := localIdentity(input.Name, input.Email, input.Password)
	if err != nil || !validRole(input.Role) {
		return errInvalid
	}
	hash, err := a.hashPassword(r.Context(), input.Password)
	if err != nil {
		return err
	}
	user := User{ID: newID(), Name: input.Name, Email: email}
	tx, err := a.pool.Begin(r.Context())
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err = tx.Exec(r.Context(), `INSERT INTO `+a.table("users")+`(id,name,email,password_hash,created_at) VALUES($1,$2,$3,$4,$5)`,
		user.ID, user.Name, user.Email, hash, a.config.Now().UTC()); err != nil {
		return err
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO `+a.table("memberships")+`(workspace_id,user_id,role) VALUES($1,$2,$3)`,
		workspace.ID, user.ID, input.Role); err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	writeJSON(w, 201, map[string]any{"user": user})
	return nil
}
