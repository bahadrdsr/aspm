package installer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func v3ManagedPlan(t *testing.T, f *fixture) Plan {
	t.Helper()
	equal(t, "V3 exercises an approved managed store", section(t, f.config, "objectStore")["mode"], any("managed"))
	t.Setenv("ASPM_S3_PREPARE_READINESS", "false")
	p := f.plan()
	state, err := f.app.Execute(f.ctx, f.input, Approval{DryRun: true})
	ok(t, "managed-install dry-run", err)
	equal(t, "dry-run remains planning only", state.Phase, "planned")
	equal(t, "planning does not write application secrets/files", f.files.writes, 0)
	equal(t, "planning does not mutate native resources", f.mutationCount(), 0)
	return p
}

func TestInstallerV3_ManagedHelmValuesOptInOnlyCoreReadiness(t *testing.T) {
	f := newFixture(t, "kubernetes")
	forward := f.options.Run
	var captured [][]byte
	var captureErr error
	f.options.Run = func(ctx context.Context, command Command) (CommandResult, error) {
		if command.Tool == "helm" && slices.Contains(command.Args, "upgrade") {
			selected := flag(command.Args, "--values", "-f")
			var data []byte
			if selected == "-" {
				data = bytes.Clone(command.Stdin)
			} else {
				relative, err := filepath.Rel(f.options.Root, selected)
				if err != nil || !filepath.IsLocal(relative) {
					captureErr = errors.New("Helm values were not an owned input")
				} else {
					data, captureErr = f.files.root.ReadFile(relative)
				}
			}
			captured = append(captured, data)
		}
		return forward(ctx, command)
	}
	f.reopen()
	state := f.execute(v3ManagedPlan(t, f))
	equal(t, "real managed execution completes", state.Phase, "applied")
	ok(t, "observe actual Helm values without altering tool results", captureErr)
	if len(captured) == 0 {
		t.Fatal("no actual Helm producer values were captured")
	}
	for _, raw := range captured {
		var values object
		ok(t, "decode actual production Helm values", json.Unmarshal(raw, &values))
		core := section(t, values, "core")
		if core["prepareReadiness"] != true {
			t.Error("managed Helm producer omitted core.prepareReadiness=true")
		}
		for role, selected := range map[string]RoleSelection{"core": f.input.RuntimeRoles.Core, "ingestion": f.input.RuntimeRoles.Ingestion} {
			actual := section(t, values, role)
			equal(t, role+" raw scope unchanged", actual["rawPrefix"], selected.RawPrefix)
			equal(t, role+" readiness key unchanged", actual["readinessKey"], selected.ReadinessKey)
			secret := section(t, actual, "s3Secret")
			equal(t, role+" private key reference unchanged", secret["name"], selected.S3Secret.Name)
			equal(t, role+" access selector unchanged", secret["accessKeyKey"], selected.S3Secret.AccessKeyKey)
			equal(t, role+" secret selector unchanged", secret["secretKeyKey"], selected.S3Secret.SecretKeyKey)
		}
		for _, role := range []string{"ingestion", "reports"} {
			if _, present := section(t, values, role)["prepareReadiness"]; present {
				t.Errorf("%s must receive no readiness preparation flag", role)
			}
		}
	}
	f.roleSecret(f.input.RuntimeRoles.Core, f.options.RoleKeys.Core)
	f.roleSecret(f.input.RuntimeRoles.Ingestion, f.options.RoleKeys.Ingestion)
	f.secretFiles(state)
	f.noLeaks(state, nil)
}

func TestInstallerV3_ManagedLinuxFilesOptInOnlyCoreReadiness(t *testing.T) {
	f := newFixture(t, "linux")
	state := f.execute(v3ManagedPlan(t, f))
	equal(t, "real Linux execution completes behind its tool boundary", state.Phase, "applied")
	f.secretFiles(state)
	for role, selected := range map[string]RoleSelection{"core": f.input.RuntimeRoles.Core, "ingestion": f.input.RuntimeRoles.Ingestion} {
		env, _ := f.environment(role + ".env")
		if role == "core" && env["ASPM_S3_PREPARE_READINESS"] != "true" {
			t.Error("managed core.env omitted literal ASPM_S3_PREPARE_READINESS=true")
		}
		if role == "ingestion" {
			if _, present := env["ASPM_S3_PREPARE_READINESS"]; present {
				t.Error("ingestion.env must receive no preparation flag")
			}
			if _, present := env["ASPM_BOOTSTRAP_TOKEN"]; present {
				t.Error("ingestion gained a core bootstrap credential")
			}
			equal(t, "ingestion normalized scope unchanged", env["ASPM_S3_NORMALIZED_PREFIX"], selected.NormalizedPrefix)
		}
		key := f.options.RoleKeys.Core
		if role == "ingestion" {
			key = f.options.RoleKeys.Ingestion
		}
		equal(t, role+" raw scope unchanged", env["ASPM_S3_PREFIX"], selected.RawPrefix)
		equal(t, role+" readiness key unchanged", env["ASPM_S3_READINESS_KEY"], selected.ReadinessKey)
		equal(t, role+" exact role access bytes unchanged", env["ASPM_S3_ACCESS_KEY"], key.AccessKey)
		equal(t, role+" exact role secret bytes unchanged", env["ASPM_S3_SECRET_KEY"], key.SecretKey)
	}
	reports, _ := f.environment("reports.env")
	if reports["ASPM_DATABASE_URL"] == "" {
		t.Fatal("reports lost its required database credential")
	}
	for name := range reports {
		if strings.HasPrefix(name, "ASPM_S3_") || strings.HasPrefix(name, "AWS_") || name == "ASPM_BOOTSTRAP_TOKEN" {
			t.Errorf("DB-only reports.env gained forbidden %s", name)
		}
	}
	f.noLeaks(state, nil)
}
