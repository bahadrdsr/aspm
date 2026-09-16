package deployment_roles

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"
	"time"
)

type QuadletRoleConfig struct {
	Role, EnvironmentFile, RawPrefix, ReadinessKey, NormalizedPrefix string
}

var Production struct {
	RenderQuadletRole func(context.Context, []byte, QuadletRoleConfig) ([]byte, error)
}

func shippedUnit(t *testing.T, role string) []byte {
	t.Helper()
	file := "aspm-" + role + ".container"
	if role == "ingestion" {
		file = "aspm-ingestion@.container"
	}
	data, err := os.ReadFile(filepath.Join("..", "..", "deploy", "quadlet", file))
	if err != nil {
		t.Fatalf("required shipped Quadlet %s is unavailable", file)
	}
	return data
}

func unit(t *testing.T, data []byte) map[string]map[string][]string {
	t.Helper()
	sections := map[string]map[string][]string{}
	section := ""
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasSuffix(line, "\\") {
			t.Fatal("role environment artifact profile requires literal single-line directives")
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = line[1 : len(line)-1]
			if sections[section] == nil {
				sections[section] = map[string][]string{}
			}
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || section == "" {
			t.Fatal("invalid Quadlet section/directive")
		}
		key, value = strings.TrimSpace(key), strings.TrimSpace(value)
		if strings.HasPrefix(value, `"`) {
			unquoted, err := strconv.Unquote(value)
			if err != nil {
				t.Fatal("role environment artifact profile requires a single literal directive value")
			}
			value = unquoted
		}
		if value == "" {
			sections[section][key] = nil
		} else {
			sections[section][key] = append(sections[section][key], value)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatal("Quadlet read failed or exceeded its line bound")
	}
	return sections
}

func roleEnvironment(t *testing.T, sections map[string]map[string][]string, role, file string) map[string]string {
	t.Helper()
	container := sections["Container"]
	if !reflect.DeepEqual(container["EnvironmentFile"], []string{file}) {
		t.Errorf("%s must require only its selected role file %s, not shared/optional runtime.env: got %q", role, file, container["EnvironmentFile"])
	}
	host := container["EnvironmentHost"]
	if len(sections["Service"]["EnvironmentFile"]) != 0 || len(container["PodmanArgs"]) != 0 || len(container["Secret"]) != 0 || (len(host) != 0 && !reflect.DeepEqual(host, []string{"false"})) {
		t.Error("role unit must not reintroduce bulk/shared host environment or opaque Podman env overrides")
	}
	env := map[string]string{}
	for _, assignment := range container["Environment"] {
		key, value, ok := strings.Cut(assignment, "=")
		if !ok || strings.ContainsAny(key, " \t") {
			t.Fatal("use explicit KEY=value environment directives, not inherited variables")
		}
		if _, duplicate := env[key]; duplicate {
			t.Errorf("duplicate inline environment key %s", key)
		}
		env[key] = value
	}
	if role == "reports" {
		for key := range env {
			switch key {
			case "ASPM_DATABASE_URL", "ASPM_SCHEMA", "ASPM_DB_MAX_CONNECTIONS", "ASPM_LISTEN", "ASPM_WORKER_ID":
			default:
				t.Errorf("report-only unit has non-DB/runtime inline env %s", key)
			}
		}
	} else {
		for _, key := range []string{"ASPM_S3_ACCESS_KEY", "ASPM_S3_SECRET_KEY", "AWS_ACCESS_KEY_ID", "AWS_SECRET_ACCESS_KEY"} {
			if _, present := env[key]; present {
				t.Errorf("%s must obtain credentials only from its role-specific file, not inline %s", role, key)
			}
		}
	}
	return env
}

func selectedScope(role, raw, normalized string) QuadletRoleConfig {
	config := QuadletRoleConfig{Role: role, EnvironmentFile: "/etc/aspm/" + role + ".env"}
	if role != "reports" {
		config.RawPrefix, config.ReadinessKey = raw, raw+"readiness/role-probe.txt"
	}
	if role == "ingestion" {
		config.NormalizedPrefix = normalized
	}
	return config
}

func renderQuadlet(t *testing.T, source []byte, config QuadletRoleConfig) ([]byte, error) {
	t.Helper()
	if Production.RenderQuadletRole == nil {
		t.Fatal("production Quadlet renderer binding missing: RenderQuadletRole; no test-side renderer is permitted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result, err := Production.RenderQuadletRole(ctx, source, config)
	if len(result) > 65536 {
		t.Fatal("Quadlet renderer exceeded the bounded unit size")
	}
	return result, err
}

func TestDeploymentQuadletRoleFilesAndNarrowScopes(t *testing.T) {
	for _, role := range []string{"core", "ingestion", "reports"} {
		t.Run("shipped-"+role, func(t *testing.T) {
			roleEnvironment(t, unit(t, shippedUnit(t, role)), role, "/etc/aspm/"+role+".env")
		})
	}
	t.Run("selected-production-rendering", func(t *testing.T) {
		for _, selection := range [][2]string{{"evidence/", "normalized/"}, {"raw/selected-tenant/", "normalized/selected-tenant/"}} {
			for _, role := range []string{"core", "ingestion", "reports"} {
				config := selectedScope(role, selection[0], selection[1])
				source := shippedUnit(t, role)
				rendered, err := renderQuadlet(t, source, config)
				if err != nil || len(rendered) == 0 {
					t.Fatalf("selected %s scope failed production rendering (%T)", role, err)
				}
				before, after := unit(t, source), unit(t, rendered)
				env := roleEnvironment(t, after, role, config.EnvironmentFile)
				if role != "reports" && (env["ASPM_S3_PREFIX"] != config.RawPrefix || env["ASPM_S3_READINESS_KEY"] != config.ReadinessKey) {
					t.Errorf("%s output did not preserve the caller-selected raw/readiness scope", role)
				}
				if role == "ingestion" && env["ASPM_S3_NORMALIZED_PREFIX"] != config.NormalizedPrefix {
					t.Error("ingestion output did not preserve the caller-selected normalized prefix")
				}
				for _, assignment := range before["Container"]["Environment"] {
					key, value, _ := strings.Cut(assignment, "=")
					if key != "ASPM_S3_PREFIX" && key != "ASPM_S3_READINESS_KEY" && key != "ASPM_S3_NORMALIZED_PREFIX" && env[key] != value {
						t.Errorf("scope renderer changed unrelated environment %s", key)
					}
				}
				for _, parsed := range []map[string]map[string][]string{before, after} {
					delete(parsed["Container"], "EnvironmentFile")
					delete(parsed["Container"], "Environment")
				}
				if !reflect.DeepEqual(before, after) {
					t.Error("scope rendering replaced or changed unrelated shipped unit directives")
				}
			}
		}
	})
	t.Run("missing-selected-scope-never-falls-back", func(t *testing.T) {
		for _, item := range []struct{ role, field string }{
			{"core", "EnvironmentFile"}, {"core", "RawPrefix"}, {"core", "ReadinessKey"},
			{"ingestion", "EnvironmentFile"}, {"ingestion", "RawPrefix"}, {"ingestion", "ReadinessKey"}, {"ingestion", "NormalizedPrefix"},
			{"reports", "EnvironmentFile"}, {"reports", "storage-not-admitted"}, {"core", "common-file-not-admitted"},
			{"core", "readiness-outside-selection"}, {"ingestion", "overlapping-normalized"},
		} {
			config := selectedScope(item.role, "evidence/", "normalized/")
			switch item.field {
			case "EnvironmentFile":
				config.EnvironmentFile = ""
			case "RawPrefix":
				config.RawPrefix = ""
			case "ReadinessKey":
				config.ReadinessKey = ""
			case "NormalizedPrefix":
				config.NormalizedPrefix = ""
			case "storage-not-admitted":
				config.RawPrefix = "evidence/"
			case "common-file-not-admitted":
				config.EnvironmentFile = "/etc/aspm/runtime.env"
			case "readiness-outside-selection":
				config.ReadinessKey = "outside-selected-prefix/ready.txt"
			case "overlapping-normalized":
				config.NormalizedPrefix = config.RawPrefix + "normalized/"
			}
			// Deliberate stale defaults in a test-owned copy are not production policy.
			source := append(shippedUnit(t, item.role), []byte("\n[Container]\nEnvironment=ASPM_S3_PREFIX=operator-default/\nEnvironment=ASPM_S3_READINESS_KEY=operator-default/ready.txt\nEnvironment=ASPM_S3_NORMALIZED_PREFIX=operator-normalized/\n")...)
			rendered, err := renderQuadlet(t, source, config)
			if err == nil || len(rendered) != 0 {
				t.Errorf("%s %s must reject missing/forbidden selection without using source/operator defaults", item.role, item.field)
			}
		}
	})
}
