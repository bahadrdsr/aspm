package connectors

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

	"github.com/aws/aws-sdk-go-v2/aws"
)

var (
	ErrAuth           = errors.New("connector authentication or permission denied")
	ErrRateLimited    = errors.New("connector rate limited")
	ErrUncertain      = errors.New("delivery acknowledgement uncertain")
	ErrRequiredFields = errors.New("required work-item fields missing")
	ErrScope          = errors.New("connector scope denied")
	ErrLimit          = errors.New("connector collection limit reached")
	ErrUnavailable    = errors.New("source or artifact unavailable")
)

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
	Prior                                                                *Delivery
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
	CollectedAt  time.Time
	Complete     bool
	Records      []Record
	Gaps         []string
	Continuation map[string]string
	RetryAfter   time.Duration
}

type Collector interface {
	Collect(context.Context, CollectRequest) (Collection, error)
}

var Production struct {
	OpenDelivery  func(context.Context, DeliveryConfig) (DeliveryAdapter, error)
	OpenCollector func(context.Context, CollectorConfig) (Collector, error)
}

const syntheticToken = "synthetic-not-a-real-credential"

func boundedContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func requireOK(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("operation failed (%T; response/credential details withheld)", err)
	}
}

func check(t *testing.T, condition bool, message string) {
	t.Helper()
	if !condition {
		t.Error(message)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func endpoint(t *testing.T, handler http.HandlerFunc) (string, *http.Client, *atomic.Int32) {
	t.Helper()
	calls := new(atomic.Int32)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) > 24 {
			t.Error("adapter exceeded fixture's hard request ceiling")
			w.WriteHeader(http.StatusTooManyRequests)
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
		if r.URL.Scheme != origin.Scheme || r.URL.Host != origin.Host {
			t.Error("adapter attempted an unconfigured destination; fixture blocked network access")
			return nil, ErrScope
		}
		return transport.RoundTrip(r)
	})
	return server.URL, client, calls
}

func jsonBody(t *testing.T, r *http.Request) any {
	t.Helper()
	defer r.Body.Close()
	data, err := io.ReadAll(io.LimitReader(r.Body, 65537))
	check(t, err == nil && len(data) <= 65536, "request body exceeded fixture bounds")
	var result any
	check(t, json.Unmarshal(data, &result) == nil, "request body is not JSON")
	return result
}

func at(t *testing.T, value any, path ...any) any {
	t.Helper()
	for _, key := range path {
		switch key := key.(type) {
		case string:
			object, ok := value.(map[string]any)
			if !ok {
				t.Error("missing JSON object on required native payload path")
				return nil
			}
			value = object[key]
		case int:
			array, ok := value.([]any)
			if !ok || key < 0 || key >= len(array) {
				t.Error("missing JSON array item on required native payload path")
				return nil
			}
			value = array[key]
		}
	}
	return value
}

func reply(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func openDelivery(t *testing.T, config DeliveryConfig) DeliveryAdapter {
	t.Helper()
	if Production.OpenDelivery == nil {
		t.Fatal("M08 production binding missing: OpenDelivery; add forwarding-only production_bindings_test.go")
	}
	adapter, err := Production.OpenDelivery(boundedContext(t), config)
	requireOK(t, err)
	if adapter == nil {
		t.Fatal("OpenDelivery returned nil")
	}
	return adapter
}

func openCollector(t *testing.T, config CollectorConfig) Collector {
	t.Helper()
	if Production.OpenCollector == nil {
		t.Fatal("M09 production binding missing: OpenCollector; add forwarding-only production_bindings_test.go")
	}
	adapter, err := Production.OpenCollector(boundedContext(t), config)
	requireOK(t, err)
	if adapter == nil {
		t.Fatal("OpenCollector returned nil")
	}
	return adapter
}

func collectionRequest() CollectRequest {
	return CollectRequest{
		Identity:   Identity{"synthetic-workspace", "synthetic-source", "synthetic-run", "synthetic-scope"},
		Repository: "owner/repo", Organization: "org", Project: "31", PipelineID: "7", JobID: "9", BuildID: "81",
		ArtifactName: "aspm-report", ArtifactPath: "reports/report.sarif",
		SubscriptionID: "11111111-1111-4111-8111-111111111111", AccountID: "123456789012", Region: "us-east-1",
	}
}

func collectorConfig(profile, base string, client *http.Client) CollectorConfig {
	return CollectorConfig{Profile: profile, Endpoint: base, FindingsEndpoint: base, Token: syntheticToken,
		Client: client, Limits: Limits{Requests: 8, Pages: 2, PageSize: 5, Bytes: 65536}}
}

func collected(t *testing.T, adapter Collector, request CollectRequest) Collection {
	t.Helper()
	before := time.Now()
	result, err := adapter.Collect(boundedContext(t), request)
	requireOK(t, err)
	check(t, result.Identity == request.Identity, "collection changed workspace/source/run/scope identity")
	check(t, !result.CollectedAt.Before(before) && !result.CollectedAt.After(time.Now()), "collection time is not current collection time")
	check(t, result.Complete && len(result.Gaps) == 0, "successful bounded scope must be explicitly complete")
	return result
}

func record(t *testing.T, result Collection, kind, id string) Record {
	t.Helper()
	for _, item := range result.Records {
		if item.Kind == kind && item.ExternalID == id {
			check(t, len(item.Raw) > 0 && item.RawURL != "", "native record lost raw report/reference")
			return item
		}
	}
	t.Fatalf("missing native %s record %s", kind, id)
	return Record{}
}
