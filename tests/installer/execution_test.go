package installer

import (
	"bytes"
	"crypto/ed25519"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func section(t *testing.T, value object, name string) object {
	t.Helper()
	result, valid := value[name].(map[string]any)
	if !valid {
		t.Fatalf("missing configuration object %s", name)
	}
	return result
}

func TestM03Execution_PlanAndDryRunAreInspectedButReadOnly(t *testing.T) {
	f := newFixture(t, "kubernetes")
	for _, name := range []string{"bootstrap", "postgres", "objectStore"} {
		field := "credentialRef"
		if name == "bootstrap" {
			field = "secretRef"
		}
		path := "inputs/never-read-" + name
		ok(t, "supply opaque synthetic credential reference", f.seed(filepath.FromSlash(path), []byte("synthetic-protected-input-not-a-real-credential")))
		section(t, f.config, name)[field] = object{"kind": "file", "name": path}
	}
	p := f.plan()
	equal(t, "existing configuration preview hash", p.ConfigID, "config-"+digest(f.input.Configuration))
	raw, err := f.files.root.ReadFile(filepath.Join("bundle", "manifest.json"))
	ok(t, "read fixture manifest bytes", err)
	equal(t, "exact signed bundle identity", p.BundleDigest, digest(raw))
	equal(t, "exact pinned image identities", p.Images, f.manifest.Images)
	equal(t, "selected nonsecret runtime role configuration", p.RuntimeRoles, f.input.RuntimeRoles)
	equal(t, "signed trust status", p.Trust, "signed")
	equal(t, "approved operation", p.Operation, "apply")
	if p.ID == "" || p.ID == p.ConfigID || p.TargetFingerprint == "" {
		t.Fatal("configuration preview ID is not inspected deployment approval")
	}
	equal(t, "stable inspected approval", f.plan().ID, p.ID)
	inspected := false
	for _, call := range f.calls {
		inspected = inspected || (call.Tool == "kubectl" && slices.Contains(call.Args, "get") && slices.Contains(call.Args, "kube-system"))
	}
	if !inspected {
		t.Fatal("Kubernetes approval did not inspect the selected target")
	}
	state, err := f.app.Execute(f.ctx, f.input, Approval{DryRun: true})
	ok(t, "dry-run without mutation approval", err)
	equal(t, "dry-run is not deployment success", state.Phase, "planned")
	equal(t, "plan/dry-run infrastructure mutations", f.mutationCount(), 0)
	equal(t, "plan/dry-run file or secret writes", f.files.writes, 0)
	for _, path := range f.files.reads {
		if strings.HasPrefix(path, "inputs/") {
			t.Fatal("plan/dry-run resolved a secret reference")
		}
	}
}

func TestM03Execution_ChangedApprovalInputsRejectBeforeMutation(t *testing.T) {
	f := newFixture(t, "kubernetes")
	p := f.plan()
	f.rejected(f.input, "", ErrApproval)
	f.rejected(f.input, p.ConfigID, ErrApproval)
	access := section(t, f.config, "access")
	originalURL := access["baseURL"]
	access["baseURL"] = "https://another.example.invalid"
	f.input.Configuration = encoded(t, f.config)
	f.rejected(f.input, p.ID, ErrApproval)
	if f.plan().ID == p.ID {
		t.Fatal("changed configuration reused approval")
	}
	access["baseURL"] = originalURL
	equal(t, "original config approval restored", f.plan().ID, p.ID)
	image := f.manifest.Images["application"]
	f.manifest.Images["application"] = "aspm:0.1.0-dev@" + digest([]byte("different synthetic OCI identity"))
	f.sign()
	f.rejected(f.input, p.ID, ErrApproval)
	if f.plan().ID == p.ID {
		t.Fatal("changed signed artifact/image identity reused approval")
	}
	f.manifest.Images["application"] = image
	f.sign()
	equal(t, "original signed bundle approval restored", f.plan().ID, p.ID)
	roles := f.input.RuntimeRoles
	f.input.RuntimeRoles.Core.RawPrefix = "raw/another-selected-scope/"
	f.input.RuntimeRoles.Core.ReadinessKey = "raw/another-selected-scope/core-ready.txt"
	f.input.RuntimeRoles.Ingestion.RawPrefix = f.input.RuntimeRoles.Core.RawPrefix
	f.input.RuntimeRoles.Ingestion.ReadinessKey = "raw/another-selected-scope/ingestion-ready.txt"
	f.input.RuntimeRoles.Ingestion.NormalizedPrefix = "normalized/another-selected-scope/"
	f.rejected(f.input, p.ID, ErrApproval)
	if f.plan().ID == p.ID {
		t.Fatal("changed selected role scopes reused deployment approval")
	}
	f.input.RuntimeRoles = roles
	equal(t, "original role selection approval restored", f.plan().ID, p.ID)
	keys := f.options.RoleKeys
	f.options.RoleKeys.Ingestion.SecretKey += "-changed"
	f.reopen()
	f.rejected(f.input, p.ID, ErrApproval)
	if f.plan().ID == p.ID {
		t.Fatal("changed role credential identity reused deployment approval")
	}
	f.options.RoleKeys = keys
	f.reopen()
	equal(t, "original role credentials approval restored", f.plan().ID, p.ID)
	f.clusterUID = "22222222-2222-4222-8222-222222222222"
	f.rejected(f.input, p.ID, ErrApproval)
	next := f.plan()
	if next.ID == p.ID || next.TargetFingerprint == p.TargetFingerprint {
		t.Fatal("changed inspected cluster identity reused approval")
	}
}

func TestM03Execution_RequiredCredentialsAndIndependentTrustFailBeforeTools(t *testing.T) {
	f := newFixture(t, "kubernetes")
	p := f.plan()
	f.roleInputsRejected(p)
	file := filepath.Join("bundle", "deploy", "helm", "aspm", "values.yaml")
	original, err := f.files.root.ReadFile(file)
	ok(t, "read original owned chart fixture", err)
	ok(t, "alter only the owned bundle copy", f.seed(file, append(bytes.Clone(original), []byte("\n# changed fixture\n")...)))
	f.rejectedBeforeTools(f.input, p.ID, ErrBundle)
	ok(t, "restore owned chart fixture", f.seed(file, original))
	ok(t, "damage only the owned signature", f.seed(filepath.Join("bundle", "manifest.sig"), []byte("invalid synthetic signature")))
	f.rejectedBeforeTools(f.input, p.ID, ErrBundle)
	f.sign()
	ok(t, "remove owned signature", f.files.root.Remove(filepath.Join("bundle", "manifest.sig")))
	f.rejectedBeforeTools(f.input, p.ID, ErrBundle)
	f.signingKey = ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x62}, ed25519.SeedSize))
	untrusted := f.signingKey.Public().(ed25519.PublicKey)
	ok(t, "supply untrusted bundle-local public key", f.seed(filepath.Join("bundle", "public.key"), untrusted))
	f.manifest.Files["public.key"] = digest(untrusted)
	f.sign()
	f.rejectedBeforeTools(f.input, p.ID, ErrBundle)
	f.options.TrustedKey = nil
	f.rejectedOptionsBeforeTools(p.ID, ErrBundle)
	f.signingKey = ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x31}, ed25519.SeedSize))
	f.options.TrustedKey = bytes.Clone(f.signingKey.Public().(ed25519.PublicKey))
	f.sign()
	if f.app != nil {
		ok(t, "close rejected untrusted instance", f.app.Close())
	}
	f.app = nil
	f.open()
	p = f.plan()
	trusted := bytes.Clone(f.options.TrustedKey)
	f.options.TrustedKey = nil
	f.rejectedOptionsBeforeTools(p.ID, ErrBundle)
	f.options.TrustedKey = trusted
	if f.app != nil {
		ok(t, "close missing-key instance", f.app.Close())
	}
	f.app = nil
	f.open()
	for _, name := range []string{"bootstrap", "postgres", "objectStore"} {
		key := "credentialRef"
		if name == "bootstrap" {
			key = "secretRef"
		}
		parent := section(t, f.config, name)
		original := parent[key]
		path := filepath.Join("inputs", "required-"+name)
		ok(t, "seed required caller reference before approval", f.seed(path, []byte("synthetic-protected-input-not-a-real-credential")))
		parent[key] = object{"kind": "file", "name": filepath.ToSlash(path)}
		withReference := f.plan()
		ok(t, "remove caller reference after approval", f.files.root.Remove(path))
		f.rejectedBeforeTools(f.input, withReference.ID, ErrCredential)
		parent[key] = original
		p = f.plan()
	}
	callerPath := f.options.Kubeconfig
	callerBytes, err := f.files.root.ReadFile(callerPath)
	ok(t, "retain synthetic explicit caller fixture", err)
	ok(t, "remove caller credential after approval", f.files.root.Remove(callerPath))
	f.rejectedBeforeTools(f.input, p.ID, ErrCredential)
	ok(t, "seed empty caller credential", f.seed(callerPath, nil))
	f.rejectedBeforeTools(f.input, p.ID, ErrCredential)
	var noAuthentication object
	ok(t, "decode synthetic kubeconfig", json.Unmarshal(callerBytes, &noAuthentication))
	noAuthentication["users"] = []any{object{"name": "operator", "user": object{}}}
	ok(t, "remove caller authentication without removing config", f.seed(callerPath, encoded(t, noAuthentication)))
	f.rejectedBeforeTools(f.input, p.ID, ErrCredential)
	ok(t, "restore explicit synthetic kubeconfig", f.seed(callerPath, callerBytes))
	t.Setenv("KUBECONFIG", filepath.Join(f.options.Root, callerPath))
	f.options.Kubeconfig = ""
	f.rejectedOptionsBeforeTools(p.ID, ErrCredential)
	equal(t, "trust failures caused no infrastructure mutations", f.mutationCount(), 0)
	equal(t, "trust failures caused no state or credential writes", f.files.writes, 0)
}

func TestM03Execution_KubernetesUsesOwnedChartAndProtectedSecretChannels(t *testing.T) {
	f := newFixture(t, "kubernetes")
	section(t, section(t, f.config, "services"), "core-api")["replicas"] = float64(2)
	p := f.plan()
	state := f.execute(p)
	equal(t, "command-complete state", state.Phase, "applied")
	equal(t, "state binds deployment approval", state.PlanID, p.ID)
	equal(t, "state retains signed trust", state.Trust, "signed")
	f.secretFiles(state)
	for _, key := range []string{"postgres-password", "database-url", "bootstrap-token"} {
		if f.secret[key] == "" {
			t.Fatalf("production Secret stdin is missing the existing chart key %s", key)
		}
	}
	if f.secretName == "" {
		t.Fatal("no named Secret was submitted through protected stdin")
	}
	for _, key := range []string{"s3-access-key", "s3-secret-key"} {
		if _, present := f.secret[key]; present {
			t.Fatal("common database/control-plane Secret still supplies a global runtime S3 fallback")
		}
	}
	f.roleSecret(f.input.RuntimeRoles.Core, f.options.RoleKeys.Core)
	f.roleSecret(f.input.RuntimeRoles.Ingestion, f.options.RoleKeys.Ingestion)
	var upgrade *Command
	for i := range f.calls {
		if f.calls[i].Tool == "helm" && slices.Contains(f.calls[i].Args, "upgrade") {
			upgrade = &f.calls[i]
		}
	}
	if upgrade == nil || !slices.Contains(upgrade.Args, "--install") || !slices.Contains(upgrade.Args, "aspm") {
		t.Fatal("Kubernetes apply must use idempotent Helm upgrade --install for release aspm")
	}
	if len(f.helmInputs) == 0 {
		t.Fatal("no actual Helm chart input was captured")
	}
	v2RequireApprovedChart(t, f.helmInputs[len(f.helmInputs)-1], v2ExpectedChart(f))
	timeout, err := time.ParseDuration(flag(upgrade.Args, "--timeout"))
	wait := slices.Contains(upgrade.Args, "--wait") || slices.Contains([]string{"true", "legacy", "watcher"}, flag(upgrade.Args, "--wait"))
	if err != nil || timeout <= 0 || timeout > 10*time.Minute || !wait {
		t.Fatal("Helm apply must wait with an explicit bounded timeout")
	}
	valuesPath := flag(upgrade.Args, "--values", "-f")
	var raw []byte
	if valuesPath == "-" {
		raw = upgrade.Stdin
	} else {
		relative, err := filepath.Rel(f.options.Root, valuesPath)
		ok(t, "resolve sandbox Helm values path", err)
		raw, err = f.files.root.ReadFile(relative)
		ok(t, "read actual production-rendered Helm values", err)
	}
	var values object
	ok(t, "decode production-rendered JSON values", json.Unmarshal(raw, &values))
	equal(t, "existing chart Secret reference", values["existingSecret"], f.secretName)
	equal(t, "M00 service count reaches Helm", section(t, values, "core")["replicas"], float64(2))
	equal(t, "configured origin reaches Helm", values["publicOrigin"], section(t, f.config, "access")["baseURL"])
	for name, expected := range map[string]RoleSelection{"core": f.input.RuntimeRoles.Core, "ingestion": f.input.RuntimeRoles.Ingestion} {
		role := section(t, values, name)
		selection := section(t, role, "s3Secret")
		equal(t, name+" selected Secret", selection["name"], expected.S3Secret.Name)
		equal(t, name+" selected access-key field", selection["accessKeyKey"], expected.S3Secret.AccessKeyKey)
		equal(t, name+" selected secret-key field", selection["secretKeyKey"], expected.S3Secret.SecretKeyKey)
		equal(t, name+" selected raw scope", role["rawPrefix"], expected.RawPrefix)
		equal(t, name+" selected readiness object", role["readinessKey"], expected.ReadinessKey)
	}
	equal(t, "ingestion selected normalized scope", section(t, values, "ingestion")["normalizedPrefix"], f.input.RuntimeRoles.Ingestion.NormalizedPrefix)
	reports := section(t, values, "reports")
	equal(t, "ingestion remains enabled", section(t, values, "ingestion")["replicas"], float64(1))
	equal(t, "DB-only reports remains enabled", reports["replicas"], float64(1))
	for _, forbidden := range []string{"s3Secret", "rawPrefix", "readinessKey", "normalizedPrefix", "bootstrapToken"} {
		if _, present := reports[forbidden]; present {
			t.Fatal("reports values contain non-database credential configuration")
		}
	}
	image := section(t, values, "image")
	equal(t, "exact application OCI reference", fmt.Sprintf("%s:%s", image["repository"], image["tag"]), f.manifest.Images["application"])
	equal(t, "exact PostgreSQL OCI reference", section(t, values, "database")["image"], f.manifest.Images["postgres"])
	equal(t, "exact storage OCI reference", section(t, values, "storage")["image"], f.manifest.Images["storage"])
	for _, data := range f.secrets {
		for _, value := range data {
			if bytes.Contains(raw, []byte(value)) {
				t.Fatal("Helm values must contain Secret references, not credential material")
			}
		}
	}
	f.noLeaks(state, nil)
}

func TestM03Execution_FailureResumeAndReapplyRetainCredentialsAndState(t *testing.T) {
	f := newFixture(t, "kubernetes")
	p := f.plan()
	f.failUpgrade = true
	failed, err := f.app.Execute(f.ctx, f.input, Approval{PlanID: p.ID})
	if !errors.Is(err, ErrCommand) || failed.Phase != "failed" || failed.FailedStep == "" || failed.FailureCode != "command-failed" {
		t.Fatal("external command failure must remain an explicit failed step/state")
	}
	if len(failed.Completed) == 0 || slices.Contains(failed.Completed, failed.FailedStep) {
		t.Fatal("failure must retain prior completed steps but not complete the failed step")
	}
	f.noLeaks(failed, err)
	credentials := f.secretFiles(failed)
	values := encoded(t, f.secrets)
	f.reopen()
	persisted, err := f.app.Status(f.ctx)
	ok(t, "read persisted failed execution", err)
	equal(t, "failure survives reopen", persisted.Phase, "failed")
	equal(t, "failed approval survives reopen", persisted.PlanID, p.ID)
	f.failUpgrade = false
	resumed := f.execute(p)
	equal(t, "resumed execution completes", resumed.Phase, "applied")
	equal(t, "resume retains credential files", f.secretFiles(resumed), credentials)
	equal(t, "resume retains all per-role published credentials", encoded(t, f.secrets), values)
	for _, step := range failed.Completed {
		if !slices.Contains(resumed.Completed, step) {
			t.Fatal("resume erased an earlier completed checkpoint")
		}
	}
	upgrades := 0
	for _, call := range f.calls {
		if call.Tool == "helm" && slices.Contains(call.Args, "upgrade") {
			upgrades++
		}
	}
	if upgrades < 2 {
		t.Fatal("resume skipped the previously failed Helm command")
	}
	f.reopen()
	reapplied := f.execute(p)
	equal(t, "reapply is still the same installation", reapplied.PlanID, p.ID)
	equal(t, "reapply retains credentials", f.secretFiles(reapplied), credentials)
	equal(t, "reapply retains all per-role published values", encoded(t, f.secrets), values)
	seen := map[string]bool{}
	for _, step := range reapplied.Completed {
		if seen[step] {
			t.Fatal("idempotent reapply duplicated a completed checkpoint")
		}
		seen[step] = true
	}
}

func TestM03Execution_UninstallPreservesDataUnlessSeparatelyApproved(t *testing.T) {
	f := newFixture(t, "kubernetes")
	apply := f.plan()
	installed := f.execute(apply)
	credentials := f.secretFiles(installed)
	f.input.Operation = "uninstall"
	remove := f.plan()
	if remove.ID == apply.ID {
		t.Fatal("apply approval also authorized uninstall")
	}
	f.rejected(f.input, apply.ID, ErrApproval)
	start := len(f.calls)
	removed := f.execute(remove)
	equal(t, "uninstall state", removed.Phase, "uninstalled")
	equal(t, "ordinary uninstall retains credentials", f.secretFiles(removed), credentials)
	for _, call := range f.calls[start:] {
		if call.Tool == "kubectl" && slices.Contains(call.Args, "delete") {
			t.Fatal("ordinary uninstall must retain PVCs, namespace and Secret")
		}
	}
	f.input.DeleteData = true
	purge := f.plan()
	if purge.ID == remove.ID || !purge.DeleteData {
		t.Fatal("destructive cleanup needs a distinct explicit deployment approval")
	}
	f.rejected(f.input, remove.ID, ErrApproval)
	start = len(f.calls)
	f.execute(purge)
	deleted := map[string]bool{}
	for _, call := range f.calls[start:] {
		if call.Tool != "kubectl" || !slices.Contains(call.Args, "delete") {
			continue
		}
		equal(t, "destructive namespace is explicit", flag(call.Args, "--namespace", "-n"), "aspm-installer-test")
		for _, forbidden := range []string{"--all", "namespace", "ns", "pv", "unrelated-claim"} {
			if slices.Contains(call.Args, forbidden) {
				t.Fatal("cleanup exceeded specifically owned namespaced data")
			}
		}
		if slices.Contains(call.Args, "pvc") {
			for _, name := range []string{"data-aspm-aspm-postgres-0", "data-aspm-aspm-storage-0"} {
				if slices.Contains(call.Args, name) {
					deleted[name] = true
				}
			}
		}
	}
	equal(t, "explicit cleanup names the two owned PVCs", len(deleted), 2)
}

func TestM03Execution_LocalPrivilegedLinuxUsesExistingQuadletsWithoutElevation(t *testing.T) {
	f := newFixture(t, "linux")
	p := f.plan()
	if p.TargetFingerprint == "" {
		t.Fatal("Linux plan did not inspect local machine identity")
	}
	ok(t, "change only sandbox machine identity", f.seed(filepath.Join("etc", "machine-id"), []byte("fedcba9876543210fedcba9876543210\n")))
	f.rejected(f.input, p.ID, ErrApproval)
	ok(t, "restore sandbox machine identity", f.seed(filepath.Join("etc", "machine-id"), []byte("0123456789abcdef0123456789abcdef\n")))
	state := f.execute(p)
	equal(t, "declared privileged Linux apply", state.Phase, "applied")
	f.secretFiles(state)
	for name, image := range map[string]string{"aspm-core.container": "application", "aspm-ingestion@.container": "application", "aspm-reports.container": "application",
		"aspm-postgres.container": "postgres", "aspm-storage.container": "storage"} {
		data, err := f.files.root.ReadFile(filepath.Join("etc", "containers", "systemd", name))
		ok(t, "read production-rendered Quadlet", err)
		if !strings.Contains(string(data), "Image="+f.manifest.Images[image]) || !strings.Contains(string(data), "[Container]") {
			t.Fatal("Quadlet renderer did not retain the existing unit with the approved OCI identity")
		}
		source, err := f.files.root.ReadFile(filepath.Join("bundle", "deploy", "quadlet", name))
		ok(t, "read original signed Quadlet", err)
		role := map[string]string{"aspm-core.container": "core", "aspm-ingestion@.container": "ingestion", "aspm-reports.container": "reports"}[name]
		for _, line := range strings.Split(string(source), "\n") {
			line = strings.TrimSpace(line)
			if role != "" {
				if strings.HasPrefix(line, "EnvironmentFile=") {
					line = "EnvironmentFile=/etc/aspm/" + role + ".env"
				}
				selected := f.input.RuntimeRoles.Core
				if role == "ingestion" {
					selected = f.input.RuntimeRoles.Ingestion
				}
				for key, value := range map[string]string{"ASPM_S3_PREFIX": selected.RawPrefix, "ASPM_S3_READINESS_KEY": selected.ReadinessKey,
					"ASPM_S3_NORMALIZED_PREFIX": selected.NormalizedPrefix} {
					if strings.HasPrefix(line, "Environment="+key+"=") {
						line = "Environment=" + key + "=" + value
					}
				}
			}
			if line != "" && !strings.HasPrefix(line, "Image=") && !strings.Contains(string(data), line) {
				t.Fatal("default Quadlet apply discarded existing service, volume or isolation settings")
			}
		}
		if role != "" {
			if strings.Contains(string(data), "runtime.env") || strings.Contains(string(data), "EnvironmentHost=true") ||
				strings.Contains(string(data), "PodmanArgs=") || strings.Contains(string(data), "Environment=ASPM_S3_ACCESS_KEY=") ||
				strings.Contains(string(data), "Environment=ASPM_S3_SECRET_KEY=") || strings.Contains(string(data), "Environment=AWS_") {
				t.Fatal("application Quadlet bypasses protected per-role environment files")
			}
			if role == "reports" && (strings.Contains(string(data), "ASPM_S3_") || strings.Contains(string(data), "ASPM_BOOTSTRAP_TOKEN")) {
				t.Fatal("reports Quadlet carries storage or bootstrap configuration")
			}
		}
	}
	for _, name := range []string{"aspm.network", "aspm-postgres.volume", "aspm-storage.volume"} {
		got, err := f.files.root.ReadFile(filepath.Join("etc", "containers", "systemd", name))
		ok(t, "read retained Quadlet network/volume artifact", err)
		equal(t, "existing Quadlet artifact retained", digest(got), f.manifest.Files["deploy/quadlet/"+name])
	}
	core, coreBytes := f.environment("core.env")
	ingestion, ingestionBytes := f.environment("ingestion.env")
	reports, reportsBytes := f.environment("reports.env")
	postgres, _ := f.environment("postgres.env")
	if core["ASPM_DATABASE_URL"] == "" || core["ASPM_BOOTSTRAP_TOKEN"] == "" || postgres["POSTGRES_PASSWORD"] == "" {
		t.Fatal("database/core bootstrap credentials were lost during role separation")
	}
	equal(t, "ingestion retains its database configuration", ingestion["ASPM_DATABASE_URL"], core["ASPM_DATABASE_URL"])
	equal(t, "reports retains only database credentials", reports["ASPM_DATABASE_URL"], core["ASPM_DATABASE_URL"])
	for name, selected := range map[string]RoleSelection{"core": f.input.RuntimeRoles.Core, "ingestion": f.input.RuntimeRoles.Ingestion} {
		env, key := core, f.options.RoleKeys.Core
		if name == "ingestion" {
			env, key = ingestion, f.options.RoleKeys.Ingestion
		}
		equal(t, name+" runtime role access key", env["ASPM_S3_ACCESS_KEY"], key.AccessKey)
		equal(t, name+" runtime role secret key", env["ASPM_S3_SECRET_KEY"], key.SecretKey)
		equal(t, name+" runtime raw scope", env["ASPM_S3_PREFIX"], selected.RawPrefix)
		equal(t, name+" runtime readiness object", env["ASPM_S3_READINESS_KEY"], selected.ReadinessKey)
		f.secrets[name] = map[string]string{"access": key.AccessKey, "secret": key.SecretKey, "database-url": env["ASPM_DATABASE_URL"]}
	}
	equal(t, "ingestion normalized scope", ingestion["ASPM_S3_NORMALIZED_PREFIX"], f.input.RuntimeRoles.Ingestion.NormalizedPrefix)
	if _, present := ingestion["ASPM_BOOTSTRAP_TOKEN"]; present {
		t.Fatal("ingestion received a core-only bootstrap credential")
	}
	for name := range reports {
		if !slices.Contains([]string{"ASPM_DATABASE_URL", "ASPM_SCHEMA", "ASPM_DB_MAX_CONNECTIONS", "ASPM_LISTEN", "ASPM_WORKER_ID", "ASPM_WORKSPACE"}, name) {
			t.Fatal("reports.env contains non-database/non-identity configuration")
		}
	}
	policy, err := f.files.root.ReadFile(filepath.Join("etc", "aspm", "s3.json"))
	ok(t, "read separate private operator policy", err)
	f.secrets["operator-policy"] = map[string]string{"s3.json": string(policy)}
	f.secret = map[string]string{"postgres-password": postgres["POSTGRES_PASSWORD"], "bootstrap-token": core["ASPM_BOOTSTRAP_TOKEN"]}
	for _, value := range f.operatorKeys() {
		if bytes.Contains(coreBytes, []byte(value)) || bytes.Contains(ingestionBytes, []byte(value)) || bytes.Contains(reportsBytes, []byte(value)) {
			t.Fatal("operator storage-policy credential was copied into an application role file")
		}
	}
	for _, key := range []RoleCredential{f.options.RoleKeys.Core, f.options.RoleKeys.Ingestion} {
		if bytes.Contains(reportsBytes, []byte(key.AccessKey)) || bytes.Contains(reportsBytes, []byte(key.SecretKey)) {
			t.Fatal("DB-only reports.env received an application storage credential")
		}
	}
	if bytes.Contains(coreBytes, []byte(f.options.RoleKeys.Ingestion.AccessKey)) ||
		bytes.Contains(coreBytes, []byte(f.options.RoleKeys.Ingestion.SecretKey)) ||
		bytes.Contains(ingestionBytes, []byte(f.options.RoleKeys.Core.AccessKey)) ||
		bytes.Contains(ingestionBytes, []byte(f.options.RoleKeys.Core.SecretKey)) {
		t.Fatal("core and ingestion environment files share another role's credential")
	}
	f.noLeaks(state, nil)
	started, reloaded := map[string]bool{}, false
	for _, call := range f.calls {
		if call.Tool == "systemctl" && (slices.Contains(call.Args, "start") || slices.Contains(call.Args, "--now")) {
			for _, arg := range call.Args {
				started[arg] = true
			}
		}
		reloaded = reloaded || (call.Tool == "systemctl" && slices.Contains(call.Args, "daemon-reload"))
	}
	for _, name := range []string{"aspm-core.service", "aspm-ingestion@1.service", "aspm-reports.service", "aspm-postgres.service", "aspm-storage.service"} {
		if !started[name] {
			t.Fatal("local apply did not activate every declared service")
		}
	}
	if !reloaded {
		t.Fatal("writing Quadlets alone is not local service activation")
	}
	f.options.LocalHost.EUID = 1000
	f.reopen()
	f.rejected(f.input, p.ID, ErrUnsupported)
	f.options.LocalHost.EUID = 0
	f.options.LocalHost.OS = "windows"
	f.reopen()
	f.rejected(f.input, p.ID, ErrUnsupported)
	f.options.LocalHost.OS = "linux"
	f.reopen()
	f.config["target"] = object{"kind": "linux", "host": "remote.example.invalid"}
	f.input.Configuration = encoded(t, f.config)
	f.rejected(f.input, p.ID, ErrUnsupported)
}

func TestM03Execution_DevelopmentFlagsCannotBypassRequiredSignature(t *testing.T) {
	f := newFixture(t, "kubernetes")
	signed := f.plan()
	equal(t, "existing schema requires signatures", section(t, f.config, "artifacts")["signatureRequired"], true)
	ok(t, "remove signature only in fixture", f.files.root.Remove(filepath.Join("bundle", "manifest.sig")))
	f.rejectedBeforeTools(f.input, signed.ID, ErrBundle)
	f.input.DevelopmentPolicy = "allow-unsigned-development"
	f.rejectedBeforeTools(f.input, signed.ID, ErrBundle, ErrUnsupported)
}
