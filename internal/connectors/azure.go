package connectors

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
)

const (
	ResourceGraphAPIVersion = "2022-10-01"
	AssessmentsAPIVersion   = "2020-01-01"
	ResourceGraphQuery      = "Resources | project id, name, type, location, subscriptionId"
)

type azureCollector struct{ collectorBase }

func (c *azureCollector) Collect(ctx context.Context, request CollectRequest) (Collection, error) {
	result, err := startCollection(ctx, request.Identity)
	if err != nil {
		return result, err
	}
	if !uuidID.MatchString(request.SubscriptionID) {
		return result.fail("scope", connectorError(ErrScope))
	}
	budget := httpBudget{http: c.http}
	if err := c.resources(ctx, &budget, request, &result); err != nil {
		return result.fail("resources", err)
	}
	if err := c.assessments(ctx, &budget, request, &result); err != nil {
		return result.fail("assessments", err)
	}
	result.Complete = true
	return result, nil
}

func armScope(id, subscription string) bool {
	return strings.HasPrefix(id, "/") && safePath(id) &&
		strings.HasPrefix(strings.ToLower(id), "/subscriptions/"+strings.ToLower(subscription)+"/") &&
		len(strings.Split(strings.Trim(id, "/"), "/")) >= 5
}

func assessmentParentScope(id, subscription string) bool {
	if armScope(id, subscription) {
		return true
	}
	if !safePath(id) {
		return false
	}
	if strings.EqualFold(id, "/subscriptions/"+subscription) {
		return true
	}
	parts := strings.Split(id, "/")
	return len(parts) == 5 && parts[0] == "" && strings.EqualFold(parts[1], "subscriptions") &&
		strings.EqualFold(parts[2], subscription) && strings.EqualFold(parts[3], "resourceGroups") &&
		textID(parts[4], 256)
}

func (c *azureCollector) resources(ctx context.Context, budget *httpBudget, request CollectRequest, result *Collection) error {
	target := withQuery(apiURL(c.base, "providers", "Microsoft.ResourceGraph", "resources"), url.Values{"api-version": {ResourceGraphAPIVersion}})
	headers := jsonHeaders(c.config.Token)
	token := ""
	seen := make(map[string]bool)
	for page := 1; page <= c.http.limits.Pages; page++ {
		query := struct {
			Subscriptions []string `json:"subscriptions"`
			Query         string   `json:"query"`
			Options       struct {
				ResultFormat string `json:"resultFormat"`
				Top          int    `json:"$top"`
				SkipToken    string `json:"$skipToken,omitempty"`
			} `json:"options"`
		}{Subscriptions: []string{request.SubscriptionID}, Query: ResourceGraphQuery}
		query.Options.ResultFormat, query.Options.Top, query.Options.SkipToken = "objectArray", c.http.limits.PageSize, token
		body, err := json.Marshal(query)
		if err != nil {
			return connectorError(ErrProtocol)
		}
		response, err := budget.do(ctx, http.MethodPost, target, headers, body, nil)
		if err != nil {
			return err
		}
		var native struct {
			Data            []json.RawMessage `json:"data"`
			SkipToken       string            `json:"$skipToken"`
			ResultTruncated json.RawMessage   `json:"resultTruncated"`
		}
		if response.Status != http.StatusOK || json.Unmarshal(response.Body, &native) != nil || native.Data == nil {
			return connectorError(ErrProtocol)
		}
		if len(native.Data) > c.http.limits.PageSize {
			return connectorError(ErrLimit)
		}
		for _, raw := range native.Data {
			var resource struct {
				ID             string `json:"id"`
				SubscriptionID string `json:"subscriptionId"`
				Location       string `json:"location"`
			}
			if json.Unmarshal(raw, &resource) != nil {
				return connectorError(ErrProtocol)
			}
			if !armScope(resource.ID, request.SubscriptionID) || !strings.EqualFold(resource.SubscriptionID, request.SubscriptionID) {
				return connectorError(ErrScope)
			}
			result.Records = append(result.Records, Record{
				Kind: "resource", ExternalID: resource.ID, ParentID: "/subscriptions/" + request.SubscriptionID,
				Location: resource.Location, Raw: raw, RawURL: target.String(),
			})
		}
		truncated := string(native.ResultTruncated) == "true" || string(native.ResultTruncated) == `"true"`
		if !truncated &&
			string(native.ResultTruncated) != "false" && string(native.ResultTruncated) != `"false"` {
			return connectorError(ErrProtocol)
		}
		if truncated && native.SkipToken == "" {
			return connectorError(ErrLimit)
		}
		if err := continueFeed(result, "resources", native.SkipToken, seen, page, c.http.limits); err != nil {
			return err
		}
		if native.SkipToken == "" {
			return nil
		}
		token = native.SkipToken
	}
	return connectorError(ErrLimit)
}

func (c *azureCollector) assessments(ctx context.Context, budget *httpBudget, request CollectRequest, result *Collection) error {
	target := withQuery(apiURL(c.base, "subscriptions", request.SubscriptionID, "providers", "Microsoft.Security", "assessments"), url.Values{"api-version": {AssessmentsAPIVersion}})
	headers := jsonHeaders(c.config.Token)
	seen := make(map[string]bool)
	for page := 1; page <= c.http.limits.Pages; page++ {
		var native struct {
			Value    []json.RawMessage `json:"value"`
			NextLink string            `json:"nextLink"`
		}
		_, err := budget.readJSON(ctx, target, headers, &native)
		if err != nil {
			return err
		}
		if native.Value == nil {
			return connectorError(ErrProtocol)
		}
		for _, raw := range native.Value {
			record, err := assessmentRecord(raw, request.SubscriptionID, target.String())
			if err != nil {
				return err
			}
			result.Records = append(result.Records, record)
		}
		if native.NextLink == "" {
			delete(result.Continuation, "assessments")
			return nil
		}
		next, err := scopedLink(target, native.NextLink)
		if err != nil {
			return err
		}
		query, err := url.ParseQuery(next.RawQuery)
		if err != nil || len(query["api-version"]) != 1 || query.Get("api-version") != AssessmentsAPIVersion {
			return connectorError(ErrScope)
		}
		skipKey := "$skiptoken"
		if _, exists := query["$skipToken"]; exists {
			skipKey = "$skipToken"
		}
		if len(query) != 2 || len(query[skipKey]) != 1 || !textID(query.Get(skipKey), 16384) {
			return connectorError(ErrScope)
		}
		if err := continueFeed(result, "assessments", next.String(), seen, page, c.http.limits); err != nil {
			return err
		}
		target = next
	}
	return connectorError(ErrLimit)
}

func assessmentRecord(raw []byte, subscription, rawURL string) (Record, error) {
	var native struct {
		ID         string `json:"id"`
		Name       string `json:"name"`
		Properties struct {
			ResourceDetails struct {
				ID     string `json:"id"`
				Source string `json:"source"`
			} `json:"resourceDetails"`
			Status struct {
				Code string `json:"code"`
			} `json:"status"`
			Metadata struct {
				Severity string `json:"severity"`
			} `json:"metadata"`
		} `json:"properties"`
	}
	if json.Unmarshal(raw, &native) != nil || !textID(native.Name, 256) || !textID(native.Properties.Status.Code, 128) {
		return Record{}, connectorError(ErrProtocol)
	}
	parent := native.Properties.ResourceDetails.ID
	if !armScope(native.ID, subscription) || !assessmentParentScope(parent, subscription) || strings.Contains(native.Name, "/") ||
		native.Properties.ResourceDetails.Source != "Azure" ||
		!strings.EqualFold(native.ID, parent+"/providers/Microsoft.Security/assessments/"+native.Name) {
		return Record{}, connectorError(ErrScope)
	}
	return Record{
		Kind: "assessment", ExternalID: native.ID, ParentID: parent, State: native.Properties.Status.Code,
		Severity: native.Properties.Metadata.Severity, Raw: raw, RawURL: rawURL,
	}, nil
}
