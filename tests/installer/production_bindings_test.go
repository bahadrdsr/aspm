package installer

import (
	"context"
	"errors"

	"github.com/bahadrdsr/aspm/internal/install/execution"
)

func init() {
	Production.OpenInstaller = func(ctx context.Context, input Options) (Installer, error) {
		var run func(context.Context, execution.Command) (execution.CommandResult, error)
		if input.Run != nil {
			run = func(ctx context.Context, command execution.Command) (execution.CommandResult, error) {
				result, err := input.Run(ctx, Command{Tool: command.Tool, Args: command.Args, Stdin: command.Stdin})
				return execution.CommandResult{ExitCode: result.ExitCode, Stdout: result.Stdout, Stderr: result.Stderr}, err
			}
		}
		real, err := execution.Open(ctx, execution.Options{
			Root: input.Root, Kubeconfig: input.Kubeconfig, Files: input.Files,
			Run: run, TrustedKey: input.TrustedKey, Output: input.Output,
			RoleKeys: execution.RoleCredentials{
				Core:      execution.RoleCredential{AccessKey: input.RoleKeys.Core.AccessKey, SecretKey: input.RoleKeys.Core.SecretKey},
				Ingestion: execution.RoleCredential{AccessKey: input.RoleKeys.Ingestion.AccessKey, SecretKey: input.RoleKeys.Ingestion.SecretKey},
			},
			LocalHost: execution.Host{OS: input.LocalHost.OS, EUID: input.LocalHost.EUID},
		})
		if err != nil {
			return nil, installerError(err)
		}
		return installerAdapter{real: real}, nil
	}
}

type installerAdapter struct {
	real execution.Installer
}

func (a installerAdapter) Plan(ctx context.Context, input Intent) (Plan, error) {
	result, err := a.real.Plan(ctx, executionIntent(input))
	return Plan{
		ID: result.ID, ConfigID: result.ConfigID, BundleDigest: result.BundleDigest,
		TargetFingerprint: result.TargetFingerprint, Trust: result.Trust,
		Images: result.Images, Operation: result.Operation, DeleteData: result.DeleteData,
		RuntimeRoles: RuntimeRoles{
			Core:      installerSelection(result.RuntimeRoles.Core),
			Ingestion: installerSelection(result.RuntimeRoles.Ingestion),
		},
	}, installerError(err)
}

func (a installerAdapter) Execute(ctx context.Context, input Intent, approval Approval) (State, error) {
	result, err := a.real.Execute(ctx, executionIntent(input), execution.Approval{PlanID: approval.PlanID, DryRun: approval.DryRun})
	return installerState(result), installerError(err)
}

func (a installerAdapter) Status(ctx context.Context) (State, error) {
	result, err := a.real.Status(ctx)
	return installerState(result), installerError(err)
}

func (a installerAdapter) Close() error {
	return installerError(a.real.Close())
}

func executionIntent(input Intent) execution.Intent {
	return execution.Intent{
		Configuration: input.Configuration, BundleDir: input.BundleDir,
		Operation: input.Operation, DeleteData: input.DeleteData, DevelopmentPolicy: input.DevelopmentPolicy,
		RuntimeRoles: execution.RuntimeRoles{
			Core:      executionSelection(input.RuntimeRoles.Core),
			Ingestion: executionSelection(input.RuntimeRoles.Ingestion),
		},
	}
}

func executionSelection(input RoleSelection) execution.RoleSelection {
	return execution.RoleSelection{
		S3Secret: execution.SecretSelection{
			Name: input.S3Secret.Name, AccessKeyKey: input.S3Secret.AccessKeyKey, SecretKeyKey: input.S3Secret.SecretKeyKey,
		},
		RawPrefix: input.RawPrefix, ReadinessKey: input.ReadinessKey, NormalizedPrefix: input.NormalizedPrefix,
	}
}

func installerSelection(input execution.RoleSelection) RoleSelection {
	return RoleSelection{
		S3Secret: SecretSelection{
			Name: input.S3Secret.Name, AccessKeyKey: input.S3Secret.AccessKeyKey, SecretKeyKey: input.S3Secret.SecretKeyKey,
		},
		RawPrefix: input.RawPrefix, ReadinessKey: input.ReadinessKey, NormalizedPrefix: input.NormalizedPrefix,
	}
}

func installerState(input execution.State) State {
	return State{
		Phase: input.Phase, PlanID: input.PlanID, Trust: input.Trust,
		FailedStep: input.FailedStep, FailureCode: input.FailureCode,
		Completed: input.Completed, SecretFiles: input.SecretFiles,
	}
}

func installerError(err error) error {
	if err == nil {
		return nil
	}
	for _, pair := range [][2]error{
		{execution.ErrApproval, ErrApproval},
		{execution.ErrBundle, ErrBundle},
		{execution.ErrCredential, ErrCredential},
		{execution.ErrCommand, ErrCommand},
		{execution.ErrUnsupported, ErrUnsupported},
	} {
		if errors.Is(err, pair[0]) {
			return errors.Join(pair[1], err)
		}
	}
	return err
}
