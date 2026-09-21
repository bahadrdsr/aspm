package providers

import (
	"errors"
	"net/http"
	"time"
)

var (
	ErrPolicy      = errors.New("analysis policy denied")
	ErrAuth        = errors.New("provider authentication denied")
	ErrRateLimited = errors.New("provider rate limited")
	ErrProvider    = errors.New("provider unavailable")
	ErrOutput      = errors.New("invalid or incomplete assessment")
	ErrToolOutput  = errors.New("model tool output refused")
	ErrLimit       = errors.New("analysis limit exceeded")
	ErrCapability  = errors.New("provider capability is not approved")
)

type OutputErrorKind string

const (
	OutputSchema     OutputErrorKind = "schema"
	OutputRefusal    OutputErrorKind = "refusal"
	OutputGrounding  OutputErrorKind = "grounding"
	OutputIncomplete OutputErrorKind = "incomplete"
)

// OutputError classifies rejected structured output without retaining model
// text. Existing callers can still use errors.Is(err, ErrOutput).
type OutputError struct {
	Kind OutputErrorKind
}

func (e *OutputError) Error() string {
	switch e.Kind {
	case OutputRefusal:
		return "assessment refused"
	case OutputGrounding:
		return "assessment references unapproved evidence"
	case OutputIncomplete:
		return "assessment incomplete"
	default:
		return "assessment schema rejected"
	}
}

func (*OutputError) Unwrap() error { return ErrOutput }

type Profile struct {
	ID, Family, Endpoint, Model, Deployment, Revision string
	APIKey                                            string `json:"-"`
	StructuredOutput                                  bool
}

type Policy struct {
	Mode, ApprovalRef, Revision                         string
	Workspaces, Tasks, DataClasses, AllowedDestinations []string
	FallbackProfileIDs                                  []string
}

type Config struct {
	Profiles                     map[string]Profile
	Policy                       Policy
	Client                       *http.Client
	MaxAttempts, MaxOutputTokens int
	MaxResponseBytes             int64
}

type Request struct {
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

type Result struct {
	WorkspaceID, RunID, FindingID, ProfileID, ProfileRevision, PolicyRevision string
	PromptRevision, EvidenceDigest, RequestID, RequestedModel, ReturnedModel  string
	Deployment, StopReason                                                    string
	Assessment                                                                *Assessment
	Usage                                                                     Usage
	RetryAfter                                                                time.Duration
}
