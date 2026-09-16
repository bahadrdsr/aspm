package analysis

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bahadrdsr/aspm/internal/evidence"
)

var (
	ErrPolicy      = errors.New("analysis or verification policy denied")
	ErrAuth        = errors.New("provider authentication denied")
	ErrRateLimited = errors.New("provider rate limited")
	ErrProvider    = errors.New("provider unavailable or failed")
	ErrOutput      = errors.New("invalid or incomplete structured assessment")
	ErrToolOutput  = errors.New("untrusted model tool output refused")
	ErrLimit       = errors.New("analysis response limit reached")
	ErrCapability  = errors.New("requested provider capability unverified or unsupported")
)

type Profile struct {
	ID, Family, Endpoint, Model, Deployment, APIKey, Revision string
	StructuredOutput                                          bool
}

type Policy struct {
	Mode, ApprovalRef, Revision                         string
	Workspaces, Tasks, DataClasses, AllowedDestinations []string
	FallbackProfileIDs                                  []string
}

type ProviderConfig struct {
	Profiles                     map[string]Profile
	Policy                       Policy
	Client                       *http.Client
	MaxAttempts, MaxOutputTokens int
	MaxResponseBytes             int64
}

type AssessmentRequest struct {
	WorkspaceID, RunID, FindingID, ProfileID, Task, DataClass string
	PromptRevision, EvidenceID, EvidenceDigest, EvidenceText  string
}

type Assessment struct {
	Conclusion   string   `json:"conclusion"`
	Uncertainty  string   `json:"uncertainty"`
	EvidenceRefs []string `json:"evidenceRefs"`
}

type Usage struct {
	InputTokens, OutputTokens, CachedInputTokens, CacheWriteTokens int64
	Known                                                          bool
}

type AssessmentResult struct {
	WorkspaceID, RunID, FindingID, ProfileID, ProfileRevision, PolicyRevision string
	PromptRevision, EvidenceDigest, RequestID, RequestedModel, ReturnedModel  string
	Deployment, StopReason                                                    string
	Assessment                                                                *Assessment
	Usage                                                                     Usage
	RetryAfter                                                                time.Duration
}

type Assessor interface {
	Assess(context.Context, AssessmentRequest) (AssessmentResult, error)
}

type EvidenceReader interface {
	Open(context.Context, string, evidence.Ref) (io.ReadCloser, error)
}

type Approval struct {
	ID, WorkspaceID, FindingID, Method, ScopeRevision, EnvironmentID, EvidenceSHA256 string
	Approved                                                                         bool
	ExpiresAt                                                                        time.Time
}

type VerifierConfig struct {
	Evidence       EvidenceReader
	LookupApproval func(context.Context, string) (Approval, error)
	Now            func() time.Time
	MaxBytes       int64
}

type VerificationRequest struct {
	WorkspaceID, FindingID, RunID, ApprovalRef, Method, ScopeRevision, EnvironmentID string
	Evidence                                                                         evidence.Ref
}

type VerificationResult struct {
	WorkspaceID, FindingID, RunID, EnvironmentID, ScopeRevision, Outcome string
	Artifacts                                                            []evidence.Ref
	CloseFinding, FalsePositive                                          bool
}

var Production struct {
	OpenAssessor func(context.Context, ProviderConfig) (Assessor, error)
	OpenVerifier func(context.Context, VerifierConfig) (Verifier, error)
}

type Verifier interface {
	Verify(context.Context, VerificationRequest) (VerificationResult, error)
}

const assessmentSchema = `{"type":"object","properties":{"conclusion":{"type":"string","enum":["supported","contradicted","inconclusive"]},"uncertainty":{"type":"string"},"evidenceRefs":{"type":"array","items":{"type":"string"}}},"required":["conclusion","uncertainty","evidenceRefs"],"additionalProperties":false}`
const syntheticKey = "synthetic-provider-key-not-real"

func requireOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("operation failed (%T; sensitive details withheld)", err)
	}
}

func check(t *testing.T, condition bool, message string) {
	t.Helper()
	if !condition {
		t.Error(message)
	}
}

func boundedContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	return ctx
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func endpoint(t *testing.T, handler http.HandlerFunc) (string, *http.Client, *atomic.Int32) {
	t.Helper()
	calls := new(atomic.Int32)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) > 4 {
			t.Error("provider exceeded the fixture request ceiling")
			w.WriteHeader(429)
			return
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	client := server.Client()
	client.Timeout = time.Second
	transport := client.Transport
	origin, err := url.Parse(server.URL)
	requireOK(t, err)
	client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host != origin.Host || r.URL.Scheme != origin.Scheme {
			t.Error("provider attempted an unconfigured destination; fixture denied network access")
			return nil, ErrPolicy
		}
		return transport.RoundTrip(r)
	})
	return server.URL, client, calls
}

func at(t *testing.T, value any, path ...string) any {
	t.Helper()
	for _, key := range path {
		object, ok := value.(map[string]any)
		if !ok {
			t.Error("required native JSON object missing")
			return nil
		}
		value = object[key]
	}
	return value
}

func decode(t *testing.T, data []byte) any {
	t.Helper()
	var value any
	check(t, json.Unmarshal(data, &value) == nil, "invalid JSON fixture/request")
	return value
}

func openAssessor(t *testing.T, config ProviderConfig) Assessor {
	t.Helper()
	if Production.OpenAssessor == nil {
		t.Fatal("M11 production binding missing: OpenAssessor; add forwarding-only production_bindings_test.go")
	}
	adapter, err := Production.OpenAssessor(boundedContext(t), config)
	requireOK(t, err)
	if adapter == nil {
		t.Fatal("OpenAssessor returned nil")
	}
	return adapter
}

func openVerifier(t *testing.T, config VerifierConfig) Verifier {
	t.Helper()
	if Production.OpenVerifier == nil {
		t.Fatal("M12 production binding missing: OpenVerifier; add forwarding-only production_bindings_test.go")
	}
	adapter, err := Production.OpenVerifier(boundedContext(t), config)
	requireOK(t, err)
	if adapter == nil {
		t.Fatal("OpenVerifier returned nil")
	}
	return adapter
}
