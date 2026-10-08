package app

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode"

	"github.com/bahadrdsr/aspm/internal/connectors"
)

type WebhookTarget struct {
	Origin    string `json:"origin"`
	Path      string `json:"path"`
	Signature string `json:"signature"`
}

func (target *WebhookTarget) UnmarshalJSON(data []byte) error {
	type value WebhookTarget
	var decoded value
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&decoded); err != nil {
		return err
	}
	*target = WebhookTarget(decoded)
	return nil
}

func canonicalWebhookOrigin(value string) (string, string, error) {
	if len(value) == 0 || len(value) > 16384 || !strings.HasPrefix(value, "https://") ||
		strings.ContainsAny(strings.TrimPrefix(value, "https://"), "/?#%@\\*") ||
		strings.ContainsFunc(value, func(r rune) bool { return r <= ' ' || r >= 0x7f }) {
		return "", "", errInvalid
	}
	target, err := url.Parse(value)
	if err != nil || target.Scheme != "https" || target.Host == "" || target.User != nil ||
		target.Path != "" || target.RawPath != "" || target.RawQuery != "" ||
		target.Fragment != "" || target.Opaque != "" || target.Host != strings.ToLower(target.Host) {
		return "", "", errInvalid
	}
	port := target.Port()
	if port == "" {
		port = "443"
	} else {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 || strconv.Itoa(number) != port {
			return "", "", errInvalid
		}
	}
	return value, strings.ToLower(target.Hostname()) + ":" + port, nil
}

func ValidateWebhookOrigins(values []string) error {
	if len(values) > 16 {
		return errors.New("webhook origins require at most 16 unique canonical HTTPS origins")
	}
	seen, effective := make(map[string]struct{}, len(values)), make(map[string]struct{}, len(values))
	for _, value := range values {
		origin, address, err := canonicalWebhookOrigin(value)
		if err != nil {
			return errors.New("webhook origins require at most 16 unique canonical HTTPS origins")
		}
		if _, duplicate := seen[origin]; duplicate {
			return errors.New("webhook origins require at most 16 unique canonical HTTPS origins")
		}
		if _, duplicate := effective[address]; duplicate {
			return errors.New("webhook origins require at most 16 unique canonical HTTPS origins")
		}
		seen[origin], effective[address] = struct{}{}, struct{}{}
	}
	return nil
}

func webhookOriginAllowed(origin string, allowed []string) bool {
	for _, value := range allowed {
		if value == origin {
			return true
		}
	}
	return false
}

func parseWebhookTarget(value string, allowed []string) (*WebhookTarget, bool) {
	if len(value) == 0 || len(value) > 16384 || len(allowed) == 0 {
		return nil, false
	}
	target, err := url.Parse(value)
	if err != nil || target.Scheme != "https" || target.Host == "" || target.User != nil ||
		target.Opaque != "" || target.RawQuery != "" || target.Fragment != "" ||
		target.Path == "" || target.Path == "/" || target.RawPath != "" ||
		strings.ContainsAny(target.Path, "\\?#") ||
		strings.ContainsFunc(target.Path, unicode.IsControl) {
		return nil, false
	}
	origin := "https://" + target.Host
	if canonical, _, err := canonicalWebhookOrigin(origin); err != nil ||
		canonical != origin || !webhookOriginAllowed(origin, allowed) {
		return nil, false
	}
	for _, segment := range strings.Split(target.Path, "/") {
		if segment == "." || segment == ".." {
			return nil, false
		}
	}
	escaped := strings.ToLower(target.EscapedPath())
	if strings.Contains(escaped, "%2f") || strings.Contains(escaped, "%5c") {
		return nil, false
	}
	return &WebhookTarget{Origin: origin, Path: target.Path, Signature: "hmac-sha256"}, true
}

func validWebhookTarget(target *WebhookTarget) bool {
	if target == nil || target.Signature != "hmac-sha256" {
		return false
	}
	parsed, valid := parseWebhookTarget(target.Origin+target.Path, []string{target.Origin})
	return valid && *parsed == *target
}

func validWebhookSecret(value string) bool {
	return len(value) >= 32 && len(value) <= 4096 &&
		strings.TrimSpace(value) == value &&
		!strings.ContainsFunc(value, unicode.IsControl)
}

func webhookEndpoint(target *WebhookTarget) string {
	if !validWebhookTarget(target) {
		return ""
	}
	return target.Origin + target.Path
}

func validWebhookProfile(profile string) bool {
	return profile == connectors.GenericWebhookV1
}

func (a *Application) writeWebhookPreview(w http.ResponseWriter, delivery FindingDelivery, digest string) {
	writeJSON(w, http.StatusOK, map[string]any{"preview": struct {
		WorkspaceID        string              `json:"workspaceId"`
		FindingID          string              `json:"findingId"`
		ConnectionID       string              `json:"connectionId"`
		ConnectionRevision int64               `json:"connectionRevision"`
		Profile            string              `json:"profile"`
		RequestedBy        string              `json:"requestedBy"`
		Webhook            *WebhookTarget      `json:"webhook"`
		Payload            FindingNotification `json:"payload"`
		BindingDigest      string              `json:"bindingDigest"`
		NativeValidation   string              `json:"nativeValidation"`
		ReviewRequirements []string            `json:"reviewRequirements"`
	}{
		delivery.WorkspaceID, delivery.FindingID, delivery.ConnectionID,
		delivery.ConnectionRevision, delivery.Profile, delivery.RequestedBy,
		delivery.Webhook, delivery.Payload, digest, "not-run",
		[]string{
			"explicit-queue-consent", "operator-approved-origin",
			"receiver-signature-verification",
		},
	}})
}
