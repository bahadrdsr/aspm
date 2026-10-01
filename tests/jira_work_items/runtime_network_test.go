//go:build integration && jira_runtime

package jira_work_items

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgproto3"
)

// The relay forwards original bytes only. PG decoding supplies counts, never replies.
type runtimeRelay struct {
	t                                    *testing.T
	label, address, upstream             string
	ctx                                  context.Context
	cancel                               context.CancelFunc
	listener                             net.Listener
	done                                 chan struct{}
	once                                 sync.Once
	mu                                   sync.Mutex
	conns                                map[net.Conn]bool
	wg                                   sync.WaitGroup
	fixture                              *fixture
	accepted, forwarded, queries, claims  atomic.Int32
	workerActive, workerPeak, workerOpens atomic.Int32
	schemaOpens                          atomic.Int32
	toUpstream, toCaller                  atomic.Int64
	bad                                  atomic.Bool
	changed                              chan struct{}
}

func newRuntimeRelay(t *testing.T, ctx context.Context, label, upstream string, f *fixture) *runtimeRelay {
	t.Helper()
	host, _, err := net.SplitHostPort(upstream)
	must(t, "parse owned relay destination", err)
	check(t, host == "127.0.0.1", "relay may forward only to an exact owned loopback listener")
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	must(t, "open owned byte observer", err)
	owned, cancel := context.WithCancel(ctx)
	r := &runtimeRelay{
		t: t, label: label, address: listener.Addr().String(), upstream: upstream,
		ctx: owned, cancel: cancel, listener: listener, done: make(chan struct{}),
		conns: make(map[net.Conn]bool), fixture: f, changed: make(chan struct{}, 1),
	}
	go r.accept()
	stopOnCancel := context.AfterFunc(ctx, r.close)
	t.Cleanup(func() {
		stopOnCancel()
		r.close()
		runtimeEvidence(t, label+"-network", r.snapshot())
		check(t, !r.bad.Load(), "owned forwarding observer encountered an unsupported protocol or exceeded its bound")
		check(t, r.accepted.Load() <= 64 && r.workerPeak.Load() <= 1, "owned socket/worker-pool cap exceeded")
	})
	return r
}

func (r *runtimeRelay) origin() string { return "https://" + r.address }

func (r *runtimeRelay) snapshot() object {
	return object{
		"label": r.label, "accepted": r.accepted.Load(), "forwarded": r.forwarded.Load(),
		"bytesToUpstream": r.toUpstream.Load(), "bytesToCaller": r.toCaller.Load(),
		"sqlExecutions": r.queries.Load(), "claimExecutions": r.claims.Load(),
		"workerPoolPeak": r.workerPeak.Load(), "workerConnections": r.workerOpens.Load(),
		"schemaConnections": r.schemaOpens.Load(), "observerFault": r.bad.Load(),
		"destination": "exact owned loopback only", "rawPacketsRecorded": false,
	}
}

func (r *runtimeRelay) accept() {
	defer close(r.done)
	for {
		conn, err := r.listener.Accept()
		if err != nil {
			return
		}
		if r.accepted.Add(1) > 64 {
			r.bad.Store(true)
			_ = conn.Close()
			continue
		}
		r.mu.Lock()
		r.conns[conn] = true
		r.mu.Unlock()
		r.wg.Add(1)
		go func() {
			defer r.wg.Done()
			defer func() {
				_ = conn.Close()
				r.mu.Lock()
				delete(r.conns, conn)
				r.mu.Unlock()
			}()
			upstream, err := (&net.Dialer{Timeout: time.Second}).DialContext(r.ctx, "tcp", r.upstream)
			if err != nil {
				if r.ctx.Err() == nil {
					r.bad.Store(true)
				}
				return
			}
			defer upstream.Close()
			r.forwarded.Add(1)
			unblock := context.AfterFunc(r.ctx, func() { _ = conn.Close(); _ = upstream.Close() })
			defer unblock()
			if deadline, ok := r.ctx.Deadline(); ok {
				_ = conn.SetDeadline(deadline)
				_ = upstream.SetDeadline(deadline)
			}
			back := make(chan struct{})
			go func() {
				defer close(back)
				n, _ := io.Copy(conn, upstream)
				r.toCaller.Add(n)
				_ = conn.Close()
			}()
			if r.fixture == nil {
				n, _ := io.Copy(upstream, conn)
				r.toUpstream.Add(n)
			} else {
				r.forwardPG(upstream, conn)
			}
			_ = upstream.Close()
			_ = conn.Close()
			<-back
		}()
	}
}

func (r *runtimeRelay) close() {
	r.once.Do(func() {
		r.cancel()
		_ = r.listener.Close()
		<-r.done
		r.mu.Lock()
		for conn := range r.conns {
			_ = conn.Close()
		}
		r.mu.Unlock()
		r.wg.Wait()
	})
}

func runtimePGFrame(src io.Reader, startup bool) (byte, []byte, []byte, error) {
	size := 5
	if startup {
		size = 4
	}
	header := make([]byte, size)
	if _, err := io.ReadFull(src, header); err != nil {
		return 0, nil, nil, err
	}
	offset, kind := 1, header[0]
	if startup {
		offset, kind = 0, 0
	}
	length := int64(binary.BigEndian.Uint32(header[offset:]))
	if length < 4 || length > 1<<20 || startup && length > 10000 {
		return 0, nil, nil, errRuntimePGFrame
	}
	body := make([]byte, int(length)-4)
	if _, err := io.ReadFull(src, body); err != nil {
		return 0, nil, nil, err
	}
	return kind, header, body, nil
}

var errRuntimePGFrame = errors.New("bounded PG observer cannot account for this frame")

func runtimeClaimSQL(query string) bool {
	return strings.HasPrefix(strings.TrimSpace(query), "SELECT ") &&
		strings.Contains(query, "app_finding_deliveries") &&
		strings.Contains(query, "state='queued'") && strings.Contains(query, "FOR UPDATE SKIP LOCKED")
}

func (r *runtimeRelay) forwardPG(dst io.Writer, src io.Reader) {
	_, header, body, err := runtimePGFrame(src, true)
	if err != nil {
		if errors.Is(err, errRuntimePGFrame) {
			r.bad.Store(true)
		}
		return
	}
	if len(body) < 4 {
		r.bad.Store(true)
		return
	}
	protocol := binary.BigEndian.Uint32(body)
	if protocol != 196608 && protocol != 196610 && protocol != 80877102 {
		// No SSL/GSS downgrade or synthetic negotiation is allowed.
		r.bad.Store(true)
		return
	}
	if protocol != 80877102 {
		var startup pgproto3.StartupMessage
		if startup.Decode(body) != nil {
			r.bad.Store(true)
			return
		}
		switch startup.Parameters["application_name"] {
		case "aspm-schema-migration":
			r.schemaOpens.Add(1)
		case "aspm-delivery":
			r.workerOpens.Add(1)
			active := r.workerActive.Add(1)
			defer r.workerActive.Add(-1)
			for peak := r.workerPeak.Load(); active > peak; peak = r.workerPeak.Load() {
				if r.workerPeak.CompareAndSwap(peak, active) {
					break
				}
			}
			if active > 1 {
				r.bad.Store(true)
				return
			}
		default:
			r.bad.Store(true)
			return
		}
	}
	send := func(header, body []byte) bool {
		n, err := dst.Write(append(header, body...))
		r.toUpstream.Add(int64(n))
		return err == nil && n == len(header)+len(body)
	}
	if !send(header, body) || protocol == 80877102 {
		return
	}
	statements, portals := make(map[string]bool), make(map[string]bool)
	for {
		kind, header, body, err := runtimePGFrame(src, false)
		if err != nil {
			if errors.Is(err, errRuntimePGFrame) {
				r.bad.Store(true)
			}
			return
		}
		count, claim := false, false
		switch kind {
		case 'Q':
			var message pgproto3.Query
			err = message.Decode(body)
			count, claim = true, runtimeClaimSQL(message.String)
		case 'P':
			var message pgproto3.Parse
			err = message.Decode(body)
			statements[message.Name] = runtimeClaimSQL(message.Query)
		case 'B':
			var message pgproto3.Bind
			err = message.Decode(body)
			selected, known := statements[message.PreparedStatement]
			if !known {
				err = errRuntimePGFrame
			}
			portals[message.DestinationPortal] = selected
		case 'E':
			var message pgproto3.Execute
			err = message.Decode(body)
			var known bool
			claim, known = portals[message.Portal]
			count = true
			if !known {
				err = errRuntimePGFrame
			}
		case 'F', 'd':
			// These command paths are not used by the accepted delivery worker.
			err = errRuntimePGFrame
		}
		if err != nil || len(statements) > 512 || len(portals) > 512 {
			r.bad.Store(true)
			return
		}
		if count {
			r.queries.Add(1)
			if r.fixture.queries.calls.Add(1) > 1400 {
				r.bad.Store(true)
				return
			}
		}
		if !send(header, body) {
			return
		}
		if claim {
			r.claims.Add(1)
			select {
			case r.changed <- struct{}{}:
			default:
			}
		}
	}
}

func runtimeDatabase(h *harness, label string) (string, *runtimeRelay) {
	h.t.Helper()
	u, err := url.Parse(h.cfg.DatabaseURL)
	must(h.t, "parse explicit owned database", err)
	check(h.t, u.Host == "127.0.0.1:15432" && u.RawQuery == "sslmode=disable" && u.Fragment == "",
		"BLOCKED: counted actual-main PG requires the explicitly plaintext loopback fixture, without a TLS downgrade")
	r := newRuntimeRelay(h.t, h.ctx, label+"-pg", u.Host, h.fixture)
	u.Host = r.address
	value := u.String()
	h.remember(value)
	return value, r
}

type runtimeRoutes struct{ jira, slack, trap *runtimeRelay }

func (r runtimeRoutes) close() {
	r.jira.close()
	r.slack.close()
	r.trap.close()
}

func runtimeObserveNative(t *testing.T, ctx context.Context, n *jiraServer, label string) runtimeRoutes {
	t.Helper()
	relay := func(schemeURL, kind string) *runtimeRelay {
		u, err := url.Parse(schemeURL)
		must(t, "parse existing native fixture endpoint", err)
		return newRuntimeRelay(t, ctx, label+"-"+kind, u.Host, nil)
	}
	r := runtimeRoutes{relay(n.server.URL, "jira"), relay(n.slack.URL, "slack"), relay(n.trap.URL, "trap")}
	original := n.trap.URL
	n.trap.URL = r.trap.origin()
	t.Cleanup(func() { n.trap.URL = original })
	return r
}

func (r runtimeRoutes) target(n *jiraServer) jiraTarget {
	target := n.target()
	target.APIBase = r.jira.origin() + "/ex/jira/" + cloudID
	return target
}

func runtimeAwaitClaims(t *testing.T, ctx context.Context, r *runtimeRelay, after int32, exited <-chan struct{}) {
	t.Helper()
	bound, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	for r.claims.Load() < after+3 {
		select {
		case <-r.changed:
		case <-exited:
			t.Fatal("actual delivery entrypoint exited before later claim-loop progress")
		case <-bound.Done():
			t.Fatal("actual delivery claim progress was not observed within its bound")
		}
	}
	check(t, !r.bad.Load(), "claim progress cannot hide unaccounted SQL")
}
