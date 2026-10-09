package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestM13RecoveryCLIHelpIsExplicitAndClosedByDefault(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want []string
	}{
		{"backup-plan", []string{"backup", "plan", "-h"}, []string{"--config", "--backup"}},
		{"backup-create", []string{"backup", "create", "-h"}, []string{"--config", "--backup", "--approve-plan"}},
		{"backup-verify", []string{"backup", "verify", "-h"}, []string{"--config", "--backup"}},
		{"backup-status", []string{"backup", "status", "-h"}, []string{"--backup"}},
		{"backup-cleanup", []string{"backup", "cleanup", "-h"}, []string{"--backup", "--confirm-backup"}},
		{"restore-plan", []string{"restore", "plan", "-h"}, []string{"--config", "--backup", "--state"}},
		{"restore-apply", []string{"restore", "apply", "-h"}, []string{"--config", "--backup", "--state", "--approve-plan", "--confirm-instance"}},
		{"restore-verify", []string{"restore", "verify", "-h"}, []string{"--config", "--backup", "--state"}},
		{"restore-status", []string{"restore", "status", "-h"}, []string{"--state"}},
		{"restore-cleanup-plan", []string{"restore", "cleanup", "-h"}, []string{"--config", "--backup", "--state", "--dry-run", "--approve-plan", "--confirm-instance"}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var output bytes.Buffer
			err := run(test.args, strings.NewReader(""), &output)
			if !errors.Is(err, flag.ErrHelp) {
				t.Fatalf("%v must expose bounded help without performing recovery; got %T", test.args, err)
			}
			help := output.String()
			for _, value := range test.want {
				if !strings.Contains(help, value) {
					t.Errorf("%v help omitted required explicit flag %s", test.args, value)
				}
			}
			for _, forbidden := range []string{
				"--force", "--overwrite", "--drop", "--all", "--discover",
				"--database-url", "--access-key", "--secret-key",
				"ASPM_DATABASE_URL", "AWS_ACCESS_KEY_ID", "PGPASSWORD",
			} {
				if strings.Contains(help, forbidden) {
					t.Errorf("%v help admitted unsafe or ambient surface %q", test.args, forbidden)
				}
			}
		})
	}
}

func TestM13PartialBackupCleanupRequiresOwnedStagingDirectory(t *testing.T) {
	backupID := strings.Repeat("a", 32)
	marker := struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		BackupID   string `json:"backupId"`
		Phase      string `json:"phase"`
	}{recoveryAPIVersion, "PartialBackup", backupID, "failed"}
	root := t.TempDir()
	arbitrary := filepath.Join(root, "arbitrary")
	if err := os.Mkdir(arbitrary, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := writeCanonicalJSON(filepath.Join(arbitrary, "partial.json"), marker, 0600); err != nil {
		t.Fatal(err)
	}
	if err := cleanupPartialBackup(arbitrary, backupID); err == nil {
		t.Fatal("cleanup accepted a directory outside the owned .partial-<backup-id> shape")
	}
	if _, err := os.Stat(arbitrary); err != nil {
		t.Fatal("rejected cleanup changed the arbitrary directory")
	}

	partial := filepath.Join(root, "backup.partial-"+backupID)
	if err := os.Mkdir(partial, 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := writeCanonicalJSON(filepath.Join(partial, "partial.json"), marker, 0600); err != nil {
		t.Fatal(err)
	}
	if err := cleanupPartialBackup(partial, backupID); err != nil {
		t.Fatalf("cleanup rejected its exact owned partial directory: %v", err)
	}
	if _, err := os.Stat(partial); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("approved partial backup cleanup did not remove the exact directory")
	}
}

func TestM13RestoreStateRequiresCanonicalBoundedCheckpoints(t *testing.T) {
	path := filepath.Join(t.TempDir(), "restore-state.json")
	state := recoveryRestoreState{
		APIVersion: recoveryAPIVersion,
		Kind:       "RestoreState",
		Phase:      "failed",
		BackupID:   strings.Repeat("a", 32),
		RestoreID:  strings.Repeat("b", 32),
		ManifestSHA256: "sha256:" +
			strings.Repeat("c", 64),
		InstanceID:  "12345678-1234-4123-8123-123456789abc",
		Schema:      "aspm",
		CreatedKeys: []string{"evidence/one"},
		PendingKey:  "archive/two",
		UpdatedAt:   time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
		Failure:     "restore did not complete",
	}
	if _, err := writeCanonicalJSON(path, state, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := readRestoreState(path)
	if err != nil {
		t.Fatalf("read valid restore state: %v", err)
	}
	if loaded.PendingKey != state.PendingKey || len(loaded.CreatedKeys) != 1 {
		t.Fatal("restore state lost cleanup checkpoints")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, append(data, []byte("{}")...), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = readRestoreState(path); err == nil {
		t.Fatal("restore state accepted trailing JSON")
	}
}

func TestM13ObjectIndexRejectsNoncanonicalAndTrailingJSON(t *testing.T) {
	object := recoveryObject{
		Purpose: "raw", Bucket: "aspm-evidence", Key: "evidence/one",
		SizeBytes: 2, SHA256: "sha256:" + strings.Repeat("d", 64),
		BlobPath:    "objects/sha256/" + strings.Repeat("d", 64),
		ContentType: "application/json", Metadata: map[string]string{
			"sha256": strings.Repeat("d", 64),
		},
	}
	line, err := json.Marshal(object)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = parseRecoveryObjectLines(append(line, '\n')); err != nil {
		t.Fatalf("canonical object index was rejected: %v", err)
	}
	if _, err = parseRecoveryObjectLines(append(append([]byte(" "), line...), '\n')); err == nil {
		t.Fatal("object index accepted noncanonical whitespace")
	}
	trailing := append(append([]byte(nil), line...), []byte(" null\n")...)
	if _, err = parseRecoveryObjectLines(trailing); err == nil {
		t.Fatal("object index accepted trailing JSON")
	}
}
