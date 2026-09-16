package connectors

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

type jiraDelivery struct{ deliveryBase }

type jiraField struct {
	FieldID         string `json:"fieldId"`
	ID              string `json:"id"`
	Required        bool   `json:"required"`
	HasDefaultValue bool   `json:"hasDefaultValue"`
	Schema          struct {
		Type string `json:"type"`
	} `json:"schema"`
}

func (d *jiraDelivery) Preview(ctx context.Context, action Action) (Preview, error) {
	result := Preview{}
	if err := d.validateAction(action, false); err != nil {
		return result, err
	}
	budget := httpBudget{http: d.http}
	start := 0
	missing := make(map[string]bool)
	if strings.TrimSpace(action.Title) == "" {
		missing["summary"] = true
	}
	for page := 0; page < d.http.limits.Pages; page++ {
		target := withQuery(apiURL(d.base, "rest", "api", "3", "issue", "createmeta", d.config.Project, "issuetypes", d.config.IssueType), url.Values{
			"startAt": {strconv.Itoa(start)}, "maxResults": {strconv.Itoa(d.http.limits.PageSize)},
		})
		response, err := budget.do(ctx, http.MethodGet, target, jsonHeaders(d.config.Token), nil, nil)
		if err != nil {
			return result, err
		}
		var metadata struct {
			StartAt *int        `json:"startAt"`
			Total   *int        `json:"total"`
			IsLast  *bool       `json:"isLast"`
			Fields  []jiraField `json:"fields"`
			Values  []jiraField `json:"values"`
		}
		if json.Unmarshal(response.Body, &metadata) != nil ||
			metadata.StartAt != nil && *metadata.StartAt != start ||
			metadata.Fields != nil && metadata.Values != nil {
			return result, connectorError(ErrProtocol)
		}
		fields := metadata.Fields
		if fields == nil {
			fields = metadata.Values
		}
		if fields == nil || len(fields) > d.http.limits.PageSize {
			return result, connectorError(ErrProtocol)
		}
		for _, field := range fields {
			id := field.FieldID
			if id == "" {
				id = field.ID
			}
			if !textID(id, 128) {
				return result, connectorError(ErrProtocol)
			}
			if !field.Required || field.HasDefaultValue {
				continue
			}
			value := ""
			switch id {
			case "project":
				value = d.config.Project
			case "issuetype":
				value = d.config.IssueType
			case "summary":
				value = action.Title
			case "description":
				value = action.Body
			default:
				if customFieldID.MatchString(id) && (field.Schema.Type == "" || field.Schema.Type == "string") {
					value = action.Fields[id]
				}
			}
			if strings.TrimSpace(value) == "" {
				missing[id] = true
			}
		}
		result.MissingFields = result.MissingFields[:0]
		for id := range missing {
			result.MissingFields = append(result.MissingFields, id)
		}
		slices.Sort(result.MissingFields)
		start += len(fields)
		done := metadata.IsLast != nil && *metadata.IsLast ||
			metadata.Total != nil && start >= *metadata.Total ||
			metadata.IsLast == nil && metadata.Total == nil && len(fields) < d.http.limits.PageSize
		if done {
			if len(missing) != 0 {
				return result, connectorError(ErrRequiredFields)
			}
			return result, nil
		}
		if len(fields) == 0 {
			return result, connectorError(ErrProtocol)
		}
	}
	return result, connectorError(ErrLimit)
}

type adfMark struct {
	Type  string            `json:"type"`
	Attrs map[string]string `json:"attrs"`
}

type adfText struct {
	Type  string    `json:"type"`
	Text  string    `json:"text"`
	Marks []adfMark `json:"marks,omitempty"`
}

type adfParagraph struct {
	Type    string    `json:"type"`
	Content []adfText `json:"content"`
}

func (d *jiraDelivery) Send(ctx context.Context, action Action) (Delivery, error) {
	result, finished, err := d.prepare(ctx, action)
	if finished {
		return result, err
	}
	if utf8.RuneCountInString(action.Title) > 255 {
		return deliveryFailure(result, nativeResponse{}, connectorError(ErrLimit))
	}
	bodyParagraph := adfParagraph{Type: "paragraph", Content: []adfText{}}
	if action.Body != "" {
		bodyParagraph.Content = []adfText{{Type: "text", Text: action.Body}}
	}
	fields := map[string]any{
		"project":   map[string]string{"key": d.config.Project},
		"issuetype": map[string]string{"id": d.config.IssueType},
		"summary":   action.Title,
		"description": struct {
			Type    string         `json:"type"`
			Version int            `json:"version"`
			Content []adfParagraph `json:"content"`
		}{
			Type: "doc", Version: 1,
			Content: []adfParagraph{bodyParagraph, {
				Type: "paragraph", Content: []adfText{{
					Type: "text", Text: "View finding",
					Marks: []adfMark{{Type: "link", Attrs: map[string]string{"href": action.DeepLink}}},
				}},
			}},
		},
	}
	for key, value := range action.Fields {
		fields[key] = value
	}
	body, err := json.Marshal(struct {
		Fields map[string]any `json:"fields"`
	}{fields})
	if err != nil {
		return deliveryFailure(result, nativeResponse{}, connectorError(ErrProtocol))
	}
	budget := httpBudget{http: d.http}
	response, err := budget.do(ctx, http.MethodPost, apiURL(d.base, "rest", "api", "3", "issue"), jsonHeaders(d.config.Token), body, nil)
	if response.Status == http.StatusBadRequest {
		var failure struct {
			Errors map[string]string `json:"errors"`
		}
		if json.Unmarshal(response.Body, &failure) == nil && len(failure.Errors) > 0 {
			for id := range failure.Errors {
				if textID(id, 128) {
					result.MissingFields = append(result.MissingFields, id)
				}
			}
			slices.Sort(result.MissingFields)
			err = responseError(response, ErrRequiredFields)
		}
	}
	if err != nil {
		return deliveryFailure(result, response, err)
	}
	var receipt struct {
		Key  string `json:"key"`
		Self string `json:"self"`
	}
	if response.Status != http.StatusCreated || json.Unmarshal(response.Body, &receipt) != nil || !d.issueKey(receipt.Key) {
		return deliveryFailure(result, response, connectorError(ErrProtocol))
	}
	result.State, result.RemoteID = "confirmed", receipt.Key
	if remote, err := tlsURL(receipt.Self, true); err == nil {
		result.RemoteURL = remote.String()
	}
	return result, nil
}

func (d *jiraDelivery) issueKey(key string) bool {
	return strings.HasPrefix(key, d.config.Project+"-") && numericID.MatchString(strings.TrimPrefix(key, d.config.Project+"-"))
}

func (d *jiraDelivery) Status(ctx context.Context, key string) (StatusLink, error) {
	result := StatusLink{RemoteID: key}
	if !d.issueKey(key) {
		return result, connectorError(ErrScope)
	}
	budget := httpBudget{http: d.http}
	target := withQuery(apiURL(d.base, "rest", "api", "3", "issue", key), url.Values{"fields": {"status"}})
	response, err := budget.do(ctx, http.MethodGet, target, jsonHeaders(d.config.Token), nil, nil)
	if err != nil {
		return result, err
	}
	var issue struct {
		Key    string `json:"key"`
		Fields struct {
			Status struct {
				ID string `json:"id"`
			} `json:"status"`
		} `json:"fields"`
	}
	if response.Status != http.StatusOK || json.Unmarshal(response.Body, &issue) != nil || !numericID.MatchString(issue.Fields.Status.ID) {
		return result, connectorError(ErrProtocol)
	}
	if issue.Key != key {
		return result, connectorError(ErrScope)
	}
	result.NativeStatus = issue.Fields.Status.ID
	result.LinkedState = d.config.StatusMap[issue.Fields.Status.ID]
	return result, nil
}
