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
	config configuration
	bundle verifiedBundle
	input  Intent
	caller callerIdentity
}

func Open(ctx context.Context, options Options) (Installer, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if storagepolicy.ValidateRuntimeCredentials(credential(options.RoleKeys.Core), credential(options.RoleKeys.Ingestion)) != nil {
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
		DeleteData: input.DeleteData, RuntimeRoles: input.RuntimeRoles}
	bound := struct {
		Plan           Plan
		RoleKeys       string
		TrustKey       string
		CallerIdentity string
	}{
		Plan: plan, RoleKeys: privateRoleDigest(i.options.RoleKeys), TrustKey: digest(i.options.TrustedKey),
		CallerIdentity: p.caller.digest,
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
	p.plan = plan
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
		i.options.RoleKeys.Ingestion.AccessKey, i.options.RoleKeys.Ingestion.SecretKey}, additional...)
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
	if storagepolicy.ValidateRuntimeCredentials(credential(i.options.RoleKeys.Core), credential(i.options.RoleKeys.Ingestion)) != nil {
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
	case "systemctl":
		if p.config.Target.Kind != "linux" || i.options.LocalHost.OS != "linux" || i.options.LocalHost.EUID != 0 {
			return CommandResult{}, ErrUnsupported
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
