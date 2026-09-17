package collection_deployment

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

func TestCollectionDeploymentImageSourceAndHostPackagingInventory(t *testing.T) {
	image := containerInstructions(source(t, "Containerfile"))
	folded := strings.ReplaceAll(strings.ReplaceAll(image, "\\\r\n", " "), "\\\n", " ")
	binaries := []string{"core-api", "ingestion", "report-worker", "delivery-worker", "aspmctl", "collection-worker"}
	for _, binary := range binaries {
		recipe := `go build[^&\r\n]*-o\s+/out/` + regexp.QuoteMeta(binary) + `\s+\./cmd/` + regexp.QuoteMeta(binary) + `(?:\s|$)`
		if !regexp.MustCompile(recipe).MatchString(folded) {
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
			t.Error("optional collection packaging must preserve the image's default core command")
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
	wsl, distro := os.Getenv("ASPM_COLLECTION_DEPLOY_WSL"), os.Getenv("ASPM_COLLECTION_DEPLOY_DISTRO")
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
	generator := os.Getenv("ASPM_COLLECTION_DEPLOY_GENERATOR")
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

func TestCollectionDeploymentProtectedOptInQuadletAndNativeGeneration(t *testing.T) {
	t.Run("existing-delivery-native-control", func(t *testing.T) {
		unchangedInstaller(t)
		unit := generatedUnit(t, "aspm-delivery", source(t, "deploy", "quadlet", "aspm-delivery.container"))
		if !strings.Contains(strings.Join(unit["Service"]["ExecStart"], " "), "/app/bin/delivery-worker") {
			t.Fatal("real Podman parser did not preserve the existing delivery command")
		}
		if len(unit["Install"]["WantedBy"]) != 0 || len(unit["Install"]["RequiredBy"]) != 0 {
			t.Fatal("existing delivery opt-in behavior changed")
		}
	})
	t.Run("collection-protected-manual-unit", func(t *testing.T) {
		unchangedInstaller(t)
		data := source(t, "deploy", "quadlet", "aspm-collection.container")
		unit := parseUnit(t, data)
		c := unit["Container"]
		for key, want := range map[string][]string{
			"EnvironmentFile": {"/etc/aspm/collection.env"}, "Exec": {"/app/bin/collection-worker"},
			"Network": {"aspm.network"}, "ReadOnly": {"true"}, "NoNewPrivileges": {"true"}, "DropCapability": {"all"},
		} {
			if !reflect.DeepEqual(c[key], want) {
				t.Errorf("shipped collection unit must use its exact protected/isolated %s directive", key)
			}
		}
		for _, field := range []string{"Environment", "PodmanArgs", "Secret", "Volume"} {
			if len(c[field]) != 0 {
				t.Errorf("collection unit may not add inline/opaque/credential authority through %s", field)
			}
		}
		if host := c["EnvironmentHost"]; len(host) != 0 && !reflect.DeepEqual(host, []string{"false"}) {
			t.Error("collection unit imports host environment")
		}
		for _, field := range []string{"Environment", "EnvironmentFile", "PassEnvironment"} {
			if len(unit["Service"][field]) != 0 {
				t.Error("service environment bypasses the sole protected collection.env authority")
			}
		}
		if len(unit["Install"]) != 0 {
			t.Error("collection must not have an Install autostart/alias configuration")
		}
		if !reflect.DeepEqual(unit["Service"]["Restart"], []string{"on-failure"}) {
			t.Error("manual collection unit must keep the ordinary on-failure restart policy")
		}
		tmp := false
		for _, mount := range c["Tmpfs"] {
			tmp = tmp || strings.HasPrefix(mount, "/tmp:rw,") && strings.Contains(mount, ",size=")
		}
		if !tmp {
			t.Error("read-only collection unit must retain a bounded writable /tmp mount")
		}
		generated := generatedUnit(t, "aspm-collection", data)
		command := strings.Join(generated["Service"]["ExecStart"], " ")
		if !strings.Contains(command, "/app/bin/collection-worker") ||
			!strings.Contains(command, "--env-file /etc/aspm/collection.env") ||
			!strings.Contains(command, "--read-only") ||
			!strings.Contains(command, "--security-opt=no-new-privileges") || !strings.Contains(command, "--cap-drop=all") {
			t.Error("native Podman 4.9 output lost the selected command, protected env or isolation")
		}
		if len(generated["Install"]) != 0 {
			t.Error("actual native generated collection service gained autostart configuration")
		}
	})
}
