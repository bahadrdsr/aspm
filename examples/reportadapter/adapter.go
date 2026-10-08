// Package exampleadapter demonstrates a safe compiled-in report adapter.
package exampleadapter

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"strconv"
	"strings"

	"github.com/bahadrdsr/aspm/pkg/reportadapter"
)

type adapter struct{}

func New() reportadapter.Adapter {
	return adapter{}
}

func (adapter) Descriptor() reportadapter.Descriptor {
	return reportadapter.Descriptor{
		ID: "example-json", Name: "Example JSON", Kind: "report-importer",
		ImplementationStatus: "implemented", SupportMaturity: "experimental",
		ReadyToImport: true, SupportedVersions: []string{"example/v1"},
		FieldCoverage: []string{
			"sourceFindingId", "title", "sourceSeverity", "normalizedSeverity",
			"sourceLocation", "impact", "remediation", "unmapped",
		},
		LifecycleCapabilities: []string{
			"stable-source-identity", "per-run-observation", "source-time-envelope",
			"completeness-envelope", "change-classification",
		},
		MappingMode: "none", TestFixture: "examples/reportadapter/testdata/example-v1.json",
	}
}

func (adapter) ValidateMapping(mapping reportadapter.Mapping) error {
	if mapping != (reportadapter.Mapping{}) {
		return reportadapter.ErrInvalid
	}
	return nil
}

func (adapter) Parse(data []byte, _ reportadapter.Mapping) ([]reportadapter.Finding, error) {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var document map[string]any
	if decoder.Decode(&document) != nil || decoder.Decode(new(any)) != io.EOF ||
		text(document["profile"]) != "example/v1" {
		return nil, reportadapter.ErrInvalid
	}
	items, ok := document["findings"].([]any)
	if !ok {
		return nil, reportadapter.ErrInvalid
	}
	findings := make([]reportadapter.Finding, 0, len(items))
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok {
			return nil, reportadapter.ErrInvalid
		}
		line, err := line(item["line"])
		if err != nil {
			return nil, err
		}
		sourceID := text(item["id"])
		finding := reportadapter.Finding{
			Identity: identity("example-json", sourceID), SourceFindingID: sourceID,
			Title: text(item["title"]), Description: text(item["description"]),
			SourceSeverity: text(item["severity"]), Severity: severity(text(item["severity"])),
			Location: reportadapter.Location{URI: text(item["uri"]), Line: line},
			Impact:   text(item["impact"]), Remediation: text(item["remediation"]),
			EvidenceText: text(item["description"]), SourceLabel: "Example JSON report",
			Unmapped: remaining(item, "id", "title", "description", "severity", "uri", "line", "impact", "remediation"),
		}
		findings = append(findings, finding)
	}
	return findings, nil
}

func identity(parts ...string) string {
	raw, _ := json.Marshal(parts)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

func severity(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "critical":
		return "critical"
	case "high":
		return "high"
	case "medium":
		return "medium"
	case "low":
		return "low"
	default:
		return "info"
	}
}

func text(value any) string {
	result, _ := value.(string)
	return result
}

func line(value any) (int, error) {
	number, ok := value.(json.Number)
	if !ok {
		return 0, reportadapter.ErrInvalid
	}
	parsed, err := strconv.ParseInt(number.String(), 10, 32)
	if err != nil || parsed < 0 {
		return 0, reportadapter.ErrInvalid
	}
	return int(parsed), nil
}

func remaining(item map[string]any, names ...string) map[string]any {
	result := make(map[string]any, len(item))
	for name, value := range item {
		result[name] = value
	}
	for _, name := range names {
		delete(result, name)
	}
	return result
}
