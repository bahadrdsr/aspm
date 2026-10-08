package connectors

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

type genericWebhookDelivery struct{ deliveryBase }

type genericWebhookTrigger struct {
	Kind                  string  `json:"kind"`
	PolicyID              *string `json:"policyId"`
	PolicyRevision        *int64  `json:"policyRevision"`
	FindingChangeRevision *int64  `json:"findingChangeRevision"`
}

type genericWebhookNotification struct {
	Title    string `json:"title"`
	Body     string `json:"body"`
	DeepLink string `json:"deepLink"`
}

type genericWebhookEnvelope struct {
	APIVersion   string                     `json:"apiVersion"`
	Event        string                     `json:"event"`
	DeliveryID   string                     `json:"deliveryId"`
	WorkspaceID  string                     `json:"workspaceId"`
	FindingID    string                     `json:"findingId"`
	Trigger      genericWebhookTrigger      `json:"trigger"`
	Notification genericWebhookNotification `json:"notification"`
}

func validWebhookSecret(value string) bool {
	return len(value) >= 32 && len(value) <= 4096 &&
		strings.TrimSpace(value) == value &&
		!strings.ContainsFunc(value, unicode.IsControl)
}

func canonicalWebhookOrigin(value string) (string, string, error) {
	if len(value) == 0 || len(value) > 16384 || !strings.HasPrefix(value, "https://") ||
		strings.ContainsAny(strings.TrimPrefix(value, "https://"), "/?#%@\\*") ||
		strings.ContainsFunc(value, func(r rune) bool { return r <= ' ' || r >= 0x7f }) {
		return "", "", connectorError(ErrScope)
	}
	target, err := url.Parse(value)
	if err != nil || target.Scheme != "https" || target.Host == "" || target.User != nil ||
		target.Path != "" || target.RawPath != "" || target.RawQuery != "" || target.Fragment != "" ||
		target.Opaque != "" || target.Host != strings.ToLower(target.Host) {
		return "", "", connectorError(ErrScope)
	}
	port := target.Port()
	if port == "" {
		port = "443"
	} else {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 || strconv.Itoa(number) != port {
			return "", "", connectorError(ErrScope)
		}
	}
	return value, strings.ToLower(target.Hostname()) + ":" + port, nil
}

func genericWebhookOrigins(values []string) (map[string]struct{}, error) {
	if len(values) == 0 {
		return nil, connectorError(ErrScope)
	}
	if len(values) > 16 {
		return nil, connectorError(ErrLimit)
	}
	result := make(map[string]struct{}, len(values))
	effective := make(map[string]struct{}, len(values))
	for _, value := range values {
		origin, address, err := canonicalWebhookOrigin(value)
		if err != nil {
			return nil, err
		}
		if _, duplicate := result[origin]; duplicate {
			return nil, connectorError(ErrScope)
		}
		if _, duplicate := effective[address]; duplicate {
			return nil, connectorError(ErrScope)
		}
		result[origin], effective[address] = struct{}{}, struct{}{}
	}
	return result, nil
}

func validateGenericWebhookConfig(config DeliveryConfig, target *url.URL) error {
	origins, err := genericWebhookOrigins(config.AllowedOrigins)
	if err != nil {
		return err
	}
	origin := "https://" + target.Host
	if _, allowed := origins[origin]; !allowed || len(config.Endpoint) > 16384 ||
		target.Scheme != "https" || target.User != nil || target.Opaque != "" ||
		target.RawQuery != "" || target.Fragment != "" || target.Path == "" || target.Path == "/" ||
		target.RawPath != "" || strings.ContainsAny(target.Path, "\\?#") ||
		strings.ContainsFunc(target.Path, unicode.IsControl) {
		return connectorError(ErrScope)
	}
	for _, segment := range strings.Split(target.Path, "/") {
		if segment == "." || segment == ".." {
			return connectorError(ErrScope)
		}
	}
	escaped := strings.ToLower(target.EscapedPath())
	if strings.Contains(escaped, "%2f") || strings.Contains(escaped, "%5c") {
		return connectorError(ErrScope)
	}
	return nil
}

func validDeliveryTrigger(trigger DeliveryTrigger) bool {
	switch trigger.Kind {
	case "manual":
		return trigger.PolicyID == nil && trigger.PolicyRevision == nil &&
			trigger.FindingChangeRevision == nil
	case "notification-policy":
		return trigger.PolicyID != nil && textID(*trigger.PolicyID, 256) &&
			trigger.PolicyRevision != nil && *trigger.PolicyRevision > 0 &&
			trigger.FindingChangeRevision != nil && *trigger.FindingChangeRevision > 0
	default:
		return false
	}
}

func (d *genericWebhookDelivery) Preview(ctx context.Context, action Action) (Preview, error) {
	if err := ctx.Err(); err != nil {
		return Preview{}, connectorError(err)
	}
	if err := d.validateAction(action, false); err != nil {
		return Preview{}, err
	}
	if !validDeliveryTrigger(action.Trigger) {
		return Preview{}, connectorError(ErrScope)
	}
	if strings.TrimSpace(action.Title) == "" {
		return Preview{MissingFields: []string{"summary"}}, connectorError(ErrRequiredFields)
	}
	return Preview{}, nil
}

func (*genericWebhookDelivery) Status(context.Context, string) (StatusLink, error) {
	return StatusLink{}, connectorError(ErrUnsupported)
}

func genericWebhookBody(action Action) ([]byte, error) {
	return json.Marshal(genericWebhookEnvelope{
		APIVersion: "aspm/webhook/v1", Event: "finding.notification",
		DeliveryID: action.IntentID, WorkspaceID: action.WorkspaceID, FindingID: action.FindingID,
		Trigger: genericWebhookTrigger{
			Kind: action.Trigger.Kind, PolicyID: action.Trigger.PolicyID,
			PolicyRevision:        action.Trigger.PolicyRevision,
			FindingChangeRevision: action.Trigger.FindingChangeRevision,
		},
		Notification: genericWebhookNotification{
			Title: action.Title, Body: action.Body, DeepLink: action.DeepLink,
		},
	})
}

func (d *genericWebhookDelivery) Send(ctx context.Context, action Action) (Delivery, error) {
	if action.Prior != nil {
		if action.Prior.WorkspaceID != action.WorkspaceID || action.Prior.IntentID != action.IntentID {
			return Delivery{}, connectorError(ErrScope)
		}
		result := *action.Prior
		result.MissingFields = append([]string(nil), action.Prior.MissingFields...)
		switch result.State {
		case "accepted":
			return result, nil
		case "uncertain":
			return result, connectorError(ErrUncertain)
		case "blocked":
			return result, connectorError(ErrScope)
		case "rate-limited":
			return result, connectorError(ErrRateLimited)
		case "failed":
			return result, connectorError(ErrProtocol)
		default:
			return Delivery{}, connectorError(ErrScope)
		}
	}
	result, finished, err := d.prepare(ctx, action)
	if finished {
		return result, err
	}
	if !validDeliveryTrigger(action.Trigger) {
		return deliveryFailure(result, nativeResponse{}, connectorError(ErrScope))
	}
	body, err := genericWebhookBody(action)
	if err != nil {
		return deliveryFailure(result, nativeResponse{}, connectorError(ErrProtocol))
	}
	mac := hmac.New(sha256.New, []byte(d.config.Token))
	_, _ = mac.Write(body)
	headers := http.Header{
		"Content-Type":       {"application/json"},
		"Accept":             {"application/json"},
		"X-ASPM-Event":       {"finding.notification.v1"},
		"X-ASPM-Delivery-ID": {action.IntentID},
		"X-ASPM-Signature":   {"sha256=" + hex.EncodeToString(mac.Sum(nil))},
	}
	budget := httpBudget{http: d.http}
	response, nativeErr := budget.do(ctx, http.MethodPost, d.base, headers, body, func(request *http.Request) error {
		request.Close = true
		return nil
	})
	if nativeErr == nil {
		result.State = "accepted"
		return result, nil
	}
	switch {
	case response.Status >= 300 && response.Status < 400 || errors.Is(nativeErr, ErrScope):
		result.State = "blocked"
		return result, responseError(response, ErrScope)
	case response.Status == http.StatusTooManyRequests || errors.Is(nativeErr, ErrRateLimited):
		result.State, result.RetryAfter = "rate-limited", responseError(response, ErrRateLimited).RetryAfter
		return result, responseError(response, ErrRateLimited)
	case response.Status >= 400 && response.Status < 500:
		result.State = "failed"
		return result, responseError(response, ErrProtocol)
	default:
		result.State = "uncertain"
		return result, responseError(response, ErrUncertain)
	}
}
