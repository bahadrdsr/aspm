package execution

import (
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/bahadrdsr/aspm/internal/storagepolicy"
)

type installer struct {
	options     Options
	root        string
	files       Files
	owned       *os.Root
	nativeFiles bool
	mu          sync.Mutex
	closed      bool
}

type prepared struct {
	plan   Plan
	record stateRecord
	config configuration
	bundle verifiedBundle
	input  Intent
	caller callerIdentity
}

func Open(ctx context.Context, options Options) (Installer, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if storagepolicy.ValidateRuntimeCredentials(credential(options.RoleKeys.Core), credential(options.RoleKeys.Ingestion),
		credential(options.RoleKeys.Retention)) != nil {
		return nil, ErrCredential
	}
	if len(options.TrustedKey) != ed25519.PublicKeySize {
		return nil, ErrBundle
	}
	if options.Root == "" || !filepath.IsAbs(options.Root) || options.Run == nil {
		return nil, ErrUnsupported
	}
	if options.Output == nil {
		options.Output = io.Discard
	}
	options.TrustedKey = append(ed25519.PublicKey(nil), options.TrustedKey...)
	i := &installer{options: options, root: filepath.Clean(options.Root), files: options.Files}
	if i.files == nil {
		root, err := os.OpenRoot(i.root)
		if err != nil {
			return nil, fmt.Errorf("%w: installer root is unavailable", ErrUnsupported)
		}
		i.owned, i.files, i.nativeFiles = root, rootFiles{root}, true
	}
	return i, nil
}

func (i *installer) Close() error {
	i.mu.Lock()
	defer i.mu.Unlock()
	if i.closed {
		return nil
	}
	i.closed = true
	if i.owned != nil {
		return i.owned.Close()
	}
	return nil
}

func (i *installer) Plan(ctx context.Context, input Intent) (Plan, error) {
	i.mu.Lock()
	defer i.mu.Unlock()
	p, err := i.prepare(ctx, input)
	if err != nil {
		return Plan{}, err
	}
	p.plan.Images = maps.Clone(p.plan.Images)
	return p.plan, nil
}

func (i *installer) prepare(ctx context.Context, input Intent) (prepared, error) {
	if i.closed {
		return prepared{}, ErrUnsupported
	}
	if err := ctx.Err(); err != nil {
		return prepared{}, err
	}
	if len(i.options.TrustedKey) != ed25519.PublicKeySize {
		return prepared{}, ErrBundle
	}
	resolved, config, err := i.resolve(ctx, input)
	if err != nil {
		return prepared{}, err
	}
	record, err := i.loadState(ctx)
	if err != nil {
		return prepared{}, err
	}
	if record.Version == 1 {
		return prepared{}, fmt.Errorf("%w: legacy caller-bound ownership requires a separately reviewed migration; checkpoint and credentials were preserved", ErrUnsupported)
	}
	if input.Operation == "rollback" && record.State.RollbackMode == "data-restore-required" {
		return prepared{}, ErrRestoreRequired
	}
	bundle, err := i.verifyBundle(ctx, input.BundleDir, config)
	if err != nil {
		return prepared{}, err
	}
	p := prepared{config: config, bundle: bundle, input: input}
	if config.Target.Kind == "kubernetes" {
		p.caller, err = i.kubeCaller(ctx, config.Target.Context)
		if err != nil {
			return prepared{}, err
		}
	}
	fingerprint, err := i.inspect(ctx, p)
	if err != nil {
		return prepared{}, err
	}
	plan := Plan{ConfigID: resolved.ID, BundleDigest: bundle.digest, Images: maps.Clone(bundle.manifest.Images),
		TargetFingerprint: fingerprint, Trust: "signed", Operation: input.Operation,
		DeleteData: input.DeleteData, RuntimeRoles: input.RuntimeRoles,
		TargetRelease: config.Release.Version, RollbackPolicy: "not-applicable"}
	switch input.Operation {
	case "uninstall":
		if record.Release == "" {
			return prepared{}, ErrUnsupported
		}
		plan.CurrentRelease, plan.CurrentBundleDigest = record.Release, record.BundleDigest
		plan.ChangeKind = "uninstall"
	case "rollback":
		if record.State.RollbackMode == "data-restore-required" {
			return prepared{}, ErrRestoreRequired
		}
		if record.State.RollbackMode != "activation-eligible" ||
			record.PreviousRelease == "" ||
			record.PreviousRelease != config.Release.Version ||
			record.PreviousBundleDigest != bundle.digest ||
			record.PreviousConfigID != resolved.ID {
			return prepared{}, ErrUnsupported
		}
		plan.CurrentRelease, plan.CurrentBundleDigest = record.Release, record.BundleDigest
		plan.ChangeKind = "rollback"
		plan.RollbackPolicy = "activation-before-runtime-or-data-restore"
	case "apply":
		switch {
		case record.TargetFingerprint == "":
			plan.ChangeKind = "initial-install"
		case record.Release == "":
			if record.State.Phase != "applied" || record.ConfigID != resolved.ID ||
				record.RuntimeRoles != input.RuntimeRoles {
				return prepared{}, ErrUnsupported
			}
			plan.CurrentRelease, plan.CurrentBundleDigest = config.Release.Version, bundle.digest
			plan.ChangeKind = "checkpoint-migration"
		case record.State.Phase != "applied" &&
			record.State.ChangeKind == "initial-install" &&
			record.Release == config.Release.Version && record.BundleDigest == bundle.digest:
			plan.ChangeKind = "initial-install"
		case record.State.Phase != "applied" &&
			record.State.ChangeKind == "upgrade" &&
			record.PreviousRelease != "" &&
			record.Release == config.Release.Version && record.BundleDigest == bundle.digest:
			plan.CurrentRelease, plan.CurrentBundleDigest = record.PreviousRelease, record.PreviousBundleDigest
			plan.ChangeKind = "upgrade"
			plan.RollbackPolicy = "activation-before-runtime-or-data-restore"
		case record.Release == config.Release.Version && record.BundleDigest == bundle.digest:
			plan.CurrentRelease, plan.CurrentBundleDigest = record.Release, record.BundleDigest
			plan.ChangeKind = "reapply"
		case record.State.Phase != "applied":
			return prepared{}, fmt.Errorf("%w: incomplete release checkpoint does not match the selected signed bundle", ErrApproval)
		default:
			plan.CurrentRelease, plan.CurrentBundleDigest = record.Release, record.BundleDigest
			plan.ChangeKind = "upgrade"
			plan.RollbackPolicy = "activation-before-runtime-or-data-restore"
		}
	default:
		return prepared{}, ErrUnsupported
	}
	if plan.ChangeKind == "reapply" && record.State.PlanID != "" &&
		record.TargetFingerprint == plan.TargetFingerprint &&
		record.CallerIdentity == p.caller.digest {
		plan.ID = record.State.PlanID
		p.plan, p.record = plan, record
		return p, nil
	}
	bound := struct {
		Plan           Plan
		RoleKeys       string
		TrustKey       string
		CallerIdentity string
	}{
		Plan: plan, RoleKeys: privateRoleDigest(i.options.RoleKeys), TrustKey: digest(i.options.TrustedKey),
		CallerIdentity: p.caller.digest,
	}
	if input.Operation == "apply" && record.State.PlanID != "" &&
		(record.State.Phase == "planned" || record.State.Phase == "failed") &&
		record.State.ChangeKind == plan.ChangeKind &&
		record.Release == plan.TargetRelease &&
		record.BundleDigest == plan.BundleDigest &&
		record.ConfigID == plan.ConfigID &&
		record.TargetFingerprint == plan.TargetFingerprint &&
		record.CallerIdentity == p.caller.digest &&
		record.RuntimeRoles == input.RuntimeRoles {
		plan.ID = record.State.PlanID
		p.plan, p.record = plan, record
		return p, nil
	}
	encoded, err := json.Marshal(bound)
	if err != nil {
		return prepared{}, ErrUnsupported
	}
	plan.ID = "deployment-" + digest(encoded)
	public, err := json.Marshal(plan)
	if err != nil || i.containsPrivate(string(public), p.caller.privateValues) {
		return prepared{}, ErrCredential
	}
	p.plan, p.record = plan, record
	return p, nil
}

func (i *installer) inspect(ctx context.Context, p prepared) (string, error) {
	if p.config.Target.Kind == "linux" {
		data, err := i.read(ctx, filepath.Join("etc", "machine-id"), 128)
		id := strings.TrimSpace(string(data))
		if err != nil || !machineID.MatchString(id) {
			return "", fmt.Errorf("%w: local machine identity is unavailable", ErrUnsupported)
		}
		return "linux-" + digest([]byte(id)), nil
	}
	result, err := i.command(ctx, p, Command{Tool: "kubectl", Args: append(i.kubeArgs(p),
		"get", "namespace", "kube-system", "-o", "json")}, nil)
	if err != nil {
		return "", err
	}
	var namespace struct {
		APIVersion string                     `json:"apiVersion"`
		Kind       string                     `json:"kind"`
		Metadata   struct{ Name, UID string } `json:"metadata"`
	}
	if json.Unmarshal(result.Stdout, &namespace) != nil || namespace.APIVersion != "v1" ||
		namespace.Kind != "Namespace" || namespace.Metadata.Name != "kube-system" || !namespaceUID.MatchString(namespace.Metadata.UID) {
		return "", fmt.Errorf("%w: target inspection returned no valid cluster identity", ErrCommand)
	}
	// Endpoint/context/authentication bytes belong to a particular approval,
	// not to persistent ownership of this cluster's selected namespace.
	data, _ := json.Marshal([]string{strings.ToLower(namespace.Metadata.UID), p.config.Target.Namespace})
	return "kubernetes-" + digest(data), nil
}

func (i *installer) kubeArgs(p prepared) []string {
	return []string{"--kubeconfig", filepath.Join(i.root, p.caller.path),
		"--context", p.config.Target.Context, "--namespace", p.config.Target.Namespace}
}

func (i *installer) helmArgs(p prepared) []string {
	return []string{"--kubeconfig", filepath.Join(i.root, p.caller.path),
		"--kube-context", p.config.Target.Context, "--namespace", p.config.Target.Namespace}
}

func (i *installer) containsPrivate(value string, additional []string) bool {
	values := append([]string{i.options.RoleKeys.Core.AccessKey, i.options.RoleKeys.Core.SecretKey,
		i.options.RoleKeys.Ingestion.AccessKey, i.options.RoleKeys.Ingestion.SecretKey,
		i.options.RoleKeys.Retention.AccessKey, i.options.RoleKeys.Retention.SecretKey}, additional...)
	for _, secret := range values {
		if secret == "" {
			continue
		}
		for _, representation := range []string{secret, base64.StdEncoding.EncodeToString([]byte(secret)), url.QueryEscape(secret)} {
			if strings.Contains(value, representation) {
				return true
			}
		}
	}
	return false
}

func (i *installer) command(ctx context.Context, p prepared, command Command, secrets []string) (CommandResult, error) {
	if err := ctx.Err(); err != nil {
		return CommandResult{}, err
	}
	if storagepolicy.ValidateRuntimeCredentials(credential(i.options.RoleKeys.Core), credential(i.options.RoleKeys.Ingestion),
		credential(i.options.RoleKeys.Retention)) != nil {
		return CommandResult{}, ErrCredential
	}
	if err := i.requireApprovedBundle(ctx, p); err != nil {
		return CommandResult{}, err
	}
	switch command.Tool {
	case "kubectl", "helm":
		if p.config.Target.Kind != "kubernetes" || p.caller.path == "" {
			return CommandResult{}, ErrUnsupported
		}
		caller, err := i.kubeCaller(ctx, p.config.Target.Context)
		if err != nil {
			return CommandResult{}, err
		}
		if caller.digest != p.caller.digest {
			return CommandResult{}, ErrApproval
		}
	case "systemctl", "podman":
		if p.config.Target.Kind != "linux" || i.options.LocalHost.OS != "linux" || i.options.LocalHost.EUID != 0 {
			return CommandResult{}, ErrUnsupported
		}
		if command.Tool == "podman" {
			create := slices.Equal(command.Args, []string{"secret", "create", policySecretName, "-"})
			remove := slices.Equal(command.Args, []string{"secret", "rm", "--ignore", policySecretName})
			if !create && !remove {
				return CommandResult{}, ErrUnsupported
			}
		}
	default:
		return CommandResult{}, ErrUnsupported
	}
	secrets = append(append([]string(nil), secrets...), p.caller.privateValues...)
	for _, argument := range command.Args {
		if strings.ContainsRune(argument, 0) || i.containsPrivate(argument, secrets) {
			return CommandResult{}, ErrCredential
		}
	}
	callCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	result, err := i.options.Run(callCtx, command)
	if err != nil || result.ExitCode != 0 {
		return CommandResult{}, errors.Join(fmt.Errorf("%w: allowlisted %s step did not complete", ErrCommand, command.Tool), ctx.Err())
	}
	if len(result.Stdout) > maxInputBytes || len(result.Stderr) > maxInputBytes {
		return CommandResult{}, fmt.Errorf("%w: native response exceeded its bound", ErrCommand)
	}
	return result, ctx.Err()
}
