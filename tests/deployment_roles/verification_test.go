package deployment_roles

import (
	"bytes"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func m13RootFile(t *testing.T, parts ...string) []byte {
	t.Helper()
	path := filepath.Join(append([]string{"..", ".."}, parts...)...)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("required M13 source artifact %s is unavailable", filepath.Join(parts...))
	}
	return data
}

func TestVerificationDeploymentCommandAndImageInventory(t *testing.T) {
	t.Run("standalone-common-runtime-command", func(t *testing.T) {
		path := filepath.Join("..", "..", "cmd", "verification-worker", "main.go")
		source := m13RootFile(t, "cmd", "verification-worker", "main.go")
		file, err := parser.ParseFile(token.NewFileSet(), path, source, 0)
		if err != nil || file.Name.Name != "main" {
			t.Fatal("verification-worker must be a real compilable main package")
		}
		environment, run := 0, 0
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			pkg, ok := selector.X.(*ast.Ident)
			if !ok || pkg.Name != "service" {
				return true
			}
			literal := func(index int) string {
				if index >= len(call.Args) {
					return ""
				}
				value, ok := call.Args[index].(*ast.BasicLit)
				if !ok || value.Kind != token.STRING {
					return ""
				}
				decoded, _ := strconv.Unquote(value.Value)
				return decoded
			}
			switch selector.Sel.Name {
			case "Environment":
				if literal(0) == "verification" {
					environment++
				}
			case "Run":
				if literal(1) == "verification" {
					run++
				}
			}
			return true
		})
		if environment != 1 || run != 1 {
			t.Fatal("verification-worker must call the common service Environment/Run path exactly once for role verification")
		}
		text := string(source)
		if !strings.Contains(text, "signal.NotifyContext") ||
			!strings.Contains(text, "context.Canceled") ||
			!strings.Contains(text, "verification worker stopped") {
			t.Fatal("verification-worker must retain bounded signal cancellation and ordinary command diagnostics")
		}
	})

	t.Run("source-container-and-runtime-copy", func(t *testing.T) {
		source := string(m13RootFile(t, "Containerfile"))
		folded := strings.ReplaceAll(strings.ReplaceAll(source, "\\\r\n", " "), "\\\n", " ")
		commands := []string{"core-api", "ingestion", "retention-worker", "report-worker", "delivery-worker",
			"collection-worker", "assessment-worker", "verification-worker", "aspmctl"}
		for _, command := range commands {
			pattern := `go build[^&\r\n]*-o\s+/out/` + regexp.QuoteMeta(command) +
				`\s+\./cmd/` + regexp.QuoteMeta(command) + `(?:\s|$)`
			if len(regexp.MustCompile(pattern).FindAllString(folded, -1)) != 1 {
				t.Errorf("source Containerfile must build %s exactly once from its actual command", command)
			}
		}
		if !regexp.MustCompile(`(?m)^COPY\s+--from=backend\s+/out/\s+/app/bin/\s*$`).MatchString(source) ||
			!regexp.MustCompile(`(?m)^CMD\s+\["/app/bin/core-api"\]\s*$`).MatchString(source) {
			t.Fatal("verification packaging must retain the complete bin copy and default core command")
		}
		runtime := string(m13RootFile(t, "deploy", "Containerfile.runtime"))
		if !regexp.MustCompile(`(?m)^COPY\s+bin/\s+/app/bin/\s*$`).MatchString(runtime) ||
			!regexp.MustCompile(`(?m)^CMD\s+\["/app/bin/core-api"\]\s*$`).MatchString(runtime) {
			t.Fatal("runtime artifact recipe must preserve the complete command inventory and default core process")
		}
	})

	t.Run("host-packaging-loop", func(t *testing.T) {
		source := m13RootFile(t, "scripts", "package-image.mjs")
		match := regexp.MustCompile(`(?m)^const commands\s*=\s*(\[[^;\r\n]+\]);`).FindSubmatch(source)
		if len(match) != 2 {
			t.Fatal("actual host packaging command inventory is not statically inspectable")
		}
		var commands []string
		if json.Unmarshal(match[1], &commands) != nil {
			t.Fatal("actual host packaging command inventory is not a JSON string array")
		}
		want := []string{"core-api", "ingestion", "retention-worker", "report-worker", "delivery-worker",
			"collection-worker", "assessment-worker", "verification-worker", "aspmctl"}
		counts := map[string]int{}
		for _, command := range commands {
			counts[command]++
		}
		for _, command := range want {
			if counts[command] != 1 {
				t.Errorf("host packaging must include %s exactly once", command)
			}
		}
		if len(commands) != len(want) {
			t.Fatalf("host packaging inventory has %d entries, want the exact nine supported commands", len(commands))
		}
		if !bytes.Contains(source, []byte("for (const command of commands)")) ||
			!bytes.Contains(source, []byte("`./cmd/${command}`")) {
			t.Fatal("the inspected inventory must drive the actual existing host build loop")
		}
	})
}

type m13Probe struct {
	HTTPGet struct {
		Path string `yaml:"path"`
		Port any    `yaml:"port"`
	} `yaml:"httpGet"`
}

type m13Port struct {
	Name          string `yaml:"name"`
	ContainerPort int    `yaml:"containerPort"`
}

type m13Container struct {
	Name            string           `yaml:"name"`
	Command         []string         `yaml:"command"`
	Args            []string         `yaml:"args"`
	Env             []envVar         `yaml:"env"`
	EnvFrom         []any            `yaml:"envFrom"`
	Ports           []m13Port        `yaml:"ports"`
	Resources       map[string]any   `yaml:"resources"`
	SecurityContext map[string]any   `yaml:"securityContext"`
	VolumeMounts    []map[string]any `yaml:"volumeMounts"`
	StartupProbe    m13Probe         `yaml:"startupProbe"`
	ReadinessProbe  m13Probe         `yaml:"readinessProbe"`
	LivenessProbe   m13Probe         `yaml:"livenessProbe"`
}

type m13Manifest struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name   string            `yaml:"name"`
		Labels map[string]string `yaml:"labels"`
	} `yaml:"metadata"`
	Spec struct {
		Replicas *int `yaml:"replicas"`
		Template struct {
			Spec struct {
				AutomountServiceAccountToken *bool            `yaml:"automountServiceAccountToken"`
				SecurityContext              map[string]any   `yaml:"securityContext"`
				Containers                   []m13Container   `yaml:"containers"`
				InitContainers               []m13Container   `yaml:"initContainers"`
				Volumes                      []map[string]any `yaml:"volumes"`
			} `yaml:"spec"`
		} `yaml:"template"`
	} `yaml:"spec"`
}

func m13Manifests(t *testing.T, data []byte) []m13Manifest {
	t.Helper()
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	var result []m13Manifest
	for index := 0; ; index++ {
		var item m13Manifest
		err := decoder.Decode(&item)
		if errors.Is(err, io.EOF) {
			return result
		}
		if err != nil || index >= 32 {
			t.Fatal("actual Helm output is invalid or exceeded the bounded document count")
		}
		result = append(result, item)
	}
}

func m13VerificationValues(enabled any) map[string]any {
	input := values()
	input["verification"] = map[string]any{
		"enabled": enabled, "replicas": 1, "leaseDuration": "90s",
		"authorizationInterval": "100ms", "maxFixtureBytes": 65536,
		"resources": map[string]any{
			"requests": map[string]any{"cpu": "100m", "memory": "128Mi"},
			"limits":   map[string]any{"cpu": "1", "memory": "512Mi"},
		},
	}
	return input
}

func m13VerificationDeployment(t *testing.T, data []byte) (m13Manifest, int) {
	t.Helper()
	var selected m13Manifest
	count := 0
	for _, item := range m13Manifests(t, data) {
		if item.Kind == "Deployment" && item.Metadata.Labels["app.kubernetes.io/component"] == "verification" {
			selected, count = item, count+1
		}
		if item.Kind != "Deployment" && item.Metadata.Labels["app.kubernetes.io/component"] == "verification" {
			t.Fatalf("verification opt-in introduced an unrelated %s resource", item.Kind)
		}
	}
	return selected, count
}

func m13VerificationEnvironment(t *testing.T, container m13Container) map[string]envVar {
	t.Helper()
	if len(container.EnvFrom) != 0 {
		t.Fatal("verification deployment must not bulk-import Secret or ConfigMap authority")
	}
	result := map[string]envVar{}
	for _, variable := range container.Env {
		if _, duplicate := result[variable.Name]; duplicate {
			t.Fatalf("verification deployment duplicated environment %s", variable.Name)
		}
		result[variable.Name] = variable
	}
	return result
}

func m13ProbePath(t *testing.T, container m13Container, selected m13Probe, want string) {
	t.Helper()
	if selected.HTTPGet.Path != want {
		t.Fatalf("verification probe path=%q, want=%q", selected.HTTPGet.Path, want)
	}
	for _, port := range container.Ports {
		if selected.HTTPGet.Port == port.Name || selected.HTTPGet.Port == port.ContainerPort {
			return
		}
	}
	t.Fatal("verification probe does not resolve to a declared container port")
}

func TestVerificationDeploymentHelmIsExplicitDatabaseOnlyOptIn(t *testing.T) {
	baseline, baselineDiagnostic, err := render(t, values())
	if err != nil {
		t.Fatalf("existing Helm baseline failed: %s (%T)", baselineDiagnostic, err)
	}
	if _, count := m13VerificationDeployment(t, baseline); count != 0 {
		t.Fatal("verification role must remain disabled by default")
	}
	off, diagnostic, err := render(t, m13VerificationValues(false))
	if err != nil {
		t.Fatalf("explicit verification=false failed: %s (%T)", diagnostic, err)
	}
	if !bytes.Equal(baseline, off) {
		t.Fatal("explicit verification=false changed existing rendered resources")
	}

	input := m13VerificationValues(true)
	output, diagnostic, err := render(t, input)
	if err != nil {
		t.Fatalf("complete verification opt-in failed real Helm rendering: %s (%T)", diagnostic, err)
	}
	deployment, count := m13VerificationDeployment(t, output)
	if count != 1 {
		t.Fatalf("verification enablement must add exactly one independent Deployment, found %d", count)
	}
	pod := deployment.Spec.Template.Spec
	if deployment.Spec.Replicas == nil || *deployment.Spec.Replicas != 1 ||
		len(pod.Containers) != 1 || len(pod.InitContainers) != 0 {
		t.Fatal("verification role must have its own selected replica and exactly one worker container")
	}
	container := pod.Containers[0]
	if container.Name != "verification" ||
		!reflect.DeepEqual(container.Command, []string{"/app/bin/verification-worker"}) ||
		len(container.Args) != 0 {
		t.Fatal("verification Deployment does not execute only the standalone worker command")
	}
	env := m13VerificationEnvironment(t, container)
	allowed := map[string]string{
		"ASPM_SCHEMA":                              "aspm",
		"ASPM_DB_MAX_CONNECTIONS":                  "5",
		"ASPM_LISTEN":                              "0.0.0.0:8080",
		"ASPM_VERIFICATION_LEASE_DURATION":         "90s",
		"ASPM_VERIFICATION_AUTHORIZATION_INTERVAL": "100ms",
		"ASPM_VERIFICATION_MAX_FIXTURE_BYTES":      "65536",
	}
	database := env["ASPM_DATABASE_URL"].ValueFrom.SecretKeyRef
	if database == nil || database.Key != "database-url" || database.Optional ||
		env["ASPM_DATABASE_URL"].Value != "" {
		t.Fatal("verification role must retain one required database-url Secret reference")
	}
	for name, want := range allowed {
		value, present := env[name]
		if !present || value.Value != want || value.ValueFrom.SecretKeyRef != nil {
			t.Errorf("verification %s=%q, want exact literal %q", name, value.Value, want)
		}
	}
	if len(env) != len(allowed)+1 {
		t.Fatalf("verification role received %d environment values, want only database/runtime values", len(env))
	}
	for name, value := range env {
		if name == "ASPM_WORKER_ID" || strings.HasPrefix(name, "ASPM_S3_") ||
			strings.HasPrefix(name, "ASPM_COLLECTION_") || strings.HasPrefix(name, "ASPM_ASSESSMENT_") ||
			strings.HasPrefix(name, "AWS_") || name == "ASPM_INTEGRATION_ENCRYPTION_KEY" ||
			name == "ASPM_BOOTSTRAP_TOKEN" || name == "ASPM_PUBLIC_ORIGIN" {
			t.Fatalf("verification role gained unnecessary authority through %s", name)
		}
		if value.ValueFrom.SecretKeyRef != nil && name != "ASPM_DATABASE_URL" {
			t.Fatal("verification role received a non-database Secret authority")
		}
	}
	if !reflect.DeepEqual(container.Resources, input["verification"].(map[string]any)["resources"]) {
		t.Fatal("verification Deployment did not retain its own selected resource profile")
	}
	if pod.AutomountServiceAccountToken == nil || *pod.AutomountServiceAccountToken ||
		pod.SecurityContext["runAsNonRoot"] != true ||
		pod.SecurityContext["runAsUser"] != 10001 ||
		pod.SecurityContext["runAsGroup"] != 10001 {
		t.Fatal("verification role lost nonroot and no-service-account-token hardening")
	}
	if container.SecurityContext["allowPrivilegeEscalation"] != false ||
		container.SecurityContext["readOnlyRootFilesystem"] != true ||
		!reflect.DeepEqual(container.SecurityContext["capabilities"], map[string]any{"drop": []any{"ALL"}}) ||
		!reflect.DeepEqual(container.SecurityContext["seccompProfile"], map[string]any{"type": "RuntimeDefault"}) {
		t.Fatal("verification role lost read-only, no-escalation, drop-all or seccomp hardening")
	}
	if len(pod.Volumes) != 1 || pod.Volumes[0]["name"] != "tmp" ||
		len(container.VolumeMounts) != 1 || container.VolumeMounts[0]["name"] != "tmp" ||
		container.VolumeMounts[0]["mountPath"] != "/tmp" {
		t.Fatal("verification role must have only one bounded writable temporary mount")
	}
	empty, ok := pod.Volumes[0]["emptyDir"].(map[string]any)
	if !ok || empty["medium"] != "Memory" || empty["sizeLimit"] != "64Mi" {
		t.Fatal("verification role temporary storage must be memory-backed and bounded to 64Mi")
	}
	m13ProbePath(t, container, container.StartupProbe, "/readyz")
	m13ProbePath(t, container, container.ReadinessProbe, "/readyz")
	m13ProbePath(t, container, container.LivenessProbe, "/healthz")
}

func TestVerificationDeploymentHelmDefaultsAndFiniteValidation(t *testing.T) {
	var selected map[string]any
	if yaml.Unmarshal(m13RootFile(t, "deploy", "helm", "aspm", "values.yaml"), &selected) != nil {
		t.Fatal("actual values.yaml is not valid YAML")
	}
	if !reflect.DeepEqual(selected["verification"], m13VerificationValues(false)["verification"]) {
		t.Fatal("values.yaml must declare the exact disabled verification runtime profile and defaults")
	}
	cases := []struct {
		name, path string
		change     func(map[string]any)
	}{
		{"enabled-string", "verification.enabled", func(v map[string]any) {
			v["verification"].(map[string]any)["enabled"] = "true"
		}},
		{"blank-lease", "verification.leaseDuration", func(v map[string]any) {
			v["verification"].(map[string]any)["leaseDuration"] = ""
		}},
		{"numeric-authorization", "verification.authorizationInterval", func(v map[string]any) {
			v["verification"].(map[string]any)["authorizationInterval"] = 100
		}},
		{"zero-fixture", "verification.maxFixtureBytes", func(v map[string]any) {
			v["verification"].(map[string]any)["maxFixtureBytes"] = 0
		}},
		{"oversize-fixture", "verification.maxFixtureBytes", func(v map[string]any) {
			v["verification"].(map[string]any)["maxFixtureBytes"] = 65537
		}},
		{"string-fixture", "verification.maxFixtureBytes", func(v map[string]any) {
			v["verification"].(map[string]any)["maxFixtureBytes"] = "65536"
		}},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			input := m13VerificationValues(true)
			item.change(input)
			_, diagnostic, err := render(t, input)
			var exit *exec.ExitError
			if !errors.As(err, &exit) || !strings.Contains(diagnostic, item.path) {
				t.Errorf("actual Helm render must reject invalid %s with that exact values path; error type %T",
					item.path, err)
			}
		})
	}
}

func TestVerificationDeploymentManualQuadletIsProtectedAndNotActivated(t *testing.T) {
	source := shippedUnit(t, "verification")
	sections := unit(t, source)
	container := sections["Container"]
	for key, want := range map[string][]string{
		"EnvironmentFile": {"/etc/aspm/verification.env"},
		"Exec":            {"/app/bin/verification-worker"},
		"Network":         {"aspm.network"},
		"ReadOnly":        {"true"},
		"NoNewPrivileges": {"true"},
		"DropCapability":  {"all"},
	} {
		if !reflect.DeepEqual(container[key], want) {
			t.Errorf("verification Quadlet must retain exact protected %s=%q", key, want)
		}
	}
	for _, key := range []string{"Environment", "EnvironmentHost", "PodmanArgs", "Secret", "Volume", "Entrypoint"} {
		if len(container[key]) != 0 {
			t.Errorf("verification Quadlet gained inline, host, opaque, credential or extra-volume authority through %s", key)
		}
	}
	for _, key := range []string{"Environment", "EnvironmentFile", "PassEnvironment",
		"ImportCredential", "LoadCredential", "LoadCredentialEncrypted", "SetCredential"} {
		if len(sections["Service"][key]) != 0 {
			t.Errorf("verification Quadlet Service bypasses protected verification.env through %s", key)
		}
	}
	if _, present := sections["Install"]; present {
		t.Fatal("manual verification Quadlet must not declare autostart, alias or installer activation")
	}
	if !reflect.DeepEqual(sections["Service"]["Restart"], []string{"on-failure"}) {
		t.Fatal("manual verification Quadlet must retain ordinary on-failure restart")
	}
	for _, values := range sections["Unit"] {
		if strings.Contains(strings.Join(values, " "), "aspm-storage") {
			t.Fatal("database-only verification Quadlet must not depend on the storage service")
		}
	}
	tmp := false
	for _, mount := range container["Tmpfs"] {
		match := regexp.MustCompile(`^/tmp:rw,(?:[^,]+,)*size=([1-9][0-9]*)([mk])(?:,.*)?$`).FindStringSubmatch(mount)
		if len(match) == 3 {
			size, err := strconv.ParseUint(match[1], 10, 64)
			tmp = err == nil && (match[2] == "m" && size <= 64 || match[2] == "k" && size <= 64*1024)
		}
	}
	if !tmp || len(container["Tmpfs"]) != 1 {
		t.Fatal("verification Quadlet must have one positive writable /tmp mount bounded to at most 64MiB")
	}

	config := QuadletRoleConfig{Role: "verification", EnvironmentFile: "/etc/aspm/verification.env"}
	rendered, err := renderQuadlet(t, source, config)
	if err != nil || len(rendered) == 0 {
		t.Fatalf("selected verification Quadlet failed production rendering (%T)", err)
	}
	before, after := unit(t, source), unit(t, rendered)
	if !reflect.DeepEqual(after["Container"]["EnvironmentFile"], []string{config.EnvironmentFile}) {
		t.Fatal("verification Quadlet renderer did not preserve the selected protected environment file")
	}
	for _, parsed := range []map[string]map[string][]string{before, after} {
		delete(parsed["Container"], "EnvironmentFile")
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatal("verification Quadlet renderer changed unrelated shipped directives")
	}
	for _, invalid := range []QuadletRoleConfig{
		{Role: "verification"},
		{Role: "verification", EnvironmentFile: "/etc/aspm/runtime.env"},
		{Role: "verification", EnvironmentFile: "/etc/aspm/verification.env", RawPrefix: "evidence/"},
		{Role: "verification", EnvironmentFile: "/etc/aspm/verification.env", ReadinessKey: "evidence/ready.txt"},
		{Role: "verification", EnvironmentFile: "/etc/aspm/verification.env", NormalizedPrefix: "normalized/"},
		{Role: "verification", EnvironmentFile: "/etc/aspm/verification.env", ArchivePrefix: "archive/"},
	} {
		output, renderErr := renderQuadlet(t, source, invalid)
		if renderErr == nil || len(output) != 0 {
			t.Fatal("verification Quadlet renderer accepted missing/shared environment or storage scope authority")
		}
	}
	if bytes.Contains(m13RootFile(t, "internal", "install", "execution", "linux.go"), []byte("aspm-verification")) {
		t.Fatal("manual verification Quadlet was added to automatic installer copy/start authority")
	}
}

func requireM13DocumentationFragments(t *testing.T, data []byte, fragments ...string) {
	t.Helper()
	text := strings.ToLower(string(data))
	for _, fragment := range fragments {
		if !strings.Contains(text, strings.ToLower(fragment)) {
			t.Errorf("operator documentation is missing required bounded statement %q", fragment)
		}
	}
}

func TestVerificationDeploymentReleaseAndOperatorDocumentationContract(t *testing.T) {
	requireM13DocumentationFragments(t, m13RootFile(t, "README.md"),
		`cmd\verification-worker`,
		`docs\m13-verification-worker.md`,
		"database-only",
		"synthetic fixture reproduction is not proof")
	requireM13DocumentationFragments(t, m13RootFile(t, "docs", "m02-runtime.md"),
		"verification.enabled: false",
		"/app/bin/verification-worker",
		"/etc/aspm/verification.env",
		"storage:not-required",
		"manual opt-in")
	requireM13DocumentationFragments(t, m13RootFile(t, "docs", "m13-verification-worker.md"),
		"ASPM_DATABASE_URL",
		"ASPM_SCHEMA",
		"ASPM_DB_MAX_CONNECTIONS",
		"ASPM_LISTEN",
		"ASPM_TLS_CERT_FILE",
		"ASPM_TLS_KEY_FILE",
		"ASPM_VERIFICATION_LEASE_DURATION",
		"90s",
		"ASPM_VERIFICATION_AUTHORIZATION_INTERVAL",
		"100ms",
		"ASPM_VERIFICATION_MAX_FIXTURE_BYTES",
		"65536",
		"lower-case 32-hex",
		"database reachable",
		"schema compatible",
		"storage not required",
		"does not claim",
		"provider",
		"target",
		"does not close",
		"backup",
		"restore",
		"high availability",
		"performance",
		"signing",
		"release certification")
}
