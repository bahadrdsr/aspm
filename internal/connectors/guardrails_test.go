package connectors

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
)

type testTransport func(*http.Request) (*http.Response, error)

func (f testTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type testCredentials func(context.Context) (aws.Credentials, error)

func (f testCredentials) Retrieve(ctx context.Context) (aws.Credentials, error) { return f(ctx) }

func localAPI(t *testing.T, handler http.HandlerFunc) (string, *http.Client, *atomic.Int32) {
	t.Helper()
	calls := new(atomic.Int32)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) > 16 {
			t.Error("request ceiling exceeded")
			w.WriteHeader(429)
			return
		}
		handler(w, r)
	}))
	t.Cleanup(server.Close)
	client := server.Client()
	client.Timeout = 2 * time.Second
	transport := client.Transport
	origin, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	client.Transport = testTransport(func(r *http.Request) (*http.Response, error) {
		if r.URL.Scheme != origin.Scheme || r.URL.Host != origin.Host {
			t.Error("attempted a destination outside the local fixture")
			return nil, ErrScope
		}
		return transport.RoundTrip(r)
	})
	return server.URL, client, calls
}

func testAction() Action {
	return Action{
		WorkspaceID: "workspace", IntentID: "intent", ApprovalRef: "approved-ref", FindingID: "finding",
		Title: "Synthetic title", Body: "Synthetic context", DeepLink: "https://aspm.invalid/workspaces/workspace/findings/finding",
	}
}

func testDeliveryConfig(profile, base string, client *http.Client) DeliveryConfig {
	if profile == TeamsWorkflows {
		base += "/workflows/synthetic/triggers/manual/paths/invoke?sig=synthetic-secret"
	}
	return DeliveryConfig{
		Profile: profile, Endpoint: base, Client: client, Token: "synthetic-only", WorkspaceID: "workspace",
		Project: "SYN", IssueType: "1", Channel: "C123", Limits: Limits{Requests: 4, Pages: 2, PageSize: 5, Bytes: 65536},
	}
}

func TestFactoriesDoNoIOOrCredentialDiscovery(t *testing.T) {
	var calls atomic.Int32
	client := &http.Client{Transport: testTransport(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("unexpected request")
	})}
	provider := testCredentials(func(context.Context) (aws.Credentials, error) {
		calls.Add(1)
		return aws.Credentials{}, errors.New("unexpected credential retrieval")
	})
	for _, profile := range []string{JiraCloudV3, TeamsWorkflows, SlackWorkspaceBot} {
		adapter, err := OpenDelivery(context.Background(), testDeliveryConfig(profile, "https://api.invalid", client))
		if err != nil || adapter == nil {
			t.Fatalf("opening %s: %v", profile, err)
		}
	}
	for _, profile := range []string{GitHubCloudApp, GitLabArtifacts, ADOArtifacts, AWSEC2SecurityHub, AzureAssessments} {
		adapter, err := OpenCollector(context.Background(), CollectorConfig{
			Profile: profile, Endpoint: "https://api.invalid", FindingsEndpoint: "https://findings.invalid",
			Token: "synthetic-only", Credentials: provider, Client: client,
		})
		if err != nil || adapter == nil {
			t.Fatalf("opening %s: %v", profile, err)
		}
	}
	if calls.Load() != 0 || client.Timeout != 0 || client.CheckRedirect != nil {
		t.Fatal("a factory did I/O or mutated its supplied client")
	}
}

func TestConfigurationAndResourceValidation(t *testing.T) {
	client := &http.Client{Transport: testTransport(func(*http.Request) (*http.Response, error) {
		t.Error("an invalid action attempted a request")
		return nil, ErrScope
	})}
	for _, endpoint := range []string{
		"http://api.invalid", "https://user:pass@api.invalid", "https://api.invalid/#fragment",
		"https://api.invalid/a/../b", "https://api.invalid/a%2fb", "https://api.invalid/a%252fb",
		"https://api.invalid:70000", "https://api.invalid/?secret=value",
	} {
		t.Run(endpoint, func(t *testing.T) {
			if _, err := OpenDelivery(context.Background(), testDeliveryConfig(JiraCloudV3, endpoint, client)); !errors.Is(err, ErrScope) {
				t.Fatalf("invalid endpoint accepted: %v", err)
			}
		})
	}
	for _, field := range []string{"project", "issuetype", "description", "../customfield_1"} {
		config := testDeliveryConfig(JiraCloudV3, "https://api.invalid", client)
		adapter, err := OpenDelivery(context.Background(), config)
		if err != nil {
			t.Fatal(err)
		}
		action := testAction()
		action.Fields = map[string]string{field: "other-scope"}
		if _, err := adapter.Send(context.Background(), action); !errors.Is(err, ErrScope) {
			t.Fatalf("scope-changing field %q accepted: %v", field, err)
		}
	}
	config := testDeliveryConfig(JiraCloudV3, "https://api.invalid", client)
	config.StatusMap = map[string]string{"1": "verified"}
	if _, err := OpenDelivery(context.Background(), config); !errors.Is(err, ErrScope) {
		t.Fatal("verified closure mapping accepted")
	}
	config = testDeliveryConfig(JiraCloudV3, "https://api.invalid", client)
	config.Token = "synthetic\r\nother: token"
	if _, err := OpenDelivery(context.Background(), config); !errors.Is(err, ErrAuth) {
		t.Fatal("invalid credential accepted")
	}
	config = testDeliveryConfig(JiraCloudV3, "https://api.invalid", client)
	config.Limits.Requests = -1
	if _, err := OpenDelivery(context.Background(), config); !errors.Is(err, ErrLimit) {
		t.Fatal("negative request limit accepted")
	}
}

func TestDurableSnapshotsReplayAcrossFreshAdapters(t *testing.T) {
	for _, state := range []string{"confirmed", "accepted", "uncertain"} {
		t.Run(state, func(t *testing.T) {
			var calls atomic.Int32
			client := &http.Client{Transport: testTransport(func(*http.Request) (*http.Response, error) {
				calls.Add(1)
				return nil, errors.New("replay attempted a request")
			})}
			action := testAction()
			action.Prior = &Delivery{
				WorkspaceID: action.WorkspaceID, IntentID: action.IntentID, State: state,
				RemoteID: "native-receipt", MissingFields: []string{"preserved"},
			}
			for range 2 {
				adapter, err := OpenDelivery(context.Background(), testDeliveryConfig(JiraCloudV3, "https://api.invalid", client))
				if err != nil {
					t.Fatal(err)
				}
				result, err := adapter.Send(context.Background(), action)
				if state == "uncertain" && !errors.Is(err, ErrUncertain) || state != "uncertain" && err != nil {
					t.Fatalf("wrong replay error: %v", err)
				}
				if result.RemoteID != action.Prior.RemoteID || result.State != state {
					t.Fatal("replay changed the persisted receipt")
				}
				result.MissingFields[0] = "changed"
				if action.Prior.MissingFields[0] != "preserved" {
					t.Fatal("receipt aliases the caller's durable snapshot")
				}
			}
			if calls.Load() != 0 {
				t.Fatal("durable snapshot replay performed I/O")
			}
			action.Prior.IntentID = "other-intent"
			adapter, err := OpenDelivery(context.Background(), testDeliveryConfig(JiraCloudV3, "https://api.invalid", client))
			if err != nil {
				t.Fatal(err)
			}
			if _, err := adapter.Send(context.Background(), action); !errors.Is(err, ErrScope) {
				t.Fatal("foreign prior intent accepted")
			}
		})
	}
}

func TestAmbiguousAcknowledgementsStayUncertain(t *testing.T) {
	for _, test := range []struct {
		name, profile, body string
		status              int
	}{
		{"jira-invalid-json", JiraCloudV3, `{"key":`, 201},
		{"jira-server-error", JiraCloudV3, `{"code":"AccessDenied"}`, 503},
		{"slack-missing-ok", SlackWorkspaceBot, `{}`, 200},
		{"slack-other-channel", SlackWorkspaceBot, `{"ok":true,"channel":"C999","ts":"123.000001"}`, 200},
		{"teams-unexpected-success", TeamsWorkflows, `{}`, 200},
	} {
		t.Run(test.name, func(t *testing.T) {
			base, client, calls := localAPI(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost {
					t.Error("unexpected non-write request")
				}
				w.WriteHeader(test.status)
				_, _ = io.WriteString(w, test.body)
			})
			config := testDeliveryConfig(test.profile, base, client)
			adapter, err := OpenDelivery(context.Background(), config)
			if err != nil {
				t.Fatal(err)
			}
			action := testAction()
			result, err := adapter.Send(context.Background(), action)
			if !errors.Is(err, ErrUncertain) || result.State != "uncertain" {
				t.Fatalf("ambiguous write was not uncertain: %s %v", result.State, err)
			}
			action.Prior = &result
			reopened, err := OpenDelivery(context.Background(), config)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := reopened.Send(context.Background(), action); !errors.Is(err, ErrUncertain) || calls.Load() != 1 {
				t.Fatal("fresh adapter resent an uncertain durable intent")
			}
		})
	}
}

func TestJiraMetadataPagingAndCaps(t *testing.T) {
	for _, cap := range []string{"complete", "pages", "requests"} {
		t.Run(cap, func(t *testing.T) {
			base, client, calls := localAPI(t, func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.URL.Query().Get("maxResults") != "1" {
					t.Error("unbounded metadata request or side effect")
				}
				start := r.URL.Query().Get("startAt")
				if start != "0" && start != "1" {
					t.Error("invalid metadata cursor")
				}
				_, _ = io.WriteString(w, `{"startAt":`+start+`,"total":2,"values":[{"fieldId":"customfield_`+start+`","required":true,"schema":{"type":"string"}}]}`)
			})
			config := testDeliveryConfig(JiraCloudV3, base, client)
			config.Limits.PageSize = 1
			if cap == "pages" {
				config.Limits.Pages = 1
			}
			if cap == "requests" {
				config.Limits.Requests = 1
			}
			adapter, err := OpenDelivery(context.Background(), config)
			if err != nil {
				t.Fatal(err)
			}
			result, err := adapter.Preview(context.Background(), testAction())
			if cap == "complete" {
				if !errors.Is(err, ErrRequiredFields) || len(result.MissingFields) != 2 || calls.Load() != 2 {
					t.Fatalf("metadata paging lost required fields: %+v %v", result, err)
				}
			} else if !errors.Is(err, ErrLimit) || len(result.MissingFields) != 1 || calls.Load() != 1 {
				t.Fatalf("metadata limit not honored: %+v %v", result, err)
			}
		})
	}
}

func TestErrorCodesBoundsAndRedaction(t *testing.T) {
	base, client, calls := localAPI(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(429)
		_, _ = io.WriteString(w, strings.Repeat("x", 2048))
	})
	target, err := tlsURL(base, false)
	if err != nil {
		t.Fatal(err)
	}
	httpClient, err := newHTTP(client, Limits{Bytes: 32})
	if err != nil {
		t.Fatal(err)
	}
	budget := httpBudget{http: httpClient}
	_, err = budget.do(context.Background(), http.MethodGet, target, nil, nil, nil)
	var detail *Error
	if !errors.Is(err, ErrLimit) || !errors.Is(err, ErrRateLimited) || !errors.As(err, &detail) ||
		detail.Code != "rate_limited" || detail.HTTPStatus != 429 || detail.RetryAfter != 7*time.Second || calls.Load() != 1 {
		t.Fatalf("rate limit/body cap detail was lost: %v", err)
	}
	privateClient := &http.Client{Transport: testTransport(func(*http.Request) (*http.Response, error) {
		return nil, errors.New("transport failure at https://workflow.invalid/?sig=synthetic-secret")
	})}
	adapter, err := OpenDelivery(context.Background(), testDeliveryConfig(TeamsWorkflows, "https://workflow.invalid", privateClient))
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.Send(context.Background(), testAction())
	if !errors.Is(err, ErrUncertain) || strings.Contains(err.Error(), "synthetic-secret") || strings.Contains(err.Error(), "https://") {
		t.Fatal("transport error disclosed a credential URL or lost uncertainty")
	}
	now := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	if retryAfter(now.Add(9*time.Second).Format(http.TimeFormat), now) != 9*time.Second ||
		retryAfter("-1", now) != 0 || retryAfter("9223372036854775807", now) <= 0 {
		t.Fatal("Retry-After date, sign, or overflow handling is incorrect")
	}
}

func TestCancellationBeforeIOKeepsCollectionIdentity(t *testing.T) {
	var calls atomic.Int32
	client := &http.Client{Transport: testTransport(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, errors.New("canceled collection attempted I/O")
	})}
	adapter, err := OpenCollector(context.Background(), CollectorConfig{Profile: GitHubCloudApp, Endpoint: "https://api.invalid", Token: "synthetic", Client: client})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	identity := Identity{"workspace", "source", "run", "scope"}
	result, err := adapter.Collect(ctx, CollectRequest{Identity: identity, Repository: "owner/repo"})
	if !errors.Is(err, context.Canceled) || result.Identity != identity || result.Complete || len(result.Gaps) != 1 || calls.Load() != 0 {
		t.Fatalf("canceled collection lost identity/gap or performed I/O: %v", err)
	}
}
