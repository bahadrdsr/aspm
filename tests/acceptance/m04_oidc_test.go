//go:build integration

package acceptance

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"
)

type oidcFixture struct {
	server                 *httptest.Server
	key                    *rsa.PrivateKey
	nonce, challenge, mode string
	clientID, clientSecret string
}

func enableOIDC(t *testing.T, h *harness) *oidcFixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	ok(t, "generate local identity-provider fixture key", err)
	p := &oidcFixture{key: key, clientID: "aspm-acceptance", clientSecret: secret(t)}
	p.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/.well-known/openid-configuration":
			_ = json.NewEncoder(w).Encode(object{
				"issuer": p.server.URL, "authorization_endpoint": p.server.URL + "/authorize",
				"token_endpoint": p.server.URL + "/token", "jwks_uri": p.server.URL + "/keys",
				"response_types_supported": []string{"code"}, "subject_types_supported": []string{"public"},
				"id_token_signing_alg_values_supported": []string{"RS256"},
				"code_challenge_methods_supported":      []string{"S256"},
			})
		case "/keys":
			e := big.NewInt(int64(p.key.PublicKey.E))
			_ = json.NewEncoder(w).Encode(object{"keys": []object{{
				"kty": "RSA", "kid": "synthetic-oidc-key", "use": "sig", "alg": "RS256",
				"n": base64.RawURLEncoding.EncodeToString(p.key.N.Bytes()), "e": base64.RawURLEncoding.EncodeToString(e.Bytes()),
			}}})
		case "/token":
			if r.Method != "POST" {
				t.Error("OIDC token exchange must use POST")
			}
			ok(t, "parse fixture token request", r.ParseForm())
			clientID, clientSecret, basic := r.BasicAuth()
			if !basic {
				clientID, clientSecret = r.Form.Get("client_id"), r.Form.Get("client_secret")
			}
			if clientID != p.clientID || clientSecret != p.clientSecret || r.Form.Get("grant_type") != "authorization_code" {
				t.Error("OIDC token client authentication or grant mapping is incorrect")
			}
			verifier := r.Form.Get("code_verifier")
			digest := sha256.Sum256([]byte(verifier))
			if len(verifier) < 43 || base64.RawURLEncoding.EncodeToString(digest[:]) != p.challenge {
				t.Error("OIDC code exchange lost its PKCE S256 verifier")
			}
			nonceValue, email := p.nonce, "sso-user@example.invalid"
			if p.mode == "wrong-nonce" {
				nonceValue = "not-the-approved-nonce"
			}
			if p.mode == "local-email" {
				email = h.admin.user.Email
			}
			claims := object{
				"iss": p.server.URL, "sub": "synthetic-oidc-subject", "aud": p.clientID,
				"exp": time.Now().Add(time.Hour).Unix(), "iat": time.Now().Add(-time.Minute).Unix(),
				"nonce": nonceValue, "email": email, "email_verified": p.mode != "unverified-email",
				"name": "Synthetic SSO user", "role": "admin", "groups": []string{"administrators"},
			}
			header := base64.RawURLEncoding.EncodeToString(encode(t, object{"alg": "RS256", "kid": "synthetic-oidc-key"}))
			payload := header + "." + base64.RawURLEncoding.EncodeToString(encode(t, claims))
			sum := sha256.Sum256([]byte(payload))
			signature, err := rsa.SignPKCS1v15(rand.Reader, p.key, crypto.SHA256, sum[:])
			ok(t, "sign owned fixture identity token", err)
			_ = json.NewEncoder(w).Encode(object{
				"access_token": "synthetic-unused-access-token", "token_type": "Bearer", "expires_in": 3600,
				"id_token": payload + "." + base64.RawURLEncoding.EncodeToString(signature),
			})
		default:
			t.Error("application requested an undeclared identity-provider endpoint")
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(p.server.Close)
	h.services.cfg.OIDC = &OIDCConfig{Issuer: p.server.URL, ClientID: p.clientID, ClientSecret: p.clientSecret, WorkspaceID: h.admin.workspace, Client: p.server.Client()}
	h.restart()
	return p
}

func startOIDC(t *testing.T, h *harness, p *oidcFixture) (url.Values, []*http.Cookie) {
	t.Helper()
	response := h.request(actor{}, "GET", "/api/v1/auth/oidc/start", nil, 0)
	if response.Code != http.StatusFound && response.Code != http.StatusSeeOther {
		t.Fatalf("OIDC start returned %d; expected an authorization redirect", response.Code)
	}
	destination, err := url.Parse(response.Header().Get("Location"))
	ok(t, "parse authorization location", err)
	if destination.Scheme+"://"+destination.Host != p.server.URL || destination.Path != "/authorize" {
		t.Fatal("OIDC start redirected outside the configured authorization endpoint")
	}
	q := destination.Query()
	if q.Get("state") == "" || q.Get("nonce") == "" || q.Get("code_challenge") == "" ||
		q.Get("code_challenge_method") != "S256" || q.Get("response_type") != "code" ||
		q.Get("redirect_uri") != "https://aspm.test/api/v1/auth/oidc/callback" {
		t.Fatal("OIDC state, nonce, PKCE or callback binding is missing")
	}
	if strings.Contains(destination.String(), p.clientSecret) {
		t.Fatal("OIDC authorization URL exposed the client secret")
	}
	p.nonce, p.challenge = q.Get("nonce"), q.Get("code_challenge")
	cookies := response.Result().Cookies()
	if len(cookies) == 0 {
		t.Fatal("OIDC flow needs browser binding, not state alone")
	}
	for _, cookie := range cookies {
		if cookie.Value != "" && (!cookie.HttpOnly || !cookie.Secure) {
			t.Fatal("OIDC browser-binding cookies must be protected")
		}
	}
	return q, cookies
}

func oidcCallback(h *harness, state string, cookies []*http.Cookie) *httptest.ResponseRecorder {
	r := httptest.NewRequest("GET", "https://aspm.test/api/v1/auth/oidc/callback?code=synthetic-code&state="+url.QueryEscape(state), nil).WithContext(h.services.ctx)
	for _, cookie := range cookies {
		r.AddCookie(cookie)
	}
	response := httptest.NewRecorder()
	h.app.Handler.ServeHTTP(response, r)
	return response
}

func TestM04_OIDCUsesVerifiedSubjectAndLeastPrivilege(t *testing.T) {
	h := newHarness(t, true)
	provider := enableOIDC(t, h)
	q, cookies := startOIDC(t, h, provider)
	response := oidcCallback(h, q.Get("state"), cookies)
	if response.Code != 302 && response.Code != 303 {
		t.Fatalf("verified OIDC callback returned %d", response.Code)
	}
	var sessionCookie *http.Cookie
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == "aspm_session" {
			sessionCookie = cookie
		}
	}
	if sessionCookie == nil || !sessionCookie.HttpOnly || !sessionCookie.Secure {
		t.Fatal("OIDC login did not create a protected application session")
	}
	sso := actor{workspace: h.admin.workspace, cookie: sessionCookie}
	session := h.json(sso, "GET", "/api/v1/session", nil, 200)
	if session.User.ID == "" || session.User.ID == h.admin.user.ID {
		t.Fatal("OIDC subject was not assigned its own identity")
	}
	equal(t, "default OIDC enrollment ignores privileged group/role claims", session.Workspaces[0].Role, "viewer")
	h.denied(sso, "POST", "/api/v1/assets", object{"name": "Cannot elevate via ID token", "kind": "repository"}, 403, "forbidden")
	replay := oidcCallback(h, q.Get("state"), cookies)
	if replay.Code < 400 {
		t.Fatal("consumed OIDC authorization flow was replayable")
	}
}

func TestM04_OIDCDeniesUnboundOrUnverifiedIdentities(t *testing.T) {
	for _, mode := range []string{"wrong-state", "wrong-nonce", "unverified-email", "local-email"} {
		t.Run(mode, func(t *testing.T) {
			h := newHarness(t, true)
			provider := enableOIDC(t, h)
			provider.mode = mode
			q, cookies := startOIDC(t, h, provider)
			state := q.Get("state")
			if mode == "wrong-state" {
				state = "unbound-synthetic-state"
			}
			response := oidcCallback(h, state, cookies)
			if response.Code < 400 || response.Code >= 500 {
				t.Fatalf("untrusted OIDC case %s returned %d; expected an explicit rejection", mode, response.Code)
			}
			for _, cookie := range response.Result().Cookies() {
				if cookie.Name == "aspm_session" && cookie.Value != "" && cookie.MaxAge >= 0 {
					t.Fatal("OIDC rejection issued an application session")
				}
			}
			equal(t, "local administrator survives rejected identity linking", h.json(h.admin, "GET", "/api/v1/session", nil, 200).User.ID, h.admin.user.ID)
		})
	}
}
