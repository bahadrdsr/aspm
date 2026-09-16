//go:build integration

package acceptance

import (
	"net/http"
	"slices"
	"testing"
	"time"
)

func TestM04_BootstrapRequiresOutOfBandTokenAndIsOneTime(t *testing.T) {
	h := newHarness(t, false)
	h.denied(actor{}, "POST", "/api/v1/bootstrap", h.enrollment(), 401, "unauthorized")
	r := h.decode(h.request(actor{}, "POST", "/api/v1/bootstrap", encode(t, h.enrollment()), 401,
		"X-ASPM-Bootstrap-Token", secret(t)))
	equal(t, "wrong bootstrap token", r.Error.Code, "unauthorized")
	h.enroll()
	session := h.json(h.admin, "GET", "/api/v1/session", nil, 200)
	equal(t, "bootstrap admin membership", len(session.Workspaces), 1)
	equal(t, "bootstrap role", session.Workspaces[0].Role, "admin")
	h.restart()
	r = h.decode(h.request(actor{}, "POST", "/api/v1/bootstrap", encode(t, h.enrollment()), 409,
		"X-ASPM-Bootstrap-Token", h.services.cfg.BootstrapToken))
	equal(t, "bootstrap remains closed after reopen", r.Error.Code, "conflict")
	equal(t, "original administrator retained", h.json(h.admin, "GET", "/api/v1/session", nil, 200).User.ID, h.admin.user.ID)
}

func TestM04_LoginLogoutInvalidAndExpiredSessions(t *testing.T) {
	h := newHarness(t, true)
	badLogin := h.request(actor{}, "POST", "/api/v1/login", encode(t, object{"email": h.admin.user.Email, "password": secret(t)}), 401)
	equal(t, "invalid login", h.decode(badLogin).Error.Code, "unauthorized")
	for _, c := range badLogin.Result().Cookies() {
		if c.Name == "aspm_session" && c.Value != "" && c.MaxAge >= 0 {
			t.Fatal("invalid login issued an active session cookie")
		}
	}
	h.denied(actor{}, "GET", "/api/v1/session", nil, 401, "unauthorized")
	invalid := h.admin
	invalid.cookie = &http.Cookie{Name: "aspm_session", Value: nonce(t)}
	h.denied(invalid, "GET", "/api/v1/assets", nil, 401, "unauthorized")
	h.denied(invalid, "GET", "/api/v1/session", nil, 401, "unauthorized")
	equal(t, "authenticated identity", h.json(h.admin, "GET", "/api/v1/session", nil, 200).User.ID, h.admin.user.ID)
	rr := h.request(h.admin, "POST", "/api/v1/logout", nil, 204)
	cleared := false
	for _, c := range rr.Result().Cookies() {
		cleared = cleared || (c.Name == "aspm_session" && c.MaxAge < 0)
	}
	if !cleared {
		t.Fatal("logout must clear the browser session cookie")
	}
	h.denied(h.admin, "GET", "/api/v1/session", nil, 401, "unauthorized")
	h.denied(h.admin, "GET", "/api/v1/assets", nil, 401, "unauthorized")
	fresh := h.login(h.admin.user.Email, h.password, h.admin.workspace)
	h.clock.Add(int64(h.services.cfg.SessionTTL + time.Second))
	h.denied(fresh, "GET", "/api/v1/session", nil, 401, "unauthorized")
	h.denied(fresh, "POST", "/api/v1/assets", object{"name": "Cannot create after expiry", "kind": "repository"}, 401, "unauthorized")
}

func TestM04_RolesAndWorkspacesEnforceReadAndWriteIsolation(t *testing.T) {
	h := newHarness(t, true)
	viewer, analyst := h.addUser(h.admin, "viewer"), h.addUser(h.admin, "analyst")
	second := h.addWorkspace()
	other := h.addUser(second, "admin")
	a := h.asset(h.admin, "Workspace A repository", nil)
	b := h.asset(second, "Workspace B repository", &other.user.ID)
	equal(t, "viewer can read", h.json(viewer, "GET", "/api/v1/assets/"+a.ID, nil, 200).Asset.ID, a.ID)
	for _, entry := range []struct{ method, path string }{
		{"POST", "/api/v1/assets"}, {"PATCH", "/api/v1/assets/" + a.ID}, {"DELETE", "/api/v1/assets/" + a.ID},
	} {
		h.denied(viewer, entry.method, entry.path, object{"name": "Denied change", "kind": "repository"}, 403, "forbidden")
	}
	h.json(analyst, "PATCH", "/api/v1/assets/"+a.ID, object{"name": "Analyst maintained repository"}, 200)
	for _, a := range []actor{viewer, analyst} {
		h.denied(a, "POST", "/api/v1/workspaces", object{"name": "Denied workspace"}, 403, "forbidden")
		h.denied(a, "POST", "/api/v1/users", object{"name": "Denied admin", "email": "denied@example.invalid", "password": secret(t), "role": "admin"}, 403, "forbidden")
	}
	for _, method := range []string{"GET", "PATCH", "DELETE"} {
		var body any
		if method == "PATCH" {
			body = object{"name": "Foreign change"}
		}
		h.denied(other, method, "/api/v1/assets/"+a.ID, body, 404, "not-found")
		h.denied(analyst, method, "/api/v1/assets/"+b.ID, body, 404, "not-found")
	}
	forgedSelection := other
	forgedSelection.workspace = h.admin.workspace
	h.denied(forgedSelection, "GET", "/api/v1/assets", nil, 403, "forbidden")
	h.denied(h.admin, "PATCH", "/api/v1/assets/"+a.ID, object{"ownerId": other.user.ID}, 400, "invalid-input")
	h.denied(h.admin, "POST", "/api/v1/users", object{"name": "Invalid role", "email": "invalid-role@example.invalid", "password": secret(t), "role": "superuser"}, 400, "invalid-input")
	list := items[asset](t, h.json(viewer, "GET", "/api/v1/assets", nil, 200))
	equal(t, "workspace-scoped asset list", len(list), 1)
	equal(t, "denials did not mutate asset", list[0].Name, "Analyst maintained repository")
	session := h.json(other, "GET", "/api/v1/session", nil, 200)
	equal(t, "no foreign membership disclosure", len(session.Workspaces), 1)
	equal(t, "actual workspace membership", session.Workspaces[0].ID, second.workspace)
}

func TestM04_AssetCRUDPreservesExplicitOwnership(t *testing.T) {
	h := newHarness(t, true)
	a := h.asset(h.admin, "Unowned repository", nil)
	untouched := h.asset(h.admin, "Other repository", &h.admin.user.ID)
	equal(t, "asset workspace", a.WorkspaceID, h.admin.workspace)
	if a.OwnerID != nil {
		t.Fatal("unowned asset must not acquire a guessed owner")
	}
	h.json(h.admin, "PATCH", "/api/v1/assets/"+a.ID, object{"name": "Maintained repository", "ownerId": h.admin.user.ID,
		"environment": "staging", "criticality": "high", "tags": []string{"synthetic", "reviewed"}}, 200)
	a = h.json(h.admin, "GET", "/api/v1/assets/"+a.ID, nil, 200).Asset
	equal(t, "updated name", a.Name, "Maintained repository")
	equal(t, "kind retained", a.Kind, "repository")
	equal(t, "updated environment", a.Environment, "staging")
	equal(t, "updated criticality", a.Criticality, "high")
	equal(t, "updated tags", len(a.Tags), 2)
	if !slices.Contains(a.Tags, "synthetic") || !slices.Contains(a.Tags, "reviewed") {
		t.Fatal("asset update lost explicit tags")
	}
	if a.OwnerID == nil || *a.OwnerID != h.admin.user.ID {
		t.Fatal("explicit asset ownership was not persisted")
	}
	equal(t, "list after creates", h.json(h.admin, "GET", "/api/v1/assets", nil, 200).Total, 2)
	h.request(h.admin, "DELETE", "/api/v1/assets/"+a.ID, nil, 204)
	h.denied(h.admin, "GET", "/api/v1/assets/"+a.ID, nil, 404, "not-found")
	list := items[asset](t, h.json(h.admin, "GET", "/api/v1/assets", nil, 200))
	equal(t, "list after delete", len(list), 1)
	equal(t, "delete preserved other asset", list[0].ID, untouched.ID)
}
