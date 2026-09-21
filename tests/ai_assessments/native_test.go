//go:build integration

package ai_assessments

import (
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

	"github.com/bahadrdsr/aspm/internal/app"
	"github.com/jackc/pgx/v5"
)

type nativeScript struct {
	family, key, model, deployment, text, ref, contextDigest, mode, conclusion, jobID string
	held                                                                              bool
	entered, release, cancelled                                                       chan struct{}
	replyReady                                                                        atomic.Bool
	requests                                                                          atomic.Int32
	releaseOnce                                                                       sync.Once
}

func (s *nativeScript) allowResponse() { s.releaseOnce.Do(func() { close(s.release) }) }

type nativeFixture struct {
	f           *fixture
	tls, local  *httptest.Server
	client      *http.Client
	mu          sync.Mutex
	script      *nativeScript
	calls       int
	markerReads int
}

func newNative(t *testing.T, f *fixture) *nativeFixture {
	n := &nativeFixture{f: f}
	n.tls = httptest.NewTLSServer(http.HandlerFunc(n.serve))
	n.local = httptest.NewServer(http.HandlerFunc(n.serve))
	t.Cleanup(n.tls.Close)
	t.Cleanup(n.local.Close)
	roots := x509.NewCertPool()
	roots.AddCert(n.tls.Certificate())
	allowed := map[string]bool{strings.TrimPrefix(n.tls.URL, "https://"): true, strings.TrimPrefix(n.local.URL, "http://"): true}
	transport := &http.Transport{Proxy: nil, TLSClientConfig: &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12},
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			if !allowed[address] {
				f.foreignDials.Add(1)
				return nil, errors.New("native fixture denied a foreign destination")
			}
			return (&net.Dialer{Timeout: time.Second}).DialContext(ctx, network, address)
		}}
	t.Cleanup(transport.CloseIdleConnections)
	n.client = &http.Client{Transport: transport, Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}}
	return n
}
func (n *nativeFixture) count() int { n.mu.Lock(); defer n.mu.Unlock(); return n.calls }
func (n *nativeFixture) arm(p app.AIProfile, key string, v preview, jobID, mode, conclusion string, held bool) *nativeScript {
	return n.armEvidence(p, key, v.Context, v.ContextRef, v.ContextDigest, jobID, mode, conclusion, held)
}
func (n *nativeFixture) armEvidence(p app.AIProfile, key, text, ref, contextDigest, jobID, mode, conclusion string, held bool) *nativeScript {
	s := &nativeScript{family: p.Family, key: key, model: p.Model, deployment: p.Deployment, text: text, ref: ref,
		contextDigest: contextDigest, jobID: jobID, mode: mode, conclusion: conclusion, held: held,
		entered: make(chan struct{}), release: make(chan struct{}), cancelled: make(chan struct{})}
	n.mu.Lock()
	n.script = s
	n.mu.Unlock()
	return s
}
func objectAt(value any, fields ...string) any {
	for _, field := range fields {
		object, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		value = object[field]
	}
	return value
}
func (n *nativeFixture) verify(ok bool, label string) {
	if !ok {
		n.f.t.Error("owned native boundary: " + label)
	}
}
func (n *nativeFixture) serve(w http.ResponseWriter, r *http.Request) {
	n.mu.Lock()
	n.calls++
	count, s := n.calls, n.script
	n.mu.Unlock()
	if count > 32 || s == nil {
		n.f.t.Error("native request exceeded bound or occurred without an explicit assessment scenario")
		http.Error(w, "unexpected native request", 503)
		return
	}
	if s.requests.Add(1) != 1 {
		n.f.t.Error("native assessment was retried or duplicated")
		http.Error(w, "duplicate native request", 503)
		return
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, 256<<10))
	_ = r.Body.Close()
	n.verify(err == nil && len(data) < 256<<10 && r.Method == "POST" && r.URL.RawQuery == "", "bounded single native JSON POST required")
	var body map[string]any
	n.verify(json.Unmarshal(data, &body) == nil, "native request JSON invalid")
	n.f.noSecrets(data)
	n.verify(!strings.Contains(string(data), "SYNTHETIC-RAW-NOT-APPROVED") &&
		!strings.Contains(string(data), "SYNTHETIC-NOTE-NOT-APPROVED"), "unapproved raw/notes content in native request")
	n.verify(body["stream"] == false && body["tools"] == nil && body["tool_choice"] == nil, "streaming/tools must not be enabled")
	var messages any
	model := s.model
	var schema any
	allowedFields := map[string]bool{"model": true, "stream": true}
	switch s.family {
	case "anthropic":
		for _, field := range []string{"system", "messages", "max_tokens", "output_config"} {
			allowedFields[field] = true
		}
		n.verify(r.URL.Path == "/v1/messages" && r.Header.Get("x-api-key") == s.key &&
			r.Header.Get("anthropic-version") == "2023-06-01" && r.Header.Get("anthropic-beta") == "", "Messages path/auth/version mapping")
		n.verify(body["max_tokens"] == float64(128), "Messages output cap")
		messages = body["messages"]
		n.verify(body["system"] == serverInstructions && objectAt(body, "output_config", "format", "type") == "json_schema", "native Messages schema/system")
		schema = objectAt(body, "output_config", "format", "schema")
		w.Header().Set("request-id", "fixture-native-request")
	case "local":
		for _, field := range []string{"messages", "max_tokens", "response_format"} {
			allowedFields[field] = true
		}
		n.verify(r.URL.Path == "/v1/chat/completions" && r.Header.Get("Authorization") == "", "local compatible route/keyless mapping")
		n.verify(body["max_tokens"] == float64(128), "chat output cap")
		messages = body["messages"]
		n.verify(objectAt(body, "response_format", "type") == "json_schema" &&
			objectAt(body, "response_format", "json_schema", "strict") == true, "compatible Chat structured output")
		schema = objectAt(body, "response_format", "json_schema", "schema")
		w.Header().Set("x-request-id", "fixture-native-request")
	default:
		for _, field := range []string{"input", "store", "max_output_tokens", "text"} {
			allowedFields[field] = true
		}
		expectedPath, header := "/v1/responses", "x-request-id"
		if s.family == "azure-foundry" {
			expectedPath, header, model = "/openai/v1/responses", "apim-request-id", s.deployment
			n.verify(r.Header.Get("api-key") == s.key && r.Header.Get("Authorization") == "", "Foundry deployment/key mapping")
		} else {
			n.verify(r.Header.Get("Authorization") == "Bearer "+s.key, "OpenAI bearer mapping")
		}
		n.verify(len(body) == len(allowedFields), "undeclared native body fields may not upload additional data")
		for field := range body {
			n.verify(allowedFields[field], "native request included an undeclared field")
		}
		n.verify(r.URL.Path == expectedPath && body["store"] == false && body["max_output_tokens"] == float64(128), "Responses route/privacy/output cap")
		messages = body["input"]
		n.verify(objectAt(body, "text", "format", "type") == "json_schema" && objectAt(body, "text", "format", "strict") == true, "Responses structured output")
		schema = objectAt(body, "text", "format", "schema")
		w.Header().Set(header, "fixture-native-request")
	}
	n.verify(body["model"] == model, "operator model/deployment must not be guessed or replaced")
	var expectedSchema any
	_ = json.Unmarshal([]byte(assessmentSchema), &expectedSchema)
	n.verify(reflect.DeepEqual(schema, expectedSchema), "adapter assessment schema changed")
	var content strings.Builder
	if items, ok := messages.([]any); ok {
		if s.family == "anthropic" {
			n.verify(len(items) == 1, "Messages included unapproved conversation context")
		} else {
			n.verify(len(items) == 2 && objectAt(items[0], "role") == "system" &&
				objectAt(items[0], "content") == serverInstructions, "trusted server prompt was replaced or expanded")
		}
		for _, item := range items {
			if objectAt(item, "role") == "user" {
				text, _ := objectAt(item, "content").(string)
				content.WriteString(text)
			}
		}
	}
	expectedContent := "Evidence identifier: " + s.ref + "\nEvidence SHA-256: " + s.contextDigest +
		"\nBEGIN UNTRUSTED EVIDENCE\n" + s.text + "\nEND UNTRUSTED EVIDENCE"
	n.verify(content.String() == expectedContent, "only the exact approved context and trusted wrapper may be sent")
	for _, forbidden := range []string{"SYNTHETIC-RAW-NOT-APPROVED", "SYNTHETIC-NOTE-NOT-APPROVED", n.f.config.BootstrapToken,
		n.f.rawStorage.SecretKey, n.f.rawStorage.AccessKey, n.f.database.DatabaseURL, s.key} {
		if forbidden != "" {
			n.verify(!strings.Contains(content.String(), forbidden), "credentials/raw/unapproved context crossed native boundary")
		}
	}
	if s.jobID != "" {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		var state string
		var attempts int
		var started *time.Time
		err := n.f.db.QueryRow(ctx, "SELECT state,attempts,dispatch_started_at FROM "+n.f.table("assessment_jobs")+" WHERE id=$1", s.jobID).Scan(&state, &attempts, &started)
		cancel()
		n.verify(err == nil && state == "dispatching" && attempts == 1 && started != nil, "dispatch marker must be committed before native POST")
		n.mu.Lock()
		n.markerReads++
		n.mu.Unlock()
	}
	close(s.entered)
	if s.held {
		select {
		case <-s.release:
		case <-r.Context().Done():
			close(s.cancelled)
			return
		}
	}
	status, response := nativeResponse(s)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Retry-After", "3")
	if s.mode == "redirect" {
		w.Header().Set("Location", n.tls.URL+"/must-not-fetch")
	}
	w.WriteHeader(status)
	s.replyReady.Store(true)
	_, _ = w.Write(response)
}

func nativeResponse(s *nativeScript) (int, []byte) {
	answer := map[string]any{"conclusion": s.conclusion, "uncertainty": "Synthetic advisory only, not proof or permission to mutate a finding.", "evidenceRefs": []string{s.ref}}
	if s.mode == "schema" {
		delete(answer, "evidenceRefs")
	}
	if s.mode == "grounding" {
		answer["evidenceRefs"] = []string{"unapproved-context"}
	}
	text, _ := json.Marshal(answer)
	var response map[string]any
	switch s.family {
	case "anthropic":
		response = map[string]any{"model": "fixture-returned-model", "content": []any{map[string]any{"type": "text", "text": string(text)}},
			"stop_reason": "end_turn", "usage": map[string]any{"input_tokens": 8, "output_tokens": 7, "cache_read_input_tokens": 3, "cache_creation_input_tokens": 2}}
	case "local":
		response = map[string]any{"model": "fixture-returned-model", "choices": []any{map[string]any{
			"message": map[string]any{"role": "assistant", "content": string(text)}, "finish_reason": "stop"}},
			"usage": map[string]any{"prompt_tokens": 11, "completion_tokens": 7, "prompt_tokens_details": map[string]int{"cached_tokens": 3}}}
	default:
		response = map[string]any{"model": "fixture-returned-model", "status": "completed", "output": []any{
			map[string]any{"type": "reasoning", "summary": []any{}},
			map[string]any{"type": "message", "role": "assistant", "content": []any{map[string]any{"type": "output_text", "text": string(text)}}}},
			"usage": map[string]any{"input_tokens": 11, "output_tokens": 7, "input_tokens_details": map[string]int{"cached_tokens": 3}}}
	}
	switch s.mode {
	case "unknown-usage":
		delete(response, "usage")
	case "tool-output":
		if s.family == "anthropic" {
			response["content"], response["stop_reason"] = []any{map[string]any{"type": "tool_use", "id": "untrusted-tool",
				"name": "never_execute_network_or_shell", "input": map[string]string{"value": "untrusted data"}}}, "tool_use"
		} else {
			response["output"] = []any{map[string]any{"type": "function_call", "name": "never_execute_network_or_shell", "arguments": "{}"}}
		}
	case "refusal":
		response["output"] = []any{map[string]any{"type": "message", "role": "assistant",
			"content": []any{map[string]string{"type": "refusal", "refusal": "Synthetic refusal."}}}}
	case "response-limit":
		return 200, []byte(strings.Repeat("x", (32<<10)+1))
	case "auth":
		return 401, []byte(`{"error":{"message":"synthetic rejected credential"}}`)
	case "rate-limited":
		return 429, []byte(`{"error":{"message":"synthetic shared provider rate limit"}}`)
	case "provider":
		data, _ := json.Marshal(map[string]any{"error": map[string]string{"message": "must not persist diagnostic " + s.key}})
		return 503, data
	case "redirect":
		return 307, nil
	}
	data, _ := json.Marshal(response)
	return 200, data
}

type finalizationGate struct {
	armed   *atomic.Bool
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (g *finalizationGate) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	sql := strings.ToLower(strings.TrimSpace(data.SQL))
	finalizing := strings.HasPrefix(sql, "begin") ||
		(strings.Contains(sql, "assessment_jobs") && strings.Contains(sql, "update"))
	if g.armed.Load() && finalizing {
		g.once.Do(func() {
			close(g.entered)
			select {
			case <-g.release:
			case <-ctx.Done():
			}
		})
	}
	return ctx
}
func (*finalizationGate) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}
