package execution

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"slices"
	"strconv"
	"time"
)

const (
	statePath    = "var/lib/aspm/installer/state.json"
	stateVersion = 3
)

type stateRecord struct {
	Version              int               `json:"version"`
	TargetFingerprint    string            `json:"targetFingerprint"`
	ConfigID             string            `json:"configId"`
	Release              string            `json:"release,omitempty"`
	BundleDigest         string            `json:"bundleDigest,omitempty"`
	PreviousRelease      string            `json:"previousRelease,omitempty"`
	PreviousBundleDigest string            `json:"previousBundleDigest,omitempty"`
	PreviousConfigID     string            `json:"previousConfigId,omitempty"`
	CallerIdentity       string            `json:"callerIdentity,omitempty"`
	RuntimeRoles         RuntimeRoles      `json:"runtimeRoles"`
	State                State             `json:"state"`
	OwnedUnits           map[string]string `json:"ownedUnits,omitempty"`
}

func integer(value int) string { return strconv.Itoa(value) }

func validChangeKind(value string) bool {
	return slices.Contains([]string{"", "initial-install", "checkpoint-migration", "reapply", "upgrade", "rollback", "uninstall"}, value)
}

func validRollbackMode(value string) bool {
	return slices.Contains([]string{"", "not-needed", "activation-eligible", "data-restore-required"}, value)
}

func syncStateMetadata(record *stateRecord) {
	record.State.Release = record.Release
	record.State.BundleDigest = record.BundleDigest
	record.State.PreviousRelease = record.PreviousRelease
	record.State.PreviousBundleDigest = record.PreviousBundleDigest
}

func (i *installer) loadState(ctx context.Context) (stateRecord, error) {
	data, err := i.read(ctx, statePath, maxInputBytes)
	if errors.Is(err, fs.ErrNotExist) {
		return stateRecord{Version: stateVersion, State: State{Phase: "not-started", Completed: []string{}, SecretFiles: []string{}}}, nil
	}
	if err != nil {
		return stateRecord{}, errors.New("installer checkpoint could not be read")
	}
	var record stateRecord
	if decode(data, &record) != nil ||
		(record.Version != 1 && record.Version != 2 && record.Version != stateVersion) {
		return stateRecord{}, errors.New("installer checkpoint is invalid")
	}
	for _, path := range record.State.SecretFiles {
		if !slices.Contains(credentialPaths, path) {
			return stateRecord{}, errors.New("installer checkpoint contains an unowned secret reference")
		}
	}
	for path, hash := range record.OwnedUnits {
		if !ownedActivationPath(path) || !fileDigest.MatchString(hash) {
			return stateRecord{}, errors.New("installer checkpoint contains an unowned activation definition")
		}
	}
	if (record.Release == "") != (record.BundleDigest == "") ||
		(record.Release != "" && (!releaseVersion.MatchString(record.Release) || !fileDigest.MatchString(record.BundleDigest))) ||
		(record.PreviousRelease == "") != (record.PreviousBundleDigest == "") ||
		(record.PreviousRelease == "") != (record.PreviousConfigID == "") ||
		(record.PreviousRelease != "" && (!releaseVersion.MatchString(record.PreviousRelease) ||
			!fileDigest.MatchString(record.PreviousBundleDigest))) ||
		(record.CallerIdentity != "" && !fileDigest.MatchString(record.CallerIdentity)) ||
		!validChangeKind(record.State.ChangeKind) || !validRollbackMode(record.State.RollbackMode) {
		return stateRecord{}, errors.New("installer checkpoint release lineage is invalid")
	}
	if record.Release == "" {
		if record.State.Release != "" || record.State.BundleDigest != "" ||
			record.State.PreviousRelease != "" || record.State.PreviousBundleDigest != "" {
			return stateRecord{}, errors.New("installer checkpoint release state is invalid")
		}
	} else if record.State.Release != record.Release || record.State.BundleDigest != record.BundleDigest ||
		record.State.PreviousRelease != record.PreviousRelease ||
		record.State.PreviousBundleDigest != record.PreviousBundleDigest {
		return stateRecord{}, errors.New("installer checkpoint release state is inconsistent")
	}
	return record, nil
}

func (i *installer) saveState(ctx context.Context, record stateRecord) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := json.Marshal(record)
	if err != nil {
		return errors.New("installer checkpoint could not be encoded")
	}
	next := statePath + ".next"
	if err := i.files.WriteFile(next, data, 0600); err != nil {
		return errors.New("installer checkpoint could not be staged")
	}
	if err := i.files.Rename(next, statePath); err != nil {
		return errors.New("installer checkpoint could not be published")
	}
	return nil
}

func (i *installer) Status(ctx context.Context) (State, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed {
		return State{}, ErrUnsupported
	}
	record, err := i.loadState(ctx)
	syncStateMetadata(&record)
	return record.State, err
}

type executionStep struct {
	name               string
	activationBoundary bool
	run                func() error
}

func (i *installer) Execute(ctx context.Context, input Intent, approval Approval) (State, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	p, err := i.prepare(ctx, input)
	if err != nil {
		return State{}, err
	}
	if approval.DryRun {
		return State{
			Phase: "planned", PlanID: p.plan.ID, Trust: p.plan.Trust,
			Release: p.plan.TargetRelease, BundleDigest: p.plan.BundleDigest,
			PreviousRelease: p.plan.CurrentRelease, PreviousBundleDigest: p.plan.CurrentBundleDigest,
			ChangeKind: p.plan.ChangeKind, Completed: []string{}, SecretFiles: []string{},
		}, nil
	}
	if approval.PlanID == "" || approval.PlanID != p.plan.ID {
		return State{}, fmt.Errorf("%w: approved plan does not match the current inspected inputs", ErrApproval)
	}
	record := p.record
	if record.TargetFingerprint != "" && record.TargetFingerprint != p.plan.TargetFingerprint {
		return State{}, fmt.Errorf("%w: selected target does not match the installed target", ErrApproval)
	}
	if p.plan.ChangeKind == "checkpoint-migration" {
		record.Version = stateVersion
		record.Release, record.BundleDigest = p.plan.TargetRelease, p.plan.BundleDigest
		record.TargetFingerprint, record.ConfigID, record.CallerIdentity =
			p.plan.TargetFingerprint, p.plan.ConfigID, p.caller.digest
		record.RuntimeRoles = input.RuntimeRoles
		record.State.PlanID, record.State.Trust = p.plan.ID, p.plan.Trust
		record.State.Phase, record.State.FailedStep, record.State.FailureCode = "applied", "", ""
		record.State.ChangeKind, record.State.RollbackMode = "checkpoint-migration", "not-needed"
		syncStateMetadata(&record)
		if err := i.saveState(ctx, record); err != nil {
			return record.State, err
		}
		return record.State, nil
	}
	if input.Operation == "uninstall" && (record.TargetFingerprint == "" || record.ConfigID != p.plan.ConfigID || record.RuntimeRoles != input.RuntimeRoles) {
		return State{}, ErrApproval
	}
	reapply := record.State.Phase == "applied" &&
		p.plan.ChangeKind == "reapply" &&
		record.Release == p.plan.TargetRelease &&
		record.BundleDigest == p.plan.BundleDigest
	if reapply || record.State.PlanID != p.plan.ID {
		record.State.Completed = []string{}
	}
	if p.config.Target.Kind == "linux" && len(record.OwnedUnits) != 0 {
		if err := i.validateOwnedUnits(ctx, record); err != nil {
			return record.State, err
		}
	}
	record.Version, record.TargetFingerprint, record.CallerIdentity, record.RuntimeRoles =
		stateVersion, p.plan.TargetFingerprint, p.caller.digest, input.RuntimeRoles
	switch p.plan.ChangeKind {
	case "initial-install":
		record.Release, record.BundleDigest, record.ConfigID = p.plan.TargetRelease, p.plan.BundleDigest, p.plan.ConfigID
		record.PreviousRelease, record.PreviousBundleDigest, record.PreviousConfigID = "", "", ""
		record.State.RollbackMode = "not-needed"
	case "upgrade":
		if record.State.PlanID != p.plan.ID {
			record.PreviousRelease, record.PreviousBundleDigest, record.PreviousConfigID =
				record.Release, record.BundleDigest, record.ConfigID
			record.Release, record.BundleDigest, record.ConfigID =
				p.plan.TargetRelease, p.plan.BundleDigest, p.plan.ConfigID
			record.State.RollbackMode = "activation-eligible"
		}
	case "rollback":
		if record.State.RollbackMode != "activation-eligible" {
			return record.State, ErrRestoreRequired
		}
	case "reapply":
		record.Release, record.BundleDigest, record.ConfigID =
			p.plan.TargetRelease, p.plan.BundleDigest, p.plan.ConfigID
	case "uninstall":
		record.State.RollbackMode = "not-needed"
	default:
		return record.State, ErrUnsupported
	}
	record.State.PlanID, record.State.Trust = p.plan.ID, p.plan.Trust
	record.State.Phase, record.State.FailedStep, record.State.FailureCode = "planned", "", ""
	record.State.ChangeKind = p.plan.ChangeKind
	syncStateMetadata(&record)
	if err := i.saveState(ctx, record); err != nil {
		return record.State, err
	}
	var material credentialMaterial
	steps := []executionStep{{name: "protected-credentials", run: func() error {
		var err error
		material, err = i.materialize(ctx, p)
		if err == nil {
			record.State.SecretFiles = append([]string(nil), credentialPaths...)
		}
		return err
	}}}
	if input.Operation == "apply" || input.Operation == "rollback" {
		if p.config.Target.Kind == "kubernetes" {
			steps = append(steps, i.kubernetesApply(ctx, p, &material)...)
		} else {
			steps = append(steps, i.linuxApply(ctx, p, &material, &record)...)
		}
	} else if p.config.Target.Kind == "kubernetes" {
		steps = append(steps, i.kubernetesUninstall(ctx, p, &material, &record)...)
	} else {
		steps = append(steps, i.linuxUninstall(ctx, p, &material, &record)...)
	}
	for _, step := range steps {
		if step.name != "protected-credentials" && !reapply && slices.Contains(record.State.Completed, step.name) {
			continue
		}
		if err := i.requireApprovedBundle(ctx, p); err != nil {
			return i.failedStep(record, step.name, err)
		}
		if step.activationBoundary &&
			(p.plan.ChangeKind == "upgrade" || p.plan.ChangeKind == "rollback") &&
			record.State.RollbackMode == "activation-eligible" {
			record.State.RollbackMode = "data-restore-required"
			syncStateMetadata(&record)
			if err := i.saveState(ctx, record); err != nil {
				return record.State, err
			}
		}
		if err := step.run(); err != nil {
			return i.failedStep(record, step.name, err)
		}
		if !slices.Contains(record.State.Completed, step.name) {
			record.State.Completed = append(record.State.Completed, step.name)
		}
		if err := i.saveState(ctx, record); err != nil {
			return record.State, err
		}
		if _, err := fmt.Fprintf(i.options.Output, "Completed installer step: %s\n", step.name); err != nil {
			return record.State, errors.New("installer progress output failed")
		}
	}
	record.State.Phase = "applied"
	if input.Operation == "uninstall" {
		record.State.Phase = "uninstalled"
	}
	if p.plan.ChangeKind == "rollback" {
		record.Release, record.BundleDigest, record.ConfigID =
			p.plan.TargetRelease, p.plan.BundleDigest, p.plan.ConfigID
		record.PreviousRelease, record.PreviousBundleDigest, record.PreviousConfigID = "", "", ""
		record.State.RollbackMode = "not-needed"
	}
	syncStateMetadata(&record)
	if err := i.saveState(ctx, record); err != nil {
		return record.State, err
	}
	return record.State, nil
}

func (i *installer) failedStep(record stateRecord, step string, cause error) (State, error) {
	record.State.Phase, record.State.FailedStep = "failed", step
	record.State.FailureCode = "step-failed"
	switch {
	case errors.Is(cause, ErrCommand):
		record.State.FailureCode = "command-failed"
	case errors.Is(cause, ErrApproval):
		record.State.FailureCode = "approval-mismatch"
	case errors.Is(cause, ErrBundle):
		record.State.FailureCode = "bundle-invalid"
	}
	syncStateMetadata(&record)
	checkpointCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	return record.State, errors.Join(cause, i.saveState(checkpointCtx, record))
}
