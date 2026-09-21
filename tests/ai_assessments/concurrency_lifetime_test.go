//go:build integration

package ai_assessments

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

type a2LocalLifetime struct {
	mu          sync.Mutex
	active, max int
	started     int
	closed      int
	live        map[string]bool
}
type a2ObservedConn struct {
	net.Conn
	owner *a2LocalLifetime
	once  sync.Once
}

func (c *a2ObservedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() {
		c.owner.mu.Lock()
		c.owner.active--
		c.owner.closed++
		delete(c.owner.live, c.LocalAddr().String())
		c.owner.mu.Unlock()
	})
	return err
}
func (l *a2LocalLifetime) isLive(address string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.live[address]
}
func (l *a2LocalLifetime) snapshot() (int, int, int, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.active, l.max, l.started, l.closed
}
func (l *a2LocalLifetime) client(t *testing.T, server *httptest.Server) *http.Client {
	t.Helper()
	roots := x509.NewCertPool()
	roots.AddCert(server.Certificate())
	tr := &http.Transport{Proxy: nil, DisableKeepAlives: true, ForceAttemptHTTP2: false,
		TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
		DialContext: func(ctx context.Context, network, target string) (net.Conn, error) {
			if target != server.Listener.Addr().String() {
				t.Error("A2 local I/O observer denied an unowned native destination")
				return nil, errors.New("unowned A2 native socket")
			}
			conn, err := (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, target)
			if err != nil {
				return nil, err
			}
			l.mu.Lock()
			if l.started >= 3 {
				l.mu.Unlock()
				_ = conn.Close()
				t.Error("A2 local native socket ceiling exceeded")
				return nil, errors.New("owned A2 socket bound")
			}
			if l.live == nil {
				l.live = map[string]bool{}
			}
			l.live[conn.LocalAddr().String()] = true
			l.active++
			l.started++
			if l.active > l.max {
				l.max = l.active
			}
			l.mu.Unlock()
			return &a2ObservedConn{Conn: conn, owner: l}, nil
		}}
	t.Cleanup(tr.CloseIdleConnections)
	return &http.Client{Transport: tr, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
}

type a2AuthorityGate struct {
	armed   atomic.Bool
	held    atomic.Bool
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (g *a2AuthorityGate) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	sql := strings.ToLower(strings.TrimSpace(data.SQL))
	if g.armed.Load() && strings.HasPrefix(sql, "select") && strings.Contains(sql, "app_workspaces") && strings.Contains(sql, "for share") {
		g.once.Do(func() {
			g.held.Store(true)
			defer g.held.Store(false)
			close(g.entered)
			select {
			case <-g.release:
			case <-ctx.Done():
			}
		})
	}
	return ctx
}
func (*a2AuthorityGate) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

type a2HeldNativeRequest struct {
	job                       assessment
	ready, cancelled, release chan struct{}
	allowOnce                 sync.Once
	posts                     atomic.Int32
	remote                    string
	ctx                       context.Context
}

func (r *a2HeldNativeRequest) allow() { r.allowOnce.Do(func() { close(r.release) }) }

type a2LifetimeServer struct {
	h      *harness
	server *httptest.Server
	key    string
	mu     sync.Mutex
	jobs   map[string]*a2HeldNativeRequest
	posts  atomic.Int32
}

func newA2LifetimeServer(h *harness, key string) *a2LifetimeServer {
	s := &a2LifetimeServer{h: h, key: key, jobs: map[string]*a2HeldNativeRequest{}}
	s.server = httptest.NewUnstartedServer(http.HandlerFunc(s.serve))
	s.server.Config.ReadHeaderTimeout = time.Second
	s.server.Config.ReadTimeout = 5 * time.Second
	s.server.Config.WriteTimeout = 6 * time.Second
	s.server.StartTLS()
	h.t.Cleanup(s.server.Close)
	return s
}
func (s *a2LifetimeServer) add(job assessment) *a2HeldNativeRequest {
	r := &a2HeldNativeRequest{job: job, ready: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{})}
	s.mu.Lock()
	s.jobs[job.ContextRef] = r
	s.mu.Unlock()
	s.h.t.Cleanup(r.allow)
	return r
}
func (s *a2LifetimeServer) verify(value bool, message string) bool {
	if !value {
		s.h.t.Error("A2 owned native boundary: " + message)
	}
	return value
}
func (s *a2LifetimeServer) serve(w http.ResponseWriter, request *http.Request) {
	if s.posts.Add(1) > 3 {
		s.h.t.Error("A2 bounded native request count exceeded")
		http.Error(w, "owned request bound", 503)
		return
	}
	data, err := io.ReadAll(io.LimitReader(request.Body, (128<<10)+1))
	_ = request.Body.Close()
	valid := s.verify(err == nil && len(data) <= 128<<10 && request.Method == "POST" && request.URL.Path == "/v1/responses" &&
		request.URL.RawQuery == "" && request.Header.Get("Authorization") == "Bearer "+s.key && request.TLS != nil &&
		request.Header.Get("Content-Type") == "application/json", "actual native protocol/body/auth mapping")
	s.h.noSecrets(data)
	var body map[string]any
	valid = s.verify(json.Unmarshal(data, &body) == nil, "actual native JSON invalid") && valid
	messages, ok := body["input"].([]any)
	if !ok || len(messages) != 2 {
		s.h.t.Error("A2 native messages missing")
		http.Error(w, "owned messages invalid", 400)
		return
	}
	content, _ := objectAt(messages[1], "content").(string)
	ref := strings.TrimPrefix(strings.SplitN(content, "\n", 2)[0], "Evidence identifier: ")
	s.mu.Lock()
	r := s.jobs[ref]
	s.mu.Unlock()
	if r == nil {
		s.h.t.Error("A2 native request was not a separately API-approved job")
		http.Error(w, "owned context invalid", 400)
		return
	}
	if !s.verify(r.posts.Add(1) == 1, "old/new job emitted duplicate inference") {
		http.Error(w, "owned duplicate request", 503)
		return
	}
	expected := "Evidence identifier: " + r.job.ContextRef + "\nEvidence SHA-256: " + r.job.ContextDigest +
		"\nBEGIN UNTRUSTED EVIDENCE\n" + r.job.Context + "\nEND UNTRUSTED EVIDENCE"
	valid = s.verify(content == expected && objectAt(messages[0], "content") == serverInstructions && body["model"] == r.job.Model &&
		objectAt(messages[0], "role") == "system" && objectAt(messages[1], "role") == "user" &&
		body["stream"] == false && body["store"] == false && body["tools"] == nil && body["tool_choice"] == nil &&
		body["max_output_tokens"] == float64(128), "exact approved context/trusted prompt/model/caps/privacy changed") && valid
	allowed := map[string]bool{"model": true, "stream": true, "store": true, "max_output_tokens": true, "input": true, "text": true}
	for field := range body {
		valid = s.verify(allowed[field], "unapproved native body field") && valid
	}
	var schema any
	_ = json.Unmarshal([]byte(assessmentSchema), &schema)
	valid = s.verify(reflect.DeepEqual(objectAt(body, "text", "format", "schema"), schema) &&
		objectAt(body, "text", "format", "strict") == true && objectAt(body, "text", "format", "type") == "json_schema",
		"structured output contract changed") && valid
	valid = s.verify(!bytes.Contains(data, []byte("SYNTHETIC-RAW-NOT-APPROVED")) && !bytes.Contains(data, []byte("SYNTHETIC-NOTE-NOT-APPROVED")),
		"unapproved raw/notes context leaked") && valid
	probe, cancel := context.WithTimeout(request.Context(), time.Second)
	var state string
	var attempts int
	var marker *time.Time
	err = s.h.db.QueryRow(probe, "SELECT state,attempts,dispatch_started_at FROM "+s.h.table("assessment_jobs")+" WHERE id=$1", r.job.ID).Scan(&state, &attempts, &marker)
	cancel()
	valid = s.verify(err == nil && state == "dispatching" && attempts == 1 && marker != nil && request.Context().Err() == nil,
		"actual committed dispatch marker or live native context missing") && valid
	if !valid {
		http.Error(w, "owned native boundary invalid", 400)
		return
	}
	r.remote, r.ctx = request.RemoteAddr, request.Context()
	close(r.ready)
	select {
	case <-request.Context().Done():
		close(r.cancelled)
		return
	case <-r.release:
	}
	answer := string(encoded(s.h.t, map[string]any{"conclusion": "inconclusive", "uncertainty": "Synthetic local native response; no remote-compute claim.", "evidenceRefs": []string{r.job.ContextRef}}))
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("x-request-id", "a2-local-lifetime-request")
	_, _ = w.Write(encoded(s.h.t, map[string]any{"model": "owned-lifetime-model", "status": "completed",
		"output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]string{"type": "output_text", "text": answer}}}},
		"usage":  map[string]int{"input_tokens": 11, "output_tokens": 7}}))
}

func TestAA10LocalNativeLifetimeRetainsSharedConcurrency(t *testing.T) {
	for _, retirement := range []string{"explicit-cancel", "expired-lease"} {
		t.Run(retirement, func(t *testing.T) {
			h := newHarness(t, false)
			finding := h.seed()
			key := secret(t)
			h.remember(key)
			server := newA2LifetimeServer(h, key)
			p := h.json(h.admin, "POST", profilesPath, map[string]any{"name": "Owned concurrency lifetime regression", "family": "openai",
				"endpoint": server.server.URL, "model": "operator-selected-model", "deployment": "", "enabled": true, "structuredOutput": true, "apiKey": key}, 201).Profile
			pol, grant := h.approve(p)
			v1 := h.preview(h.admin, finding, p, pol, grant, reviewedContext())
			first := h.enqueue(h.admin, v1, "lifetime-first", 202)
			v2 := h.preview(h.admin, finding, p, pol, grant, reviewedContext()+"\nSeparately reviewed second job.")
			second := h.enqueue(h.admin, v2, "lifetime-second", 202)
			firstNative, secondNative := server.add(first), server.add(second)
			before, storage := h.domainSnapshot(), h.storageCalls.Load()
			lifetime := &a2LocalLifetime{}
			gate := &a2AuthorityGate{entered: make(chan struct{}), release: make(chan struct{})}
			var release sync.Once
			unblock := func() { release.Do(func() { close(gate.release) }) }
			defer unblock()
			config := h.configForWorker("lifetime-owner-a")
			config.LeaseDuration = 250 * time.Millisecond
			config.RequestsPerWindow = 8
			config.Client = lifetime.client(t, server.server)
			config.Database.QueryTracer = gate
			a := h.worker(config)
			config.WorkerID, config.Database.QueryTracer = "lifetime-owner-b", nil
			config.Client = lifetime.client(t, server.server)
			b := h.worker(config)
			check(t, config.MaxConcurrent == 1 && config.RequestsPerWindow >= 2, "request budget must not hide a concurrency defect")
			doneA := make(chan error, 1)
			go func() { _, err := a.ProcessNext(h.ctx); doneA <- err }()
			event(t, h.ctx, firstNative.ready, "first actual native POST and committed marker")
			gate.armed.Store(true)
			event(t, h.ctx, gate.entered, "owner's real read-only authority SELECT paused before execution")
			active, maximum, started, closed := lifetime.snapshot()
			check(t, active == 1 && started == 1 && closed == 0 && lifetime.isLive(firstNative.remote) && gate.held.Load() &&
				firstNative.ctx.Err() == nil, "first local provider I/O lifetime is not demonstrably active")
			if retirement == "explicit-cancel" {
				cancelled := h.json(h.admin, "POST", jobsPath+"/"+first.ID+"/cancel", map[string]any{}, 200).Assessment
				check(t, cancelled.State == "cancelled" && cancelled.DispatchState == "possibly-sent" && cancelled.Attempts == 1,
					"explicit cancellation did not publish truthful terminal metadata")
			} else {
				expiry, stop := context.WithTimeout(h.ctx, time.Second)
				wait(t, expiry, "actual database lease expiry, no lease rewrite", func() bool {
					var expired bool
					must(t, "readonly actual lease-clock observation", h.db.QueryRow(expiry,
						"SELECT lease_until<=clock_timestamp() FROM "+h.table("assessment_jobs")+" WHERE id=$1", first.ID).Scan(&expired))
					return expired
				})
				stop()
				process(t, h.ctx, b, true)
				check(t, h.job(h.admin, first.ID).State == "uncertain", "fresh processor did not conservatively settle expired dispatch")
			}
			active, _, _, _ = lifetime.snapshot()
			check(t, active == 1 && lifetime.isLive(firstNative.remote) && firstNative.ctx.Err() == nil && gate.held.Load(),
				"local I/O already ended before the tested terminal-state/admission window")
			retired := h.job(h.admin, first.ID)
			var charges int
			must(t, "readonly shared request-window capacity", h.db.QueryRow(h.ctx,
				"SELECT count(*) FROM "+h.table("assessment_jobs")+" WHERE scope=$1 AND dispatch_started_at>clock_timestamp()-interval '1 minute'",
				h.scope).Scan(&charges))
			check(t, charges == 1 && charges < config.RequestsPerWindow, "request-window charge could mask the local concurrency defect")
			pending := h.job(h.admin, second.ID)
			check(t, pending.State == "queued" && pending.Attempts == 0, "second job was not genuinely new/unattempted")
			type outcome struct {
				worked bool
				err    error
			}
			doneB := make(chan outcome, 1)
			go func() { worked, err := b.ProcessNext(h.ctx); doneB <- outcome{worked, err} }()
			overlap := false
			bFinished := false
			var bResult outcome
			observation, stop := context.WithTimeout(h.ctx, time.Second)
			select {
			case <-secondNative.ready:
				overlap = true
			case bResult = <-doneB:
				bFinished = true
			case <-observation.Done():
				t.Error("second processor did not promptly decline unavailable concurrency or reach an actual native request")
			}
			stop()
			active, maximum, _, closed = lifetime.snapshot()
			var beforeDeadline bool
			must(t, "readonly database-clock conservative request deadline observation", h.db.QueryRow(h.ctx,
				"SELECT clock_timestamp()<dispatch_started_at+$2::bigint*interval '1 microsecond' FROM "+h.table("assessment_jobs")+" WHERE id=$1",
				first.ID, config.RequestTimeout.Microseconds()).Scan(&beforeDeadline))
			firstStillLive := lifetime.isLive(firstNative.remote) && firstNative.ctx.Err() == nil && gate.held.Load()
			t.Logf("A2 concurrency observation: scenario=%s MaxConcurrent=1 RequestsPerWindow=%d preAdmissionWindowCharges=%d publicOldState=%s actualNativePOSTs=%d liveLocalSockets=%d maximumLiveLocalSockets=%d closedSockets=%d secondPOSTBeforeFirstTermination=%t firstSocketAndContextLive=%t beforeConservativeRequestDeadline=%t workerPoolSlots=1 fixturePoolSlots=2",
				retirement, config.RequestsPerWindow, charges, retired.State, server.posts.Load(), active, maximum, closed, overlap, firstStillLive, beforeDeadline)
			check(t, firstStillLive && beforeDeadline, "BLOCKED: first local I/O/deadline no longer exposes the claimed admission window")
			if overlap {
				check(t, lifetime.isLive(secondNative.remote) && secondNative.ctx.Err() == nil && active == 2 && closed == 0,
					"second complete native POST did not actually overlap both live local transports")
			}
			if overlap || maximum > 1 {
				t.Error("shared concurrency was retired while first local native I/O remained active; a second actual POST/socket overlapped")
			} else {
				check(t, bFinished && bResult.err == nil && !bResult.worked && secondNative.posts.Load() == 0,
					"capacity-unavailable processing must not claim or dispatch the fresh second job")
			}
			unblock()
			termination, stop := context.WithTimeout(h.ctx, 2*time.Second)
			event(t, termination, firstNative.cancelled, "cooperative server observes first request cancellation")
			select {
			case err := <-doneA:
				check(t, err == nil || errors.Is(err, context.Canceled), "retired owner returned an unrelated error after real cancellation")
			case <-termination.Done():
				t.Fatal("first owner did not return after the actual authority read resumed")
			}
			wait(t, termination, "first local socket terminated", func() bool {
				_, _, _, n := lifetime.snapshot()
				return n >= 1 && !lifetime.isLive(firstNative.remote)
			})
			stop()
			secondNative.allow()
			if overlap {
				if !bFinished {
					select {
					case bResult = <-doneB:
					case <-h.ctx.Done():
						t.Fatal("second actual operation did not finish")
					}
				}
				must(t, "finish observed overlapping native request", bResult.err)
				t.Log("A2 expected post-termination admission branch NOT RUN: second job had already been incorrectly admitted; its real held request completed once after first Close.")
			} else {
				available, stop := context.WithTimeout(h.ctx, 5*time.Second)
				wait(t, available, "fresh job becomes processable after local termination or conservative deadline", func() bool {
					worked, err := b.ProcessNext(available)
					must(t, "process fresh job after reservation lifetime", err)
					return worked
				})
				stop()
				t.Log("A2 post-termination admission branch executed: still-unattempted second job processed once after termination/deadline.")
			}
			final := h.job(h.admin, second.ID)
			check(t, final.State == "succeeded" && final.Attempts == 1 && final.Result != nil &&
				reflect.DeepEqual(final.Result.EvidenceRefs, []string{v2.ContextRef}), "new explicit job was not processable after resource termination")
			old := h.job(h.admin, first.ID)
			check(t, old.Result == nil && old.Attempts == 1 && (old.State == "cancelled" || old.State == "uncertain") && sameAssessment(old, retired),
				"old owner resent/finalized/resurrected its retired job")
			check(t, server.posts.Load() == 2 && firstNative.posts.Load() == 1 && secondNative.posts.Load() == 1, "native attempts duplicated")
			process(t, h.ctx, b, false)
			check(t, server.posts.Load() == 2, "later scheduling resent a terminal job")
			wait(t, h.ctx, "both actual local native connections closed", func() bool {
				active, _, _, closed := lifetime.snapshot()
				return active == 0 && closed == 2
			})
			h.assertReadonly(before, storage)
			t.Log("A2 executed cleanup tail: first local transport ended; second explicit job completed once; old result absent; findings unchanged. No remote-compute termination claim.")
		})
	}
}
