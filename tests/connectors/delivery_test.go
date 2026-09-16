package connectors

import (
	"errors"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func actionFixture() Action {
	return Action{
		WorkspaceID: "synthetic-workspace", IntentID: "synthetic-intent", ApprovalRef: "synthetic-approved-action",
		FindingID: "finding-1", Title: "Synthetic finding", Body: "Synthetic remediation context only.",
		DeepLink: "https://aspm.invalid/workspaces/synthetic-workspace/findings/finding-1",
		Fields:   map[string]string{"customfield_10010": "Synthetic owner"},
	}
}

func deliveryConfig(profile, base string, client *http.Client) DeliveryConfig {
	return DeliveryConfig{Profile: profile, Endpoint: base, Client: client, Token: syntheticToken,
		WorkspaceID: "synthetic-workspace", Project: "SYN", IssueType: "10001", Channel: "C123",
		StatusMap: map[string]string{"10002": "pending-retest"}, Limits: Limits{Requests: 4, Pages: 2, PageSize: 5, Bytes: 65536}}
}

func TestM08JiraNativeFieldsCreateAndStatus(t *testing.T) {
	var posts atomic.Int32
	action := actionFixture()
	base, client, _ := endpoint(t, func(w http.ResponseWriter, r *http.Request) {
		check(t, r.Header.Get("Authorization") == "Bearer "+syntheticToken, "Jira OAuth bearer mapping missing")
		switch r.Method + " " + r.URL.Path {
		case "GET /rest/api/3/issue/createmeta/SYN/issuetypes/10001":
			check(t, r.URL.Query().Get("maxResults") == "5", "Jira metadata page is not bounded")
			reply(w, 200, `{"startAt":0,"maxResults":5,"total":1,"fields":[{"fieldId":"customfield_10010","required":true,"hasDefaultValue":false,"name":"Synthetic owner","schema":{"type":"string"}}]}`)
		case "POST /rest/api/3/issue":
			posts.Add(1)
			body := jsonBody(t, r)
			check(t, at(t, body, "fields", "project", "key") == "SYN" && at(t, body, "fields", "issuetype", "id") == "10001", "Jira project/type mapping missing")
			check(t, at(t, body, "fields", "summary") == action.Title && at(t, body, "fields", "customfield_10010") == action.Fields["customfield_10010"], "Jira native field mapping missing")
			check(t, at(t, body, "fields", "description", "type") == "doc" && at(t, body, "fields", "description", "version") == float64(1), "Jira description must be ADF, not generic webhook text")
			check(t, at(t, body, "fields", "description", "content", 0, "content", 0, "text") == action.Body, "Jira ADF lost remediation context")
			check(t, at(t, body, "fields", "description", "content", 1, "content", 0, "marks", 0, "attrs", "href") == action.DeepLink, "Jira ADF lost scoped deep link")
			reply(w, 201, `{"id":"42","key":"SYN-42","self":"https://jira.invalid/rest/api/3/issue/42"}`)
		case "GET /rest/api/3/issue/SYN-42":
			check(t, r.URL.Query().Get("fields") == "status", "status lookup must request only status")
			reply(w, 200, `{"key":"SYN-42","fields":{"status":{"id":"10002","name":"Done","statusCategory":{"key":"done"}}}}`)
		default:
			t.Error("unexpected Jira route or side effect")
			w.WriteHeader(404)
		}
	})
	adapter := openDelivery(t, deliveryConfig("jira-cloud-v3", base, client))
	missing := action
	missing.Fields = nil
	preview, err := adapter.Preview(boundedContext(t), missing)
	check(t, errors.Is(err, ErrRequiredFields) && len(preview.MissingFields) == 1 && preview.MissingFields[0] == "customfield_10010", "native required-field preview must identify the missing field")
	check(t, posts.Load() == 0, "preview created an external issue")
	_, err = adapter.Preview(boundedContext(t), action)
	requireOK(t, err)
	sent, err := adapter.Send(boundedContext(t), action)
	requireOK(t, err)
	check(t, sent.State == "confirmed" && sent.RemoteID == "SYN-42" && sent.IntentID == action.IntentID && sent.WorkspaceID == action.WorkspaceID, "Jira receipt lost native or local identity")
	action.Prior = &sent
	replayed, err := adapter.Send(boundedContext(t), action)
	requireOK(t, err)
	check(t, posts.Load() == 1 && replayed.RemoteID == sent.RemoteID, "confirmed outbox retry duplicated issue creation")
	status, err := adapter.Status(boundedContext(t), sent.RemoteID)
	requireOK(t, err)
	check(t, status.RemoteID == "SYN-42" && status.NativeStatus == "10002" && status.LinkedState == "pending-retest" && !status.CloseFinding, "Jira status mapping must not imply verified finding closure")
}

func TestM08TeamsAndSlackNativePayloads(t *testing.T) {
	for _, profile := range []string{"teams-workflows-channel", "slack-workspace-bot"} {
		t.Run(profile, func(t *testing.T) {
			action := actionFixture()
			base, client, calls := endpoint(t, func(w http.ResponseWriter, r *http.Request) {
				check(t, r.Method == "POST", "notification must use the declared native POST")
				body := jsonBody(t, r)
				if profile == "teams-workflows-channel" {
					check(t, r.URL.Path == "/workflows/synthetic/triggers/manual/paths/invoke" && r.URL.Query().Get("sig") == "synthetic-only", "Teams must preserve its configured Workflow URL")
					check(t, r.Header.Get("Authorization") == "", "Workflow URL auth must not receive another connector's token")
					check(t, at(t, body, "type") == "message" && at(t, body, "attachments", 0, "contentType") == "application/vnd.microsoft.card.adaptive", "Teams native Adaptive Card envelope missing")
					check(t, at(t, body, "attachments", 0, "content", "type") == "AdaptiveCard" && at(t, body, "attachments", 0, "content", "version") == "1.4", "Teams reviewed card profile missing")
					check(t, at(t, body, "attachments", 0, "content", "body", 0, "type") == "TextBlock" && at(t, body, "attachments", 0, "content", "body", 0, "text") == action.Title && at(t, body, "attachments", 0, "content", "body", 1, "text") == action.Body, "Teams card lost notification context")
					check(t, at(t, body, "attachments", 0, "content", "actions", 0, "type") == "Action.OpenUrl" && at(t, body, "attachments", 0, "content", "actions", 0, "url") == action.DeepLink, "Teams notification lost its deep link")
					w.WriteHeader(202)
				} else {
					check(t, r.URL.Path == "/api/chat.postMessage" && r.Header.Get("Authorization") == "Bearer "+syntheticToken, "Slack must use bot-authenticated chat.postMessage")
					text, ok := at(t, body, "text").(string)
					check(t, at(t, body, "channel") == "C123" && ok && strings.Contains(text, action.Title), "Slack lost channel or accessible fallback text")
					check(t, at(t, body, "blocks", 0, "type") == "section" && at(t, body, "blocks", 1, "type") == "actions" && at(t, body, "blocks", 1, "elements", 0, "type") == "button" && at(t, body, "blocks", 1, "elements", 0, "url") == action.DeepLink, "Slack Block Kit deep-link payload missing")
					check(t, at(t, body, "blocks", 1, "elements", 0, "text", "type") == "plain_text", "Slack link button needs native plain-text labeling")
					check(t, at(t, body, "unfurl_links") == false && at(t, body, "unfurl_media") == false, "Slack must not automatically unfurl evidence links")
					reply(w, 200, `{"ok":true,"channel":"C123","ts":"1720000000.000001"}`)
				}
			})
			config := deliveryConfig(profile, base, client)
			wantState := "confirmed"
			if profile == "teams-workflows-channel" {
				config.Endpoint += "/workflows/synthetic/triggers/manual/paths/invoke?sig=synthetic-only"
				wantState = "accepted"
			}
			adapter := openDelivery(t, config)
			sent, err := adapter.Send(boundedContext(t), action)
			requireOK(t, err)
			check(t, sent.State == wantState && sent.IntentID == action.IntentID && sent.WorkspaceID == action.WorkspaceID, "notification acceptance/confirmation or intent mapping incorrect")
			if profile == "slack-workspace-bot" {
				check(t, sent.RemoteID == "C123:1720000000.000001", "Slack receipt must preserve channel and timestamp")
			}
			action.Prior = &sent
			_, err = adapter.Send(boundedContext(t), action)
			requireOK(t, err)
			check(t, calls.Load() == 1, "accepted/confirmed notification retry created another side effect")
		})
	}
}

func TestM08DeliveryFailuresAndRetrySafety(t *testing.T) {
	for _, tc := range []struct {
		name, profile, state string
		status               int
		body                 string
		want                 error
	}{
		{"rate-limit", "jira-cloud-v3", "rate-limited", 429, `{}`, ErrRateLimited},
		{"authentication", "jira-cloud-v3", "failed", 401, `{}`, ErrAuth},
		{"required-field", "jira-cloud-v3", "failed", 400, `{"errors":{"customfield_10010":"Required"}}`, ErrRequiredFields},
		{"slack-logical-auth-error", "slack-workspace-bot", "failed", 200, `{"ok":false,"error":"invalid_auth"}`, ErrAuth},
		{"lost-acknowledgement", "jira-cloud-v3", "uncertain", 0, "", ErrUncertain},
		{"redirect", "jira-cloud-v3", "blocked", 307, "", ErrScope},
		{"not-approved", "jira-cloud-v3", "blocked", -1, "", ErrScope},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base, client, calls := endpoint(t, func(w http.ResponseWriter, r *http.Request) {
				check(t, r.Method == "POST" && !strings.Contains(r.URL.Path, "credential-trap"), "delivery retried a write or followed a credential redirect")
				if tc.status == 0 {
					jsonBody(t, r)
					conn, _, err := w.(http.Hijacker).Hijack()
					if err != nil {
						t.Error("fixture could not simulate lost acknowledgement")
						return
					}
					_ = conn.Close()
					return
				}
				if tc.status == -1 {
					reply(w, 403, `{}`)
					return
				}
				w.Header().Set("Retry-After", "7")
				if tc.status == 307 {
					w.Header().Set("Location", "https://"+r.Host+"/credential-trap")
				}
				reply(w, tc.status, tc.body)
			})
			adapter := openDelivery(t, deliveryConfig(tc.profile, base, client))
			action := actionFixture()
			if tc.status == -1 {
				action.ApprovalRef = ""
			}
			result, err := adapter.Send(boundedContext(t), action)
			check(t, errors.Is(err, tc.want) && result.State == tc.state, "delivery failure was hidden, misclassified or retried")
			if tc.status == 429 {
				check(t, result.RetryAfter == 7*time.Second, "delivery lost Retry-After")
			}
			if tc.status == 400 {
				check(t, len(result.MissingFields) == 1 && result.MissingFields[0] == "customfield_10010", "Jira required-field response was discarded")
			}
			if tc.want == ErrUncertain {
				action.Prior = &result
				_, err = adapter.Send(boundedContext(t), action)
				check(t, errors.Is(err, ErrUncertain), "uncertain delivery must require reconciliation before retry")
			}
			wantCalls := int32(1)
			if tc.status == -1 {
				wantCalls = 0
			}
			check(t, calls.Load() == wantCalls, "unexpected retry, redirect or unapproved side effect")
		})
	}
}
