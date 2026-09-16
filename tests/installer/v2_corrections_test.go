package installer

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestInstallerV2_EnvFilesPreserveNativeLiteralBytes(t *testing.T) {
	f := newFixture(t, "linux")
	f.options.RoleKeys = RoleCredentials{
		Core:      RoleCredential{AccessKey: `core"access\segment=value`, SecretKey: `"core-secret\folder=a=b"`},
		Ingestion: RoleCredential{AccessKey: `ingestion'access\segment=value`, SecretKey: `ingestion-secret\folder=="quoted"`},
	}
	f.reopen()
	password := `synthetic-db"password\segment=a=b'`
	bootstrap := `synthetic-bootstrap"token\segment=a=b'`
	for _, value := range []struct{ section, field, name, data string }{
		{"postgres", "credentialRef", "v2-database", password},
		{"bootstrap", "secretRef", "v2-bootstrap", bootstrap},
	} {
		name := filepath.Join("inputs", value.name)
		ok(t, "seed bounded synthetic credential input", f.seed(name, []byte(value.data)))
		section(t, f.config, value.section)[value.field] = object{"kind": "file", "name": filepath.ToSlash(name)}
	}
	f.secret["postgres-password"], f.secret["bootstrap-token"] = password, bootstrap
	state := f.execute(f.plan())
	f.secretFiles(state)
	databaseURL := (&url.URL{Scheme: "postgres", User: url.UserPassword("aspm", password),
		Host: "aspm-postgres:5432", Path: "/aspm", RawQuery: "sslmode=disable"}).String()
	for role, expected := range map[string]map[string]string{
		"core": {"ASPM_DATABASE_URL": databaseURL, "ASPM_SCHEMA": "aspm",
			"ASPM_S3_ACCESS_KEY": f.options.RoleKeys.Core.AccessKey, "ASPM_S3_SECRET_KEY": f.options.RoleKeys.Core.SecretKey,
			"ASPM_BOOTSTRAP_TOKEN": bootstrap},
		"ingestion": {"ASPM_DATABASE_URL": databaseURL, "ASPM_SCHEMA": "aspm",
			"ASPM_S3_ACCESS_KEY": f.options.RoleKeys.Ingestion.AccessKey, "ASPM_S3_SECRET_KEY": f.options.RoleKeys.Ingestion.SecretKey},
		"reports":  {"ASPM_DATABASE_URL": databaseURL, "ASPM_SCHEMA": "aspm"},
		"postgres": {"POSTGRES_DB": "aspm", "POSTGRES_USER": "aspm", "POSTGRES_PASSWORD": password},
	} {
		env, _ := f.environment(role + ".env")
		for name, want := range expected {
			if env[name] != want {
				t.Errorf("%s.env %s is not the exact native env-file value; credential bytes withheld", role, name)
			}
		}
		if role == "reports" {
			for name := range env {
				if strings.HasPrefix(name, "ASPM_S3_") || strings.HasPrefix(name, "AWS_") || name == "ASPM_BOOTSTRAP_TOKEN" {
					t.Error("DB-only reports file gained storage/bootstrap material")
				}
			}
		}
		if role == "ingestion" {
			if _, present := env["ASPM_BOOTSTRAP_TOKEN"]; present {
				t.Error("ingestion file gained core bootstrap material")
			}
		}
	}
	f.noLeaks(state, nil)
}

func TestInstallerV2_UnsignedTemplateCannotEnterApprovedHelmInput(t *testing.T) {
	for _, insertion := range []string{"before-plan", "after-plan"} {
		t.Run(insertion, func(t *testing.T) {
			f := newFixture(t, "kubernetes")
			expected := v2ExpectedChart(f)
			addInertFile := func() {
				ok(t, "add only an inert unlisted fixture template", f.seed(
					filepath.Join("bundle", "deploy", "helm", "aspm", "templates", "unsigned-v2.yaml"),
					[]byte("# Inert unsigned V2 fixture; no template action or resource.\n")))
			}
			if insertion == "before-plan" {
				addInertFile()
			}
			p, err := f.app.Plan(f.ctx, f.input)
			if errors.Is(err, ErrBundle) {
				equal(t, "unlisted bundle rejection precedes infrastructure mutation", f.mutationCount(), 0)
				equal(t, "unlisted bundle rejection precedes credential writes", f.files.writes, 0)
				return
			}
			ok(t, "approve the declared signed chart", err)
			if insertion == "after-plan" {
				addInertFile()
			}
			state, err := f.app.Execute(f.ctx, f.input, Approval{PlanID: p.ID})
			f.noLeaks(state, err)
			if err != nil {
				if !errors.Is(err, ErrBundle) && !errors.Is(err, ErrApproval) {
					t.Fatal("unlisted input failed for an unrelated reason")
				}
				equal(t, "unlisted input rejection precedes native mutation", f.mutationCount(), 0)
				return
			}
			if state.Phase != "applied" || len(f.helmInputs) == 0 {
				t.Fatal("no actual approved chart input reached the command boundary")
			}
			for _, capture := range f.helmInputs {
				v2RequireApprovedChart(t, capture, expected)
			}
		})
	}
}

func TestInstallerV2_SameReleaseBundleReplacementStopsLaterMutation(t *testing.T) {
	f := newFixture(t, "kubernetes")
	p := f.plan()
	run := f.options.Run
	replaced, mutationsAtReplacement := false, 0
	f.options.Run = func(ctx context.Context, command Command) (CommandResult, error) {
		result, err := run(ctx, command)
		if !replaced && command.Tool == "kubectl" && slices.Contains(command.Args, "apply") && err == nil && result.ExitCode == 0 {
			// Scheduling mutation of test-owned input only; command result is unchanged.
			path := filepath.Join("bundle", "deploy", "helm", "aspm", "values.yaml")
			raw, readErr := f.files.root.ReadFile(path)
			ok(t, "read owned approved input before replacement", readErr)
			raw = append(bytes.Clone(raw), []byte("\n# Different, validly signed same-release bundle.\n")...)
			ok(t, "replace owned bundle input between real execution steps", f.seed(path, raw))
			f.manifest.Files["deploy/helm/aspm/values.yaml"] = digest(raw)
			f.sign()
			replaced, mutationsAtReplacement = true, f.mutationCount()
		}
		return result, err
	}
	f.reopen()
	state, err := f.app.Execute(f.ctx, f.input, Approval{PlanID: p.ID})
	if !replaced {
		t.Fatal("replacement interleaving did not follow a completed real execution step")
	}
	if !errors.Is(err, ErrApproval) && !errors.Is(err, ErrBundle) {
		t.Error("new signed same-release bundle identity was accepted under the old approval")
	}
	if state.Phase == "applied" || f.mutationCount() != mutationsAtReplacement {
		t.Errorf("execution mutated after approved bundle replacement: later native mutations=%d", f.mutationCount()-mutationsAtReplacement)
	}
	if len(f.helmInputs) != 0 {
		t.Error("replaced bundle reached a later Helm mutation")
	}
	f.noLeaks(state, err)
}

func TestInstallerV2_LinuxUninstallDeactivatesOwnedDefinitionsButKeepsData(t *testing.T) {
	f := newFixture(t, "linux")
	unrelated := filepath.Join("etc", "containers", "systemd", "unrelated-operator.container")
	unrelatedBytes := []byte("[Container]\nImage=operator-owned-placeholder\n[Install]\nWantedBy=multi-user.target\n")
	ok(t, "seed unrelated owned-fixture unit", f.seed(unrelated, unrelatedBytes))
	retained := map[string][]byte{
		filepath.Join("var", "lib", "containers", "storage", "volumes", "aspm-postgres", "_data", "sentinel"): []byte("synthetic actual database-volume material"),
		filepath.Join("var", "lib", "containers", "storage", "volumes", "aspm-storage", "_data", "sentinel"):  []byte("synthetic actual evidence-volume material"),
	}
	for path, data := range retained {
		ok(t, "seed only owned volume material", f.seed(path, data))
	}
	installed := f.execute(f.plan())
	credentials := f.secretFiles(installed)
	f.input.Operation = "uninstall"
	p := f.plan()
	first := len(f.calls)
	removed := f.execute(p)
	equal(t, "ordinary uninstall records its terminal state", removed.Phase, "uninstalled")
	reloaded := false
	for _, command := range f.calls[first:] {
		reloaded = reloaded || (command.Tool == "systemctl" && slices.Contains(command.Args, "daemon-reload"))
	}
	if !reloaded {
		t.Error("Linux uninstall stopped services without reloading deactivated owned definitions")
	}
	for _, name := range []string{"aspm-core.container", "aspm-ingestion@.container", "aspm-reports.container",
		"aspm-postgres.container", "aspm-storage.container"} {
		data, err := f.files.root.ReadFile(filepath.Join("etc", "containers", "systemd", name))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		ok(t, "inspect retained owned activation definition", err)
		for _, line := range strings.Split(string(data), "\n") {
			line = strings.TrimSpace(line)
			if strings.HasPrefix(line, "WantedBy=") || strings.HasPrefix(line, "RequiredBy=") {
				if _, value, _ := strings.Cut(line, "="); strings.TrimSpace(value) != "" {
					t.Errorf("uninstalled owned Quadlet %s still enables automatic activation", name)
				}
			}
		}
	}
	got, err := f.files.root.ReadFile(unrelated)
	ok(t, "read unrelated unit after uninstall", err)
	equal(t, "unrelated unit byte preservation", got, unrelatedBytes)
	for path, data := range retained {
		got, err := f.files.root.ReadFile(path)
		ok(t, "read retained actual volume material", err)
		equal(t, "volume bytes survive non-destructive uninstall", got, data)
	}
	equal(t, "all private credential files survive", f.secretFiles(removed), credentials)
}

func TestInstallerV2_ReapprovedCallerChangesPreserveStableDeploymentOwnership(t *testing.T) {
	for _, change := range []string{"format-only", "unused-context", "renewed-auth", "different-cluster"} {
		t.Run(change, func(t *testing.T) {
			f := newFixture(t, "kubernetes")
			p := f.plan()
			installed := f.execute(p)
			credentials := f.secretFiles(installed)
			raw, err := f.files.root.ReadFile(f.options.Kubeconfig)
			ok(t, "read test-owned caller input", err)
			if change == "different-cluster" {
				f.clusterUID = "33333333-3333-4333-8333-333333333333"
			} else if change == "format-only" {
				var formatted bytes.Buffer
				ok(t, "reformat equivalent synthetic kubeconfig", json.Indent(&formatted, raw, "", "    "))
				raw = append(formatted.Bytes(), '\n')
			} else {
				var config object
				ok(t, "decode synthetic caller input", json.Unmarshal(raw, &config))
				if change == "unused-context" {
					config["clusters"] = append(config["clusters"].([]any), object{"name": "unused", "cluster": object{"server": "https://unused.example.invalid"}})
					config["contexts"] = append(config["contexts"].([]any), object{"name": "unused", "context": object{"cluster": "unused", "user": "unused"}})
					config["users"] = append(config["users"].([]any), object{"name": "unused", "user": object{"token": "synthetic-unused-caller-token"}})
				} else {
					config["users"].([]any)[0].(map[string]any)["user"] = object{"token": "synthetic-renewed-caller-token-not-real"}
				}
				raw = encoded(t, config)
			}
			ok(t, "supply explicitly changed caller input", f.seed(f.options.Kubeconfig, raw))
			f.reopen()
			fresh := f.plan()
			if change == "different-cluster" {
				f.rejected(f.input, p.ID, ErrApproval)
				f.rejected(f.input, fresh.ID, ErrApproval)
				equal(t, "other cluster cannot alter existing credentials", f.secretFiles(installed), credentials)
				return
			}
			if fresh.TargetFingerprint != p.TargetFingerprint {
				t.Error("same inspected cluster ownership changed with caller serialization/context/auth input")
			}
			if change == "renewed-auth" {
				if fresh.ID == p.ID {
					t.Error("renewed caller authentication did not invalidate stale approval")
				}
				f.rejected(f.input, p.ID, ErrApproval)
			}
			state, err := f.app.Execute(f.ctx, f.input, Approval{PlanID: fresh.ID})
			if err != nil || state.Phase != "applied" {
				t.Errorf("freshly approved same-cluster %s reapply is blocked (%T; details withheld)", change, err)
			} else {
				equal(t, "new approval is recorded without changing deployment ownership", state.PlanID, fresh.ID)
			}
			equal(t, "caller update does not rotate any application credential", f.secretFiles(installed), credentials)
			f.noLeaks(state, err)
		})
	}
}
