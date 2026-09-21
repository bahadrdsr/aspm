package assessment_deployment

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

var assessmentFields = map[string]string{
	"scope":                 "ASPM_ASSESSMENT_SCOPE",
	"leaseDuration":         "ASPM_ASSESSMENT_LEASE_DURATION",
	"authorizationInterval": "ASPM_ASSESSMENT_AUTHORIZATION_INTERVAL",
	"requestTimeout":        "ASPM_ASSESSMENT_REQUEST_TIMEOUT",
	"requestWindow":         "ASPM_ASSESSMENT_REQUEST_WINDOW",
	"maxConcurrent":         "ASPM_ASSESSMENT_MAX_CONCURRENT",
	"requestsPerWindow":     "ASPM_ASSESSMENT_REQUESTS_PER_WINDOW",
	"maxInputBytes":         "ASPM_ASSESSMENT_MAX_INPUT_BYTES",
	"maxOutputTokens":       "ASPM_ASSESSMENT_MAX_OUTPUT_TOKENS",
	"maxResponseBytes":      "ASPM_ASSESSMENT_MAX_RESPONSE_BYTES",
}

func assessmentDefaults() object {
	return object{
		"enabled": false, "replicas": 1, "scope": "", "leaseDuration": "15s",
		"authorizationInterval": "100ms", "requestTimeout": "10s", "requestWindow": "1m",
		"maxConcurrent": 1, "requestsPerWindow": 30, "maxInputBytes": 32768,
		"maxOutputTokens": 1024, "maxResponseBytes": 65536,
		"resources": object{"requests": object{"cpu": "100m", "memory": "128Mi"},
			"limits": object{"cpu": "1", "memory": "512Mi"}},
	}
}

func assessmentValues(scope string, key bool) object {
	input := legacyValues(false)
	if key {
		selectIntegrationKey(input)
	}
	input["assessment"] = object{"enabled": true, "scope": scope}
	return input
}

func expectedSettings(input object) object {
	settings := assessmentDefaults()
	for key, value := range input["assessment"].(object) {
		settings[key] = value
	}
	return settings
}

func noUnrelatedAssessmentAuthority(t *testing.T, output rendered, scope string) {
	t.Helper()
	for role, workload := range output.Roles {
		if role == "assessment" {
			continue
		}
		for _, c := range append(workload.Spec.Template.Spec.InitContainers, workload.Spec.Template.Spec.Containers...) {
			for name, value := range environment(t, c) {
				if !strings.HasPrefix(name, "ASPM_ASSESSMENT_") {
					continue
				}
				if role != "core" || c.Name != "core" || name != "ASPM_ASSESSMENT_SCOPE" || value.ValueFrom != nil || value.Value != scope {
					t.Fatalf("unrelated role/container %s/%s received assessment authority %s", role, c.Name, name)
				}
			}
		}
	}
	if scope != "" {
		requireString(t, environment(t, mainContainer(t, output, "core")), "ASPM_ASSESSMENT_SCOPE", scope)
	}
}

func cloneDocument(t *testing.T, doc object) object {
	t.Helper()
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatal("cannot normalize parsed artifact for comparison")
	}
	var result object
	if json.Unmarshal(data, &result) != nil {
		t.Fatal("cannot normalize parsed artifact document")
	}
	return result
}

func withoutCoreScope(doc object) {
	spec := doc["spec"].(object)["template"].(object)["spec"].(object)
	for _, raw := range spec["containers"].([]any) {
		c := raw.(object)
		if c["name"] != "core" {
			continue
		}
		var kept []any
		for _, rawEnv := range c["env"].([]any) {
			if rawEnv.(object)["name"] != "ASPM_ASSESSMENT_SCOPE" {
				kept = append(kept, rawEnv)
			}
		}
		c["env"] = kept
	}
}

func preserveExistingArtifacts(t *testing.T, baseline, output rendered) {
	t.Helper()
	coreKey := "Deployment/" + baseline.Roles["core"].Metadata.Name
	for key, original := range baseline.Documents {
		current, present := output.Documents[key]
		if !present {
			t.Fatalf("assessment configuration dropped existing resource %s", key)
		}
		old, now := cloneDocument(t, original), cloneDocument(t, current)
		if key == coreKey {
			withoutCoreScope(old)
			withoutCoreScope(now)
		}
		if !reflect.DeepEqual(old, now) {
			t.Fatalf("assessment changed existing workload/environment/managed-storage/policy resource %s", key)
		}
	}
	for key := range output.Documents {
		if _, present := baseline.Documents[key]; present {
			continue
		}
		assessment, exists := output.Roles["assessment"]
		if !exists || key != "Deployment/"+assessment.Metadata.Name {
			t.Fatalf("assessment added unauthorized Secret/Service/Ingress/RBAC/policy or other resource %s", key)
		}
	}
}

func preserveManagedInfrastructure(t *testing.T, baseline, output rendered) {
	t.Helper()
	for key, original := range baseline.Documents {
		if original["kind"] == "Deployment" {
			continue
		}
		if !reflect.DeepEqual(original, output.Documents[key]) {
			t.Fatalf("independent opt-in changed existing managed PG/S3/service/policy resource %s", key)
		}
	}
	for key, current := range output.Documents {
		if current["kind"] != "Deployment" {
			if _, present := baseline.Documents[key]; !present {
				t.Fatalf("independent opt-in introduced an extra non-workload resource %s", key)
			}
		}
	}
}

func boundedTmpSize(value any) bool {
	text, ok := value.(string)
	if !ok {
		return false
	}
	match := regexp.MustCompile(`^([1-9][0-9]*)(Mi|Gi)$`).FindStringSubmatch(text)
	if len(match) != 3 {
		return false
	}
	size, err := strconv.ParseUint(match[1], 10, 64)
	return err == nil && (match[2] == "Mi" && size <= 2048 || match[2] == "Gi" && size <= 2)
}

func requireAssessment(t *testing.T, output rendered, input object) {
	t.Helper()
	c := mainContainer(t, output, "assessment")
	workload := output.Roles["assessment"]
	pod := workload.Spec.Template.Spec
	settings := expectedSettings(input)
	if !reflect.DeepEqual(c.Command, []string{"/app/bin/assessment-worker"}) || len(c.Args) != 0 ||
		len(pod.Containers) != 1 || len(pod.InitContainers) != 0 {
		t.Fatal("assessment must use exactly its independent actual worker, without wrappers, sidecars or init authority")
	}
	if workload.Spec.Replicas == nil || *workload.Spec.Replicas != settings["replicas"] ||
		!reflect.DeepEqual(c.Resources, settings["resources"]) {
		t.Fatal("assessment replicas/resources are not its own exact selected profile")
	}
	if workload.Spec.Selector.MatchLabels["app.kubernetes.io/component"] != "assessment" ||
		workload.Spec.Template.Metadata.Labels["app.kubernetes.io/component"] != "assessment" {
		t.Fatal("assessment selector/pod role is not independent")
	}
	env := environment(t, c)
	allowed := map[string]bool{"ASPM_DATABASE_URL": true, "ASPM_SCHEMA": true, "ASPM_DB_MAX_CONNECTIONS": true, "ASPM_LISTEN": true}
	for field, name := range assessmentFields {
		allowed[name] = true
		requireString(t, env, name, fmt.Sprint(settings[field]))
	}
	requireReference(t, env, "ASPM_DATABASE_URL", "synthetic-operator-db", "database-url")
	requireString(t, env, "ASPM_SCHEMA", "aspm")
	requireString(t, env, "ASPM_DB_MAX_CONNECTIONS", "5")
	if _, exists := env["ASPM_LISTEN"]; exists {
		requireString(t, env, "ASPM_LISTEN", "0.0.0.0:8080")
	}
	if selected, exists := input["integrationKeySecret"].(object); exists && len(selected) != 0 {
		allowed["ASPM_INTEGRATION_ENCRYPTION_KEY"] = true
		requireReference(t, env, "ASPM_INTEGRATION_ENCRYPTION_KEY", selected["name"].(string), selected["key"].(string))
		requireReference(t, environment(t, mainContainer(t, output, "core")), "ASPM_INTEGRATION_ENCRYPTION_KEY",
			selected["name"].(string), selected["key"].(string))
	} else {
		for _, role := range []string{"core", "assessment"} {
			if _, present := environment(t, mainContainer(t, output, role))["ASPM_INTEGRATION_ENCRYPTION_KEY"]; present {
				t.Fatal("keyless assessment/core received a fallback or invented encryption key")
			}
		}
	}
	for name, value := range env {
		if !allowed[name] {
			t.Fatalf("assessment gained unrelated storage/bootstrap/assets/AWS/CA/gateway/model/template authority %s", name)
		}
		if value.ValueFrom != nil && name != "ASPM_DATABASE_URL" && name != "ASPM_INTEGRATION_ENCRYPTION_KEY" {
			t.Fatal("assessment received an extra Secret authority")
		}
	}
	requireRoleHardening(t, workload, c)
	noUnrelatedAssessmentAuthority(t, output, settings["scope"].(string))
}

func requireRoleHardening(t *testing.T, workload deployment, c container) {
	t.Helper()
	pod := workload.Spec.Template.Spec
	if pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken ||
		pod.SecurityContext["runAsNonRoot"] != true || pod.SecurityContext["runAsUser"] != 10001 ||
		pod.SecurityContext["runAsGroup"] != 10001 {
		t.Fatal("assessment lost existing nonroot/no-service-account-token hardening")
	}
	for _, field := range []string{"serviceAccount", "serviceAccountName", "imagePullSecrets", "hostNetwork", "hostPID", "hostIPC"} {
		if _, present := pod.Other[field]; present {
			t.Fatalf("assessment gained extra identity/host capability %s", field)
		}
	}
	if c.SecurityContext["allowPrivilegeEscalation"] != false || c.SecurityContext["readOnlyRootFilesystem"] != true ||
		!reflect.DeepEqual(c.SecurityContext["capabilities"], object{"drop": []any{"ALL"}}) ||
		!reflect.DeepEqual(c.SecurityContext["seccompProfile"], object{"type": "RuntimeDefault"}) {
		t.Fatal("assessment lost read-only/no-escalation/drop-all/seccomp hardening")
	}
	for key := range c.SecurityContext {
		if key != "allowPrivilegeEscalation" && key != "readOnlyRootFilesystem" && key != "capabilities" && key != "seccompProfile" {
			t.Fatalf("minimal assessment profile gained an extra security-context directive %s", key)
		}
	}
	if len(pod.Volumes) != 1 || pod.Volumes[0]["name"] != "tmp" || len(pod.Volumes[0]) != 2 ||
		len(c.VolumeMounts) != 1 || c.VolumeMounts[0]["name"] != "tmp" || c.VolumeMounts[0]["mountPath"] != "/tmp" {
		t.Fatal("assessment must have only the bounded writable tmp volume, no CA or credential mount schema")
	}
	directory, ok := pod.Volumes[0]["emptyDir"].(object)
	if !ok || !boundedTmpSize(directory["sizeLimit"]) {
		t.Fatal("assessment tmp volume lacks a positive size bound <=2Gi")
	}
	for _, selected := range []struct {
		value probe
		path  string
	}{{c.StartupProbe, "/readyz"}, {c.ReadinessProbe, "/readyz"}, {c.LivenessProbe, "/healthz"}} {
		if selected.value.HTTPGet.Path != selected.path {
			t.Fatal("assessment probes must use the accepted native worker health paths")
		}
		matched := false
		for _, port := range c.Ports {
			matched = matched || selected.value.HTTPGet.Port == port.Name && port.Name != "" ||
				selected.value.HTTPGet.Port == port.ContainerPort
		}
		if !matched {
			t.Fatal("assessment probe port does not resolve to its declared worker port")
		}
	}
}

func TestAD2HelmOptInCoreScopeKeylessAndIndependentRoles(t *testing.T) {
	base := successfulRender(t, legacyValues(false))
	withKey := legacyValues(false)
	selectIntegrationKey(withKey)
	keyBase := successfulRender(t, withKey)
	var combined rendered
	t.Run("legacy-default-and-explicit-empty-off", func(t *testing.T) {
		if len(base.Roles) != 3 {
			t.Fatal("default opt-ins must leave only the original three application roles")
		}
		input := legacyValues(false)
		input["assessment"] = object{"enabled": false, "scope": ""}
		off := successfulRender(t, input)
		if !reflect.DeepEqual(base.Documents, off.Documents) {
			t.Fatal("explicit empty/off assessment changed the existing rendered resources")
		}
		noUnrelatedAssessmentAuthority(t, base, "")
		noUnrelatedAssessmentAuthority(t, off, "")
		preserveExistingArtifacts(t, base, off)
		for _, role := range []string{"core", "ingestion", "reports"} {
			requireRoleHardening(t, base.Roles[role], mainContainer(t, base, role))
		}
	})
	t.Run("legacy-independent-delivery", func(t *testing.T) {
		output := successfulRender(t, legacyValues(true))
		if len(output.Roles) != 4 || !reflect.DeepEqual(mainContainer(t, output, "delivery").Command, []string{"/app/bin/delivery-worker"}) {
			t.Fatal("existing independent delivery opt-in changed")
		}
		env := environment(t, mainContainer(t, output, "delivery"))
		requireReference(t, env, "ASPM_INTEGRATION_ENCRYPTION_KEY", "synthetic-integration-key", "encryption-key")
		requireString(t, env, "ASPM_DELIVERY_LEASE_DURATION", "15s")
		requireString(t, env, "ASPM_SLACK_ENDPOINT", "https://slack.com")
		noUnrelatedAssessmentAuthority(t, output, "")
		preserveManagedInfrastructure(t, base, output)
	})
	t.Run("legacy-independent-collection", func(t *testing.T) {
		input := collectionValues(true, false)
		output := successfulRender(t, input)
		if len(output.Roles) != 4 || !reflect.DeepEqual(mainContainer(t, output, "collection").Command, []string{"/app/bin/collection-worker"}) {
			t.Fatal("existing independent collection opt-in changed")
		}
		requireStorage(t, environment(t, mainContainer(t, output, "collection")), input, input["collection"].(object)["s3Secret"].(object))
		requireStorage(t, environment(t, mainContainer(t, output, "core")), input, input["core"].(object)["collectionS3Secret"].(object))
		noUnrelatedAssessmentAuthority(t, output, "")
		preserveManagedInfrastructure(t, base, output)
	})
	t.Run("legacy-delivery-and-collection-together", func(t *testing.T) {
		combined = successfulRender(t, collectionValues(true, true))
		if len(combined.Roles) != 5 {
			t.Fatal("existing independent delivery plus collection roles did not coexist")
		}
		noUnrelatedAssessmentAuthority(t, combined, "")
		preserveManagedInfrastructure(t, base, combined)
	})
	t.Run("core-only-explicit-unicode-scope", func(t *testing.T) {
		input := assessmentValues("preconfigure/安全", true)
		input["assessment"].(object)["enabled"] = false
		output := successfulRender(t, input)
		if _, present := output.Roles["assessment"]; present {
			t.Fatal("core scope preconfiguration automatically enabled a worker")
		}
		noUnrelatedAssessmentAuthority(t, output, "preconfigure/安全")
		preserveExistingArtifacts(t, keyBase, output)
	})
	t.Run("enabled-defaults-with-explicit-key", func(t *testing.T) {
		input := assessmentValues("owned-shared-assessment-pool", true)
		output := successfulRender(t, input)
		requireAssessment(t, output, input)
		if len(output.Roles) != 4 {
			t.Fatal("assessment enablement must add exactly one independent Deployment")
		}
		preserveExistingArtifacts(t, keyBase, output)
	})
	t.Run("enabled-keyless-exact-128-byte-scope", func(t *testing.T) {
		input := assessmentValues(strings.Repeat("x", 128), false)
		output := successfulRender(t, input)
		requireAssessment(t, output, input)
		preserveExistingArtifacts(t, base, output)
	})
	t.Run("selected-limits-resources-coexist-with-both-workers", func(t *testing.T) {
		input := collectionValues(true, true)
		input["assessment"] = object{"enabled": true, "replicas": 2, "scope": "selected-shared-scope",
			"leaseDuration": "20s", "authorizationInterval": "200ms", "requestTimeout": "8s", "requestWindow": "2m",
			"maxConcurrent": 2, "requestsPerWindow": 7, "maxInputBytes": 4096, "maxOutputTokens": 512, "maxResponseBytes": 32768,
			"resources": object{"requests": object{"cpu": "137m", "memory": "192Mi"}, "limits": object{"cpu": "2", "memory": "768Mi"}}}
		output := successfulRender(t, input)
		requireAssessment(t, output, input)
		if len(output.Roles) != 6 {
			t.Fatal("all three independently selected worker roles must coexist")
		}
		preserveExistingArtifacts(t, combined, output)
	})
}

func TestAD3HelmDefaultsAndPreciseFiniteValidation(t *testing.T) {
	t.Run("declared-default-profile", func(t *testing.T) {
		var values object
		if yaml.Unmarshal(source(t, "deploy", "helm", "aspm", "values.yaml"), &values) != nil {
			t.Fatal("actual values.yaml cannot be parsed")
		}
		if !reflect.DeepEqual(values["assessment"], assessmentDefaults()) {
			t.Fatal("actual values.yaml must declare exactly the opt-in assessment profile/defaults, without generic env/Secret/CA/provider knobs")
		}
	})
	type invalid struct {
		name, path string
		change     func(object)
	}
	rows := []invalid{
		{"assessment-type", "assessment", func(v object) { v["assessment"] = "not-a-mapping" }},
		{"enabled-string-false", "assessment.enabled", func(v object) { v["assessment"].(object)["enabled"] = "false" }},
		{"enabled-number", "assessment.enabled", func(v object) { v["assessment"].(object)["enabled"] = 1 }},
		{"enabled-missing-scope", "assessment.scope", func(v object) { delete(v["assessment"].(object), "scope") }},
		{"disabled-padded-scope", "assessment.scope", func(v object) { v["assessment"] = object{"enabled": false, "scope": " padded "} }},
		{"disabled-control-scope", "assessment.scope", func(v object) { v["assessment"] = object{"enabled": false, "scope": "bad\nscope"} }},
		{"disabled-bool-scope", "assessment.scope", func(v object) { v["assessment"] = object{"enabled": false, "scope": false} }},
		{"disabled-oversize-utf8-scope", "assessment.scope", func(v object) { v["assessment"] = object{"enabled": false, "scope": strings.Repeat("界", 43)} }},
		{"blank-request-timeout", "assessment.requestTimeout", func(v object) { v["assessment"].(object)["requestTimeout"] = "" }},
		{"numeric-lease", "assessment.leaseDuration", func(v object) { v["assessment"].(object)["leaseDuration"] = 15 }},
		{"blank-authorization-interval", "assessment.authorizationInterval", func(v object) { v["assessment"].(object)["authorizationInterval"] = "" }},
		{"string-concurrency", "assessment.maxConcurrent", func(v object) { v["assessment"].(object)["maxConcurrent"] = "2" }},
		{"fractional-concurrency", "assessment.maxConcurrent", func(v object) { v["assessment"].(object)["maxConcurrent"] = 1.5 }},
		{"zero-request-capacity", "assessment.requestsPerWindow", func(v object) { v["assessment"].(object)["requestsPerWindow"] = 0 }},
		{"oversize-response-cap", "assessment.maxResponseBytes", func(v object) { v["assessment"].(object)["maxResponseBytes"] = 131073 }},
		{"partial-integration-name-only-disabled", "integrationKeySecret.key", func(v object) {
			v["assessment"].(object)["enabled"] = false
			v["integrationKeySecret"] = object{"name": "synthetic-integration-key"}
		}},
		{"partial-integration-key-only", "integrationKeySecret.name", func(v object) { v["integrationKeySecret"] = object{"key": "encryption-key"} }},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			input := assessmentValues("selected-valid-scope", false)
			row.change(input)
			_, diagnostic, err := render(t, input)
			var exit *exec.ExitError
			if !errors.As(err, &exit) || !strings.Contains(diagnostic, row.path) {
				t.Errorf("actual Helm render must reject supplied invalid %s with that precise path; error type %T", row.path, err)
			}
		})
	}
}
