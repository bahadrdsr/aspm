//go:build integration

package readiness_bootstrap

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
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/bahadrdsr/aspm/internal/service"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type credential struct{ access, secret string }
type call struct {
	method, condition string
	status            int
}
type tap struct {
	url      string
	mu       sync.Mutex
	calls    []call
	holdPUT  bool
	failPUT  bool
	arrivals chan struct{}
	release  chan struct{}
	once     sync.Once
}

func (p *tap) open() { p.once.Do(func() { close(p.release) }) }
func (p *tap) snapshot() []call {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]call(nil), p.calls...)
}

type fixture struct {
	ctx                              context.Context
	database, schema, prefix, key    string
	endpoint, region, bucket, assets string
	roles                            map[string]credential
	admin                            *s3.Client
}

func must(t *testing.T, what string, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("%s failed (%T; credential-bearing details withheld)", what, err)
	}
}

func required(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	if strings.TrimSpace(value) == "" {
		t.Fatalf("BLOCKED: missing private fixture variable %s; never skipped", name)
	}
	return value
}

func status(err error) int {
	var response interface{ HTTPStatusCode() int }
	if errors.As(err, &response) {
		return response.HTTPStatusCode()
	}
	return 0
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	var nonce [12]byte
	_, err := rand.Read(nonce[:])
	must(t, "generate owned fixture name", err)
	id := hex.EncodeToString(nonce[:])
	ctx, cancel := context.WithTimeout(context.Background(), 65*time.Second)
	t.Cleanup(cancel)
	f := &fixture{
		ctx: ctx, database: required(t, "ASPM_READINESS_DATABASE_URL"),
		schema: "readiness_bootstrap_" + id, prefix: "raw/team-a/readiness-" + id + "/",
		endpoint: required(t, "ASPM_READINESS_ENDPOINT"), region: required(t, "ASPM_READINESS_REGION"),
		bucket: required(t, "ASPM_READINESS_BUCKET"), roles: map[string]credential{},
	}
	if f.endpoint != "http://127.0.0.1:18335" || f.bucket != "aspm-isolation" {
		t.Fatal("only the explicitly owned local security S3 fixture is permitted")
	}
	dbURL, err := url.Parse(f.database)
	must(t, "parse private database URL", err)
	if dbURL.User == nil || dbURL.User.Username() == "" {
		t.Fatal("explicit PostgreSQL fixture credentials are required")
	}
	if password, present := dbURL.User.Password(); !present || password == "" {
		t.Fatal("ambient PostgreSQL credential fallback is not permitted")
	}
	seen := map[string]bool{}
	for _, role := range []string{"admin", "core", "ingestion"} {
		prefix := "ASPM_READINESS_" + strings.ToUpper(role)
		c := credential{required(t, prefix+"_ACCESS_KEY"), required(t, prefix+"_SECRET_KEY")}
		for _, value := range []string{c.access, c.secret} {
			if seen[value] {
				t.Fatal("fixture role credential values must be distinct; values withheld")
			}
			seen[value] = true
		}
		f.roles[role] = c
	}
	f.key = f.prefix + "readiness.txt"
	dir := filepath.Join(".run", "case-"+id)
	must(t, "create test-only assets", os.MkdirAll(dir, 0700))
	must(t, "write synthetic static asset", os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!doctype html><title>Synthetic readiness fixture</title>"), 0600))
	f.assets, err = filepath.Abs(dir)
	must(t, "resolve owned assets directory", err)
	t.Cleanup(func() { must(t, "remove only owned asset directory", os.RemoveAll(dir)) })
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	t.Cleanup(transport.CloseIdleConnections)
	operator := f.roles["admin"]
	f.admin = s3.New(s3.Options{
		Region: f.region, BaseEndpoint: aws.String(f.endpoint), UsePathStyle: true,
		Credentials: credentials.NewStaticCredentialsProvider(operator.access, operator.secret, ""),
		HTTPClient: &http.Client{Transport: transport, Timeout: 4 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("fixture redirects forbidden") }},
		RetryMaxAttempts: 1, RequestChecksumCalculation: aws.RequestChecksumCalculationWhenRequired,
		ResponseChecksumValidation: aws.ResponseChecksumValidationWhenRequired,
	})
	pc, err := pgxpool.ParseConfig(f.database)
	must(t, "parse owned fixture pool", err)
	pc.MaxConns, pc.ConnConfig.ConnectTimeout = 1, 4*time.Second
	pc.ConnConfig.RuntimeParams["application_name"] = f.schema + "_fixture"
	pool, err := pgxpool.NewWithConfig(ctx, pc)
	must(t, "open real fixture PostgreSQL", err)
	t.Cleanup(pool.Close)
	_, err = pool.Exec(ctx, "CREATE SCHEMA "+pgx.Identifier{f.schema}.Sanitize())
	must(t, "create random owned schema", err)
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		_, removeErr := f.admin.DeleteObject(cleanup, &s3.DeleteObjectInput{Bucket: aws.String(f.bucket), Key: aws.String(f.key)})
		if removeErr != nil {
			t.Errorf("owned probe cleanup failed (%T; details withheld)", removeErr)
		}
		if _, dropErr := pool.Exec(cleanup, "DROP SCHEMA "+pgx.Identifier{f.schema}.Sanitize()+" CASCADE"); dropErr != nil {
			t.Errorf("owned schema cleanup failed (%T; details withheld)", dropErr)
		}
	})
	if _, err := f.read(); status(err) != 404 {
		t.Fatalf("new probe must be absent in the actual authorized store (status %d; details withheld)", status(err))
	}
	return f
}

func (f *fixture) read() ([]byte, error) {
	result, err := f.admin.GetObject(f.ctx, &s3.GetObjectInput{Bucket: aws.String(f.bucket), Key: aws.String(f.key)})
	if err != nil {
		return nil, err
	}
	data, readErr := io.ReadAll(io.LimitReader(result.Body, 4097))
	return data, errors.Join(readErr, result.Body.Close())
}

func (f *fixture) seed(t *testing.T, data []byte) {
	t.Helper()
	_, err := f.admin.PutObject(f.ctx, &s3.PutObjectInput{Bucket: aws.String(f.bucket), Key: aws.String(f.key), Body: bytes.NewReader(data)})
	must(t, "operator seeds only the selected owned sentinel", err)
}

func (f *fixture) observe(t *testing.T, identity string) *tap {
	t.Helper()
	p := &tap{arrivals: make(chan struct{}, 4), release: make(chan struct{})}
	target, err := url.Parse(f.endpoint)
	must(t, "parse owned upstream", err)
	proxy := httputil.NewSingleHostReverseProxy(target)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	proxy.Transport, proxy.ErrorLog = transport, log.New(io.Discard, "", 0)
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) { w.WriteHeader(502) }
	proxy.ModifyResponse = func(response *http.Response) error {
		p.mu.Lock()
		p.calls = append(p.calls, call{response.Request.Method, response.Request.Header.Get("If-None-Match"), response.StatusCode})
		p.mu.Unlock()
		return nil
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, tail, present := strings.Cut(r.Header.Get("Authorization"), "Credential=")
		keyID, _, _ := strings.Cut(tail, "/")
		if !present || keyID != f.roles[identity].access {
			t.Error("actual S3 request used an unexpected role signing identity; value withheld")
			w.WriteHeader(403)
			return
		}
		query := r.URL.Query()
		operation := map[string]string{"HEAD": "HeadObject", "GET": "GetObject", "PUT": "PutObject"}[r.Method]
		validQuery := len(query) == 0 || (len(query) == 1 && len(query["x-id"]) == 1 && query.Get("x-id") == operation)
		if r.URL.Path != "/"+f.bucket+"/"+f.key || !validQuery ||
			(r.Method != "HEAD" && r.Method != "GET" && r.Method != "PUT") {
			t.Error("runtime attempted broad or unselected storage I/O")
			w.WriteHeader(403)
			return
		}
		if r.Method == "PUT" {
			if r.Header.Get("If-None-Match") != "*" {
				t.Error("readiness preparation must use atomic If-None-Match: *")
				w.WriteHeader(400)
				return
			}
			p.mu.Lock()
			hold, fail := p.holdPUT, p.failPUT
			if fail {
				p.calls = append(p.calls, call{r.Method, r.Header.Get("If-None-Match"), 0})
			}
			p.mu.Unlock()
			if fail {
				// A failed transport is injected, never a fake storage success.
				conn, _, err := w.(http.Hijacker).Hijack()
				if err == nil {
					_ = conn.Close()
				} else {
					t.Error("owned transport-failure injection could not close its connection")
					w.WriteHeader(503)
				}
				return
			}
			if hold {
				select {
				case p.arrivals <- struct{}{}:
				case <-r.Context().Done():
					return
				}
				select {
				case <-p.release:
				case <-r.Context().Done():
					return
				}
			}
		}
		proxy.ServeHTTP(w, r)
	}))
	p.url = server.URL
	t.Cleanup(func() {
		p.open()
		server.CloseClientConnections()
		server.Close()
		transport.CloseIdleConnections()
		counts := map[string]int{}
		for _, observed := range p.snapshot() {
			label := observed.method + " " + http.StatusText(observed.status)
			if observed.status == 0 {
				label = observed.method + " injected-transport-failure"
			}
			counts[label]++
		}
		labels := make([]string, 0, len(counts))
		for label := range counts {
			labels = append(labels, label)
		}
		sort.Strings(labels)
		for _, label := range labels {
			t.Logf("%s signing identity: %s x%d (selected owned object only)", identity, label, counts[label])
		}
	})
	return p
}

func freeAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, "reserve own loopback address", err)
	address := listener.Addr().String()
	must(t, "release own address for service", listener.Close())
	return address
}

func (f *fixture) configure(t *testing.T, role, prepare string, proxy *tap) (service.Config, error) {
	t.Helper()
	values := map[string]string{
		"ASPM_DATABASE_URL": f.database, "ASPM_SCHEMA": f.schema, "ASPM_DB_MAX_CONNECTIONS": "3",
		"ASPM_LISTEN": freeAddress(t), "ASPM_ASSETS": f.assets, "ASPM_PUBLIC_ORIGIN": "",
		"ASPM_TLS_CERT_FILE": "", "ASPM_TLS_KEY_FILE": "", "ASPM_BOOTSTRAP_TOKEN": "",
		"ASPM_S3_PREPARE_READINESS": prepare, "ASPM_S3_ENDPOINT": "", "ASPM_S3_ACCESS_KEY": "",
		"ASPM_S3_SECRET_KEY": "", "ASPM_S3_BUCKET": "", "ASPM_S3_PREFIX": "",
		"ASPM_S3_READINESS_KEY": "", "ASPM_S3_NORMALIZED_PREFIX": "", "ASPM_S3_REGION": "",
		"ASPM_WORKSPACE": "", "AWS_ACCESS_KEY_ID": "", "AWS_SECRET_ACCESS_KEY": "",
		"AWS_SESSION_TOKEN": "", "AWS_PROFILE": "", "AWS_SHARED_CREDENTIALS_FILE": "",
		"AWS_CONFIG_FILE": "", "AWS_EC2_METADATA_DISABLED": "true",
	}
	if role != "reports" {
		c := f.roles[role]
		values["ASPM_S3_ENDPOINT"], values["ASPM_S3_ACCESS_KEY"], values["ASPM_S3_SECRET_KEY"] = proxy.url, c.access, c.secret
		values["ASPM_S3_BUCKET"], values["ASPM_S3_PREFIX"], values["ASPM_S3_REGION"] = f.bucket, f.prefix, f.region
		values["ASPM_S3_READINESS_KEY"] = f.key
		values["ASPM_S3_NORMALIZED_PREFIX"] = strings.Replace(f.prefix, "raw/", "normalized/", 1)
	}
	for name, value := range values {
		t.Setenv(name, value)
	}
	config, err := service.Environment(role)
	if err == nil {
		config.Jobs.ApplicationName = f.schema + "_" + role
		config.Evidence.Timeout = 3 * time.Second
	}
	return config, err
}

type running struct {
	cancel  context.CancelFunc
	done    chan struct{}
	err     error
	address string
}

func launch(t *testing.T, ctx context.Context, role string, config service.Config) *running {
	t.Helper()
	ctx, cancel := context.WithCancel(ctx)
	r := &running{cancel: cancel, done: make(chan struct{}), address: config.Listen}
	go func() {
		defer close(r.done)
		r.err = service.Run(ctx, role, config)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-r.done:
		case <-time.After(12 * time.Second):
			t.Error("real service did not stop within its cleanup bound")
		}
	})
	return r
}

func (r *running) ready(t *testing.T, role string) {
	t.Helper()
	client := &http.Client{Transport: &http.Transport{}, Timeout: 300 * time.Millisecond}
	defer client.CloseIdleConnections()
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(25 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-r.done:
			t.Fatalf("actual %s exited before readiness (%T; details withheld)", role, r.err)
		case <-deadline.C:
			t.Fatalf("actual %s never reached bounded readiness", role)
		case <-tick.C:
			response, err := client.Get("http://" + r.address + "/readyz")
			if err != nil {
				continue
			}
			var data map[string]string
			err = json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&data)
			closeErr := response.Body.Close()
			if response.StatusCode == 200 && err == nil && closeErr == nil && data["service"] == role &&
				data["database"] == "reachable" && data["status"] == "ready" {
				expectedStorage := "scoped-object-accessible"
				if role == "reports" {
					expectedStorage = "not-required"
				}
				if data["storage"] != expectedStorage {
					t.Fatal("readiness response did not report the required real dependency check")
				}
				return
			}
		}
	}
}

func (r *running) failed(t *testing.T) {
	t.Helper()
	select {
	case <-r.done:
		if r.err == nil {
			t.Error("failed preparation returned success")
		}
		client := &http.Client{Transport: &http.Transport{}, Timeout: 300 * time.Millisecond}
		defer client.CloseIdleConnections()
		response, err := client.Get("http://" + r.address + "/readyz")
		if err == nil {
			must(t, "close failed-start readiness check", response.Body.Close())
			if response.StatusCode == 200 {
				t.Error("failed preparation left a success-shaped readiness endpoint")
			}
		}
	case <-time.After(10 * time.Second):
		t.Error("failed preparation left the actual service running or stalled")
		r.cancel()
	}
}

func (r *running) stop(t *testing.T) {
	t.Helper()
	r.cancel()
	select {
	case <-r.done:
		if r.err != nil && !errors.Is(r.err, context.Canceled) {
			t.Fatalf("service shutdown failed (%T; details withheld)", r.err)
		}
	case <-time.After(12 * time.Second):
		t.Fatal("service shutdown exceeded its bound")
	}
}

func ioTripwires(t *testing.T, database string) (string, string, *atomic.Int32, *atomic.Int32) {
	t.Helper()
	var dbCalls, httpCalls atomic.Int32
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, "open owned database tripwire", err)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			dbCalls.Add(1)
			_ = conn.Close()
		}
	}()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		httpCalls.Add(1)
		w.WriteHeader(503)
	}))
	t.Cleanup(func() { server.Close(); _ = listener.Close(); <-done })
	u, err := url.Parse(database)
	must(t, "parse tripwire destination", err)
	u.Host = listener.Addr().String()
	return u.String(), server.URL, &dbCalls, &httpCalls
}
