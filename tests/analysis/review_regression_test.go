package analysis

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/bahadrdsr/aspm/internal/evidence"
)

func TestM11ReviewRejectedGenerationRetainsUsage(t *testing.T) {
	for _, tc := range []struct {
		name, family, stop string
		want               error
	}{
		{"incomplete-openai", "openai", "incomplete", ErrOutput},
		{"tool-openai", "openai", "completed", ErrToolOutput},
		{"tool-anthropic", "anthropic", "tool_use", ErrToolOutput},
		{"max-tokens-anthropic", "anthropic", "max_tokens", ErrOutput},
	} {
		for _, usageCase := range []struct {
			name  string
			known bool
		}{
			{"reported-usage", true},
			{"missing-usage", false},
		} {
			t.Run(tc.name+"/"+usageCase.name, func(t *testing.T) {
				body := decode(t, []byte(providerResponse(tc.family, assessmentText))).(map[string]any)
				wantUsage := Usage{Known: true, InputTokens: 11, OutputTokens: 7, CachedInputTokens: 3}
				if tc.family == "anthropic" {
					body["stop_reason"] = tc.stop
					body["usage"].(map[string]any)["cache_creation_input_tokens"] = 5
					wantUsage.InputTokens, wantUsage.CacheWriteTokens = 16, 5
				}
				switch tc.name {
				case "incomplete-openai":
					body["status"] = tc.stop
					body["incomplete_details"] = map[string]any{"reason": "max_output_tokens"}
				case "tool-openai":
					body["output"] = []any{map[string]any{
						"id": "synthetic-function", "type": "function_call", "call_id": "synthetic-call",
						"name": "synthetic_disabled_tool", "arguments": "{}",
					}}
				case "tool-anthropic":
					body["content"] = []any{map[string]any{
						"id": "synthetic-tool", "type": "tool_use", "name": "synthetic_disabled_tool",
						"input": map[string]any{},
					}}
				}
				if !usageCase.known {
					delete(body, "usage")
					wantUsage = Usage{}
				}
				response, err := json.Marshal(body)
				requireOK(t, err)
				base, client, calls := endpoint(t, func(w http.ResponseWriter, r *http.Request) {
					path := "/v1/responses"
					if tc.family == "anthropic" {
						path = "/v1/messages"
					}
					check(t, r.Method == http.MethodPost && r.URL.Path == path, "rejected generation caused an unexpected route or tool request")
					w.Header().Set("Content-Type", "application/json")
					_, _ = w.Write(response)
				})
				result, err := openAssessor(t, providerConfig(tc.family, base, client)).Assess(boundedContext(t), assessmentRequest())
				check(t, errors.Is(err, tc.want), "rejected generation lost its expected output/tool error")
				check(t, result.Assessment == nil, "rejected generation returned an assessment")
				check(t, result.Usage == wantUsage, fmt.Sprintf("rejected generation usage: got %+v, want %+v", result.Usage, wantUsage))
				check(t, result.StopReason == tc.stop, fmt.Sprintf("rejected generation stop reason: got %q, want %q", result.StopReason, tc.stop))
				check(t, calls.Load() == 1, "rejected generation retried, fell back, or executed a tool")
			})
		}
	}
}

func TestM11ReviewLocalRefusalRejectsValidAssessment(t *testing.T) {
	body := decode(t, []byte(providerResponse("local", assessmentText))).(map[string]any)
	choice := body["choices"].([]any)[0].(map[string]any)
	message := choice["message"].(map[string]any)
	message["refusal"] = "Synthetic refusal despite assessment-shaped content."
	response, err := json.Marshal(body)
	requireOK(t, err)
	base, client, calls := endpoint(t, func(w http.ResponseWriter, r *http.Request) {
		check(t, r.Method == http.MethodPost && r.URL.Path == "/v1/chat/completions", "local refusal caused an unexpected request")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, string(response))
	})
	result, err := openAssessor(t, providerConfig("local", base, client)).Assess(boundedContext(t), assessmentRequest())
	check(t, errors.Is(err, ErrOutput), "local message.refusal was ignored despite schema-valid assessment content")
	check(t, result.Assessment == nil, "local refusal returned an assessment")
	wantUsage := Usage{Known: true, InputTokens: 11, OutputTokens: 7, CachedInputTokens: 3}
	check(t, result.Usage == wantUsage, fmt.Sprintf("local refusal usage: got %+v, want %+v", result.Usage, wantUsage))
	check(t, result.StopReason == "stop", "local refusal lost its native finish reason")
	check(t, calls.Load() == 1, "local refusal retried or fell back")
}

func TestM12ReviewVerifierCancellationAndScopeBoundaries(t *testing.T) {
	for _, tc := range []struct {
		name, outcome string
		openErr, want error
		wantOpens     int32
	}{
		{"cancelled-after-approval", "cancelled", nil, context.Canceled, 0},
		{"open-cancelled", "cancelled", context.Canceled, context.Canceled, 1},
		{"open-scope", "blocked", evidence.ErrScope, evidence.ErrScope, 1},
		{"open-integrity", "error", evidence.ErrIntegrity, evidence.ErrIntegrity, 1},
		{"open-missing", "error", evidence.ErrNotFound, evidence.ErrNotFound, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture, config, request := verificationFixture(true)
			fixture.openErr = tc.openErr
			ctx, cancel := context.WithCancel(boundedContext(t))
			t.Cleanup(cancel)
			if tc.name == "cancelled-after-approval" {
				lookup := config.LookupApproval
				config.LookupApproval = func(ctx context.Context, ref string) (Approval, error) {
					approval, err := lookup(ctx, ref)
					cancel()
					return approval, err
				}
			}
			result, err := openVerifier(t, config).Verify(ctx, request)
			check(t, errors.Is(err, tc.want), "verification boundary failure lost the original cancellation/evidence error identity")
			check(t, result.Outcome == tc.outcome, fmt.Sprintf("verification boundary outcome: got %q, want %q", result.Outcome, tc.outcome))
			check(t, fixture.opens.Load() == tc.wantOpens, fmt.Sprintf("verification evidence opens: got %d, want %d", fixture.opens.Load(), tc.wantOpens))
			check(t, fixture.closes.Load() == 0, "verification opened a reader after cancellation or fabricated a reader on Open failure")
			check(t, len(result.Artifacts) == 0, "failed verification retained success artifacts")
			check(t, !result.CloseFinding && !result.FalsePositive, "failed verification closed or classified a finding")
			check(t, result.WorkspaceID == request.WorkspaceID && result.FindingID == request.FindingID &&
				result.RunID == request.RunID && result.EnvironmentID == request.EnvironmentID &&
				result.ScopeRevision == request.ScopeRevision, "verification failure lost the approved identity snapshot")
		})
	}
}
