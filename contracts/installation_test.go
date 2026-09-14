package contracts

import (
	"regexp"
	"strings"
	"testing"
)

const (
	seaweedFSVersion = "4.47"
	seaweedFSDigest  = "sha256:ce9e796f1fe6f06968f4c04bdaf8f678dad9c8acdfef3d244133d71bfa6bf882"
)

func syntheticInstallation(target string) document {
	targetConfig := document{"kind": "linux", "host": "localhost"}
	exposure := "loopback"
	if target == "kubernetes" {
		targetConfig = document{"kind": "kubernetes", "context": "synthetic-context", "namespace": "aspm"}
		exposure = "private"
	}
	secret := func(name string) document {
		return document{"kind": "generated", "name": name}
	}
	services := document{}
	for _, name := range []string{"core-api", "ingestion-parser", "ingestion-reconciler", "background-worker"} {
		services[name] = document{"replicas": 1, "dbConnectionsPerReplica": 10}
	}
	for _, name := range []string{"ai-service", "proof-service"} {
		services[name] = document{"replicas": 0, "dbConnectionsPerReplica": 0}
	}
	return document{
		"apiVersion": apiVersion,
		"kind":       "Installation",
		"release":    document{"version": "0.0.0-dev.1", "artifactSource": "installer-bundle"},
		"target":     targetConfig,
		"access": document{
			"exposure": exposure, "baseURL": "https://aspm.example.invalid",
			"tlsMode": "internal-ca", "publicExposureApproved": false,
		},
		"bootstrap": document{"method": "one-time-enrollment", "secretRef": secret("aspm-enrollment")},
		"postgres": document{
			"mode": "managed", "engine": "postgresql", "persistent": true,
			"publicAccess": false, "credentialRef": secret("aspm-postgres"), "volumeName": "aspm-postgres",
		},
		"objectStore": document{
			"mode": "managed", "engine": "seaweedfs-community", "api": "s3",
			"persistent": true, "publicAccess": false, "credentialRef": secret("aspm-evidence"), "volumeName": "aspm-evidence",
			"image": document{"version": seaweedFSVersion, "digest": seaweedFSDigest},
			"seaweedfs": document{
				"masterTelemetry": false, "adminUI": false, "webdav": false,
				"icebergPort": 0, "lancePort": 0,
				"publishedAPIs": []string{"s3"}, "managementExposure": "internal-only",
			},
		},
		"ai": document{
			"mode": "disabled", "profiles": []string{}, "fallbackProfiles": []string{},
			"hostedProcessingApproved": false,
		},
		"proof":                    document{"enabled": false, "executionPolicy": "controlled-fixtures-only"},
		"services":                 services,
		"databaseConnectionBudget": document{"max": 80, "reservedMaintenance": 10},
		"artifacts": document{
			"mode": "online", "signatureRequired": true, "outboundRetrievalRequiresApproval": true,
		},
		"retentionProfile": "small",
	}
}

func TestInstallationSchema(t *testing.T) {
	schema := compileSchema(t, "install.schema.json")
	for _, target := range []string{"linux", "kubernetes"} {
		t.Run(target, func(t *testing.T) {
			base := syntheticInstallation(target)
			assertValid(t, schema, base)
			for _, field := range []string{
				"apiVersion", "kind", "release", "target", "access", "bootstrap", "postgres",
				"objectStore", "ai", "proof", "services", "databaseConnectionBudget", "artifacts", "retentionProfile",
				"release.version", "release.artifactSource", "target.kind",
				"access.exposure", "access.baseURL", "access.tlsMode", "access.publicExposureApproved",
				"bootstrap.method", "bootstrap.secretRef", "postgres.credentialRef", "objectStore.credentialRef",
				"postgres.mode", "postgres.engine", "postgres.persistent", "postgres.publicAccess", "postgres.volumeName",
				"objectStore.mode", "objectStore.engine", "objectStore.api", "objectStore.persistent", "objectStore.publicAccess", "objectStore.volumeName",
				"objectStore.image", "objectStore.image.version", "objectStore.image.digest", "objectStore.seaweedfs",
				"objectStore.seaweedfs.masterTelemetry", "objectStore.seaweedfs.adminUI", "objectStore.seaweedfs.webdav",
				"objectStore.seaweedfs.icebergPort", "objectStore.seaweedfs.lancePort",
				"objectStore.seaweedfs.publishedAPIs", "objectStore.seaweedfs.managementExposure",
				"ai.mode", "ai.profiles", "ai.fallbackProfiles", "ai.hostedProcessingApproved",
				"proof.enabled", "proof.executionPolicy", "databaseConnectionBudget.max", "databaseConnectionBudget.reservedMaintenance",
				"artifacts.mode", "artifacts.signatureRequired", "artifacts.outboundRetrievalRequiresApproval",
			} {
				t.Run("missing-"+field, func(t *testing.T) {
					candidate := clone(t, base)
					remove(t, candidate, field)
					assertInvalid(t, schema, candidate)
				})
			}
			mutations := []struct {
				name, path string
				value      any
			}{
				{"unknown-version", "apiVersion", "aspm/v99"},
				{"wrong-kind", "kind", "ProviderProfile"},
				{"unknown-field", "unexpected", true},
				{"unsupported-target", "target.kind", "windows"},
				{"unpinned-release", "release.version", "latest"},
				{"public-without-approval", "access.exposure", "public"},
				{"unencrypted-access", "access.tlsMode", "none"},
				{"plaintext-access-url", "access.baseURL", "http://aspm.example.invalid"},
				{"default-admin-password", "bootstrap.password", "synthetic-not-a-real-password"},
				{"public-database", "postgres.publicAccess", true},
				{"ephemeral-database", "postgres.persistent", false},
				{"public-evidence", "objectStore.publicAccess", true},
				{"ephemeral-evidence", "objectStore.persistent", false},
				{"node-local-evidence", "objectStore.engine", "filesystem"},
				{"unpinned-storage-image", "objectStore.image.version", "latest"},
				{"invalid-storage-image-digest", "objectStore.image.digest", "sha256:invalid"},
				{"storage-telemetry-enabled", "objectStore.seaweedfs.masterTelemetry", true},
				{"storage-admin-ui-enabled", "objectStore.seaweedfs.adminUI", true},
				{"storage-webdav-enabled", "objectStore.seaweedfs.webdav", true},
				{"storage-iceberg-enabled", "objectStore.seaweedfs.icebergPort", 8181},
				{"storage-lance-enabled", "objectStore.seaweedfs.lancePort", 9101},
				{"storage-admin-api-published", "objectStore.seaweedfs.publishedAPIs", []string{"s3", "admin"}},
				{"storage-management-public", "objectStore.seaweedfs.managementExposure", "public"},
				{"unknown-ai-mode", "ai.mode", "automatic"},
				{"hosted-without-approval", "ai.mode", "approved-hosted"},
				{"disabled-with-fallback", "ai.fallbackProfiles", []string{"synthetic-hosted"}},
				{"unrestricted-proof", "proof.executionPolicy", "arbitrary-shell"},
				{"unsigned-artifacts", "artifacts.signatureRequired", false},
				{"unapproved-retrieval", "artifacts.outboundRetrievalRequiresApproval", false},
				{"negative-workers", "services.ingestion-parser.replicas", -1},
				{"fractional-workers", "services.ingestion-parser.replicas", 1.5},
				{"zero-connection-budget", "databaseConnectionBudget.max", 0},
			}
			for _, mutation := range mutations {
				t.Run(mutation.name, func(t *testing.T) {
					candidate := clone(t, base)
					put(t, candidate, mutation.path, mutation.value)
					assertInvalid(t, schema, candidate)
				})
			}
			for _, field := range []string{"bootstrap.secretRef", "postgres.credentialRef", "objectStore.credentialRef"} {
				t.Run("secret-reference-"+field, func(t *testing.T) {
					for _, invalid := range []any{
						"synthetic-inline-value",
						document{"kind": "plaintext", "name": "synthetic"},
						document{"kind": "environment", "name": ""},
						document{"kind": "environment", "name": "SYNTHETIC_SECRET", "value": "not-a-real-credential"},
					} {
						candidate := clone(t, base)
						put(t, candidate, field, invalid)
						assertInvalid(t, schema, candidate)
					}
					validReferences := []document{
						{"kind": "generated", "name": "synthetic-reference"},
						{"kind": "environment", "name": "SYNTHETIC_REFERENCE"},
						{"kind": "file", "name": "synthetic-protected-input"},
					}
					if target == "kubernetes" {
						validReferences = append(validReferences, document{
							"kind": "kubernetes-secret", "name": "synthetic-secret", "namespace": "aspm", "key": "password",
						})
					}
					for _, reference := range validReferences {
						candidate := clone(t, base)
						put(t, candidate, field, reference)
						assertValid(t, schema, candidate)
					}
				})
			}
			t.Run("target-specific-fields", func(t *testing.T) {
				candidate := clone(t, base)
				targetObject := object(t, get(t, candidate, "target"))
				if target == "linux" {
					delete(targetObject, "host")
				} else {
					delete(targetObject, "context")
				}
				assertInvalid(t, schema, candidate)
				candidate = clone(t, base)
				if target == "linux" {
					put(t, candidate, "target.context", "unrelated-cluster")
				} else {
					put(t, candidate, "target.host", "unrelated-host")
				}
				assertInvalid(t, schema, candidate)
			})
			t.Run("external-storage-branch", func(t *testing.T) {
				candidate := clone(t, base)
				for _, field := range []string{"postgres", "objectStore"} {
					put(t, candidate, field+".mode", "external")
					put(t, candidate, field+".credentialRef", document{"kind": "environment", "name": "SYNTHETIC_STORAGE_REFERENCE"})
					remove(t, candidate, field+".volumeName")
				}
				put(t, candidate, "postgres.endpoint", "postgresql://database.example.invalid/aspm")
				put(t, candidate, "postgres.tlsMode", "verify-full")
				put(t, candidate, "objectStore.engine", "s3-compatible")
				remove(t, candidate, "objectStore.image")
				remove(t, candidate, "objectStore.seaweedfs")
				put(t, candidate, "objectStore.endpoint", "https://objects.example.invalid")
				put(t, candidate, "objectStore.bucket", "aspm-synthetic-evidence")
				assertValid(t, schema, candidate)
				for _, field := range []string{"postgres", "objectStore"} {
					missing := clone(t, candidate)
					delete(object(t, get(t, missing, field)), "endpoint")
					assertInvalid(t, schema, missing)
					missing = clone(t, candidate)
					delete(object(t, get(t, missing, field)), "credentialRef")
					assertInvalid(t, schema, missing)
				}
			})
			t.Run("custom-access-and-private-mirror", func(t *testing.T) {
				candidate := clone(t, base)
				put(t, candidate, "access.exposure", "public")
				put(t, candidate, "access.publicExposureApproved", true)
				put(t, candidate, "access.tlsMode", "provided")
				put(t, candidate, "access.certificateRef", document{"kind": "file", "name": "synthetic-certificate"})
				put(t, candidate, "access.privateKeyRef", document{"kind": "file", "name": "synthetic-protected-key"})
				assertValid(t, schema, candidate)
				for _, field := range []string{"access.certificateRef", "access.privateKeyRef"} {
					missing := clone(t, candidate)
					remove(t, missing, field)
					assertInvalid(t, schema, missing)
				}
				mirror := clone(t, base)
				put(t, mirror, "artifacts.mode", "private-mirror")
				put(t, mirror, "artifacts.mirrorURL", "https://mirror.example.invalid")
				put(t, mirror, "artifacts.credentialRef", document{"kind": "environment", "name": "SYNTHETIC_MIRROR_REFERENCE"})
				assertValid(t, schema, mirror)
				remove(t, mirror, "artifacts.mirrorURL")
				assertInvalid(t, schema, mirror)
				if target == "kubernetes" {
					advanced := clone(t, base)
					put(t, advanced, "target.storageClass", "synthetic-storage-class")
					put(t, advanced, "target.ingressClass", "synthetic-existing-ingress")
					assertValid(t, schema, advanced)
				}
			})
			t.Run("explicit-optional-ai-and-offline", func(t *testing.T) {
				local := clone(t, base)
				put(t, local, "ai.mode", "local-only")
				put(t, local, "ai.profiles", []string{"synthetic-local"})
				put(t, local, "services.ai-service.replicas", 1)
				put(t, local, "services.ai-service.dbConnectionsPerReplica", 10)
				assertValid(t, schema, local)
				put(t, local, "ai.hostedProcessingApproved", true)
				assertInvalid(t, schema, local)
				hosted := clone(t, base)
				put(t, hosted, "ai.mode", "approved-hosted")
				put(t, hosted, "ai.profiles", []string{"synthetic-hosted"})
				put(t, hosted, "ai.hostedProcessingApproved", true)
				put(t, hosted, "services.ai-service.replicas", 1)
				put(t, hosted, "services.ai-service.dbConnectionsPerReplica", 10)
				assertValid(t, schema, hosted)
				offline := clone(t, base)
				put(t, offline, "artifacts.mode", "offline-bundle")
				put(t, offline, "artifacts.bundleRef", "synthetic-signed-bundle")
				assertValid(t, schema, offline)
				put(t, hosted, "artifacts", get(t, offline, "artifacts"))
				assertInvalid(t, schema, hosted)
				delete(object(t, get(t, offline, "artifacts")), "bundleRef")
				assertInvalid(t, schema, offline)
				proof := clone(t, base)
				put(t, proof, "proof.enabled", true)
				put(t, proof, "services.proof-service.replicas", 1)
				put(t, proof, "services.proof-service.dbConnectionsPerReplica", 10)
				assertValid(t, schema, proof)
			})
		})
	}
}

func TestInstallationDefaults(t *testing.T) {
	root := artifact(t, "install.defaults.json", "InstallationDefaults")
	equal(t, root, "status", "contract-only")
	profiles := indexed(t, root, "profiles")
	exactIDs(t, profiles, "linux-small", "kubernetes-small")
	schema := compileSchema(t, "install.schema.json")
	pinnedVersion := regexp.MustCompile(`^v?\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$`)
	for id, profile := range profiles {
		t.Run(id, func(t *testing.T) {
			config := object(t, get(t, profile, "configuration"))
			assertValid(t, schema, config)
			target := strings.TrimSuffix(id, "-small")
			equal(t, config, "target.kind", target)
			if !pinnedVersion.MatchString(textAt(t, config, "release.version")) {
				t.Error("default release must be pinned, not a moving channel or wildcard")
			}
			for _, field := range []string{"postgres", "objectStore"} {
				equal(t, config, field+".mode", "managed")
				equal(t, config, field+".persistent", true)
				equal(t, config, field+".publicAccess", false)
				textAt(t, config, field+".volumeName")
			}
			equal(t, config, "postgres.engine", "postgresql")
			equal(t, config, "objectStore.engine", "seaweedfs-community")
			equal(t, config, "objectStore.api", "s3")
			equal(t, config, "objectStore.image.version", seaweedFSVersion)
			equal(t, config, "objectStore.image.digest", seaweedFSDigest)
			requireFlags(t, config, []string{
				"objectStore.seaweedfs.masterTelemetry", "objectStore.seaweedfs.adminUI", "objectStore.seaweedfs.webdav",
			}, false)
			equal(t, config, "objectStore.seaweedfs.icebergPort", 0)
			equal(t, config, "objectStore.seaweedfs.lancePort", 0)
			requireSet(t, config, "objectStore.seaweedfs.publishedAPIs", true, "s3")
			equal(t, config, "objectStore.seaweedfs.managementExposure", "internal-only")
			if !contains([]string{"loopback", "private"}, textAt(t, config, "access.exposure")) {
				t.Error("the default must not expose the application publicly")
			}
			equal(t, config, "access.tlsMode", "internal-ca")
			equal(t, config, "access.publicExposureApproved", false)
			equal(t, config, "bootstrap.method", "one-time-enrollment")
			equal(t, config, "ai.mode", "disabled")
			equal(t, config, "ai.hostedProcessingApproved", false)
			requireSet(t, config, "ai.profiles", true)
			requireSet(t, config, "ai.fallbackProfiles", true)
			equal(t, config, "proof.enabled", false)
			equal(t, config, "proof.executionPolicy", "controlled-fixtures-only")
			services := object(t, get(t, config, "services"))
			wantServices := []string{"core-api", "ingestion-parser", "ingestion-reconciler", "background-worker", "ai-service", "proof-service"}
			if len(services) != len(wantServices) {
				t.Fatalf("small profile must declare exactly the six service roles: %v", wantServices)
			}
			used := numberAt(t, config, "databaseConnectionBudget.reservedMaintenance")
			if used < 1 {
				t.Error("reserve maintenance/migration connections")
			}
			for _, name := range wantServices {
				service := object(t, get(t, services, name))
				replicas := 1
				if name == "ai-service" || name == "proof-service" {
					replicas = 0
				}
				equal(t, service, "replicas", replicas)
				connections := numberAt(t, service, "dbConnectionsPerReplica")
				if replicas > 0 && connections < 1 {
					t.Errorf("%s requires an explicit positive connection allowance", name)
				}
				used += float64(replicas) * connections
			}
			if used > numberAt(t, config, "databaseConnectionBudget.max") {
				t.Error("sum of replica connection allowances plus reserve exceeds the shared database budget")
			}
		})
	}
}

func TestInstallerWizardAndApproval(t *testing.T) {
	root := artifact(t, "installer.contract.json", "InstallerContract")
	stages := list(t, get(t, root, "wizard"))
	stageIDs := []string{"destination", "access-bootstrap", "review-install"}
	allowedFields := [][]string{
		{"target.kind", "target.host", "target.context", "target.namespace"},
		{"access.baseURL", "access.exposure", "bootstrap.method"},
		{"approval.planDigest"},
	}
	if len(stages) != 3 {
		t.Fatalf("default wizard has %d stages, want exactly three", len(stages))
	}
	for i, raw := range stages {
		stage := object(t, raw)
		equal(t, stage, "id", stageIDs[i])
		textAt(t, stage, "label")
		questions := list(t, get(t, stage, "questions"))
		if len(questions) == 0 || len(questions) > len(allowedFields[i]) {
			t.Errorf("%s needs a short, nonempty set of relevant questions", stageIDs[i])
		}
		seen := map[string]bool{}
		for _, raw := range questions {
			question := object(t, raw)
			field := textAt(t, question, "field")
			if !contains(allowedFields[i], field) || seen[field] {
				t.Errorf("%s contains unrelated or duplicate default question %s", stageIDs[i], field)
			}
			seen[field] = true
			target := textAt(t, question, "target")
			condition := textAt(t, question, "condition")
			if !contains([]string{"any", "linux", "kubernetes"}, target) ||
				!contains([]string{"always", "cannot-infer", "prerequisite"}, condition) {
				t.Error("question requires an explicit supported target and condition")
			}
			if field == "target.host" && (target != "linux" || condition == "always") {
				t.Error("host question is Linux-only and only necessary when not safely inferred")
			}
			if (field == "target.context" || field == "target.namespace") && (target != "kubernetes" || condition == "always") {
				t.Error("cluster questions must be conditional and Kubernetes-only")
			}
		}
		if !seen[allowedFields[i][0]] {
			t.Errorf("%s must include %s", stageIDs[i], allowedFields[i][0])
		}
	}
	branches := indexed(t, root, "advancedBranches")
	exactIDs(t, branches, "external-storage", "kubernetes-prerequisites", "custom-access", "offline-artifacts", "sizing-retention", "advanced-automation")
	branchFields := map[string][]string{
		"external-storage":         {"postgres.endpoint", "postgres.credentialRef", "objectStore.endpoint", "objectStore.bucket", "objectStore.credentialRef"},
		"kubernetes-prerequisites": {"target.namespace", "target.storageClass", "target.ingressClass"},
		"custom-access":            {"access.baseURL", "access.tlsMode", "access.certificateRef", "access.privateKeyRef"},
		"offline-artifacts":        {"artifacts.bundleRef", "artifacts.mirrorURL", "artifacts.credentialRef"},
		"sizing-retention":         {"services", "databaseConnectionBudget", "retentionProfile"},
		"advanced-automation":      {"resolved-configuration", "secret-references"},
	}
	for id, branch := range branches {
		t.Run(id, func(t *testing.T) {
			triggers := stringsAt(t, branch, "triggers")
			if len(triggers) == 0 {
				t.Error("advanced branch needs a trigger")
			}
			for _, trigger := range triggers {
				if !contains([]string{"explicit-request", "missing-prerequisite"}, trigger) {
					t.Errorf("advanced branch cannot trigger implicitly: %q", trigger)
				}
			}
			requireSet(t, branch, "fields", false, branchFields[id]...)
			requireFlags(t, branch, []string{"explanationRequired", "preserveAnswers"}, true)
		})
	}
	equal(t, root, "unattended.configFlag", "--config")
	equal(t, root, "unattended.approvalFlag", "--approve-plan")
	requireSet(t, root, "unattended.approvalBindsTo", true, "config-digest", "artifact-digests", "target-identity")
	requireFlags(t, root, []string{
		"unattended.sameResolvedConfiguration", "unattended.rejectMissingRequired",
		"unattended.approvalBeforeMutation", "unattended.exportResolvedConfiguration",
	}, true)
	requireSet(t, root, "secretInputChannels", true, "protected-prompt", "secret-reference")
	requireSet(t, root, "safetyRules", false,
		"redact-secrets", "no-default-password", "no-secrets-in-argv", "resume-verified-steps",
		"preserve-data-on-uninstall", "target-specific-destructive-approval", "no-silent-elevation",
		"no-unrelated-resource-adoption", "no-cluster-wide-service-replacement", "no-model-controlled-commands",
		"reapply-preserves-intent", "validate-shared-budgets-before-scaling", "cross-service-readiness",
	)
	requireSet(t, root, "notRequiredAtInstall", false,
		"source-accounts", "hosted-ai", "sso", "github", "proprietary-ci", "docker-desktop",
	)
}

func TestSupportedTargetMatrix(t *testing.T) {
	root := artifact(t, "installer.contract.json", "InstallerContract")
	targets := indexed(t, root, "targetMatrix")
	exactIDs(t, targets, "linux", "kubernetes")
	for id, target := range targets {
		t.Run(id, func(t *testing.T) {
			textAt(t, target, "platform")
			textAt(t, target, "referenceProfile")
			versions := stringsAt(t, target, "versions")
			if len(versions) == 0 {
				t.Fatal("freeze at least one explicit proposed platform version")
			}
			for _, version := range versions {
				if strings.ContainsAny(version, "*<>") || strings.Contains(strings.ToLower(version), "latest") {
					t.Errorf("platform version must be explicit: %q", version)
				}
			}
			requireSet(t, target, "architectures", false, "amd64")
			if id == "linux" {
				requireSet(t, target, "prerequisites", false, "systemd", "cgroup-v2", "podman")
			} else {
				requireSet(t, target, "prerequisites", false, "existing-context", "persistent-storage", "explicit-ingress")
			}
			state := textAt(t, target, "validationState")
			if !contains([]string{"not-run", "blocked", "passed", "failed"}, state) {
				t.Errorf("unknown target validation state %q", state)
			}
			if state == "passed" {
				assertEvidenceFile(t, textAt(t, target, "validationEvidenceRef"))
			} else {
				textAt(t, target, "validationReason")
				equal(t, target, "readyForProduction", false)
			}
		})
	}
}
