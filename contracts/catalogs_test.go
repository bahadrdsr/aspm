package contracts

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var launchCapabilities = map[string][]string{
	"github":       {"repository-inventory", "ownership-inventory", "code-scanning-intake"},
	"gitlab":       {"repository-inventory", "pipeline-inventory", "security-report-intake"},
	"azure-devops": {"repository-inventory", "pipeline-inventory", "security-report-intake"},
	"aws":          {"cloud-inventory", "security-hub-intake"},
	"azure":        {"cloud-inventory", "defender-for-cloud-intake"},
	"jira":         {"work-item-create", "work-item-update", "required-field-mapping", "status-linkage"},
	"teams":        {"notifications", "deep-links"},
	"slack":        {"notifications", "deep-links"},
}

func verificationError(record document) error {
	state, _ := record["state"].(string)
	reason, _ := record["reason"].(string)
	switch state {
	case "not-run", "blocked", "failed":
		if strings.TrimSpace(reason) == "" {
			return fmt.Errorf("%s verification must explain its limitation", state)
		}
		return nil
	case "passed":
		if record["method"] != "authorized-live-endpoint" || record["authorized"] != true {
			return fmt.Errorf("passed live verification requires an authorized real-endpoint run")
		}
		if record["data"] != "synthetic" && record["data"] != "explicitly-approved" {
			return fmt.Errorf("live verification requires synthetic or explicitly approved data")
		}
		evidence, _ := record["evidenceRef"].(string)
		if strings.TrimSpace(evidence) == "" {
			return fmt.Errorf("passed live verification needs reviewable evidence")
		}
		checkedAt, _ := record["checkedAt"].(string)
		timestamp, err := time.Parse(time.RFC3339, checkedAt)
		if err != nil || timestamp.After(time.Now().Add(time.Minute)) {
			return fmt.Errorf("passed live verification needs a valid, non-future timestamp")
		}
		return nil
	default:
		return fmt.Errorf("unknown live verification state %q", state)
	}
}

func assertEvidenceFile(t *testing.T, reference string) string {
	t.Helper()
	normalized := filepath.FromSlash(strings.ReplaceAll(reference, `\`, "/"))
	if filepath.IsAbs(normalized) || strings.Contains(normalized, ":") {
		t.Fatal("evidence references must be local repository-relative paths, not URLs or absolute paths")
	}
	for _, part := range strings.Split(strings.ReplaceAll(reference, `\`, "/"), "/") {
		if part == ".." {
			t.Fatal("evidence reference must not escape the project")
		}
	}
	path := filepath.Join("..", normalized)
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("verification claim lacks an existing evidence file: %v", err)
	}
	root, err := filepath.Abs("..")
	if err != nil {
		t.Fatal(err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		t.Fatal(err)
	}
	relative, err := filepath.Rel(root, resolved)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		t.Fatal("verification evidence resolves outside this project")
	}
	info, err := os.Stat(resolved)
	if err != nil || info.IsDir() || info.Size() == 0 {
		t.Fatal("verification evidence must be a nonempty file")
	}
	return resolved
}

func assertLiveVerification(t *testing.T, profile document, subject string) {
	t.Helper()
	record := object(t, get(t, profile, "liveVerification"))
	if err := verificationError(record); err != nil {
		t.Fatal(err)
	}
	if record["state"] == "passed" {
		path := assertEvidenceFile(t, textAt(t, record, "evidenceRef"))
		evidence := readDocument(t, path)
		equal(t, evidence, "subject", subject)
		for _, field := range []string{"state", "method", "authorized", "data", "checkedAt"} {
			equal(t, evidence, field, record[field])
		}
	}
}

func assertMaturity(t *testing.T, profile document, subject string) {
	t.Helper()
	state := textAt(t, profile, "supportMaturity")
	if !contains([]string{"planned", "experimental", "supported", "deprecated", "unsupported"}, state) {
		t.Errorf("unknown support maturity %q", state)
	}
	implementation := textAt(t, profile, "implementationStatus")
	if !contains([]string{"not-implemented", "implemented"}, implementation) {
		t.Errorf("unknown implementation status %q", implementation)
	}
	ready, ok := get(t, profile, "readyToConnect").(bool)
	if !ok {
		t.Fatal("readyToConnect must be an explicit boolean")
	}
	assertLiveVerification(t, profile, subject)
	if ready || state == "supported" {
		equal(t, profile, "implementationStatus", "implemented")
		equal(t, profile, "liveVerification.state", "passed")
		assertEvidenceFile(t, textAt(t, profile, "deterministicTestEvidenceRef"))
	}
	if ready {
		equal(t, profile, "supportMaturity", "supported")
	}
	textAt(t, profile, "setupRecipe")
	plan := object(t, get(t, profile, "verificationPlan"))
	textAt(t, plan, "environment")
	equal(t, plan, "data", "synthetic")
	equal(t, plan, "requiresAuthorization", true)
	requireSet(t, plan, "cases", false, "authentication", "permission-gap", "rate-limit", "retry", "cancellation")
}

func TestHarnessVerificationClaims(t *testing.T) {
	for _, state := range []string{"not-run", "blocked", "failed"} {
		if err := verificationError(document{"state": state, "reason": "No authorized synthetic endpoint available."}); err != nil {
			t.Errorf("honest %s state rejected: %v", state, err)
		}
		if err := verificationError(document{"state": state}); err == nil {
			t.Errorf("%s without an explanation accepted", state)
		}
	}
	record := document{
		"state": "passed", "method": "authorized-live-endpoint", "authorized": true,
		"data": "synthetic", "evidenceRef": `docs\evidence\synthetic-test-only-reference.json`,
		"checkedAt": "2026-09-01T12:00:00Z",
	}
	if err := verificationError(record); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct {
		field string
		value any
	}{
		{"method", "deterministic-fixture"}, {"method", "mock"}, {"authorized", false},
		{"data", "unapproved-customer-data"}, {"evidenceRef", ""}, {"checkedAt", "not-a-time"},
		{"checkedAt", "2999-01-01T00:00:00Z"}, {"state", "mock-passed"},
	} {
		candidate := clone(t, record)
		candidate[mutation.field] = mutation.value
		if err := verificationError(candidate); err == nil {
			t.Errorf("misleading verification accepted: %s=%v", mutation.field, mutation.value)
		}
	}
}

func TestLaunchIntegrationCatalog(t *testing.T) {
	root := artifact(t, "integrations.catalog.json", "IntegrationCatalog")
	families := indexed(t, root, "launchFamilies")
	exactIDs(t, families, "github", "gitlab", "azure-devops", "aws", "azure", "jira", "teams", "slack")
	requireSet(t, root, "connectionStates", true,
		"unconfigured", "authenticating", "syncing", "partially-authorized", "healthy", "stale", "paused", "failed",
	)
	requireSet(t, root, "healthDimensions", false,
		"endpoint-connectivity", "permission-coverage", "collection", "parsing", "enrichment", "reconciliation", "pipeline-progress",
	)
	requireFlags(t, root, []string{
		"rules.supportSeparateFromConnectionState", "rules.tokenDoesNotImplyCoverage",
		"rules.backendCredentialsOnly", "rules.noSecretsInBrowserStorage",
		"rules.explicitOutboundAction", "rules.preserveNonSecretDrafts",
	}, true)
	for id, family := range families {
		t.Run(id, func(t *testing.T) {
			equal(t, family, "kind", "native")
			equal(t, family, "requiredFor", "R1")
			equal(t, family, "requiredAtInstall", false)
			requireSet(t, family, "capabilities", false, launchCapabilities[id]...)
			profiles := indexed(t, family, "profiles")
			if len(profiles) == 0 {
				t.Fatal("required launch family needs at least one concrete planned profile")
			}
			covered := map[string]bool{}
			for profileID, profile := range profiles {
				t.Run(profileID, func(t *testing.T) {
					textAt(t, profile, "edition")
					textAt(t, profile, "apiVersion")
					if len(stringsAt(t, profile, "authModes")) == 0 || len(stringsAt(t, profile, "requiredPermissions")) == 0 {
						t.Error("freeze authentication and explain least-privilege permission requirements")
					}
					stringsAt(t, profile, "unsupportedCapabilities")
					assertMaturity(t, profile, id+"/"+profileID)
					capabilities := object(t, get(t, profile, "capabilities"))
					for capability, raw := range capabilities {
						state, ok := raw.(string)
						if !ok || !contains([]string{"planned", "supported", "unsupported"}, state) {
							t.Errorf("%s needs an honest planned/supported/unsupported capability state", capability)
						}
						if state == "planned" || state == "supported" {
							covered[capability] = true
						}
						if state == "supported" {
							equal(t, profile, "implementationStatus", "implemented")
						}
					}
					if contains([]string{"github", "gitlab", "azure-devops", "aws", "azure"}, id) {
						requireSet(t, profile, "fieldSupport", false,
							"sourceId", "sourceScanAt", "collectedAt", "importedAt", "sourceSeverity", "sourceLocation", "scopeId", "completeness",
						)
						requireSet(t, profile, "verificationPlan.cases", false, "pagination", "partial-response", "stale-source")
					}
					if id == "jira" {
						requireSet(t, profile, "verificationPlan.cases", false, "required-field-mapping", "uncertain-delivery", "idempotent-retry")
					}
					if id == "teams" {
						textAt(t, profile, "deliveryProfile")
						if len(stringsAt(t, profile, "ownershipConstraints")) == 0 {
							t.Error("Teams profile must declare its workflow/bot ownership and channel constraints")
						}
					}
				})
			}
			for _, capability := range launchCapabilities[id] {
				if !covered[capability] {
					t.Errorf("required R1 capability has no planned or implemented profile: %s", capability)
				}
			}
		})
	}
	importers := indexed(t, root, "reportIntake")
	for _, id := range []string{"sarif", "trivy", "zap", "gitleaks", "generic-json", "generic-csv", "manual"} {
		importer, exists := importers[id]
		if !exists {
			t.Errorf("missing separately labeled report/manual intake %q", id)
			continue
		}
		kind := "report-importer"
		if id == "manual" {
			kind = "manual-intake"
		}
		equal(t, importer, "kind", kind)
		equal(t, importer, "countsAsNativeLaunchFamily", false)
	}
}

func TestAIProviderCatalog(t *testing.T) {
	root := artifact(t, "providers.catalog.json", "ProviderCatalog")
	requireSet(t, root, "modes", true, "disabled", "local-only", "approved-hosted")
	equal(t, root, "defaultMode", "disabled")
	requireFlags(t, root, []string{
		"policy.disabledNoInference", "policy.localOnlyForbidsHosted", "policy.localRuntimeEgressEnforced",
		"policy.hostedRequiresWorkspaceApproval", "policy.fallbackExplicitAndAudited",
		"policy.modelToolCallsAreUntrustedData", "policy.sharedQuotaAcrossReplicas",
		"policy.noTransactionHeldDuringInference", "policy.providerRetentionNotUniversal", "policy.nonAIProductUsable",
	}, true)
	families := indexed(t, root, "families")
	exactIDs(t, families, "openai", "azure-foundry", "anthropic", "local")
	for id, family := range families {
		t.Run(id, func(t *testing.T) {
			equal(t, family, "requiredAtInstall", false)
			equal(t, family, "hosted", id != "local")
			if id != "local" {
				equal(t, family, "hostedServiceIsFOSS", false)
			}
			profiles := indexed(t, family, "profiles")
			if len(profiles) == 0 {
				t.Fatal("provider family needs at least one declared profile")
			}
			protocols := []string{}
			for profileID, profile := range profiles {
				t.Run(profileID, func(t *testing.T) {
					protocols = append(protocols, textAt(t, profile, "protocol"))
					equal(t, profile, "modelSelection", "operator-configured")
					identity := textAt(t, profile, "modelIdentityStatus")
					if !contains([]string{"unresolved-alias", "resolved"}, identity) {
						t.Error("model identity must be resolved or explicitly labeled as an unresolved alias")
					}
					if identity == "resolved" {
						textAt(t, profile, "resolvedModelIdentity")
					}
					assertMaturity(t, profile, id+"/"+profileID)
					capabilities := object(t, get(t, profile, "capabilities"))
					for _, capability := range []string{"structured-output", "tools", "streaming", "usage", "cancellation"} {
						state := textAt(t, capabilities, capability)
						if !contains([]string{"unknown", "supported", "unsupported"}, state) {
							t.Errorf("invalid capability-specific state for %s: %s", capability, state)
						}
					}
					requireSet(t, profile, "verificationPlan.cases", false,
						"request-mapping", "schema-validation", "usage-extraction", "privacy-policy", "shared-quota", "invalid-output",
					)
					if id == "local" {
						for _, component := range []string{"runtime", "modelSystem"} {
							status := textAt(t, profile, "licenseReview."+component+".status")
							if !contains([]string{"not-reviewed", "reviewed-compatible", "not-compatible"}, status) {
								t.Error("runtime and entire model-system openness require separate license reviews")
							}
							textAt(t, profile, "licenseReview."+component+".reason")
						}
						if get(t, profile, "bundlingApproved") == true {
							for _, component := range []string{"runtime", "modelSystem"} {
								equal(t, profile, "licenseReview."+component+".status", "reviewed-compatible")
								assertEvidenceFile(t, textAt(t, profile, "licenseReview."+component+".evidenceRef"))
							}
						} else {
							equal(t, profile, "bundlingApproved", false)
						}
					}
				})
			}
			requiredProtocols := map[string][]string{
				"openai": {"openai-v1"}, "azure-foundry": {"openai-v1"},
				"anthropic": {"anthropic-messages"}, "local": {"openai-compatible", "ollama"},
			}
			for _, protocol := range requiredProtocols[id] {
				if !contains(protocols, protocol) {
					t.Errorf("missing required planned protocol profile %q", protocol)
				}
			}
		})
	}
}
