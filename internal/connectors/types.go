// Package connectors implements explicitly scoped native delivery and collection
// profiles. It does not discover credentials, persist intents, or certify scans.
package connectors

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
)

const (
	JiraCloudV3       = "jira-cloud-v3"
	TeamsWorkflows    = "teams-workflows-channel"
	SlackWorkspaceBot = "slack-workspace-bot"
	GitHubCloudApp    = "github-cloud-app"
	GitLabArtifacts   = "gitlab-com-artifacts-v4"
	ADOArtifacts      = "ado-services-build-artifacts"
	AWSEC2SecurityHub = "aws-commercial-ec2-securityhub"
	AzureAssessments  = "azure-public-resourcegraph-assessments"
)

var (
	ErrAuth           = errors.New("connector authentication or permission denied")
	ErrRateLimited    = errors.New("connector rate limited")
	ErrUncertain      = errors.New("delivery acknowledgement uncertain")
	ErrRequiredFields = errors.New("required work-item fields missing")
	ErrScope          = errors.New("connector scope denied")
	ErrLimit          = errors.New("connector collection limit reached")
	ErrUnavailable    = errors.New("source or artifact unavailable")
	ErrProtocol       = errors.New("invalid native connector response")
	ErrUnsupported    = errors.New("unsupported connector operation")
)

// Error deliberately omits response bodies, credentials, and endpoint URLs.
// Code and NativeCode are machine-readable; RetryAfter never causes a sleep.
type Error struct {
	Code, NativeCode string
	HTTPStatus       int
	RetryAfter       time.Duration
	cause            error
}

func (e *Error) Error() string { return "connector: " + e.Code }
func (e *Error) Unwrap() error { return e.cause }

type Limits struct {
	Requests, Pages, PageSize int
	Bytes                     int64
}

type DeliveryConfig struct {
	Profile, Endpoint, Token, WorkspaceID, Project, IssueType, Channel string
	StatusMap                                                          map[string]string
	Client                                                             *http.Client
	Limits                                                             Limits
}

type Action struct {
	WorkspaceID, IntentID, ApprovalRef, FindingID, Title, Body, DeepLink string
	Fields                                                               map[string]string
	// Prior must come from the caller's trusted, serialized durable outbox.
	Prior *Delivery
}

type Preview struct{ MissingFields []string }

type Delivery struct {
	WorkspaceID, IntentID, State, RemoteID, RemoteURL string
	MissingFields                                     []string
	RetryAfter                                        time.Duration
}

type StatusLink struct {
	RemoteID, NativeStatus, LinkedState string
	CloseFinding                        bool
}

type DeliveryAdapter interface {
	Preview(context.Context, Action) (Preview, error)
	Send(context.Context, Action) (Delivery, error)
	Status(context.Context, string) (StatusLink, error)
}

type Identity struct{ WorkspaceID, SourceID, RunID, ScopeID string }

type CollectRequest struct {
	Identity
	Repository, Organization, Project, PipelineID, JobID, BuildID string
	ArtifactName, ArtifactPath, SubscriptionID, AccountID, Region string
}

type CollectorConfig struct {
	Profile, Endpoint, FindingsEndpoint, Token string
	Client                                     *http.Client
	Credentials                                aws.CredentialsProvider
	Limits                                     Limits
}

type Record struct {
	Kind, ExternalID, ParentID, NativeRunID, Severity, State, Location, RawURL string
	Raw                                                                        []byte
	SourceScanAt, SourceUpdatedAt                                              *time.Time
}

type Collection struct {
	Identity
	CollectedAt time.Time
	// Complete describes the selected collection feeds, not scanner execution,
	// vulnerability absence, account-wide inventory, or finding verification.
	Complete     bool
	Records      []Record
	Gaps         []string
	Continuation map[string]string
	RetryAfter   time.Duration
}

type Collector interface {
	Collect(context.Context, CollectRequest) (Collection, error)
}
