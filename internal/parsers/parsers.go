// Package parsers admits bounded report data, not scanner commands or executable
// mappings. Source scan time is supplied by the import envelope, never guessed
// from report generation or commit timestamps.
package parsers

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/bahadrdsr/aspm/pkg/reportadapter"
)

var (
	ErrUnsupported = reportadapter.ErrUnsupported
	ErrInvalid     = reportadapter.ErrInvalid
)

const maxFindings = reportadapter.MaxFindings

type Location = reportadapter.Location
type Finding = reportadapter.Finding
type Mapping = reportadapter.Mapping

func Supported(format string) bool {
	return defaultRegistry.Supports(format)
}

func ValidateMapping(format string, m Mapping) error {
	return defaultRegistry.ValidateMapping(format, m)
}

func Parse(format string, data []byte, mapping Mapping) ([]Finding, error) {
	return defaultRegistry.Parse(format, data, mapping)
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
	return reportadapter.ValidateFinding(f)
}
