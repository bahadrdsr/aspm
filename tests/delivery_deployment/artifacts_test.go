package delivery_deployment

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
	"time"
)

func TestDeliveryDeploymentContainerSourceBuildsAndRetainsWorkerBinary(t *testing.T) {
	containerfile := containerInstructions(source(t, "Containerfile"))
	folded := strings.ReplaceAll(strings.ReplaceAll(containerfile, "\\\r\n", " "), "\\\n", " ")
	for _, binary := range []string{"core-api", "ingestion", "report-worker", "aspmctl", "delivery-worker"} {
		pattern := `go build[^&\r\n]*-o\s+/out/` + regexp.QuoteMeta(binary) + `\s+\./cmd/` + regexp.QuoteMeta(binary)
		if !regexp.MustCompile(pattern).MatchString(folded) {
			t.Errorf("source Containerfile does not build the required %s binary from its actual command package", binary)
		}
	}
	if !regexp.MustCompile(`(?m)^COPY\s+--from=backend\s+/out/\s+/app/bin/\s*$`).MatchString(containerfile) {
		t.Error("source image must retain the complete backend binary output under /app/bin")
	}
	runtime := containerInstructions(source(t, "deploy", "Containerfile.runtime"))
	if !regexp.MustCompile(`(?m)^COPY\s+bin/\s+/app/bin/\s*$`).MatchString(runtime) {
		t.Error("runtime artifact image must retain the complete supplied bin directory")
	}
	for _, text := range []string{containerfile, runtime} {
		if !strings.Contains(text, `CMD ["/app/bin/core-api"]`) {
			t.Error("packaging must not replace the default core command with the optional delivery worker")
		}
	}
	helper := string(source(t, "scripts", "package-image.mjs"))
	match := regexp.MustCompile(`(?s)const commands\s*=\s*(\[[^;]+\]);`).FindStringSubmatch(helper)
	if len(match) != 2 {
		t.Fatal("actual host packaging helper no longer exposes its reviewable command inventory")
	}
	var commands []string
	if json.Unmarshal([]byte(match[1]), &commands) != nil {
		t.Fatal("host packaging command inventory could not be parsed")
	}
	found := false
	for _, command := range commands {
		found = found || command == "delivery-worker"
	}
	if !found {
		t.Error("actual host artifact packaging helper omits delivery-worker")
	}
	t.Log("Source artifact gate only: no image build, fake binary, helper build execution or container activation is claimed.")
}

func containerInstructions(data []byte) string {
	var lines []string
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			continue
		}
		lines = append(lines, line)
	}
	return strings.Join(lines, "\n")
}

func parseUnit(t *testing.T, data []byte) map[string]map[string][]string {
	t.Helper()
	result := map[string]map[string][]string{}
	section := ""
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = line[1 : len(line)-1]
			if result[section] == nil {
				result[section] = map[string][]string{}
			}
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if !ok || section == "" || strings.HasSuffix(line, "\\") {
			t.Fatal("unit must contain bounded literal INI directives, not opaque continuations")
		}
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		result[section][name] = append(result[section][name], value)
	}
	if scanner.Err() != nil {
		t.Fatal("unit parse failed")
	}
	return result
}

func nativeCommand(t *testing.T, args ...string) []byte {
	t.Helper()
	wsl := os.Getenv("ASPM_DELIVERY_DEPLOY_WSL")
	distro := os.Getenv("ASPM_DELIVERY_DEPLOY_DISTRO")
	if !filepath.IsAbs(wsl) || distro == "" {
		t.Fatal("explicit existing WSL/native generator runner is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, wsl, append([]string{"--distribution", distro, "--exec"}, args...)...)
	var output boundedOutput
	command.Stdout, command.Stderr = &output, &output
	if err := command.Run(); err != nil || ctx.Err() != nil {
		t.Fatalf("actual rootless generator command failed (%T): %s", err, output.String())
	}
	return output.Bytes()
}

func nativePath(t *testing.T, value string) string {
	t.Helper()
	absolute, err := filepath.Abs(value)
	if err != nil {
		t.Fatal("cannot resolve owned native-generator path")
	}
	return strings.TrimSpace(string(nativeCommand(t, "wslpath", "-a", "-u", absolute)))
}

func generateUnit(t *testing.T, filename string, data []byte) []byte {
	t.Helper()
	if strings.TrimSpace(string(nativeCommand(t, "id", "-u"))) == "0" {
		t.Fatal("native syntax validation must use the existing unprivileged WSL user")
	}
	generator := os.Getenv("ASPM_DELIVERY_DEPLOY_GENERATOR")
	if !filepath.IsAbs(generator) {
		t.Fatal("explicit cached Podman 4.9 generator binary is required")
	}
	executable := nativePath(t, generator)
	version := strings.TrimSpace(string(nativeCommand(t, executable, "-version")))
	if !strings.HasPrefix(version, "4.9.") {
		t.Fatalf("native syntax gate requires the accepted Podman 4.9 key set, got %s", version)
	}
	directory := ownedDirectory(t, "quadlet")
	if os.WriteFile(filepath.Join(directory, filename), data, 0600) != nil ||
		os.WriteFile(filepath.Join(directory, "aspm.network"), source(t, "deploy", "quadlet", "aspm.network"), 0600) != nil {
		t.Fatal("cannot stage only the selected current unit/network for native dryrun")
	}
	nativeDirectory := nativePath(t, directory)
	output := nativeCommand(t, "env",
		"QUADLET_UNIT_DIRS="+nativeDirectory, "XDG_RUNTIME_DIR="+nativeDirectory,
		"XDG_CONFIG_HOME="+nativeDirectory, "HOME="+nativeDirectory, "TMPDIR="+nativeDirectory,
		executable, "-user", "-dryrun", "-no-kmsg-log")
	if os.WriteFile(filepath.Join(directory, "native-generator.txt"), output, 0600) != nil {
		t.Fatal("cannot preserve actual generator output")
	}
	t.Log("actual rootless Podman", version, "dryrun:", directory)
	return output
}

func installerUnchanged(t *testing.T) {
	t.Helper()
	var before struct {
		Files []struct {
			Path, SHA256 string
		}
	}
	data, err := os.ReadFile(filepath.Join("reviews", "v1", "BEFORE.json"))
	if err != nil || json.Unmarshal(data, &before) != nil {
		t.Fatal("missing initial installer/control hash witness")
	}
	checked := 0
	for _, file := range before.Files {
		if file.Path != "internal\\install\\execution\\linux.go" && file.Path != "internal\\install\\execution\\bundle.go" {
			continue
		}
		bytes, err := os.ReadFile(filepath.Join("..", "..", file.Path))
		if err != nil {
			t.Fatal("existing installer source disappeared")
		}
		hash := sha256.Sum256(bytes)
		if hex.EncodeToString(hash[:]) != file.SHA256 {
			t.Error("opt-in artifact task must not modify installer copy/start behavior")
		}
		checked++
	}
	if checked != 2 {
		t.Fatal("installer control coverage was lost")
	}
}

func generatedService(t *testing.T, output []byte, name string) map[string]map[string][]string {
	t.Helper()
	marker := "---" + name + ".service---"
	_, body, found := strings.Cut(string(output), marker)
	if !found {
		t.Fatal("native generator omitted the selected actual service")
	}
	if end := strings.Index(body, "\n---"); end >= 0 {
		body = body[:end]
	}
	return parseUnit(t, []byte(body))
}

func TestDeliveryDeploymentQuadletIsProtectedAndOptInWithNativeSyntax(t *testing.T) {
	t.Run("existing-native-parser-control", func(t *testing.T) {
		output := generateUnit(t, "aspm-reports.container", source(t, "deploy", "quadlet", "aspm-reports.container"))
		service := generatedService(t, output, "aspm-reports")
		if !strings.Contains(strings.Join(service["Service"]["ExecStart"], " "), "/app/bin/report-worker") {
			t.Fatal("actual native generator did not produce the existing report service command")
		}
	})
	t.Run("delivery-opt-in-unit", func(t *testing.T) {
		installerUnchanged(t)
		data := source(t, "deploy", "quadlet", "aspm-delivery.container")
		unit := parseUnit(t, data)
		container := unit["Container"]
		for name, expected := range map[string][]string{
			"EnvironmentFile": {"/etc/aspm/delivery.env"}, "Exec": {"/app/bin/delivery-worker"},
			"Network": {"aspm.network"}, "ReadOnly": {"true"}, "NoNewPrivileges": {"true"}, "DropCapability": {"all"},
		} {
			if !reflect.DeepEqual(container[name], expected) {
				t.Errorf("delivery Quadlet must retain the exact protected/isolated %s directive", name)
			}
		}
		for _, name := range []string{"Environment", "PodmanArgs", "Secret"} {
			if len(container[name]) != 0 {
				t.Errorf("delivery Quadlet must not smuggle inline/ambient values through %s", name)
			}
		}
		if values := container["EnvironmentHost"]; len(values) != 0 && !reflect.DeepEqual(values, []string{"false"}) {
			t.Error("delivery unit inherits host environment")
		}
		if len(unit["Service"]["Environment"]) != 0 || len(unit["Service"]["EnvironmentFile"]) != 0 {
			t.Error("service-level environment bypasses the single protected container env file")
		}
		for _, name := range []string{"WantedBy", "RequiredBy", "Alias"} {
			if len(unit["Install"][name]) != 0 {
				t.Errorf("delivery must remain manual opt-in, not auto-enabled by Install.%s", name)
			}
		}
		for _, value := range append(unit["Unit"]["After"], unit["Unit"]["Requires"]...) {
			if strings.Contains(value, "aspm-storage") {
				t.Error("delivery unit must not acquire a storage service dependency")
			}
		}
		output := generateUnit(t, "aspm-delivery.container", data)
		service := generatedService(t, output, "aspm-delivery")
		command := strings.Join(service["Service"]["ExecStart"], " ")
		if !strings.Contains(command, "/app/bin/delivery-worker") ||
			!strings.Contains(command, "--env-file /etc/aspm/delivery.env") {
			t.Error("native Podman 4.9 output lost the delivery command or required protected env file")
		}
		if len(service["Install"]["WantedBy"]) != 0 || len(service["Install"]["RequiredBy"]) != 0 {
			t.Error("native generated delivery service unexpectedly acquires an autostart target")
		}
	})
}
