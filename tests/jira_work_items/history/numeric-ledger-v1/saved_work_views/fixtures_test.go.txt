//go:build integration

package saved_work_views

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bahadrdsr/aspm/internal/app"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const viewsPath = "/api/v1/work/views"
const origin = "https://saved-work-views.synthetic.invalid"

type object = map[string]any
type savedView struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Query     string    `json:"query"`
	Sort      string    `json:"sort"`
	Revision  string    `json:"revision"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}
type actor struct {
	User      app.User
	Workspace string
	Cookie    *http.Cookie
}
type reply struct {
	APIVersion, DataOrigin string
	User                   app.User
	Workspace              app.Workspace
	Workspaces             []app.Workspace
	Asset                  app.Asset
	Import                 app.Import
	Finding                app.Finding
	View                   json.RawMessage
	Items                  []json.RawMessage
	Total                  int
	NextCursor             *string
	Error                  *app.Failure
}

func must(t *testing.T, label string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s (%T; private details withheld)", label, err)
	}
}
func check(t *testing.T, ok bool, label string) {
	t.Helper()
	if !ok {
		t.Fatal(label)
	}
}
func same(t *testing.T, label string, got, want any) {
	t.Helper()
	check(t, reflect.DeepEqual(got, want), label)
}
func encode(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	must(t, "encode fixture value", err)
	return data
}
func nonce(t *testing.T) string {
	t.Helper()
	var data [12]byte
	_, err := rand.Read(data[:])
	must(t, "generate owned identity", err)
	return hex.EncodeToString(data[:])
}
func secret(t *testing.T) string { return "synthetic-views-" + nonce(t) }
func digest(data []byte) string  { return fmt.Sprintf("%x", sha256.Sum256(data)) }

type queryBudget struct{ all, findingReads atomic.Int32 }

func (b *queryBudget) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	if strings.Contains(data.SQL, "app_findings") && strings.HasPrefix(strings.TrimSpace(data.SQL), "SELECT") {
		b.findingReads.Add(1)
	}
	if b.all.Add(1) > 1800 {
		cancelled, cancel := context.WithCancel(ctx)
		cancel()
		return cancelled
	}
	return ctx
}
func (*queryBudget) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

type countedConn struct {
	net.Conn
	writes *atomic.Int32
}

func (c countedConn) Write(data []byte) (int, error) {
	if c.writes.Add(1) > 160 {
		return 0, errors.New("owned S3 native write budget exceeded")
	}
	return c.Conn.Write(data)
}

type fixture struct {
	t                         *testing.T
	ctx                       context.Context
	db                        *pgxpool.Pool
	s3                        *s3.Client
	config                    app.Config
	ownedSchema               string
	operatorRevoked           bool
	clock                     atomic.Int64
	queries                   queryBudget
	apiCalls, writes, blocked atomic.Int32
	mu                        sync.Mutex
	secrets                   []string
}

func (f *fixture) now() time.Time { return time.Unix(0, f.clock.Load()).UTC() }
func (f *fixture) remember(value string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if value != "" {
		f.secrets = append(f.secrets, value, url.QueryEscape(value))
	}
}
func (f *fixture) private(data []byte) {
	f.t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, value := range f.secrets {
		check(f.t, !bytes.Contains(data, []byte(value)), "private fixture value appeared in an HTTP response")
	}
}
func (f *fixture) table(name string) string {
	return pgx.Identifier{f.config.Schema, "app_" + name}.Sanitize()
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	path := os.Getenv("ASPM_SAVED_VIEWS_RUNTIME")
	check(t, path != "", "BLOCKED: explicit private runtime.json path is required")
	raw, err := os.ReadFile(path)
	must(t, "read private runtime.json", err)
	var runtime struct{ DatabaseURL, S3Endpoint, S3AccessKey, S3SecretKey, S3Bucket string }
	must(t, "decode explicit fixture credentials", json.Unmarshal(raw, &runtime))
	dbURL, err := url.Parse(runtime.DatabaseURL)
	must(t, "parse explicit database boundary", err)
	check(t, (dbURL.Scheme == "postgres" || dbURL.Scheme == "postgresql") &&
		dbURL.Hostname() == "127.0.0.1" && dbURL.Port() == "15432" &&
		dbURL.User != nil && dbURL.User.Username() != "" && len(dbURL.Path) > 1,
		"BLOCKED: only the selected existing loopback PG fixture is permitted")
	password, exists := dbURL.User.Password()
	check(t, exists && password != "", "BLOCKED: explicit PG password missing")
	endpoint, err := url.Parse(runtime.S3Endpoint)
	must(t, "parse explicit S3 boundary", err)
	check(t, endpoint.Scheme == "http" && endpoint.Hostname() == "127.0.0.1" &&
		endpoint.Port() == "18333" && endpoint.User == nil && endpoint.RawQuery == "" &&
		endpoint.Fragment == "" && (endpoint.Path == "" || endpoint.Path == "/") &&
		runtime.S3AccessKey != "" && runtime.S3SecretKey != "" && runtime.S3Bucket != "" &&
		!strings.ContainsAny(runtime.S3Bucket, "/\\:"),
		"BLOCKED: only explicitly credentialed existing loopback S3 is permitted")
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	f := &fixture{t: t, ctx: ctx}
	f.clock.Store(time.Date(2026, 9, 30, 12, 0, 0, 0, time.UTC).UnixNano())
	id := nonce(t)
	f.config = app.Config{DatabaseURL: runtime.DatabaseURL, Schema: "work_views_" + id,
		ApplicationName: "work-views-" + id, MaxConnections: 3, BootstrapToken: secret(t),
		Now: f.now, LogOutput: io.Discard, QueryTracer: &f.queries, SessionTTL: time.Hour,
		MaxUploadBytes: 64 << 10, ManualProcessing: true, PublicOrigin: origin,
		Storage: app.StorageConfig{Endpoint: runtime.S3Endpoint, Bucket: runtime.S3Bucket,
			Prefix: "saved-work-views/" + id + "/", Region: "us-east-1",
			AccessKey: runtime.S3AccessKey, SecretKey: runtime.S3SecretKey, Timeout: 5 * time.Second}}
	for _, value := range []string{runtime.DatabaseURL, password, runtime.S3AccessKey, runtime.S3SecretKey, f.config.BootstrapToken} {
		f.remember(value)
	}
	original := http.DefaultTransport
	transport := original.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != endpoint.Host {
			f.blocked.Add(1)
			return nil, errors.New("saved views forbid nonfixture HTTP/provider I/O")
		}
		conn, err := (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return countedConn{conn, &f.writes}, nil
	}
	http.DefaultTransport = transport
	t.Cleanup(func() {
		http.DefaultTransport = original
		transport.CloseIdleConnections()
		check(t, f.blocked.Load() == 0 && f.queries.all.Load() <= 1800 && f.writes.Load() <= 160,
			"native I/O/query boundary or budget exceeded")
		t.Logf("owned fixture budgets: API=%d/220 SQL=%d/1800 S3-writes=%d/160 blocked-dials=%d",
			f.apiCalls.Load(), f.queries.all.Load(), f.writes.Load(), f.blocked.Load())
	})
	pg, err := pgxpool.ParseConfig(runtime.DatabaseURL)
	must(t, "parse owned fixture pool", err)
	pg.MaxConns, pg.MinConns = 1, 0
	pg.ConnConfig.ConnectTimeout = 3 * time.Second
	pg.ConnConfig.Tracer = &f.queries
	pg.ConnConfig.RuntimeParams["application_name"] = f.config.ApplicationName + "-fixture"
	f.db, err = pgxpool.NewWithConfig(ctx, pg)
	must(t, "open real fixture pool", err)
	t.Cleanup(f.db.Close)
	must(t, "BLOCKED unless existing owned PG authenticates", f.db.Ping(ctx))
	f.s3 = s3.New(s3.Options{Region: "us-east-1", BaseEndpoint: aws.String(runtime.S3Endpoint),
		UsePathStyle: true, RetryMaxAttempts: 1,
		Credentials: credentials.NewStaticCredentialsProvider(runtime.S3AccessKey, runtime.S3SecretKey, ""),
		HTTPClient: &http.Client{Transport: transport, Timeout: 5 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("no fixture redirects") }},
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired})
	_, err = f.s3.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(runtime.S3Bucket)})
	must(t, "BLOCKED unless existing owned S3 authenticates", err)
	_, err = f.db.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{f.config.Schema}.Sanitize())
	must(t, "create only random owned schema", err)
	f.ownedSchema = f.config.Schema
	t.Cleanup(f.cleanup)
	return f
}

func (f *fixture) cleanup() {
	if f.config.Schema != f.ownedSchema || !regexp.MustCompile(`^work_views_[a-f0-9]{24}$`).MatchString(f.config.Schema) ||
		f.config.Storage.Prefix != "saved-work-views/"+strings.TrimPrefix(f.config.Schema, "work_views_")+"/" {
		f.t.Error("refusing cleanup outside exact owned schema/prefix")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	st := f.config.Storage
	list, err := f.s3.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(st.Bucket),
		Prefix: aws.String(st.Prefix), MaxKeys: aws.Int32(32)})
	if err != nil || aws.ToBool(list.IsTruncated) {
		f.t.Errorf("bounded owned S3 cleanup listing failed (%T; details withheld)", err)
	} else {
		for _, item := range list.Contents {
			if !strings.HasPrefix(aws.ToString(item.Key), st.Prefix) {
				f.t.Error("refusing S3 cleanup outside owned prefix")
				continue
			}
			if _, err = f.s3.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(st.Bucket), Key: item.Key}); err != nil {
				f.t.Errorf("exact owned S3 cleanup failed (%T; details withheld)", err)
			}
		}
	}
	if _, err = f.db.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{f.config.Schema}.Sanitize()+" CASCADE"); err != nil {
		f.t.Errorf("exact owned schema cleanup failed (%T; details withheld)", err)
	}
}

type harness struct {
	*fixture
	core  *app.Application
	admin actor
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{fixture: newFixture(t)}
	h.open()
	password := secret(t)
	h.remember(password)
	r := h.json(actor{}, "POST", "/api/v1/bootstrap", object{"workspaceName": "Owned saved views",
		"name": "Morgan Owner", "email": "admin@views.invalid", "password": password}, 201,
		"X-ASPM-Bootstrap-Token", h.config.BootstrapToken)
	h.admin = h.login("admin@views.invalid", password, r.Workspace.ID)
	return h
}
func (h *harness) open() {
	h.t.Helper()
	instance, err := app.Open(h.ctx, h.config)
	must(h.t, "open actual current production app/pool/migrations", err)
	h.core = instance
	h.t.Cleanup(func() { must(h.t, "close actual app before cleanup", instance.Close()) })
}
func (h *harness) reopen() {
	must(h.t, "close actual app for durable reopen", h.core.Close())
	h.open()
}
func (h *harness) request(who actor, method, path string, body any, expected int, headers ...string) *httptest.ResponseRecorder {
	h.t.Helper()
	check(h.t, h.apiCalls.Add(1) <= 220, "bounded saved-view API budget exceeded")
	var data []byte
	if raw, ok := body.([]byte); ok {
		data = raw
	} else if body != nil {
		data = encode(h.t, body)
	}
	request := httptest.NewRequest(method, origin+path, bytes.NewReader(data)).WithContext(h.ctx)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", origin)
	if who.Workspace != "" {
		request.Header.Set("X-ASPM-Workspace-ID", who.Workspace)
	}
	if who.Cookie != nil {
		request.AddCookie(who.Cookie)
	}
	for i := 0; i < len(headers); i += 2 {
		request.Header.Set(headers[i], headers[i+1])
	}
	searches, writes := h.queries.findingReads.Load(), h.writes.Load()
	response := httptest.NewRecorder()
	h.core.Handler.ServeHTTP(response, request)
	if strings.HasPrefix(path, viewsPath) && method != "GET" {
		check(h.t, searches == h.queries.findingReads.Load() && writes == h.writes.Load(),
			"saving a preference ran finding search or native S3 I/O")
	}
	if expected != 0 && response.Code != expected {
		h.t.Fatalf("actual %s %s returned %d, want %d; private response withheld", method, path, response.Code, expected)
	}
	h.private(response.Body.Bytes())
	check(h.t, response.Body.Len() <= 128<<10, "bounded API response exceeded fixture budget")
	if strings.HasPrefix(path, viewsPath) && response.Code >= 200 && response.Code < 300 && response.Code != 204 {
		var fields object
		must(h.t, "inspect saved-view envelope", json.Unmarshal(response.Body.Bytes(), &fields))
		if _, exists := fields["view"]; exists {
			check(h.t, len(fields) == 2 && fields["apiVersion"] != nil, "saved-view response exposed undeclared data")
		} else {
			_, hasCursor := fields["nextCursor"]
			check(h.t, len(fields) == 4 && fields["apiVersion"] != nil && fields["items"] != nil &&
				fields["total"] != nil && hasCursor, "saved-view list changed native envelope or exposed undeclared data")
		}
	}
	return response
}
func (h *harness) decode(rr *httptest.ResponseRecorder) reply {
	h.t.Helper()
	var value reply
	must(h.t, "decode actual API response", json.Unmarshal(rr.Body.Bytes(), &value))
	same(h.t, "existing API envelope version changed", value.APIVersion, app.APIVersion)
	if rr.Code >= 400 {
		var fields object
		must(h.t, "inspect denial envelope", json.Unmarshal(rr.Body.Bytes(), &fields))
		check(h.t, len(fields) == 2 && fields["apiVersion"] != nil && fields["error"] != nil &&
			value.Error != nil && value.Error.Code != "" && value.Error.Message != "" &&
			value.Error.RequestID == rr.Header().Get("X-Request-ID") && value.Error.RequestID != "" &&
			!value.Error.Retryable, "denial omitted typed error or disclosed resource metadata")
		errorFields, ok := fields["error"].(map[string]any)
		check(h.t, ok && len(errorFields) == 4 && errorFields["code"] != nil && errorFields["message"] != nil &&
			errorFields["requestId"] != nil && errorFields["retryable"] != nil, "denial contains undeclared metadata")
	}
	return value
}
func (h *harness) json(who actor, method, path string, body any, expected int, headers ...string) reply {
	h.t.Helper()
	return h.decode(h.request(who, method, path, body, expected, headers...))
}
func (h *harness) denied(who actor, method, path string, body any, status int, code string) {
	h.t.Helper()
	result := h.json(who, method, path, body, status)
	check(h.t, result.Error != nil && result.Error.Code == code, "denial has wrong typed error")
}
func (h *harness) login(email, password, workspace string) actor {
	h.t.Helper()
	rr := h.request(actor{}, "POST", "/api/v1/login", object{"email": email, "password": password}, 200)
	value := h.decode(rr)
	for _, cookie := range rr.Result().Cookies() {
		if cookie.Name == "aspm_session" && cookie.Value != "" && cookie.Secure && cookie.HttpOnly && cookie.Path == "/" {
			h.remember(cookie.Value)
			return actor{value.User, workspace, cookie}
		}
	}
	h.t.Fatal("real login omitted protected session cookie")
	return actor{}
}
func (h *harness) user(admin actor, role string) actor {
	password, email := secret(h.t), nonce(h.t)+"@views.invalid"
	h.remember(password)
	h.json(admin, "POST", "/api/v1/users", object{"name": "Owned " + role, "email": email, "password": password, "role": role}, 201)
	return h.login(email, password, admin.Workspace)
}
func (h *harness) workspace() actor {
	return h.workspaceFor(h.admin)
}
func (h *harness) workspaceFor(who actor) actor {
	previous := who.Workspace
	value := h.json(who, "POST", "/api/v1/workspaces", object{"name": "Second owned workspace"}, 201)
	who.Workspace = value.Workspace.ID
	check(h.t, who.Workspace != "" && who.Workspace != previous, "second workspace is not distinct")
	return who
}
func view(t *testing.T, raw json.RawMessage) savedView {
	t.Helper()
	var value savedView
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	must(t, "decode canonical saved view only", decoder.Decode(&value))
	var fields object
	must(t, "inspect saved preference field set", json.Unmarshal(raw, &fields))
	revision, err := strconv.ParseInt(value.Revision, 10, 64)
	name, nameString := fields["name"].(string)
	query, queryString := fields["query"].(string)
	sortOrder, sortString := fields["sort"].(string)
	check(t, len(fields) == 7 && regexp.MustCompile(`^[a-f0-9]{32}$`).MatchString(value.ID) &&
		err == nil && revision > 0 && strconv.FormatInt(revision, 10) == value.Revision &&
		nameString && strings.TrimSpace(name) == name && name != "" && len(name) <= 256 && !strings.ContainsRune(name, 0) &&
		queryString && strings.TrimSpace(query) == query && len(query) <= 512 && !strings.ContainsRune(query, 0) &&
		sortString && (sortOrder == "source-order" || sortOrder == "severity" || sortOrder == "title") &&
		!value.CreatedAt.IsZero() && !value.UpdatedAt.Before(value.CreatedAt),
		"saved view has noncanonical fields/identity/revision/timestamps")
	return value
}
func (h *harness) create(who actor, name, query, sort string) savedView {
	h.t.Helper()
	return view(h.t, h.json(who, "POST", viewsPath, object{"name": name, "query": query, "sort": sort}, 201).View)
}
func (h *harness) get(who actor, id string) savedView {
	h.t.Helper()
	return view(h.t, h.json(who, "GET", viewsPath+"/"+id, nil, 200).View)
}
func (h *harness) patch(who actor, current savedView, fields object) savedView {
	h.t.Helper()
	fields["revision"] = current.Revision
	return view(h.t, h.json(who, "PATCH", viewsPath+"/"+current.ID, fields, 200).View)
}
func report(t *testing.T, titles ...string) []byte {
	rules, results := []any{}, []any{}
	for i, title := range titles {
		id := fmt.Sprintf("SYN%03d", i)
		rules = append(rules, object{"id": id, "shortDescription": object{"text": title},
			"fullDescription": object{"text": "description-token"}, "help": object{"text": "Review synthetic configuration."}})
		results = append(results, object{"guid": fmt.Sprintf("11111111-1111-4111-8111-%012d", i+1),
			"ruleId": id, "level": []string{"warning", "error"}[i%2], "message": object{"text": "Owned synthetic evidence."}})
	}
	return encode(t, object{"version": "2.1.0", "runs": []any{object{
		"tool": object{"driver": object{"name": "Saved views acceptance", "rules": rules}}, "results": results}}})
}
func (h *harness) seed(who actor, name string, owner *string, titles ...string) (app.Asset, app.Import, []app.WorkItem) {
	h.t.Helper()
	asset := h.json(who, "POST", "/api/v1/assets", object{"name": name, "kind": "repository",
		"environment": "test", "criticality": "medium", "tags": []string{"synthetic"}, "ownerId": owner}, 201).Asset
	input := object{"apiVersion": app.APIVersion, "assetId": asset.ID, "format": "sarif",
		"report": string(report(h.t, titles...)), "sourceId": "saved-views-source", "scanId": nonce(h.t),
		"scope": app.Scope{ID: asset.ID, Revision: "1", Branch: "refs/heads/main"}, "sourceScanAt": h.now().Add(-time.Hour),
		"collectedAt": h.now(), "sourceStatus": "succeeded", "scanKind": "full", "completeness": "complete"}
	queued := h.json(who, "POST", "/api/v1/imports", input, 202).Import
	check(h.t, queued.State == "queued", "finding intake was not durably queued")
	must(h.t, "process legitimate real S3 finding intake", h.core.ProcessImports(h.ctx))
	completed := h.json(who, "GET", "/api/v1/imports/"+queued.ID, nil, 200).Import
	check(h.t, completed.State == "succeeded" && completed.ObservationCount == len(titles), "real finding intake did not succeed")
	page := h.json(who, "GET", "/api/v1/work?q="+url.QueryEscape(name), nil, 200)
	var items []app.WorkItem
	for _, raw := range page.Items {
		var item app.WorkItem
		must(h.t, "decode imported Work identity", json.Unmarshal(raw, &item))
		items = append(items, item)
	}
	check(h.t, len(items) == len(titles) && page.Total == len(titles) && page.NextCursor == nil,
		"real finding identities were not exposed by Work")
	return asset, completed, items
}
func (f *fixture) rows(query string, args ...any) []string {
	f.t.Helper()
	rows, err := f.db.Query(f.ctx, query, args...)
	must(f.t, "read owned catalog/business state", err)
	defer rows.Close()
	result := []string{}
	for rows.Next() {
		var value string
		must(f.t, "read actual owned row", rows.Scan(&value))
		result = append(result, value)
		check(f.t, len(result) <= 100, "owned SQL read exceeded row budget")
	}
	must(f.t, "finish owned readonly query", rows.Err())
	return result
}
func (f *fixture) ledger() []string {
	return f.rows("SELECT version::text FROM " + f.table("schema_versions") + " ORDER BY version")
}
func (f *fixture) snapshot(tables []string) map[string][]string {
	result := map[string][]string{}
	for _, name := range tables {
		result[name] = f.rows("SELECT to_jsonb(v)::text FROM " + f.table(name) + " v ORDER BY to_jsonb(v)::text")
	}
	return result
}
