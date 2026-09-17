//go:build integration

package remediation

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
	"time"
)

type slackCall struct {
	Method, Path, AuthorizationHash string
	Body                            object
}

type slackPlan struct {
	token, channel, mode string
	marker               func() error
	arrived              chan slackCall
	release              chan struct{}
	cancelled            chan struct{}
	once                 sync.Once
}

func (p *slackPlan) allowReply() { p.once.Do(func() { close(p.release) }) }

type slackServer struct {
	t      *testing.T
	server *httptest.Server
	client *http.Client
	plans  chan *slackPlan
	mu     sync.Mutex
	calls  []slackCall
}

type ownedTransport struct {
	origin string
	next   http.RoundTripper
	t      *testing.T
}

func (g ownedTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.URL.Scheme+"://"+request.URL.Host != g.origin || request.URL.Path != "/api/chat.postMessage" ||
		request.URL.RawQuery != "" || request.Method != "POST" {
		g.t.Error("unapproved provider request blocked before network I/O")
		return nil, errors.New("test-owned Slack boundary denied request")
	}
	return g.next.RoundTrip(request)
}

func newSlack(t *testing.T) *slackServer {
	t.Helper()
	s := &slackServer{t: t, plans: make(chan *slackPlan, 12)}
	s.server = httptest.NewTLSServer(http.HandlerFunc(s.serve))
	parsed, err := url.Parse(s.server.URL)
	must(t, "parse task-owned TLS address", err)
	transport := s.server.Client().Transport.(*http.Transport).Clone()
	check(t, transport.TLSClientConfig != nil && !transport.TLSClientConfig.InsecureSkipVerify,
		"task provider client must validate its owned TLS certificate")
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if address != parsed.Host {
			t.Error("provider transport attempted another address")
			return nil, errors.New("unapproved provider dial")
		}
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, address)
	}
	s.client = &http.Client{Transport: ownedTransport{s.server.URL, transport, t}, Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("provider redirects denied") }}
	t.Cleanup(func() {
		for {
			select {
			case plan := <-s.plans:
				plan.allowReply()
			default:
				transport.CloseIdleConnections()
				s.server.Close()
				return
			}
		}
	})
	return s
}

func (s *slackServer) plan(token, channel, mode string, marker func() error) *slackPlan {
	p := &slackPlan{token: token, channel: channel, mode: mode, marker: marker,
		arrived: make(chan slackCall, 1), release: make(chan struct{}), cancelled: make(chan struct{})}
	if mode != "held" {
		p.allowReply()
	}
	s.plans <- p
	s.t.Cleanup(p.allowReply)
	return p
}

func (s *slackServer) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.calls)
}

func (s *slackServer) serve(w http.ResponseWriter, r *http.Request) {
	var plan *slackPlan
	select {
	case plan = <-s.plans:
	default:
		s.t.Error("unexpected native write: no explicitly authorized fixture reply remains")
		http.Error(w, "unexpected native write", 500)
		return
	}
	defer close(plan.cancelled)
	data, err := io.ReadAll(io.LimitReader(r.Body, 32769))
	var body object
	if err != nil || len(data) > 32768 || json.Unmarshal(data, &body) != nil ||
		r.Method != "POST" || r.URL.Path != "/api/chat.postMessage" || r.URL.RawQuery != "" ||
		r.Header.Get("Authorization") != "Bearer "+plan.token || body["channel"] != plan.channel ||
		r.Header.Get("Idempotency-Key") != "" {
		s.t.Error("native Slack request violated the selected channel/token/protocol contract; values withheld")
		http.Error(w, "invalid synthetic request", 400)
		return
	}
	if plan.marker != nil {
		if err = plan.marker(); err != nil {
			s.t.Error(err.Error())
			http.Error(w, "uncommitted dispatch marker", 500)
			return
		}
	}
	call := slackCall{Method: r.Method, Path: r.URL.Path, AuthorizationHash: fingerprint([]byte(r.Header.Get("Authorization"))), Body: body}
	s.mu.Lock()
	s.calls = append(s.calls, call)
	s.mu.Unlock()
	plan.arrived <- call
	select {
	case <-r.Context().Done():
		return
	case <-plan.release:
	}
	w.Header().Set("Content-Type", "application/json")
	switch plan.mode {
	case "drop":
		conn, _, hijackErr := w.(http.Hijacker).Hijack()
		if hijackErr != nil {
			s.t.Error("owned lost-ACK connection could not be closed")
			return
		}
		_ = conn.Close()
	case "auth":
		_ = json.NewEncoder(w).Encode(object{"ok": false, "error": "invalid_auth", "message": plan.token})
	case "channel":
		_ = json.NewEncoder(w).Encode(object{"ok": false, "error": "channel_not_found", "message": plan.token})
	case "429":
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(429)
		_ = json.NewEncoder(w).Encode(object{"error": "ratelimited", "message": plan.token})
	case "500":
		w.WriteHeader(503)
		_ = json.NewEncoder(w).Encode(object{"error": "service_unavailable", "message": plan.token})
	case "malformed":
		_, _ = io.WriteString(w, `{"ok":true,"channel":`)
	case "missing":
		_ = json.NewEncoder(w).Encode(object{"ok": true, "channel": plan.channel})
	case "redirect":
		w.Header().Set("Location", s.server.URL+"/not-approved?credential="+url.QueryEscape(plan.token))
		w.WriteHeader(307)
	default:
		_ = json.NewEncoder(w).Encode(object{"ok": true, "channel": plan.channel, "ts": "1789560000.123456"})
	}
}

func awaitCall(t *testing.T, plan *slackPlan) slackCall {
	t.Helper()
	select {
	case call := <-plan.arrived:
		return call
	case <-time.After(5 * time.Second):
		t.Fatal("owned native POST did not arrive within its bound")
		return slackCall{}
	}
}

func assertSlack(t *testing.T, call slackCall, channel string, value notification, reportText string) {
	t.Helper()
	body := call.Body
	check(t, len(body) == 6 && body["channel"] == channel && body["parse"] == "none" &&
		body["unfurl_links"] == false && body["unfurl_media"] == false,
		"native Slack channel/parse/unfurl fields differ from the existing adapter")
	check(t, body["text"] == value.Title+"\n"+value.Body+"\n"+value.DeepLink, "accessible Slack fallback text is not the immutable canonical notification")
	blocks, ok := body["blocks"].([]any)
	check(t, ok && len(blocks) == 2, "native Slack blocks are missing")
	first := decodeItem[struct {
		Type string
		Text struct{ Type, Text string }
	}](t, blocks[0])
	check(t, first.Type == "section" && first.Text.Type == "plain_text" &&
		first.Text.Text == value.Title+"\n"+value.Body, "canonical source text must remain native Slack plain_text")
	second := decodeItem[struct {
		Type     string
		Elements []struct {
			Type, URL string
			Text      struct{ Type, Text string }
		}
	}](t, blocks[1])
	check(t, second.Type == "actions" && len(second.Elements) == 1 &&
		second.Elements[0].Type == "button" && second.Elements[0].URL == value.DeepLink &&
		second.Elements[0].Text.Type == "plain_text", "finding link must be derived from the trusted app PublicOrigin")
	check(t, !bytesContain(encode(t, body), reportText, "scanner-not-approval", "untrusted.synthetic.invalid"),
		"native payload included raw evidence or untrusted scanner routing/authority")
}

func bytesContain(data []byte, values ...string) bool {
	for _, value := range values {
		if bytes.Contains(data, []byte(value)) {
			return true
		}
	}
	return false
}
