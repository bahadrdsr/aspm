package parsers

import (
	"bytes"
	"encoding/csv"
	"io"
	"strings"
)

func parseGeneric(root any, mapping Mapping) ([]Finding, error) {
	items, ok := array(root)
	if !ok || len(items) > maxFindings {
		return nil, ErrInvalid
	}
	result := make([]Finding, 0, len(items))
	for _, item := range items {
		f, err := mappedFinding(obj(item), mapping)
		if err != nil {
			return nil, err
		}
		f.SourceLabel = "Mapped JSON report"
		result = append(result, f)
	}
	return result, nil
}

func parseCSV(data []byte, mapping Mapping) ([]Finding, error) {
	reader := csv.NewReader(bytes.NewReader(data))
	reader.ReuseRecord = true
	headers, err := reader.Read()
	if err != nil || len(headers) > 256 {
		return nil, ErrInvalid
	}
	headers = append([]string(nil), headers...)
	seen := make(map[string]bool, len(headers))
	for _, header := range headers {
		if header == "" || len(header) > 256 || seen[header] || strings.ContainsRune(header, 0) {
			return nil, ErrInvalid
		}
		seen[header] = true
	}
	for _, field := range mapping.fields() {
		if field != "" && !seen[field] {
			return nil, ErrInvalid
		}
	}
	result := []Finding{}
	for {
		record, err := reader.Read()
		if err == io.EOF {
			return result, nil
		}
		if err != nil || len(result) >= maxFindings {
			return nil, ErrInvalid
		}
		item := make(map[string]any, len(headers))
		for i, value := range record {
			if strings.ContainsRune(value, 0) {
				return nil, ErrInvalid
			}
			item[headers[i]] = value
		}
		f, err := mappedFinding(item, mapping)
		if err != nil || !validFinding(f) {
			return nil, ErrInvalid
		}
		f.SourceLabel = "Mapped CSV report"
		result = append(result, f)
	}
}

func mappedFinding(item map[string]any, mapping Mapping) (Finding, error) {
	if item == nil {
		return Finding{}, ErrInvalid
	}
	for _, field := range mapping.fields() {
		if field != "" {
			if _, exists := item[field]; !exists {
				return Finding{}, ErrInvalid
			}
		}
	}
	line, err := sourceLine(item[mapping.SourceLine])
	if err != nil {
		return Finding{}, err
	}
	f := Finding{
		SourceFindingID: str(item[mapping.SourceFindingID]), Title: str(item[mapping.Title]),
		SourceSeverity: str(item[mapping.SourceSeverity]), Description: str(item[mapping.Description]),
		Impact: str(item[mapping.Impact]), Remediation: str(item[mapping.Remediation]),
		Location: Location{URI: str(item[mapping.SourceLocation]), Line: line},
		Unmapped: remaining(item, mapping.fields()...),
	}
	f.Severity, f.EvidenceText = severity(f.SourceSeverity), first(f.Description, f.Title)
	f.Identity = identity("mapped", f.SourceFindingID)
	return f, nil
}

func parseManual(root any) ([]Finding, error) {
	item := obj(root)
	if item == nil {
		return nil, ErrInvalid
	}
	loc := obj(item["sourceLocation"])
	if loc == nil {
		return nil, ErrInvalid
	}
	line, err := sourceLine(loc["line"])
	if err != nil {
		return nil, err
	}
	f := Finding{
		SourceFindingID: str(item["sourceFindingId"]), Title: str(item["title"]),
		SourceSeverity: str(item["severity"]), Description: str(item["description"]),
		Impact: str(item["impact"]), Remediation: str(item["remediation"]),
		Location: Location{URI: str(loc["uri"]), Line: line}, SourceLabel: "Manual report",
		Unmapped: remaining(item, "sourceFindingId", "title", "severity", "description", "impact", "remediation", "sourceLocation"),
	}
	f.Severity, f.EvidenceText = severity(f.SourceSeverity), first(f.Description, f.Title)
	f.Identity = identity("manual", f.SourceFindingID)
	return []Finding{f}, nil
}
