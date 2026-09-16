package parsers

import (
	"errors"
	"strings"
	"testing"
)

func TestSARIFStableGUIDRetainsLocationAndSourceMeaning(t *testing.T) {
	raw := `{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"Owned report","rules":[
		{"id":"R1","shortDescription":{"text":"Owned title"},"fullDescription":{"text":"Description"},"help":{"text":"Remediation"}}]}},
		"results":[{"guid":"stable-guid","ruleId":"R1","level":"warning","message":{"text":"Evidence"},
		"locations":[{"physicalLocation":{"artifactLocation":{"uri":"source.go"},"region":{"startLine":12}}}],
		"properties":{"impact":"Impact","vendorNote":{"retained":true}}}]}]}`
	before, err := Parse("sarif", []byte(raw), Mapping{})
	if err != nil || len(before) != 1 {
		t.Fatal("valid SARIF fixture was not parsed")
	}
	after, err := Parse("sarif", []byte(strings.Replace(raw, `"startLine":12`, `"startLine":19`, 1)), Mapping{})
	if err != nil || len(after) != 1 {
		t.Fatal("valid moved SARIF fixture was not parsed")
	}
	if before[0].Identity != after[0].Identity || before[0].Location.Line != 12 || after[0].Location.Line != 19 ||
		before[0].SourceSeverity != "warning" || before[0].Severity != "medium" ||
		before[0].Title != "Owned title" || before[0].EvidenceText != "Evidence" ||
		before[0].Impact != "Impact" || before[0].Unmapped["vendorNote"] == nil {
		t.Fatal("SARIF source meaning or stable identity was lost")
	}
}

func TestMalformedOrUnadmittedReportsNeverBecomeEmptySuccess(t *testing.T) {
	for _, tc := range []struct{ format, body string }{
		{"sarif", `{`}, {"sarif", `{}`}, {"sarif", `{"version":"2.0.0","runs":[]}`},
		{"sarif", `{"version":"2.1.0","runs":[{"tool":{"driver":{"name":"Owned"}}}]}`},
		{"trivy", `{"SchemaVersion":1,"Results":[]}`},
		{"trivy", `{"SchemaVersion":2,"Results":[{"Target":"owned","OtherResults":[]}]}`},
		{"zap", `{"@version":"1.0","site":[]}`}, {"gitleaks", `{}`},
		{"manual", `{"sourceFindingId":"owned","title":"Owned","sourceLocation":{"uri":"source","line":-1}}`},
	} {
		t.Run(tc.format+tc.body, func(t *testing.T) {
			if _, err := Parse(tc.format, []byte(tc.body), Mapping{}); !errors.Is(err, ErrInvalid) {
				t.Fatal("unadmitted input did not produce a parser failure")
			}
		})
	}
	if _, err := Parse("sarif", []byte(strings.Repeat("[", 65)+"0"+strings.Repeat("]", 65)), Mapping{}); !errors.Is(err, ErrInvalid) {
		t.Fatal("deep input was not bounded")
	}
}

func TestMappedCSVUsesLiteralColumnsAndPreservesQuotedFields(t *testing.T) {
	mapping := Mapping{SourceFindingID: "ticket", Title: "summary", SourceSeverity: "risk", SourceLine: "line"}
	result, err := Parse("generic-csv", []byte("ticket,summary,risk,line,note\n1,Owned,HIGH,7,\"Keep, \"\"quoted\"\".\"\n"), mapping)
	if err != nil || len(result) != 1 || result[0].Location.Line != 7 ||
		result[0].Severity != "high" || result[0].Unmapped["note"] != `Keep, "quoted".` {
		t.Fatal("mapped quoted CSV fields were not preserved")
	}
	mapping.Title = "not a column"
	if _, err := Parse("generic-csv", []byte("ticket,summary\n1,Owned\n"), mapping); !errors.Is(err, ErrInvalid) {
		t.Fatal("missing literal mapping column must be rejected")
	}
}
