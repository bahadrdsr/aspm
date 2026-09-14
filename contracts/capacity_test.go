package contracts

import (
	"math"
	"testing"
)

func TestCapacityProfilesAndBudgets(t *testing.T) {
	root := artifact(t, "capacity.profiles.json", "CapacityProfiles")
	equal(t, root, "status", "hypothesis")
	equal(t, root, "fixturePolicy.data", "synthetic")
	requireFlags(t, root, []string{"fixturePolicy.networkCalls", "fixturePolicy.requiresDocker", "fixturePolicy.requiresKubernetes"}, false)
	equal(t, root, "hardware.status", "hypothesis")
	hardwareID := textAt(t, root, "hardware.id")
	for _, field := range []string{"cpuModel", "storageMedia", "os"} {
		textAt(t, root, "hardware."+field)
	}
	for _, field := range []string{"vcpus", "memoryMiB", "storageGiB", "networkMbps"} {
		if numberAt(t, root, "hardware."+field) <= 0 {
			t.Errorf("hardware.%s must be an explicit positive hypothesis", field)
		}
	}
	requireFlags(t, root, []string{
		"measurementConditions.controlPlaneFixed", "measurementConditions.databaseFixed",
		"measurementConditions.storageFixed", "measurementConditions.verifyHeadroomBeforeScaling",
		"measurementConditions.recordInputLimitsAndRejections", "measurementConditions.budgetChangesRequireRecordedDecision",
		"measurementConditions.measureBrowserSeparately", "measurementConditions.measureIndexAndWALGrowth",
	}, true)
	formats := indexed(t, root, "fixtureFormats")
	for _, id := range []string{"structured-json", "structured-xml"} {
		format, ok := formats[id]
		if !ok {
			t.Fatalf("missing declared streaming fixture format %q", id)
		}
		equal(t, format, "supportsStreaming", true)
		sizes := list(t, get(t, format, "supportedReportSizesBytes"))
		seen := map[float64]bool{}
		for _, raw := range sizes {
			size := numberAt(t, document{"size": raw}, "size")
			if size <= 0 || seen[size] || math.Trunc(size) != size {
				t.Error("report sizes must be unique positive integer byte counts")
			}
			seen[size] = true
		}
		for _, size := range []float64{10 * 1024 * 1024, 100 * 1024 * 1024, 1024 * 1024 * 1024} {
			if !seen[size] {
				t.Errorf("%s must declare the supported %v-byte structured report fixture", id, size)
			}
		}
		if numberAt(t, format, "maxRecordBytes") <= 0 {
			t.Error("fixture format must declare its own record-size bound")
		}
		textAt(t, format, "sizeDistribution")
	}
	profiles := indexed(t, root, "profiles")
	exactIDs(t, profiles, "development", "enterprise-e1", "bursty-e1", "history-churn", "failure-quota")
	for id, profile := range profiles {
		t.Run(id, func(t *testing.T) {
			equal(t, profile, "status", "hypothesis")
			equal(t, profile, "hardwareRef", hardwareID)
			textAt(t, profile, "fixtureGenerator")
			seed := numberAt(t, profile, "seed")
			if seed < 0 || math.Trunc(seed) != seed {
				t.Error("deterministic fixture seed must be a nonnegative integer")
			}
			references := stringsAt(t, profile, "fixtureFormatIds")
			if len(references) == 0 {
				t.Error("profile needs explicit format/size distribution references")
			}
			for _, reference := range references {
				if _, exists := formats[reference]; !exists {
					t.Errorf("unknown fixture format reference %q", reference)
				}
			}
			for _, field := range []string{"assets", "issues", "observations", "workspaces"} {
				count := numberAt(t, profile, "dataset."+field)
				if count < 1 || math.Trunc(count) != count {
					t.Errorf("dataset.%s must be a positive integer", field)
				}
			}
			for _, field := range []string{"activeImportJobs", "interactiveUsers", "reportExports"} {
				count := numberAt(t, profile, "load."+field)
				if count < 1 || math.Trunc(count) != count {
					t.Errorf("load.%s must explicitly exercise concurrent import, interactive, and export work", field)
				}
			}
		})
	}
	for _, item := range []struct {
		id                           string
		assets, issues, observations int
	}{
		{"development", 1000, 10000, 100000},
		{"enterprise-e1", 25000, 1000000, 25000000},
		{"bursty-e1", 25000, 1000000, 25000000},
	} {
		profile := profiles[item.id]
		equal(t, profile, "dataset.assets", item.assets)
		equal(t, profile, "dataset.issues", item.issues)
		equal(t, profile, "dataset.observations", item.observations)
		if item.id != "development" {
			equal(t, profile, "dataset.workspaces", 20)
			equal(t, profile, "load.activeImportJobs", 50)
			equal(t, profile, "load.interactiveUsers", 200)
		}
	}
	equal(t, profiles["bursty-e1"], "load.dominantWorkspaceSharePercent", 80)
	for _, id := range []string{"history-churn", "failure-quota"} {
		for _, field := range []string{"assets", "issues", "observations", "workspaces"} {
			if numberAt(t, profiles[id], "dataset."+field) > numberAt(t, profiles["enterprise-e1"], "dataset."+field) {
				t.Errorf("%s must declare a reproducible E1 subset, not an unspecified larger workload", id)
			}
		}
	}
	budgets := indexed(t, root, "budgets")
	for id, budget := range budgets {
		t.Run("budget-"+id, func(t *testing.T) {
			equal(t, budget, "status", "hypothesis")
			textAt(t, budget, "metric")
			textAt(t, budget, "measurement")
			textAt(t, budget, "unit")
			comparison := textAt(t, budget, "comparison")
			if !contains([]string{"lte", "gte", "eq"}, comparison) {
				t.Errorf("unknown comparison %q", comparison)
			}
			if numberAt(t, budget, "target") < 0 {
				t.Error("budget target cannot be negative")
			}
			references := stringsAt(t, budget, "appliesTo")
			if len(references) == 0 {
				t.Error("budget must name its workload profiles")
			}
			for _, reference := range references {
				if _, exists := profiles[reference]; !exists {
					t.Errorf("unknown budget workload reference %q", reference)
				}
			}
		})
	}
	for _, want := range []struct {
		id, unit, comparison, profile string
		value                         float64
	}{
		{"interactive-p95", "ms", "lte", "enterprise-e1", 500},
		{"interactive-p99", "ms", "lte", "enterprise-e1", 1500},
		{"streaming-worker-rss", "MiB", "lte", "enterprise-e1", 512},
		{"parsing-scale-ratio", "ratio", "gte", "enterprise-e1", 1.6},
		{"acknowledged-observation-loss", "observations", "eq", "failure-quota", 0},
		{"duplicate-business-effects", "effects", "eq", "failure-quota", 0},
		{"incorrect-source-closures", "closures", "eq", "failure-quota", 0},
	} {
		budget, exists := budgets[want.id]
		if !exists {
			t.Errorf("missing frozen budget %q", want.id)
			continue
		}
		equal(t, budget, "unit", want.unit)
		equal(t, budget, "comparison", want.comparison)
		equal(t, budget, "target", want.value)
		requireSet(t, budget, "appliesTo", false, want.profile)
	}
}

func TestFailureChurnAndRetention(t *testing.T) {
	root := artifact(t, "capacity.profiles.json", "CapacityProfiles")
	budgets := indexed(t, root, "budgets")
	for _, want := range []struct {
		id, unit, profile string
	}{
		{"storage-growth-per-cycle", "MiB", "history-churn"},
		{"index-growth-per-cycle", "MiB", "history-churn"},
		{"restore-duration", "ms", "history-churn"},
		{"max-provider-attempts", "attempts", "failure-quota"},
		{"max-provider-retry-elapsed", "ms", "failure-quota"},
		{"small-workspace-progress", "ms", "bursty-e1"},
		{"stalled-stage-visible", "ms", "failure-quota"},
	} {
		budget, exists := budgets[want.id]
		if !exists {
			t.Errorf("missing bounded failure/churn hypothesis %q", want.id)
			continue
		}
		equal(t, budget, "status", "hypothesis")
		equal(t, budget, "unit", want.unit)
		equal(t, budget, "comparison", "lte")
		if numberAt(t, budget, "target") <= 0 {
			t.Errorf("%s requires a positive explicit bound, not unlimited or TBD", want.id)
		}
		if want.id == "max-provider-attempts" && math.Trunc(numberAt(t, budget, "target")) != numberAt(t, budget, "target") {
			t.Error("retry attempt budget must be an integer")
		}
		requireSet(t, budget, "appliesTo", false, want.profile)
	}
	scenarios := indexed(t, root, "failureScenarios")
	required := map[string][]string{
		"worker-termination":    {"acknowledged-work-retained", "expired-worker-cannot-finalize", "replay-idempotent"},
		"duplicate-delivery":    {"no-duplicate-business-effects", "no-extra-observations"},
		"expired-lease":         {"fencing-enforced", "stale-worker-cannot-finalize"},
		"storage-interruption":  {"visible-incomplete-state", "no-dangling-evidence", "no-source-closure"},
		"database-interruption": {"acknowledged-work-retained", "bounded-retries", "no-source-closure"},
		"provider-throttling":   {"bounded-retries", "visible-queued-or-blocked", "no-unapproved-fallback", "shared-quota"},
		"stalled-analyzer":      {"stalled-stage-visible", "unrelated-queues-progress", "no-fake-freshness"},
	}
	for id, invariants := range required {
		scenario, exists := scenarios[id]
		if !exists {
			t.Errorf("missing required failure scenario %q", id)
			continue
		}
		equal(t, scenario, "method", "controlled-fixture")
		equal(t, scenario, "profileId", "failure-quota")
		requireSet(t, scenario, "invariants", false, invariants...)
		references := stringsAt(t, scenario, "budgetRefs")
		if len(references) == 0 {
			t.Errorf("%s requires explicit success/failure budget references", id)
		}
		for _, reference := range references {
			if _, exists := budgets[reference]; !exists {
				t.Errorf("%s references unknown budget %s", id, reference)
			}
		}
	}
	churn := object(t, get(t, root, "churn"))
	for _, field := range []string{"cycles", "importsPerScan"} {
		count := numberAt(t, churn, field)
		if count < 2 || math.Trunc(count) != count {
			t.Errorf("churn.%s must exercise recurring scans and repeated imports", field)
		}
	}
	if len(stringsAt(t, churn, "parserRevisions")) < 2 {
		t.Error("churn must include at least two parser revisions")
	}
	requireFlags(t, churn, []string{"stableCanonicalIssueSet", "scanIdentitySeparateFromImportAttempt"}, true)
	requireSet(t, churn, "invariants", false,
		"stable-issue-count", "distinct-scan-import-identities", "immutable-evidence", "run-variant-membership",
		"notes-and-dispositions", "parser-revision-provenance", "explicit-evidence-availability", "restore-preserves-links",
	)
	requireSet(t, churn, "budgetRefs", false, "storage-growth-per-cycle", "index-growth-per-cycle", "restore-duration")
	policies := indexed(t, root, "retentionPolicies")
	exactIDs(t, policies, "hot-history", "archived-evidence", "raw-report", "audit")
	for id, policy := range policies {
		t.Run(id, func(t *testing.T) {
			days := numberAt(t, policy, "retainDays")
			if days <= 0 || math.Trunc(days) != days {
				t.Error("retention duration must be a positive explicit day count")
			}
			requireFlags(t, policy, []string{"heldEvidenceExempt", "preserveActiveDecisions", "previewRequired"}, true)
			state := textAt(t, policy, "evidenceStateAfterExpiry")
			if !contains([]string{"archived", "expired"}, state) {
				t.Error("expired evidence must be accurately labeled, not reported as still available")
			}
		})
	}
	requireSet(t, root, "evidenceAvailabilityStates", true, "available", "archived", "expired", "missing", "corrupt")
}
