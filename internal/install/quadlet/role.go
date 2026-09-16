package quadlet

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const maxUnitBytes = 65536

var (
	ErrConfiguration = errors.New("invalid or missing Quadlet role configuration")
	ErrUnit          = errors.New("unsupported or invalid Quadlet role unit")
	segmentPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	environmentName  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

type Config struct {
	Role             string
	EnvironmentFile  string
	RawPrefix        string
	ReadinessKey     string
	NormalizedPrefix string
}

func ValidateConfig(config Config) error {
	switch config.Role {
	case "core", "ingestion", "reports":
	default:
		return ErrConfiguration
	}
	if config.EnvironmentFile != "/etc/aspm/"+config.Role+".env" {
		return ErrConfiguration
	}
	if config.Role == "reports" {
		if config.RawPrefix != "" || config.ReadinessKey != "" || config.NormalizedPrefix != "" {
			return ErrConfiguration
		}
		return nil
	}
	if !validKey(config.RawPrefix, true) || !validKey(config.ReadinessKey, false) ||
		!strings.HasPrefix(config.ReadinessKey, config.RawPrefix) {
		return ErrConfiguration
	}
	if config.Role == "core" {
		if config.NormalizedPrefix != "" {
			return ErrConfiguration
		}
		return nil
	}
	if !validKey(config.NormalizedPrefix, true) ||
		strings.HasPrefix(config.NormalizedPrefix, config.RawPrefix) ||
		strings.HasPrefix(config.RawPrefix, config.NormalizedPrefix) {
		return ErrConfiguration
	}
	return nil
}

func validKey(value string, prefix bool) bool {
	if len(value) == 0 || len(value) > 1024 || strings.HasSuffix(value, "/") != prefix {
		return false
	}
	for _, part := range strings.Split(strings.TrimSuffix(value, "/"), "/") {
		if !segmentPattern.MatchString(part) {
			return false
		}
	}
	return true
}

// RenderRole changes only the selected role file and inline storage scopes.
// Secret material and deployment authority are deliberately absent from Config.
func RenderRole(ctx context.Context, source []byte, config Config) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := ValidateConfig(config); err != nil {
		return nil, err
	}
	if len(source) == 0 || len(source) > maxUnitBytes || !utf8.Valid(source) {
		return nil, ErrUnit
	}
	text := string(source)
	for index, char := range text {
		if char == '\r' && (index+1 >= len(text) || text[index+1] != '\n') {
			return nil, ErrUnit
		}
		if unicode.IsControl(char) && char != '\n' && char != '\r' && char != '\t' {
			return nil, ErrUnit
		}
	}
	newline := "\n"
	if strings.Contains(text, "\r\n") {
		newline = "\r\n"
	}
	selected := []string{"EnvironmentFile=" + config.EnvironmentFile}
	if config.Role != "reports" {
		selected = append(selected,
			"Environment=ASPM_S3_PREFIX="+config.RawPrefix,
			"Environment=ASPM_S3_READINESS_KEY="+config.ReadinessKey,
		)
	}
	if config.Role == "ingestion" {
		selected = append(selected, "Environment=ASPM_S3_NORMALIZED_PREFIX="+config.NormalizedPrefix)
	}
	replacement := strings.Join(selected, newline) + newline
	var result strings.Builder
	result.Grow(len(text) + len(replacement))
	section := ""
	hasContainer, inserted := false, false
	seenEnvironment := make(map[string]struct{})
	for _, original := range strings.SplitAfter(text, "\n") {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		line := strings.TrimSpace(original)
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			result.WriteString(original)
			continue
		}
		if strings.HasSuffix(line, "\\") {
			return nil, ErrUnit
		}
		if strings.HasPrefix(line, "[") {
			if !strings.HasSuffix(line, "]") || len(line) < 3 || strings.ContainsAny(line[1:len(line)-1], "[] \t\r\n") {
				return nil, ErrUnit
			}
			section = line[1 : len(line)-1]
			hasContainer = hasContainer || section == "Container"
			result.WriteString(original)
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || section == "" {
			return nil, ErrUnit
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if key == "" {
			return nil, ErrUnit
		}
		if section == "Service" {
			switch key {
			case "Environment", "EnvironmentFile", "PassEnvironment":
				return nil, ErrUnit
			}
		}
		if section != "Container" {
			result.WriteString(original)
			continue
		}
		switch key {
		case "EnvironmentFile":
			if !inserted {
				result.WriteString(replacement)
				inserted = true
			}
			continue
		case "Secret", "PodmanArgs", "ContainersConfModule", "GlobalArgs":
			if value != "" {
				return nil, ErrUnit
			}
		case "EnvironmentHost":
			if value != "" && value != "false" && value != `"false"` {
				return nil, ErrUnit
			}
		case "Environment":
			name, err := assignmentName(value)
			if err != nil {
				return nil, err
			}
			if config.Role == "reports" && !reportEnvironment(name) {
				return nil, ErrUnit
			}
			if strings.HasPrefix(name, "AWS_") || name == "ASPM_S3_ACCESS_KEY" ||
				name == "ASPM_S3_SECRET_KEY" || name == "ASPM_BOOTSTRAP_TOKEN" {
				return nil, ErrUnit
			}
			if name == "ASPM_S3_PREFIX" || name == "ASPM_S3_READINESS_KEY" || name == "ASPM_S3_NORMALIZED_PREFIX" {
				continue
			}
			if _, duplicate := seenEnvironment[name]; duplicate {
				return nil, ErrUnit
			}
			seenEnvironment[name] = struct{}{}
		}
		result.WriteString(original)
	}
	if !hasContainer {
		return nil, ErrUnit
	}
	if !inserted {
		if !strings.HasSuffix(text, "\n") {
			result.WriteString(newline)
		}
		result.WriteString("[Container]" + newline + replacement)
	}
	if result.Len() > maxUnitBytes {
		return nil, ErrUnit
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return []byte(result.String()), nil
}

func assignmentName(value string) (string, error) {
	if strings.HasPrefix(value, `"`) {
		decoded, err := strconv.Unquote(value)
		if err != nil {
			return "", ErrUnit
		}
		value = decoded
	} else if strings.IndexFunc(value, unicode.IsSpace) >= 0 {
		return "", ErrUnit
	}
	name, _, ok := strings.Cut(value, "=")
	if !ok || !environmentName.MatchString(name) {
		return "", ErrUnit
	}
	return name, nil
}

func reportEnvironment(name string) bool {
	switch name {
	case "ASPM_DATABASE_URL", "ASPM_SCHEMA", "ASPM_DB_MAX_CONNECTIONS", "ASPM_LISTEN", "ASPM_WORKER_ID":
		return true
	default:
		return false
	}
}
