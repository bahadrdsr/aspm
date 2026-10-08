package parsers

import (
	"strings"

	"github.com/bahadrdsr/aspm/pkg/reportadapter"
)

type compiledAdapter struct {
	descriptor reportadapter.Descriptor
	validate   func(Mapping) error
	parse      func([]byte, Mapping) ([]Finding, error)
}

func (a compiledAdapter) Descriptor() reportadapter.Descriptor { return a.descriptor }
func (a compiledAdapter) ValidateMapping(mapping Mapping) error {
	return a.validate(mapping)
}
func (a compiledAdapter) Parse(data []byte, mapping Mapping) ([]Finding, error) {
	return a.parse(data, mapping)
}

func noMapping(mapping Mapping) error {
	if mapping != (Mapping{}) {
		return ErrInvalid
	}
	return nil
}

func declarativeMapping(mapping Mapping) error {
	if strings.TrimSpace(mapping.SourceFindingID) == "" || strings.TrimSpace(mapping.Title) == "" {
		return ErrInvalid
	}
	seen := make(map[string]bool)
	for _, key := range mapping.Fields() {
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

func jsonAdapter(descriptor reportadapter.Descriptor, parse func(any) ([]Finding, error)) compiledAdapter {
	return compiledAdapter{
		descriptor: descriptor,
		validate:   noMapping,
		parse: func(data []byte, _ Mapping) ([]Finding, error) {
			root, err := decodeJSON(data)
			if err != nil {
				return nil, err
			}
			return parse(root)
		},
	}
}

func descriptor(id, name, kind, version, fixture string, mapping bool, omittedFields ...string) reportadapter.Descriptor {
	mode := "none"
	if mapping {
		mode = "declarative-fields"
	}
	omitted := make(map[string]bool, len(omittedFields))
	for _, field := range omittedFields {
		omitted[field] = true
	}
	coverage := make([]string, 0, 8-len(omitted))
	for _, field := range []string{
		"sourceFindingId", "title", "sourceSeverity", "normalizedSeverity",
		"sourceLocation", "impact", "remediation", "unmapped",
	} {
		if !omitted[field] {
			coverage = append(coverage, field)
		}
	}
	return reportadapter.Descriptor{
		ID: id, Name: name, Kind: kind, ImplementationStatus: "implemented",
		SupportMaturity: "experimental", ReadyToImport: true,
		SupportedVersions: []string{version},
		FieldCoverage:     coverage,
		LifecycleCapabilities: []string{
			"stable-source-identity", "per-run-observation", "source-time-envelope",
			"completeness-envelope", "change-classification",
		},
		MappingMode: mode, TestFixture: fixture,
	}
}

func builtinAdapters() []reportadapter.Adapter {
	return []reportadapter.Adapter{
		jsonAdapter(descriptor("sarif", "SARIF 2.1.0", "report-importer",
			"SARIF 2.1.0", "internal/parsers/testdata/sarif-2.1.0.json", false), parseSARIF),
		jsonAdapter(descriptor("trivy", "Trivy JSON", "report-importer",
			"SchemaVersion 2", "internal/parsers/testdata/trivy-schema-2.json", false), parseTrivy),
		jsonAdapter(descriptor("zap", "OWASP ZAP JSON", "report-importer",
			"traditional JSON @version 2.16.1", "internal/parsers/testdata/zap-2.16.1.json", false), parseZAP),
		jsonAdapter(descriptor("gitleaks", "Gitleaks JSON", "report-importer",
			"v8 JSON array with Fingerprint", "internal/parsers/testdata/gitleaks-v8.json", false,
			"sourceSeverity", "remediation"), parseGitleaks),
		compiledAdapter{
			descriptor: descriptor("generic-json", "Mapped JSON", "report-importer",
				"declarative mapping v1", "internal/parsers/testdata/generic-json-v1.json", true),
			validate: declarativeMapping,
			parse: func(data []byte, mapping Mapping) ([]Finding, error) {
				root, err := decodeJSON(data)
				if err != nil {
					return nil, err
				}
				return parseGeneric(root, mapping)
			},
		},
		compiledAdapter{
			descriptor: descriptor("generic-csv", "Mapped CSV", "report-importer",
				"UTF-8 comma-delimited mapping v1", "internal/parsers/testdata/generic-csv-v1.csv", true),
			validate: declarativeMapping, parse: parseCSV,
		},
		jsonAdapter(descriptor("manual", "Manual structured finding", "manual-intake",
			"manual finding v1", "internal/parsers/testdata/manual-v1.json", false), parseManual),
	}
}

var defaultRegistry = reportadapter.MustNewRegistry(builtinAdapters()...)

func Adapters() []reportadapter.Descriptor {
	return defaultRegistry.Descriptors()
}

func ParseWithAdapter(adapter reportadapter.Adapter, data []byte, mapping Mapping) ([]Finding, error) {
	return reportadapter.Parse(adapter, data, mapping)
}
