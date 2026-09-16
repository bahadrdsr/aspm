package deployment_roles

import (
	"bytes"
	"errors"
	"io"
	"os/exec"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

const prepareReadinessEnv = "ASPM_S3_PREPARE_READINESS"

func preparationValues(value any, supplied bool) map[string]any {
	input := values()
	if supplied {
		input["core"].(map[string]any)["prepareReadiness"] = value
	}
	return input
}

func readinessNode(node *yaml.Node, key string) *yaml.Node {
	if node.Kind == yaml.DocumentNode && len(node.Content) == 1 {
		node = node.Content[0]
	}
	for index := 0; node.Kind == yaml.MappingNode && index+1 < len(node.Content); index += 2 {
		if node.Content[index].Value == key {
			return node.Content[index+1]
		}
	}
	return &yaml.Node{}
}

func preparationScalarTag(t *testing.T, rendered []byte) string {
	t.Helper()
	decoder := yaml.NewDecoder(bytes.NewReader(rendered))
	for {
		var document yaml.Node
		err := decoder.Decode(&document)
		if errors.Is(err, io.EOF) {
			return ""
		}
		if err != nil {
			t.Fatalf("cannot inspect real rendered YAML scalar type (%T)", err)
		}
		labels := readinessNode(readinessNode(&document, "metadata"), "labels")
		if readinessNode(&document, "kind").Value != "Deployment" || readinessNode(labels, "app.kubernetes.io/component").Value != "core" {
			continue
		}
		pod := readinessNode(readinessNode(readinessNode(&document, "spec"), "template"), "spec")
		for _, container := range readinessNode(pod, "containers").Content {
			if readinessNode(container, "name").Value != "core" {
				continue
			}
			for _, variable := range readinessNode(container, "env").Content {
				if readinessNode(variable, "name").Value == prepareReadinessEnv {
					return readinessNode(variable, "value").ShortTag()
				}
			}
		}
	}
}

func TestDeploymentHelmPrepareReadinessExplicitCoreOptIn(t *testing.T) {
	for _, selection := range []struct {
		name     string
		value    bool
		supplied bool
		want     string
	}{
		{"default-off", false, false, "false"},
		{"explicit-false", false, true, "false"},
		{"explicit-true", true, true, "true"},
	} {
		t.Run(selection.name, func(t *testing.T) {
			output, diagnostic, err := render(t, preparationValues(selection.value, selection.supplied))
			if err != nil {
				t.Fatalf("valid complete role selection failed real rendering: %s (%T)", diagnostic, err)
			}
			roles := deployments(t, output)
			for _, role := range []string{"core", "ingestion", "reports"} {
				env := environment(t, roles[role], role)
				if role == "core" {
					flag, present := env[prepareReadinessEnv]
					if !present || flag.Value != selection.want || flag.ValueFrom.SecretKeyRef != nil {
						t.Errorf("core.prepareReadiness %s must render core %s as literal %q; present=%t value=%q",
							selection.name, prepareReadinessEnv, selection.want, present, flag.Value)
					}
					if present && preparationScalarTag(t, output) != "!!str" {
						t.Error("Kubernetes env.value must be a YAML string, not a boolean scalar")
					}
				} else {
					pod := roles[role].Spec.Template.Spec
					for _, container := range append(pod.InitContainers, pod.Containers...) {
						for _, variable := range container.Env {
							if variable.Name == prepareReadinessEnv {
								t.Errorf("%s must not receive the core-only readiness preparation flag", role)
							}
						}
					}
				}
			}
		})
	}
}

func TestDeploymentHelmPrepareReadinessRejectsNonBoolean(t *testing.T) {
	output, diagnostic, err := render(t, preparationValues(true, true))
	if err != nil {
		t.Fatalf("valid real-render control failed: %s (%T)", diagnostic, err)
	}
	deployments(t, output)
	for _, selection := range []struct {
		name  string
		value any
	}{
		{"string-true", "true"},
		{"string-false", "false"},
		{"integer-one", 1},
	} {
		t.Run(selection.name, func(t *testing.T) {
			_, diagnostic, err := render(t, preparationValues(selection.value, true))
			var exit *exec.ExitError
			if !errors.As(err, &exit) || !strings.Contains(diagnostic, "core.prepareReadiness") {
				t.Errorf("explicit non-boolean %s must fail real rendering with core.prepareReadiness diagnostic; errorType=%T", selection.name, err)
			}
		})
	}
}

func TestDeploymentShippingUnitsDoNotOverrideReadinessOptIn(t *testing.T) {
	for _, role := range []string{"core", "ingestion", "reports"} {
		t.Run(role, func(t *testing.T) {
			sections := unit(t, shippedUnit(t, role))
			roleEnvironment(t, sections, role, "/etc/aspm/"+role+".env")
			for _, section := range []string{"Container", "Service"} {
				for _, assignment := range sections[section]["Environment"] {
					name, _, _ := strings.Cut(assignment, "=")
					if name == prepareReadinessEnv {
						t.Errorf("shipping %s unit must not impose inline %s.%s; opt-in belongs to protected core.env only", role, section, name)
					}
				}
			}
		})
	}
}
