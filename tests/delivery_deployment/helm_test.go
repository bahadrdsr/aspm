package delivery_deployment

import (
	"errors"
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func selectedKey(input map[string]any) {
	input["integrationKeySecret"] = map[string]any{"name": "synthetic-integration-key", "key": "integration-encryption-key"}
}

func TestDeliveryDeploymentHelmOptInAndExactCoreDeliveryKeyReferences(t *testing.T) {
	t.Run("default-absence-control", func(t *testing.T) {
		roles := successfulRender(t, values())
		if _, present := roles["delivery"]; present {
			t.Fatal("default chart silently enables the delivery worker")
		}
		assertNoIntegrationKey(t, roles, "core")
		assertNoIntegrationKey(t, roles, "ingestion")
		assertNoIntegrationKey(t, roles, "reports")
	})
	t.Run("configure-core-before-worker-rollout", func(t *testing.T) {
		input := values()
		selectedKey(input)
		input["delivery"] = map[string]any{"enabled": false}
		roles := successfulRender(t, input)
		if _, present := roles["delivery"]; present {
			t.Fatal("explicit disabled delivery unexpectedly rendered a worker")
		}
		requireReference(t, environment(t, mainContainer(t, roles, "core")), "ASPM_INTEGRATION_ENCRYPTION_KEY",
			"synthetic-integration-key", "integration-encryption-key")
		assertNoIntegrationKey(t, roles, "ingestion")
		assertNoIntegrationKey(t, roles, "reports")
	})
	t.Run("enabled-worker-defaults", func(t *testing.T) {
		input := values()
		selectedKey(input)
		input["delivery"] = map[string]any{"enabled": true}
		roles := successfulRender(t, input)
		delivery := mainContainer(t, roles, "delivery")
		if !reflect.DeepEqual(delivery.Command, []string{"/app/bin/delivery-worker"}) {
			t.Fatal("enabled delivery must run the distinct actual worker binary")
		}
		for _, role := range []string{"core", "delivery"} {
			requireReference(t, environment(t, mainContainer(t, roles, role)), "ASPM_INTEGRATION_ENCRYPTION_KEY",
				"synthetic-integration-key", "integration-encryption-key")
		}
		env := environment(t, delivery)
		if env["ASPM_DELIVERY_LEASE_DURATION"].Value != "15s" || env["ASPM_SLACK_ENDPOINT"].Value != "https://slack.com" {
			t.Fatal("delivery must render bounded lease/native gateway defaults as strings")
		}
	})
}

func TestDeliveryDeploymentHelmRejectsInvalidSelectionsAndIsolatesWorker(t *testing.T) {
	for _, row := range []struct {
		name, requiredPath string
		configure          func(map[string]any)
	}{
		{"missing-selected-key", "integrationKeySecret.name", func(v map[string]any) { delete(v, "integrationKeySecret") }},
		{"missing-key-name", "integrationKeySecret.name", func(v map[string]any) { v["integrationKeySecret"].(map[string]any)["name"] = "" }},
		{"missing-key-field", "integrationKeySecret.key", func(v map[string]any) { v["integrationKeySecret"].(map[string]any)["key"] = "" }},
		{"disabled-partial-key", "integrationKeySecret.key", func(v map[string]any) {
			v["delivery"].(map[string]any)["enabled"] = false
			v["integrationKeySecret"].(map[string]any)["key"] = ""
		}},
		{"string-true", "delivery.enabled", func(v map[string]any) { v["delivery"].(map[string]any)["enabled"] = "true" }},
		{"string-false", "delivery.enabled", func(v map[string]any) { v["delivery"].(map[string]any)["enabled"] = "false" }},
		{"integer-enabled", "delivery.enabled", func(v map[string]any) { v["delivery"].(map[string]any)["enabled"] = 1 }},
		{"credential-bearing-endpoint", "delivery.slackEndpoint", func(v map[string]any) {
			v["delivery"].(map[string]any)["slackEndpoint"] = "https://synthetic:never-a-credential@gateway.synthetic.invalid"
		}},
	} {
		t.Run(row.name, func(t *testing.T) {
			input := values()
			selectedKey(input)
			input["delivery"] = map[string]any{"enabled": true}
			row.configure(input)
			_, diagnostic, err := render(t, input)
			var exit *exec.ExitError
			if !errors.As(err, &exit) || !strings.Contains(diagnostic, row.requiredPath) {
				t.Errorf("actual Helm rendering must reject %s explicitly; error type %T", row.requiredPath, err)
			}
		})
	}
	t.Run("enabled-isolation-and-health", func(t *testing.T) {
		input := values()
		selectedKey(input)
		input["delivery"] = map[string]any{"enabled": true, "leaseDuration": "30s", "slackEndpoint": "https://gateway.synthetic.invalid/slack"}
		roles := successfulRender(t, input)
		delivery := mainContainer(t, roles, "delivery")
		variables := environment(t, delivery)
		requireReference(t, variables, "ASPM_DATABASE_URL", "synthetic-operator-db", "database-url")
		if variables["ASPM_DELIVERY_LEASE_DURATION"].Value != "30s" ||
			variables["ASPM_SLACK_ENDPOINT"].Value != "https://gateway.synthetic.invalid/slack" {
			t.Error("explicit trusted worker lease/gateway selections were rewritten")
		}
		for _, item := range append(roles["delivery"].Spec.Template.Spec.InitContainers, roles["delivery"].Spec.Template.Spec.Containers...) {
			for name, value := range environment(t, item) {
				if strings.HasPrefix(name, "ASPM_S3") || strings.HasPrefix(name, "AWS_") ||
					name == "ASPM_BOOTSTRAP_TOKEN" || name == "ASPM_ASSETS" {
					t.Errorf("delivery runtime must not receive %s", name)
				}
				if ref := value.ValueFrom.SecretKeyRef; ref != nil &&
					!(ref.Name == "synthetic-operator-db" && ref.Key == "database-url") &&
					!(ref.Name == "synthetic-integration-key" && ref.Key == "integration-encryption-key") {
					t.Error("delivery received an unselected storage/operator credential reference")
				}
			}
		}
		if delivery.StartupProbe.HTTPGet.Path != "/readyz" || delivery.ReadinessProbe.HTTPGet.Path != "/readyz" ||
			delivery.LivenessProbe.HTTPGet.Path != "/healthz" {
			t.Error("delivery must use the accepted runtime health endpoints")
		}
		for _, role := range []string{"ingestion", "reports"} {
			assertNoIntegrationKey(t, roles, role)
		}
		for _, role := range []string{"core", "ingestion"} {
			env := environment(t, mainContainer(t, roles, role))
			requireReference(t, env, "ASPM_S3_ACCESS_KEY", "synthetic-"+role+"-storage", role+"-access")
			requireReference(t, env, "ASPM_S3_SECRET_KEY", "synthetic-"+role+"-storage", role+"-secret")
			if env["ASPM_S3_PREFIX"].Value != "raw/owned-team/" || env["ASPM_S3_READINESS_KEY"].Value != "raw/owned-team/readiness.txt" {
				t.Errorf("delivery selection altered %s storage scope", role)
			}
		}
	})
}
