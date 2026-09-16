package connectors

import (
	"context"
	"encoding/json"
	"net/http"
	"regexp"
	"strings"
	"unicode/utf8"
)

type notificationDelivery struct{ deliveryBase }

func (d *notificationDelivery) Preview(ctx context.Context, action Action) (Preview, error) {
	if err := ctx.Err(); err != nil {
		return Preview{}, connectorError(err)
	}
	if err := d.validateAction(action, false); err != nil {
		return Preview{}, err
	}
	if strings.TrimSpace(action.Title) == "" {
		return Preview{MissingFields: []string{"summary"}}, connectorError(ErrRequiredFields)
	}
	return Preview{}, nil
}

func (*notificationDelivery) Status(context.Context, string) (StatusLink, error) {
	return StatusLink{}, connectorError(ErrUnsupported)
}

type cardText struct {
	Type   string `json:"type"`
	Text   string `json:"text"`
	Wrap   bool   `json:"wrap"`
	Weight string `json:"weight,omitempty"`
}

type cardAction struct {
	Type  string `json:"type"`
	Title string `json:"title"`
	URL   string `json:"url"`
}

type adaptiveCard struct {
	Type    string       `json:"type"`
	Version string       `json:"version"`
	Body    []cardText   `json:"body"`
	Actions []cardAction `json:"actions"`
}

type cardAttachment struct {
	ContentType string       `json:"contentType"`
	ContentURL  *string      `json:"contentUrl"`
	Content     adaptiveCard `json:"content"`
}

type slackText struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type slackButton struct {
	Type string    `json:"type"`
	Text slackText `json:"text"`
	URL  string    `json:"url"`
}

type slackBlock struct {
	Type     string        `json:"type"`
	Text     *slackText    `json:"text,omitempty"`
	Elements []slackButton `json:"elements,omitempty"`
}

var slackTimestamp = regexp.MustCompile(`^[0-9]{1,20}\.[0-9]{1,10}$`)

func (d *notificationDelivery) Send(ctx context.Context, action Action) (Delivery, error) {
	result, finished, err := d.prepare(ctx, action)
	if finished {
		return result, err
	}
	target := d.base
	headers := http.Header{"Content-Type": {"application/json"}, "Accept": {"application/json"}}
	var payload any
	if d.config.Profile == TeamsWorkflows {
		payload = struct {
			Type        string           `json:"type"`
			Attachments []cardAttachment `json:"attachments"`
		}{
			Type: "message",
			Attachments: []cardAttachment{{
				ContentType: "application/vnd.microsoft.card.adaptive",
				Content: adaptiveCard{
					Type: "AdaptiveCard", Version: "1.4",
					Body: []cardText{
						{Type: "TextBlock", Text: action.Title, Wrap: true, Weight: "Bolder"},
						{Type: "TextBlock", Text: action.Body, Wrap: true},
					},
					Actions: []cardAction{{Type: "Action.OpenUrl", Title: "View finding", URL: action.DeepLink}},
				},
			}},
		}
	} else {
		text := action.Title + "\n" + action.Body
		if utf8.RuneCountInString(text) > 3000 {
			return deliveryFailure(result, nativeResponse{}, connectorError(ErrLimit))
		}
		target = apiURL(d.base, "api", "chat.postMessage")
		headers = jsonHeaders(d.config.Token)
		payload = struct {
			Channel     string       `json:"channel"`
			Text        string       `json:"text"`
			Blocks      []slackBlock `json:"blocks"`
			UnfurlLinks bool         `json:"unfurl_links"`
			UnfurlMedia bool         `json:"unfurl_media"`
			Parse       string       `json:"parse"`
		}{
			Channel: d.config.Channel, Text: text + "\n" + action.DeepLink, Parse: "none",
			Blocks: []slackBlock{
				{Type: "section", Text: &slackText{Type: "plain_text", Text: text}},
				{Type: "actions", Elements: []slackButton{{Type: "button", Text: slackText{Type: "plain_text", Text: "View finding"}, URL: action.DeepLink}}},
			},
		}
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return deliveryFailure(result, nativeResponse{}, connectorError(ErrProtocol))
	}
	if d.config.Profile == TeamsWorkflows && len(body) > 28*1024 {
		return deliveryFailure(result, nativeResponse{}, connectorError(ErrLimit))
	}
	budget := httpBudget{http: d.http}
	response, err := budget.do(ctx, http.MethodPost, target, headers, body, nil)
	if err != nil {
		return deliveryFailure(result, response, err)
	}
	if d.config.Profile == TeamsWorkflows {
		if response.Status != http.StatusAccepted {
			return deliveryFailure(result, response, connectorError(ErrProtocol))
		}
		result.State = "accepted"
		return result, nil
	}
	var receipt struct {
		OK      *bool  `json:"ok"`
		Error   string `json:"error"`
		Channel string `json:"channel"`
		TS      string `json:"ts"`
	}
	if response.Status != http.StatusOK || json.Unmarshal(response.Body, &receipt) != nil || receipt.OK == nil {
		return deliveryFailure(result, response, connectorError(ErrProtocol))
	}
	if !*receipt.OK {
		cause := ErrProtocol
		switch receipt.Error {
		case "not_authed", "invalid_auth", "account_inactive", "token_revoked", "token_expired", "missing_scope", "no_permission", "not_allowed_token_type", "org_login_required", "team_access_not_granted":
			cause = ErrAuth
		case "ratelimited", "rate_limited":
			cause = ErrRateLimited
		case "channel_not_found", "is_archived":
			cause = ErrUnavailable
		case "internal_error", "fatal_error", "request_timeout", "service_unavailable":
			return deliveryFailure(result, response, responseError(response, ErrUncertain))
		}
		failure := responseError(response, cause)
		result.State, result.RetryAfter = "failed", failure.RetryAfter
		if cause == ErrRateLimited {
			result.State = "rate-limited"
		}
		return result, failure
	}
	if receipt.Channel != d.config.Channel || !slackTimestamp.MatchString(receipt.TS) {
		return deliveryFailure(result, response, connectorError(ErrProtocol))
	}
	result.State, result.RemoteID = "confirmed", receipt.Channel+":"+receipt.TS
	return result, nil
}
