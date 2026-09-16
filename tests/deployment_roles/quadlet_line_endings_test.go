package deployment_roles

import (
	"bytes"
	"errors"
	"reflect"
	"testing"

	"github.com/bahadrdsr/aspm/internal/install/quadlet"
)

func lineEndingBaseline(t *testing.T, role string) ([]byte, QuadletRoleConfig) {
	t.Helper()
	source, config := loaderBaseline(t, role)
	lf := bytes.ReplaceAll(source, []byte("\r\n"), []byte("\n"))
	if bytes.ContainsRune(lf, '\r') || !bytes.HasSuffix(lf, []byte("\n")) {
		t.Fatal("baseline must contain only ordinary LF/CRLF and a final newline")
	}
	return lf, config
}

func TestDeploymentQuadletPreservesLFAndCRLF(t *testing.T) {
	for _, role := range []string{"core", "ingestion", "reports"} {
		for _, style := range []string{"LF", "CRLF"} {
			t.Run(role+"/"+style, func(t *testing.T) {
				source, config := lineEndingBaseline(t, role)
				if style == "CRLF" {
					source = bytes.ReplaceAll(source, []byte("\n"), []byte("\r\n"))
				}
				output, err := renderQuadlet(t, source, config)
				if err != nil || len(output) == 0 {
					t.Fatalf("valid %s source failed rendering (%T)", style, err)
				}
				if style == "LF" {
					if bytes.ContainsRune(output, '\r') {
						t.Error("ordinary LF source gained a CR byte")
					}
				} else if bytes.ContainsAny(bytes.ReplaceAll(output, []byte("\r\n"), nil), "\r\n") {
					t.Error("ordinary CRLF style was not preserved")
				}
				before, after := unit(t, source), unit(t, output)
				env := roleEnvironment(t, after, role, config.EnvironmentFile)
				if env["ASPM_SCHEMA"] != "synthetic_loader_guard" {
					t.Error("line-ending validation dropped safe Container environment")
				}
				if !reflect.DeepEqual(before["Service"], after["Service"]) {
					t.Error("line-ending validation changed unrelated service directives")
				}
				if role != "reports" && (env["ASPM_S3_PREFIX"] != config.RawPrefix || env["ASPM_S3_READINESS_KEY"] != config.ReadinessKey) {
					t.Error("valid line endings changed selected raw/readiness scope")
				}
				if role == "ingestion" && env["ASPM_S3_NORMALIZED_PREFIX"] != config.NormalizedPrefix {
					t.Error("valid line endings changed selected normalized scope")
				}
				for _, parsed := range []map[string]map[string][]string{before, after} {
					delete(parsed["Container"], "EnvironmentFile")
					delete(parsed["Container"], "Environment")
				}
				if !reflect.DeepEqual(before, after) {
					t.Error("valid rendering changed unrelated source settings")
				}
			})
		}
	}
}

func TestDeploymentQuadletRejectsMalformedCRBoundaries(t *testing.T) {
	for _, role := range []string{"core", "ingestion", "reports"} {
		t.Run(role, func(t *testing.T) {
			source, config := lineEndingBaseline(t, role)
			marker := []byte("RestartSec=11\n")
			if bytes.Count(source, marker) != 1 {
				t.Fatal("fixture must contain exactly one benign service-line marker")
			}
			cases := []struct {
				name  string
				input []byte
			}{
				{"bare-CR-lines", bytes.ReplaceAll(source, []byte("\n"), []byte("\r"))},
				{"embedded-service-CR", bytes.Replace(source, marker,
					[]byte("RestartSec=11\rEnvironment=CONTAINERS_CONF=/synthetic-never-loaded/cr-boundary.conf\n"), 1)},
				{"trailing-bare-CR", append(append([]byte(nil), source...), '\r')},
				{"doubled-CR-before-LF", bytes.ReplaceAll(source, []byte("\n"), []byte("\r\r\n"))},
			}
			for _, item := range cases {
				t.Run(item.name, func(t *testing.T) {
					// Byte fixtures only: no directive or referenced file is executed or loaded.
					output, err := renderQuadlet(t, item.input, config)
					if !errors.Is(err, quadlet.ErrUnit) || len(output) != 0 {
						t.Errorf("%s must return ErrUnit and zero output; errUnit=%t outputBytes=%d errorType=%T",
							item.name, errors.Is(err, quadlet.ErrUnit), len(output), err)
					}
				})
			}
		})
	}
}
