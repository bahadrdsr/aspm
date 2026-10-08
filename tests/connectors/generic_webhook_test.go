package connectors

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const genericWebhookProfile = "generic-webhook-v1"

type expectedWebhookTrigger struct {
	Kind                  string  `json:"kind"`
	PolicyID              *string `json:"policyId"`
	PolicyRevision        *int64  `json:"policyRevision"`
	FindingChangeRevision *int64  `json:"findingChangeRevision"`
}

type expectedWebhookNotification struct {
	Title    string `json:"title"`
	Body     string `json:"body"`
	DeepLink string `json:"deepLink"`
}

type expectedWebhookEnvelope struct {
	APIVersion   string                      `json:"apiVersion"`
	Event        string                      `json:"event"`
	DeliveryID   string                      `json:"deliveryId"`
	WorkspaceID  string                      `json:"workspaceId"`
	FindingID    string                      `json:"findingId"`
	Trigger      expectedWebhookTrigger      `json:"trigger"`
	Notification expectedWebhookNotification `json:"notification"`
}

func webhookConfig(endpoint, origin, secret string, client *http.Client) DeliveryConfig {
	return DeliveryConfig{
		Profile: genericWebhookProfile, Endpoint: endpoint, Token: secret,
		WorkspaceID: "synthetic-workspace", Client: client,
		Limits:         Limits{Requests: 1, Pages: 1, PageSize: 1, Bytes: 64 << 10},
		AllowedOrigins: []string{origin},
	}
}

func webhookAction(trigger DeliveryTrigger) Action {
	return Action{
		WorkspaceID: "synthetic-workspace", IntentID: "synthetic-delivery-id",
		ApprovalRef: "aspm:remediation:synthetic-workspace:synthetic-delivery-id",
		FindingID:   "synthetic-finding", Title: `Synthetic "finding"`,
		Body:     "Severity: high\nAsset: synthetic-repository\nChange: changed",
		DeepLink: "https://aspm.invalid/#/work?finding=synthetic-finding",
		Fields: map[string]string{
			"evidence": "SYNTHETIC-EVIDENCE-MUST-NOT-BE-SENT",
			"notes":    "SYNTHETIC-NOTE-MUST-NOT-BE-SENT",
		},
		Trigger: trigger,
	}
}

func openGenericWebhookDelivery(t *testing.T, config DeliveryConfig) DeliveryAdapter {
	t.Helper()
	if Production.OpenDelivery == nil {
		t.Fatal("production delivery binding missing")
	}
	adapter, err := Production.OpenDelivery(boundedContext(t), config)
	if errors.Is(err, ErrUnsupported) {
		t.Fatal("production delivery adapter does not implement generic-webhook-v1")
	}
	requireOK(t, err)
	if adapter == nil {
		t.Fatal("generic-webhook-v1 delivery adapter returned nil")
	}
	return adapter
}

func expectedWebhookBytes(t *testing.T, action Action) []byte {
	t.Helper()
	body, err := json.Marshal(expectedWebhookEnvelope{
		APIVersion: "aspm/webhook/v1", Event: "finding.notification",
		DeliveryID: action.IntentID, WorkspaceID: action.WorkspaceID, FindingID: action.FindingID,
		Trigger: expectedWebhookTrigger{
			Kind: action.Trigger.Kind, PolicyID: action.Trigger.PolicyID,
			PolicyRevision:        action.Trigger.PolicyRevision,
			FindingChangeRevision: action.Trigger.FindingChangeRevision,
		},
		Notification: expectedWebhookNotification{
			Title: action.Title, Body: action.Body, DeepLink: action.DeepLink,
		},
	})
	requireOK(t, err)
	return body
}

func TestM08GenericWebhookExactBodyHeadersAndHMAC(t *testing.T) {
	secret := "0123456789abcdef0123456789abcdef"
	for _, row := range []struct {
		name    string
		trigger DeliveryTrigger
	}{
		{name: "manual", trigger: DeliveryTrigger{Kind: "manual"}},
		{name: "notification-policy", trigger: func() DeliveryTrigger {
			policyID, policyRevision, findingRevision := "policy-1", int64(3), int64(9)
			return DeliveryTrigger{
				Kind: "notification-policy", PolicyID: &policyID,
				PolicyRevision: &policyRevision, FindingChangeRevision: &findingRevision,
			}
		}()},
	} {
		t.Run(row.name, func(t *testing.T) {
			action := webhookAction(row.trigger)
			wantBody := expectedWebhookBytes(t, action)
			var received []byte
			base, client, calls := endpoint(t, func(w http.ResponseWriter, r *http.Request) {
				check(t, r.Method == http.MethodPost && r.URL.Path == "/owned/aspm" && r.URL.RawQuery == "",
					"generic webhook changed the configured method, path or query")
				check(t, r.Header.Get("Content-Type") == "application/json" &&
					r.Header.Get("Accept") == "application/json",
					"generic webhook omitted fixed JSON content negotiation")
				check(t, r.Header.Get("X-ASPM-Event") == "finding.notification.v1" &&
					r.Header.Get("X-ASPM-Delivery-ID") == action.IntentID,
					"generic webhook omitted fixed event or durable delivery identity")
				check(t, r.Header.Get("Authorization") == "" && r.Header.Get("Cookie") == "",
					"generic webhook emitted authorization or cookie credentials")
				for name := range r.Header {
					if strings.HasPrefix(strings.ToLower(name), "x-") &&
						name != "X-Aspm-Event" && name != "X-Aspm-Delivery-Id" && name != "X-Aspm-Signature" {
						t.Errorf("generic webhook emitted undeclared application header %s", name)
					}
				}
				data, err := io.ReadAll(io.LimitReader(r.Body, (64<<10)+1))
				requireOK(t, err)
				received = bytes.Clone(data)
				check(t, bytes.Equal(data, wantBody), "generic webhook body bytes differ from the fixed envelope")
				mac := hmac.New(sha256.New, []byte(secret))
				_, _ = mac.Write(data)
				wantSignature := "sha256=" + hex.EncodeToString(mac.Sum(nil))
				check(t, r.Header.Get("X-ASPM-Signature") == wantSignature,
					"generic webhook signature is not lowercase HMAC-SHA256 over the exact body")
				check(t, !bytes.Contains(data, []byte("SYNTHETIC-EVIDENCE")) &&
					!bytes.Contains(data, []byte("SYNTHETIC-NOTE")),
					"generic webhook included unapproved finding fields")
				w.WriteHeader(http.StatusNoContent)
			})
			adapter := openGenericWebhookDelivery(t, webhookConfig(base+"/owned/aspm", base, secret, client))
			result, err := adapter.Send(boundedContext(t), action)
			requireOK(t, err)
			check(t, result.State == "accepted" && result.RemoteID == "" && result.RemoteURL == "",
				"HTTP 2xx must mean endpoint accepted without a remote receipt")
			_, statusErr := adapter.Status(boundedContext(t), action.IntentID)
			check(t, errors.Is(statusErr, ErrUnsupported),
				"generic webhook exposed provider status linkage")
			check(t, calls.Load() == 1 && bytes.Equal(received, wantBody),
				"generic webhook did not perform exactly one stable POST")
		})
	}
}

func TestM08GenericWebhookResponseMappingAndNoBlindRetry(t *testing.T) {
	secret := "0123456789abcdef0123456789abcdef"
	for _, row := range []struct {
		name, state string
		status      int
		retry       string
		want        error
	}{
		{name: "accepted-200", status: 200, state: "accepted"},
		{name: "accepted-299", status: 299, state: "accepted"},
		{name: "client-400", status: 400, state: "failed", want: ErrProtocol},
		{name: "client-404", status: 404, state: "failed", want: ErrProtocol},
		{name: "rate-limited", status: 429, retry: "7", state: "rate-limited", want: ErrRateLimited},
		{name: "server-500", status: 500, state: "uncertain", want: ErrUncertain},
		{name: "server-503", status: 503, state: "uncertain", want: ErrUncertain},
		{name: "dropped-ack", status: 0, state: "uncertain", want: ErrUncertain},
		{name: "redirect", status: 307, state: "blocked", want: ErrScope},
	} {
		t.Run(row.name, func(t *testing.T) {
			base, client, calls := endpoint(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = io.Copy(io.Discard, r.Body)
				if row.status == 0 {
					connection, _, err := w.(http.Hijacker).Hijack()
					requireOK(t, err)
					_ = connection.Close()
					return
				}
				if row.retry != "" {
					w.Header().Set("Retry-After", row.retry)
				}
				if row.status == http.StatusTemporaryRedirect {
					w.Header().Set("Location", baseURLForRedirect(r)+"/credential-trap")
				}
				w.WriteHeader(row.status)
			})
			adapter := openGenericWebhookDelivery(t, webhookConfig(base+"/outbound", base, secret, client))
			result, err := adapter.Send(boundedContext(t), webhookAction(DeliveryTrigger{Kind: "manual"}))
			check(t, result.State == row.state, "generic webhook response mapped to the wrong durable state")
			if row.want == nil {
				requireOK(t, err)
			} else {
				check(t, errors.Is(err, row.want), "generic webhook response lost its bounded error class")
			}
			if row.status == 429 {
				check(t, result.RetryAfter == 7*time.Second, "generic webhook lost literal bounded Retry-After")
			}
			action := webhookAction(DeliveryTrigger{Kind: "manual"})
			action.Prior = &result
			_, replayErr := adapter.Send(boundedContext(t), action)
			if row.state == "accepted" {
				requireOK(t, replayErr)
			} else {
				check(t, replayErr != nil, "terminal generic webhook outcome became eligible for blind retry")
			}
			check(t, calls.Load() == 1, "generic webhook automatically retried or followed a redirect")
		})
	}
}

func baseURLForRedirect(r *http.Request) string {
	return "https://" + r.Host
}

func TestM08GenericWebhookAllowlistAndTriggerDenialsBeforeIO(t *testing.T) {
	secret := "0123456789abcdef0123456789abcdef"
	base, client, calls := endpoint(t, func(w http.ResponseWriter, _ *http.Request) {
		t.Error("invalid generic webhook configuration reached HTTP")
		w.WriteHeader(http.StatusNoContent)
	})
	other, err := url.Parse(base)
	requireOK(t, err)
	other.Host = "127.0.0.1:1"
	for _, row := range []struct {
		name   string
		config DeliveryConfig
	}{
		{name: "unapproved-origin", config: webhookConfig(other.String()+"/outbound", base, secret, client)},
		{name: "http-downgrade", config: webhookConfig(strings.Replace(base, "https:", "http:", 1)+"/outbound", base, secret, client)},
		{name: "query-secret", config: webhookConfig(base+"/outbound?sig=forbidden", base, secret, client)},
		{name: "fragment", config: webhookConfig(base+"/outbound#forbidden", base, secret, client)},
		{name: "dot-segment", config: webhookConfig(base+"/owned/../outbound", base, secret, client)},
		{name: "encoded-separator", config: webhookConfig(base+"/owned%2foutbound", base, secret, client)},
		{name: "empty-allowlist", config: webhookConfig(base+"/outbound", base, secret, client)},
		{name: "duplicate-effective-port", config: func() DeliveryConfig {
			config := webhookConfig("https://receiver.invalid/outbound",
				"https://receiver.invalid", secret, client)
			config.AllowedOrigins = []string{"https://receiver.invalid", "https://receiver.invalid:443"}
			return config
		}()},
		{name: "too-many-origins", config: func() DeliveryConfig {
			config := webhookConfig(base+"/outbound", base, secret, client)
			config.AllowedOrigins = make([]string, 17)
			for index := range config.AllowedOrigins {
				config.AllowedOrigins[index] = "https://allowed-" + string(rune('a'+index)) + ".invalid"
			}
			return config
		}()},
	} {
		t.Run(row.name, func(t *testing.T) {
			if row.name == "empty-allowlist" {
				row.config.AllowedOrigins = nil
			}
			adapter, err := Production.OpenDelivery(boundedContext(t), row.config)
			if err == nil && adapter != nil {
				_, err = adapter.Send(boundedContext(t), webhookAction(DeliveryTrigger{Kind: "manual"}))
			}
			check(t, errors.Is(err, ErrScope) || errors.Is(err, ErrAuth) || errors.Is(err, ErrLimit),
				"invalid generic webhook origin/configuration did not fail closed")
		})
	}

	policyID, revision := "policy-1", int64(1)
	for _, trigger := range []DeliveryTrigger{
		{Kind: "manual", PolicyID: &policyID},
		{Kind: "notification-policy"},
		{Kind: "notification-policy", PolicyID: &policyID, PolicyRevision: &revision},
		{Kind: "other"},
	} {
		adapter, err := Production.OpenDelivery(boundedContext(t),
			webhookConfig(base+"/outbound", base, secret, client))
		if errors.Is(err, ErrUnsupported) {
			t.Fatal("production delivery adapter does not implement generic-webhook-v1")
		}
		requireOK(t, err)
		_, err = adapter.Send(boundedContext(t), webhookAction(trigger))
		check(t, errors.Is(err, ErrScope), "invalid generic webhook trigger reached the receiver")
	}
	check(t, calls.Load() == 0, "allowlist or trigger denial performed HTTP")
}

func TestM08GenericWebhookCancellationAndMalformedTransportAreUncertain(t *testing.T) {
	secret := "0123456789abcdef0123456789abcdef"
	entered := make(chan struct{}, 1)
	cancelled := make(chan struct{}, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(io.LimitReader(r.Body, (64<<10)+1))
		check(t, err == nil && len(data) <= 64<<10,
			"held generic webhook receiver could not consume the bounded request body")
		check(t, r.Body.Close() == nil, "held generic webhook receiver could not close the request body")
		entered <- struct{}{}
		<-r.Context().Done()
		cancelled <- struct{}{}
	}))
	t.Cleanup(server.Close)
	client := server.Client()
	client.Timeout = 2 * time.Second
	adapter := openGenericWebhookDelivery(t, webhookConfig(server.URL+"/held", server.URL, secret, client))
	ctx, cancel := context.WithCancel(boundedContext(t))
	done := make(chan struct {
		result Delivery
		err    error
	}, 1)
	go func() {
		result, err := adapter.Send(ctx, webhookAction(DeliveryTrigger{Kind: "manual"}))
		done <- struct {
			result Delivery
			err    error
		}{result, err}
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("generic webhook did not reach the held synthetic receiver")
	}
	cancel()
	select {
	case result := <-done:
		check(t, result.result.State == "uncertain" && errors.Is(result.err, ErrUncertain),
			"canceled possible generic webhook POST was not uncertain")
	case <-time.After(time.Second):
		t.Fatal("generic webhook did not honor request cancellation")
	}
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("synthetic generic webhook receiver did not observe request context cancellation")
	}

	var calls atomic.Int32
	malformed := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls.Add(1)
		return nil, nil
	}), Timeout: time.Second}
	adapter = openGenericWebhookDelivery(t, webhookConfig("https://receiver.invalid/outbound",
		"https://receiver.invalid", secret, malformed))
	result, err := adapter.Send(boundedContext(t), webhookAction(DeliveryTrigger{Kind: "manual"}))
	check(t, result.State == "uncertain" && errors.Is(err, ErrUncertain) && calls.Load() == 1,
		"malformed transport result was not a single uncertain attempt")
}

func TestM08GenericWebhookSignatureIsStableForImmutableDelivery(t *testing.T) {
	secret := "0123456789abcdef0123456789abcdef"
	var mu sync.Mutex
	var bodies, signatures []string
	base, client, calls := endpoint(t, func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		requireOK(t, err)
		mu.Lock()
		bodies = append(bodies, string(data))
		signatures = append(signatures, r.Header.Get("X-ASPM-Signature"))
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	})
	action := webhookAction(DeliveryTrigger{Kind: "manual"})
	for index := 0; index < 2; index++ {
		adapter := openGenericWebhookDelivery(t, webhookConfig(base+"/stable", base, secret, client))
		result, err := adapter.Send(boundedContext(t), action)
		requireOK(t, err)
		check(t, result.State == "accepted", "stable generic webhook send was not accepted")
	}
	check(t, calls.Load() == 2, "stability fixture did not observe two independent exact sends")
	mu.Lock()
	defer mu.Unlock()
	check(t, len(bodies) == 2 && bodies[0] == bodies[1] &&
		len(signatures) == 2 && signatures[0] == signatures[1],
		"immutable generic webhook body or signature changed between equivalent dispatches")
}
