//go:build integration

package assessment_runtime

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type nativeRequest struct {
	job                       assessment
	key                       string
	local, held               bool
	ready, cancelled, release chan struct{}
	cancelledAt               time.Time
	once                      sync.Once
	posts                     atomic.Int32
}

func (r *nativeRequest) allow() { r.once.Do(func() { close(r.release) }) }

type nativeFixture struct {
	f          *fixture
	tls, local *httptest.Server
	client     *http.Client
	ca         string
	calls      atomic.Int32
	mu         sync.Mutex
	current    *nativeRequest
}

func newNative(f *fixture) *nativeFixture {
	n := &nativeFixture{f: f}
	n.tls = httptest.NewTLSServer(http.HandlerFunc(n.serve))
	n.local = httptest.NewServer(http.HandlerFunc(n.serve))
	f.t.Cleanup(n.tls.Close)
	f.t.Cleanup(n.local.Close)
	roots := x509.NewCertPool()
	roots.AddCert(n.tls.Certificate())
	allowed := map[string]bool{n.tls.Listener.Addr().String(): true, n.local.Listener.Addr().String(): true}
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if !allowed[address] {
				f.t.Error("assessment transport left owned native fixture")
				return nil, errors.New("unowned native destination")
			}
			return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, address)
		}}
	f.t.Cleanup(transport.CloseIdleConnections)
	n.client = &http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	directory := filepath.Join(required(f.t, "ASPM_ASSESSMENT_RUNTIME_ARTIFACT_DIR"), "native-ca-"+nonce(f.t))
	must(f.t, "create owned public certificate directory", os.MkdirAll(directory, 0700))
	n.ca = filepath.Join(directory, "native-ca.pem")
	must(f.t, "write only synthetic public certificate", os.WriteFile(n.ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: n.tls.Certificate().Raw}), 0600))
	f.t.Cleanup(func() { _ = os.Remove(n.ca); _ = os.Remove(directory) })
	return n
}
func (n *nativeFixture) arm(job assessment, key string, held bool) *nativeRequest {
	n.mu.Lock()
	defer n.mu.Unlock()
	r := &nativeRequest{job: job, key: key, local: job.Family == "local", held: held, ready: make(chan struct{}), cancelled: make(chan struct{}), release: make(chan struct{})}
	n.current = r
	n.f.t.Cleanup(r.allow)
	return r
}
func (n *nativeFixture) verify(ok bool, message string) {
	if !ok {
		n.f.t.Error("owned assessment runtime native boundary: " + message)
	}
}
func (n *nativeFixture) serve(w http.ResponseWriter, request *http.Request) {
	n.mu.Lock()
	r := n.current
	n.mu.Unlock()
	if n.calls.Add(1) > 12 || r == nil {
		n.f.t.Error("native call before explicit job or above bound")
		http.Error(w, "owned bound", 503)
		return
	}
	if r.posts.Add(1) != 1 {
		n.f.t.Error("runtime duplicated a native attempt")
		http.Error(w, "duplicate", 503)
		return
	}
	data, err := io.ReadAll(io.LimitReader(request.Body, (128<<10)+1))
	_ = request.Body.Close()
	n.verify(err == nil && len(data) <= 128<<10 && request.Method == "POST" && request.URL.RawQuery == "", "bounded single native POST required")
	n.f.noSecrets(data)
	var body map[string]any
	n.verify(json.Unmarshal(data, &body) == nil, "invalid native JSON")
	n.verify(body["model"] == r.job.Model && body["stream"] == false && body["tools"] == nil && body["tool_choice"] == nil, "selected model/no-tools contract changed")
	content := ""
	var schema any
	if r.local {
		n.verify(request.URL.Path == "/v1/chat/completions" && request.Header.Get("Authorization") == "", "keyless local protocol mapping")
		n.verify(body["max_tokens"] == float64(1024) && len(body) == 5, "local output/default parameter contract")
		schema = at(body, "response_format", "json_schema", "schema")
		n.verify(at(body, "response_format", "json_schema", "strict") == true, "local schema strictness")
		body["input"] = body["messages"]
	} else {
		n.verify(request.URL.Path == "/v1/responses" && request.Header.Get("Authorization") == "Bearer "+r.key, "Responses path/key mapping")
		n.verify(body["store"] == false && body["max_output_tokens"] == float64(1024) && len(body) == 6, "Responses requested retention/output cap")
		n.verify(at(body, "text", "format", "strict") == true, "Responses strict schema")
		schema = at(body, "text", "format", "schema")
	}
	if messages, ok := body["input"].([]any); ok {
		n.verify(len(messages) == 2 && at(messages[0], "role") == "system" && at(messages[0], "content") == serverInstructions, "trusted prompt changed")
		if len(messages) == 2 {
			content, _ = at(messages[1], "content").(string)
			n.verify(at(messages[1], "role") == "user", "context is not untrusted user data")
		}
	}
	var wanted any
	_ = json.Unmarshal([]byte(assessmentSchema), &wanted)
	n.verify(reflect.DeepEqual(schema, wanted), "native advisory schema changed")
	want := "Evidence identifier: " + r.job.ContextRef + "\nEvidence SHA-256: " + r.job.ContextDigest + "\nBEGIN UNTRUSTED EVIDENCE\n" + r.job.Context + "\nEND UNTRUSTED EVIDENCE"
	n.verify(content == want, "exact approved context/digest/reference was not preserved")
	n.verify(!strings.Contains(content, "RAW-RUNTIME-REPORT-NOT-APPROVED") && !strings.Contains(content, "HUMAN-RUNTIME-NOTE-NOT-APPROVED"), "automatic raw context/notes upload")
	probe, cancel := context.WithTimeout(request.Context(), time.Second)
	var state string
	var attempts int
	var started *time.Time
	err = n.f.db.QueryRow(probe, "SELECT state,attempts,dispatch_started_at FROM "+n.f.table("assessment_jobs")+" WHERE id=$1", r.job.ID).Scan(&state, &attempts, &started)
	cancel()
	n.verify(err == nil && state == "dispatching" && attempts == 1 && started != nil, "actual committed dispatch marker missing")
	close(r.ready)
	if r.held {
		select {
		case <-r.release:
		case <-request.Context().Done():
			r.cancelledAt = time.Now()
			close(r.cancelled)
			return
		}
	}
	answer := map[string]any{"conclusion": "inconclusive", "uncertainty": "Synthetic advisory only, not verification or permission to mutate a finding.", "evidenceRefs": []string{r.job.ContextRef}}
	text := string(encoded(n.f.t, answer))
	var response any
	if r.local {
		response = map[string]any{"model": "owned-runtime-returned-model", "choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": text}, "finish_reason": "stop"}},
			"usage": map[string]any{"prompt_tokens": 11, "completion_tokens": 7, "prompt_tokens_details": map[string]int{"cached_tokens": 3}}}
	} else {
		response = map[string]any{"model": "owned-runtime-returned-model", "status": "completed", "output": []any{map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]string{"type": "output_text", "text": text}}}},
			"usage": map[string]any{"input_tokens": 11, "output_tokens": 7, "input_tokens_details": map[string]int{"cached_tokens": 3}}}
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("x-request-id", "owned-runtime-request")
	_, _ = w.Write(encoded(n.f.t, response))
}
func at(value any, path ...string) any {
	for _, part := range path {
		v, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		value = v[part]
	}
	return value
}
