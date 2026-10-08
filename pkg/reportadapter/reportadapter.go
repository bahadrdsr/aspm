// Package reportadapter defines the bounded compiled-in report adapter contract.
//
// The contract accepts report bytes and declarative field mappings only. It
// does not load plugins, scripts, templates, or other executable import data.
package reportadapter

import (
	"errors"
	"reflect"
	"slices"
	"strings"
	"unicode/utf8"
)

var (
	ErrUnsupported = errors.New("unsupported report format")
	ErrInvalid     = errors.New("report does not match the admitted format")
)

const (
	MaxFindings    = 5000
	MaxReportBytes = 32 << 20
)

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

func (m Mapping) Fields() []string {
	return []string{
		m.SourceFindingID, m.Title, m.SourceSeverity, m.SourceLocation,
		m.SourceLine, m.Impact, m.Remediation, m.Description,
	}
}

type Descriptor struct {
	ID                    string   `json:"id"`
	Name                  string   `json:"name"`
	Kind                  string   `json:"kind"`
	ImplementationStatus  string   `json:"implementationStatus"`
	SupportMaturity       string   `json:"supportMaturity"`
	CountsAsNativeFamily  bool     `json:"countsAsNativeLaunchFamily"`
	ReadyToImport         bool     `json:"readyToImport"`
	SupportedVersions     []string `json:"supportedVersions"`
	FieldCoverage         []string `json:"fieldCoverage"`
	LifecycleCapabilities []string `json:"lifecycleCapabilities"`
	MappingMode           string   `json:"mappingMode"`
	TestFixture           string   `json:"deterministicTestEvidenceRef"`
}

func (d Descriptor) Clone() Descriptor {
	d.SupportedVersions = slices.Clone(d.SupportedVersions)
	d.FieldCoverage = slices.Clone(d.FieldCoverage)
	d.LifecycleCapabilities = slices.Clone(d.LifecycleCapabilities)
	return d
}

type Adapter interface {
	Descriptor() Descriptor
	ValidateMapping(Mapping) error
	Parse([]byte, Mapping) ([]Finding, error)
}

func isNilAdapter(adapter Adapter) bool {
	if adapter == nil {
		return true
	}
	value := reflect.ValueOf(adapter)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

func ValidateDescriptor(descriptor Descriptor) error {
	if !validID(descriptor.ID) || strings.TrimSpace(descriptor.Name) != descriptor.Name ||
		descriptor.Name == "" || len(descriptor.Name) > 256 ||
		(descriptor.Kind != "report-importer" && descriptor.Kind != "manual-intake") ||
		descriptor.ImplementationStatus != "implemented" ||
		(descriptor.SupportMaturity != "experimental" && descriptor.SupportMaturity != "supported") ||
		descriptor.CountsAsNativeFamily || !descriptor.ReadyToImport ||
		(descriptor.MappingMode != "none" && descriptor.MappingMode != "declarative-fields") ||
		!validReference(descriptor.TestFixture) {
		return ErrInvalid
	}
	for _, values := range [][]string{
		descriptor.SupportedVersions,
		descriptor.FieldCoverage,
		descriptor.LifecycleCapabilities,
	} {
		if !validSet(values) {
			return ErrInvalid
		}
	}
	return nil
}

func ValidateFinding(finding Finding) bool {
	return strings.TrimSpace(finding.Identity) != "" && len(finding.Identity) <= 4096 &&
		strings.TrimSpace(finding.SourceFindingID) != "" && len(finding.SourceFindingID) <= 4096 &&
		strings.TrimSpace(finding.Title) != "" && len(finding.Title) <= 4096 &&
		contains([]string{"critical", "high", "medium", "low", "info"}, finding.Severity) &&
		len(finding.SourceSeverity) <= 4096 && len(finding.Location.URI) <= 8192 &&
		finding.Location.Line >= 0 && len(finding.Description) <= 1<<20 &&
		len(finding.Remediation) <= 1<<20 && len(finding.Impact) <= 1<<20 &&
		len(finding.EvidenceText) <= 1<<20 && len(finding.SourceLabel) <= 4096
}

func Parse(adapter Adapter, data []byte, mapping Mapping) ([]Finding, error) {
	if isNilAdapter(adapter) || ValidateDescriptor(adapter.Descriptor()) != nil {
		return nil, ErrInvalid
	}
	if err := adapter.ValidateMapping(mapping); err != nil {
		if errors.Is(err, ErrUnsupported) {
			return nil, ErrUnsupported
		}
		return nil, ErrInvalid
	}
	if len(data) == 0 || len(data) > MaxReportBytes || !utf8.Valid(data) {
		return nil, ErrInvalid
	}
	findings, err := adapter.Parse(data, mapping)
	if err != nil {
		if errors.Is(err, ErrUnsupported) {
			return nil, ErrUnsupported
		}
		return nil, ErrInvalid
	}
	if len(findings) > MaxFindings {
		return nil, ErrInvalid
	}
	for _, finding := range findings {
		if !ValidateFinding(finding) {
			return nil, ErrInvalid
		}
	}
	if findings == nil {
		findings = []Finding{}
	}
	return findings, nil
}

func validID(value string) bool {
	if value == "" || len(value) > 64 || value[0] < 'a' || value[0] > 'z' || value[len(value)-1] == '-' {
		return false
	}
	for _, char := range value {
		if char != '-' && (char < 'a' || char > 'z') && (char < '0' || char > '9') {
			return false
		}
	}
	return !strings.Contains(value, "--")
}

func validReference(value string) bool {
	if value == "" || len(value) > 512 || strings.Contains(value, `\`) ||
		strings.HasPrefix(value, "/") || strings.Contains(value, ":") {
		return false
	}
	for _, part := range strings.Split(value, "/") {
		if part == "" || part == "." || part == ".." {
			return false
		}
	}
	return true
}

func validSet(values []string) bool {
	if len(values) == 0 || len(values) > 64 {
		return false
	}
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) != value || value == "" || len(value) > 256 || seen[value] {
			return false
		}
		seen[value] = true
	}
	return true
}

func contains(values []string, candidate string) bool {
	return slices.Contains(values, candidate)
}
