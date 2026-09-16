package execution

import (
	"context"
	"errors"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/bahadrdsr/aspm/internal/install/quadlet"
)

var activationUnits = []struct{ file, service string }{
	{"aspm-core.container", "aspm-core.service"},
	{"aspm-ingestion@.container", "aspm-ingestion@1.service"},
	{"aspm-reports.container", "aspm-reports.service"},
	{"aspm-storage.container", "aspm-storage.service"},
	{"aspm-postgres.container", "aspm-postgres.service"},
}

func ownedActivationPath(path string) bool {
	for _, unit := range activationUnits {
		if path == "etc/containers/systemd/"+unit.file {
			return true
		}
	}
	return false
}

func (i *installer) validateOwnedUnits(ctx context.Context, record stateRecord) error {
	if len(record.OwnedUnits) > len(activationUnits) {
		return ErrApproval
	}
	for path, expected := range record.OwnedUnits {
		if !ownedActivationPath(path) || !fileDigest.MatchString(expected) {
			return ErrApproval
		}
		data, err := i.read(ctx, path, 64<<10)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil || digest(data) != expected {
			return ErrApproval
		}
	}
	return nil
}

func pinImage(source []byte, image string) ([]byte, error) {
	if !pinnedImage.MatchString(image) || len(source) > 64<<10 {
		return nil, ErrBundle
	}
	lines := strings.Split(strings.ReplaceAll(string(source), "\r\n", "\n"), "\n")
	section, count := "", 0
	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") && strings.HasSuffix(trimmed, "]") {
			section = trimmed
		}
		if strings.HasPrefix(trimmed, "Image=") {
			if section != "[Container]" {
				return nil, ErrUnsupported
			}
			lines[index], count = "Image="+image, count+1
		}
	}
	if count != 1 {
		return nil, ErrUnsupported
	}
	return []byte(strings.Join(lines, "\n")), nil
}

func (i *installer) linuxApply(ctx context.Context, p prepared, material *credentialMaterial, record *stateRecord) []executionStep {
	return []executionStep{
		{name: "quadlet-files", run: func() error {
			if record.OwnedUnits == nil {
				record.OwnedUnits = make(map[string]string)
			}
			for _, unit := range []struct{ name, image, role string }{
				{"aspm-postgres.container", "postgres", ""}, {"aspm-storage.container", "storage", ""},
				{"aspm-core.container", "application", "core"}, {"aspm-ingestion@.container", "application", "ingestion"},
				{"aspm-reports.container", "application", "reports"},
			} {
				data := p.bundle.files["deploy/quadlet/"+unit.name]
				if unit.role != "" {
					selection := quadlet.Config{Role: unit.role, EnvironmentFile: "/etc/aspm/" + unit.role + ".env"}
					if unit.role == "core" {
						selection.RawPrefix, selection.ReadinessKey = p.input.RuntimeRoles.Core.RawPrefix, p.input.RuntimeRoles.Core.ReadinessKey
					} else if unit.role == "ingestion" {
						selection.RawPrefix, selection.ReadinessKey, selection.NormalizedPrefix =
							p.input.RuntimeRoles.Ingestion.RawPrefix, p.input.RuntimeRoles.Ingestion.ReadinessKey, p.input.RuntimeRoles.Ingestion.NormalizedPrefix
					}
					var err error
					data, err = quadlet.RenderRole(ctx, data, selection)
					if err != nil {
						return ErrUnsupported
					}
				}
				data, err := pinImage(data, p.plan.Images[unit.image])
				if err != nil {
					return err
				}
				path := filepath.Join("etc", "containers", "systemd", unit.name)
				if err := i.write(ctx, path, data); err != nil {
					return err
				}
				record.OwnedUnits[filepath.ToSlash(path)] = digest(data)
			}
			for _, name := range []string{"aspm.network", "aspm-postgres.volume", "aspm-storage.volume"} {
				if err := i.write(ctx, filepath.Join("etc", "containers", "systemd", name), p.bundle.files["deploy/quadlet/"+name]); err != nil {
					return err
				}
			}
			return nil
		}},
		{name: "system-manager-reload", run: func() error {
			_, err := i.command(ctx, p, Command{Tool: "systemctl", Args: []string{"--no-ask-password", "daemon-reload"}}, material.values())
			return err
		}},
		{name: "start-postgres", run: func() error {
			_, err := i.command(ctx, p, Command{Tool: "systemctl", Args: []string{"--no-ask-password", "start", "aspm-postgres.service"}}, material.values())
			return err
		}},
		{name: "start-storage", run: func() error {
			_, err := i.command(ctx, p, Command{Tool: "systemctl", Args: []string{"--no-ask-password", "start", "aspm-storage.service"}}, material.values())
			return err
		}},
		{name: "start-application-roles", run: func() error {
			_, err := i.command(ctx, p, Command{Tool: "systemctl", Args: []string{"--no-ask-password", "start",
				"aspm-core.service", "aspm-ingestion@1.service", "aspm-reports.service"}}, material.values())
			return err
		}},
	}
}

func (i *installer) linuxUninstall(ctx context.Context, p prepared, material *credentialMaterial, record *stateRecord) []executionStep {
	return []executionStep{
		{name: "stop-owned-services", run: func() error {
			if len(record.OwnedUnits) == 0 {
				return ErrApproval
			}
			if err := i.validateOwnedUnits(ctx, *record); err != nil {
				return err
			}
			args := []string{"--no-ask-password", "stop"}
			for _, unit := range activationUnits {
				if _, owned := record.OwnedUnits["etc/containers/systemd/"+unit.file]; owned {
					args = append(args, unit.service)
				}
			}
			_, err := i.command(ctx, p, Command{Tool: "systemctl", Args: args}, material.values())
			return err
		}},
		{name: "remove-owned-activation-definitions", run: func() error {
			if err := i.validateOwnedUnits(ctx, *record); err != nil {
				return err
			}
			for _, unit := range activationUnits {
				path := "etc/containers/systemd/" + unit.file
				if _, owned := record.OwnedUnits[path]; !owned {
					continue
				}
				if err := ctx.Err(); err != nil {
					return err
				}
				if err := i.files.Remove(filepath.FromSlash(path)); err != nil && !errors.Is(err, fs.ErrNotExist) {
					return errors.New("owned activation definition could not be removed")
				}
			}
			return nil
		}},
		{name: "reload-after-uninstall", run: func() error {
			_, err := i.command(ctx, p, Command{Tool: "systemctl", Args: []string{"--no-ask-password", "daemon-reload"}}, material.values())
			return err
		}},
	}
}
