package contracts

import (
	"reflect"
	"strings"
	"testing"
)

// Secret and StorageClass resource names use DNS subdomains, not namespace labels.
// Secret keys intentionally retain their separate, broader character alphabet.
// Coder handoff: use a separate resource-name constraint for kubernetes-secret.name
// in both schemas and target.storageClass in install.schema.json:
//
//	{"type":"string","minLength":1,"maxLength":253,
//	 "pattern":"^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$"}
//
// Keep namespace limits/patterns and Secret key validation unchanged.
// https://kubernetes.io/docs/concepts/overview/working-with-objects/names/
// https://kubernetes.io/docs/concepts/overview/working-with-objects/namespaces/
// https://kubernetes.io/docs/concepts/configuration/secret/#constraints-on-secret-names-and-data
// https://kubernetes.io/docs/concepts/storage/dynamic-provisioning/#enabling-dynamic-provisioning
func TestM00ReviewKubernetesResourceNames(t *testing.T) {
	label63 := strings.Repeat("a", 63)
	subdomain253 := strings.Repeat(label63+".", 3) + strings.Repeat("b", 61)
	validNames := []struct{ name, value string }{
		{"single-character", "a"},
		{"label-63", label63},
		{"dotted", "synthetic.credentials"},
		{"subdomain-64", strings.Repeat("a", 32) + "." + strings.Repeat("b", 31)},
		{"subdomain-253", subdomain253},
	}
	invalidNames := []struct{ name, value string }{
		{"empty", ""},
		{"uppercase", "Synthetic"},
		{"underscore", "synthetic_credentials"},
		{"leading-dot", ".synthetic"},
		{"trailing-dot", "synthetic."},
		{"empty-label", "synthetic..credentials"},
		{"leading-hyphen", "-synthetic"},
		{"trailing-hyphen", "synthetic-"},
		{"label-leading-hyphen", "synthetic.-credentials"},
		{"label-trailing-hyphen", "synthetic-.credentials"},
		{"slash", "synthetic/credentials"},
		{"subdomain-254", subdomain253 + "b"},
		{"undotted-254", strings.Repeat("a", 254)},
	}
	invalidNamespaces := []struct{ name, value string }{
		{"empty", ""},
		{"dotted", "synthetic.namespace"},
		{"label-64", strings.Repeat("a", 64)},
		{"uppercase", "Synthetic"},
		{"underscore", "synthetic_namespace"},
		{"leading-hyphen", "-synthetic"},
		{"trailing-hyphen", "synthetic-"},
	}
	secretReference := document{
		"kind": "kubernetes-secret", "name": "synthetic-secret",
		"namespace": "aspm", "key": "TOKEN_v1.json",
	}
	installation := syntheticInstallation("kubernetes")
	for _, path := range []string{"bootstrap.secretRef", "postgres.credentialRef", "objectStore.credentialRef"} {
		put(t, installation, path, clone(t, secretReference))
	}
	put(t, installation, "target.storageClass", "synthetic-storage")
	provider := syntheticProvider("openai")
	put(t, provider, "auth.secretRef", clone(t, secretReference))

	for _, fixture := range []struct {
		name, schema              string
		configuration             document
		resourceNames, namespaces []string
	}{
		{
			name: "installation", schema: "install.schema.json", configuration: installation,
			resourceNames: []string{
				"bootstrap.secretRef.name", "postgres.credentialRef.name",
				"objectStore.credentialRef.name", "target.storageClass",
			},
			namespaces: []string{
				"target.namespace", "bootstrap.secretRef.namespace",
				"postgres.credentialRef.namespace", "objectStore.credentialRef.namespace",
			},
		},
		{
			name: "provider", schema: "provider.schema.json", configuration: provider,
			resourceNames: []string{"auth.secretRef.name"},
			namespaces:    []string{"auth.secretRef.namespace"},
		},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			schema := compileSchema(t, fixture.schema)
			assertValid(t, schema, fixture.configuration)
			for _, path := range fixture.resourceNames {
				t.Run(path, func(t *testing.T) {
					for _, example := range validNames {
						t.Run("valid-"+example.name, func(t *testing.T) {
							candidate := clone(t, fixture.configuration)
							put(t, candidate, path, example.value)
							assertValid(t, schema, candidate)
						})
					}
					for _, example := range invalidNames {
						t.Run("invalid-"+example.name, func(t *testing.T) {
							candidate := clone(t, fixture.configuration)
							put(t, candidate, path, example.value)
							assertInvalid(t, schema, candidate)
						})
					}
				})
			}
			for _, path := range fixture.namespaces {
				t.Run(path, func(t *testing.T) {
					t.Run("valid-label-63", func(t *testing.T) {
						candidate := clone(t, fixture.configuration)
						put(t, candidate, path, label63)
						assertValid(t, schema, candidate)
					})
					for _, example := range invalidNamespaces {
						t.Run("invalid-"+example.name, func(t *testing.T) {
							candidate := clone(t, fixture.configuration)
							put(t, candidate, path, example.value)
							assertInvalid(t, schema, candidate)
						})
					}
				})
			}
		})
	}
}

// Coder handoff: the ado-services-build-artifacts profile needs this declaration:
//
//	"lifecycleIdentity": {
//	  "comparisonScopeFields": ["organizationId", "projectId", "repositoryId", "ref",
//	                           "scannerConfigurationId", "reportRole"],
//	  "runIdentityFields": ["buildId", "artifactInstanceId"]
//	}
//
// Update lifecyclePolicy to agree. scannerConfigurationId identifies the immutable
// scanner/tool/rules/options configuration; reportRole is logical, not a per-build
// artifact name or digest. Run identity is source-scoped, not globally unique.
// This projects catalog declarations onto synthetic records, not a connector engine.
func TestM00ReviewAzureDevOpsComparisonScope(t *testing.T) {
	catalog := artifact(t, "integrations.catalog.json", "IntegrationCatalog")
	equal(t, catalog, "rules.absenceRequiresComparableCompleteRun", true)
	families := indexed(t, catalog, "launchFamilies")
	family, exists := families["azure-devops"]
	if !exists {
		t.Fatal("missing azure-devops launch family")
	}
	profiles := indexed(t, family, "profiles")
	profile, exists := profiles["ado-services-build-artifacts"]
	if !exists {
		t.Fatal("missing ado-services-build-artifacts profile")
	}
	identity := object(t, get(t, profile, "lifecycleIdentity"))
	requireSet(t, identity, "comparisonScopeFields", true,
		"organizationId", "projectId", "repositoryId", "ref", "scannerConfigurationId", "reportRole")
	requireSet(t, identity, "runIdentityFields", true, "buildId", "artifactInstanceId")
	scopeFields := stringsAt(t, identity, "comparisonScopeFields")
	runFields := stringsAt(t, identity, "runIdentityFields")
	project := func(t *testing.T, record document, fields []string) []string {
		t.Helper()
		values := make([]string, len(fields))
		for i, field := range fields {
			values[i] = textAt(t, record, field)
		}
		return values
	}
	prior := document{
		"organizationId": "synthetic-org", "projectId": "synthetic-project",
		"repositoryId": "synthetic-repository", "ref": "refs/heads/main",
		"scannerConfigurationId": "synthetic-scanner-config-v1", "reportRole": "synthetic-sast-report",
		"buildId": "synthetic-build-100", "artifactInstanceId": "synthetic-artifact-100",
	}
	successor := clone(t, prior)
	put(t, successor, "buildId", "synthetic-build-101")
	put(t, successor, "artifactInstanceId", "synthetic-artifact-101")
	priorScope := project(t, prior, scopeFields)
	priorRun := project(t, prior, runFields)

	for _, example := range []struct {
		name                       string
		changes                    document
		wantComparable, wantNewRun bool
	}{
		{"successive-builds-same-coverage", document{}, true, true},
		{"new-build-id-only", document{"artifactInstanceId": prior["artifactInstanceId"]}, true, true},
		{"new-artifact-instance-only", document{"buildId": prior["buildId"]}, true, true},
		{"same-build-redownload", document{"buildId": prior["buildId"], "artifactInstanceId": prior["artifactInstanceId"]}, true, false},
		{"changed-organization", document{"organizationId": "synthetic-other-org"}, false, true},
		{"changed-project", document{"projectId": "synthetic-other-project"}, false, true},
		{"changed-repository", document{"repositoryId": "synthetic-other-repository"}, false, true},
		{"changed-ref-cannot-infer-resolution", document{"ref": "refs/heads/release"}, false, true},
		{"changed-configuration-cannot-infer-resolution", document{"scannerConfigurationId": "synthetic-scanner-config-v2"}, false, true},
		{"changed-logical-report-role", document{"reportRole": "synthetic-dependency-report"}, false, true},
	} {
		t.Run(example.name, func(t *testing.T) {
			incoming := clone(t, successor)
			for field, value := range example.changes {
				put(t, incoming, field, value)
			}
			comparable := reflect.DeepEqual(priorScope, project(t, incoming, scopeFields))
			if comparable != example.wantComparable {
				t.Errorf("comparable scope = %t, want %t; only comparable complete runs may infer resolution from absence",
					comparable, example.wantComparable)
			}
			newRun := !reflect.DeepEqual(priorRun, project(t, incoming, runFields))
			if newRun != example.wantNewRun {
				t.Errorf("new source run = %t, want %t; build/artifact identity must stay separate from comparison scope",
					newRun, example.wantNewRun)
			}
		})
	}
}
