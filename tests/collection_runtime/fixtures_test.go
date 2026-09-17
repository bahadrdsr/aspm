//go:build integration

package collection_runtime

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/smithy-go"
	"github.com/bahadrdsr/aspm/internal/app"
	"github.com/bahadrdsr/aspm/internal/connectors"
	"github.com/bahadrdsr/aspm/internal/evidence"
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
	_, err := rand.Read(b)
	must(t, "generate owned private input", err)
	return b
}
func nonce(t *testing.T) string { return hex.EncodeToString(random(t, 12)) }
func secret(t *testing.T) string {
	return "synthetic-collection-runtime-" + base64.RawURLEncoding.EncodeToString(random(t, 24))
}
func encode(t *testing.T, v any) []byte {
	t.Helper()
	b, e := json.Marshal(v)
	must(t, "encode owned API input", e)
	return b
}
func digest(b []byte) string { s := sha256.Sum256(b); return "sha256:" + hex.EncodeToString(s[:]) }
func required(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	check(t, v != "", "BLOCKED: private fixture field "+name+" missing")
	return v
}

type safeLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *safeLog) Write(b []byte) (int, error) { l.mu.Lock(); defer l.mu.Unlock(); return l.b.Write(b) }
func (l *safeLog) data() []byte                { l.mu.Lock(); defer l.mu.Unlock(); return bytes.Clone(l.b.Bytes()) }

type fixture struct {
	t                          *testing.T
	ctx                        context.Context
	db                         *pgxpool.Pool
	database                   app.DatabaseConfig
	raw                        evidence.Config
	read, publish              app.StorageConfig
	adminConfig                app.StorageConfig
	readS3, publishS3, adminS3 *s3.Client
	readTap, publishTap        *storageTap
	key                        []byte
	bootstrap                  string
	log                        safeLog
	mu                         sync.Mutex
	allowed                    map[string]bool
	secrets                    []string
	violations                 []string
}

func (f *fixture) remember(value string) {
	if value != "" {
		f.mu.Lock()
		f.secrets = append(f.secrets, value, url.QueryEscape(value), base64.StdEncoding.EncodeToString([]byte(value)))
		f.mu.Unlock()
	}
}
func (f *fixture) noSecrets(data []byte) {
	f.t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, s := range f.secrets {
		check(f.t, !bytes.Contains(data, []byte(s)), "private fixture input escaped API/log output")
	}
}
func (f *fixture) violate(message string) {
	f.mu.Lock()
	f.violations = append(f.violations, message)
	f.mu.Unlock()
}

func localURL(t *testing.T, value string, schemes ...string) *url.URL {
	t.Helper()
	u, err := url.Parse(value)
	must(t, "parse selected local URL", err)
	allowed := false
	for _, s := range schemes {
		allowed = allowed || u.Scheme == s
	}
	check(t, allowed && u.Hostname() == "127.0.0.1" && u.Port() != "", "only explicit owned loopback fixtures are allowed")
	return u
}
func newFixture(t *testing.T) *fixture {
	t.Helper()
	id := nonce(t)
	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Second)
	t.Cleanup(cancel)
	f := &fixture{t: t, ctx: ctx, key: random(t, 32), bootstrap: secret(t), allowed: map[string]bool{}}
	f.database = app.DatabaseConfig{DatabaseURL: required(t, "ASPM_COLLECTION_RUNTIME_DATABASE_URL"), Schema: "collection_runtime_" + id, ApplicationName: "collection-runtime-" + id, MaxConnections: 3}
	f.raw = evidence.Config{Endpoint: required(t, "ASPM_COLLECTION_RUNTIME_RAW_ENDPOINT"), Bucket: required(t, "ASPM_COLLECTION_RUNTIME_RAW_BUCKET"),
		Prefix: "collection-runtime-raw/" + id + "/", Region: "us-east-1", AccessKey: required(t, "ASPM_COLLECTION_RUNTIME_RAW_ACCESS_KEY"), SecretKey: required(t, "ASPM_COLLECTION_RUNTIME_RAW_SECRET_KEY"), Timeout: 4 * time.Second}
	f.read = app.StorageConfig{Endpoint: required(t, "ASPM_COLLECTION_RUNTIME_EVIDENCE_ENDPOINT"), Bucket: required(t, "ASPM_COLLECTION_RUNTIME_EVIDENCE_BUCKET"),
		Prefix: "approved/team-a/grant-1/collection-runtime-" + id + "/", Region: required(t, "ASPM_COLLECTION_RUNTIME_EVIDENCE_REGION"),
		AccessKey: required(t, "ASPM_COLLECTION_RUNTIME_READER_ACCESS_KEY"), SecretKey: required(t, "ASPM_COLLECTION_RUNTIME_READER_SECRET_KEY"), Timeout: 4 * time.Second}
	f.publish = f.read
	f.publish.AccessKey = required(t, "ASPM_COLLECTION_RUNTIME_PUBLISHER_ACCESS_KEY")
	f.publish.SecretKey = required(t, "ASPM_COLLECTION_RUNTIME_PUBLISHER_SECRET_KEY")
	f.adminConfig = f.read
	f.adminConfig.AccessKey = required(t, "ASPM_COLLECTION_RUNTIME_ADMIN_ACCESS_KEY")
	f.adminConfig.SecretKey = required(t, "ASPM_COLLECTION_RUNTIME_ADMIN_SECRET_KEY")
	dbURL := localURL(t, f.database.DatabaseURL, "postgres", "postgresql")
	check(t, dbURL.User != nil, "explicit PG identity required")
	password, present := dbURL.User.Password()
	check(t, present && password != "", "explicit PG password required")
	rawURL := localURL(t, f.raw.Endpoint, "http", "https")
	selectedURL := localURL(t, f.read.Endpoint, "http", "https")
	check(t, f.read.Bucket == "aspm-isolation" && selectedURL.Port() == "18335", "only the existing owned scoped-policy store is selected")
	check(t, f.read.AccessKey != f.publish.AccessKey && f.read.AccessKey != f.adminConfig.AccessKey && f.publish.AccessKey != f.adminConfig.AccessKey, "actual source evidence identities must be distinct")
	for _, v := range []string{f.database.DatabaseURL, password, f.raw.AccessKey, f.raw.SecretKey, f.read.AccessKey, f.read.SecretKey, f.publish.AccessKey, f.publish.SecretKey, f.adminConfig.AccessKey, f.adminConfig.SecretKey, f.bootstrap, string(f.key), hex.EncodeToString(f.key), base64.StdEncoding.EncodeToString(f.key)} {
		f.remember(v)
	}
	f.allowed[rawURL.Host] = true
	f.allowed[selectedURL.Host] = true
	original, logger := http.DefaultTransport, slog.Default()
	guard := original.(*http.Transport).Clone()
	guard.Proxy = nil
	guard.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		f.mu.Lock()
		allowed := f.allowed[address]
		f.mu.Unlock()
		if !allowed {
			f.violate("ambient HTTP or credential-discovery destination blocked")
			return nil, errors.New("owned fixture ambient boundary")
		}
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, address)
	}
	http.DefaultTransport = guard
	slog.SetDefault(slog.New(slog.NewTextHandler(&f.log, nil)))
	t.Cleanup(func() {
		http.DefaultTransport = original
		slog.SetDefault(logger)
		guard.CloseIdleConnections()
		f.noSecrets(f.log.data())
		f.mu.Lock()
		count := len(f.violations)
		f.mu.Unlock()
		check(t, count == 0, "unapproved storage/provider operation attempted")
	})
	pc, err := pgxpool.ParseConfig(f.database.DatabaseURL)
	must(t, "parse owned PG pool", err)
	pc.MaxConns = 2
	pc.MinConns = 0
	pc.ConnConfig.RuntimeParams["application_name"] = f.database.ApplicationName + "-fixture"
	f.db, err = pgxpool.NewWithConfig(ctx, pc)
	must(t, "open actual owned PG fixture", err)
	t.Cleanup(f.db.Close)
	must(t, "authenticate PG fixture", f.db.Ping(ctx))
	_, err = f.db.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{f.database.Schema}.Sanitize())
	must(t, "create only random owned schema", err)
	f.readS3 = f.s3(f.read)
	f.publishS3 = f.s3(f.publish)
	f.adminS3 = f.s3(f.adminConfig)
	t.Cleanup(f.cleanup)
	f.readTap = f.tap(f.read, true)
	f.publishTap = f.tap(f.publish, false)
	return f
}
func (f *fixture) s3(config app.StorageConfig) *s3.Client {
	client := &http.Client{Transport: http.DefaultTransport.(*http.Transport).Clone(), Timeout: 4 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("storage redirect denied") }}
	f.t.Cleanup(client.CloseIdleConnections)
	return s3.New(s3.Options{Region: config.Region, BaseEndpoint: aws.String(config.Endpoint), UsePathStyle: true,
		Credentials: credentials.NewStaticCredentialsProvider(config.AccessKey, config.SecretKey, ""), HTTPClient: client, RetryMaxAttempts: 1,
		RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired, ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired})
}
func (f *fixture) rights() {
	f.t.Helper()
	data := []byte("owned real read-only collection credential probe")
	key := f.read.Prefix + "permission-probe"
	_, err := f.publishS3.PutObject(f.ctx, &s3.PutObjectInput{Bucket: aws.String(f.read.Bucket), Key: aws.String(key), Body: bytes.NewReader(data)})
	must(f.t, "actual publisher key write to owned prefix", err)
	object, err := f.readS3.GetObject(f.ctx, &s3.GetObjectInput{Bucket: aws.String(f.read.Bucket), Key: aws.String(key)})
	must(f.t, "actual core RO key read from selected prefix", err)
	got, err := io.ReadAll(io.LimitReader(object.Body, 4096))
	must(f.t, "complete real RO key read", errors.Join(err, object.Body.Close()))
	check(f.t, bytes.Equal(got, data), "real storage read changed bytes")
	_, err = f.readS3.PutObject(f.ctx, &s3.PutObjectInput{Bucket: aws.String(f.read.Bucket), Key: aws.String(f.read.Prefix + "reader-write-denied"), Body: bytes.NewReader(data)})
	var status interface{ HTTPStatusCode() int }
	var apiError smithy.APIError
	check(f.t, errors.As(err, &status) && status.HTTPStatusCode() == 403 && errors.As(err, &apiError) && apiError.ErrorCode() == "AccessDenied", "real selected core identity was not denied S3 PutObject with 403/AccessDenied")
	f.t.Log("actual store-key proof: selected publisher PUT and selected core-RO GET succeed; core-RO PUT denied by real S3 403/AccessDenied")
}
func (f *fixture) cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	if !strings.HasPrefix(f.database.Schema, "collection_runtime_") || !strings.HasPrefix(f.read.Prefix, "approved/team-a/grant-1/collection-runtime-") {
		f.t.Error("refusing non-owned cleanup")
		return
	}
	pages := s3.NewListObjectsV2Paginator(f.adminS3, &s3.ListObjectsV2Input{Bucket: aws.String(f.read.Bucket), Prefix: aws.String(f.read.Prefix), MaxKeys: aws.Int32(100)})
	for n := 0; pages.HasMorePages() && n < 4; n++ {
		page, err := pages.NextPage(ctx)
		if err != nil {
			f.t.Errorf("owned prefix cleanup failed (%T)", err)
			break
		}
		for _, item := range page.Contents {
			if !strings.HasPrefix(aws.ToString(item.Key), f.read.Prefix) {
				f.t.Error("outside-prefix cleanup refused")
				continue
			}
			_, err = f.adminS3.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(f.read.Bucket), Key: item.Key})
			if err != nil {
				f.t.Errorf("owned deletion failed (%T)", err)
			}
		}
	}
	if pages.HasMorePages() {
		f.t.Error("owned cleanup page bound exceeded")
	}
	_, err := f.db.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{f.database.Schema}.Sanitize()+" CASCADE")
	if err != nil {
		f.t.Errorf("owned schema cleanup failed (%T)", err)
	}
}

type putGate struct {
	arrived, done, release chan struct{}
	once                   sync.Once
}

func (g *putGate) allow() { g.once.Do(func() { close(g.release) }) }

type storageTap struct {
	server *httptest.Server
	mu     sync.Mutex
	next   *putGate
	calls  atomic.Int32
	writes atomic.Int32
}

func (s *storageTap) holdPut() *putGate {
	g := &putGate{arrived: make(chan struct{}), done: make(chan struct{}), release: make(chan struct{})}
	s.mu.Lock()
	s.next = g
	s.mu.Unlock()
	return g
}
func (f *fixture) tap(config app.StorageConfig, readOnly bool) *storageTap {
	target, err := url.Parse(config.Endpoint)
	must(f.t, "parse selected storage forwarding target", err)
	tap := &storageTap{}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorLog = log.New(io.Discard, "", 0)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	proxy.Transport = transport
	f.t.Cleanup(transport.CloseIdleConnections)
	tap.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/"+config.Bucket+"/")
		_, signed, hasCredential := strings.Cut(r.Header.Get("Authorization"), "Credential=")
		access, _, _ := strings.Cut(signed, "/")
		query := r.URL.Query()
		operation := map[string]string{"GET": "GetObject", "HEAD": "HeadObject", "PUT": "PutObject"}[r.Method]
		queryOK := r.URL.RawQuery == "" || len(query) == 1 && len(query["x-id"]) == 1 && query.Get("x-id") == operation
		allowed := hasCredential && access == config.AccessKey && strings.HasPrefix(key, config.Prefix) && queryOK && (r.Method == "GET" || r.Method == "HEAD" || !readOnly && r.Method == "PUT")
		if !allowed {
			f.violate("selected storage tap rejected wrong signer/prefix/method/bucket probe or ungranted write")
			http.Error(w, "owned source capability denied", 403)
			return
		}
		tap.calls.Add(1)
		if r.Method == "PUT" {
			tap.writes.Add(1)
			tap.mu.Lock()
			gate := tap.next
			tap.next = nil
			tap.mu.Unlock()
			if gate != nil {
				defer close(gate.done)
				body, err := io.ReadAll(io.LimitReader(r.Body, (32<<20)+1))
				if err != nil || len(body) > 32<<20 {
					f.violate("owned held storage body read failed")
					http.Error(w, "owned body failure", 400)
					return
				}
				r.Body = io.NopCloser(bytes.NewReader(body))
				close(gate.arrived)
				select {
				case <-r.Context().Done():
					return
				case <-gate.release:
				}
			}
		}
		proxy.ServeHTTP(w, r)
	}))
	f.mu.Lock()
	f.allowed[tap.server.Listener.Addr().String()] = true
	f.mu.Unlock()
	f.t.Cleanup(tap.server.Close)
	return tap
}

func ownedAddress(t *testing.T) string {
	t.Helper()
	l, e := net.Listen("tcp", "127.0.0.1:0")
	must(t, "reserve owned listener", e)
	a := l.Addr().String()
	must(t, "release listener for actual service", l.Close())
	return a
}
func ownedClient(t *testing.T, address string) *http.Client {
	t.Helper()
	transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, network, target string) (net.Conn, error) {
		if target != address {
			return nil, errors.New("unapproved service dial")
		}
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, target)
	}}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect denied") }}
}

type runningRole struct {
	cancel  context.CancelFunc
	done    chan struct{}
	err     error
	address string
	client  *http.Client
}

func startRole(t *testing.T, parent context.Context, role string, config runtimeConfig) *runningRole {
	t.Helper()
	check(t, Production.Run != nil, "real service.Run binding missing")
	ctx, cancel := context.WithCancel(parent)
	r := &runningRole{cancel: cancel, done: make(chan struct{}), address: config.Listen, client: ownedClient(t, config.Listen)}
	t.Cleanup(func() { stopRole(t, r) })
	go func() { r.err = Production.Run(ctx, role, config); close(r.done) }()
	awaitReady(t, r)
	return r
}
func awaitReady(t *testing.T, r *runningRole) {
	t.Helper()
	deadline := time.NewTimer(8 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(30 * time.Millisecond)
	defer ticker.Stop()
	for {
		response, err := r.client.Get("http://" + r.address + "/readyz")
		if err == nil {
			status := response.StatusCode
			response.Body.Close()
			if status == 200 {
				return
			}
		}
		select {
		case <-r.done:
			t.Fatalf("actual role exited before readiness (%T; private output withheld)", r.err)
		case <-deadline.C:
			t.Fatal("actual role readiness bound exceeded")
		case <-ticker.C:
		}
	}
}
func stopRole(t *testing.T, r *runningRole) {
	t.Helper()
	r.cancel()
	select {
	case <-r.done:
		check(t, r.err == nil || errors.Is(r.err, context.Canceled), "actual role shutdown failed")
	case <-time.After(6 * time.Second):
		t.Error("actual role did not stop in bound")
	}
}
func health(t *testing.T, r *runningRole, path string, status int) map[string]string {
	t.Helper()
	resp, err := r.client.Get("http://" + r.address + path)
	must(t, "read actual role health", err)
	defer resp.Body.Close()
	check(t, resp.StatusCode == status, "unexpected actual role HTTP surface")
	value := map[string]string{}
	if status == 200 {
		must(t, "decode health metadata", json.NewDecoder(io.LimitReader(resp.Body, 16384)).Decode(&value))
	}
	return value
}
func checkCollectionHealth(t *testing.T, r *runningRole) {
	t.Helper()
	a := health(t, r, "/healthz", 200)
	check(t, a["service"] == "collection" && a["status"] == "alive", "health misidentifies the collection role")
	b := health(t, r, "/readyz", 200)
	check(t, b["service"] == "collection" && b["status"] == "ready" && b["database"] == "reachable" && b["storage"] == "configured-not-probed", "readiness must not certify storage/provider authority from DB Ping")
	health(t, r, "/api/session", 404)
	health(t, r, "/api/v1/session", 404)
	health(t, r, "/", 404)
}

type coreAPI struct {
	f             *fixture
	role          *runningRole
	server        *httptest.Server
	client        *http.Client
	admin, writer actor
	calls         int
}

func (f *fixture) coreService() *coreAPI {
	t := f.t
	address := ownedAddress(t)
	target, err := url.Parse("http://" + address)
	must(t, "parse exact local core target", err)
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorLog = log.New(io.Discard, "", 0)
	proxy.Transport = ownedClient(t, address).Transport
	server := httptest.NewTLSServer(proxy)
	t.Cleanup(server.Close)
	client := server.Client()
	client.Timeout = 2 * time.Second
	assets := filepath.Join(required(t, "ASPM_COLLECTION_RUNTIME_ARTIFACT_DIR"), "empty-ui-"+nonce(t))
	must(t, "create owned API-only UI directory", os.MkdirAll(assets, 0700))
	t.Cleanup(func() { _ = os.Remove(assets) })
	setBaseEnvironment(t, f.database)
	setRawEnvironment(t, f.raw)
	setCollectionEnvironment(t, &app.StorageConfig{Endpoint: f.readTap.server.URL, Bucket: f.read.Bucket, Prefix: f.read.Prefix, Region: f.read.Region, AccessKey: f.read.AccessKey, SecretKey: f.read.SecretKey})
	t.Setenv("ASPM_LISTEN", address)
	t.Setenv("ASPM_ASSETS", assets)
	t.Setenv("ASPM_PUBLIC_ORIGIN", server.URL)
	t.Setenv("ASPM_BOOTSTRAP_TOKEN", f.bootstrap)
	t.Setenv("ASPM_INTEGRATION_ENCRYPTION_KEY", base64.StdEncoding.EncodeToString(f.key))
	config, err := Production.Environment("core")
	must(t, "parse actual core environment with independent RO collection identity", err)
	check(t, config.CollectionStorage != nil && config.CollectionStorage.AccessKey == f.read.AccessKey && config.CollectionStorage.Prefix == f.read.Prefix, "core environment did not select its explicit RO capability")
	c := &coreAPI{f: f, server: server, client: client, role: startRole(t, f.ctx, "core", config)}
	password := secret(t)
	f.remember(password)
	created := c.json(actor{}, "POST", "/api/v1/bootstrap", map[string]any{"workspaceName": "Owned collection runtime", "name": "Synthetic admin", "email": "admin@collection-runtime.invalid", "password": password}, 201, "X-ASPM-Bootstrap-Token", f.bootstrap)
	c.admin = c.login("admin@collection-runtime.invalid", password, created.Workspace.ID)
	pwd := secret(t)
	f.remember(pwd)
	c.json(c.admin, "POST", "/api/v1/users", map[string]any{"name": "Synthetic analyst", "email": "writer@collection-runtime.invalid", "password": pwd, "role": "analyst"}, 201)
	c.writer = c.login("writer@collection-runtime.invalid", pwd, c.admin.Workspace)
	return c
}
func (c *coreAPI) request(who actor, method, path string, body any, status int, headers ...string) ([]byte, []*http.Cookie) {
	c.f.t.Helper()
	c.calls++
	check(c.f.t, c.calls <= 200, "bounded runtime API budget exceeded")
	request, err := http.NewRequestWithContext(c.f.ctx, method, c.server.URL+path, bytes.NewReader(encode(c.f.t, body)))
	must(c.f.t, "create real core HTTP request", err)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", c.server.URL)
	if who.Workspace != "" {
		request.Header.Set("X-ASPM-Workspace-ID", who.Workspace)
	}
	if who.Cookie != nil {
		request.AddCookie(who.Cookie)
	}
	for i := 0; i < len(headers); i += 2 {
		request.Header.Set(headers[i], headers[i+1])
	}
	response, err := c.client.Do(request)
	must(c.f.t, "call actual core service", err)
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 65537))
	must(c.f.t, "read bounded core response", err)
	check(c.f.t, len(data) <= 65536, "core response bound exceeded")
	if response.StatusCode != status {
		c.f.t.Fatalf("actual core %s %s status %d, want %d; body withheld", method, path, response.StatusCode, status)
	}
	c.f.noSecrets(data)
	return data, response.Cookies()
}
func (c *coreAPI) json(who actor, method, path string, body any, status int, headers ...string) reply {
	data, _ := c.request(who, method, path, body, status, headers...)
	var result reply
	must(c.f.t, "decode real core DTO", json.Unmarshal(data, &result))
	check(c.f.t, result.APIVersion == "aspm/v1alpha1", "API version changed")
	return result
}
func (c *coreAPI) login(email, password, workspace string) actor {
	data, cookies := c.request(actor{}, "POST", "/api/v1/login", map[string]any{"email": email, "password": password}, 200)
	var result reply
	must(c.f.t, "decode actual login", json.Unmarshal(data, &result))
	for _, cookie := range cookies {
		if cookie.Name == "aspm_session" {
			check(c.f.t, cookie.Secure && cookie.HttpOnly, "actual protected session required")
			c.f.remember(cookie.Value)
			return actor{result.User.ID, workspace, cookie}
		}
	}
	c.f.t.Fatal("real login omitted protected cookie")
	return actor{}
}
func (c *coreAPI) source(token string) string {
	c.f.remember(token)
	return c.json(c.admin, "POST", "/api/v1/sources", map[string]any{"profile": "github-cloud-app", "name": "Owned runtime source", "repository": "owned-owner/repository", "token": token, "enabled": true}, 201).Source.ID
}
func (c *coreAPI) enqueue(source, key string) string {
	value := c.json(c.writer, "POST", "/api/v1/sources/"+source+"/collections", map[string]string{"idempotencyKey": key}, 202)
	check(c.f.t, value.Collection.State == "queued", "core dispatched rather than queued")
	return value.Collection.ID
}
func (c *coreAPI) collection(id string) reply {
	return c.json(c.writer, "GET", "/api/v1/sources/collections/"+id, nil, 200)
}
func (c *coreAPI) await(id, state string) reply {
	c.f.t.Helper()
	timer := time.NewTimer(6 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(40 * time.Millisecond)
	defer ticker.Stop()
	for reads := 0; reads < 100; reads++ {
		value := c.collection(id)
		if value.Collection.State == state {
			return value
		}
		select {
		case <-timer.C:
			c.f.t.Fatal("real collection state did not settle in bound")
		case <-ticker.C:
		}
	}
	c.f.t.Fatal("bounded collection observation count exceeded")
	return reply{}
}

type githubFixture struct {
	server             *httptest.Server
	client             *http.Client
	count              atomic.Int32
	hold               atomic.Bool
	arrived, cancelled chan struct{}
	repo, alert        []byte
}

func github(t *testing.T, token string, held bool) *githubFixture {
	t.Helper()
	g := &githubFixture{arrived: make(chan struct{}, 3), cancelled: make(chan struct{}, 3), repo: []byte(" { \"id\":420001, \"full_name\":\"owned-owner/repository\", \"description\":\"RAW_RUNTIME_REPO\" }\r\n"), alert: []byte(`{ "number":7, "state":"open", "updated_at":"2026-09-17T10:00:00Z", "rule":{"security_severity_level":"high"}, "most_recent_instance":{"location":{"path":"src/owned.go","start_line":4}}, "raw_marker":"RAW_RUNTIME_ALERT" }`)}
	g.hold.Store(held)
	g.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer "+token || r.Header.Get("Accept") != "application/vnd.github+json" || r.Header.Get("X-GitHub-Api-Version") != "2026-03-10" {
			t.Error("native runtime request changed selected protocol/credential")
			http.Error(w, "owned protocol rejected", 400)
			return
		}
		if g.count.Add(1) > 12 {
			t.Error("owned native request bound exceeded")
			http.Error(w, "owned bound", 429)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/repos/owned-owner/repository" && r.URL.RawQuery == "" {
			w.Write(g.repo)
			return
		}
		if r.URL.Path != "/repos/owned-owner/repository/code-scanning/alerts" || r.URL.Query().Get("page") != "1" || (r.URL.Query().Get("per_page") != "1" && r.URL.Query().Get("per_page") != "50") || len(r.URL.Query()) != 2 {
			t.Error("native request widened selected source/page")
			http.Error(w, "owned source rejected", 400)
			return
		}
		if g.hold.Load() {
			g.arrived <- struct{}{}
			select {
			case <-r.Context().Done():
				g.cancelled <- struct{}{}
				return
			case <-time.After(7 * time.Second):
				t.Error("held source was not canceled")
				return
			}
		}
		w.Write(append(append([]byte("[ "), g.alert...), []byte(" ]")...))
	}))
	address := g.server.Listener.Addr().String()
	transport := g.server.Client().Transport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig.MinVersion = tls.VersionTLS12
	transport.DialContext = func(ctx context.Context, network, target string) (net.Conn, error) {
		if target != address {
			t.Error("native collection dial left owned TLS endpoint")
			return nil, errors.New("owned native boundary")
		}
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, target)
	}
	g.client = &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("native redirect denied") }}
	t.Cleanup(func() { transport.CloseIdleConnections(); g.server.Close() })
	return g
}
func (f *fixture) collectionConfig(g *githubFixture) runtimeConfig {
	storage := f.publish
	storage.Endpoint = f.publishTap.server.URL
	db := f.database
	db.MaxConnections = 1
	db.ApplicationName = "collection-role-" + nonce(f.t)
	return runtimeConfig{Database: db, CollectionStorage: &storage, Key: f.key, Listen: ownedAddress(f.t), WorkerID: "collection-" + nonce(f.t), Lease: time.Second, Endpoint: g.server.URL, Client: g.client, Limits: connectors.Limits{Requests: 4, Pages: 2, PageSize: 1, Bytes: 32768}}
}
func wait(t *testing.T, ch <-chan struct{}, name string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(5 * time.Second):
		t.Fatal(name + " exceeded owned bound")
	}
}

func httptestStorageCounter(t *testing.T, count *atomic.Int32) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		count.Add(1)
		http.Error(w, "preflight must not reach storage", 500)
	}))
	t.Cleanup(server.Close)
	return server.URL
}
