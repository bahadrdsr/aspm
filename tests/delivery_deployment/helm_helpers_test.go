package delivery_deployment

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

type secretRef struct {
	Name, Key string
	Optional  bool
}
type envVar struct {
	Name      string
	Value     any
	ValueFrom struct {
		SecretKeyRef *secretRef `yaml:"secretKeyRef"`
	} `yaml:"valueFrom"`
}
type probe struct {
	HTTPGet struct {
		Path string
		Port any
	} `yaml:"httpGet"`
}
type container struct {
	Name           string
	Command        []string
	Env            []envVar
	EnvFrom        []any `yaml:"envFrom"`
	ReadinessProbe probe `yaml:"readinessProbe"`
	LivenessProbe  probe `yaml:"livenessProbe"`
	StartupProbe   probe `yaml:"startupProbe"`
}
type deployment struct {
	Kind     string
	Metadata struct {
		Name   string
		Labels map[string]string
	}
	Spec struct {
		Template struct {
			Spec struct {
				Containers     []container
				InitContainers []container `yaml:"initContainers"`
			}
		}
	}
}

func ownedDirectory(t *testing.T, label string) string {
	t.Helper()
	root := os.Getenv("ASPM_DELIVERY_DEPLOY_ARTIFACTS")
	if root == "" || !filepath.IsAbs(root) {
		t.Fatal("explicit project-owned artifact root is required")
	}
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		t.Fatal("cannot generate owned artifact name")
	}
	directory := filepath.Join(root, label+"-"+hex.EncodeToString(nonce[:]))
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal("cannot create owned artifact directory")
	}
	return directory
}

func source(t *testing.T, parts ...string) []byte {
	t.Helper()
	path := filepath.Join(append([]string{"..", ".."}, parts...)...)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("required production artifact is absent: %s", path)
	}
	return data
}

func values() map[string]any {
	role := func(name string) map[string]any {
		return map[string]any{
			"replicas": 1, "rawPrefix": "raw/owned-team/", "readinessKey": "raw/owned-team/readiness.txt",
			"s3Secret": map[string]any{"name": "synthetic-" + name + "-storage", "accessKeyKey": name + "-access", "secretKeyKey": name + "-secret"},
		}
	}
	core, ingestion := role("core"), role("ingestion")
	ingestion["normalizedPrefix"] = "normalized/owned-team/"
	return map[string]any{
		"existingSecret": "synthetic-operator-db", "core": core, "ingestion": ingestion,
		"reports": map[string]any{"replicas": 1}, "storage": map[string]any{"managed": true},
	}
}

type boundedOutput struct{ bytes.Buffer }

func (b *boundedOutput) Write(data []byte) (int, error) {
	if b.Len()+len(data) > 2<<20 {
		return 0, errors.New("bounded renderer output exceeded 2 MiB")
	}
	return b.Buffer.Write(data)
}

func render(t *testing.T, input map[string]any) ([]byte, string, error) {
	t.Helper()
	helm := os.Getenv("ASPM_DELIVERY_DEPLOY_HELM")
	if !filepath.IsAbs(helm) {
		t.Fatal("explicit existing Helm executable is required; fake rendering is forbidden")
	}
	directory := ownedDirectory(t, "helm")
	data, err := json.Marshal(input)
	if err != nil {
		t.Fatal("cannot encode synthetic chart values")
	}
	valueFile, config := filepath.Join(directory, "values.json"), filepath.Join(directory, "empty-kubeconfig.json")
	if os.WriteFile(valueFile, data, 0600) != nil ||
		os.WriteFile(config, []byte(`{"apiVersion":"v1","kind":"Config","clusters":[],"contexts":[],"users":[],"current-context":""}`), 0600) != nil {
		t.Fatal("cannot write owned client-render inputs")
	}
	chart, err := filepath.Abs(filepath.Join("..", "..", "deploy", "helm", "aspm"))
	if err != nil {
		t.Fatal("cannot resolve actual chart")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, helm, "template", "delivery-probe", chart,
		"--namespace", "synthetic-delivery", "--kubeconfig", config, "--dry-run=client", "-f", valueFile)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(name, "HELM_") && !strings.HasPrefix(name, "KUBE") {
			command.Env = append(command.Env, entry)
		}
	}
	command.Env = append(command.Env, "KUBECONFIG="+config, "HELM_CACHE_HOME="+directory,
		"HELM_CONFIG_HOME="+directory, "HELM_DATA_HOME="+directory, "HELM_NO_PLUGINS=1",
		"HELM_PLUGINS="+filepath.Join(directory, "no-plugins"))
	var stdout, stderr boundedOutput
	command.Stdout, command.Stderr = &stdout, &stderr
	err = command.Run()
	if ctx.Err() != nil {
		t.Fatal("actual Helm client render exceeded its bound")
	}
	if os.WriteFile(filepath.Join(directory, "render.yaml"), stdout.Bytes(), 0600) != nil ||
		os.WriteFile(filepath.Join(directory, "stderr.txt"), stderr.Bytes(), 0600) != nil {
		t.Fatal("cannot preserve actual Helm render evidence")
	}
	t.Log("actual Helm render:", directory)
	return stdout.Bytes(), stderr.String(), err
}

func parseDeployments(t *testing.T, data []byte) map[string]deployment {
	t.Helper()
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	result := map[string]deployment{}
	for count := 0; ; count++ {
		var item deployment
		err := decoder.Decode(&item)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || count >= 32 {
			t.Fatal("actual Helm output is invalid YAML or exceeds document bounds")
		}
		if item.Kind != "Deployment" {
			continue
		}
		role := item.Metadata.Labels["app.kubernetes.io/component"]
		switch role {
		case "core", "ingestion", "reports", "delivery":
			if _, duplicate := result[role]; duplicate {
				t.Fatal("duplicate actual rendered role")
			}
			result[role] = item
		}
	}
	for _, role := range []string{"core", "ingestion", "reports"} {
		if _, ok := result[role]; !ok {
			t.Fatalf("actual render dropped original %s role", role)
		}
	}
	return result
}

func successfulRender(t *testing.T, input map[string]any) map[string]deployment {
	t.Helper()
	data, diagnostic, err := render(t, input)
	if err != nil {
		t.Fatalf("valid synthetic chart selections failed real render (%T): %s", err, diagnostic)
	}
	return parseDeployments(t, data)
}

func mainContainer(t *testing.T, roles map[string]deployment, role string) container {
	t.Helper()
	pod, present := roles[role]
	if !present {
		t.Fatalf("actual Helm output is missing required %s Deployment", role)
	}
	for _, item := range pod.Spec.Template.Spec.Containers {
		if item.Name == role {
			return item
		}
	}
	t.Fatalf("rendered %s Deployment has no matching main container", role)
	return container{}
}

func environment(t *testing.T, item container) map[string]envVar {
	t.Helper()
	if len(item.EnvFrom) != 0 {
		t.Fatal("bulk envFrom may not import operator/storage/integration secrets")
	}
	result := map[string]envVar{}
	for _, variable := range item.Env {
		if _, duplicate := result[variable.Name]; duplicate {
			t.Fatalf("duplicate environment input %s", variable.Name)
		}
		result[variable.Name] = variable
	}
	return result
}

func requireReference(t *testing.T, variables map[string]envVar, name, secretName, key string) {
	t.Helper()
	value, present := variables[name]
	ref := value.ValueFrom.SecretKeyRef
	if !present || value.Value != nil || ref == nil || ref.Name != secretName || ref.Key != key || ref.Optional {
		t.Fatalf("%s must use only the exact required Secret reference %s/%s", name, secretName, key)
	}
}

func assertNoIntegrationKey(t *testing.T, roles map[string]deployment, role string) {
	t.Helper()
	pod := roles[role]
	for _, item := range append(pod.Spec.Template.Spec.InitContainers, pod.Spec.Template.Spec.Containers...) {
		for name, value := range environment(t, item) {
			if name == "ASPM_INTEGRATION_ENCRYPTION_KEY" ||
				value.ValueFrom.SecretKeyRef != nil && value.ValueFrom.SecretKeyRef.Name == "synthetic-integration-key" {
				t.Errorf("%s must not receive the integration encryption key", role)
			}
		}
	}
}
