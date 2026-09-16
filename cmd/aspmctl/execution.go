package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/bahadrdsr/aspm/internal/install/execution"
)

func deployment(args []string, output io.Writer) error {
	flags := flag.NewFlagSet(args[0], flag.ContinueOnError)
	flags.SetOutput(output)
	rootPath := flags.String("root", ".", "Existing execution root; native Linux requires the filesystem root")
	configPath := flags.String("config", "install.json", "Root-relative M00 Installation JSON")
	bundlePath := flags.String("bundle", "bundle", "Root-relative signed bundle directory")
	roleSelectionPath := flags.String("runtime-roles", "", "Root-relative nonsecret runtime role selection JSON")
	roleKeysPath := flags.String("role-keys", "", "Root-relative owner-only role credential JSON file")
	trustPath := flags.String("bundle-trust", "", "Independent Ed25519 PUBLIC KEY PEM file, outside the bundle")
	trustFingerprint := flags.String("trust-fingerprint", "", "Optional sha256:SPKI fingerprint for the independent public key")
	kubeconfigPath := flags.String("kubeconfig", "", "Explicit root-relative JSON kubeconfig, with no exec/auth-provider plugins")
	approval := flags.String("approve-plan", "", "Exact inspected deployment plan ID")
	operation := flags.String("operation", "apply", "deploy-plan only: apply or uninstall")
	dryRun := flags.Bool("dry-run", false, "Inspect and validate only; never resolve or generate application secrets")
	deleteData := flags.Bool("delete-data", false, "Uninstall only: separately approve deletion of named owned cluster data")
	if err := flags.Parse(args[1:]); err != nil {
		return err
	}
	if flags.NArg() != 0 || *roleKeysPath == "" || *trustPath == "" {
		return errors.New("operational commands require explicit --role-keys and independent --bundle-trust files, with no positional arguments")
	}
	rootName, err := filepath.Abs(*rootPath)
	if err != nil {
		return errors.New("invalid execution root")
	}
	root, err := os.OpenRoot(rootName)
	if err != nil {
		return errors.New("execution root must already exist")
	}
	defer root.Close()
	roleData, err := rootedInput(root, *roleKeysPath, true)
	if err != nil {
		return errors.New("private role-key input is missing, unsafe or invalid")
	}
	var supplied struct {
		Core, Ingestion struct {
			AccessKey string `json:"accessKey"`
			SecretKey string `json:"secretKey"`
		}
	}
	if boundedJSON(roleData, &supplied) != nil {
		return errors.New("private role-key file must contain explicit core and ingestion entries")
	}
	publicFile, err := filepath.Abs(*trustPath)
	if err != nil || !filepath.IsLocal(*bundlePath) {
		return execution.ErrBundle
	}
	bundleAbsolute := filepath.Join(rootName, *bundlePath)
	if relative, err := filepath.Rel(bundleAbsolute, publicFile); err == nil && filepath.IsLocal(relative) {
		return errors.New("bundle-local keys cannot establish independent trust")
	}
	trust, err := readPublicAuthority(publicFile, *trustFingerprint)
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	installer, err := execution.Open(ctx, execution.Options{
		Root: rootName, Kubeconfig: *kubeconfigPath, TrustedKey: trust, Output: output,
		Run: nativeCommand, LocalHost: execution.Host{OS: runtime.GOOS, EUID: os.Geteuid()},
		RoleKeys: execution.RoleCredentials{
			Core:      execution.RoleCredential{AccessKey: supplied.Core.AccessKey, SecretKey: supplied.Core.SecretKey},
			Ingestion: execution.RoleCredential{AccessKey: supplied.Ingestion.AccessKey, SecretKey: supplied.Ingestion.SecretKey},
		},
	})
	if err != nil {
		return err
	}
	defer installer.Close()
	encoder := json.NewEncoder(output)
	encoder.SetIndent("", "  ")
	if args[0] == "status" {
		state, err := installer.Status(ctx)
		if err != nil {
			return err
		}
		return encoder.Encode(state)
	}
	if *roleSelectionPath == "" {
		return errors.New("--runtime-roles is required; deployment selection cannot use implicit storage defaults")
	}
	configuration, err := rootedInput(root, *configPath, false)
	if err != nil {
		return errors.New("installation configuration is missing or exceeds its bound")
	}
	var destination struct {
		Target struct{ Kind string }
	}
	if json.Unmarshal(configuration, &destination) != nil {
		return execution.ErrUnsupported
	}
	if destination.Target.Kind == "linux" && (runtime.GOOS != "linux" || os.Geteuid() != 0 || rootName != string(filepath.Separator)) {
		return errors.New("native Linux execution requires the actual privileged local host and filesystem root; no elevation or staged-root execution is provided")
	}
	selectionBytes, err := rootedInput(root, *roleSelectionPath, false)
	if err != nil {
		return errors.New("runtime role selection is missing or unsafe")
	}
	var roles execution.RuntimeRoles
	if boundedJSON(selectionBytes, &roles) != nil {
		return errors.New("runtime role selection JSON is invalid")
	}
	intent := execution.Intent{Configuration: configuration, BundleDir: *bundlePath, Operation: "apply", RuntimeRoles: roles, DeleteData: *deleteData}
	if args[0] == "deploy-plan" {
		intent.Operation = *operation
	} else if *operation != "apply" {
		return errors.New("--operation is only supported by deploy-plan")
	} else if args[0] == "uninstall" {
		intent.Operation = "uninstall"
	}
	if args[0] == "deploy-plan" {
		plan, err := installer.Plan(ctx, intent)
		if err != nil {
			return err
		}
		return encoder.Encode(plan)
	}
	state, err := installer.Execute(ctx, intent, execution.Approval{PlanID: *approval, DryRun: *dryRun})
	if writeErr := encoder.Encode(state); writeErr != nil {
		return writeErr
	}
	return err
}

func rootedInput(root *os.Root, path string, private bool) ([]byte, error) {
	if path == "" || !filepath.IsLocal(path) || strings.ContainsRune(path, 0) {
		return nil, execution.ErrCredential
	}
	info, err := root.Stat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > 1<<20 {
		return nil, execution.ErrCredential
	}
	if private && runtime.GOOS != "windows" && info.Mode().Perm()&0077 != 0 {
		return nil, execution.ErrCredential
	}
	file, err := root.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		return nil, execution.ErrCredential
	}
	return data, nil
}

func boundedJSON(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return execution.ErrUnsupported
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return execution.ErrUnsupported
	}
	return nil
}

func readPublicAuthority(path, fingerprint string) (ed25519.PublicKey, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, execution.ErrBundle
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, (64<<10)+1))
	if err != nil || len(raw) > 64<<10 {
		return nil, execution.ErrBundle
	}
	block, rest := pem.Decode(raw)
	if block == nil || block.Type != "PUBLIC KEY" || len(bytes.TrimSpace(rest)) != 0 {
		return nil, execution.ErrBundle
	}
	value, err := x509.ParsePKIXPublicKey(block.Bytes)
	key, ok := value.(ed25519.PublicKey)
	if err != nil || !ok {
		return nil, execution.ErrBundle
	}
	sum := sha256.Sum256(block.Bytes)
	if fingerprint != "" && fingerprint != "sha256:"+hex.EncodeToString(sum[:]) {
		return nil, execution.ErrBundle
	}
	return key, nil
}

type nativeOutput struct{ buffer bytes.Buffer }

func (b *nativeOutput) Write(data []byte) (int, error) {
	if b.buffer.Len()+len(data) > 1<<20 {
		return 0, errors.New("native output exceeded the configured limit")
	}
	return b.buffer.Write(data)
}

func nativeCommand(ctx context.Context, command execution.Command) (execution.CommandResult, error) {
	switch command.Tool {
	case "kubectl", "helm", "systemctl":
	default:
		return execution.CommandResult{}, execution.ErrUnsupported
	}
	path, err := exec.LookPath(command.Tool)
	if err != nil {
		return execution.CommandResult{}, errors.New("required native tool is unavailable")
	}
	cmd := exec.CommandContext(ctx, path, command.Args...)
	cmd.WaitDelay = 2 * time.Second
	for _, variable := range os.Environ() {
		name, _, _ := strings.Cut(variable, "=")
		upper := strings.ToUpper(name)
		if upper == "KUBECONFIG" || strings.HasPrefix(upper, "ASPM_") || strings.HasPrefix(upper, "AWS_") ||
			strings.HasPrefix(upper, "HELM_") {
			continue
		}
		cmd.Env = append(cmd.Env, variable)
	}
	cmd.Env = append(cmd.Env, "HELM_NO_PLUGINS=1")
	cmd.Stdin = bytes.NewReader(command.Stdin)
	var stdout, stderr nativeOutput
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err = cmd.Run()
	result := execution.CommandResult{Stdout: stdout.buffer.Bytes(), Stderr: stderr.buffer.Bytes()}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	if err != nil {
		return result, execution.ErrCommand
	}
	return result, nil
}
