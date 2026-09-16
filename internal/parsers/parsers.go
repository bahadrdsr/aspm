// Package parsers admits bounded report data, not scanner commands or executable
// mappings. Source scan time is supplied by the import envelope, never guessed
// from report generation or commit timestamps.
package parsers

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"unicode/utf8"
)

var (
	ErrUnsupported = errors.New("unsupported report format")
	ErrInvalid     = errors.New("report does not match the admitted format")
)

const maxFindings = 5000

type Location struct {
	URI  string `json:"uri"`
	Line int    `json:"line"`
}

type Finding struct {
	Identity, SourceFindingID, Title, Description string
	SourceSeverity, Severity, Impact, Remediation string
	EvidenceText, SourceLabel                     string
	Location                                      Location
	Unmapped                                      map[string]any
}

type Mapping struct {
	SourceFindingID string `json:"sourceFindingId,omitempty"`
	Title           string `json:"title,omitempty"`
	SourceSeverity  string `json:"sourceSeverity,omitempty"`
	SourceLocation  string `json:"sourceLocation,omitempty"`
	SourceLine      string `json:"sourceLine,omitempty"`
	Impact          string `json:"impact,omitempty"`
	Remediation     string `json:"remediation,omitempty"`
	Description     string `json:"description,omitempty"`
}

func Supported(format string) bool {
	switch format {
	case "sarif", "trivy", "zap", "gitleaks", "generic-json", "generic-csv", "manual":
		return true
	}
	return false
}

func ValidateMapping(format string, m Mapping) error {
	if !Supported(format) {
		return ErrUnsupported
	}
	if format != "generic-json" && format != "generic-csv" {
		if m != (Mapping{}) {
			return ErrInvalid
		}
		return nil
	}
	if strings.TrimSpace(m.SourceFindingID) == "" || strings.TrimSpace(m.Title) == "" {
		return ErrInvalid
	}
	seen := make(map[string]bool)
	for _, key := range m.fields() {
		if len(key) > 256 || strings.ContainsRune(key, 0) {
			return ErrInvalid
		}
		if key != "" {
			if seen[key] {
				return ErrInvalid
			}
			seen[key] = true
		}
	}
	return nil
}

func (m Mapping) fields() []string {
	return []string{m.SourceFindingID, m.Title, m.SourceSeverity, m.SourceLocation, m.SourceLine, m.Impact, m.Remediation, m.Description}
}

func Parse(format string, data []byte, mapping Mapping) ([]Finding, error) {
	if err := ValidateMapping(format, mapping); err != nil {
		return nil, err
	}
	if len(data) == 0 || len(data) > 32<<20 || !utf8.Valid(data) {
		return nil, ErrInvalid
	}
	if format == "generic-csv" {
		return parseCSV(data, mapping)
	}
	root, err := decodeJSON(data)
	if err != nil {
		return nil, err
	}
	var findings []Finding
	switch format {
	case "sarif":
		findings, err = parseSARIF(root)
	case "trivy":
		findings, err = parseTrivy(root)
	case "zap":
		findings, err = parseZAP(root)
	case "gitleaks":
		findings, err = parseGitleaks(root)
	case "generic-json":
		findings, err = parseGeneric(root, mapping)
	case "manual":
		findings, err = parseManual(root)
	}
	if err != nil {
		return nil, ErrInvalid
	}
	if len(findings) > maxFindings {
		return nil, ErrInvalid
	}
	for _, f := range findings {
		if !validFinding(f) {
			return nil, ErrInvalid
		}
	}
	if findings == nil {
		findings = []Finding{}
	}
	return findings, nil
}

func decodeJSON(data []byte) (any, error) {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.UseNumber()
	var v any
	if dec.Decode(&v) != nil {
		return nil, ErrInvalid
	}
	var extra any
	if dec.Decode(&extra) != io.EOF {
		return nil, ErrInvalid
	}
	nodes := 0
	var bounded func(any, int) bool
	bounded = func(v any, depth int) bool {
		nodes++
		if depth > 64 || nodes > 250000 {
			return false
		}
		switch t := v.(type) {
		case map[string]any:
			for k, child := range t {
				if strings.ContainsRune(k, 0) || !bounded(child, depth+1) {
					return false
				}
			}
		case []any:
			for _, child := range t {
				if !bounded(child, depth+1) {
					return false
				}
			}
		case string:
			if strings.ContainsRune(t, 0) {
				return false
			}
		}
		return true
	}
	if !bounded(v, 0) {
		return nil, ErrInvalid
	}
	return v, nil
}

func obj(v any) map[string]any {
	m, _ := v.(map[string]any)
	return m
}

func array(v any) ([]any, bool) {
	a, ok := v.([]any)
	return a, ok
}

func str(v any) string {
	s, _ := v.(string)
	return s
}

func scalar(v any) (string, bool) {
	switch s := v.(type) {
	case string:
		return s, true
	case json.Number:
		return s.String(), true
	}
	return "", false
}

func sourceLine(v any) (int, error) {
	if v == nil || v == "" {
		return 0, nil
	}
	s, ok := scalar(v)
	if !ok {
		return 0, ErrInvalid
	}
	n, err := strconv.ParseInt(s, 10, 32)
	if err != nil || n < 0 {
		return 0, ErrInvalid
	}
	return int(n), nil
}

func text(v any) string {
	m := obj(v)
	if s := str(m["text"]); s != "" {
		return s
	}
	return str(m["markdown"])
}

func first(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func remaining(m map[string]any, fields ...string) map[string]any {
	result := make(map[string]any, len(m))
	for key, value := range m {
		result[key] = value
	}
	for _, key := range fields {
		delete(result, key)
	}
	return result
}

func identity(parts ...string) string {
	raw, _ := json.Marshal(parts)
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

func severity(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "critical":
		return "critical"
	case "high", "error", "3":
		return "high"
	case "medium", "moderate", "warning", "2":
		return "medium"
	case "low", "1":
		return "low"
	default:
		return "info"
	}
}

func validFinding(f Finding) bool {
	return f.Identity != "" && strings.TrimSpace(f.SourceFindingID) != "" &&
		len(f.SourceFindingID) <= 4096 && strings.TrimSpace(f.Title) != "" &&
		len(f.Title) <= 4096 && len(f.Location.URI) <= 8192 && f.Location.Line >= 0 &&
		len(f.Description) <= 1<<20 && len(f.Remediation) <= 1<<20 &&
		len(f.Impact) <= 1<<20 && len(f.EvidenceText) <= 1<<20
}
