package delivery_deployment

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const jiraOriginsEnvironment = "ASPM_JIRA_API_ORIGINS"

func jiraRenderedRoles(t *testing.T, input map[string]any) map[string]deployment {
	t.Helper()
	data, diagnostic, err := render(t, input)
	if err != nil {
		t.Fatalf("valid Jira origin selection failed actual Helm rendering (%T): %s", err, diagnostic)
	}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	roles := map[string]deployment{}
	for count := 0; ; count++ {
		var item deployment
		err := decoder.Decode(&item)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || count >= 32 {
			t.Fatal("actual Helm output is invalid or exceeds its document bound")
		}
		if item.Kind != "Deployment" {
			continue
		}
		role := item.Metadata.Labels["app.kubernetes.io/component"]
		if _, duplicate := roles[role]; role == "" || duplicate {
			t.Fatal("rendered deployment has a missing or duplicate role")
		}
		roles[role] = item
	}
	for _, role := range []string{"core", "ingestion", "reports"} {
		if _, present := roles[role]; !present {
			t.Fatalf("Jira origin selection removed the original %s role", role)
		}
	}
	return roles
}

func assertJiraOrigins(t *testing.T, roles map[string]deployment, expected []string) {
	t.Helper()
	found := 0
	for role, workload := range roles {
		containers := append([]container{}, workload.Spec.Template.Spec.InitContainers...)
		containers = append(containers, workload.Spec.Template.Spec.Containers...)
		for _, item := range containers {
			variable, present := environment(t, item)[jiraOriginsEnvironment]
			if !present {
				continue
			}
			found++
			if role != "delivery" || item.Name != "delivery" || len(expected) == 0 {
				t.Fatalf("unexpected Jira origin admission in %s/%s", role, item.Name)
			}
			value, stringValue := variable.Value.(string)
			if !stringValue || variable.ValueFrom.SecretKeyRef != nil {
				t.Fatal("Jira origins must be a literal environment string, not a list or Secret selector")
			}
			var actual []string
			if json.Unmarshal([]byte(value), &actual) != nil || !reflect.DeepEqual(actual, expected) {
				t.Fatal("rendered Jira origins did not preserve the exact ordered JSON array")
			}
		}
	}
	if len(expected) == 0 && found != 0 || len(expected) != 0 && found != 1 {
		t.Fatal("Jira origins were omitted, duplicated or implicitly supplied")
	}
}

func TestJiraDeploymentHelmDefaultsAndDisabledSelection(t *testing.T) {
	t.Run("shipped-empty-list", func(t *testing.T) {
		var defaults map[string]any
		if yaml.Unmarshal(source(t, "deploy", "helm", "aspm", "values.yaml"), &defaults) != nil {
			t.Fatal("shipped Helm values are not valid YAML")
		}
		delivery, ok := defaults["delivery"].(map[string]any)
		if !ok {
			t.Fatal("shipped delivery settings are missing")
		}
		origins, ok := delivery["jiraAPIOrigins"].([]any)
		if !ok || len(origins) != 0 || delivery["enabled"] != false {
			t.Fatal("delivery.jiraAPIOrigins must ship as [] with delivery disabled")
		}
	})
	for _, row := range []struct {
		name      string
		selection map[string]any
		enabled   bool
	}{
		{"default-disabled", nil, false},
		{"selected-but-disabled", map[string]any{"enabled": false, "jiraAPIOrigins": []string{"https://jira.example.invalid"}}, false},
		{"enabled-omitted", map[string]any{"enabled": true}, true},
		{"enabled-empty", map[string]any{"enabled": true, "jiraAPIOrigins": []string{}}, true},
		{"helm-null-removes-key", map[string]any{"enabled": true, "jiraAPIOrigins": nil}, true},
	} {
		t.Run(row.name, func(t *testing.T) {
			input := values()
			selectedKey(input)
			if row.selection != nil {
				input["delivery"] = row.selection
			}
			input["global"] = map[string]any{"jiraAPIOrigins": []string{"https://global.example.invalid"}}
			input["jiraAPIOrigins"] = []string{"https://top-level.example.invalid"}
			roles := jiraRenderedRoles(t, input)
			_, enabled := roles["delivery"]
			if enabled != row.enabled {
				t.Fatal("Jira origins changed the independently selected delivery activation")
			}
			assertJiraOrigins(t, roles, nil)
			if enabled {
				env := environment(t, mainContainer(t, roles, "delivery"))
				if env["ASPM_SLACK_ENDPOINT"].Value != "https://slack.com" ||
					env["ASPM_DELIVERY_LEASE_DURATION"].Value != "15s" {
					t.Fatal("empty Jira selection changed existing Slack or lease defaults")
				}
			}
		})
	}
}

func TestJiraDeploymentHelmExactOriginsAndAllRoleIsolation(t *testing.T) {
	for _, allRoles := range []bool{true, false} {
		name := "all-six-roles"
		origins := []string{"https://jira-gateway.example.invalid:8443", "https://api.atlassian.com", "https://[::1]:18443"}
		if !allRoles {
			name = "sixteen-origin-boundary"
			origins = []string{}
			for i := 0; i < 16; i++ {
				origins = append(origins, fmt.Sprintf("https://jira-%d.example.invalid", i))
			}
		}
		t.Run(name, func(t *testing.T) {
			input := values()
			selectedKey(input)
			input["delivery"] = map[string]any{
				"enabled": true, "jiraAPIOrigins": origins,
				"slackEndpoint": "https://slack-gateway.example.invalid/slack", "leaseDuration": "30s",
			}
			if allRoles {
				input["core"].(map[string]any)["collectionS3Secret"] = map[string]any{
					"name": "synthetic-collection-reader", "accessKeyKey": "reader-access", "secretKeyKey": "reader-secret",
				}
				input["collection"] = map[string]any{
					"enabled": true,
					"storage": map[string]any{
						"endpoint": "https://evidence.example.invalid", "bucket": "selected-source-evidence",
						"prefix": "collections/owned-team/", "region": "selected-region",
					},
					"s3Secret": map[string]any{
						"name": "synthetic-collection-publisher", "accessKeyKey": "publisher-access", "secretKeyKey": "publisher-secret",
					},
				}
				input["assessment"] = map[string]any{"enabled": true, "scope": "synthetic-jira-deployment"}
			}
			roles := jiraRenderedRoles(t, input)
			expectedRoles := []string{"core", "ingestion", "reports", "delivery"}
			if allRoles {
				expectedRoles = append(expectedRoles, "collection", "assessment")
			}
			if len(roles) != len(expectedRoles) {
				t.Fatal("Jira origin selection changed the selected workload set")
			}
			for _, role := range expectedRoles {
				mainContainer(t, roles, role)
			}
			assertJiraOrigins(t, roles, origins)
			delivery := mainContainer(t, roles, "delivery")
			env := environment(t, delivery)
			if !reflect.DeepEqual(delivery.Command, []string{"/app/bin/delivery-worker"}) ||
				env["ASPM_SLACK_ENDPOINT"].Value != "https://slack-gateway.example.invalid/slack" ||
				env["ASPM_DELIVERY_LEASE_DURATION"].Value != "30s" {
				t.Fatal("Jira origin selection rewrote the worker command or independent settings")
			}
			requireReference(t, env, "ASPM_DATABASE_URL", "synthetic-operator-db", "database-url")
			requireReference(t, env, "ASPM_INTEGRATION_ENCRYPTION_KEY", "synthetic-integration-key", "integration-encryption-key")
			for key := range env {
				if strings.HasPrefix(key, "ASPM_S3") || strings.HasPrefix(key, "AWS_") ||
					key == "ASPM_BOOTSTRAP_TOKEN" || key == "ASPM_ASSETS" {
					t.Fatalf("Jira delivery received unrelated authority %s", key)
				}
			}
		})
	}
}

func TestJiraDeploymentHelmRejectsMalformedOrigins(t *testing.T) {
	tooMany := []string{}
	for i := 0; i < 17; i++ {
		tooMany = append(tooMany, fmt.Sprintf("https://jira-%d.example.invalid", i))
	}
	for _, row := range []struct {
		name  string
		value any
	}{
		{"scalar-origin", "https://jira.example.invalid"},
		{"encoded-json-string", `["https://jira.example.invalid"]`},
		{"boolean", true},
		{"number", 1},
		{"object", map[string]any{"origin": "https://jira.example.invalid"}},
		{"null-entry", []any{nil}},
		{"number-entry", []any{1}},
		{"empty-entry", []string{""}},
		{"whitespace-entry", []string{" "}},
		{"seventeen-origins", tooMany},
		{"duplicate", []string{"https://jira.example.invalid", "https://jira.example.invalid"}},
		{"http", []string{"http://jira.example.invalid"}},
		{"credential-bearing", []string{"https://synthetic-private:do-not-echo@jira.example.invalid"}},
		{"trailing-slash", []string{"https://jira.example.invalid/"}},
		{"path", []string{"https://jira.example.invalid/ex/jira/tenant"}},
		{"query", []string{"https://jira.example.invalid?selected=true"}},
		{"fragment", []string{"https://jira.example.invalid#selected"}},
		{"wildcard", []string{"https://*.example.invalid"}},
		{"control", []string{"https://jira.\nexample.invalid"}},
	} {
		for _, enabled := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s-enabled-%t", row.name, enabled), func(t *testing.T) {
				input := values()
				selectedKey(input)
				input["delivery"] = map[string]any{"enabled": enabled, "jiraAPIOrigins": row.value}
				_, diagnostic, err := render(t, input)
				var exit *exec.ExitError
				if !errors.As(err, &exit) || !strings.Contains(diagnostic, "delivery.jiraAPIOrigins") {
					t.Fatalf("real Helm must reject delivery.jiraAPIOrigins explicitly, error type %T", err)
				}
				if strings.Contains(diagnostic, "synthetic-private") || strings.Contains(diagnostic, "do-not-echo") {
					t.Fatal("render failure echoed a credential-bearing invalid origin")
				}
			})
		}
	}
}
