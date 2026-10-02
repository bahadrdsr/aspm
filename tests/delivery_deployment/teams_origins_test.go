//go:build teams_runtime

package delivery_deployment

import (
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const teamsOriginsEnvironment = "ASPM_TEAMS_WORKFLOW_ORIGINS"

func assertTeamsOrigins(t *testing.T, roles map[string]deployment, expected []string) {
	t.Helper()
	encoded, err := json.Marshal(expected)
	if err != nil {
		t.Fatal("cannot encode ordered synthetic Teams origins")
	}
	found := 0
	for role, workload := range roles {
		items := append([]container{}, workload.Spec.Template.Spec.InitContainers...)
		items = append(items, workload.Spec.Template.Spec.Containers...)
		for _, item := range items {
			variable, present := environment(t, item)[teamsOriginsEnvironment]
			if !present {
				continue
			}
			found++
			if role != "delivery" || item.Name != "delivery" || len(expected) == 0 {
				t.Fatal("Teams origin admission escaped the enabled delivery main")
			}
			value, literal := variable.Value.(string)
			if !literal || variable.ValueFrom.SecretKeyRef != nil || value != string(encoded) {
				t.Fatal("Teams origins must be one literal ordered JSON string, not a Secret, list, joined value or double encoding")
			}
		}
	}
	if len(expected) == 0 && found != 0 || len(expected) > 0 && found != 1 {
		t.Fatal("Teams environment was omitted, duplicated or implicitly supplied")
	}
}

func TestTeamsDeploymentHelmDefaultsAndInvalidSelections(t *testing.T) {
	t.Run("shipped-empty-and-disabled", func(t *testing.T) {
		var defaults map[string]any
		if yaml.Unmarshal(source(t, "deploy", "helm", "aspm", "values.yaml"), &defaults) != nil {
			t.Fatal("shipped values are not YAML")
		}
		delivery, ok := defaults["delivery"].(map[string]any)
		if !ok {
			t.Fatal("shipped delivery settings are absent")
		}
		origins, ok := delivery["teamsWorkflowOrigins"].([]any)
		if !ok || len(origins) != 0 || delivery["enabled"] != false {
			t.Fatal("delivery.teamsWorkflowOrigins must default to [] without enabling the worker")
		}
	})
	for _, row := range []struct {
		name      string
		selection map[string]any
		enabled   bool
	}{
		{"default-disabled", nil, false},
		{"selected-disabled", map[string]any{"enabled": false, "teamsWorkflowOrigins": []string{"https://workflow.example.invalid"}}, false},
		{"enabled-omitted", map[string]any{"enabled": true}, true},
		{"enabled-empty", map[string]any{"enabled": true, "teamsWorkflowOrigins": []string{}}, true},
		{"helm-null-removes-key", map[string]any{"enabled": true, "teamsWorkflowOrigins": nil}, true},
	} {
		t.Run(row.name, func(t *testing.T) {
			input := values()
			selectedKey(input)
			if row.selection != nil {
				input["delivery"] = row.selection
			}
			input["global"] = map[string]any{"teamsWorkflowOrigins": []string{"https://global.example.invalid"}}
			input["teamsWorkflowOrigins"] = []string{"https://top-level.example.invalid"}
			roles := jiraRenderedRoles(t, input)
			_, enabled := roles["delivery"]
			if enabled != row.enabled {
				t.Fatal("Teams origins changed independent delivery opt-in")
			}
			assertTeamsOrigins(t, roles, nil)
			assertJiraOrigins(t, roles, nil)
			if enabled {
				env := environment(t, mainContainer(t, roles, "delivery"))
				if env["ASPM_SLACK_ENDPOINT"].Value != "https://slack.com" ||
					env["ASPM_DELIVERY_LEASE_DURATION"].Value != "15s" {
					t.Fatal("empty Teams selection changed Slack/lease defaults")
				}
			}
		})
	}

	tooMany := make([]string, 17)
	for i := range tooMany {
		tooMany[i] = fmt.Sprintf("https://private-origin-canary-%d.invalid", i)
	}
	for _, row := range []struct {
		name  string
		value any
	}{
		{"scalar", "https://private-origin-canary.invalid"},
		{"encoded-json-string", `["https://private-origin-canary.invalid"]`},
		{"boolean", true}, {"number", 1}, {"object", map[string]any{"private-origin-canary": true}},
		{"null-entry", []any{nil}}, {"number-entry", []any{1}}, {"boolean-entry", []any{false}},
		{"nested-entry", []any{[]any{}}}, {"empty-entry", []string{""}}, {"whitespace-entry", []string{" "}},
		{"seventeen", tooMany},
		{"duplicate", []string{"https://private-origin-canary.invalid", "https://private-origin-canary.invalid"}},
		{"http", []string{"http://private-origin-canary.invalid"}},
		{"userinfo", []string{"https://private-origin-canary@localhost"}},
		{"trailing-slash", []string{"https://private-origin-canary.invalid/"}},
		{"path", []string{"https://private-origin-canary.invalid/workflows/private"}},
		{"query", []string{"https://private-origin-canary.invalid?sig=private-origin-canary"}},
		{"fragment", []string{"https://private-origin-canary.invalid#private"}},
		{"wildcard", []string{"https://*.private-origin-canary.invalid"}},
		{"control", []string{"https://private-origin-canary.\ninvalid"}},
		{"backslash", []string{"https://private-origin-canary.invalid\\private"}},
	} {
		for _, enabled := range []bool{true, false} {
			t.Run(fmt.Sprintf("%s-enabled-%t", row.name, enabled), func(t *testing.T) {
				input := values()
				selectedKey(input)
				input["delivery"] = map[string]any{
					"enabled": enabled, "teamsWorkflowOrigins": row.value,
					"jiraAPIOrigins": []string{"https://jira.example.invalid"},
				}
				_, diagnostic, err := render(t, input)
				var exit *exec.ExitError
				if !errors.As(err, &exit) || !strings.Contains(diagnostic, "delivery.teamsWorkflowOrigins") {
					t.Fatalf("actual Helm must reject delivery.teamsWorkflowOrigins explicitly (%T)", err)
				}
				if strings.Contains(diagnostic, "private-origin-canary") {
					t.Fatal("Helm Teams diagnostic echoed an invalid private origin")
				}
			})
		}
	}
}

func TestTeamsDeploymentHelmExactJSONAndAllRoleIsolation(t *testing.T) {
	for _, allRoles := range []bool{true, false} {
		name := "all-six-roles-with-shared-origin"
		jira := []string{"https://jira.example.invalid:8443", "https://shared.example.invalid"}
		teams := []string{"https://workflow.example.invalid:8443", "https://[::1]:18444", "https://shared.example.invalid"}
		if !allRoles {
			name = "sixteen-in-each-independent-list"
			jira, teams = make([]string, 16), make([]string, 16)
			for i := range teams {
				jira[i] = fmt.Sprintf("https://jira-%d.example.invalid", 15-i)
				teams[i] = fmt.Sprintf("https://teams-%d.example.invalid", 15-i)
			}
		}
		t.Run(name, func(t *testing.T) {
			input := values()
			selectedKey(input)
			input["delivery"] = map[string]any{
				"enabled": true, "jiraAPIOrigins": jira, "teamsWorkflowOrigins": teams,
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
				input["assessment"] = map[string]any{"enabled": true, "scope": "synthetic-teams-deployment"}
			}
			roles := jiraRenderedRoles(t, input)
			expectedRoles := []string{"core", "ingestion", "reports", "delivery"}
			if allRoles {
				expectedRoles = append(expectedRoles, "collection", "assessment")
			}
			if len(roles) != len(expectedRoles) {
				t.Fatal("Teams/Jira selection changed the independent role set")
			}
			for _, role := range expectedRoles {
				mainContainer(t, roles, role)
			}
			assertTeamsOrigins(t, roles, teams)
			assertJiraOrigins(t, roles, jira)
			delivery := mainContainer(t, roles, "delivery")
			env := environment(t, delivery)
			if !reflect.DeepEqual(delivery.Command, []string{"/app/bin/delivery-worker"}) ||
				env["ASPM_SLACK_ENDPOINT"].Value != "https://slack-gateway.example.invalid/slack" ||
				env["ASPM_DELIVERY_LEASE_DURATION"].Value != "30s" {
				t.Fatal("Teams origin selection changed worker command or existing delivery settings")
			}
			requireReference(t, env, "ASPM_DATABASE_URL", "synthetic-operator-db", "database-url")
			requireReference(t, env, "ASPM_INTEGRATION_ENCRYPTION_KEY", "synthetic-integration-key", "integration-encryption-key")
			core := environment(t, mainContainer(t, roles, "core"))
			requireReference(t, core, "ASPM_INTEGRATION_ENCRYPTION_KEY", "synthetic-integration-key", "integration-encryption-key")
			for _, role := range []string{"ingestion", "reports"} {
				assertNoIntegrationKey(t, roles, role)
			}
			for key := range env {
				if strings.HasPrefix(key, "ASPM_S3") || strings.HasPrefix(key, "AWS_") ||
					key == "ASPM_BOOTSTRAP_TOKEN" || key == "ASPM_ASSETS" {
					t.Fatal("Teams delivery received unrelated storage/bootstrap/UI authority")
				}
			}
		})
	}
}
