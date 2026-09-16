package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type collectorBase struct {
	config CollectorConfig
	base   *url.URL
	http   *boundedHTTP
}

// OpenCollector does no I/O, including credential retrieval. Each Collect call
// starts a separate bounded, selected-scope observation, never a scanner run.
func OpenCollector(ctx context.Context, config CollectorConfig) (Collector, error) {
	if err := ctx.Err(); err != nil {
		return nil, connectorError(err)
	}
	base, err := tlsURL(config.Endpoint, false)
	if err != nil {
		return nil, err
	}
	client, err := newHTTP(config.Client, config.Limits)
	if err != nil {
		return nil, err
	}
	if config.Profile != AWSEC2SecurityHub && !textID(config.Token, 16384) {
		return nil, connectorError(ErrAuth)
	}
	common := collectorBase{config: config, base: base, http: client}
	switch config.Profile {
	case GitHubCloudApp:
		return &githubCollector{collectorBase: common}, nil
	case GitLabArtifacts:
		return &gitlabCollector{collectorBase: common}, nil
	case ADOArtifacts:
		return &adoCollector{collectorBase: common}, nil
	case AWSEC2SecurityHub:
		findings, err := tlsURL(config.FindingsEndpoint, false)
		if err != nil {
			return nil, err
		}
		if config.Credentials == nil {
			return nil, connectorError(ErrAuth)
		}
		if client.limits.PageSize < 5 {
			return nil, connectorError(ErrLimit)
		}
		return &awsCollector{collectorBase: common, findings: findings}, nil
	case AzureAssessments:
		return &azureCollector{collectorBase: common}, nil
	default:
		return nil, connectorError(ErrUnsupported)
	}
}

func startCollection(ctx context.Context, identity Identity) (Collection, error) {
	result := Collection{Identity: identity, CollectedAt: time.Now().UTC(), Continuation: make(map[string]string)}
	if err := ctx.Err(); err != nil {
		return result.fail("scope", connectorError(err))
	}
	for _, id := range []string{identity.WorkspaceID, identity.SourceID, identity.RunID, identity.ScopeID} {
		if !textID(id, 256) {
			return result.fail("scope", connectorError(ErrScope))
		}
	}
	return result, nil
}

func (c Collection) fail(feed string, err error) (Collection, error) {
	c.Complete = false
	var detail *Error
	if !errors.As(err, &detail) {
		detail = connectorError(err)
	}
	c.Gaps = append(c.Gaps, feed+":"+detail.Code)
	if detail.RetryAfter > c.RetryAfter {
		c.RetryAfter = detail.RetryAfter
	}
	return c, err
}

func (b *httpBudget) readJSON(ctx context.Context, target *url.URL, headers http.Header, out any) (nativeResponse, error) {
	response, err := b.do(ctx, http.MethodGet, target, headers, nil, nil)
	if err != nil {
		return response, err
	}
	if response.Status != http.StatusOK || json.Unmarshal(response.Body, out) != nil {
		return response, responseError(response, ErrProtocol)
	}
	return response, nil
}

func sourceTime(value string) (*time.Time, error) {
	if value == "" {
		return nil, nil
	}
	stamp, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return nil, connectorError(ErrProtocol)
	}
	return &stamp, nil
}

// Artifact collection is not a report parser. Only one unambiguous declared
// SARIF invocation supplies a scan time; pipeline/build/poll time never does.
func sarifScanTime(raw []byte) *time.Time {
	var report struct {
		Version string `json:"version"`
		Runs    []struct {
			Invocations []struct {
				StartTime string `json:"startTime"`
			} `json:"invocations"`
		} `json:"runs"`
	}
	if json.Unmarshal(raw, &report) != nil || report.Version != "2.1.0" ||
		len(report.Runs) != 1 || len(report.Runs[0].Invocations) != 1 {
		return nil
	}
	stamp, _ := sourceTime(report.Runs[0].Invocations[0].StartTime)
	return stamp
}

func continueFeed(c *Collection, feed, token string, seen map[string]bool, page int, limits Limits) error {
	if token == "" {
		delete(c.Continuation, feed)
		return nil
	}
	if !textID(token, 16384) || seen[token] {
		return connectorError(ErrProtocol)
	}
	seen[token] = true
	c.Continuation[feed] = token
	if page >= limits.Pages {
		return connectorError(ErrLimit)
	}
	return nil
}

func repositoryName(value string) bool {
	parts := strings.Split(value, "/")
	return len(parts) == 2 && nameID.MatchString(parts[0]) && nameID.MatchString(parts[1])
}
