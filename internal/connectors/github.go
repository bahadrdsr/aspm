package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	GitHubAPIVersion    = "2026-03-10"
	githubMaxRetryAfter = 24 * time.Hour
)

type githubCollector struct{ collectorBase }

func (c *githubCollector) Collect(ctx context.Context, request CollectRequest) (Collection, error) {
	result, err := startCollection(ctx, request.Identity)
	if err != nil {
		return result, err
	}
	if !repositoryName(request.Repository) {
		return result.fail("repository", connectorError(ErrScope))
	}
	budget := httpBudget{http: c.http}
	headers := jsonHeaders(c.config.Token)
	headers.Set("Accept", "application/vnd.github+json")
	headers.Set("X-GitHub-Api-Version", GitHubAPIVersion)
	parts := strings.Split(request.Repository, "/")
	target := apiURL(c.base, "repos", parts[0], parts[1])
	var repository struct {
		ID       json.Number `json:"id"`
		FullName string      `json:"full_name"`
	}
	response, err := budget.readJSON(ctx, target, headers, &repository)
	if err != nil {
		return result.fail("repository", githubFailure(response, err))
	}
	if !numericID.MatchString(repository.ID.String()) || !strings.EqualFold(repository.FullName, request.Repository) {
		return result.fail("repository", connectorError(ErrScope))
	}
	result.Records = append(result.Records, Record{
		Kind: "repository", ExternalID: repository.ID.String(), Raw: response.Body, RawURL: target.String(),
	})
	target = withQuery(apiURL(c.base, "repos", parts[0], parts[1], "code-scanning", "alerts"), url.Values{
		"per_page": {strconv.Itoa(c.http.limits.PageSize)}, "page": {"1"},
	})
	seen := make(map[string]bool)
	for page := 1; page <= c.http.limits.Pages; page++ {
		var alerts []json.RawMessage
		response, err = budget.readJSON(ctx, target, headers, &alerts)
		if err != nil {
			return result.fail("alerts", githubFailure(response, err))
		}
		if alerts == nil {
			return result.fail("alerts", connectorError(ErrProtocol))
		}
		if len(alerts) > c.http.limits.PageSize {
			return result.fail("alerts", connectorError(ErrLimit))
		}
		for _, raw := range alerts {
			var alert struct {
				Number    json.Number `json:"number"`
				State     string      `json:"state"`
				UpdatedAt string      `json:"updated_at"`
				Rule      struct {
					SecuritySeverity string `json:"security_severity_level"`
				} `json:"rule"`
				Instance struct {
					Location struct {
						Path      string `json:"path"`
						StartLine int    `json:"start_line"`
					} `json:"location"`
				} `json:"most_recent_instance"`
			}
			if json.Unmarshal(raw, &alert) != nil || !numericID.MatchString(alert.Number.String()) {
				return result.fail("alerts", connectorError(ErrProtocol))
			}
			updated, err := sourceTime(alert.UpdatedAt)
			if err != nil {
				return result.fail("alerts", err)
			}
			location := alert.Instance.Location.Path
			if alert.Instance.Location.StartLine > 0 {
				location += ":" + strconv.Itoa(alert.Instance.Location.StartLine)
			}
			result.Records = append(result.Records, Record{
				Kind: "finding", ExternalID: alert.Number.String(), ParentID: repository.ID.String(),
				State: alert.State, Severity: alert.Rule.SecuritySeverity,
				Location: location, Raw: raw, RawURL: target.String(), SourceUpdatedAt: updated,
			})
		}
		nextValue, err := nextLink(response.Header.Values("Link"))
		if err != nil {
			return result.fail("alerts", err)
		}
		if nextValue == "" {
			result.Complete = true
			delete(result.Continuation, "alerts")
			return result, nil
		}
		next, err := scopedLink(target, nextValue)
		if err != nil {
			return result.fail("alerts", err)
		}
		if !exactQuery(next, url.Values{"per_page": {strconv.Itoa(c.http.limits.PageSize)}, "page": {strconv.Itoa(page + 1)}}) {
			return result.fail("alerts", connectorError(ErrScope))
		}
		if err := continueFeed(&result, "alerts", next.String(), seen, page, c.http.limits); err != nil {
			return result.fail("alerts", err)
		}
		target = next
	}
	return result.fail("alerts", connectorError(ErrLimit))
}

func githubFailure(response nativeResponse, err error) error {
	if response.Status != http.StatusForbidden && response.Status != http.StatusTooManyRequests {
		return err
	}
	primary := strings.TrimSpace(response.Header.Get("X-RateLimit-Remaining")) == "0"
	var native struct {
		Message string `json:"message"`
	}
	if json.Unmarshal(response.Body, &native) != nil {
		native.Message = ""
	}
	message := strings.ToLower(native.Message)
	if response.Status != http.StatusTooManyRequests && !primary &&
		!strings.Contains(message, "secondary rate limit") && !strings.Contains(message, "api rate limit exceeded") {
		return err
	}
	cause := ErrRateLimited
	if errors.Is(err, ErrLimit) {
		cause = errors.Join(cause, ErrLimit)
	}
	failure := responseError(response, cause)
	now := time.Now()
	failure.RetryAfter = retryAfter(response.Header.Get("Retry-After"), now)
	if primary {
		reset, parseErr := strconv.ParseInt(strings.TrimSpace(response.Header.Get("X-RateLimit-Reset")), 10, 64)
		if parseErr == nil && reset > now.Unix() {
			delay := githubMaxRetryAfter
			if reset <= now.Add(githubMaxRetryAfter).Unix() {
				delay = time.Unix(reset, 0).Sub(now)
			}
			if delay > failure.RetryAfter {
				failure.RetryAfter = delay
			}
		}
	}
	if failure.RetryAfter > githubMaxRetryAfter {
		failure.RetryAfter = githubMaxRetryAfter
	}
	return failure
}

// Split Link entries only outside their URI and quoted parameters.
func nextLink(headers []string) (string, error) {
	value := strings.Join(headers, ",")
	if len(value) > 16384 {
		return "", connectorError(ErrLimit)
	}
	var entries []string
	start, angle, quoted, escaped := 0, false, false, false
	for i, ch := range value {
		if escaped {
			escaped = false
			continue
		}
		if quoted && ch == '\\' {
			escaped = true
			continue
		}
		switch ch {
		case '"':
			if !angle {
				quoted = !quoted
			}
		case '<':
			if !quoted {
				angle = true
			}
		case '>':
			if !quoted {
				angle = false
			}
		case ',':
			if !angle && !quoted {
				entries = append(entries, value[start:i])
				start = i + 1
			}
		}
	}
	if angle || quoted || escaped {
		return "", connectorError(ErrProtocol)
	}
	entries = append(entries, value[start:])
	next := ""
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		end := strings.IndexByte(entry, '>')
		if !strings.HasPrefix(entry, "<") || end < 2 {
			return "", connectorError(ErrProtocol)
		}
		for _, parameter := range strings.Split(entry[end+1:], ";") {
			key, value, ok := strings.Cut(strings.TrimSpace(parameter), "=")
			if !ok || !strings.EqualFold(key, "rel") {
				continue
			}
			for _, relation := range strings.Fields(strings.Trim(value, `"`)) {
				if relation == "next" {
					candidate := entry[1:end]
					if next != "" && next != candidate {
						return "", connectorError(ErrScope)
					}
					next = candidate
				}
			}
		}
	}
	return next, nil
}
