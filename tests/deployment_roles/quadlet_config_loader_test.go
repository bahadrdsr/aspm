package deployment_roles

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/bahadrdsr/aspm/internal/install/quadlet"
)

func loaderSourceLine(t *testing.T, source []byte, section, directive string) []byte {
	t.Helper()
	text, newline := string(source), "\n"
	if strings.Contains(text, "\r\n") {
		newline = "\r\n"
	}
	heading := "[" + section + "]" + newline
	if strings.Count(text, heading) != 1 || strings.ContainsAny(directive, "\r\n") {
		t.Fatal("source-only fixture requires one existing section and one inert directive")
	}
	return []byte(strings.Replace(text, heading, heading+directive+newline, 1))
}

func loaderBaseline(t *testing.T, role string) ([]byte, QuadletRoleConfig) {
	t.Helper()
	source := shippedUnit(t, role)
	source = loaderSourceLine(t, source, "Container", "Environment=ASPM_SCHEMA=synthetic_loader_guard")
	source = loaderSourceLine(t, source, "Service", "RestartSec=11")
	config := selectedScope(role, "evidence/", "normalized/")
	rendered, err := renderQuadlet(t, source, config)
	if err != nil || len(rendered) == 0 {
		t.Fatalf("valid %s source/config control failed (%T)", role, err)
	}
	before, after := unit(t, source), unit(t, rendered)
	env := roleEnvironment(t, after, role, config.EnvironmentFile)
	if env["ASPM_SCHEMA"] != "synthetic_loader_guard" {
		t.Error("safe existing Container environment was not preserved")
	}
	for _, key := range []string{"Restart", "TimeoutStartSec", "RestartSec"} {
		if len(before["Service"][key]) == 0 || !reflect.DeepEqual(before["Service"][key], after["Service"][key]) {
			t.Errorf("truly unrelated Service.%s was not preserved", key)
		}
	}
	if role != "reports" && (env["ASPM_S3_PREFIX"] != config.RawPrefix || env["ASPM_S3_READINESS_KEY"] != config.ReadinessKey) {
		t.Error("loader hardening changed explicit selected raw/readiness scope")
	}
	if role == "ingestion" && env["ASPM_S3_NORMALIZED_PREFIX"] != config.NormalizedPrefix {
		t.Error("loader hardening changed explicit selected normalized scope")
	}
	for _, parsed := range []map[string]map[string][]string{before, after} {
		delete(parsed["Container"], "EnvironmentFile")
		delete(parsed["Container"], "Environment")
	}
	if !reflect.DeepEqual(before, after) {
		t.Error("valid rendering changed unrelated shipped directives")
	}
	return source, config
}

func TestDeploymentQuadletLoaderBaselinePreservesSafeSettings(t *testing.T) {
	for _, role := range []string{"core", "ingestion", "reports"} {
		t.Run(role, func(t *testing.T) { loaderBaseline(t, role) })
	}
}

func TestDeploymentQuadletRejectsAdditionalConfigurationSources(t *testing.T) {
	cases := []struct {
		name, section, directive string
	}{
		{"module", "Container", "ContainersConfModule=/synthetic-never-loaded/role-module.conf"},
		{"global-module-selector", "Container", "GlobalArgs=--module=/synthetic-never-loaded/role-module.conf"},
		{"containers-conf", "Service", "Environment=CONTAINERS_CONF=/synthetic-never-loaded/containers.conf"},
		{"storage-conf", "Service", "Environment=CONTAINERS_STORAGE_CONF=/synthetic-never-loaded/storage.conf"},
		{"registries-conf", "Service", "Environment=CONTAINERS_REGISTRIES_CONF=/synthetic-never-loaded/registries.conf"},
		{"aspm-access", "Service", "Environment=ASPM_S3_ACCESS_KEY=synthetic-inert-not-a-key"},
		{"aspm-secret", "Service", "Environment=ASPM_S3_SECRET_KEY=synthetic-inert-not-a-secret"},
		{"aws-access", "Service", "Environment=AWS_ACCESS_KEY_ID=synthetic-inert-not-an-aws-key"},
		{"aws-secret", "Service", "Environment=AWS_SECRET_ACCESS_KEY=synthetic-inert-not-an-aws-secret"},
		{"bootstrap", "Service", "Environment=ASPM_BOOTSTRAP_TOKEN=synthetic-inert-not-a-token"},
		{"pass-config-selectors", "Service", "PassEnvironment=CONTAINERS_CONF CONTAINERS_STORAGE_CONF CONTAINERS_REGISTRIES_CONF"},
		{"pass-credential-selectors", "Service", "PassEnvironment=ASPM_S3_ACCESS_KEY AWS_ACCESS_KEY_ID ASPM_BOOTSTRAP_TOKEN"},
	}
	for _, role := range []string{"core", "ingestion", "reports"} {
		t.Run(role, func(t *testing.T) {
			source, config := loaderBaseline(t, role)
			for _, item := range cases {
				t.Run(item.name, func(t *testing.T) {
					// These are never executed or loaded; only the real source parser receives them.
					input := loaderSourceLine(t, source, item.section, item.directive)
					output, err := renderQuadlet(t, input, config)
					if !errors.Is(err, quadlet.ErrUnit) || len(output) != 0 {
						t.Errorf("%s.%s source must return ErrUnit and zero output; errUnit=%t outputBytes=%d errorType=%T",
							item.section, strings.SplitN(item.directive, "=", 2)[0], errors.Is(err, quadlet.ErrUnit), len(output), err)
					}
				})
			}
		})
	}
}
