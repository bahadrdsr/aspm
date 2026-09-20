package app

import (
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

func validAIText(value string, limit int) bool {
	return validText(value, limit) && utf8.ValidString(value) && !strings.ContainsFunc(value, unicode.IsControl)
}

func validAIFamily(family string) bool {
	switch family {
	case "openai", "azure-foundry", "anthropic", "local":
		return true
	default:
		return false
	}
}

func validAIProfile(profile AIProfile) bool {
	if !validAIFamily(profile.Family) || !validAIText(profile.Name, 256) ||
		!validAIText(profile.Model, 256) || !validAIEndpoint(profile.Family, profile.Endpoint) {
		return false
	}
	if profile.Family == "azure-foundry" {
		if !validAIText(profile.Deployment, 256) {
			return false
		}
	} else if profile.Deployment != "" {
		return false
	}
	return profile.Family == "local" || profile.CredentialConfigured
}

// Configuration checks are deliberately pure. They do not construct an
// assessor or change the provider adapter's HTTP, TLS or payload behavior.
func validAIEndpoint(family, endpoint string) bool {
	if endpoint == "" || len(endpoint) > aiEndpointLimit || !utf8.ValidString(endpoint) ||
		strings.ContainsAny(endpoint, "?#\\") ||
		strings.ContainsFunc(endpoint, func(r rune) bool { return unicode.IsControl(r) || unicode.IsSpace(r) }) {
		return false
	}
	u, err := url.Parse(endpoint)
	if err != nil || u.Opaque != "" || u.Hostname() == "" || u.User != nil ||
		u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return false
	}
	host := u.Hostname()
	ip := net.ParseIP(host)
	if strings.HasPrefix(u.Host, "[") {
		if ip == nil || !strings.Contains(host, ":") {
			return false
		}
	} else if strings.ContainsAny(host, ":[]") || (ip == nil && !validAIHostname(host)) {
		return false
	}
	if strings.HasSuffix(u.Host, ":") {
		return false
	}
	if port := u.Port(); port != "" {
		number, err := strconv.ParseUint(port, 10, 16)
		if err != nil || number == 0 {
			return false
		}
	}
	if u.Scheme != "https" && (u.Scheme != "http" || family != "local" ||
		ip == nil || (!ip.IsPrivate() && !ip.IsLoopback())) {
		return false
	}
	if u.Path != "" && !strings.HasPrefix(u.Path, "/") || strings.Contains(u.Path, "//") {
		return false
	}
	for _, segment := range strings.Split(u.EscapedPath(), "/") {
		decoded, err := url.PathUnescape(segment)
		// Encoded separators and nested escapes can change the reviewed path
		// boundary at a gateway. A single trailing slash remains exact metadata.
		if err != nil || decoded == "." || decoded == ".." || !utf8.ValidString(decoded) ||
			strings.ContainsAny(decoded, "/\\%?#") || strings.ContainsFunc(decoded, unicode.IsControl) {
			return false
		}
	}
	return true
}

func validAIHostname(host string) bool {
	if len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(strings.TrimSuffix(host, "."), ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && (c < '0' || c > '9') && c != '-' {
				return false
			}
		}
	}
	return true
}

func validAIPolicyMode(mode string) bool {
	return mode == "disabled" || mode == "local-only" || mode == "approved-hosted"
}
