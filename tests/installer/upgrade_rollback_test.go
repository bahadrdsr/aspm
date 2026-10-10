package installer

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func setFixtureRelease(f *fixture, release, identity string) {
	f.t.Helper()
	f.manifest.Release = release
	f.manifest.Images["application"] = "aspm:" + release + "@" + digest([]byte(identity))
	section(f.t, f.config, "release")["version"] = release
	f.sign()
}

func TestInstallerV4_UpgradeFailureBeforeActivationAllowsSignedRollback(t *testing.T) {
	f := newFixture(t, "kubernetes")
	initialPlan := f.plan()
	if initialPlan.ChangeKind != "initial-install" || initialPlan.TargetRelease != f.manifest.Release {
		t.Fatal("initial deployment plan omitted its release transition")
	}
	initial := f.execute(initialPlan)
	if initial.Release != f.manifest.Release || initial.RollbackMode != "not-needed" {
		t.Fatal("initial deployment state omitted its current release boundary")
	}
	previousRelease := f.manifest.Release
	previousImage := f.manifest.Images["application"]

	setFixtureRelease(f, "0.1.0-rc.1", "synthetic release candidate application")
	run := f.run
	failBeforeActivation := true
	f.options.Run = func(ctx context.Context, command Command) (CommandResult, error) {
		if failBeforeActivation && command.Tool == "kubectl" && slices.Contains(command.Args, "apply") {
			failBeforeActivation = false
			return CommandResult{ExitCode: 19}, nil
		}
		return run(ctx, command)
	}
	f.reopen()
	upgrade := f.plan()
	if upgrade.ChangeKind != "upgrade" || upgrade.CurrentRelease != previousRelease ||
		upgrade.TargetRelease != f.manifest.Release ||
		upgrade.RollbackPolicy != "activation-before-runtime-or-data-restore" {
		t.Fatal("upgrade plan omitted its current, target, or rollback boundary")
	}
	failed, err := f.app.Execute(f.ctx, f.input, Approval{PlanID: upgrade.ID})
	if !errors.Is(err, ErrCommand) || failed.Phase != "failed" ||
		failed.RollbackMode != "activation-eligible" ||
		failed.PreviousRelease != previousRelease ||
		failed.Release != f.manifest.Release {
		t.Fatal("pre-activation upgrade failure did not retain a bounded rollback checkpoint")
	}

	f.manifest.Release = previousRelease
	f.manifest.Images["application"] = previousImage
	section(t, f.config, "release")["version"] = previousRelease
	f.sign()
	f.input.Operation = "rollback"
	f.options.Run = f.run
	f.reopen()
	rollback := f.plan()
	if rollback.ChangeKind != "rollback" || rollback.CurrentRelease != failed.Release ||
		rollback.TargetRelease != previousRelease {
		t.Fatal("rollback plan did not bind the failed and previous signed releases")
	}
	restored := f.execute(rollback)
	if restored.Phase != "applied" || restored.Release != previousRelease ||
		restored.BundleDigest != rollback.BundleDigest ||
		restored.PreviousRelease != "" || restored.RollbackMode != "not-needed" {
		t.Fatal("approved activation rollback did not settle on the previous signed release")
	}
}

func TestInstallerV4_ActivationFailureRequiresDataRestore(t *testing.T) {
	f := newFixture(t, "kubernetes")
	f.execute(f.plan())
	previousRelease := f.manifest.Release
	previousImage := f.manifest.Images["application"]

	setFixtureRelease(f, "0.1.0-rc.1", "synthetic activated release candidate")
	f.failUpgrade = true
	upgrade := f.plan()
	failed, err := f.app.Execute(f.ctx, f.input, Approval{PlanID: upgrade.ID})
	if !errors.Is(err, ErrCommand) || failed.RollbackMode != "data-restore-required" ||
		failed.PreviousRelease != previousRelease {
		t.Fatal("activation-boundary failure did not require data restore")
	}
	f.failUpgrade = false
	f.manifest.Release = previousRelease
	f.manifest.Images["application"] = previousImage
	section(t, f.config, "release")["version"] = previousRelease
	f.sign()
	f.input.Operation = "rollback"
	f.reopen()
	before := f.mutationCount()
	plan, err := f.app.Plan(f.ctx, f.input)
	if !errors.Is(err, ErrRestoreRequired) || plan.ID != "" {
		t.Fatal("unsafe in-place rollback returned a usable plan")
	}
	if f.mutationCount() != before {
		t.Fatal("rejected data rollback mutated the selected target")
	}
}

func TestInstallerV4_MigratesV2CheckpointWithoutNativeMutation(t *testing.T) {
	f := newFixture(t, "kubernetes")
	installed := f.execute(f.plan())
	path := filepath.Join("var", "lib", "aspm", "installer", "state.json")
	data, err := f.files.ReadFile(path)
	ok(t, "read current installer checkpoint", err)
	var checkpoint map[string]any
	ok(t, "decode current installer checkpoint", json.Unmarshal(data, &checkpoint))
	checkpoint["version"] = float64(2)
	for _, name := range []string{
		"release", "bundleDigest", "previousRelease", "previousBundleDigest",
		"previousConfigId", "callerIdentity",
	} {
		delete(checkpoint, name)
	}
	state := checkpoint["state"].(map[string]any)
	for _, name := range []string{
		"release", "bundleDigest", "previousRelease", "previousBundleDigest",
		"changeKind", "rollbackMode",
	} {
		delete(state, name)
	}
	legacy, err := json.Marshal(checkpoint)
	ok(t, "encode v2 installer checkpoint", err)
	ok(t, "write v2 installer checkpoint", f.files.WriteFile(path, legacy, 0600))

	f.reopen()
	before := f.mutationCount()
	plan := f.plan()
	if plan.ChangeKind != "checkpoint-migration" || plan.CurrentRelease != plan.TargetRelease {
		t.Fatal("v2 checkpoint did not require explicit release-lineage migration")
	}
	migrated := f.execute(plan)
	if f.mutationCount() != before || migrated.Phase != "applied" ||
		migrated.Release != plan.TargetRelease ||
		migrated.BundleDigest != plan.BundleDigest ||
		migrated.ChangeKind != "checkpoint-migration" ||
		migrated.RollbackMode != "not-needed" {
		t.Fatal("checkpoint migration changed native resources or omitted release lineage")
	}
	if !slices.Equal(migrated.SecretFiles, installed.SecretFiles) {
		t.Fatal("checkpoint migration changed retained credential ownership")
	}
}

func TestInstallerV4_FailedReapplyResumesItsFailedStep(t *testing.T) {
	f := newFixture(t, "kubernetes")
	plan := f.plan()
	f.execute(plan)
	f.failUpgrade = true
	failed, err := f.app.Execute(f.ctx, f.input, Approval{PlanID: plan.ID})
	if !errors.Is(err, ErrCommand) || failed.Phase != "failed" ||
		failed.FailedStep != "helm-upgrade" || slices.Contains(failed.Completed, failed.FailedStep) {
		t.Fatal("failed reapply did not replace the prior completed-step set")
	}
	f.reopen()
	f.failUpgrade = false
	resumed := f.execute(plan)
	if resumed.Phase != "applied" || !slices.Contains(resumed.Completed, "helm-upgrade") {
		t.Fatal("reapply resume skipped its previously failed activation step")
	}
}

func TestInstallerV4_OperatorDocumentationDefinesRollbackBoundary(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "docs", "m13-upgrade-rollback.md"))
	ok(t, "read upgrade and rollback operator documentation", err)
	text := strings.Join(strings.Fields(strings.ToLower(string(data))), " ")
	for _, required := range []string{
		"technical preview",
		"aspmctl deploy-plan --operation apply",
		"aspmctl apply",
		"aspmctl deploy-plan --operation rollback",
		"aspmctl rollback",
		"activation-eligible",
		"data-restore-required",
		"checkpoint-migration",
		"forward-only",
		"absent schema",
		"empty selected prefixes",
		"do not start an old binary",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("upgrade documentation omitted required boundary %q", required)
		}
	}
	for _, forbidden := range []string{
		"zero downtime",
		"provides automatic rollback",
		"database down-migration is supported",
		"safe to run the old binary",
	} {
		if strings.Contains(text, forbidden) {
			t.Errorf("upgrade documentation made unsupported claim %q", forbidden)
		}
	}
}
