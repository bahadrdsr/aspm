//go:build integration

package acceptance

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

var sourceTime = time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC)

type user struct{ ID, Name, Email string }
type workspace struct{ ID, Name, Role string }
type asset struct {
	ID, WorkspaceID, Name, Kind, Environment, Criticality string
	OwnerID                                               *string
	Tags                                                  []string
}
type scope struct {
	ID       string `json:"id"`
	Revision string `json:"revision"`
	Branch   string `json:"branch"`
}
type workItem struct {
	ID, Title, AssetName, Severity, WorkflowState string
	OwnerName                                     *string
	SourceScanAt                                  *time.Time
	CollectedAt, ImportedAt                       time.Time
}
type observation struct {
	ID, RunID, SourceID, ScanID, SourceFindingID, SourceSeverity, NormalizedSeverity string
	EvidenceDigest, Impact, Remediation                                              string
	Scope                                                                            scope
	SourceScanAt                                                                     *time.Time
	SourceLocation                                                                   struct {
		URI  string
		Line int
	}
	Unmapped object
}
type finding struct {
	workItem
	AssetID, WorkspaceID, ScopeLabel, Description, Remediation, SourceState, Disposition string
	OwnerID                                                                              *string
	SourceFreshnessAt, AcceptedRiskExpiresAt                                             *time.Time
	RiskAcceptanceExpired, VerifiedResolution                                            bool
	Evidence                                                                             struct{ Text, SourceLabel, VerificationState string }
	Observations                                                                         []observation
	Notes                                                                                []struct{ ID, Text string }
}
type apiFailure struct {
	Code, Message, RequestID string
	Retryable                bool
}
type imported struct {
	ID, RunID, State, Format, SourceID, ScanID, ReportDigest string
	Scope                                                    scope
	SourceScanAt                                             *time.Time
	CollectedAt, ImportedAt                                  time.Time
	ObservationCount                                         int
	Failure                                                  *apiFailure
}
type reply struct {
	APIVersion, DataOrigin string
	Error                  *apiFailure
	User                   user
	Workspace              workspace
	Workspaces             []workspace
	Asset                  asset
	Finding                finding
	Import                 imported
	Items                  []json.RawMessage
	Total                  int
	NextCursor             *string
	ExpiresAt              time.Time
}
type actor struct {
	user      user
	workspace string
	cookie    *http.Cookie
}
type harness struct {
	t        *testing.T
	services *services
	app      Application
	clock    atomic.Int64
	admin    actor
	password string
}

func newHarness(t *testing.T, enroll bool) *harness {
	t.Helper()
	requireApplication(t)
	h := &harness{t: t, services: ownedServices(t), password: secret(t)}
	h.clock.Store(time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC).UnixNano())
	h.services.cfg.Now = func() time.Time { return time.Unix(0, h.clock.Load()).UTC() }
	h.services.cfg.BootstrapToken = secret(t)
	h.services.cfg.LogOutput = io.Discard
	h.open()
	t.Cleanup(func() {
		if h.app.Close != nil {
			ok(t, "close application before owned-resource cleanup", h.app.Close())
		}
	})
	if enroll {
		h.enroll()
	}
	return h
}

func (h *harness) open() {
	h.t.Helper()
	var err error
	h.app, err = Production.OpenApplication(h.services.ctx, h.services.cfg)
	ok(h.t, "open production application", err)
	if h.app.Handler == nil || h.app.Close == nil {
		h.t.Fatal("production binding missing: handler or Close")
	}
}

func (h *harness) restart() {
	h.t.Helper()
	ok(h.t, "close application for durable reopen", h.app.Close())
	h.app = Application{}
	h.open()
}

func (h *harness) request(a actor, method, path string, body []byte, want int, headers ...string) *httptest.ResponseRecorder {
	h.t.Helper()
	req := httptest.NewRequest(method, "https://aspm.test"+path, bytes.NewReader(body)).WithContext(h.services.ctx)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://aspm.test")
	if a.workspace != "" {
		req.Header.Set("X-ASPM-Workspace-ID", a.workspace)
	}
	if a.cookie != nil {
		req.AddCookie(a.cookie)
	}
	for i := 0; i < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	rr := httptest.NewRecorder()
	h.app.Handler.ServeHTTP(rr, req)
	if want != 0 && rr.Code != want {
		h.t.Fatalf("%s %s: status %d, want %d; response withheld", method, path, rr.Code, want)
	}
	cfg := h.services.cfg
	for _, value := range []string{cfg.DatabaseURL, cfg.Storage.AccessKey, cfg.Storage.SecretKey, cfg.BootstrapToken, h.password} {
		if value != "" && bytes.Contains(rr.Body.Bytes(), []byte(value)) {
			h.t.Fatal("HTTP response exposed a private fixture credential")
		}
	}
	return rr
}

func (h *harness) decode(rr *httptest.ResponseRecorder) reply {
	h.t.Helper()
	var r reply
	ok(h.t, "decode API response", json.Unmarshal(rr.Body.Bytes(), &r))
	equal(h.t, "API version", r.APIVersion, apiVersion)
	if rr.Code >= 400 && (r.Error == nil || r.Error.Code == "" || r.Error.Message == "" || r.Error.RequestID == "" || r.Error.Retryable) {
		h.t.Fatal("rejection needs a non-retryable versioned error with code, message and requestId")
	}
	return r
}

func (h *harness) json(a actor, method, path string, body any, want int) reply {
	h.t.Helper()
	var data []byte
	if body != nil {
		data = encode(h.t, body)
	}
	return h.decode(h.request(a, method, path, data, want))
}

func (h *harness) denied(a actor, method, path string, body any, status int, code string) {
	h.t.Helper()
	r := h.json(a, method, path, body, status)
	if r.Error == nil {
		h.t.Fatal("missing denial")
	}
	equal(h.t, "denial code", r.Error.Code, code)
}

func (h *harness) enrollment() object {
	return object{"workspaceName": "Acceptance workspace", "name": "Synthetic Admin",
		"email": "admin@example.invalid", "password": h.password}
}

func (h *harness) enroll() {
	h.t.Helper()
	r := h.decode(h.request(actor{}, "POST", "/api/v1/bootstrap", encode(h.t, h.enrollment()), 201,
		"X-ASPM-Bootstrap-Token", h.services.cfg.BootstrapToken))
	if r.User.ID == "" || r.Workspace.ID == "" {
		h.t.Fatal("enrollment must create an administrator and workspace")
	}
	h.admin = h.login("admin@example.invalid", h.password, r.Workspace.ID)
	equal(h.t, "enrolled user", h.admin.user.ID, r.User.ID)
}

func (h *harness) login(email, password, workspaceID string) actor {
	h.t.Helper()
	rr := h.request(actor{}, "POST", "/api/v1/login", encode(h.t, object{"email": email, "password": password}), 200)
	r := h.decode(rr)
	for _, c := range rr.Result().Cookies() {
		if c.Name != "aspm_session" {
			continue
		}
		if !c.HttpOnly || !c.Secure || c.Path != "/" || c.Value == "" ||
			(c.SameSite != http.SameSiteLaxMode && c.SameSite != http.SameSiteStrictMode) {
			h.t.Fatal("login must set a protected HTTPS session cookie")
		}
		if bytes.Contains(rr.Body.Bytes(), []byte(password)) || bytes.Contains(rr.Body.Bytes(), []byte(c.Value)) {
			h.t.Fatal("login exposed a password or cookie value in JSON")
		}
		if r.User.ID == "" || r.User.Email != email || r.User.Name == "" || !r.ExpiresAt.After(h.services.cfg.Now()) ||
			r.ExpiresAt.After(h.services.cfg.Now().Add(h.services.cfg.SessionTTL)) {
			h.t.Fatal("login must identify the user and session expiry")
		}
		return actor{user: r.User, workspace: workspaceID, cookie: c}
	}
	h.t.Fatal("login did not set aspm_session")
	return actor{}
}

func (h *harness) addUser(admin actor, role string) actor {
	h.t.Helper()
	password, email := secret(h.t), nonce(h.t)+"@example.invalid"
	rr := h.request(admin, "POST", "/api/v1/users", encode(h.t, object{"name": "Synthetic " + role, "email": email, "password": password, "role": role}), 201)
	if bytes.Contains(rr.Body.Bytes(), []byte(password)) {
		h.t.Fatal("user creation exposed a password in JSON")
	}
	r := h.decode(rr)
	a := h.login(email, password, admin.workspace)
	equal(h.t, "created user identity", a.user.ID, r.User.ID)
	return a
}

func (h *harness) addWorkspace() actor {
	h.t.Helper()
	r := h.json(h.admin, "POST", "/api/v1/workspaces", object{"name": "Second acceptance workspace"}, 201)
	if r.Workspace.ID == "" || r.Workspace.ID == h.admin.workspace {
		h.t.Fatal("workspace creation did not create a distinct workspace")
	}
	a := h.admin
	a.workspace = r.Workspace.ID
	return a
}

func (h *harness) asset(a actor, name string, owner *string) asset {
	h.t.Helper()
	r := h.json(a, "POST", "/api/v1/assets", object{"name": name, "kind": "repository", "environment": "test",
		"criticality": "medium", "tags": []string{"synthetic"}, "ownerId": owner}, 201)
	if r.Asset.ID == "" {
		h.t.Fatal("asset creation returned no identity")
	}
	return r.Asset
}

func items[T any](t *testing.T, r reply) []T {
	t.Helper()
	var result []T
	for _, raw := range r.Items {
		var value T
		ok(t, "decode list item", json.Unmarshal(raw, &value))
		result = append(result, value)
	}
	return result
}

func (h *harness) work(a actor, q string) []workItem {
	h.t.Helper()
	r := h.json(a, "GET", "/api/v1/work?q="+url.QueryEscape(q), nil, 200)
	if (r.DataOrigin != "live" && r.DataOrigin != "synthetic") || r.NextCursor != nil {
		h.t.Fatal("small work response needs truthful dataOrigin and no further page")
	}
	result := items[workItem](h.t, r)
	equal(h.t, "small work total", r.Total, len(result))
	for _, w := range result {
		if w.ID == "" || w.Title == "" || w.AssetName == "" || w.CollectedAt.IsZero() || w.ImportedAt.IsZero() {
			h.t.Fatal("work response omitted usable M01 fields or collection/import times")
		}
	}
	return result
}

func (h *harness) finding(a actor, id string) finding {
	h.t.Helper()
	return h.json(a, "GET", "/api/v1/findings/"+id, nil, 200).Finding
}

func fixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	ok(t, "read owned synthetic report", err)
	return data
}

func (h *harness) input(assetID, format string, report []byte) object {
	return object{"apiVersion": apiVersion, "assetId": assetID, "format": format, "report": string(report),
		"sourceId": "acceptance-source", "scanId": "scan-1", "scope": scope{"owned-repository", "1", "refs/heads/main"},
		"sourceScanAt": sourceTime, "collectedAt": sourceTime.Add(time.Hour),
		"sourceStatus": "succeeded", "scanKind": "full", "completeness": "complete"}
}

func (h *harness) upload(input object) imported {
	h.t.Helper()
	rr := h.request(h.admin, "POST", "/api/v1/imports", encode(h.t, input), 0)
	if rr.Code != 202 && rr.Code != 200 {
		h.t.Fatalf("upload returned %d, want queued 202 or idempotent replay 200", rr.Code)
	}
	r := h.decode(rr).Import
	if r.ID == "" || r.RunID == "" || (rr.Code == 202 && r.State != "queued") || (rr.Code == 200 && r.State != "succeeded") {
		h.t.Fatal("upload must identify a queued attempt or an already completed identical run")
	}
	return r
}

func (h *harness) finish(id, state string) imported {
	h.t.Helper()
	if h.app.ProcessImports == nil {
		h.t.Fatal("production binding missing: ProcessImports (required for M05+ intake tests)")
	}
	ctx, cancel := context.WithTimeout(h.services.ctx, 20*time.Second)
	defer cancel()
	ok(h.t, "process actual queued imports to terminal state", h.app.ProcessImports(ctx))
	r := h.json(h.admin, "GET", "/api/v1/imports/"+id, nil, 200).Import
	equal(h.t, "terminal processing state, not merely queued", r.State, state)
	return r
}

func (h *harness) seed() (object, imported, finding) {
	h.t.Helper()
	a := h.asset(h.admin, "Owned repository", &h.admin.user.ID)
	input := h.input(a.ID, "sarif", fixture(h.t, "sarif.json"))
	run := h.finish(h.upload(input).ID, "succeeded")
	work := h.work(h.admin, "")
	equal(h.t, "one seeded finding", len(work), 1)
	return input, run, h.finding(h.admin, work[0].ID)
}

func sameTime(t *testing.T, label string, got, want *time.Time) {
	t.Helper()
	if (got == nil) != (want == nil) || (got != nil && !got.Equal(*want)) {
		t.Fatalf("%s lost source time semantics", label)
	}
}
