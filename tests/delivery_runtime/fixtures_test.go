//go:build integration

package delivery_runtime

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
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
	"github.com/bahadrdsr/aspm/internal/app"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

const apiVersion = "aspm/v1alpha1"
const browserOrigin = "https://delivery-runtime.synthetic.invalid"

func must(t *testing.T, label string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s failed (%T; private values withheld)", label, err)
	}
}

func require(t *testing.T, ok bool, label string) {
	t.Helper()
	if !ok {
		t.Fatal(label)
	}
}

func random(t *testing.T, size int) []byte {
	t.Helper()
	value := make([]byte, size)
	_, err := rand.Read(value)
	must(t, "generate private fixture input", err)
	return value
}
func id(t *testing.T) string { return hex.EncodeToString(random(t, 12)) }
func secret(t *testing.T) string {
	return "synthetic-delivery-runtime-" + base64.RawURLEncoding.EncodeToString(random(t, 24))
}
func encoded(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	must(t, "encode owned fixture input", err)
	return data
}
func env(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	require(t, value != "", "BLOCKED: required private fixture variable "+name+" is missing")
	return value
}
func loopbackURL(t *testing.T, value string, schemes ...string) *url.URL {
	t.Helper()
	u, err := url.Parse(value)
	must(t, "parse selected local fixture URL", err)
	allowed := false
	for _, scheme := range schemes {
		allowed = allowed || u.Scheme == scheme
	}
	require(t, allowed && u.Hostname() == "127.0.0.1" && u.Port() != "", "only explicitly owned loopback fixtures are permitted")
	return u
}

type lockedLog struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedLog) Write(b []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(b)
}
func (l *lockedLog) data() []byte {
	l.mu.Lock()
	defer l.mu.Unlock()
	return bytes.Clone(l.b.Bytes())
}

type actor struct {
	id, workspace string
	cookie        *http.Cookie
}
type reply struct {
	APIVersion string
	User       struct{ ID string }
	Workspace  struct{ ID string }
	Asset      struct{ ID string }
	Import     struct{ ID, State string }
	Connection struct{ ID string }
	Delivery   struct {
		ID, State, RequestedBy string
		Receipt                *struct{ RemoteID string }
	}
	Items []struct{ ID string }
}
type fixture struct {
	t       *testing.T
	ctx     context.Context
	db      *pgxpool.Pool
	core    *app.Application
	config  app.Config
	admin   actor
	writer  actor
	store   *s3.Client
	log     lockedLog
	secrets []string
}

func (f *fixture) remember(value string) {
	if value != "" {
		f.secrets = append(f.secrets, value, url.QueryEscape(value), base64.StdEncoding.EncodeToString([]byte(value)))
	}
}
func (f *fixture) noSecrets(data []byte) {
	f.t.Helper()
	for _, value := range f.secrets {
		require(f.t, !bytes.Contains(data, []byte(value)), "private material leaked in runtime output")
	}
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	f := &fixture{t: t, ctx: ctx}
	nonce := id(t)
	f.config = app.Config{
		DatabaseURL: env(t, "ASPM_DELIVERY_TEST_DATABASE_URL"), Schema: "delivery_runtime_" + nonce,
		ApplicationName: "delivery-runtime-" + nonce, MaxConnections: 2,
		Storage: app.StorageConfig{
			Endpoint: env(t, "ASPM_DELIVERY_TEST_S3_ENDPOINT"), Bucket: env(t, "ASPM_DELIVERY_TEST_S3_BUCKET"),
			AccessKey: env(t, "ASPM_DELIVERY_TEST_S3_ACCESS_KEY"), SecretKey: env(t, "ASPM_DELIVERY_TEST_S3_SECRET_KEY"),
			Prefix: "delivery-runtime/" + nonce + "/", Region: "us-east-1",
		},
		IntegrationEncryptionKey: random(t, 32), BootstrapToken: secret(t), PublicOrigin: browserOrigin,
		ManualProcessing: true, LogOutput: &f.log,
	}
	database := loopbackURL(t, f.config.DatabaseURL, "postgres", "postgresql")
	require(t, database.User != nil, "explicit local DB identity required")
	password, present := database.User.Password()
	require(t, present && password != "", "explicit local DB password required")
	endpoint := loopbackURL(t, f.config.Storage.Endpoint, "http", "https")
	require(t, endpoint.User == nil && endpoint.RawQuery == "" && !strings.ContainsAny(f.config.Storage.Bucket, "/\\:"),
		"invalid owned local S3 fixture")
	for _, value := range []string{f.config.DatabaseURL, password, f.config.Storage.AccessKey, f.config.Storage.SecretKey,
		f.config.BootstrapToken, string(f.config.IntegrationEncryptionKey), hex.EncodeToString(f.config.IntegrationEncryptionKey),
		base64.StdEncoding.EncodeToString(f.config.IntegrationEncryptionKey)} {
		f.remember(value)
	}
	originalTransport, originalLogger := http.DefaultTransport, slog.Default()
	guard := originalTransport.(*http.Transport).Clone()
	guard.Proxy = nil
	guard.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != endpoint.Host {
			t.Error("ambient HTTP destination blocked before network I/O")
			return nil, errors.New("ambient HTTP denied")
		}
		return (&net.Dialer{Timeout: 2 * time.Second}).DialContext(ctx, network, address)
	}
	http.DefaultTransport = guard
	slog.SetDefault(slog.New(slog.NewTextHandler(&f.log, nil)))
	t.Cleanup(func() {
		http.DefaultTransport = originalTransport
		slog.SetDefault(originalLogger)
		guard.CloseIdleConnections()
		f.noSecrets(f.log.data())
	})
	pool, err := pgxpool.ParseConfig(f.config.DatabaseURL)
	must(t, "parse owned PG pool", err)
	pool.MaxConns, pool.MinConns = 2, 0
	pool.ConnConfig.RuntimeParams["application_name"] = f.config.ApplicationName + "-fixture"
	f.db, err = pgxpool.NewWithConfig(ctx, pool)
	must(t, "open actual owned PG fixture", err)
	t.Cleanup(f.db.Close)
	must(t, "authenticate PG fixture", f.db.Ping(ctx))
	_, err = f.db.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{f.config.Schema}.Sanitize())
	must(t, "create only the random owned schema", err)
	client := &http.Client{Transport: guard.Clone(), Timeout: 4 * time.Second}
	t.Cleanup(client.CloseIdleConnections)
	f.store = s3.New(s3.Options{Region: f.config.Storage.Region, BaseEndpoint: aws.String(f.config.Storage.Endpoint), UsePathStyle: true,
		Credentials: credentials.NewStaticCredentialsProvider(f.config.Storage.AccessKey, f.config.Storage.SecretKey, ""),
		HTTPClient:  client, RetryMaxAttempts: 1, RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired})
	t.Cleanup(f.cleanup)
	f.core, err = app.Open(ctx, f.config)
	must(t, "open accepted core application", err)
	t.Cleanup(func() { must(t, "close core before owned cleanup", f.core.Close()) })
	pwd := secret(t)
	f.remember(pwd)
	created := f.json(actor{}, "POST", "/api/v1/bootstrap", map[string]any{
		"workspaceName": "Owned delivery runtime", "name": "Synthetic runtime admin", "email": "admin@runtime.invalid", "password": pwd,
	}, 201, "X-ASPM-Bootstrap-Token", f.config.BootstrapToken)
	f.admin = f.login("admin@runtime.invalid", pwd, created.Workspace.ID)
	writerPassword := secret(t)
	f.remember(writerPassword)
	f.json(f.admin, "POST", "/api/v1/users", map[string]any{
		"name": "Synthetic runtime analyst", "email": "writer@runtime.invalid", "password": writerPassword, "role": "analyst",
	}, 201)
	f.writer = f.login("writer@runtime.invalid", writerPassword, f.admin.workspace)
	return f
}

func (f *fixture) cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	expected := "delivery-runtime/" + strings.TrimPrefix(f.config.Schema, "delivery_runtime_") + "/"
	if !strings.HasPrefix(f.config.Schema, "delivery_runtime_") || f.config.Storage.Prefix != expected {
		f.t.Error("refusing cleanup outside owned schema/prefix")
		return
	}
	pages := s3.NewListObjectsV2Paginator(f.store, &s3.ListObjectsV2Input{
		Bucket: aws.String(f.config.Storage.Bucket), Prefix: aws.String(expected), MaxKeys: aws.Int32(100),
	})
	for count := 0; pages.HasMorePages() && count < 4; count++ {
		page, err := pages.NextPage(ctx)
		if err != nil {
			f.t.Errorf("owned prefix cleanup failed (%T)", err)
			break
		}
		for _, item := range page.Contents {
			if strings.HasPrefix(aws.ToString(item.Key), expected) {
				_, err = f.store.DeleteObject(ctx, &s3.DeleteObjectInput{Bucket: aws.String(f.config.Storage.Bucket), Key: item.Key})
				if err != nil {
					f.t.Errorf("owned object cleanup failed (%T)", err)
				}
			}
		}
	}
	if pages.HasMorePages() {
		f.t.Error("owned cleanup exceeded its bound")
	}
	_, err := f.db.Exec(ctx, "DROP SCHEMA "+pgx.Identifier{f.config.Schema}.Sanitize()+" CASCADE")
	if err != nil {
		f.t.Errorf("owned schema cleanup failed (%T)", err)
	}
}

func (f *fixture) request(who actor, method, path string, body any, status int, headers ...string) *httptest.ResponseRecorder {
	f.t.Helper()
	request := httptest.NewRequest(method, browserOrigin+path, bytes.NewReader(encoded(f.t, body))).WithContext(f.ctx)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", browserOrigin)
	if who.workspace != "" {
		request.Header.Set("X-ASPM-Workspace-ID", who.workspace)
	}
	if who.cookie != nil {
		request.AddCookie(who.cookie)
	}
	for i := 0; i < len(headers); i += 2 {
		request.Header.Set(headers[i], headers[i+1])
	}
	response := httptest.NewRecorder()
	f.core.Handler.ServeHTTP(response, request)
	if response.Code != status {
		f.t.Fatalf("actual core %s %s returned %d, want %d; body withheld", method, path, response.Code, status)
	}
	f.noSecrets(response.Body.Bytes())
	return response
}
func (f *fixture) json(who actor, method, path string, body any, status int, headers ...string) reply {
	f.t.Helper()
	var value reply
	must(f.t, "decode actual core response", json.Unmarshal(f.request(who, method, path, body, status, headers...).Body.Bytes(), &value))
	require(f.t, value.APIVersion == apiVersion, "actual core API version mismatch")
	return value
}
func (f *fixture) login(email, password, workspace string) actor {
	response := f.request(actor{}, "POST", "/api/v1/login", map[string]any{"email": email, "password": password}, 200)
	var result reply
	must(f.t, "decode actual login", json.Unmarshal(response.Body.Bytes(), &result))
	for _, cookie := range response.Result().Cookies() {
		if cookie.Name == "aspm_session" {
			require(f.t, cookie.Secure && cookie.HttpOnly, "core did not establish a protected session")
			f.remember(cookie.Value)
			return actor{id: result.User.ID, workspace: workspace, cookie: cookie}
		}
	}
	f.t.Fatal("core login omitted its real session cookie")
	return actor{}
}

func (f *fixture) queued(token, key string) (findingID, connectionID, deliveryID string) {
	f.t.Helper()
	f.remember(token)
	asset := f.json(f.admin, "POST", "/api/v1/assets", map[string]any{
		"name": "Owned runtime source", "kind": "repository", "environment": "test", "criticality": "medium", "tags": []string{}, "ownerId": nil,
	}, 201)
	raw, err := os.ReadFile(filepath.Join("..", "acceptance", "testdata", "sarif.json"))
	must(f.t, "read unchanged canonical-intake fixture", err)
	imported := f.json(f.admin, "POST", "/api/v1/imports", map[string]any{
		"apiVersion": apiVersion, "assetId": asset.Asset.ID, "format": "sarif", "report": string(raw),
		"sourceId": "owned-runtime-source", "scanId": id(f.t),
		"scope":        map[string]string{"id": "runtime-scope", "revision": "1", "branch": "main"},
		"sourceScanAt": "2026-09-01T08:00:00Z", "collectedAt": "2026-09-02T08:00:00Z",
		"sourceStatus": "succeeded", "scanKind": "full", "completeness": "complete",
	}, 202)
	require(f.t, imported.Import.State == "queued", "real source intake must be queued first")
	must(f.t, "process real PG/local-S3 canonical intake", f.core.ProcessImports(f.ctx))
	work := f.json(f.writer, "GET", "/api/v1/work", nil, 200)
	require(f.t, len(work.Items) == 1, "real canonical finding did not appear")
	findingID = work.Items[0].ID
	connection := f.json(f.admin, "POST", "/api/v1/integrations/connections", map[string]any{
		"profile": "slack-workspace-bot", "name": "Owned runtime channel", "channel": "C123", "token": token, "enabled": true,
	}, 201)
	connectionID = connection.Connection.ID
	delivery := f.json(f.writer, "POST", "/api/v1/findings/"+findingID+"/deliveries", map[string]string{
		"connectionId": connectionID, "idempotencyKey": key,
	}, 202).Delivery
	require(f.t, delivery.State == "queued" && delivery.RequestedBy == f.writer.id, "actual actor-owned enqueue dispatched or lost authority")
	return findingID, connectionID, delivery.ID
}

func freeAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, "reserve owned loopback address", err)
	address := listener.Addr().String()
	must(t, "release address for actual role listener", listener.Close())
	return address
}
func exactClient(t *testing.T, address string) *http.Client {
	t.Helper()
	transport := &http.Transport{Proxy: nil, DialContext: func(ctx context.Context, network, target string) (net.Conn, error) {
		if target != address {
			t.Error("test HTTP client attempted an unapproved destination")
			return nil, errors.New("unapproved destination")
		}
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, target)
	}}
	t.Cleanup(transport.CloseIdleConnections)
	return &http.Client{Transport: transport, Timeout: 750 * time.Millisecond,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect denied") }}
}

type nativeFixture struct {
	t         *testing.T
	server    *httptest.Server
	client    *http.Client
	count     atomic.Int32
	entered   chan struct{}
	cancelled chan struct{}
	hold      atomic.Bool
}

func newNative(t *testing.T, token string, held bool) *nativeFixture {
	t.Helper()
	n := &nativeFixture{t: t, entered: make(chan struct{}, 4), cancelled: make(chan struct{}, 4)}
	n.hold.Store(held)
	n.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(io.LimitReader(r.Body, 32769))
		var payload struct{ Channel string }
		if err != nil || len(raw) > 32768 || json.Unmarshal(raw, &payload) != nil ||
			r.Method != "POST" || r.URL.Path != "/api/chat.postMessage" || r.URL.RawQuery != "" ||
			r.Header.Get("Authorization") != "Bearer "+token || payload.Channel != "C123" {
			t.Error("owned Slack protocol request escaped exact method/path/credential/channel boundary")
			http.Error(w, "invalid owned request", 400)
			return
		}
		if n.count.Add(1) > 3 {
			t.Error("owned native request budget exceeded")
			http.Error(w, "owned request budget exceeded", 429)
			return
		}
		select {
		case n.entered <- struct{}{}:
		default:
			t.Error("unexpected duplicate native activity")
			return
		}
		if n.hold.Load() {
			select {
			case <-r.Context().Done():
				n.cancelled <- struct{}{}
				return
			case <-time.After(8 * time.Second):
				t.Error("held native response was not canceled within the fixture bound")
				return
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "channel": "C123", "ts": "1789560000.123456"})
	}))
	address := n.server.Listener.Addr().String()
	transport := n.server.Client().Transport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig.MinVersion = tls.VersionTLS12
	transport.DialContext = func(ctx context.Context, network, target string) (net.Conn, error) {
		if target != address {
			t.Error("programmatic delivery transport attempted another address")
			return nil, errors.New("unapproved native address")
		}
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, target)
	}
	n.client = &http.Client{Transport: transport, Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("redirect denied") }}
	t.Cleanup(func() { transport.CloseIdleConnections(); n.server.Close() })
	return n
}
