//go:build integration

package jira_work_items

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bahadrdsr/aspm/internal/connectors"
)

type nativeCall struct {
	Method, Path, AuthorizationHash string
	Body                            object
}
type jiraPlan struct {
	token, metadataMode, createMode string
	target                          jiraTarget
	marker                          func() error
	gets, posts                     atomic.Int32
	getArrived, postArrived         chan nativeCall
	getRelease, postRelease         chan struct{}
	getCancelled, postCancelled     chan struct{}
	getOnce, postOnce               sync.Once
}

func (p *jiraPlan) releaseGET()  { p.getOnce.Do(func() { close(p.getRelease) }) }
func (p *jiraPlan) releasePOST() { p.postOnce.Do(func() { close(p.postRelease) }) }

type jiraServer struct {
	t                *testing.T
	server, slack    *httptest.Server
	trap             *httptest.Server
	client           *http.Client
	rootDER          []byte
	calls, forbidden atomic.Int32
	slackPosts       atomic.Int32
	mu               sync.Mutex
	current          *jiraPlan
	slackToken       string
	slackChannel     string
	slackArrival     chan nativeCall
}

func freshTLS(t *testing.T) (tls.Certificate, *x509.CertPool, []byte) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	must(t, "generate fresh in-memory CA", err)
	now := time.Now()
	ca := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "Owned Jira acceptance CA"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign,
	}
	rootDER, err := x509.CreateCertificate(rand.Reader, ca, ca, public, private)
	must(t, "sign fresh in-memory CA", err)
	root, err := x509.ParseCertificate(rootDER)
	must(t, "parse owned root", err)
	leafPublic, leafPrivate, err := ed25519.GenerateKey(rand.Reader)
	must(t, "generate fresh TLS server key", err)
	leaf := &x509.Certificate{
		SerialNumber: big.NewInt(2), Subject: pkix.Name{CommonName: "Owned Jira TLS fixture"},
		NotBefore: now.Add(-time.Minute), NotAfter: now.Add(time.Hour),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses: []net.IP{net.ParseIP("127.0.0.1")}, DNSNames: []string{"localhost"},
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leaf, root, leafPublic, private)
	must(t, "sign fresh TLS leaf", err)
	roots := x509.NewCertPool()
	roots.AddCert(root)
	return tls.Certificate{Certificate: [][]byte{leafDER, rootDER}, PrivateKey: leafPrivate}, roots, rootDER
}
func startTLS(t *testing.T, cert tls.Certificate, handler http.Handler) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(handler)
	server.TLS = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
	server.StartTLS()
	t.Cleanup(server.Close)
	return server
}
func newJira(t *testing.T) *jiraServer {
	t.Helper()
	cert, roots, rootDER := freshTLS(t)
	n := &jiraServer{t: t, rootDER: rootDER}
	n.server = startTLS(t, cert, http.HandlerFunc(n.serve))
	trap := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n.forbidden.Add(1)
		t.Error("Jira credential/request reached Slack or another owned origin")
		w.WriteHeader(http.StatusForbidden)
	})
	n.slack, n.trap = startTLS(t, cert, http.HandlerFunc(n.serveSlack)), startTLS(t, cert, trap)
	allowed := map[string]bool{
		strings.TrimPrefix(n.server.URL, "https://"): true,
		strings.TrimPrefix(n.slack.URL, "https://"):  true,
		strings.TrimPrefix(n.trap.URL, "https://"):   true,
	}
	transport := &http.Transport{
		Proxy: nil, TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
		ResponseHeaderTimeout: 2 * time.Second,
	}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		if !allowed[address] {
			n.forbidden.Add(1)
			return nil, errors.New("unapproved native destination")
		}
		return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, address)
	}
	n.client = &http.Client{Transport: transport, Timeout: 3 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	t.Cleanup(func() {
		n.mu.Lock()
		if n.current != nil {
			n.current.releaseGET()
			n.current.releasePOST()
		}
		n.mu.Unlock()
		transport.CloseIdleConnections()
		check(t, n.calls.Load() <= 64 && n.slackPosts.Load() <= 1 && n.forbidden.Load() == 0, "native I/O budget or destination isolation failed")
	})
	return n
}
func (n *jiraServer) allowSlack(token, channel string) <-chan nativeCall {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.slackToken, n.slackChannel, n.slackArrival = token, channel, make(chan nativeCall, 1)
	return n.slackArrival
}
func (n *jiraServer) serveSlack(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	token, channel, arrived := n.slackToken, n.slackChannel, n.slackArrival
	n.mu.Unlock()
	var body object
	data, err := io.ReadAll(io.LimitReader(r.Body, (32<<10)+1))
	if token == "" || r.Header.Get("Authorization") != "Bearer "+token ||
		r.Method != "POST" || r.URL.Path != "/api/chat.postMessage" || r.URL.RawQuery != "" ||
		r.Header.Get("Idempotency-Key") != "" || err != nil || len(data) > 32<<10 ||
		json.Unmarshal(data, &body) != nil || body["channel"] != channel || n.slackPosts.Add(1) != 1 {
		n.forbidden.Add(1)
		n.t.Error("Jira credential/request reached Slack or the historical Slack request changed")
		w.WriteHeader(403)
		return
	}
	arrived <- nativeCall{Method: r.Method, Path: r.URL.Path, Body: body}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(object{"ok": true, "channel": channel, "ts": "1789560000.123456"})
}
func (n *jiraServer) target() jiraTarget {
	return jiraTarget{
		CredentialType: "oauth2-bearer", CloudID: cloudID, APIBase: n.server.URL + "/ex/jira/" + cloudID,
		SiteOrigin: siteOrigin, Project: "SYN", IssueType: "10001",
		FieldMappings: map[string]string{"customfield_10010": "asset.name", "customfield_10011": "finding.severity"},
	}
}
func (n *jiraServer) arm(token, metadata, create string, marker func() error) *jiraPlan {
	p := &jiraPlan{
		token: token, metadataMode: metadata, createMode: create, target: n.target(), marker: marker,
		getArrived: make(chan nativeCall, 4), postArrived: make(chan nativeCall, 2),
		getRelease: make(chan struct{}), postRelease: make(chan struct{}),
		getCancelled: make(chan struct{}), postCancelled: make(chan struct{}),
	}
	if metadata != "hold" {
		p.releaseGET()
	}
	if create != "hold" {
		p.releasePOST()
	}
	n.mu.Lock()
	n.current = p
	n.mu.Unlock()
	n.t.Cleanup(func() { p.releaseGET(); p.releasePOST() })
	return p
}
func (n *jiraServer) serve(w http.ResponseWriter, r *http.Request) {
	if n.calls.Add(1) > 64 {
		n.t.Error("native request budget exceeded")
		http.Error(w, "bounded fixture", 429)
		return
	}
	n.mu.Lock()
	p := n.current
	n.mu.Unlock()
	if p == nil {
		n.t.Error("native I/O occurred without an explicit fixture plan")
		w.WriteHeader(500)
		return
	}
	if r.Header.Get("Authorization") != "Bearer "+p.token || r.Header.Get("Idempotency-Key") != "" {
		n.t.Error("native Jira credential type/identity or nonreplayable write boundary differs")
		w.WriteHeader(401)
		return
	}
	call := nativeCall{Method: r.Method, Path: r.URL.Path, AuthorizationHash: digest([]byte(r.Header.Get("Authorization")))}
	base := "/ex/jira/" + p.target.CloudID + "/rest/api/3/issue"
	w.Header().Set("Content-Type", "application/json")
	switch r.Method + " " + r.URL.Path {
	case "GET " + base + "/createmeta/" + p.target.Project + "/issuetypes/" + p.target.IssueType:
		count := p.gets.Add(1)
		maximum, err := strconv.Atoi(r.URL.Query().Get("maxResults"))
		start, startErr := strconv.Atoi(r.URL.Query().Get("startAt"))
		expectedStart := 0
		if count == 2 && p.metadataMode == "paged" {
			expectedStart = 2
		}
		if err != nil || startErr != nil || maximum < 1 || maximum > 50 || start != expectedStart ||
			len(r.URL.Query()) != 2 || count > 2 || count == 2 && p.metadataMode != "paged" {
			n.t.Error("native createmeta paging/request bound or exact scope differs")
			w.WriteHeader(400)
			return
		}
		if p.metadataMode == "hold" {
			w.WriteHeader(200)
			w.(http.Flusher).Flush()
		}
		p.getArrived <- call
		select {
		case <-r.Context().Done():
			close(p.getCancelled)
			return
		case <-p.getRelease:
		}
		switch p.metadataMode {
		case "403", "429":
			status, _ := strconv.Atoi(p.metadataMode)
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(object{"message": p.token})
			return
		case "redirect":
			w.Header().Set("Location", n.trap.URL+"/credential-trap")
			w.WriteHeader(307)
			return
		}
		fields := []object{
			{"fieldId": "project", "required": true}, {"fieldId": "issuetype", "required": true},
			{"fieldId": "summary", "required": true}, {"fieldId": "description", "required": true},
			{"fieldId": "customfield_10010", "required": true, "schema": object{"type": "string"}},
		}
		if p.metadataMode == "missing" {
			fields = append(fields, object{"fieldId": "customfield_10099", "required": true, "schema": object{"type": "string"}})
		}
		if p.metadataMode == "unsupported" {
			fields[4]["schema"] = object{"type": "array", "items": "option"}
		}
		if p.metadataMode == "paged" {
			fields = fields[2:5]
			if count == 1 {
				_ = json.NewEncoder(w).Encode(object{"startAt": 0, "total": 3, "isLast": false, "fields": fields[:2]})
			} else {
				_ = json.NewEncoder(w).Encode(object{"startAt": 2, "total": 3, "isLast": true, "values": fields[2:]})
			}
			return
		}
		_ = json.NewEncoder(w).Encode(object{"startAt": 0, "total": len(fields), "isLast": true, "fields": fields})
	case "POST " + base:
		if p.posts.Add(1) != 1 || r.URL.RawQuery != "" {
			n.t.Error("native create was replayed or rerouted")
			w.WriteHeader(500)
			return
		}
		data, err := io.ReadAll(io.LimitReader(r.Body, (64<<10)+1))
		if err != nil || len(data) > 64<<10 || json.Unmarshal(data, &call.Body) != nil {
			n.t.Error("native create is not bounded JSON")
			w.WriteHeader(400)
			return
		}
		if p.marker != nil && p.marker() != nil {
			n.t.Error("POST arrived without a committed fenced create-attempt marker")
			w.WriteHeader(500)
			return
		}
		if p.createMode == "hold" {
			w.WriteHeader(201)
			w.(http.Flusher).Flush()
		}
		p.postArrived <- call
		select {
		case <-r.Context().Done():
			close(p.postCancelled)
			return
		case <-p.postRelease:
		}
		switch p.createMode {
		case "drop":
			conn, _, err := w.(http.Hijacker).Hijack()
			if err != nil {
				n.t.Error("owned lost-ACK fixture could not close its connection")
				return
			}
			_ = conn.Close()
		case "403", "429", "500":
			status, _ := strconv.Atoi(p.createMode)
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(status)
			_ = json.NewEncoder(w).Encode(object{"message": p.token})
		case "400":
			w.WriteHeader(400)
			_ = json.NewEncoder(w).Encode(object{"errors": object{"customfield_10099": "Required " + p.token}})
		case "no-result":
			w.WriteHeader(201)
			_, _ = io.WriteString(w, `{}`)
		case "malformed":
			w.WriteHeader(201)
			_, _ = io.WriteString(w, `{"key":`)
		default:
			if p.createMode != "hold" {
				w.WriteHeader(201)
			}
			_ = json.NewEncoder(w).Encode(object{"id": "42", "key": "SYN-42", "self": n.trap.URL + "/not-a-browser-link"})
		}
	default:
		n.t.Error("native adapter used an undeclared profile/endpoint/tenant/route")
		w.WriteHeader(404)
	}
}
func awaitCall(t *testing.T, channel <-chan nativeCall) nativeCall {
	t.Helper()
	select {
	case value := <-channel:
		return value
	case <-time.After(2 * time.Second):
		t.Fatal("native receipt did not arrive within its bounded wait")
		return nativeCall{}
	}
}
func awaitCancel(t *testing.T, channel <-chan struct{}) {
	t.Helper()
	select {
	case <-channel:
	case <-time.After(time.Second):
		t.Fatal("held native request did not actively observe cancellation within one second")
	}
}
func assertADF(t *testing.T, call nativeCall, expected payload) {
	t.Helper()
	fields, ok := call.Body["fields"].(map[string]any)
	check(t, ok && len(call.Body) == 1 && len(fields) == 4+len(expected.Fields), "native payload is not exact declared Jira fields")
	same(t, "Jira project mismatch", fields["project"], object{"key": "SYN"})
	same(t, "Jira issue type mismatch", fields["issuetype"], object{"id": "10001"})
	check(t, fields["summary"] == expected.Title, "canonical summary changed")
	for field, value := range expected.Fields {
		check(t, fields[field] == value, "approved string-field mapping did not reach native create")
	}
	want := object{"type": "doc", "version": float64(1), "content": []any{
		object{"type": "paragraph", "content": []any{object{"type": "text", "text": expected.Body}}},
		object{"type": "paragraph", "content": []any{object{"type": "text", "text": "View finding",
			"marks": []any{object{"type": "link", "attrs": object{"href": expected.DeepLink}}}}}},
	}}
	same(t, "source text must remain inert ADF plus the trusted finding link", fields["description"], want)
	data := string(encoded(t, call.Body))
	for _, canary := range []string{rawCanary, noteCanary, "SOURCE-DESCRIPTION-NOT-APPROVED", "scanner-not-approval",
		"scanner-not-a-credential", "scanner-not-a-mapping", "not-approved.invalid"} {
		check(t, !strings.Contains(data, canary), "native Jira export exceeded explicit field contract")
	}
}

func TestJiraNativeTLSCalibration(t *testing.T) {
	n := newJira(t)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	config := connectors.DeliveryConfig{
		Profile: connectors.JiraCloudV3, Endpoint: n.target().APIBase, Token: secret(t),
		WorkspaceID: "owned-calibration", Project: "SYN", IssueType: "10001", Client: n.client,
		Limits: connectors.Limits{Requests: 4, Pages: 4, PageSize: 50, Bytes: 64 << 10},
	}
	a, err := connectors.OpenDelivery(ctx, config)
	must(t, "open unchanged existing native Jira profile", err)
	action := connectors.Action{
		WorkspaceID: config.WorkspaceID, FindingID: "fixture-finding", IntentID: "fixture-intent", ApprovalRef: "fixture-only",
		Title: "Synthetic <script>inert text</script>", Body: "Severity: high\nAsset: owned fixture",
		DeepLink: origin + "/#/work?finding=fixture-finding",
		Fields:   map[string]string{"customfield_10010": "owned fixture", "customfield_10011": "high"},
	}
	p := n.arm(config.Token, "paged", "ok", nil)
	v, err := a.Preview(ctx, action)
	must(t, "real native paginated TLS createmeta", err)
	check(t, len(v.MissingFields) == 0 && p.gets.Load() == 2 && p.posts.Load() == 0, "native preview calibration failed")
	sent, err := a.Send(ctx, action)
	must(t, "real native TLS create", err)
	check(t, sent.State == "confirmed" && sent.RemoteID == "SYN-42" && p.posts.Load() == 1, "native receipt calibration failed")
	assertADF(t, awaitCall(t, p.postArrived), payload{action.Title, action.Body, action.DeepLink, action.Fields})
	action.Prior = &sent
	_, err = a.Send(ctx, action)
	must(t, "native confirmed-prior no-write calibration", err)
	check(t, p.posts.Load() == 1, "native confirmed prior repeated creation")
	action.Prior = nil
	p = n.arm(config.Token, "unsupported", "ok", nil)
	v, err = a.Preview(ctx, action)
	check(t, errors.Is(err, connectors.ErrRequiredFields) && reflectStrings(v.MissingFields, []string{"customfield_10010"}) &&
		p.posts.Load() == 0, "unsupported required native type did not diagnose without POST")
	p = n.arm(config.Token, "ok", "drop", nil)
	sent, err = a.Send(ctx, action)
	check(t, errors.Is(err, connectors.ErrUncertain) && sent.State == "uncertain" && p.posts.Load() == 1,
		"owned dropped-ACK calibration did not produce native uncertainty")
	action.Prior = &sent
	_, err = a.Send(ctx, action)
	check(t, errors.Is(err, connectors.ErrUncertain) && p.posts.Load() == 1, "native uncertainty calibration resent")
	action.Prior = nil
	for _, tc := range []struct {
		mode  string
		state string
		code  error
		http  int
	}{
		{"403", "failed", connectors.ErrAuth, 403},
		{"429", "rate-limited", connectors.ErrRateLimited, 429},
		{"400", "failed", connectors.ErrRequiredFields, 400},
		{"no-result", "uncertain", connectors.ErrUncertain, 201},
		{"malformed", "uncertain", connectors.ErrUncertain, 201},
	} {
		p = n.arm(config.Token, "ok", tc.mode, nil)
		result, err := a.Send(ctx, action)
		var detail *connectors.Error
		check(t, errors.Is(err, tc.code) && errors.As(err, &detail) && detail.HTTPStatus == tc.http &&
			result.State == tc.state && p.posts.Load() == 1 && p.gets.Load() == 0,
			"native error/receipt calibration differs from the unchanged adapter")
		if tc.http == 429 {
			check(t, result.RetryAfter == 7*time.Second, "native Retry-After calibration failed")
		}
	}
	for _, phase := range []string{"metadata", "create"} {
		metadata, create := "hold", "ok"
		if phase == "create" {
			metadata, create = "ok", "hold"
		}
		p = n.arm(config.Token, metadata, create, nil)
		held, stop := context.WithCancel(ctx)
		done := make(chan error, 1)
		go func() {
			if phase == "metadata" {
				_, err := a.Preview(held, action)
				done <- err
			} else {
				_, err := a.Send(held, action)
				done <- err
			}
		}()
		arrived, cancelled := p.getArrived, p.getCancelled
		if phase == "create" {
			arrived, cancelled = p.postArrived, p.postCancelled
		}
		awaitCall(t, arrived)
		stop()
		awaitCancel(t, cancelled)
		select {
		case err := <-done:
			check(t, errors.Is(err, context.Canceled), "native held-body fixture did not retain context cancellation")
		case <-ctx.Done():
			t.Fatal("native cancellation calibration exceeded its deadline")
		}
	}
	t.Log("CALIBRATED: fresh trusted TLS, paginated GET, POST/ADF/receipt, required-field zero-POST, native 403/429/400/no-result/drop-ACK and held-body cancellation; not application acceptance")
}
func reflectStrings(a, b []string) bool {
	return strings.Join(a, "\x00") == strings.Join(b, "\x00")
}
