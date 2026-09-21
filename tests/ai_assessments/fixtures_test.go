//go:build integration

package ai_assessments

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
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

func must(t *testing.T, label string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s failed (%T; private details withheld)", label, err)
	}
}
func check(t *testing.T, value bool, label string) {
	t.Helper()
	if !value {
		t.Fatal(label)
	}
}
func random(t *testing.T, n int) []byte {
	b := make([]byte, n)
	_, err := rand.Read(b)
	must(t, "generate owned random identity", err)
	return b
}
func nonce(t *testing.T) string { return hex.EncodeToString(random(t, 12)) }
func secret(t *testing.T) string {
	return "synthetic-assessment-" + base64.RawURLEncoding.EncodeToString(random(t, 24))
}
func encoded(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	must(t, "encode bounded fixture value", err)
	return data
}
func digest(data []byte) string { return fmt.Sprintf("sha256:%x", sha256.Sum256(data)) }
func convert[T any](t *testing.T, value any) T {
	var result T
	must(t, "decode public metadata", json.Unmarshal(encoded(t, value), &result))
	return result
}
func required(t *testing.T, key string) string {
	t.Helper()
	value := os.Getenv(key)
	check(t, value != "", "BLOCKED: explicit owned fixture setting missing: "+key)
	return value
}

type safeLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *safeLog) Write(data []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(data)
}
func (l *safeLog) data() []byte { l.mu.Lock(); defer l.mu.Unlock(); return bytes.Clone(l.b.Bytes()) }

type fixture struct {
	t            *testing.T
	ctx          context.Context
	db           *pgxpool.Pool
	database     app.DatabaseConfig
	config       app.Config
	rawStorage   app.StorageConfig
	s3           *s3.Client
	key          []byte
	scope        string
	offset       atomic.Int64
	apiCalls     atomic.Int32
	storageCalls atomic.Int32
	foreignDials atomic.Int32
	log          safeLog
	mu           sync.Mutex
	secrets      []string
	native       *nativeFixture
}

func (f *fixture) now() time.Time { return time.Now().UTC().Add(time.Duration(f.offset.Load())) }
func (f *fixture) table(name string) string {
	return pgx.Identifier{f.database.Schema, "app_" + name}.Sanitize()
}
func (f *fixture) remember(value string) {
	if value == "" {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.secrets = append(f.secrets, value, url.QueryEscape(value), base64.StdEncoding.EncodeToString([]byte(value)))
}
func (f *fixture) noSecrets(data []byte) {
	f.t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, value := range f.secrets {
		check(f.t, !bytes.Contains(data, []byte(value)), "private credential appeared in metadata, context or logs")
	}
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)
	id := nonce(t)
	f := &fixture{t: t, ctx: ctx, key: random(t, 32), scope: "owned-assessment-pool"}
	dbURL := required(t, "ASPM_ASSESSMENT_DATABASE_URL")
	u, err := url.Parse(dbURL)
	must(t, "parse selected loopback PG", err)
	check(t, (u.Scheme == "postgres" || u.Scheme == "postgresql") && u.Hostname() == "127.0.0.1" &&
		u.Port() != "" && u.User != nil, "only explicit owned loopback PG is allowed")
	password, present := u.User.Password()
	check(t, present && password != "", "explicit PG authentication required")
	endpoint := required(t, "ASPM_ASSESSMENT_S3_ENDPOINT")
	storeURL, err := url.Parse(endpoint)
	must(t, "parse selected local intake store", err)
	check(t, storeURL.Hostname() == "127.0.0.1" && storeURL.Port() != "" &&
		(storeURL.Scheme == "http" || storeURL.Scheme == "https") && storeURL.User == nil &&
		storeURL.RawQuery == "" && storeURL.Fragment == "", "only the owned loopback intake store is allowed")
	f.rawStorage = app.StorageConfig{Endpoint: endpoint, Bucket: required(t, "ASPM_ASSESSMENT_S3_BUCKET"),
		Prefix: "assessment-fixture/" + id + "/", Region: "us-east-1",
		AccessKey: required(t, "ASPM_ASSESSMENT_S3_ACCESS_KEY"), SecretKey: required(t, "ASPM_ASSESSMENT_S3_SECRET_KEY"),
		Timeout: 5 * time.Second}
	check(t, !strings.ContainsAny(f.rawStorage.Bucket, "/\\:"), "fixture bucket must already exist")
	f.database = app.DatabaseConfig{DatabaseURL: dbURL, Schema: "ai_assess_" + id, ApplicationName: "ai-assess-" + id,
		MaxConnections: 3, Now: f.now, LogOutput: &f.log}
	f.config = app.Config{DatabaseURL: dbURL, Schema: f.database.Schema, ApplicationName: f.database.ApplicationName,
		MaxConnections: 3, Now: f.now, LogOutput: &f.log, IntegrationEncryptionKey: f.key, BootstrapToken: secret(t),
		Storage: f.rawStorage, ManualProcessing: true, PublicOrigin: "https://ai-assessments.synthetic.invalid",
		MaxUploadBytes: 256 << 10, SessionTTL: time.Hour}
	for _, value := range []string{dbURL, password, f.config.BootstrapToken, f.rawStorage.AccessKey, f.rawStorage.SecretKey,
		string(f.key), hex.EncodeToString(f.key), base64.StdEncoding.EncodeToString(f.key)} {
		f.remember(value)
	}
	pc, err := pgxpool.ParseConfig(dbURL)
	must(t, "parse fixture PG pool", err)
	pc.MaxConns, pc.MinConns = 2, 0
	pc.ConnConfig.ConnectTimeout = 3 * time.Second
	pc.ConnConfig.RuntimeParams["application_name"] = f.database.ApplicationName + "-fixture"
	f.db, err = pgxpool.NewWithConfig(ctx, pc)
	must(t, "open actual fixture pool", err)
	t.Cleanup(f.db.Close)
	must(t, "authenticate actual fixture PG", f.db.Ping(ctx))
	_, err = f.db.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{f.database.Schema}.Sanitize())
	must(t, "create only a random owned schema", err)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != storeURL.Host {
			f.foreignDials.Add(1)
			return nil, errors.New("fixture forbids another intake-store origin")
		}
		return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, network, address)
	}
	t.Cleanup(transport.CloseIdleConnections)
	f.s3 = s3.New(s3.Options{Region: "us-east-1", BaseEndpoint: aws.String(endpoint), UsePathStyle: true, RetryMaxAttempts: 1,
		Credentials: credentials.NewStaticCredentialsProvider(f.rawStorage.AccessKey, f.rawStorage.SecretKey, ""),
		HTTPClient: &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
			return errors.New("fixture redirects denied")
		}}, RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired})
	_, err = f.s3.HeadBucket(ctx, &s3.HeadBucketInput{Bucket: aws.String(f.rawStorage.Bucket)})
	must(t, "authenticate only the existing owned intake bucket", err)
	t.Cleanup(f.cleanup)
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.storageCalls.Add(1)
		clone := r.Clone(r.Context())
		target := *r.URL
		target.Scheme, target.Host = storeURL.Scheme, storeURL.Host
		clone.URL, clone.RequestURI = &target, ""
		response, failure := transport.RoundTrip(clone)
		if failure != nil {
			http.Error(w, "owned intake unavailable", 503)
			return
		}
		defer response.Body.Close()
		for key, values := range response.Header {
			for _, value := range values {
				w.Header().Add(key, value)
			}
		}
		w.WriteHeader(response.StatusCode)
		_, _ = io.Copy(w, io.LimitReader(response.Body, 512<<10))
	}))
	t.Cleanup(proxy.Close)
	f.config.Storage.Endpoint = proxy.URL
	proxyURL, err := url.Parse(proxy.URL)
	must(t, "parse owned intake proxy", err)
	original := http.DefaultTransport
	guard := original.(*http.Transport).Clone()
	guard.Proxy = nil
	guard.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address == proxyURL.Host {
			return (&net.Dialer{Timeout: 3 * time.Second}).DialContext(ctx, network, address)
		}
		f.foreignDials.Add(1)
		return nil, errors.New("ambient HTTP/provider discovery is forbidden")
	}
	http.DefaultTransport = guard
	t.Cleanup(func() {
		http.DefaultTransport = original
		guard.CloseIdleConnections()
		check(t, f.foreignDials.Load() == 0, "a fixture attempted ambient or nonowned network")
		f.noSecrets(f.log.data())
	})
	f.native = newNative(t, f)
	return f
}

func (f *fixture) cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	check(f.t, strings.HasPrefix(f.database.Schema, "ai_assess_") &&
		f.rawStorage.Prefix == "assessment-fixture/"+strings.TrimPrefix(f.database.Schema, "ai_assess_")+"/",
		"refusing unowned cleanup")
	page, err := f.s3.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: aws.String(f.rawStorage.Bucket),
		Prefix: aws.String(f.rawStorage.Prefix), MaxKeys: aws.Int32(100)})
	must(f.t, "list only owned intake objects for cleanup", err)
	check(f.t, !aws.ToBool(page.IsTruncated), "fixture object bound exceeded")
	for _, item := range page.Contents {
		check(f.t, strings.HasPrefix(aws.ToString(item.Key), f.rawStorage.Prefix), "unowned object cleanup denied")
		_, err = f.s3.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(f.rawStorage.Bucket), Key: item.Key})
		must(f.t, "remove only owned intake object", err)
	}
	_, err = f.db.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{f.database.Schema}.Sanitize()+" CASCADE")
	must(f.t, "drop only owned assessment schema", err)
}

type harness struct {
	*fixture
	core        *app.Application
	admin       actor
	fixtureOnly bool
}

func newHarness(t *testing.T, fixtureOnly bool) *harness {
	h := &harness{fixture: newFixture(t), fixtureOnly: fixtureOnly}
	h.open()
	h.enroll()
	return h
}
func (h *harness) open() {
	var err error
	if h.fixtureOnly {
		h.core, err = app.Open(h.ctx, h.config)
	} else {
		check(h.t, Production.OpenCore != nil, "BLOCKED: real assessment core binding missing")
		h.core, err = Production.OpenCore(h.ctx, h.config, h.scope)
	}
	must(h.t, "open actual application core", err)
	check(h.t, h.core != nil, "actual core constructor returned nil")
	core := h.core
	h.t.Cleanup(func() { must(h.t, "close owned application", core.Close()) })
}
func (h *harness) reopen() { must(h.t, "close owned core before reopen", h.core.Close()); h.open() }
func (h *harness) request(who actor, method, route string, body any, status int, headers ...string) *httptest.ResponseRecorder {
	h.t.Helper()
	check(h.t, h.apiCalls.Add(1) <= 220, "bounded fixture API budget exceeded")
	var data []byte
	if body != nil {
		data = encoded(h.t, body)
	}
	request := httptest.NewRequest(method, h.config.PublicOrigin+route, bytes.NewReader(data)).WithContext(h.ctx)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", h.config.PublicOrigin)
	if who.Workspace != "" {
		request.Header.Set("X-ASPM-Workspace-ID", who.Workspace)
	}
	if who.Cookie != nil {
		request.AddCookie(who.Cookie)
	}
	for index := 0; index < len(headers); index += 2 {
		request.Header.Set(headers[index], headers[index+1])
	}
	response := httptest.NewRecorder()
	h.core.Handler.ServeHTTP(response, request)
	if response.Code != status {
		h.t.Fatalf("actual %s %s returned %d, want %d; private response withheld", method, route, response.Code, status)
	}
	h.noSecrets(response.Body.Bytes())
	check(h.t, response.Body.Len() <= 512<<10, "unbounded public assessment metadata")
	if strings.Contains(route, "/assessments") || strings.Contains(route, "/assessment-previews") {
		var value any
		must(h.t, "inspect secret-safe assessment DTO", json.Unmarshal(response.Body.Bytes(), &value))
		var inspect func(any)
		inspect = func(value any) {
			switch item := value.(type) {
			case map[string]any:
				for key, child := range item {
					switch key {
					case "apiKey", "ciphertext", "credentialCiphertext", "encryptionKey", "headers", "rawResponse", "providerEnvelope":
						h.t.Fatal("secret or raw provider envelope escaped the assessment DTO")
					}
					inspect(child)
				}
			case []any:
				for _, child := range item {
					inspect(child)
				}
			}
		}
		inspect(value)
	}
	return response
}
func (h *harness) json(who actor, method, route string, body any, status int, headers ...string) reply {
	h.t.Helper()
	var value reply
	must(h.t, "decode actual API envelope", json.Unmarshal(h.request(who, method, route, body, status, headers...).Body.Bytes(), &value))
	check(h.t, value.APIVersion == apiVersion, "API version lost")
	if status >= 400 {
		check(h.t, value.Error != nil && value.Error.Code != "" && !value.Error.Retryable, "rejection must be explicit and nonretryable")
	}
	return value
}
func (h *harness) enroll() {
	password := secret(h.t)
	h.remember(password)
	value := h.json(actor{}, "POST", "/api/v1/bootstrap", map[string]any{
		"workspaceName": "Owned assessment workspace", "name": "Synthetic assessment admin",
		"email": "admin@assessment.invalid", "password": password,
	}, 201, "X-ASPM-Bootstrap-Token", h.config.BootstrapToken)
	h.admin = h.login("admin@assessment.invalid", password, value.Workspace.ID)
}
func (h *harness) login(email, password, workspace string) actor {
	response := h.request(actor{}, "POST", "/api/v1/login", map[string]any{"email": email, "password": password}, 200)
	var value reply
	must(h.t, "decode actual session", json.Unmarshal(response.Body.Bytes(), &value))
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == "aspm_session" {
			check(h.t, cookie.HttpOnly && cookie.Secure, "real protected cookie required")
			h.remember(cookie.Value)
			return actor{ID: value.User.ID, Workspace: workspace, Cookie: cookie}
		}
	}
	h.t.Fatal("actual login omitted session")
	return actor{}
}
func (h *harness) addUser(role string) actor {
	password, email := secret(h.t), nonce(h.t)+"@assessment.invalid"
	h.remember(password)
	h.json(h.admin, "POST", "/api/v1/users", map[string]any{"name": "Synthetic " + role, "email": email, "password": password, "role": role}, 201)
	return h.login(email, password, h.admin.Workspace)
}
func (h *harness) otherWorkspace() actor {
	value := h.json(h.admin, "POST", "/api/v1/workspaces", map[string]string{"name": "Other owned workspace"}, 201)
	other := h.admin
	other.Workspace = value.Workspace.ID
	return other
}
func reportBytes(t *testing.T, message string) []byte {
	return encoded(t, map[string]any{"version": "2.1.0", "runs": []any{map[string]any{
		"tool": map[string]any{"driver": map[string]any{"name": "Owned assessment SARIF", "rules": []any{map[string]any{
			"id": "SYNTHETIC-ASSESS-1", "shortDescription": map[string]string{"text": "Synthetic reviewed configuration"},
			"fullDescription": map[string]string{"text": "Derived source description, not automatically approved."},
		}}}},
		"results": []any{map[string]any{"ruleId": "SYNTHETIC-ASSESS-1", "guid": "11111111-1111-4111-8111-111111111111",
			"level": "warning", "message": map[string]string{"text": message},
			"locations": []any{map[string]any{"physicalLocation": map[string]any{
				"artifactLocation": map[string]string{"uri": "synthetic/config.txt"}, "region": map[string]int{"startLine": 12},
			}}}, "properties": map[string]string{"unmapped-secret-canary": "SYNTHETIC-RAW-NOT-APPROVED"}},
		},
	}}})
}
func (h *harness) ingest(who actor, assetID, scan, message string) (app.Import, app.Finding) {
	h.t.Helper()
	now := h.now().Add(-time.Minute)
	input := map[string]any{"apiVersion": apiVersion, "assetId": assetID, "format": "sarif",
		"report": string(reportBytes(h.t, message)), "sourceId": "owned-assessment-source", "scanId": scan,
		"scope":        app.Scope{ID: "owned-repository", Revision: "1", Branch: "refs/heads/main"},
		"sourceScanAt": now, "collectedAt": now.Add(time.Second), "sourceStatus": "succeeded", "scanKind": "full", "completeness": "complete"}
	queued := h.json(who, "POST", "/api/v1/imports", input, 202).Import
	ctx, cancel := context.WithTimeout(h.ctx, 10*time.Second)
	defer cancel()
	must(h.t, "perform legitimate report ingestion", h.core.ProcessImports(ctx))
	done := h.json(who, "GET", "/api/v1/imports/"+queued.ID, nil, 200).Import
	check(h.t, done.State == "succeeded" && done.ObservationCount == 1, "real intake did not produce an observation")
	work := h.json(who, "GET", "/api/v1/work", nil, 200)
	for _, item := range work.Items {
		finding := h.finding(who, item["id"].(string))
		if finding.AssetID == assetID {
			check(h.t, len(finding.Observations) > 0, "finding omitted real observation")
			return done, finding
		}
	}
	h.t.Fatal("real ingestion did not create the selected finding")
	return app.Import{}, app.Finding{}
}
func (h *harness) seed() app.Finding {
	asset := h.json(h.admin, "POST", "/api/v1/assets", map[string]any{"name": "Owned assessment repository",
		"kind": "repository", "environment": "test", "criticality": "medium", "tags": []string{"synthetic"},
		"ownerId": h.admin.ID}, 201).Asset
	_, finding := h.ingest(h.admin, asset.ID, "scan-1", "SYNTHETIC-RAW-NOT-APPROVED: analyst must supply an independently reviewed context.")
	h.json(h.admin, "PATCH", "/api/v1/findings/"+finding.ID, map[string]any{
		"ownerId": h.admin.ID, "workflowState": "in-progress", "disposition": "accepted-risk", "acceptedRiskExpiresAt": h.now().Add(time.Hour),
	}, 200)
	h.json(h.admin, "POST", "/api/v1/findings/"+finding.ID+"/notes", map[string]string{"text": "SYNTHETIC-NOTE-NOT-APPROVED"}, 201)
	return h.finding(h.admin, finding.ID)
}
func (h *harness) finding(who actor, id string) app.Finding {
	return h.json(who, "GET", "/api/v1/findings/"+id, nil, 200).Finding
}
func (h *harness) profile(family string, reviewed bool) (app.AIProfile, string) {
	key, endpoint := secret(h.t), h.native.tls.URL
	if family == "local" {
		key, endpoint = "", h.native.local.URL
	}
	h.remember(key)
	deployment := ""
	if family == "azure-foundry" {
		deployment = "operator-selected-deployment"
	}
	input := map[string]any{"name": "Synthetic " + family, "family": family, "endpoint": endpoint,
		"model": "operator-selected-model", "deployment": deployment, "enabled": true, "structuredOutput": reviewed}
	if key != "" {
		input["apiKey"] = key
	}
	p := h.json(h.admin, "POST", profilesPath, input, 201).Profile
	check(h.t, p.ID != "" && p.Revision != "" && p.Model == input["model"] && p.Deployment == deployment, "explicit real profile metadata lost")
	return p, key
}
func (h *harness) approve(p app.AIProfile) (app.AIPolicy, app.AIEgressGrant) {
	mode := "approved-hosted"
	if p.Family == "local" {
		mode = "local-only"
	}
	pol := h.json(h.admin, "PATCH", policyPath, map[string]string{"mode": mode}, 200).Policy
	if mode == "local-only" {
		return pol, app.AIEgressGrant{}
	}
	g := h.json(h.admin, "POST", grantsPath, map[string]any{"profileId": p.ID, "profileRevision": p.Revision,
		"policyRevision": pol.Revision, "destination": p.Endpoint, "task": validityTask, "dataClass": evidenceClass,
		"expiresAt": h.now().Add(time.Hour).Format(time.RFC3339Nano)}, 201).Grant
	return pol, g
}
func reviewedContext() string {
	return "USER-REVIEWED DERIVED CONTEXT ONLY\r\nSynthetic configuration concern in reviewed component A.\nNo original report, secrets or implicit source verification. 安全 review."
}
func (h *harness) preview(who actor, finding app.Finding, p app.AIProfile, pol app.AIPolicy, g app.AIEgressGrant, text string) preview {
	h.t.Helper()
	before := h.native.count()
	v := h.json(who, "POST", previewPath(finding.ID), map[string]any{"observationId": finding.Observations[0].ID,
		"profileId": p.ID, "grantId": g.ID, "context": text, "reviewed": true}, 201).Preview
	check(h.t, v.ID != "" && v.WorkspaceID == who.Workspace && v.FindingID == finding.ID &&
		v.ObservationID == finding.Observations[0].ID && v.SourceEvidenceDigest == finding.Observations[0].EvidenceDigest &&
		v.RequestedBy == who.ID && v.ProfileID == p.ID && v.ProfileRevision == p.Revision &&
		v.PolicyRevision == pol.Revision && v.GrantID == g.ID && v.Destination == p.Endpoint &&
		v.Family == p.Family && v.Model == p.Model && v.Deployment == p.Deployment &&
		v.Task == validityTask && v.DataClass == evidenceClass && v.PromptRevision == promptRevision &&
		v.ContextOrigin == contextOrigin && v.Context == text && v.ContextDigest == digest([]byte(text)) &&
		v.ContextRef == "reviewed-context:"+v.ID && v.ExpiresAt.After(v.CreatedAt) &&
		!v.ExpiresAt.After(v.CreatedAt.Add(5*time.Minute)), "preview did not bind exact trusted identities and reviewed bytes")
	if g.ID != "" {
		check(h.t, !v.ExpiresAt.After(g.ExpiresAt), "preview outlived its explicit hosted grant")
	}
	check(h.t, h.native.count() == before, "preview performed inference")
	return v
}
func (h *harness) enqueue(who actor, v preview, key string, status int) assessment {
	h.t.Helper()
	before := h.native.count()
	a := h.json(who, "POST", historyPath(v.FindingID), map[string]any{"previewId": v.ID, "idempotencyKey": key, "consent": true}, status).Assessment
	check(h.t, a.ID != "" && reflect.DeepEqual(a.binding, v.binding) && a.PreviewID == v.ID &&
		a.IdempotencyKey == key && a.Scope == h.scope && a.ConsentExpiresAt.Equal(v.ExpiresAt) && a.AdvisoryOnly,
		"queue lost exact immutable preview/requester/scope binding")
	if status == 202 {
		check(h.t, a.State == "queued" && a.Attempts == 0 && a.DispatchState == "not-started" && a.DispatchStartedAt == nil &&
			a.Result == nil && a.CompletedAt == nil, "enqueue fabricated dispatch or assessment success")
	}
	check(h.t, h.native.count() == before, "enqueue/replay performed inference")
	return a
}
func (h *harness) job(who actor, id string) assessment {
	return h.json(who, "GET", jobsPath+"/"+id, nil, 200).Assessment
}
func sameAssessment(a, b assessment) bool {
	sameOptional := func(one, two *time.Time) bool {
		return one == nil && two == nil || one != nil && two != nil && one.Equal(*two)
	}
	if !a.CreatedAt.Equal(b.CreatedAt) || !a.ConsentExpiresAt.Equal(b.ConsentExpiresAt) ||
		!sameOptional(a.DispatchStartedAt, b.DispatchStartedAt) || !sameOptional(a.CompletedAt, b.CompletedAt) {
		return false
	}
	b.CreatedAt, b.ConsentExpiresAt, b.DispatchStartedAt, b.CompletedAt = a.CreatedAt, a.ConsentExpiresAt, a.DispatchStartedAt, a.CompletedAt
	return reflect.DeepEqual(a, b)
}
func (h *harness) configForWorker(id string) workerConfig {
	db := h.database
	db.MaxConnections = 1
	db.ApplicationName = "assessment-worker-" + nonce(h.t)
	return workerConfig{Database: db, EncryptionKey: h.key, WorkerID: id, Scope: h.scope, Client: h.native.client,
		LeaseDuration: time.Second, AuthorizationInterval: 25 * time.Millisecond, RequestTimeout: 3 * time.Second,
		RequestWindow: time.Minute, MaxConcurrent: 1, RequestsPerWindow: 32, MaxInputBytes: contextLimit,
		MaxOutputTokens: 128, MaxResponseBytes: 32 << 10}
}
func (h *harness) worker(config workerConfig) assessmentWorker {
	h.t.Helper()
	check(h.t, Production.OpenWorker != nil, "BLOCKED: real independently opened assessment worker missing")
	w, err := Production.OpenWorker(h.ctx, config)
	must(h.t, "open real assessment worker", err)
	check(h.t, w != nil, "assessment worker is nil")
	h.t.Cleanup(func() { must(h.t, "close only the owned worker", w.Close()) })
	return w
}
func process(t *testing.T, ctx context.Context, w assessmentWorker, want bool) {
	t.Helper()
	got, err := w.ProcessNext(ctx)
	must(t, "process one actual assessment scheduling step", err)
	check(t, got == want, "worker claim/capacity result differs")
}
func event(t *testing.T, ctx context.Context, channel <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-channel:
	case <-ctx.Done():
		t.Fatal("bounded actual event not reached: " + label)
	}
}
func wait(t *testing.T, ctx context.Context, label string, condition func() bool) {
	t.Helper()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	for {
		if condition() {
			return
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			t.Fatal("bounded readonly observation not reached: " + label)
		}
	}
}
func (h *harness) domainSnapshot() []string {
	h.t.Helper()
	var result []string
	for _, table := range []string{"assets", "findings", "observations", "notes", "imports", "coverage", "source_connections", "source_collections", "finding_deliveries"} {
		rows, err := h.db.Query(h.ctx, "SELECT to_jsonb(v)::text FROM "+h.table(table)+" v ORDER BY to_jsonb(v)::text")
		must(h.t, "readonly domain snapshot", err)
		for rows.Next() {
			var value string
			must(h.t, "read domain snapshot row", rows.Scan(&value))
			result = append(result, table+":"+value)
		}
		must(h.t, "finish domain snapshot", rows.Err())
		rows.Close()
	}
	return result
}
func (h *harness) assertReadonly(before []string, storeCalls int32) {
	h.t.Helper()
	check(h.t, reflect.DeepEqual(before, h.domainSnapshot()), "assessment mutated findings/source/workflow/owner/risk/notes/intake state")
	check(h.t, h.storageCalls.Load() == storeCalls, "assessment obtained raw S3/operator storage authority")
	h.noSecrets(h.log.data())
}
func (h *harness) assertResult(job assessment, p app.AIProfile, v preview, conclusion string) {
	h.t.Helper()
	check(h.t, job.State == "succeeded" && job.Result != nil && job.Failure == nil && job.Attempts == 1 &&
		job.DispatchState == "response-received" && job.DispatchStartedAt != nil && job.CompletedAt != nil && job.AdvisoryOnly,
		"real worker omitted its bounded completed advisory or dispatch provenance")
	check(h.t, reflect.DeepEqual(job.binding, v.binding) && job.Result.Conclusion == conclusion &&
		job.Result.Uncertainty != "" && reflect.DeepEqual(job.Result.EvidenceRefs, []string{v.ContextRef}),
		"advisory lost the exact approved context or claimed ungrounded source proof")
	check(h.t, job.RequestedModel == p.Model && job.Deployment == p.Deployment &&
		job.ReturnedModel == "fixture-returned-model" && job.RequestID == "fixture-native-request", "provider audit metadata lost")
}
func (h *harness) readMarker(jobID string) {
	h.t.Helper()
	var state string
	var attempts int
	var started *time.Time
	err := h.db.QueryRow(h.ctx, "SELECT state,attempts,dispatch_started_at FROM "+h.table("assessment_jobs")+" WHERE id=$1", jobID).Scan(&state, &attempts, &started)
	must(h.t, "observe committed dispatch marker from independent pool", err)
	check(h.t, state == "dispatching" && attempts == 1 && started != nil, "native POST preceded committed dispatch marker")
}
func (h *harness) resolver() *app.AIConfigurationResolver {
	r, err := app.OpenAIConfigurationResolver(h.ctx, app.AIConfigurationResolverConfig{Database: h.database, EncryptionKey: h.key})
	must(h.t, "open existing trusted DB configuration resolver", err)
	h.t.Cleanup(func() { must(h.t, "close configuration resolver", r.Close()) })
	return r
}
func (h *harness) resolved(p app.AIProfile, g app.AIEgressGrant) app.AIConfiguration {
	value, err := h.resolver().Resolve(h.ctx, app.AIConfigurationRequest{WorkspaceID: h.admin.Workspace, ActorID: h.admin.ID,
		ProfileID: p.ID, GrantID: g.ID, Task: validityTask, DataClass: evidenceClass})
	must(h.t, "resolve current real persisted configuration", err)
	return value
}
