package deployment_roles

import (
	"bytes"
	"context"
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
	Name     string `yaml:"name"`
	Key      string `yaml:"key"`
	Optional bool   `yaml:"optional"`
}

type envVar struct {
	Name      string `yaml:"name"`
	Value     string `yaml:"value"`
	ValueFrom struct {
		SecretKeyRef *secretRef `yaml:"secretKeyRef"`
	} `yaml:"valueFrom"`
}

type container struct {
	Name    string   `yaml:"name"`
	Env     []envVar `yaml:"env"`
	EnvFrom []any    `yaml:"envFrom"`
}

type document struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Labels map[string]string `yaml:"labels"`
	} `yaml:"metadata"`
	Spec struct {
		Template struct {
			Spec struct {
				Containers     []container `yaml:"containers"`
				InitContainers []container `yaml:"initContainers"`
			} `yaml:"spec"`
		} `yaml:"template"`
	} `yaml:"spec"`
}

func values() map[string]any {
	role := func(name string) map[string]any {
		return map[string]any{
			"replicas": 1, "rawPrefix": "raw/team-a/", "readinessKey": "raw/team-a/readiness.txt",
			"s3Secret": map[string]any{"name": "synthetic-" + name + "-storage", "accessKeyKey": name + "-s3-access-key", "secretKeyKey": name + "-s3-secret-key"},
		}
	}
	core, ingestion := role("core"), role("ingestion")
	ingestion["normalizedPrefix"] = "normalized/team-a/"
	return map[string]any{
		"existingSecret": "synthetic-operator-control-plane",
		"core":           core, "ingestion": ingestion, "reports": map[string]any{"replicas": 1},
		"storage": map[string]any{"managed": true, "prefix": "operator-only-not-runtime/"},
	}
}

type limitedBuffer struct{ buffer bytes.Buffer }

func (b *limitedBuffer) Write(data []byte) (int, error) {
	if b.buffer.Len()+len(data) > 2<<20 {
		return 0, errors.New("Helm output exceeded the 2 MiB fixture bound")
	}
	return b.buffer.Write(data)
}

func (b *limitedBuffer) Bytes() []byte  { return b.buffer.Bytes() }
func (b *limitedBuffer) String() string { return b.buffer.String() }

func render(t *testing.T, overrides map[string]any) ([]byte, string, error) {
	t.Helper()
	helm := os.Getenv("ASPM_TEST_HELM")
	if helm == "" || !filepath.IsAbs(helm) {
		t.Fatal("explicit absolute ASPM_TEST_HELM is required; never substitute fake rendering")
	}
	info, err := os.Stat(helm)
	if err != nil || !info.Mode().IsRegular() {
		t.Fatal("explicit Helm executable is absent or not a regular file")
	}
	work := t.TempDir()
	valueFile, kubeconfig := filepath.Join(work, "values.json"), filepath.Join(work, "kubeconfig.json")
	encoded, err := json.Marshal(overrides)
	if err != nil {
		t.Fatal("cannot encode synthetic Helm value references")
	}
	if err = os.WriteFile(valueFile, encoded, 0600); err != nil {
		t.Fatal("cannot write owned Helm values")
	}
	if err = os.WriteFile(kubeconfig, []byte(`{"apiVersion":"v1","kind":"Config","clusters":[],"contexts":[],"users":[],"current-context":""}`), 0600); err != nil {
		t.Fatal("cannot write explicit empty render-only kubeconfig")
	}
	chart, err := filepath.Abs(filepath.Join("..", "..", "deploy", "helm", "aspm"))
	if err != nil {
		t.Fatal("cannot resolve the shipped chart")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, helm, "template", "role-probe", chart,
		"--namespace", "synthetic-role-namespace", "--kubeconfig", kubeconfig, "--dry-run=client", "-f", valueFile)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(name, "HELM_") && !strings.HasPrefix(name, "KUBE") {
			command.Env = append(command.Env, entry)
		}
	}
	command.Env = append(command.Env, "KUBECONFIG="+kubeconfig, "HELM_CACHE_HOME="+work, "HELM_CONFIG_HOME="+work,
		"HELM_DATA_HOME="+work, "HELM_PLUGINS="+filepath.Join(work, "no-plugins"), "HELM_NO_PLUGINS=1")
	var stdout, stderr limitedBuffer
	command.Stdout, command.Stderr = &stdout, &stderr
	runErr := command.Run()
	if ctx.Err() != nil {
		t.Fatal("real Helm rendering exceeded its bounded deadline")
	}
	if directory := os.Getenv("ASPM_TEST_RENDER_DIR"); directory != "" {
		name := strings.NewReplacer("/", "_", "\\", "_").Replace(t.Name())
		if err := os.MkdirAll(directory, 0700); err != nil {
			t.Fatal("cannot create render evidence directory")
		}
		for suffix, data := range map[string][]byte{".yaml": stdout.Bytes(), ".stderr.txt": stderr.Bytes()} {
			if err := os.WriteFile(filepath.Join(directory, name+suffix), data, 0600); err != nil {
				t.Fatal("cannot preserve actual render evidence")
			}
		}
	}
	return stdout.Bytes(), stderr.String(), runErr
}

func deployments(t *testing.T, data []byte) map[string]document {
	t.Helper()
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	result := map[string]document{}
	for n := 0; ; n++ {
		var item document
		err := decoder.Decode(&item)
		if errors.Is(err, io.EOF) {
			break
		}
		if n >= 32 {
			t.Fatal("render exceeded its bounded document count")
		}
		if err != nil {
			t.Fatalf("real Helm output is not valid YAML (%T)", err)
		}
		if item.Kind != "Deployment" {
			continue
		}
		role := item.Metadata.Labels["app.kubernetes.io/component"]
		if role != "core" && role != "ingestion" && role != "reports" {
			continue
		}
		if _, duplicate := result[role]; duplicate {
			t.Fatalf("duplicate rendered %s deployment", role)
		}
		result[role] = item
	}
	if len(result) != 3 {
		t.Fatalf("real chart must render all three selected roles; found %d", len(result))
	}
	return result
}

func rendered(t *testing.T) map[string]document {
	t.Helper()
	output, diagnostic, err := render(t, values())
	if err != nil {
		t.Fatalf("valid selected-role references failed real Helm rendering: %s (%T)", diagnostic, err)
	}
	return deployments(t, output)
}

func environment(t *testing.T, pod document, role string) map[string]envVar {
	t.Helper()
	var selected *container
	for _, item := range append(pod.Spec.Template.Spec.InitContainers, pod.Spec.Template.Spec.Containers...) {
		if len(item.EnvFrom) != 0 {
			t.Errorf("%s uses bulk envFrom; operator/common credentials must not leak into runtime roles", role)
		}
		for _, variable := range item.Env {
			if role != "reports" && (variable.Name == "ASPM_S3_ACCESS_KEY" || variable.Name == "ASPM_S3_SECRET_KEY") {
				key := role + "-s3-access-key"
				if variable.Name == "ASPM_S3_SECRET_KEY" {
					key = role + "-s3-secret-key"
				}
				ref := variable.ValueFrom.SecretKeyRef
				if ref == nil || ref.Name != "synthetic-"+role+"-storage" || ref.Key != key || ref.Optional || variable.Value != "" {
					t.Errorf("%s container %s receives an unselected credential reference for %s", role, item.Name, variable.Name)
				}
			}
			if ref := variable.ValueFrom.SecretKeyRef; ref != nil {
				switch ref.Key {
				case "s3-access-key", "s3-secret-key", "s3.json", "operator-s3-access-key", "operator-s3-secret-key":
					t.Errorf("%s runtime env %s references legacy/operator key %s", role, variable.Name, ref.Key)
				}
			}
		}
		if item.Name == role {
			copy := item
			selected = &copy
		}
	}
	if selected == nil {
		t.Fatalf("rendered %s deployment has no matching main container", role)
	}
	result := map[string]envVar{}
	for _, item := range selected.Env {
		if _, duplicate := result[item.Name]; duplicate {
			t.Errorf("%s has duplicate env %s", role, item.Name)
		}
		result[item.Name] = item
	}
	return result
}
