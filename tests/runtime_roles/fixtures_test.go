//go:build integration

package runtime_roles

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

func must(t *testing.T, operation string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s failed (%T; sensitive details withheld)", operation, err)
	}
}

func required(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		t.Fatalf("required private fixture variable %s is missing", name)
	}
	return value
}

type storageCall struct {
	method, key string
	status      int
}
type storageTap struct {
	url   string
	mu    sync.Mutex
	calls []storageCall
}

func (s *storageTap) snapshot() []storageCall {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]storageCall(nil), s.calls...)
}

type roleFixture struct {
	ctx                                               context.Context
	database                                          DatabaseConfig
	rawPrefix, normalizedPrefix, deniedKey, bootstrap string
	endpoint, region, bucket                          string
	credentials                                       map[string]ScopedStorage
	taps                                              map[string]*storageTap
	admin                                             *s3.Client
	mu                                                sync.Mutex
	keys                                              map[string]bool
}

func s3Client(t *testing.T, store ScopedStorage) *s3.Client {
	t.Helper()
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	t.Cleanup(transport.CloseIdleConnections)
	return s3.New(s3.Options{Region: store.Region, BaseEndpoint: aws.String(store.Endpoint), UsePathStyle: true,
		Credentials: credentials.NewStaticCredentialsProvider(store.AccessKey, store.SecretKey, ""),
		HTTPClient: &http.Client{Transport: transport, Timeout: 4 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("fixture redirects denied") }},
		RetryMaxAttempts: 1, RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired})
}

func objectBytes(ctx context.Context, client *s3.Client, bucket, key string) ([]byte, error) {
	object, err := client.GetObject(ctx, &s3.GetObjectInput{Bucket: aws.String(bucket), Key: aws.String(key)})
	if err != nil {
		return nil, err
	}
	if object == nil || object.Body == nil {
		return nil, errors.New("missing object body")
	}
	body, readErr := io.ReadAll(io.LimitReader(object.Body, 65537))
	return body, errors.Join(readErr, object.Body.Close())
}

func (f *roleFixture) own(key string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.keys[key] = true
}

func (f *roleFixture) tap(t *testing.T, role string) *storageTap {
	t.Helper()
	target, err := url.Parse(f.endpoint)
	must(t, "parse selected security fixture", err)
	tap := &storageTap{}
	proxy := httputil.NewSingleHostReverseProxy(target)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	proxy.Transport, proxy.ErrorLog = transport, log.New(io.Discard, "", 0)
	t.Cleanup(transport.CloseIdleConnections)
	proxy.ModifyResponse = func(response *http.Response) error {
		key := strings.TrimPrefix(response.Request.URL.Path, "/"+f.bucket+"/")
		tap.mu.Lock()
		tap.calls = append(tap.calls, storageCall{response.Request.Method, key, response.StatusCode})
		tap.mu.Unlock()
		return nil
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, credential, found := strings.Cut(r.Header.Get("Authorization"), "Credential=")
		keyID, _, _ := strings.Cut(credential, "/")
		if !found || keyID != f.credentials[role].AccessKey {
			t.Error("actual role storage request used an unexpected signing identity; values withheld")
			http.Error(w, "test credential identity mismatch", 403)
			return
		}
		key := strings.TrimPrefix(r.URL.Path, "/"+f.bucket+"/")
		root := r.URL.Path == "/"+f.bucket || r.URL.Path == "/"+f.bucket+"/"
		if root && r.Method != "GET" && r.Method != "HEAD" {
			t.Error("role attempted a bucket mutation")
			http.Error(w, "bucket mutations are outside this gate", 403)
			return
		}
		if !root && !strings.HasPrefix(key, f.rawPrefix) && !strings.HasPrefix(key, f.normalizedPrefix) && key != f.deniedKey {
			t.Error("role attempted storage outside the test-owned paths")
			http.Error(w, "test-owned path boundary", 403)
			return
		}
		if r.Method == "PUT" && !root {
			f.own(key)
		}
		// Forward unchanged signed Host/path/body to the actual object store.
		proxy.ServeHTTP(w, r)
	}))
	tap.url = server.URL
	t.Cleanup(server.Close)
	return tap
}

func newFixture(t *testing.T) *roleFixture {
	t.Helper()
	nonce := make([]byte, 12)
	_, err := rand.Read(nonce)
	must(t, "generate owned namespace", err)
	id := hex.EncodeToString(nonce)
	ctx, cancel := context.WithTimeout(context.Background(), 55*time.Second)
	t.Cleanup(cancel)
	f := &roleFixture{ctx: ctx, rawPrefix: "raw/team-a/runtime-" + id + "/", normalizedPrefix: "normalized/team-a/runtime-" + id + "/",
		deniedKey: "approved/team-a/grant-1/runtime-" + id + "/denied.txt", bootstrap: "synthetic-bootstrap-" + id,
		endpoint: required(t, "ASPM_RUNTIME_ROLE_ENDPOINT"), region: required(t, "ASPM_RUNTIME_ROLE_REGION"),
		bucket: required(t, "ASPM_RUNTIME_ROLE_BUCKET"), credentials: map[string]ScopedStorage{}, taps: map[string]*storageTap{}, keys: map[string]bool{},
		database: DatabaseConfig{URL: required(t, "ASPM_RUNTIME_ROLE_DATABASE_URL"), Schema: "runtime_role_" + id, ApplicationName: "runtime-role-" + id, MaxConnections: 3}}
	if f.endpoint != "http://127.0.0.1:18335" || f.bucket != "aspm-isolation" {
		t.Fatal("only the selected owned security store is permitted")
	}
	database, err := url.Parse(f.database.URL)
	must(t, "parse explicit PostgreSQL URL", err)
	if database.User == nil || database.User.Username() == "" {
		t.Fatal("PostgreSQL fixture credentials must be explicit")
	}
	if password, ok := database.User.Password(); !ok || password == "" {
		t.Fatal("PostgreSQL fixture password must be explicit")
	}
	seen := map[string]bool{}
	for _, role := range []string{"admin", "core", "ingestion", "ai"} {
		prefix := "ASPM_RUNTIME_ROLE_" + strings.ToUpper(role)
		store := ScopedStorage{Endpoint: f.endpoint, Region: f.region, Bucket: f.bucket, Prefix: f.rawPrefix,
			ReadinessKey: f.rawPrefix + "readiness.txt", AccessKey: required(t, prefix+"_ACCESS_KEY"), SecretKey: required(t, prefix+"_SECRET_KEY")}
		if seen[store.AccessKey] {
			t.Fatal("role credential identities must be distinct; values withheld")
		}
		seen[store.AccessKey], f.credentials[role] = true, store
	}
	f.admin = s3Client(t, f.credentials["admin"])
	pc, err := pgxpool.ParseConfig(f.database.URL)
	must(t, "parse fixture pool", err)
	pc.MaxConns, pc.ConnConfig.ConnectTimeout = 2, 4*time.Second
	pc.ConnConfig.RuntimeParams["application_name"] = f.database.ApplicationName + "-fixture"
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	must(t, "open fixture pool", err)
	t.Cleanup(pool.Close)
	_, err = pool.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{f.database.Schema}.Sanitize())
	must(t, "create test-owned schema", err)
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 12*time.Second)
		defer stop()
		f.mu.Lock()
		keys := make([]string, 0, len(f.keys))
		for key := range f.keys {
			keys = append(keys, key)
		}
		f.mu.Unlock()
		for _, key := range keys {
			_, e := f.admin.DeleteObject(cleanup, &s3.DeleteObjectInput{Bucket: aws.String(f.bucket), Key: aws.String(key)})
			if e != nil {
				t.Errorf("admin owned-object cleanup failed (%T; details withheld)", e)
			}
		}
		if _, e := pool.Exec(cleanup, "DROP SCHEMA "+pgx.Identifier{f.database.Schema}.Sanitize()+" CASCADE"); e != nil {
			t.Errorf("owned-schema cleanup failed (%T; details withheld)", e)
		}
	})
	for _, key := range []string{f.rawPrefix + "readiness.txt", f.deniedKey} {
		f.own(key)
		data := []byte("nonsecret synthetic runtime-role readiness\n")
		_, err := f.admin.PutObject(ctx, &s3.PutObjectInput{Bucket: aws.String(f.bucket), Key: aws.String(key), Body: bytes.NewReader(data)})
		must(t, "admin preseed owned readiness/denial object", err)
		got, err := objectBytes(ctx, f.admin, f.bucket, key)
		must(t, "verify seeded object exists", err)
		if !bytes.Equal(got, data) {
			t.Fatal("admin seeded-byte verification failed")
		}
	}
	for _, role := range []string{"core", "ingestion"} {
		f.taps[role] = f.tap(t, role)
	}
	return f
}

func (f *roleFixture) store(role string) ScopedStorage {
	store := f.credentials[role]
	store.Endpoint = f.taps[role].url
	return store
}

func freeAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, "reserve owned loopback address", err)
	address := listener.Addr().String()
	must(t, "release address for main-role listener", listener.Close())
	return address
}

type runningRole struct {
	cancel context.CancelFunc
	done   chan struct{}
	err    error
}

func startRole(t *testing.T, parent context.Context, run func(context.Context) error, address string) *runningRole {
	t.Helper()
	ctx, cancel := context.WithCancel(parent)
	running := &runningRole{cancel: cancel, done: make(chan struct{})}
	t.Cleanup(func() {
		cancel()
		select {
		case <-running.done:
		case <-time.After(12 * time.Second):
			t.Error("main role did not stop within bounded cleanup")
		}
	})
	go func() { running.err = run(ctx); close(running.done) }()
	client := &http.Client{Transport: &http.Transport{DialContext: (&net.Dialer{Timeout: 200 * time.Millisecond}).DialContext}, Timeout: 200 * time.Millisecond}
	t.Cleanup(client.CloseIdleConnections)
	deadline := time.NewTimer(12 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-running.done:
			t.Fatalf("real main role exited before readiness (%T; details withheld)", running.err)
		case <-deadline.C:
			t.Fatal("main role did not become reachable within its startup bound")
		case <-ticker.C:
			response, err := client.Get("http://" + address + "/healthz")
			if err == nil {
				status := response.StatusCode
				must(t, "close health response", response.Body.Close())
				if status == 200 {
					return running
				}
			}
		}
	}
}

type coreAPI struct {
	server    *httptest.Server
	client    *http.Client
	role      *runningRole
	workspace string
}

func (f *roleFixture) core(t *testing.T) *coreAPI {
	t.Helper()
	address := freeAddress(t)
	target, err := url.Parse("http://" + address)
	must(t, "parse owned core address", err)
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorLog = log.New(io.Discard, "", 0)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	proxy.Transport = transport
	t.Cleanup(transport.CloseIdleConnections)
	server := httptest.NewTLSServer(proxy)
	t.Cleanup(server.Close)
	client := server.Client()
	client.Timeout = 5 * time.Second
	client.Jar, err = cookiejar.New(nil)
	must(t, "create isolated authenticated cookie jar", err)
	if Production.Core == nil {
		t.Fatal("runtime role binding missing: Core")
	}
	role := Production.Core(CoreConfig{Database: f.database, Raw: f.store("core"), Listen: address,
		Assets: t.TempDir(), PublicOrigin: server.URL, BootstrapToken: f.bootstrap})
	if role == nil {
		t.Fatal("Core factory returned nil")
	}
	api := &coreAPI{server: server, client: client, role: startRole(t, f.ctx, role.Run, address)}
	return api
}

func (a *coreAPI) json(t *testing.T, method, path string, body any, bootstrap string, want int, result any) {
	t.Helper()
	data, err := json.Marshal(body)
	must(t, "encode synthetic API request", err)
	request, err := http.NewRequestWithContext(t.Context(), method, a.server.URL+path, bytes.NewReader(data))
	must(t, "create real core request", err)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", a.server.URL)
	if a.workspace != "" {
		request.Header.Set("X-ASPM-Workspace-ID", a.workspace)
	}
	if bootstrap != "" {
		request.Header.Set("X-ASPM-Bootstrap-Token", bootstrap)
	}
	response, err := a.client.Do(request)
	must(t, "call authenticated core API", err)
	defer response.Body.Close()
	if response.StatusCode != want {
		t.Fatalf("%s %s: status=%d, want=%d (body withheld)", method, path, response.StatusCode, want)
	}
	if result != nil {
		must(t, "decode core API response", json.NewDecoder(io.LimitReader(response.Body, 65536)).Decode(result))
	}
}

func (f *roleFixture) enroll(t *testing.T, api *coreAPI) string {
	t.Helper()
	var created struct {
		Workspace struct {
			ID string `json:"id"`
		} `json:"workspace"`
	}
	api.json(t, "POST", "/api/v1/bootstrap", map[string]any{"workspaceName": "Synthetic runtime workspace", "name": "Synthetic runtime owner", "email": "runtime@synthetic.invalid", "password": "Synthetic-runtime-password-42"}, f.bootstrap, 201, &created)
	api.workspace = created.Workspace.ID
	api.json(t, "POST", "/api/v1/login", map[string]any{"email": "runtime@synthetic.invalid", "password": "Synthetic-runtime-password-42"}, "", 200, nil)
	var asset struct {
		Asset struct {
			ID string `json:"id"`
		} `json:"asset"`
	}
	api.json(t, "POST", "/api/v1/assets", map[string]any{"name": "synthetic-runtime-repository", "kind": "repository", "environment": "test", "criticality": "medium", "tags": []string{"synthetic"}, "ownerId": nil}, "", 201, &asset)
	if api.workspace == "" || asset.Asset.ID == "" {
		t.Fatal("real enrollment/asset creation returned no durable identity")
	}
	return asset.Asset.ID
}
