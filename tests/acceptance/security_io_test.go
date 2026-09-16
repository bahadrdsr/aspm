//go:build integration

package acceptance

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

type securityGate struct {
	entered chan context.Context
	release chan struct{}
	once    sync.Once
}

func newSecurityGate() *securityGate {
	return &securityGate{entered: make(chan context.Context, 4), release: make(chan struct{})}
}
func (g *securityGate) open() { g.once.Do(func() { close(g.release) }) }
func (g *securityGate) block(ctx context.Context) error {
	select {
	case g.entered <- ctx:
	case <-ctx.Done():
		return ctx.Err()
	}
	select {
	case <-g.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

type securityStorage struct {
	server       *httptest.Server
	gate         *securityGate
	mu           sync.Mutex
	method       string
	remaining    int
	gets         atomic.Int64
	revoked      atomic.Bool
	lateBytes    atomic.Int64
	readFinished atomic.Bool
}

type securityBody struct {
	io.ReadCloser
	storage *securityStorage
}

func (b *securityBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	if b.storage.revoked.Load() {
		b.storage.lateBytes.Add(int64(n))
	}
	if errors.Is(err, io.EOF) {
		b.storage.readFinished.Store(true)
	}
	return n, err
}

func newSecurityStorage(t *testing.T, services *services) *securityStorage {
	t.Helper()
	target, err := url.Parse(services.cfg.Storage.Endpoint)
	ok(t, "parse owned S3 destination", err)
	p := &securityStorage{gate: newSecurityGate()}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Transport = transport
	proxy.ErrorLog = log.New(io.Discard, "", 0)
	proxy.ErrorHandler = func(w http.ResponseWriter, _ *http.Request, _ error) { w.WriteHeader(502) }
	proxy.ModifyResponse = func(response *http.Response) error {
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return nil
		}
		if response.Request.Method == "GET" {
			response.Body = &securityBody{ReadCloser: response.Body, storage: p}
		}
		p.mu.Lock()
		pause := response.Request.Method == p.method && p.remaining > 0
		if pause {
			p.remaining--
		}
		gate := p.gate
		p.mu.Unlock()
		if pause {
			if err := gate.block(response.Request.Context()); err != nil {
				_ = response.Body.Close()
				return err
			}
		}
		return nil
	}
	bucketPath := "/" + services.cfg.Storage.Bucket
	prefix := bucketPath + "/" + services.cfg.Storage.Prefix
	p.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !(r.Method == "HEAD" && r.URL.Path == bucketPath) && !strings.HasPrefix(r.URL.Path, prefix) {
			w.WriteHeader(403)
			return
		}
		if r.Method == "GET" {
			p.gets.Add(1)
		}
		proxy.ServeHTTP(w, r)
	}))
	t.Cleanup(func() {
		p.gate.open()
		p.server.CloseClientConnections()
		p.server.Close()
		transport.CloseIdleConnections()
	})
	services.cfg.Storage.Endpoint = p.server.URL
	return p
}

func (p *securityStorage) arm(method string, count int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.method, p.remaining = method, count
	p.gets.Store(0)
	p.lateBytes.Store(0)
	p.readFinished.Store(false)
}

type securityTraceRole struct{}
type securityTraceCount struct{}

type securityTracer struct {
	seen      atomic.Int64
	countGate *securityGate
	countOnce sync.Once
	beginGate *securityGate
	beginOnce sync.Once
	storage   *securityStorage
}

func (s *securityTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	s.seen.Add(1)
	role, _ := ctx.Value(securityTraceRole{}).(string)
	sql := strings.ToLower(strings.Join(strings.Fields(data.SQL), ""))
	if role == "work-list" && strings.Contains(sql, "count(") && s.countGate != nil {
		return context.WithValue(ctx, securityTraceCount{}, true)
	}
	if role == "worker-finalize" && strings.HasPrefix(sql, "begin") &&
		s.beginGate != nil && s.storage.readFinished.Load() {
		s.beginOnce.Do(func() { _ = s.beginGate.block(ctx) })
	}
	return ctx
}

func (s *securityTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	if data.Err == nil && ctx.Value(securityTraceCount{}) == true {
		s.countOnce.Do(func() { _ = s.countGate.block(ctx) })
	}
}

func securityHarness(t *testing.T, maxConnections int32, tracer *securityTracer) (*harness, *securityStorage) {
	t.Helper()
	requireApplication(t)
	h := &harness{t: t, services: ownedServices(t), password: secret(t)}
	h.clock.Store(time.Date(2026, 9, 15, 12, 0, 0, 0, time.UTC).UnixNano())
	h.services.cfg.Now = func() time.Time { return time.Unix(0, h.clock.Load()).UTC() }
	h.services.cfg.BootstrapToken, h.services.cfg.LogOutput = secret(t), io.Discard
	h.services.cfg.MaxConnections = maxConnections
	if tracer != nil {
		h.services.cfg.QueryTracer = tracer
	}
	storage := newSecurityStorage(t, h.services)
	if tracer != nil {
		tracer.storage = storage
	}
	h.open()
	t.Cleanup(func() {
		storage.gate.open()
		if tracer != nil {
			if tracer.countGate != nil {
				tracer.countGate.open()
			}
			if tracer.beginGate != nil {
				tracer.beginGate.open()
			}
		}
		ok(t, "close instrumented real application", h.app.Close())
	})
	h.enroll()
	if tracer != nil && tracer.seen.Load() == 0 {
		t.Fatal("BLOCKED: QueryTracer instrumentation not forwarded to the production pgx pool; no query results were substituted")
	}
	return h, storage
}

type securityHTTP struct {
	done   chan struct{}
	cancel context.CancelFunc
	result *httptest.ResponseRecorder
}

func securityRequest(h *harness, a actor, method, path string, body any, duration time.Duration, role string) *securityHTTP {
	h.t.Helper()
	ctx, cancel := context.WithTimeout(h.services.ctx, duration)
	if role != "" {
		ctx = context.WithValue(ctx, securityTraceRole{}, role)
	}
	var raw []byte
	if body != nil {
		raw = encode(h.t, body)
	}
	request := httptest.NewRequest(method, "https://aspm.test"+path, bytes.NewReader(raw)).WithContext(ctx)
	request.Header.Set("Origin", "https://aspm.test")
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-ASPM-Workspace-ID", a.workspace)
	if a.cookie != nil {
		request.AddCookie(a.cookie)
	}
	future := &securityHTTP{done: make(chan struct{}), cancel: cancel, result: httptest.NewRecorder()}
	go func() {
		defer close(future.done)
		h.app.Handler.ServeHTTP(future.result, request)
	}()
	h.t.Cleanup(func() {
		cancel()
		select {
		case <-future.done:
		case <-time.After(5 * time.Second):
			h.t.Error("real HTTP handler did not stop after context cancellation")
		}
	})
	return future
}

func (f *securityHTTP) wait(t *testing.T) *httptest.ResponseRecorder {
	t.Helper()
	select {
	case <-f.done:
		return f.result
	case <-time.After(10 * time.Second):
		t.Fatal("bounded real API request did not return")
		return nil
	}
}

type securityWorker struct {
	done   chan struct{}
	cancel context.CancelFunc
	err    error
}

func securityProcess(t *testing.T, ctx context.Context, application Application, role string) *securityWorker {
	t.Helper()
	if application.ProcessImports == nil {
		t.Fatal("production binding missing: ProcessImports")
	}
	ctx, cancel := context.WithTimeout(ctx, 25*time.Second)
	if role != "" {
		ctx = context.WithValue(ctx, securityTraceRole{}, role)
	}
	result := &securityWorker{done: make(chan struct{}), cancel: cancel}
	go func() {
		defer close(result.done)
		result.err = application.ProcessImports(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-result.done:
		case <-time.After(5 * time.Second):
			t.Error("real worker did not stop after context cancellation")
		}
	})
	return result
}

func (w *securityWorker) wait(t *testing.T) error {
	t.Helper()
	select {
	case <-w.done:
		return w.err
	case <-time.After(10 * time.Second):
		t.Fatal("bounded real worker completion did not arrive")
		return nil
	}
}

func securityEntered(t *testing.T, gate *securityGate, label string) context.Context {
	t.Helper()
	select {
	case ctx := <-gate.entered:
		return ctx
	case <-time.After(5 * time.Second):
		t.Fatalf("BLOCKED: %s interleaving barrier was not reached by real production I/O", label)
		return nil
	}
}
