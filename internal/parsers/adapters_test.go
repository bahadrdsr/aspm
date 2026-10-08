package parsers

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestBuiltinAdapterRegistryAndVersionFixtures(t *testing.T) {
	descriptors := Adapters()
	wantIDs := []string{"generic-csv", "generic-json", "gitleaks", "manual", "sarif", "trivy", "zap"}
	if len(descriptors) != len(wantIDs) {
		t.Fatalf("adapter count=%d want=%d", len(descriptors), len(wantIDs))
	}
	mapping := Mapping{
		SourceFindingID: "ticket", Title: "summary", SourceSeverity: "risk",
		SourceLocation: "path", SourceLine: "line", Impact: "impact",
		Remediation: "fix", Description: "details",
	}
	for i, descriptor := range descriptors {
		if descriptor.ID != wantIDs[i] || !Supported(descriptor.ID) ||
			descriptor.ImplementationStatus != "implemented" ||
			descriptor.SupportMaturity != "experimental" ||
			descriptor.CountsAsNativeFamily || !descriptor.ReadyToImport {
			t.Fatalf("invalid registry descriptor: %#v", descriptor)
		}
		path := filepath.Join("..", "..", filepath.FromSlash(descriptor.TestFixture))
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s fixture: %v", descriptor.ID, err)
		}
		selectedMapping := Mapping{}
		if descriptor.MappingMode == "declarative-fields" {
			selectedMapping = mapping
		}
		findings, err := Parse(descriptor.ID, data, selectedMapping)
		if err != nil || len(findings) != 1 {
			t.Fatalf("%s fixture was not admitted: findings=%d err=%v", descriptor.ID, len(findings), err)
		}
	}

	descriptors[0].SupportedVersions[0] = "mutated"
	if reflect.DeepEqual(descriptors, Adapters()) || Adapters()[0].SupportedVersions[0] == "mutated" {
		t.Fatal("adapter metadata is mutable through callers")
	}
}
