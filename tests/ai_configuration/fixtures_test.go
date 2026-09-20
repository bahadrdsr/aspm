//go:build integration

package ai_configuration

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bahadrdsr/aspm/internal/app"
	"github.com/bahadrdsr/aspm/internal/providers"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func must(t *testing.T, label string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s failed (%T; private details withheld)", label, err)
	}
}
func check(t *testing.T, ok bool, label string) {
	t.Helper()
	if !ok {
		t.Fatal(label)
	}
}
func random(t *testing.T, n int) []byte {
	t.Helper()
	b := make([]byte, n)
	_, e := rand.Read(b)
	must(t, "generate synthetic key", e)
	return b
}
func nonce(t *testing.T) string { return hex.EncodeToString(random(t, 12)) }
func secret(t *testing.T) string {
	return "synthetic-ai-config-" + base64.RawURLEncoding.EncodeToString(random(t, 24))
}
func encode(t *testing.T, value any) []byte {
	t.Helper()
	b, e := json.Marshal(value)
	must(t, "encode test request", e)
	return b
}
func convert[T any](t *testing.T, value any) T {
	t.Helper()
	var result T
	must(t, "decode returned metadata", json.Unmarshal(encode(t, value), &result))
	return result
}

type safeLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *safeLog) Write(b []byte) (int, error) { l.mu.Lock(); defer l.mu.Unlock(); return l.b.Write(b) }
func (l *safeLog) data() []byte                { l.mu.Lock(); defer l.mu.Unlock(); return bytes.Clone(l.b.Bytes()) }

type fixture struct {
	t                                 *testing.T
	ctx                               context.Context
	db                                *pgxpool.Pool
	database                          app.DatabaseConfig
	config                            app.Config
	key                               []byte
	clock                             atomic.Int64
	log                               safeLog
	httpTripwire, tlsTripwire         *httptest.Server
	httpCalls, ambientDials, tcpCalls atomic.Int32
	mu                                sync.Mutex
	secrets, ciphertexts              []string
}

func (f *fixture) now() time.Time { return time.Unix(0, f.clock.Load()).UTC() }
func (f *fixture) remember(v string) {
	if v != "" {
		f.mu.Lock()
		f.secrets = append(f.secrets, v, url.QueryEscape(v), base64.StdEncoding.EncodeToString([]byte(v)))
		f.mu.Unlock()
	}
}
func (f *fixture) noPlaintext(data []byte) {
	f.t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, v := range f.secrets {
		check(f.t, !bytes.Contains(data, []byte(v)), "private configuration value leaked")
	}
}
func (f *fixture) noSecrets(data []byte) {
	f.t.Helper()
	f.noPlaintext(data)
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, v := range f.ciphertexts {
		check(f.t, !bytes.Contains(data, []byte(v)), "encrypted secret escaped metadata/logs")
	}
}
func (f *fixture) table(name string) string {
	return pgx.Identifier{f.database.Schema, "app_" + name}.Sanitize()
}
func (f *fixture) networkNone() {
	f.t.Helper()
	check(f.t, f.httpCalls.Load() == 0 && f.ambientDials.Load() == 0 && f.tcpCalls.Load() == 0, "configuration or resolver attempted forbidden provider/storage HTTP I/O")
	f.noSecrets(f.log.data())
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	f := &fixture{t: t, ctx: ctx, key: random(t, 32)}
	f.clock.Store(time.Date(2026, 9, 17, 12, 0, 0, 0, time.UTC).UnixNano())
	tripwire := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		f.httpCalls.Add(1)
		http.Error(w, "AI setup must not call an HTTP destination", 503)
	})
	f.httpTripwire = httptest.NewServer(tripwire)
	f.tlsTripwire = httptest.NewTLSServer(tripwire)
	t.Cleanup(f.httpTripwire.Close)
	t.Cleanup(f.tlsTripwire.Close)
	f.httpTripwire.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			f.tcpCalls.Add(1)
		}
	}
	f.tlsTripwire.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			f.tcpCalls.Add(1)
		}
	}
	original := http.DefaultTransport
	guard := original.(*http.Transport).Clone()
	guard.Proxy = nil
	guard.DialContext = func(context.Context, string, string) (net.Conn, error) {
		f.ambientDials.Add(1)
		return nil, errors.New("AI configuration forbids HTTP/provider credential-discovery I/O")
	}
	http.DefaultTransport = guard
	t.Cleanup(func() { http.DefaultTransport = original; guard.CloseIdleConnections(); f.networkNone() })
	databaseURL := os.Getenv("ASPM_AI_CONFIG_DATABASE_URL")
	check(t, databaseURL != "", "BLOCKED: explicit private loopback PG fixture is missing")
	u, e := url.Parse(databaseURL)
	must(t, "parse selected fixture PG URL", e)
	check(t, (u.Scheme == "postgres" || u.Scheme == "postgresql") && u.Hostname() == "127.0.0.1" && u.Port() != "" && u.User != nil, "only explicit owned loopback PG is permitted")
	password, present := u.User.Password()
	check(t, present && password != "", "explicit owned PG credential required")
	id := nonce(t)
	f.database = app.DatabaseConfig{DatabaseURL: databaseURL, Schema: "ai_config_" + id, ApplicationName: "ai-config-" + id, MaxConnections: 3, Now: f.now, LogOutput: &f.log}
	f.config = app.Config{DatabaseURL: databaseURL, Schema: f.database.Schema, ApplicationName: f.database.ApplicationName, MaxConnections: 3, Now: f.now, LogOutput: &f.log,
		IntegrationEncryptionKey: f.key, BootstrapToken: secret(t), ManualProcessing: true, PublicOrigin: "https://ai-config.synthetic.invalid",
		Storage: app.StorageConfig{Endpoint: f.httpTripwire.URL, Bucket: "unused-ai-configuration", Prefix: "unused/", Region: "us-east-1", AccessKey: secret(t), SecretKey: secret(t), Timeout: time.Second}}
	for _, value := range []string{databaseURL, password, f.config.BootstrapToken, f.config.Storage.AccessKey, f.config.Storage.SecretKey, string(f.key), hex.EncodeToString(f.key), base64.StdEncoding.EncodeToString(f.key)} {
		f.remember(value)
	}
	pc, e := pgxpool.ParseConfig(databaseURL)
	must(t, "parse owned SQL fixture pool", e)
	pc.MaxConns = 2
	pc.MinConns = 0
	pc.ConnConfig.ConnectTimeout = 3 * time.Second
	pc.ConnConfig.RuntimeParams["application_name"] = f.database.ApplicationName + "-fixture"
	f.db, e = pgxpool.NewWithConfig(ctx, pc)
	must(t, "open real owned PG", e)
	t.Cleanup(f.db.Close)
	must(t, "authenticate owned PG", f.db.Ping(ctx))
	_, e = f.db.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{f.database.Schema}.Sanitize())
	must(t, "create only random owned schema", e)
	t.Cleanup(func() {
		clean, stop := context.WithTimeout(context.Background(), 5*time.Second)
		defer stop()
		_, err := f.db.Exec(clean, "DROP SCHEMA "+pgx.Identifier{f.database.Schema}.Sanitize()+" CASCADE")
		if err != nil {
			t.Errorf("owned schema cleanup failed (%T)", err)
		}
	})
	return f
}

type harness struct {
	*fixture
	core     *app.Application
	admin    actor
	apiCalls atomic.Int32
}

func newHarness(t *testing.T, key bool) *harness {
	t.Helper()
	f := newFixture(t)
	if !key {
		f.config.IntegrationEncryptionKey = nil
	}
	h := &harness{fixture: f}
	h.open()
	h.enroll()
	return h
}
func (h *harness) open() {
	h.t.Helper()
	instance, err := app.Open(h.ctx, h.config)
	must(h.t, "open actual configuration core", err)
	h.core = instance
	h.t.Cleanup(func() { must(h.t, "close actual core before schema cleanup", instance.Close()) })
}
func (h *harness) reopen() { must(h.t, "close actual core for persistence", h.core.Close()); h.open() }
func (h *harness) enroll() {
	password := secret(h.t)
	h.remember(password)
	value := h.json(actor{}, "POST", "/api/v1/bootstrap", map[string]any{"workspaceName": "Owned AI configuration", "name": "Synthetic AI admin", "email": "admin@ai-config.invalid", "password": password}, 201, "X-ASPM-Bootstrap-Token", h.config.BootstrapToken)
	h.admin = h.login("admin@ai-config.invalid", password, value.Workspace.ID)
}
func (h *harness) makeRequest(ctx context.Context, who actor, method, route string, body any, headers ...string) *http.Request {
	h.t.Helper()
	check(h.t, h.apiCalls.Add(1) <= 220, "bounded AI configuration API budget exceeded")
	var data []byte
	if body != nil {
		data = encode(h.t, body)
	}
	request := httptest.NewRequest(method, h.config.PublicOrigin+route, bytes.NewReader(data)).WithContext(ctx)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", h.config.PublicOrigin)
	if who.Workspace != "" {
		request.Header.Set("X-ASPM-Workspace-ID", who.Workspace)
	}
	if who.Cookie != nil {
		request.AddCookie(who.Cookie)
	}
	for i := 0; i < len(headers); i += 2 {
		request.Header.Set(headers[i], headers[i+1])
	}
	return request
}
func (h *harness) request(who actor, method, route string, body any, status int, headers ...string) *httptest.ResponseRecorder {
	h.t.Helper()
	r := h.makeRequest(h.ctx, who, method, route, body, headers...)
	response := httptest.NewRecorder()
	h.core.Handler.ServeHTTP(response, r)
	if response.Code != status {
		h.t.Fatalf("actual %s %s returned %d, want %d; private response withheld", method, route, response.Code, status)
	}
	h.noSecrets(response.Body.Bytes())
	if len(response.Body.Bytes()) > 0 && len(route) >= 10 && route[:10] == "/api/v1/ai" {
		var decoded any
		must(h.t, "inspect AI metadata response", json.Unmarshal(response.Body.Bytes(), &decoded))
		var inspect func(any)
		inspect = func(v any) {
			switch item := v.(type) {
			case map[string]any:
				for name, value := range item {
					switch name {
					case "apiKey", "token", "ciphertext", "credentialCiphertext", "encryptionKey", "headers", "credentialURL":
						h.t.Fatal("secret field appeared in AI HTTP metadata")
					}
					inspect(value)
				}
			case []any:
				for _, value := range item {
					inspect(value)
				}
			}
		}
		inspect(decoded)
	}
	return response
}
func (h *harness) json(who actor, method, path string, body any, status int, headers ...string) reply {
	h.t.Helper()
	var value reply
	must(h.t, "decode actual API response", json.Unmarshal(h.request(who, method, path, body, status, headers...).Body.Bytes(), &value))
	check(h.t, value.APIVersion == apiVersion, "actual configuration API version mismatch")
	if status >= 400 {
		check(h.t, value.Error != nil && value.Error.Code != "" && value.Error.Message != "" && value.Error.RequestID != "" && !value.Error.Retryable, "configuration rejection must be explicit and nonretryable")
	}
	return value
}
func (h *harness) login(email, password, workspace string) actor {
	response := h.request(actor{}, "POST", "/api/v1/login", map[string]any{"email": email, "password": password}, 200)
	var value reply
	must(h.t, "decode actual login", json.Unmarshal(response.Body.Bytes(), &value))
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == "aspm_session" {
			check(h.t, cookie.HttpOnly && cookie.Secure, "real protected session required")
			h.remember(cookie.Value)
			return actor{value.User.ID, workspace, cookie}
		}
	}
	h.t.Fatal("actual login omitted session cookie")
	return actor{}
}
func (h *harness) addUser(role string) actor {
	password, email := secret(h.t), nonce(h.t)+"@ai-config.invalid"
	h.remember(password)
	h.json(h.admin, "POST", "/api/v1/users", map[string]any{"name": "Synthetic " + role, "email": email, "password": password, "role": role}, 201)
	return h.login(email, password, h.admin.Workspace)
}
func (h *harness) otherWorkspace() actor {
	value := h.json(h.admin, "POST", "/api/v1/workspaces", map[string]any{"name": "Other owned AI workspace"}, 201)
	other := h.admin
	other.Workspace = value.Workspace.ID
	return other
}
func (h *harness) profileInput(family, endpoint, key string) map[string]any {
	input := map[string]any{"name": "Synthetic provider profile", "family": family, "endpoint": endpoint, "model": "synthetic-model", "deployment": "", "enabled": true, "structuredOutput": true}
	if family == "azure-foundry" {
		input["deployment"] = "synthetic-foundry-deployment"
	}
	if key != "" {
		h.remember(key)
		input["apiKey"] = key
	}
	return input
}
func (h *harness) create(who actor, family, endpoint, key string) profile {
	value := h.json(who, "POST", profilesPath, h.profileInput(family, endpoint, key), 201).Profile
	check(h.t, value.ID != "" && value.WorkspaceID == who.Workspace && value.Family == family && value.Endpoint == endpoint && value.Model == "synthetic-model" && value.Revision != "" && value.Enabled && value.StructuredOutput && value.CredentialConfigured == (key != ""), "actual profile metadata did not preserve its explicit provider configuration")
	return value
}
func (h *harness) get(who actor, id string) profile {
	return h.json(who, "GET", profilesPath+"/"+id, nil, 200).Profile
}
func (h *harness) mode(who actor, value string) policy {
	return h.json(who, "PATCH", policyPath, map[string]string{"mode": value}, 200).Policy
}
func (h *harness) grantInput(p profile, policy policy, expiry time.Time) map[string]any {
	return map[string]any{"profileId": p.ID, "profileRevision": p.Revision, "policyRevision": policy.Revision, "destination": p.Endpoint, "task": validityTask, "dataClass": evidenceClass, "expiresAt": expiry.Format(time.RFC3339Nano)}
}
func (h *harness) authorize(who actor, p profile, policy policy) grant {
	expiry := h.now().Add(time.Hour)
	value := h.json(who, "POST", grantsPath, h.grantInput(p, policy, expiry), 201).Grant
	check(h.t, value.ID != "" && value.WorkspaceID == who.Workspace && value.ProfileID == p.ID && value.ProfileRevision == p.Revision && value.PolicyRevision == policy.Revision && value.Destination == p.Endpoint && value.Task == validityTask && value.DataClass == evidenceClass && value.GrantedBy == who.ID && value.CreatedAt.Equal(h.now()) && value.ExpiresAt.Equal(expiry) && value.RevokedAt == nil, "grant was not bound to trusted current server state/actor/time/explicit expiry")
	return value
}
func (h *harness) resolver() resolver {
	h.t.Helper()
	check(h.t, Production.OpenResolver != nil, "real DB-only AI configuration resolver missing")
	db := h.database
	db.ApplicationName = "ai-lookup-" + nonce(h.t)
	db.MaxConnections = 1
	instance, err := Production.OpenResolver(h.ctx, db, h.config.IntegrationEncryptionKey)
	must(h.t, "open independent trusted AI lookup", err)
	check(h.t, instance != nil, "AI resolver is nil")
	h.t.Cleanup(func() { must(h.t, "close trusted AI configuration resolver", instance.Close()) })
	return instance
}
func lookupFor(who actor, p profile, g *grant) lookupRequest {
	r := lookupRequest{WorkspaceID: who.Workspace, ActorID: who.ID, ProfileID: p.ID, Task: validityTask, DataClass: evidenceClass}
	if g != nil {
		r.GrantID = g.ID
	}
	return r
}
func assertDenied(t *testing.T, r resolver, ctx context.Context, request lookupRequest) {
	t.Helper()
	value, err := r.Resolve(ctx, request)
	check(t, err != nil && (errors.Is(err, providers.ErrPolicy) || errors.Is(err, providers.ErrCapability)), "unapproved stored AI configuration resolved as authority")
	check(t, reflect.DeepEqual(value, resolved{}), "denied resolver leaked partial/secret configuration")
}
func assertResolved(t *testing.T, r resolver, ctx context.Context, who actor, p profile, policy policy, g *grant, key string) {
	t.Helper()
	value, err := r.Resolve(ctx, lookupFor(who, p, g))
	must(t, "read approved trusted configuration without inference", err)
	approval := ""
	if g != nil {
		approval = g.ID
	}
	check(t, value.Profile.ID == p.ID && value.Profile.Revision == p.Revision && value.Profile.Family == p.Family && value.Profile.Endpoint == p.Endpoint && value.Profile.Model == p.Model && value.Profile.Deployment == p.Deployment && value.Profile.APIKey == key && value.Profile.StructuredOutput == p.StructuredOutput, "resolver did not map the exact current providers.Profile")
	check(t, value.Policy.Mode == policy.Mode && value.Policy.Revision == policy.Revision && value.Policy.ApprovalRef == approval && reflect.DeepEqual(value.Policy.Workspaces, []string{who.Workspace}) && reflect.DeepEqual(value.Policy.Tasks, []string{validityTask}) && reflect.DeepEqual(value.Policy.DataClasses, []string{evidenceClass}) && reflect.DeepEqual(value.Policy.AllowedDestinations, []string{p.Endpoint}) && len(value.Policy.FallbackProfileIDs) == 0, "resolver widened persisted workspace/task/data/destination policy")
}
func (h *harness) credential(table, id, domain, key string) {
	h.t.Helper()
	var stored []byte
	must(h.t, "inspect selected encrypted credential", h.db.QueryRow(h.ctx, "SELECT credential_ciphertext FROM "+h.table(table)+" WHERE id=$1", id).Scan(&stored))
	if key == "" {
		check(h.t, len(stored) == 0, "keyless local profile stored a fabricated credential")
		return
	}
	block, err := aes.NewCipher(h.key)
	must(h.t, "construct independent AES assertion", err)
	aead, err := cipher.NewGCM(block)
	must(h.t, "construct independent GCM assertion", err)
	check(h.t, len(stored) > 1+aead.NonceSize()+aead.Overhead() && stored[0] == 1, "credential is not the accepted v1 authenticated envelope")
	end := 1 + aead.NonceSize()
	aad := []byte(domain + "\x00" + h.admin.Workspace + "\x00" + id)
	plain, err := aead.Open(nil, stored[1:end], stored[end:], aad)
	must(h.t, "authenticate exact configuration credential domain", err)
	check(h.t, string(plain) == key, "credential plaintext differs from explicit supplied key")
	for _, wrong := range []string{domain + "\x00other-workspace\x00" + id, domain + "\x00" + h.admin.Workspace + "\x00other-id", "other-domain\x00" + h.admin.Workspace + "\x00" + id} {
		_, err = aead.Open(nil, stored[1:end], stored[end:], []byte(wrong))
		check(h.t, err != nil, "credential lacks domain/workspace/profile authentication")
	}
	h.mu.Lock()
	h.ciphertexts = append(h.ciphertexts, string(stored), hex.EncodeToString(stored), base64.StdEncoding.EncodeToString(stored))
	h.mu.Unlock()
}
func (h *harness) stateSnapshot() []string {
	var result []string
	for _, table := range []string{"ai_profiles", "ai_policies", "ai_egress_grants"} {
		rows, err := h.db.Query(h.ctx, "SELECT to_jsonb(v)::text FROM "+h.table(table)+" v ORDER BY to_jsonb(v)::text")
		must(h.t, "read bounded current configuration rows", err)
		defer rows.Close()
		for rows.Next() {
			var value string
			must(h.t, "inspect configuration persistence privately", rows.Scan(&value))
			h.noPlaintext([]byte(value))
			result = append(result, value)
		}
		must(h.t, "complete metadata inspection", rows.Err())
		rows.Close()
	}
	return result
}
