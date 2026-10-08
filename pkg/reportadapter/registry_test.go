package reportadapter_test

import (
	"errors"
	"testing"

	"github.com/bahadrdsr/aspm/pkg/reportadapter"
)

type stubAdapter struct {
	descriptor reportadapter.Descriptor
}

func (a stubAdapter) Descriptor() reportadapter.Descriptor { return a.descriptor }
func (stubAdapter) ValidateMapping(reportadapter.Mapping) error {
	return nil
}
func (stubAdapter) Parse([]byte, reportadapter.Mapping) ([]reportadapter.Finding, error) {
	return []reportadapter.Finding{{
		Identity: "identity", SourceFindingID: "source-1", Title: "Title",
		Severity: "info", Unmapped: map[string]any{},
	}}, nil
}

func stub(id string) stubAdapter {
	return stubAdapter{descriptor: reportadapter.Descriptor{
		ID: id, Name: id, Kind: "report-importer", ImplementationStatus: "implemented",
		SupportMaturity: "experimental", ReadyToImport: true,
		SupportedVersions: []string{"v1"}, FieldCoverage: []string{"title"},
		LifecycleCapabilities: []string{"stable-source-identity"},
		MappingMode:           "none", TestFixture: "testdata/" + id + ".json",
	}}
}

func TestRegistryIsSortedImmutableAndRejectsDuplicates(t *testing.T) {
	registry, err := reportadapter.NewRegistry(stub("zeta"), stub("alpha"))
	if err != nil {
		t.Fatal(err)
	}
	descriptors := registry.Descriptors()
	if len(descriptors) != 2 || descriptors[0].ID != "alpha" || descriptors[1].ID != "zeta" {
		t.Fatal("registry descriptors are not deterministic")
	}
	descriptors[0].SupportedVersions[0] = "mutated"
	if registry.Descriptors()[0].SupportedVersions[0] != "v1" {
		t.Fatal("registry descriptor slices are mutable through callers")
	}
	if _, err := reportadapter.NewRegistry(stub("same"), stub("same")); !errors.Is(err, reportadapter.ErrInvalid) {
		t.Fatal("duplicate adapter identifiers were accepted")
	}
	var typedNil *stubAdapter
	if _, err := reportadapter.NewRegistry(typedNil); !errors.Is(err, reportadapter.ErrInvalid) {
		t.Fatal("typed nil adapter was accepted")
	}
	if _, err := reportadapter.Parse(typedNil, []byte("{}"), reportadapter.Mapping{}); !errors.Is(err, reportadapter.ErrInvalid) {
		t.Fatal("typed nil adapter parse did not fail safely")
	}
	if registry.Supports("missing") {
		t.Fatal("registry reported an unknown adapter as supported")
	}
	if _, err := registry.Parse("missing", []byte("{}"), reportadapter.Mapping{}); !errors.Is(err, reportadapter.ErrUnsupported) {
		t.Fatal("unknown registry format did not return ErrUnsupported")
	}
}
