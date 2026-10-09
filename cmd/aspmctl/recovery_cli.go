package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
)

func recoveryCommand(args []string, output io.Writer) error {
	if len(args) < 2 {
		return errors.New("choose a recovery operation")
	}
	switch args[0] {
	case "backup":
		return backupCommand(args[1:], output)
	case "restore":
		return restoreCommand(args[1:], output)
	default:
		return errors.New("unsupported recovery operation")
	}
}

func backupCommand(args []string, output io.Writer) error {
	if len(args) == 0 {
		return errors.New("choose backup plan, create, verify, status, or cleanup")
	}
	switch args[0] {
	case "plan":
		flags := recoveryFlags("backup plan", output)
		configPath := flags.String("config", "", "Private recovery configuration file")
		backup := flags.String("backup", "", "New backup directory")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		config, err := loadRecoveryConfiguration(*configPath)
		if err != nil {
			return err
		}
		plan, err := backupPlan(context.Background(), config, *backup)
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(plan)
	case "create":
		flags := recoveryFlags("backup create", output)
		configPath := flags.String("config", "", "Private recovery configuration file")
		backup := flags.String("backup", "", "New backup directory")
		approval := flags.String("approve-plan", "", "Exact approved backup plan ID")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		config, err := loadRecoveryConfiguration(*configPath)
		if err != nil {
			return err
		}
		receipt, err := createBackup(context.Background(), config, *backup, *approval)
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(receipt)
	case "verify":
		flags := recoveryFlags("backup verify", output)
		configPath := flags.String("config", "", "Private recovery configuration file")
		backup := flags.String("backup", "", "Completed backup directory")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		config, err := loadRecoveryConfiguration(*configPath)
		if err != nil {
			return err
		}
		receipt, _, _, _, err := verifyBackup(context.Background(), config, *backup)
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(receipt)
	case "status":
		flags := recoveryFlags("backup status", output)
		backup := flags.String("backup", "", "Backup directory")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		receipt, err := backupStatus(*backup)
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(receipt)
	case "cleanup":
		flags := recoveryFlags("backup cleanup", output)
		backup := flags.String("backup", "", "Partial backup directory")
		confirmation := flags.String("confirm-backup", "", "Exact partial backup ID")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		return cleanupPartialBackup(*backup, *confirmation)
	default:
		return errors.New("unsupported backup operation")
	}
}

func restoreCommand(args []string, output io.Writer) error {
	if len(args) == 0 {
		return errors.New("choose restore plan, apply, verify, status, or cleanup")
	}
	switch args[0] {
	case "plan":
		flags := recoveryFlags("restore plan", output)
		configPath := flags.String("config", "", "Private recovery configuration file")
		backup := flags.String("backup", "", "Completed backup directory")
		state := flags.String("state", "", "New restore journal file")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		config, err := loadRecoveryConfiguration(*configPath)
		if err != nil {
			return err
		}
		plan, err := restorePlan(context.Background(), config, *backup, *state)
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(plan)
	case "apply":
		flags := recoveryFlags("restore apply", output)
		configPath := flags.String("config", "", "Private recovery configuration file")
		backup := flags.String("backup", "", "Completed backup directory")
		state := flags.String("state", "", "New restore journal file")
		approval := flags.String("approve-plan", "", "Exact approved restore plan ID")
		instance := flags.String("confirm-instance", "", "Exact logical instance ID")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		config, err := loadRecoveryConfiguration(*configPath)
		if err != nil {
			return err
		}
		receipt, err := applyRestore(context.Background(), config, *backup, *state, *approval, *instance)
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(receipt)
	case "verify":
		flags := recoveryFlags("restore verify", output)
		configPath := flags.String("config", "", "Private recovery configuration file")
		backup := flags.String("backup", "", "Completed backup directory")
		state := flags.String("state", "", "Restore journal file")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		config, err := loadRecoveryConfiguration(*configPath)
		if err != nil {
			return err
		}
		receipt, err := verifyRestore(context.Background(), config, *backup, *state)
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(receipt)
	case "status":
		flags := recoveryFlags("restore status", output)
		state := flags.String("state", "", "Restore journal file")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		receipt, err := restoreStatus(*state)
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(receipt)
	case "cleanup":
		flags := recoveryFlags("restore cleanup", output)
		configPath := flags.String("config", "", "Private recovery configuration file")
		backup := flags.String("backup", "", "Completed backup directory")
		state := flags.String("state", "", "Restore journal file")
		dryRun := flags.Bool("dry-run", false, "Produce the cleanup plan without changing data")
		approval := flags.String("approve-plan", "", "Exact approved cleanup plan ID")
		instance := flags.String("confirm-instance", "", "Exact logical instance ID")
		if err := flags.Parse(args[1:]); err != nil {
			return err
		}
		config, err := loadRecoveryConfiguration(*configPath)
		if err != nil {
			return err
		}
		plan, err := cleanupRestore(context.Background(), config, *backup, *state,
			*dryRun, *approval, *instance)
		if err != nil {
			return err
		}
		return json.NewEncoder(output).Encode(plan)
	default:
		return errors.New("unsupported restore operation")
	}
}

func recoveryFlags(name string, output io.Writer) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(output)
	flags.Usage = func() {
		fmt.Fprintf(output, "Usage: aspmctl %s [options]\n", name)
		flags.VisitAll(func(selected *flag.Flag) {
			fmt.Fprintf(output, "  --%s\n      %s\n", selected.Name, selected.Usage)
		})
	}
	return flags
}
