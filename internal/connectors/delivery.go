package connectors

import (
	"context"
	"errors"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode"
)

var (
	jiraProjectID = regexp.MustCompile(`^[A-Z][A-Z0-9_]{0,254}$`)
	customFieldID = regexp.MustCompile(`^customfield_[0-9]+$`)
	slackChannel  = regexp.MustCompile(`^[CG][A-Z0-9]{2,}$`)
)

type deliveryBase struct {
	config DeliveryConfig
	base   *url.URL
	http   *boundedHTTP
}

// OpenDelivery validates configuration without making requests. The supplied
// client and token must already be authorized for this workspace/destination.
func OpenDelivery(ctx context.Context, config DeliveryConfig) (DeliveryAdapter, error) {
	if err := ctx.Err(); err != nil {
		return nil, connectorError(err)
	}
	base, err := tlsURL(config.Endpoint, config.Profile == TeamsWorkflows)
	if err != nil {
		return nil, err
	}
	client, err := newHTTP(config.Client, config.Limits)
	if err != nil {
		return nil, err
	}
	if !textID(config.WorkspaceID, 256) {
		return nil, connectorError(ErrScope)
	}
	if config.Profile == TeamsWorkflows {
		query, err := url.ParseQuery(base.RawQuery)
		signature := query.Get("sig")
		if err != nil || len(query["sig"]) != 1 || !textID(signature, 16384) ||
			strings.ContainsFunc(signature, unicode.IsSpace) {
			return nil, connectorError(ErrAuth)
		}
	} else if !textID(config.Token, 16384) {
		return nil, connectorError(ErrAuth)
	}
	statusMap := make(map[string]string, len(config.StatusMap))
	for native, state := range config.StatusMap {
		if config.Profile != JiraCloudV3 {
			break
		}
		if !numericID.MatchString(native) {
			return nil, connectorError(ErrScope)
		}
		switch state {
		case "", "open", "in-progress", "pending-retest":
			statusMap[native] = state
		default:
			return nil, connectorError(ErrScope)
		}
	}
	config.StatusMap = statusMap
	common := deliveryBase{config: config, base: base, http: client}
	switch config.Profile {
	case JiraCloudV3:
		if !jiraProjectID.MatchString(config.Project) || !numericID.MatchString(config.IssueType) {
			return nil, connectorError(ErrScope)
		}
		return &jiraDelivery{deliveryBase: common}, nil
	case TeamsWorkflows:
		if base.Path == "" || base.Path == "/" {
			return nil, connectorError(ErrScope)
		}
		return &notificationDelivery{deliveryBase: common}, nil
	case SlackWorkspaceBot:
		if !slackChannel.MatchString(config.Channel) {
			return nil, connectorError(ErrScope)
		}
		return &notificationDelivery{deliveryBase: common}, nil
	default:
		return nil, connectorError(ErrUnsupported)
	}
}

func (d *deliveryBase) validateAction(action Action, sending bool) error {
	if action.WorkspaceID != d.config.WorkspaceID || !textID(action.FindingID, 256) {
		return connectorError(ErrScope)
	}
	if sending && (!textID(action.IntentID, 256) || !textID(action.ApprovalRef, 512)) {
		return connectorError(ErrScope)
	}
	if err := validateBrowserLink(action.DeepLink); err != nil {
		return err
	}
	if d.config.Profile == JiraCloudV3 {
		for field := range action.Fields {
			if !customFieldID.MatchString(field) || len(field) > 128 {
				return connectorError(ErrScope)
			}
		}
	}
	return nil
}

// Browser fragments are payload data, never part of a provider request target.
func validateBrowserLink(value string) error {
	if len(value) > 16384 || strings.ContainsFunc(value, unicode.IsControl) {
		return connectorError(ErrScope)
	}
	base, fragment, _ := strings.Cut(value, "#")
	parsed, err := tlsURL(base, true)
	if err != nil {
		return err
	}
	decodedFragment, err := url.PathUnescape(fragment)
	if err != nil || strings.ContainsFunc(decodedFragment, unicode.IsControl) {
		return connectorError(ErrScope)
	}
	decodedQuery, err := url.QueryUnescape(parsed.RawQuery)
	if err != nil || strings.ContainsFunc(decodedQuery, unicode.IsControl) {
		return connectorError(ErrScope)
	}
	return nil
}

func (d *deliveryBase) prepare(ctx context.Context, action Action) (Delivery, bool, error) {
	result := Delivery{WorkspaceID: action.WorkspaceID, IntentID: action.IntentID, State: "blocked"}
	if err := ctx.Err(); err != nil {
		return result, true, connectorError(err)
	}
	if err := d.validateAction(action, true); err != nil {
		return result, true, err
	}
	if prior := action.Prior; prior != nil {
		if prior.WorkspaceID != action.WorkspaceID || prior.IntentID != action.IntentID {
			return result, true, connectorError(ErrScope)
		}
		copyPrior := *prior
		copyPrior.MissingFields = slices.Clone(prior.MissingFields)
		switch prior.State {
		case "confirmed", "accepted":
			return copyPrior, true, nil
		case "uncertain":
			return copyPrior, true, connectorError(ErrUncertain)
		case "failed", "blocked", "rate-limited":
		default:
			return result, true, connectorError(ErrScope)
		}
	}
	if strings.TrimSpace(action.Title) == "" {
		result.State = "failed"
		result.MissingFields = []string{"summary"}
		return result, true, connectorError(ErrRequiredFields)
	}
	return result, false, nil
}

func deliveryFailure(result Delivery, response nativeResponse, err error) (Delivery, error) {
	var detail *Error
	if errors.As(err, &detail) {
		result.RetryAfter = detail.RetryAfter
	}
	if response.Attempted && response.Status >= 500 {
		result.State = "uncertain"
		return result, responseError(response, errors.Join(ErrUncertain, err))
	}
	switch {
	case errors.Is(err, ErrScope):
		result.State = "blocked"
	case errors.Is(err, ErrAuth), errors.Is(err, ErrRequiredFields):
		result.State = "failed"
	case errors.Is(err, ErrRateLimited):
		result.State = "rate-limited"
	case response.Attempted && (response.Status == 0 || response.Status >= 500 || response.Status >= 200 && response.Status < 300):
		result.State = "uncertain"
		wrapped := responseError(response, errors.Join(ErrUncertain, err))
		return result, wrapped
	default:
		result.State = "failed"
	}
	return result, err
}
