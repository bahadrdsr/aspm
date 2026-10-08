package exampleadapter

import (
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/bahadrdsr/aspm/pkg/reportadapter"
	"github.com/bahadrdsr/aspm/pkg/reportadapter/adaptertest"
)

func TestExampleAdapterConformanceAndRegistration(t *testing.T) {
	valid, err := os.ReadFile("testdata/example-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	unsupported := []byte(strings.Replace(string(valid), `"example/v1"`, `"example/v2"`, 1))
	items := make([]map[string]any, reportadapter.MaxFindings+1)
	for i := range items {
		items[i] = map[string]any{
			"id": "source-" + strconv.Itoa(i+1), "title": "Synthetic finding",
			"description": "Synthetic description.", "severity": "HIGH",
			"uri": "src/example.go", "line": 9, "impact": "Synthetic impact.",
			"remediation": "Review the synthetic fixture.", "vendorField": "retained",
		}
	}
	overLimit, err := json.Marshal(map[string]any{"profile": "example/v1", "findings": items})
	if err != nil {
		t.Fatal(err)
	}
	adapter := New()
	adaptertest.Run(t, adaptertest.Suite{
		Adapter: adapter, Valid: valid, UnsupportedVersion: unsupported, OverLimit: overLimit,
		Expected: adaptertest.Expected{
			SourceFindingID: "EXAMPLE-1", Title: "Synthetic example finding",
			SourceSeverity: "HIGH", Severity: "high",
			Location:    reportadapter.Location{URI: "src/example.go", Line: 9},
			Impact:      "Synthetic example impact.",
			Remediation: "Review the synthetic example.",
			Unmapped:    map[string]any{"vendorField": "retained"},
		},
	})
	registry, err := reportadapter.NewRegistry(adapter)
	if err != nil || !registry.Supports("example-json") {
		t.Fatalf("example adapter did not register: %v", err)
	}
	if findings, err := registry.Parse("example-json", valid, reportadapter.Mapping{}); err != nil || len(findings) != 1 {
		t.Fatalf("registered example adapter did not dispatch: findings=%d err=%v", len(findings), err)
	}
}
