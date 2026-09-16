//go:build integration

package acceptance

import (
	"bytes"
	"strings"
	"testing"
)

type parserCase struct {
	format, file, title, sourceID, sourceSeverity, severity, uri, impact, remediation string
	line                                                                              int
	unmapped                                                                          object
	mapping                                                                           object
}

func checkParser(t *testing.T, c parserCase) {
	t.Helper()
	h := newHarness(t, true)
	a := h.asset(h.admin, "Parser fixture repository", nil)
	raw := fixture(t, c.file)
	input := h.input(a.ID, c.format, raw)
	input["sourceScanAt"] = nil
	if c.mapping != nil {
		input["mapping"] = c.mapping
	}
	run := h.finish(h.upload(input).ID, "succeeded")
	work := h.work(h.admin, "")
	equal(t, "parsed work count", len(work), 1)
	f := h.finding(h.admin, work[0].ID)
	equal(t, "preserved source title", f.Title, c.title)
	description := c.impact
	if c.mapping != nil || c.format == "manual" {
		description = "Synthetic portable description."
	}
	equal(t, "preserved source description", f.Description, description)
	equal(t, "normalized work severity", f.Severity, c.severity)
	equal(t, "parsed observation count", len(f.Observations), 1)
	o := f.Observations[0]
	equal(t, "source-native identity", o.SourceFindingID, c.sourceID)
	equal(t, "source severity retained", o.SourceSeverity, c.sourceSeverity)
	equal(t, "normalized observation severity", o.NormalizedSeverity, c.severity)
	equal(t, "source location URI", o.SourceLocation.URI, c.uri)
	equal(t, "source location line", o.SourceLocation.Line, c.line)
	equal(t, "source impact", o.Impact, c.impact)
	equal(t, "source identity", o.SourceID, "acceptance-source")
	equal(t, "scan identity", o.ScanID, "scan-1")
	equal(t, "run provenance", o.RunID, run.RunID)
	equal(t, "scope provenance", o.Scope, scope{"owned-repository", "1", "refs/heads/main"})
	sameTime(t, "unknown work scan time", f.SourceScanAt, nil)
	sameTime(t, "unknown observation scan time", o.SourceScanAt, nil)
	equal(t, "unverified imported evidence", f.Evidence.VerificationState, "not-run")
	if c.remediation != "" {
		if c.format == "trivy" {
			if !strings.Contains(f.Remediation, c.remediation) {
				t.Fatal("Trivy remediation lost the source fixed version")
			}
		} else {
			equal(t, "source remediation", f.Remediation, c.remediation)
			equal(t, "observation remediation", o.Remediation, c.remediation)
		}
	}
	for field, want := range c.unmapped {
		equal(t, "preserved native field "+field, o.Unmapped[field], want)
	}
	equal(t, "immutable report digest", o.EvidenceDigest, digest(raw))
	if !bytes.Equal(h.request(h.admin, "GET", "/api/v1/imports/"+run.ID+"/evidence", nil, 200).Body.Bytes(), raw) {
		t.Fatal("parser intake lost exact original report bytes")
	}
}

func TestM07_GenericJSONCSVAndManualPreserveMappedFields(t *testing.T) {
	requireApplication(t)
	mapping := object{"sourceFindingId": "ticket", "title": "summary", "sourceSeverity": "risk",
		"sourceLocation": "path", "sourceLine": "line", "impact": "impact", "remediation": "fix", "description": "details"}
	for _, format := range []string{"generic-json", "generic-csv", "manual"} {
		t.Run(format, func(t *testing.T) {
			c := parserCase{format: format, file: format + ".json", title: "Synthetic portable finding", sourceID: "portable-1",
				sourceSeverity: "HIGH", severity: "high", uri: "src/portable.go", line: 7, impact: "Synthetic portable impact.",
				remediation: "Review the portable fixture.", unmapped: object{"vendorNote": `Keep, "quoted" source note.`}, mapping: mapping}
			if format == "generic-csv" {
				c.file = "generic.csv"
			}
			if format == "manual" {
				c.sourceSeverity, c.mapping = "high", nil
			}
			checkParser(t, c)
		})
	}
}

func TestM07_TrivyZAPAndGitleaksPreserveSourceMeaning(t *testing.T) {
	requireApplication(t)
	for _, c := range []parserCase{
		{format: "trivy", file: "trivy.json", title: "Synthetic package advisory", sourceID: "SYN-TRIVY-1",
			sourceSeverity: "HIGH", severity: "high", uri: "fixture-image (alpine 3.20)", impact: "Synthetic package impact.", remediation: "1.0.1",
			unmapped: object{"PkgName": "fixture-lib", "InstalledVersion": "1.0.0", "FixedVersion": "1.0.1", "VendorNote": "Keep the Trivy source note."}},
		{format: "zap", file: "zap.json", title: "Synthetic header policy", sourceID: "10038",
			sourceSeverity: "2", severity: "medium", uri: "https://fixture.example.invalid/health", impact: "Synthetic response header finding.",
			remediation: "Review synthetic header policy.", unmapped: object{"confidence": "2", "otherinfo": "Keep the ZAP source note."}},
		{format: "gitleaks", file: "gitleaks.json", title: "Synthetic redacted credential fixture",
			sourceID: "0123456789abcdef0123456789abcdef01234567:config/example.txt:synthetic-rule:4", severity: "high",
			uri: "config/example.txt", line: 4, impact: "Synthetic redacted credential fixture",
			unmapped: object{"RuleID": "synthetic-rule", "Commit": "0123456789abcdef0123456789abcdef01234567", "Date": "2026-09-01T09:00:00Z"}},
	} {
		t.Run(c.format, func(t *testing.T) { checkParser(t, c) })
	}
}

func TestM07_CatalogHasExactlyEightHonestReadOnlyNativeFamilies(t *testing.T) {
	h := newHarness(t, true)
	viewer := h.addUser(h.admin, "viewer")
	r := h.json(viewer, "GET", "/api/v1/integrations/catalog", nil, 200)
	if r.DataOrigin != "live" && r.DataOrigin != "synthetic" {
		t.Fatal("catalog must retain the M01 dataOrigin contract")
	}
	expected := map[string]string{"github": "GitHub", "gitlab": "GitLab", "azure-devops": "Azure DevOps", "aws": "AWS",
		"azure": "Azure", "jira": "Jira", "teams": "Microsoft Teams", "slack": "Slack"}
	catalog := items[struct {
		ID, Name, Kind, SupportMaturity, ConnectionState string
		Capabilities                                     []string
		ReadyToConnect                                   bool
		LiveVerification                                 struct{ State, Reason string }
	}](t, r)
	equal(t, "native family count excludes import format aliases", len(catalog), len(expected))
	for _, entry := range catalog {
		name, present := expected[entry.ID]
		if !present {
			t.Fatal("duplicate or undeclared native integration family")
		}
		equal(t, "catalog family name", entry.Name, name)
		equal(t, "native kind", entry.Kind, "native")
		equal(t, "pre-M08/M09 support is planned", entry.SupportMaturity, "planned")
		equal(t, "fresh installation is unconfigured", entry.ConnectionState, "unconfigured")
		equal(t, "planned native family is not ready", entry.ReadyToConnect, false)
		equal(t, "no native verification has run", entry.LiveVerification.State, "not-run")
		if entry.LiveVerification.Reason == "" || entry.Capabilities == nil {
			t.Fatal("catalog needs a truthful verification explanation and capability array")
		}
		delete(expected, entry.ID)
	}
	h.denied(h.admin, "POST", "/api/v1/integrations/catalog", object{}, 405, "method-not-allowed")
}
