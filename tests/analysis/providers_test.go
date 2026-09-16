package analysis

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"
)

const assessmentText = `{"conclusion":"supported","uncertainty":"Synthetic fixture assessment, not a live verification.","evidenceRefs":["synthetic-evidence-1"]}`

func assessmentRequest() AssessmentRequest {
	text := "SYNTHETIC FIXTURE ONLY: component A has a documented configuration concern. No target exists."
	return AssessmentRequest{WorkspaceID: "synthetic-workspace", RunID: "synthetic-run", FindingID: "synthetic-finding",
		ProfileID: "primary", Task: "assessment", DataClass: "synthetic", PromptRevision: "synthetic-prompt-v1",
		EvidenceID: "synthetic-evidence-1", EvidenceDigest: fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(text))), EvidenceText: text}
}

func providerConfig(family, base string, client *http.Client) ProviderConfig {
	profile := Profile{ID: "primary", Family: family, Endpoint: base, Model: "operator-selected-synthetic-model",
		APIKey: syntheticKey, Revision: "synthetic-profile-v1", StructuredOutput: true}
	policy := Policy{Mode: "approved-hosted", ApprovalRef: "synthetic-hosted-approval", Revision: "synthetic-policy-v1",
		Workspaces: []string{"synthetic-workspace"}, Tasks: []string{"assessment"}, DataClasses: []string{"synthetic"}, AllowedDestinations: []string{base}}
	if family == "azure-foundry" {
		profile.Deployment = "operator-selected-deployment"
	}
	if family == "local" {
		profile.APIKey = ""
		policy.Mode, policy.ApprovalRef = "local-only", ""
	}
	return ProviderConfig{Profiles: map[string]Profile{"primary": profile}, Policy: policy, Client: client,
		MaxAttempts: 1, MaxOutputTokens: 256, MaxResponseBytes: 65536}
}

func providerResponse(family, content string) string {
	switch family {
	case "anthropic":
		return fmt.Sprintf(`{"id":"synthetic-message","type":"message","role":"assistant","model":"returned-operator-fixture","content":[{"type":"text","text":%q}],"stop_reason":"end_turn","usage":{"input_tokens":8,"output_tokens":7,"cache_read_input_tokens":3,"cache_creation_input_tokens":0}}`, content)
	case "local":
		return fmt.Sprintf(`{"id":"synthetic-completion","model":"returned-operator-fixture","choices":[{"index":0,"message":{"role":"assistant","content":%q},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":7,"total_tokens":18,"prompt_tokens_details":{"cached_tokens":3}}}`, content)
	default:
		return fmt.Sprintf(`{"id":"synthetic-response","model":"returned-operator-fixture","status":"completed","output":[{"type":"reasoning","id":"synthetic-reasoning","summary":[]},{"type":"message","role":"assistant","content":[{"type":"output_text","text":%q,"annotations":[]}]}],"usage":{"input_tokens":11,"output_tokens":7,"total_tokens":18,"input_tokens_details":{"cached_tokens":3}}}`, content)
	}
}

func TestM11NativeProvidersAndUsage(t *testing.T) {
	for _, family := range []string{"openai", "azure-foundry", "anthropic", "local"} {
		t.Run(family, func(t *testing.T) {
			request := assessmentRequest()
			base, client, calls := endpoint(t, func(w http.ResponseWriter, r *http.Request) {
				data, err := io.ReadAll(io.LimitReader(r.Body, 65537))
				check(t, err == nil && len(data) <= 65536 && r.Method == "POST" && r.URL.RawQuery == "" && strings.HasPrefix(r.Header.Get("Content-Type"), "application/json"), "provider request must be bounded non-streaming native JSON POST")
				_ = r.Body.Close()
				body := decode(t, data)
				check(t, bytes.Contains(data, []byte(request.EvidenceText)) && bytes.Contains(data, []byte(request.EvidenceID)), "provider prompt lost explicitly synthetic grounded evidence")
				check(t, !bytes.Contains(data, []byte(syntheticKey)), "provider credential was included in inference content")
				check(t, at(t, body, "tools") == nil || reflect.DeepEqual(at(t, body, "tools"), []any{}), "assessment must not register shell/network/model tools")
				check(t, at(t, body, "stream") == false, "first provider profile must explicitly disable streaming")
				var schema any
				model := "operator-selected-synthetic-model"
				switch family {
				case "anthropic":
					check(t, r.URL.Path == "/v1/messages" && r.Header.Get("x-api-key") == syntheticKey && r.Header.Get("anthropic-version") == "2023-06-01" && r.Header.Get("anthropic-beta") == "", "Anthropic must use native Messages headers/path without unreviewed beta")
					check(t, at(t, body, "max_tokens") == float64(256) && at(t, body, "messages") != nil, "Anthropic token/messages mapping missing")
					check(t, at(t, body, "output_config", "format", "type") == "json_schema", "Anthropic native structured output mapping missing")
					schema = at(t, body, "output_config", "format", "schema")
					w.Header().Set("request-id", "synthetic-request-id")
				case "local":
					check(t, r.URL.Path == "/v1/chat/completions" && r.Header.Get("Authorization") == "", "local compatible profile must not become a hosted/credentialed fallback")
					check(t, at(t, body, "max_tokens") == float64(256) && at(t, body, "messages") != nil, "local chat token/messages mapping missing")
					check(t, at(t, body, "response_format", "type") == "json_schema", "local structured-output capability mapping missing")
					check(t, at(t, body, "response_format", "json_schema", "strict") == true, "local structured-output profile must not silently downgrade its schema")
					schema = at(t, body, "response_format", "json_schema", "schema")
					w.Header().Set("x-request-id", "synthetic-request-id")
				default:
					path, header := "/v1/responses", "x-request-id"
					if family == "azure-foundry" {
						path, header, model = "/openai/v1/responses", "apim-request-id", "operator-selected-deployment"
						check(t, r.Header.Get("api-key") == syntheticKey && r.Header.Get("Authorization") == "", "Foundry key/deployment auth mapping missing")
					} else {
						check(t, r.Header.Get("Authorization") == "Bearer "+syntheticKey, "OpenAI bearer mapping missing")
					}
					check(t, r.URL.Path == path && at(t, body, "input") != nil && at(t, body, "store") == false && at(t, body, "max_output_tokens") == float64(256), "Responses path/input/privacy/token mapping missing")
					check(t, at(t, body, "text", "format", "type") == "json_schema" && at(t, body, "text", "format", "strict") == true, "Responses native structured output mapping missing")
					schema = at(t, body, "text", "format", "schema")
					w.Header().Set(header, "synthetic-request-id")
				}
				check(t, at(t, body, "model") == model && reflect.DeepEqual(schema, decode(t, []byte(assessmentSchema))), "configured model/deployment or assessment schema was changed")
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, providerResponse(family, assessmentText))
			})
			config := providerConfig(family, base, client)
			result, err := openAssessor(t, config).Assess(boundedContext(t), request)
			requireOK(t, err)
			if result.Assessment == nil {
				t.Fatal("provider returned no structured assessment")
			}
			check(t, result.Assessment.Conclusion == "supported" && result.Assessment.Uncertainty != "" && reflect.DeepEqual(result.Assessment.EvidenceRefs, []string{request.EvidenceID}), "assessment structure/grounding lost; exact generated prose is not asserted")
			check(t, result.Usage.Known && result.Usage.InputTokens == 11 && result.Usage.OutputTokens == 7 && result.Usage.CachedInputTokens == 3, "native usage mapping lost total input/output/cache semantics")
			check(t, result.WorkspaceID == request.WorkspaceID && result.RunID == request.RunID && result.FindingID == request.FindingID && result.ProfileID == request.ProfileID, "assessment identity changed")
			check(t, result.ProfileRevision == config.Profiles["primary"].Revision && result.PolicyRevision == config.Policy.Revision && result.PromptRevision == request.PromptRevision && result.EvidenceDigest == request.EvidenceDigest, "assessment provenance snapshot lost")
			check(t, result.RequestID == "synthetic-request-id" && result.RequestedModel == config.Profiles["primary"].Model && result.ReturnedModel == "returned-operator-fixture" && result.Deployment == config.Profiles["primary"].Deployment, "native request/model/deployment audit mapping lost")
			wantStop := map[string]string{"openai": "completed", "azure-foundry": "completed", "anthropic": "end_turn", "local": "stop"}[family]
			check(t, result.StopReason == wantStop && calls.Load() == 1, "native stop reason was coerced or inference retried")
		})
	}
}

func TestM11PolicyAndNoUnapprovedFallback(t *testing.T) {
	for _, mode := range []string{"disabled", "local-only-hosted", "missing-approval", "foreign-workspace", "wrong-destination", "unapproved-fallback", "unverified-capability", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			base, client, calls := endpoint(t, func(w http.ResponseWriter, r *http.Request) {
				check(t, mode == "unapproved-fallback" && r.URL.Path == "/v1/chat/completions", "policy denial or provider fallback caused unapproved processing")
				w.WriteHeader(503)
				_, _ = io.WriteString(w, `{"error":{"message":"synthetic local provider unavailable"}}`)
			})
			config := providerConfig("openai", base, client)
			request := assessmentRequest()
			ctx := boundedContext(t)
			want, wantCalls := ErrPolicy, int32(0)
			switch mode {
			case "disabled":
				config.Policy.Mode = "disabled"
			case "local-only-hosted":
				config.Policy.Mode, config.Policy.ApprovalRef = "local-only", ""
			case "missing-approval":
				config.Policy.ApprovalRef = ""
			case "foreign-workspace":
				request.WorkspaceID = "other-workspace"
			case "wrong-destination":
				config.Policy.AllowedDestinations = []string{base + "/different-approved-base"}
			case "unapproved-fallback":
				config = providerConfig("local", base, client)
				config.Profiles["unapproved"] = Profile{ID: "unapproved", Family: "openai", Endpoint: base + "/must-not-fallback", APIKey: syntheticKey, Model: "operator-fallback", StructuredOutput: true}
				want, wantCalls = ErrProvider, 1
			case "unverified-capability":
				profile := config.Profiles["primary"]
				profile.StructuredOutput = false
				config.Profiles["primary"], want = profile, ErrCapability
			case "cancelled":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx, want = cancelled, context.Canceled
			}
			result, err := openAssessor(t, config).Assess(ctx, request)
			check(t, errors.Is(err, want) && result.Assessment == nil && calls.Load() == wantCalls, "policy was bypassed or unavailable local inference silently fell back")
		})
	}
}

func TestM11ProviderFailuresAndUntrustedOutput(t *testing.T) {
	for _, tc := range []struct {
		name, family string
		status       int
		want         error
	}{
		{"rate-limit", "openai", 429, ErrRateLimited}, {"authentication", "openai", 401, ErrAuth},
		{"deployment-missing", "azure-foundry", 404, ErrProvider}, {"overloaded", "anthropic", 529, ErrProvider},
		{"invalid-schema", "openai", 200, ErrOutput}, {"ungrounded", "local", 200, ErrOutput},
		{"tool-block", "anthropic", 200, ErrToolOutput}, {"tool-call", "openai", 200, ErrToolOutput},
		{"incomplete", "openai", 200, ErrOutput}, {"response-cap", "local", 200, ErrLimit},
		{"redirect", "openai", 307, ErrPolicy},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, client, calls := endpoint(t, func(w http.ResponseWriter, r *http.Request) {
				check(t, !strings.Contains(r.URL.Path, "must-not-fetch"), "model output or redirect executed an unapproved network request")
				body := providerResponse(tc.family, assessmentText)
				switch tc.name {
				case "rate-limit":
					body = `{"error":{"type":"rate_limit_error","code":"rate_limit_exceeded","message":"synthetic quota"}}`
				case "authentication":
					body = `{"error":{"type":"invalid_request_error","code":"invalid_api_key","message":"synthetic revoked key"}}`
				case "deployment-missing":
					body = `{"error":{"code":"DeploymentNotFound","message":"synthetic missing deployment"}}`
				case "overloaded":
					body = `{"type":"error","error":{"type":"overloaded_error","message":"synthetic overload"},"request_id":"synthetic-request"}`
				case "invalid-schema":
					body = providerResponse(tc.family, `{"conclusion":"fixed"}`)
				case "ungrounded":
					body = providerResponse(tc.family, strings.Replace(assessmentText, "synthetic-evidence-1", "unprovided-evidence", 1))
				case "tool-block":
					body = `{"id":"synthetic-message","type":"message","role":"assistant","model":"returned-operator-fixture","content":[{"type":"tool_use","id":"synthetic-tool","name":"synthetic_network_tool","input":{"url":"https://` + r.Host + `/must-not-fetch"}}],"stop_reason":"tool_use","usage":{"input_tokens":8,"output_tokens":7}}`
				case "tool-call":
					body = `{"id":"synthetic-response","model":"returned-operator-fixture","status":"completed","output":[{"id":"synthetic-function","type":"function_call","name":"synthetic_disabled_shell","arguments":"{}","call_id":"synthetic-tool"}],"usage":{"input_tokens":11,"output_tokens":7,"total_tokens":18}}`
				case "incomplete":
					body = strings.Replace(body, `"status":"completed"`, `"status":"incomplete"`, 1)
				case "redirect":
					w.Header().Set("Location", "https://"+r.Host+"/must-not-fetch")
				}
				w.Header().Set("Retry-After", "3")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, body)
			})
			config := providerConfig(tc.family, base, client)
			if tc.name == "response-cap" {
				config.MaxResponseBytes = 64
			}
			result, err := openAssessor(t, config).Assess(boundedContext(t), assessmentRequest())
			check(t, errors.Is(err, tc.want) && result.Assessment == nil && calls.Load() == 1, "provider failure/tool suggestion became success, retry, fallback or execution")
			if tc.status == 429 {
				check(t, result.RetryAfter == 3*time.Second, "provider Retry-After was lost")
			}
		})
	}
}
