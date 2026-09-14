package contracts

import (
	"crypto/sha256"
	"fmt"
	"strings"
	"testing"
	"time"
)

func scanTime(t *testing.T, record document) *time.Time {
	t.Helper()
	value := get(t, record, "sourceScanAt")
	if value == nil {
		return nil
	}
	timestamp, err := time.Parse(time.RFC3339, textAt(t, record, "sourceScanAt"))
	if err != nil {
		t.Fatalf("sourceScanAt must be RFC3339 or null, not a collection-time substitute: %v", err)
	}
	return &timestamp
}

func assertSyntheticScan(t *testing.T, record document) {
	t.Helper()
	for _, field := range []string{"workspaceId", "sourceId", "scanId", "scopeId", "importAttemptId", "parserRevision"} {
		if !strings.HasPrefix(textAt(t, record, field), "synthetic-") {
			t.Errorf("%s must be a synthetic fixture identifier", field)
		}
	}
	collected, err := time.Parse(time.RFC3339, textAt(t, record, "collectedAt"))
	if err != nil {
		t.Fatal(err)
	}
	imported, err := time.Parse(time.RFC3339, textAt(t, record, "importedAt"))
	if err != nil {
		t.Fatal(err)
	}
	if imported.Before(collected) {
		t.Error("representative fixture import time cannot precede collection")
	}
	if scanned := scanTime(t, record); scanned != nil && !collected.After(*scanned) {
		t.Error("fixture must visibly distinguish source scan time from later collection/import time")
	}
	if !contains([]string{"succeeded", "failed", "unknown"}, textAt(t, record, "sourceStatus")) ||
		!contains([]string{"complete", "partial", "unknown"}, textAt(t, record, "completeness")) ||
		!contains([]string{"full", "delta"}, textAt(t, record, "scanKind")) ||
		!contains([]string{"reconciled", "partial", "failed"}, textAt(t, record, "processingStatus")) {
		t.Error("fixture must distinguish source success, source completeness, scan kind, and local processing state")
	}
	if _, ok := get(t, record, "hasFinding").(bool); !ok {
		t.Error("hasFinding must be explicit")
	}
}

func TestSourceFidelityAndLifecycle(t *testing.T) {
	root := artifact(t, "lifecycle.contract.json", "LifecycleContract")
	equal(t, root, "fixturePolicy.data", "synthetic")
	requireFlags(t, root, []string{
		"fixturePolicy.containsCredentials", "fixturePolicy.networkCalls",
		"activeVerification.defaultEnabled", "activeVerification.arbitraryShell", "activeVerification.autonomousOffense",
	}, false)
	requireSet(t, root, "activeVerification.allowedMethods", true, "deterministic-evidence", "controlled-fixtures")
	requireFlags(t, root, []string{
		"rules.sourceTimesSeparate", "rules.importAttemptsSeparateFromScans", "rules.immutableOriginalEvidence",
		"rules.preserveSourceVariants", "rules.preserveSourceSeverity", "rules.scopeAwareAbsence",
		"rules.rejectStaleLifecycleEvents", "rules.humanDecisionsOwnedByCore", "rules.processingCompletenessSeparate",
	}, true)
	requireFlags(t, root, []string{
		"rules.pollRefreshesScanTime", "rules.nonReproductionMeansFalsePositive", "rules.scannerResolutionMeansVerifiedResolution",
	}, false)
	models := indexed(t, root, "models")
	for id, fields := range map[string][]string{
		"run": {
			"workspaceId", "sourceId", "scanId", "scopeId", "sourceScanAt", "collectedAt", "importedAt",
			"importAttemptId", "parserRevision", "completeness", "processingStatus", "evidenceDigest",
		},
		"observation": {
			"observationId", "scanId", "sourceVariantId", "affectedInstanceId", "sourceSeverity",
			"normalizedSeverity", "sourceLocation", "originalEvidenceRef",
		},
		"canonical-issue":   {"canonicalIssueId", "workspaceId", "workflowState", "disposition", "analysisConclusion", "proofOutcome"},
		"source-variant":    {"sourceVariantId", "sourceId", "sourceFindingId"},
		"affected-instance": {"affectedInstanceId", "assetId", "location", "version", "environment"},
	} {
		model, exists := models[id]
		if !exists {
			t.Errorf("missing domain model contract %q", id)
			continue
		}
		requireSet(t, model, "fields", false, fields...)
	}
	requireSet(t, root, "stateAxes.workflowState", false, "open", "in-progress", "resolved")
	requireSet(t, root, "stateAxes.disposition", false, "none", "accepted-risk", "suppressed", "false-positive")
	requireSet(t, root, "stateAxes.analysisConclusion", false, "unassessed", "inconclusive")
	requireSet(t, root, "stateAxes.proofOutcome", false, "not-run", "reproduced", "not-reproduced", "inconclusive", "error", "blocked", "cancelled")
	if contains(stringsAt(t, root, "stateAxes.proofOutcome"), "false-positive") ||
		contains(stringsAt(t, root, "stateAxes.disposition"), "not-reproduced") {
		t.Error("proof outcome and human disposition must not collapse into a single status")
	}
	report := object(t, get(t, root, "representativeReport"))
	equal(t, report, "data", "synthetic")
	equal(t, report, "format", "synthetic-json-v1")
	rawJSON := textAt(t, report, "rawJSON")
	equal(t, report, "rawDigest", fmt.Sprintf("sha256:%x", sha256.Sum256([]byte(rawJSON))))
	decoded, err := decodeJSON([]byte(rawJSON))
	if err != nil {
		t.Fatalf("representative synthetic raw report is malformed: %v", err)
	}
	raw := object(t, decoded)
	equal(t, raw, "data", "synthetic")
	equal(t, raw, "severity", "high")
	projection := object(t, get(t, report, "normalized"))
	equal(t, projection, "sourceSeverity", get(t, raw, "severity"))
	equal(t, projection, "normalizedSeverity", "medium")
	for normalized, original := range map[string]string{
		"sourceScanAt": "scannedAt", "sourceLocation": "location",
		"impact": "impact", "remediation": "remediation", "unmapped.vendorNote": "vendorNote",
	} {
		equal(t, projection, normalized, textAt(t, raw, original))
	}
	cases := indexed(t, root, "cases")
	required := []string{
		"repeat-import", "new-scan-same-finding", "failed-scan", "partial-scan", "changed-scope",
		"out-of-order-scan", "delta-scan", "processing-failure", "complete-absence",
		"proof-not-reproduced", "renormalization", "unknown-source-time",
	}
	for _, id := range required {
		fixture, exists := cases[id]
		if !exists {
			t.Errorf("missing synthetic lifecycle contract case %q", id)
			continue
		}
		t.Run(id, func(t *testing.T) {
			equal(t, fixture, "data", "synthetic")
			prior := object(t, get(t, fixture, "prior"))
			incoming := object(t, get(t, fixture, "incoming"))
			expected := object(t, get(t, fixture, "expected"))
			assertSyntheticScan(t, prior)
			assertSyntheticScan(t, incoming)
			equal(t, incoming, "workspaceId", get(t, prior, "workspaceId"))
			equal(t, incoming, "sourceId", get(t, prior, "sourceId"))
			equal(t, prior, "disposition", "accepted-risk")
			equal(t, prior, "workflowState", "open")
			equal(t, expected, "verifiedResolution", false)
			equal(t, expected, "inferSourceResolution", id == "complete-absence")
			equal(t, expected, "falsePositive", false)
			equal(t, expected, "disposition", get(t, prior, "disposition"))
			equal(t, expected, "workflowState", get(t, prior, "workflowState"))
			requireFlags(t, expected, []string{"notesPreserved", "originalEvidencePreserved", "canonicalIdentityPreserved"}, true)
			if get(t, prior, "importAttemptId") == get(t, incoming, "importAttemptId") {
				t.Error("each intake attempt needs its own identity even when the source scan is unchanged")
			}
			switch id {
			case "repeat-import", "renormalization", "proof-not-reproduced":
				equal(t, incoming, "scanId", get(t, prior, "scanId"))
				equal(t, incoming, "sourceScanAt", get(t, prior, "sourceScanAt"))
				equal(t, expected, "sourceFreshnessAt", get(t, prior, "sourceScanAt"))
				equal(t, expected, "newScan", false)
				if id == "repeat-import" {
					equal(t, incoming, "parserRevision", get(t, prior, "parserRevision"))
					equal(t, expected, "additionalObservations", 0)
				}
				if id == "renormalization" {
					if get(t, incoming, "parserRevision") == get(t, prior, "parserRevision") {
						t.Error("renormalization fixture must exercise a changed parser revision")
					}
					equal(t, expected, "normalizationRevisionChanged", true)
				}
				if id == "proof-not-reproduced" {
					equal(t, fixture, "proofOutcome", "not-reproduced")
					equal(t, expected, "analysisConclusion", "inconclusive")
				}
			case "unknown-source-time":
				equal(t, prior, "sourceScanAt", nil)
				equal(t, incoming, "sourceScanAt", nil)
				equal(t, expected, "sourceFreshnessAt", nil)
			default:
				if get(t, incoming, "scanId") == get(t, prior, "scanId") {
					t.Error("a genuinely different scan needs a distinct source scan identity")
				}
				switch id {
				case "failed-scan":
					equal(t, incoming, "sourceStatus", "failed")
				case "partial-scan":
					equal(t, incoming, "completeness", "partial")
				case "changed-scope":
					if get(t, incoming, "scopeId") == get(t, prior, "scopeId") {
						t.Error("changed-scope fixture must actually change coverage scope")
					}
				case "delta-scan":
					equal(t, incoming, "scanKind", "delta")
				case "processing-failure":
					equal(t, incoming, "sourceStatus", "succeeded")
					equal(t, incoming, "completeness", "complete")
					equal(t, incoming, "processingStatus", "failed")
				case "out-of-order-scan":
					previous, next := scanTime(t, prior), scanTime(t, incoming)
					if previous == nil || next == nil || !next.Before(*previous) {
						t.Error("out-of-order fixture must contain an older source scan")
					}
					equal(t, expected, "sourceFreshnessAt", get(t, prior, "sourceScanAt"))
				case "complete-absence", "new-scan-same-finding":
					equal(t, incoming, "sourceStatus", "succeeded")
					equal(t, incoming, "completeness", "complete")
					equal(t, incoming, "processingStatus", "reconciled")
					equal(t, incoming, "scanKind", "full")
					equal(t, incoming, "scopeId", get(t, prior, "scopeId"))
					equal(t, incoming, "hasFinding", id == "new-scan-same-finding")
					previous, next := scanTime(t, prior), scanTime(t, incoming)
					if previous == nil || next == nil || !next.After(*previous) {
						t.Error("new full scan must have a later source scan time")
					}
					equal(t, expected, "sourceFreshnessAt", get(t, incoming, "sourceScanAt"))
					equal(t, expected, "newScan", true)
					if id == "new-scan-same-finding" {
						equal(t, expected, "additionalObservations", 1)
					}
				}
				if id != "new-scan-same-finding" {
					equal(t, incoming, "hasFinding", false)
				}
			}
		})
	}
}
