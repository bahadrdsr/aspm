package assessment_deployment

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
	"strconv"
	"strings"
	"testing"
	"time"
)

func containerInstructions(data []byte) string {
	var instructions []string
	for _, line := range strings.Split(string(data), "\n") {
		if !strings.HasPrefix(strings.TrimSpace(line), "#") {
			instructions = append(instructions, line)
		}
	}
	return strings.Join(instructions, "\n")
}

func TestAD1SevenRealCommandsAndActualHostPackagingLoop(t *testing.T) {
	image := containerInstructions(source(t, "Containerfile"))
	folded := strings.ReplaceAll(strings.ReplaceAll(image, "\\\r\n", " "), "\\\n", " ")
	binaries := []string{"core-api", "ingestion", "report-worker", "delivery-worker", "collection-worker", "aspmctl", "assessment-worker"}
	for _, binary := range binaries {
		source(t, "cmd", binary, "main.go")
		recipe := `go build[^&\r\n]*-o\s+/out/` + regexp.QuoteMeta(binary) + `\s+\./cmd/` + regexp.QuoteMeta(binary) + `(?:\s|$)`
		if len(regexp.MustCompile(recipe).FindAllString(folded, -1)) != 1 {
			t.Errorf("source Containerfile omits building %s from its actual command", binary)
		}
	}
	if !regexp.MustCompile(`(?m)^COPY\s+--from=backend\s+/out/\s+/app/bin/\s*$`).MatchString(image) {
		t.Error("source image no longer retains the complete backend bin directory")
	}
	runtime := containerInstructions(source(t, "deploy", "Containerfile.runtime"))
	if !regexp.MustCompile(`(?m)^COPY\s+bin/\s+/app/bin/\s*$`).MatchString(runtime) {
		t.Error("runtime artifact recipe drops the complete packaged bin directory")
	}
	for _, recipe := range []string{image, runtime} {
		if !regexp.MustCompile(`(?m)^CMD\s+\["/app/bin/core-api"\]\s*$`).MatchString(recipe) {
			t.Error("optional assessment packaging must preserve the image's default core command")
		}
		if !regexp.MustCompile(`(?m)^USER\s+10001:10001\s*$`).MatchString(recipe) {
			t.Error("assessment packaging must retain the existing nonroot runtime image identity")
		}
		if !regexp.MustCompile(`(?m)^ENTRYPOINT\s+\[\]\s*$`).MatchString(recipe) {
			t.Error("assessment packaging must retain the empty entrypoint so the core default and explicit worker command remain real")
		}
	}
	helper := source(t, "scripts", "package-image.mjs")
	match := regexp.MustCompile(`(?m)^const commands\s*=\s*(\[[^;\r\n]+\]);`).FindSubmatch(helper)
	if len(match) != 2 {
		t.Fatal("actual host packaging helper's static command inventory is no longer inspectable")
	}
	var commands []string
	if err := json.Unmarshal(match[1], &commands); err != nil {
		t.Fatal("actual packaging command inventory is not a JSON string array")
	}
	seen := map[string]int{}
	for _, command := range commands {
		seen[command]++
	}
	for _, binary := range binaries {
		if seen[binary] != 1 {
			t.Errorf("host packaging must include %s exactly once, preserving every existing command", binary)
		}
	}
	if len(commands) != 7 {
		t.Errorf("actual host command inventory has %d entries, require all seven real commands", len(commands))
	}
	loop := regexp.MustCompile("(?s)for \\(const command of commands\\) \\{\\s*run\\(\"go\", \\[\"build\",[^\\]]*\"-o\", join\\(output, \"bin\", command\\), `\\./cmd/\\$\\{command\\}`\\], \\{\\s*env: \\{[^}]*GOOS: \"linux\", GOARCH: architecture, CGO_ENABLED: \"0\"")
	if !loop.Match(helper) || !bytes.Contains(helper, []byte("const result = spawnSync(command, args, { cwd: root, stdio: \"inherit\", ...options });")) {
		t.Error("the inspected inventory must drive the actual existing host go-build loop, not a parallel or shadow packager")
	}
	t.Log("AD1 legacy source controls checked: existing six real command paths/builds, actual host loop, full bin copies, nonroot and core default")
	t.Log("Missing actual assessment packager entry is parent-owned planned RED; author does not amend scripts or weaken its source guard")
	t.Log("source artifact assertions only: no image build, binary-success substitute or execution of the shared host packager")
}

func parseUnit(t *testing.T, data []byte) map[string]map[string][]string {
	t.Helper()
	unit := map[string]map[string][]string{}
	section := ""
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = line[1 : len(line)-1]
			if unit[section] == nil {
				unit[section] = map[string][]string{}
			}
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || section == "" || strings.HasSuffix(line, "\\") {
			t.Fatal("selected unit must have bounded literal INI directives, not opaque continuations")
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if strings.HasPrefix(value, `"`) {
			decoded, err := strconv.Unquote(value)
			if err != nil {
				t.Fatal("unit contains a nonliteral quoted value")
			}
			value = decoded
		}
		unit[section][key] = append(unit[section][key], value)
	}
	if scanner.Err() != nil {
		t.Fatal("unit parsing exceeded its bound")
	}
	return unit
}

func nativeCommand(t *testing.T, args ...string) []byte {
	t.Helper()
	wsl, distro := os.Getenv("ASPM_ASSESSMENT_DEPLOY_WSL"), os.Getenv("ASPM_ASSESSMENT_DEPLOY_DISTRO")
	if !filepath.IsAbs(wsl) || distro == "" {
		t.Fatal("explicit existing WSL/native generator selection required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, wsl, append([]string{"--distribution", distro, "--exec"}, args...)...)
	var output boundedOutput
	command.Stdout, command.Stderr = &output, &output
	err := command.Run()
	if err != nil || ctx.Err() != nil {
		t.Fatalf("actual rootless native generator command failed (%T): %s", err, output.String())
	}
	return output.Bytes()
}

func nativePath(t *testing.T, path string) string {
	t.Helper()
	absolute, err := filepath.Abs(path)
	if err != nil {
		t.Fatal("cannot resolve owned generator input path")
	}
	return strings.TrimSpace(string(nativeCommand(t, "wslpath", "-a", "-u", absolute)))
}

func generatedUnit(t *testing.T, name string, data []byte) map[string]map[string][]string {
	t.Helper()
	uid, err := strconv.Atoi(strings.TrimSpace(string(nativeCommand(t, "id", "-u"))))
	if err != nil || uid <= 0 {
		t.Fatal("native dryrun requires the existing unprivileged WSL user")
	}
	generator := os.Getenv("ASPM_ASSESSMENT_DEPLOY_GENERATOR")
	if !filepath.IsAbs(generator) {
		t.Fatal("explicit existing Podman 4.9 generator path required")
	}
	executable := nativePath(t, generator)
	version := strings.TrimSpace(string(nativeCommand(t, executable, "-version")))
	if !strings.HasPrefix(version, "4.9.") {
		t.Fatal("native syntax proof must use the accepted Podman 4.9 key set")
	}
	directory := ownedDirectory(t, "quadlet")
	writeArtifact(t, filepath.Join(directory, name+".container"), data)
	writeArtifact(t, filepath.Join(directory, "aspm.network"), source(t, "deploy", "quadlet", "aspm.network"))
	nativeDirectory := nativePath(t, directory)
	output := nativeCommand(t, "env", "-i", "PATH=/usr/bin:/bin", "QUADLET_UNIT_DIRS="+nativeDirectory,
		"HOME="+nativeDirectory, "XDG_CONFIG_HOME="+nativeDirectory, "XDG_RUNTIME_DIR="+nativeDirectory,
		"TMPDIR="+nativeDirectory, executable, "-user", "-dryrun", "-no-kmsg-log")
	writeArtifact(t, filepath.Join(directory, "native-generator.txt"), output)
	receipt, err := json.Marshal(object{"test": t.Name(), "version": version, "uid": uid, "mode": "user dryrun, no kmsg"})
	if err != nil {
		t.Fatal("cannot record native generator receipt")
	}
	writeArtifact(t, filepath.Join(directory, "generator-receipt.json"), receipt)
	t.Log("actual unprivileged Podman", version, "dryrun:", directory)
	_, body, found := strings.Cut(string(output), "---"+name+".service---")
	if !found {
		t.Fatal("real native generator omitted the selected service")
	}
	if next := strings.Index(body, "\n---"); next >= 0 {
		body = body[:next]
	}
	return parseUnit(t, []byte(body))
}

func unchangedInstaller(t *testing.T) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("reviews", "v1", "BEFORE.json"))
	if err != nil {
		t.Fatal("missing installer source hash witness")
	}
	var before struct {
		Files []struct{ Path, SHA256 string }
	}
	if json.Unmarshal(data, &before) != nil {
		t.Fatal("invalid original installer witness")
	}
	count := 0
	for _, file := range before.Files {
		if file.Path != "internal\\install\\execution\\linux.go" && file.Path != "internal\\install\\execution\\bundle.go" {
			continue
		}
		current := source(t, file.Path)
		digest := sha256.Sum256(current)
		if hex.EncodeToString(digest[:]) != file.SHA256 {
			t.Error("installer copy/start/bundle sources must remain byte-identical for this opt-in manual artifact slice")
		}
		count++
	}
	if count != 2 {
		t.Fatal("installer copy/start/bundle hash coverage is incomplete")
	}
}

func TestAD4ManualQuadletNativeGeneratorAndUnchangedInstaller(t *testing.T) {
	t.Run("existing-collection-native-positive-control", func(t *testing.T) {
		unchangedInstaller(t)
		unit := generatedUnit(t, "aspm-collection", source(t, "deploy", "quadlet", "aspm-collection.container"))
		if !strings.Contains(strings.Join(unit["Service"]["ExecStart"], " "), "/app/bin/collection-worker") {
			t.Fatal("real Podman parser did not preserve the existing collection command")
		}
		if _, exists := unit["Install"]; exists {
			t.Fatal("existing collection manual opt-in behavior changed")
		}
	})
	t.Run("assessment-protected-manual-unit", func(t *testing.T) {
		unchangedInstaller(t)
		data := source(t, "deploy", "quadlet", "aspm-assessment.container")
		unit := parseUnit(t, data)
		c := unit["Container"]
		allowedContainer := map[string]bool{"Image": true, "ContainerName": true, "Network": true, "EnvironmentFile": true,
			"Exec": true, "ReadOnly": true, "Tmpfs": true, "NoNewPrivileges": true, "DropCapability": true}
		for key := range c {
			if !allowedContainer[key] {
				t.Errorf("manual assessment profile has unselected container directive %s", key)
			}
		}
		for section, fields := range unit {
			if section != "Unit" && section != "Container" && section != "Service" {
				t.Errorf("manual assessment profile has unselected section %s", section)
			}
			if section == "Service" {
				for key := range fields {
					if key != "Restart" && key != "TimeoutStartSec" && key != "TimeoutStopSec" {
						t.Errorf("manual assessment profile has a service command/environment/privilege bypass %s", key)
					}
				}
				for key := range fields {
					if key == "WantedBy" || key == "RequiredBy" || key == "Alias" {
						t.Errorf("manual assessment profile must not declare autostart/alias %s", key)
					}
				}
			}
		}
		for key, want := range map[string][]string{
			"EnvironmentFile": {"/etc/aspm/assessment.env"}, "Exec": {"/app/bin/assessment-worker"},
			"Network": {"aspm.network"}, "ReadOnly": {"true"}, "NoNewPrivileges": {"true"}, "DropCapability": {"all"},
		} {
			if !reflect.DeepEqual(c[key], want) {
				t.Errorf("shipped assessment unit must use its exact protected/isolated %s directive", key)
			}
		}
		for _, field := range []string{"Environment", "PodmanArgs", "Secret", "Volume", "EnvironmentHost", "Entrypoint"} {
			if len(c[field]) != 0 {
				t.Errorf("assessment unit may not add inline/opaque/credential/unsupported authority through %s", field)
			}
		}
		for _, field := range []string{"Environment", "EnvironmentFile", "PassEnvironment", "ImportCredential", "LoadCredential", "LoadCredentialEncrypted", "SetCredential"} {
			if len(unit["Service"][field]) != 0 {
				t.Error("service environment bypasses the sole protected assessment.env authority")
			}
		}
		if _, exists := unit["Install"]; exists {
			t.Error("assessment must not have any Install/autostart/alias section")
		}
		if !reflect.DeepEqual(unit["Service"]["Restart"], []string{"on-failure"}) {
			t.Error("manual assessment unit must keep the ordinary on-failure restart policy")
		}
		tmp := false
		for _, mount := range c["Tmpfs"] {
			parts := regexp.MustCompile(`^/tmp:rw,(?:[^,]+,)*size=([1-9][0-9]*)([mk])(?:,.*)?$`).FindStringSubmatch(mount)
			if len(parts) == 3 {
				size, err := strconv.ParseUint(parts[1], 10, 64)
				tmp = err == nil && (parts[2] == "m" && size <= 1024 || parts[2] == "k" && size <= 1024*1024)
			}
		}
		if !tmp || len(c["Tmpfs"]) != 1 {
			t.Error("read-only assessment unit must retain a positive writable /tmp mount bounded to at most 1GiB")
		}
		for _, values := range unit["Unit"] {
			if strings.Contains(strings.Join(values, " "), "aspm-storage") {
				t.Error("storage-free assessment must not acquire a storage-service dependency")
			}
		}
		generated := generatedUnit(t, "aspm-assessment", data)
		command := strings.Join(generated["Service"]["ExecStart"], " ")
		if !strings.HasSuffix(command, "/app/bin/assessment-worker") ||
			!strings.Contains(command, "--env-file /etc/aspm/assessment.env") ||
			!strings.Contains(command, "--read-only") ||
			!strings.Contains(command, "--security-opt=no-new-privileges") || !strings.Contains(command, "--cap-drop=all") {
			t.Error("native Podman 4.9 output lost the selected command, protected env or isolation")
		}
		if _, exists := generated["Install"]; exists {
			t.Error("actual native generated assessment service gained autostart configuration")
		}
	})
}
