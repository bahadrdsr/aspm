package collection_deployment

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
	"reflect"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

type object = map[string]any

type secretRef struct {
	Name, Key string
	Optional  bool
	Other     object `yaml:",inline"`
}
type envSource struct {
	Secret *secretRef `yaml:"secretKeyRef"`
	Other  object     `yaml:",inline"`
}
type envVar struct {
	Name      string
	Value     any
	ValueFrom *envSource `yaml:"valueFrom"`
	Other     object     `yaml:",inline"`
}
type probe struct {
	HTTPGet struct {
		Path string
		Port any
	} `yaml:"httpGet"`
}
type container struct {
	Name    string
	Command []string
	Args    []string
	Env     []envVar
	EnvFrom []any `yaml:"envFrom"`
	Ports   []struct {
		Name          string
		ContainerPort int `yaml:"containerPort"`
	}
	Resources      object
	StartupProbe   probe `yaml:"startupProbe"`
	ReadinessProbe probe `yaml:"readinessProbe"`
	LivenessProbe  probe `yaml:"livenessProbe"`
}
type deployment struct {
	Kind     string
	Metadata struct {
		Name, Namespace string
		Labels          map[string]string
	}
	Spec struct {
		Replicas *int
		Selector struct {
			MatchLabels map[string]string `yaml:"matchLabels"`
		}
		Template struct {
			Metadata struct{ Labels map[string]string }
			Spec     struct {
				Containers     []container
				InitContainers []container `yaml:"initContainers"`
			}
		}
	}
}
type rendered struct {
	Roles     map[string]deployment
	Documents map[string]object
}

var renderCount atomic.Int32

func source(t *testing.T, parts ...string) []byte {
	t.Helper()
	file := filepath.Join(append([]string{"..", ".."}, parts...)...)
	data, err := os.ReadFile(file)
	if err != nil {
		t.Fatalf("required production artifact is absent: %s", file)
	}
	return data
}

func ownedDirectory(t *testing.T, label string) string {
	t.Helper()
	root := os.Getenv("ASPM_COLLECTION_DEPLOY_ARTIFACTS")
	if !filepath.IsAbs(root) {
		t.Fatal("explicit project-owned artifact directory required")
	}
	var randomName [12]byte
	if _, err := rand.Read(randomName[:]); err != nil {
		t.Fatal("cannot generate task-owned artifact name")
	}
	directory := filepath.Join(root, label+"-"+hex.EncodeToString(randomName[:]))
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal("cannot create task-owned output directory")
	}
	return directory
}

func writeArtifact(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal("cannot persist owned artifact evidence")
	}
}

func legacyValues(delivery bool) object {
	rawRole := func(name string) object {
		return object{
			"replicas": 1, "rawPrefix": "raw/legacy-team/", "readinessKey": "raw/legacy-team/ready.txt",
			"s3Secret": object{"name": "synthetic-" + name + "-raw", "accessKeyKey": name + "-raw-access", "secretKeyKey": name + "-raw-secret"},
		}
	}
	core, ingestion := rawRole("core"), rawRole("ingestion")
	ingestion["normalizedPrefix"] = "normalized/legacy-team/"
	input := object{
		"existingSecret": "synthetic-operator-db", "publicOrigin": "https://app.synthetic.invalid",
		"core": core, "ingestion": ingestion, "reports": object{"replicas": 1},
		"storage": object{"managed": true, "bucket": "legacy-raw-bucket", "prefix": "legacy-operator-not-collection/",
			"region": "legacy-region", "policySecret": object{"name": "synthetic-managed-storage-policy", "key": "s3.json"}},
	}
	if delivery {
		input["delivery"] = object{"enabled": true}
		selectIntegrationKey(input)
	}
	return input
}

func selectIntegrationKey(input object) {
	input["integrationKeySecret"] = object{"name": "synthetic-integration-key", "key": "encryption-key"}
}

func collectionValues(enabled, delivery bool) object {
	input := legacyValues(delivery)
	input["collection"] = object{
		"enabled": enabled,
		"storage": object{"endpoint": "https://evidence.synthetic.invalid", "bucket": "selected-source-evidence",
			"prefix": "collections/owned-team/", "region": "selected-region"},
	}
	input["core"].(object)["collectionS3Secret"] = object{
		"name": "synthetic-collection-reader", "accessKeyKey": "reader-access", "secretKeyKey": "reader-secret",
	}
	if enabled {
		input["collection"].(object)["s3Secret"] = object{
			"name": "synthetic-collection-publisher", "accessKeyKey": "publisher-access", "secretKeyKey": "publisher-secret",
		}
		selectIntegrationKey(input)
	}
	return input
}

type boundedOutput struct{ bytes.Buffer }

func (b *boundedOutput) Write(data []byte) (int, error) {
	if b.Len()+len(data) > 2<<20 {
		return 0, errors.New("actual renderer exceeded the 2 MiB output bound")
	}
	return b.Buffer.Write(data)
}

func render(t *testing.T, input object) ([]byte, string, error) {
	t.Helper()
	if renderCount.Add(1) > 40 {
		t.Fatal("bounded package-wide Helm render limit exceeded")
	}
	helm := os.Getenv("ASPM_COLLECTION_DEPLOY_HELM")
	if !filepath.IsAbs(helm) {
		t.Fatal("explicit existing Helm executable required; no simulated rendering")
	}
	directory := ownedDirectory(t, "helm")
	values, err := json.Marshal(input)
	if err != nil {
		t.Fatal("cannot encode nonsecret selected values")
	}
	valuesFile := filepath.Join(directory, "values.json")
	kubeconfig := filepath.Join(directory, "empty-kubeconfig.json")
	writeArtifact(t, valuesFile, values)
	writeArtifact(t, kubeconfig, []byte(`{"apiVersion":"v1","kind":"Config","clusters":[],"contexts":[],"users":[],"current-context":""}`))
	chart, err := filepath.Abs(filepath.Join("..", "..", "deploy", "helm", "aspm"))
	if err != nil {
		t.Fatal("cannot resolve the actual current chart")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	args := []string{"template", "collection-probe", chart, "--namespace", "synthetic-collection",
		"--kubeconfig", kubeconfig, "--dry-run=client", "-f", valuesFile}
	command := exec.CommandContext(ctx, helm, args...)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if !strings.HasPrefix(name, "HELM_") && !strings.HasPrefix(name, "KUBE") {
			command.Env = append(command.Env, entry)
		}
	}
	command.Env = append(command.Env, "KUBECONFIG="+kubeconfig, "HELM_CACHE_HOME="+directory,
		"HELM_CONFIG_HOME="+directory, "HELM_DATA_HOME="+directory, "HELM_NO_PLUGINS=1",
		"HELM_PLUGINS="+filepath.Join(directory, "no-plugins"))
	var stdout, stderr boundedOutput
	command.Stdout, command.Stderr = &stdout, &stderr
	runErr := command.Run()
	if ctx.Err() != nil {
		t.Fatal("real Helm client-only render exceeded its deadline")
	}
	writeArtifact(t, filepath.Join(directory, "render.yaml"), stdout.Bytes())
	writeArtifact(t, filepath.Join(directory, "stderr.txt"), stderr.Bytes())
	receipt, err := json.Marshal(object{"test": t.Name(), "arguments": args, "success": runErr == nil})
	if err != nil {
		t.Fatal("cannot record actual renderer invocation")
	}
	writeArtifact(t, filepath.Join(directory, "render-receipt.json"), receipt)
	t.Log("actual client-only Helm render:", directory)
	return stdout.Bytes(), stderr.String(), runErr
}

func parseRender(t *testing.T, data []byte) rendered {
	t.Helper()
	result := rendered{Roles: map[string]deployment{}, Documents: map[string]object{}}
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	for n := 0; ; n++ {
		var node yaml.Node
		err := decoder.Decode(&node)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil || n >= 40 {
			t.Fatal("actual chart output is invalid YAML or exceeds its document bound")
		}
		var raw object
		if err := node.Decode(&raw); err != nil {
			t.Fatal("actual YAML document cannot be parsed structurally")
		}
		if len(raw) == 0 {
			continue
		}
		var item deployment
		if err := node.Decode(&item); err != nil || item.Kind == "" || item.Metadata.Name == "" {
			t.Fatal("actual artifact lacks an identifiable kind/name")
		}
		key := item.Kind + "/" + item.Metadata.Name
		if _, duplicate := result.Documents[key]; duplicate {
			t.Fatal("chart rendered a duplicate resource identity")
		}
		result.Documents[key] = raw
		if item.Kind != "Deployment" {
			continue
		}
		role := item.Metadata.Labels["app.kubernetes.io/component"]
		switch role {
		case "core", "ingestion", "reports", "delivery", "collection":
		default:
			t.Fatalf("unexpected or unlabelled application Deployment: %s", item.Metadata.Name)
		}
		if _, duplicate := result.Roles[role]; duplicate {
			t.Fatalf("duplicate %s Deployment", role)
		}
		result.Roles[role] = item
	}
	for _, role := range []string{"core", "ingestion", "reports"} {
		if _, present := result.Roles[role]; !present {
			t.Fatalf("render dropped existing %s role", role)
		}
	}
	return result
}

func successfulRender(t *testing.T, input object) rendered {
	t.Helper()
	data, diagnostic, err := render(t, input)
	if err != nil {
		t.Fatalf("valid collection artifact values failed actual Helm rendering (%T): %s", err, diagnostic)
	}
	return parseRender(t, data)
}

func mainContainer(t *testing.T, output rendered, role string) container {
	t.Helper()
	pod, present := output.Roles[role]
	if !present {
		t.Fatalf("actual chart output is missing the required %s Deployment", role)
	}
	var found []container
	for _, item := range pod.Spec.Template.Spec.Containers {
		if item.Name == role {
			found = append(found, item)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%s must have one matching actual main container", role)
	}
	return found[0]
}

func environment(t *testing.T, c container) map[string]envVar {
	t.Helper()
	if len(c.EnvFrom) != 0 {
		t.Fatalf("%s uses bulk envFrom instead of explicit selected authorities", c.Name)
	}
	result := map[string]envVar{}
	for _, item := range c.Env {
		if _, duplicate := result[item.Name]; duplicate || item.Name == "" || len(item.Other) != 0 {
			t.Fatal("duplicate, empty or unsupported environment entry")
		}
		if item.ValueFrom != nil && (len(item.ValueFrom.Other) != 0 || item.ValueFrom.Secret == nil ||
			len(item.ValueFrom.Secret.Other) != 0) {
			t.Fatal("environment imports an undeclared non-Secret authority")
		}
		result[item.Name] = item
	}
	return result
}

func requireReference(t *testing.T, env map[string]envVar, name, secret, key string) {
	t.Helper()
	item, ok := env[name]
	if !ok || item.Value != nil || item.ValueFrom == nil || item.ValueFrom.Secret == nil {
		t.Fatalf("%s must exist as the exact required SecretKeyRef, not a literal/fallback", name)
	}
	ref := item.ValueFrom.Secret
	if ref.Name != secret || ref.Key != key || ref.Optional {
		t.Fatalf("%s does not use the selected nonoptional Secret reference %s/%s", name, secret, key)
	}
}

func requireString(t *testing.T, env map[string]envVar, name, want string) {
	t.Helper()
	item, exists := env[name]
	value, isString := item.Value.(string)
	if !exists || !isString || value != want || item.ValueFrom != nil {
		t.Fatalf("%s must preserve the exact selected YAML string value, not a coercion or reference", name)
	}
}

func collectionVariables() []string {
	return []string{"ASPM_COLLECTION_S3_ENDPOINT", "ASPM_COLLECTION_S3_BUCKET", "ASPM_COLLECTION_S3_PREFIX",
		"ASPM_COLLECTION_S3_REGION", "ASPM_COLLECTION_S3_ACCESS_KEY", "ASPM_COLLECTION_S3_SECRET_KEY"}
}

func requireStorage(t *testing.T, env map[string]envVar, input object, selector object) {
	t.Helper()
	storage := input["collection"].(object)["storage"].(object)
	for _, entry := range []struct{ field, name string }{
		{"endpoint", "ASPM_COLLECTION_S3_ENDPOINT"}, {"bucket", "ASPM_COLLECTION_S3_BUCKET"},
		{"prefix", "ASPM_COLLECTION_S3_PREFIX"}, {"region", "ASPM_COLLECTION_S3_REGION"},
	} {
		requireString(t, env, entry.name, storage[entry.field].(string))
	}
	requireReference(t, env, "ASPM_COLLECTION_S3_ACCESS_KEY", selector["name"].(string), selector["accessKeyKey"].(string))
	requireReference(t, env, "ASPM_COLLECTION_S3_SECRET_KEY", selector["name"].(string), selector["secretKeyKey"].(string))
	var names []string
	for name := range env {
		if strings.HasPrefix(name, "ASPM_COLLECTION_S3_") {
			names = append(names, name)
		}
	}
	want := collectionVariables()
	sort.Strings(names)
	sort.Strings(want)
	if !reflect.DeepEqual(names, want) {
		t.Fatal("collection storage must contain exactly the six explicit variables")
	}
}

func noCollectionAuthority(t *testing.T, output rendered, roles ...string) {
	t.Helper()
	for _, role := range roles {
		pod, present := output.Roles[role]
		if !present {
			continue
		}
		for _, c := range append(pod.Spec.Template.Spec.InitContainers, pod.Spec.Template.Spec.Containers...) {
			for name, item := range environment(t, c) {
				if strings.HasPrefix(name, "ASPM_COLLECTION_") || name == "ASPM_GITHUB_ENDPOINT" {
					t.Fatalf("unrelated %s container %s received %s", role, c.Name, name)
				}
				if item.ValueFrom != nil && strings.HasPrefix(item.ValueFrom.Secret.Name, "synthetic-collection-") {
					t.Fatalf("unrelated %s container %s received a collection Secret reference", role, c.Name)
				}
			}
		}
	}
}

func preserveLegacy(t *testing.T, baseline, output rendered) {
	t.Helper()
	coreName := baseline.Roles["core"].Metadata.Name
	for key, original := range baseline.Documents {
		if key == "Deployment/"+coreName {
			continue
		}
		if current, present := output.Documents[key]; !present || !reflect.DeepEqual(current, original) {
			t.Fatalf("collection configuration changed existing workload, service or managed-storage policy artifact %s", key)
		}
	}
	for key, doc := range output.Documents {
		if _, existed := baseline.Documents[key]; !existed && (doc["kind"] == "Secret" || doc["kind"] == "ConfigMap") {
			t.Fatal("collection rendering must not generate credentials or managed-storage policy")
		}
	}
	oldCore := environment(t, mainContainer(t, baseline, "core"))
	currentCore := environment(t, mainContainer(t, output, "core"))
	for _, env := range []map[string]envVar{oldCore, currentCore} {
		for _, name := range collectionVariables() {
			delete(env, name)
		}
		delete(env, "ASPM_INTEGRATION_ENCRYPTION_KEY")
	}
	if !reflect.DeepEqual(oldCore, currentCore) ||
		!reflect.DeepEqual(mainContainer(t, output, "core").Command, []string{"/app/bin/core-api"}) ||
		len(baseline.Roles["core"].Spec.Template.Spec.Containers) != len(output.Roles["core"].Spec.Template.Spec.Containers) ||
		!reflect.DeepEqual(baseline.Roles["core"].Spec.Template.Spec.InitContainers, output.Roles["core"].Spec.Template.Spec.InitContainers) {
		t.Fatal("collection configuration changed existing core raw-storage/runtime behavior")
	}
}
