package deployment_roles

import (
	"errors"
	"os/exec"
	"strings"
	"testing"
)

func TestDeploymentHelmDistinctRuntimeSecretsAndScopes(t *testing.T) {
	roles := rendered(t)
	for _, role := range []string{"core", "ingestion"} {
		env := environment(t, roles[role], role)
		for _, item := range []struct{ name, key string }{
			{"ASPM_S3_ACCESS_KEY", role + "-s3-access-key"}, {"ASPM_S3_SECRET_KEY", role + "-s3-secret-key"},
		} {
			value, present := env[item.name]
			ref := value.ValueFrom.SecretKeyRef
			if !present || value.Value != "" || ref == nil || ref.Name != "synthetic-"+role+"-storage" || ref.Key != item.key || ref.Optional {
				t.Errorf("%s %s must require selected secret synthetic-%s-storage/%s, without a literal/common/optional fallback", role, item.name, role, item.key)
			}
		}
		for name, want := range map[string]string{"ASPM_S3_PREFIX": "raw/team-a/", "ASPM_S3_READINESS_KEY": "raw/team-a/readiness.txt"} {
			if env[name].Value != want {
				t.Errorf("%s %s=%q, want explicit %q", role, name, env[name].Value, want)
			}
		}
		if role == "ingestion" && env["ASPM_S3_NORMALIZED_PREFIX"].Value != "normalized/team-a/" {
			t.Error("ingestion must declare its separate normalized/team-a/ output prefix")
		}
	}
}

func TestDeploymentHelmReportsHaveNoStorageOrBootstrapEnv(t *testing.T) {
	pod := rendered(t)["reports"]
	env := environment(t, pod, "reports")
	database := env["ASPM_DATABASE_URL"].ValueFrom.SecretKeyRef
	if database == nil || database.Key != "database-url" || database.Optional {
		t.Error("reports must retain required database-only credential input")
	}
	for _, item := range append(pod.Spec.Template.Spec.InitContainers, pod.Spec.Template.Spec.Containers...) {
		for _, variable := range item.Env {
			if strings.HasPrefix(variable.Name, "ASPM_S3_") || strings.HasPrefix(variable.Name, "AWS_") || variable.Name == "ASPM_BOOTSTRAP_TOKEN" {
				t.Errorf("report deployment must not receive %s", variable.Name)
			}
			if ref := variable.ValueFrom.SecretKeyRef; ref != nil && ref.Key != "database-url" {
				t.Errorf("report runtime references non-database secret key %s", ref.Key)
			}
		}
	}
}

func TestDeploymentHelmMissingSelectedRoleSecretsCannotFallback(t *testing.T) {
	output, diagnostic, err := render(t, values())
	if err != nil {
		t.Fatalf("positive real-render control failed: %s (%T)", diagnostic, err)
	}
	deployments(t, output)
	for _, role := range []string{"core", "ingestion"} {
		for _, field := range []string{"name", "accessKeyKey", "secretKeyKey"} {
			t.Run(role+"-"+field, func(t *testing.T) {
				input := values()
				selection := input[role].(map[string]any)["s3Secret"].(map[string]any)
				selection[field] = ""
				_, message, renderErr := render(t, input)
				var exit *exec.ExitError
				required := role + ".s3Secret." + field
				if !errors.As(renderErr, &exit) || !strings.Contains(message, required) {
					t.Errorf("missing %s must fail real rendering explicitly even with existingSecret/operator configuration present; got error %T", required, renderErr)
				}
			})
		}
	}
}
