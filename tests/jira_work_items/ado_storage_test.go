//go:build integration && ado_collection

package jira_work_items

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
)

type adoGate struct {
	arrived, release, cancelled chan struct{}
	once                       sync.Once
}

func newADOGate(t *testing.T) *adoGate {
	gate := &adoGate{arrived: make(chan struct{}), release: make(chan struct{}), cancelled: make(chan struct{})}
	t.Cleanup(gate.allow)
	return gate
}
func (g *adoGate) allow() { g.once.Do(func() { close(g.release) }) }
func (g *adoGate) wait(ctx context.Context) bool {
	close(g.arrived)
	select {
	case <-ctx.Done():
		close(g.cancelled)
		return false
	case <-g.release:
		return true
	}
}
func adoAwait(t *testing.T, signal <-chan struct{}, label string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(3 * time.Second):
		t.Fatal("held actual boundary did not reach " + label)
	}
}

type adoStorageTap struct {
	server                 *httptest.Server
	calls, puts, forbidden atomic.Int32
	bytes                  atomic.Int64
	mu                     sync.Mutex
	method                 string
	next                   *adoGate
}

func (s *adoStorageTap) hold(t *testing.T, method string) *adoGate {
	gate := newADOGate(t)
	s.mu.Lock()
	s.method, s.next = method, gate
	s.mu.Unlock()
	return gate
}

func (h *adoHarness) storageTap(prefix string, methods ...string) *adoStorageTap {
	tap := &adoStorageTap{}
	target, err := url.Parse(h.upstream.Endpoint)
	must(h.t, "parse existing real S3 destination", err)
	allowed := map[string]bool{}
	for _, method := range methods {
		allowed[method] = true
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.ErrorLog = log.New(io.Discard, "", 0)
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	proxy.Transport = transport
	proxy.ModifyResponse = func(response *http.Response) error {
		if response.StatusCode == 401 || response.StatusCode == 403 {
			tap.forbidden.Add(1)
		}
		return nil
	}
	h.t.Cleanup(transport.CloseIdleConnections)
	tap.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		count := tap.calls.Add(1)
		key := strings.TrimPrefix(r.URL.Path, "/"+h.upstream.Bucket+"/")
		q := r.URL.Query()
		operation := map[string]string{"GET": "GetObject", "HEAD": "HeadObject", "PUT": "PutObject"}[r.Method]
		queryOK := r.URL.RawQuery == "" || len(q) == 1 && len(q["x-id"]) == 1 && q.Get("x-id") == operation
		if count > 128 || !allowed[r.Method] || !strings.HasPrefix(key, prefix) ||
			key == prefix || !queryOK || r.Header.Get("Authorization") == "" {
			h.t.Error("production crossed an object-only S3 boundary; values withheld")
			http.Error(w, "owned object boundary", 403)
			return
		}
		if r.Method == "PUT" {
			tap.puts.Add(1)
			if r.ContentLength < 0 || r.ContentLength > 64<<10 || tap.bytes.Add(r.ContentLength) > 2<<20 {
				h.t.Error("production exceeded explicit publication byte bounds")
				http.Error(w, "owned publication bound", 413)
				return
			}
		}
		tap.mu.Lock()
		var gate *adoGate
		if tap.next != nil && tap.method == r.Method {
			gate, tap.next = tap.next, nil
		}
		tap.mu.Unlock()
		if gate != nil {
			if r.Method == "PUT" {
				body, err := io.ReadAll(io.LimitReader(r.Body, (64<<10)+1))
				if err != nil || len(body) > 64<<10 {
					h.t.Error("held signed S3 body could not be bounded")
					return
				}
				r.Body = io.NopCloser(bytes.NewReader(body))
			}
			if !gate.wait(r.Context()) {
				return
			}
		}
		proxy.ServeHTTP(w, r)
	}))
	h.t.Cleanup(tap.server.Close)
	return tap
}

func (h *adoHarness) installStorageDialGuard() {
	prior := http.DefaultTransport
	owned := prior.(*http.Transport).Clone()
	owned.Proxy = nil
	allowed := map[string]bool{}
	for _, value := range []string{h.reader.server.URL, h.publisher.server.URL, h.raw.server.URL} {
		u, err := url.Parse(value)
		must(h.t, "scope owned object forwarder", err)
		allowed[u.Host] = true
	}
	originalDial := owned.DialContext
	owned.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if allowed[address] {
			return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, address)
		}
		if originalDial == nil {
			return nil, errors.New("missing original selected fixture transport")
		}
		return originalDial(ctx, network, address)
	}
	http.DefaultTransport = owned
	h.t.Cleanup(func() {
		http.DefaultTransport = prior
		owned.CloseIdleConnections()
	})
}

func (h *adoHarness) adoCorrupt(record string) {
	h.t.Helper()
	ref := h.adoRef(record)
	check(h.t, ref.Bucket == h.upstream.Bucket && strings.HasPrefix(ref.Key, h.collection.Prefix),
		"integrity fault must stay in this fixture's actual published collection object")
	_, err := h.store.PutObject(h.ctx, &s3.PutObjectInput{Bucket: aws.String(ref.Bucket), Key: aws.String(ref.Key),
		Body: bytes.NewReader(bytes.Repeat([]byte("x"), int(ref.SizeBytes)))})
	must(h.t, "replace only owned S3 bytes, preserving the real PG digest", err)
}
