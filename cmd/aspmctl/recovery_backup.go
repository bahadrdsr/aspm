package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"time"
)

type recoveryBackupObservation struct {
	Ledger    []int                 `json:"ledger"`
	Inventory recoveryInventory     `json:"inventory"`
	Objects   recoveryObjectSummary `json:"objects"`
	Tools     recoveryToolVersions  `json:"tools"`
	Backup    string                `json:"backup"`
}

func backupPlan(ctx context.Context, config recoveryConfiguration, backup string) (recoveryPlan, error) {
	if backup == "" || !filepath.IsAbs(backup) {
		return recoveryPlan{}, errors.New("backup path must be explicit and absolute")
	}
	if _, err := os.Stat(backup); !errors.Is(err, os.ErrNotExist) {
		return recoveryPlan{}, errors.New("backup destination already exists")
	}
	runtime, err := openRecoveryRuntime(ctx, config)
	if err != nil {
		return recoveryPlan{}, err
	}
	defer runtime.close()
	ledger, err := runtime.requireSourceDatabase(ctx)
	if err != nil {
		return recoveryPlan{}, err
	}
	if err = runtime.requireQuiescent(ctx); err != nil {
		return recoveryPlan{}, err
	}
	inventory, err := recoveryInventoryFor(ctx, runtime.pool, config.Database.Schema, false)
	if err != nil {
		return recoveryPlan{}, errors.New("recovery database inventory failed")
	}
	objects, err := runtime.readSelectedObjects(ctx, "")
	if err != nil {
		return recoveryPlan{}, err
	}
	planID, err := recoveryPlanID("backup", config, recoveryBackupObservation{
		Ledger: ledger, Inventory: inventory, Objects: objects, Tools: runtime.tools, Backup: filepath.Clean(backup),
	})
	if err != nil {
		return recoveryPlan{}, err
	}
	return recoveryPlan{
		APIVersion: recoveryAPIVersion, Kind: "BackupPlan", ID: planID,
		Operation: "backup", InstanceID: config.InstanceID, ReadOnly: true,
	}, nil
}

func createBackup(ctx context.Context, config recoveryConfiguration,
	backup, approval string) (recoveryReceipt, error) {
	plan, err := backupPlan(ctx, config, backup)
	if err != nil {
		return recoveryReceipt{}, err
	}
	if approval == "" || approval != plan.ID {
		return recoveryReceipt{}, errors.New("backup plan approval does not match current inputs")
	}
	backupID, err := recoveryHexID()
	if err != nil {
		return recoveryReceipt{}, err
	}
	staging := backup + ".partial-" + backupID
	if err = os.Mkdir(staging, 0700); err != nil {
		return recoveryReceipt{}, errors.New("create backup staging directory failed")
	}
	complete := false
	defer func() {
		if complete {
			return
		}
		marker := map[string]any{
			"apiVersion": recoveryAPIVersion, "kind": "PartialBackup",
			"backupId": backupID, "phase": "failed",
		}
		_, _ = writeCanonicalJSON(filepath.Join(staging, "partial.json"), marker, 0600)
	}()
	runtime, err := openRecoveryRuntime(ctx, config)
	if err != nil {
		return recoveryReceipt{}, err
	}
	defer runtime.close()
	ledger, err := runtime.requireSourceDatabase(ctx)
	if err != nil {
		return recoveryReceipt{}, err
	}
	if err = runtime.requireQuiescent(ctx); err != nil {
		return recoveryReceipt{}, err
	}
	createdAt := time.Now().UTC()
	inventory, databaseStart, databaseEnd, dumpSize, err := runtime.createDatabaseBackup(ctx, staging)
	if err != nil {
		return recoveryReceipt{}, err
	}
	inventoryBytes, err := canonicalInventoryBytes(inventory)
	if err != nil {
		return recoveryReceipt{}, err
	}
	if err = os.WriteFile(filepath.Join(staging, "database-inventory.json"), inventoryBytes, 0600); err != nil {
		return recoveryReceipt{}, err
	}
	objectStart := time.Now().UTC()
	objects, err := runtime.readSelectedObjects(ctx, staging)
	if err != nil {
		return recoveryReceipt{}, err
	}
	objectEnd := time.Now().UTC()
	objectIndex := canonicalObjectLines(objects.Objects)
	if err = os.WriteFile(filepath.Join(staging, "objects.jsonl"), objectIndex, 0600); err != nil {
		return recoveryReceipt{}, err
	}
	rechecked, err := runtime.readSelectedObjects(ctx, "")
	if err != nil {
		return recoveryReceipt{}, err
	}
	objectRecheck := time.Now().UTC()
	if !recoveryObjectsEqual(objects, rechecked) {
		return recoveryReceipt{}, errors.New("selected recovery objects changed during backup")
	}
	if err = runtime.requireQuiescent(ctx); err != nil {
		return recoveryReceipt{}, err
	}
	dumpBytes, err := os.ReadFile(filepath.Join(staging, "database.dump"))
	if err != nil || int64(len(dumpBytes)) != dumpSize {
		return recoveryReceipt{}, errors.New("recovery database dump is unavailable")
	}
	manifest := recoveryManifest{
		APIVersion: recoveryAPIVersion, Kind: "BackupManifest", FormatVersion: 1,
		BackupID: backupID, CreatedAt: createdAt.Format(time.RFC3339Nano),
		CompletedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	manifest.Source.InstanceID = config.InstanceID
	manifest.Source.ReleaseVersion = version
	manifest.Source.TargetKind = config.TargetKind
	manifest.Source.Database.Name = config.Database.Name
	manifest.Source.Database.Schema = config.Database.Schema
	manifest.Source.Database.SchemaVersion = 27
	manifest.Source.Database.Ledger = ledger
	manifest.Source.Database.ExcludedTableData =
		[]string{"app_auth_throttle", "app_oidc_flows", "app_sessions"}
	manifest.Source.Storage.Bucket = config.Storage.Bucket
	manifest.Source.Storage.Region = config.Storage.Region
	manifest.Source.Storage.Prefixes = runtime.selectedPrefixes()
	manifest.CredentialBoundary.ActiveSessionsRestored = false
	manifest.CredentialBoundary.ApplicationCiphertextIncluded = true
	manifest.CredentialBoundary.IntegrationEncryptionKeyIncluded = false
	manifest.CredentialBoundary.RuntimeSecretsIncluded = false
	manifest.CredentialBoundary.OperatorAction =
		"Reprovision runtime secrets out of band and require users to reauthenticate."
	manifest.Consistency.Mode = "quiesced-single-instance"
	manifest.Consistency.DistributedAtomic = false
	manifest.Consistency.ApplicationConnections = 0
	manifest.Consistency.TransitionalRows = 0
	manifest.Consistency.DatabaseSnapshotStartedAt = databaseStart.Format(time.RFC3339Nano)
	manifest.Consistency.DatabaseSnapshotCompletedAt = databaseEnd.Format(time.RFC3339Nano)
	manifest.Consistency.ObjectCopyStartedAt = objectStart.Format(time.RFC3339Nano)
	manifest.Consistency.ObjectCopyCompletedAt = objectEnd.Format(time.RFC3339Nano)
	manifest.Consistency.ObjectRecheckCompletedAt = objectRecheck.Format(time.RFC3339Nano)
	manifest.Tools.ASPMCTL = version
	manifest.Tools.PostgresServer = runtime.tools.Server
	manifest.Tools.PGDump = runtime.tools.PGDump
	manifest.Tools.PGRestore = runtime.tools.PGRestore
	manifest.Tools.PSQL = runtime.tools.PSQL
	manifest.Tools.S3Client = "github.com/aws/aws-sdk-go-v2/service/s3@v1.113.1"
	manifest.Tools.Checksum = "sha256"
	manifest.Database.Dump.Path = "database.dump"
	manifest.Database.Dump.SHA256 = recoveryHash(dumpBytes)
	manifest.Database.Dump.SizeBytes = dumpSize
	manifest.Database.Inventory.Path = "database-inventory.json"
	manifest.Database.Inventory.SHA256 = recoveryHash(inventoryBytes)
	manifest.Database.Inventory.SizeBytes = int64(len(inventoryBytes))
	manifest.Database.Inventory.TableCount = len(inventory.Tables)
	manifest.Objects.Index.Path = "objects.jsonl"
	manifest.Objects.Index.SHA256 = recoveryHash(objectIndex)
	manifest.Objects.Index.SizeBytes = int64(len(objectIndex))
	manifest.Objects.ObjectCount = objects.ObjectCount
	manifest.Objects.LogicalBytes = objects.LogicalBytes
	manifest.Objects.BlobCount = objects.BlobCount
	manifest.Objects.BlobBytes = objects.BlobBytes
	manifest.Limitations = []string{
		"quiesced-single-instance-only", "no-distributed-atomicity", "same-release-schema-v27-only",
		"no-ha-or-online-multi-replica", "no-point-in-time-recovery", "no-cross-target-or-name-remap",
		"no-object-version-history", "no-cloud-snapshot-certification", "no-rto-rpo-or-throughput-claim",
	}
	manifestBytes, err := writeCanonicalJSON(filepath.Join(staging, "manifest.json"), manifest, 0600)
	if err != nil {
		return recoveryReceipt{}, err
	}
	manifestDigest := recoveryHash(manifestBytes)
	if err = os.WriteFile(filepath.Join(staging, "manifest.sha256"),
		[]byte(manifestDigest+"\n"), 0600); err != nil {
		return recoveryReceipt{}, err
	}
	if err = verifyBackupArtifacts(ctx, runtime, staging, config); err != nil {
		return recoveryReceipt{}, err
	}
	if err = os.Rename(staging, backup); err != nil {
		return recoveryReceipt{}, errors.New("publish completed backup directory failed")
	}
	complete = true
	return recoveryReceipt{
		APIVersion: recoveryAPIVersion, Kind: "BackupReceipt", Phase: "completed",
		BackupID: backupID, ManifestSHA256: manifestDigest,
	}, nil
}

func verifyBackup(ctx context.Context, config recoveryConfiguration,
	backup string) (recoveryReceipt, recoveryManifest, recoveryInventory, []recoveryObject, error) {
	runtime, err := openRecoveryRuntime(ctx, config)
	if err != nil {
		return recoveryReceipt{}, recoveryManifest{}, recoveryInventory{}, nil, err
	}
	defer runtime.close()
	manifest, manifestDigest, inventory, objects, err := verifyBackupArtifactsDetailed(ctx, runtime, backup, config)
	if err != nil {
		return recoveryReceipt{}, recoveryManifest{}, recoveryInventory{}, nil, err
	}
	return recoveryReceipt{
		APIVersion: recoveryAPIVersion, Kind: "BackupVerification", Phase: "verified",
		BackupID: manifest.BackupID, ManifestSHA256: manifestDigest,
	}, manifest, inventory, objects, nil
}

func verifyBackupArtifacts(ctx context.Context, runtime *recoveryRuntime, backup string,
	config recoveryConfiguration) error {
	_, _, _, _, err := verifyBackupArtifactsDetailed(ctx, runtime, backup, config)
	return err
}

func verifyBackupArtifactsDetailed(ctx context.Context, runtime *recoveryRuntime, backup string,
	config recoveryConfiguration) (recoveryManifest, string, recoveryInventory, []recoveryObject, error) {
	if backup == "" || !filepath.IsAbs(backup) {
		return recoveryManifest{}, "", recoveryInventory{}, nil, errors.New("backup path must be explicit and absolute")
	}
	entries, err := os.ReadDir(backup)
	if err != nil {
		return recoveryManifest{}, "", recoveryInventory{}, nil, errors.New("completed backup is unavailable")
	}
	allowed := map[string]bool{
		"manifest.json": true, "manifest.sha256": true, "database.dump": true,
		"database-inventory.json": true, "objects.jsonl": true, "objects": true,
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		if !allowed[entry.Name()] {
			return recoveryManifest{}, "", recoveryInventory{}, nil, errors.New("completed backup contains undeclared artifacts")
		}
		if entry.Type()&os.ModeSymlink != 0 ||
			(entry.Name() == "objects" && !entry.IsDir()) ||
			(entry.Name() != "objects" && entry.IsDir()) {
			return recoveryManifest{}, "", recoveryInventory{}, nil, errors.New("completed backup contains unsafe artifact types")
		}
		seen[entry.Name()] = true
	}
	for _, required := range []string{
		"manifest.json", "manifest.sha256", "database.dump",
		"database-inventory.json", "objects.jsonl",
	} {
		if !seen[required] {
			return recoveryManifest{}, "", recoveryInventory{}, nil, errors.New("completed backup is incomplete")
		}
	}
	manifestBytes, err := os.ReadFile(filepath.Join(backup, "manifest.json"))
	if err != nil || len(manifestBytes) == 0 || manifestBytes[len(manifestBytes)-1] != '\n' ||
		bytes.Contains(manifestBytes[:len(manifestBytes)-1], []byte{'\n'}) ||
		bytes.Contains(manifestBytes, []byte{'\r'}) {
		return recoveryManifest{}, "", recoveryInventory{}, nil, errors.New("recovery manifest is not canonical")
	}
	var manifest recoveryManifest
	decoder := json.NewDecoder(bytes.NewReader(manifestBytes))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&manifest) != nil {
		return recoveryManifest{}, "", recoveryInventory{}, nil, errors.New("recovery manifest is invalid")
	}
	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return recoveryManifest{}, "", recoveryInventory{}, nil, errors.New("recovery manifest contains trailing JSON")
	}
	canonical, _ := json.Marshal(manifest)
	canonical = append(canonical, '\n')
	if !bytes.Equal(canonical, manifestBytes) {
		return recoveryManifest{}, "", recoveryInventory{}, nil, errors.New("recovery manifest is not canonical")
	}
	manifestDigest := recoveryHash(manifestBytes)
	checksum, err := os.ReadFile(filepath.Join(backup, "manifest.sha256"))
	if err != nil || string(checksum) != manifestDigest+"\n" {
		return recoveryManifest{}, "", recoveryInventory{}, nil, errors.New("recovery manifest checksum mismatch")
	}
	if err = validateRecoveryManifest(manifest, runtime, config); err != nil {
		return recoveryManifest{}, "", recoveryInventory{}, nil, err
	}
	dumpPath := filepath.Join(backup, filepath.FromSlash(manifest.Database.Dump.Path))
	inventoryPath := filepath.Join(backup, filepath.FromSlash(manifest.Database.Inventory.Path))
	indexPath := filepath.Join(backup, filepath.FromSlash(manifest.Objects.Index.Path))
	dump, err := os.ReadFile(dumpPath)
	if err != nil || int64(len(dump)) != manifest.Database.Dump.SizeBytes ||
		recoveryHash(dump) != manifest.Database.Dump.SHA256 ||
		int64(len(dump)) > config.Limits.MaxDatabaseBytes {
		return recoveryManifest{}, "", recoveryInventory{}, nil, errors.New("recovery database dump failed integrity validation")
	}
	inventoryBytes, err := os.ReadFile(inventoryPath)
	if err != nil || int64(len(inventoryBytes)) != manifest.Database.Inventory.SizeBytes ||
		recoveryHash(inventoryBytes) != manifest.Database.Inventory.SHA256 {
		return recoveryManifest{}, "", recoveryInventory{}, nil, errors.New("recovery database inventory failed integrity validation")
	}
	inventory, err := parseRecoveryInventory(inventoryBytes)
	if err != nil || len(inventory.Tables) != manifest.Database.Inventory.TableCount {
		return recoveryManifest{}, "", recoveryInventory{}, nil, errors.New("recovery database inventory is invalid")
	}
	canonicalInventory, err := canonicalInventoryBytes(inventory)
	if err != nil || !bytes.Equal(canonicalInventory, inventoryBytes) {
		return recoveryManifest{}, "", recoveryInventory{}, nil, errors.New("recovery database inventory is not canonical")
	}
	indexBytes, err := os.ReadFile(indexPath)
	if err != nil || int64(len(indexBytes)) != manifest.Objects.Index.SizeBytes ||
		recoveryHash(indexBytes) != manifest.Objects.Index.SHA256 {
		return recoveryManifest{}, "", recoveryInventory{}, nil, errors.New("recovery object index failed integrity validation")
	}
	objects, err := parseRecoveryObjectLines(indexBytes)
	if err != nil || len(objects) != manifest.Objects.ObjectCount ||
		len(objects) > config.Limits.MaxObjects {
		return recoveryManifest{}, "", recoveryInventory{}, nil, errors.New("recovery object index is invalid")
	}
	if !bytes.Equal(canonicalObjectLines(append([]recoveryObject(nil), objects...)), indexBytes) {
		return recoveryManifest{}, "", recoveryInventory{}, nil, errors.New("recovery object index is not canonical")
	}
	if err = validateRecoveryObjectIndex(manifest, objects, config); err != nil {
		return recoveryManifest{}, "", recoveryInventory{}, nil, err
	}
	if err = verifyRecoveryObjectArtifacts(backup, objects); err != nil {
		return recoveryManifest{}, "", recoveryInventory{}, nil, err
	}
	command := exec.CommandContext(ctx, config.Tools.PGRestore, "--list", dumpPath)
	command.Env = recoveryToolEnvironment(config.Database.URL)
	if err = command.Run(); err != nil {
		return recoveryManifest{}, "", recoveryInventory{}, nil, errors.New("recovery database dump cannot be inspected")
	}
	return manifest, manifestDigest, inventory, objects, nil
}

func validateRecoveryManifest(manifest recoveryManifest, runtime *recoveryRuntime,
	config recoveryConfiguration) error {
	wantLedger := make([]int, 27)
	for index := range wantLedger {
		wantLedger[index] = index + 1
	}
	if manifest.APIVersion != recoveryAPIVersion || manifest.Kind != "BackupManifest" ||
		manifest.FormatVersion != 1 || !recoveryHexPattern.MatchString(manifest.BackupID) ||
		manifest.Source.ReleaseVersion != version ||
		manifest.Source.InstanceID != config.InstanceID || manifest.Source.TargetKind != config.TargetKind ||
		manifest.Source.Database.Name != config.Database.Name ||
		manifest.Source.Database.Schema != config.Database.Schema ||
		manifest.Source.Database.SchemaVersion != 27 ||
		!reflect.DeepEqual(manifest.Source.Database.Ledger, wantLedger) ||
		!reflect.DeepEqual(manifest.Source.Database.ExcludedTableData,
			[]string{"app_auth_throttle", "app_oidc_flows", "app_sessions"}) ||
		manifest.Source.Storage.Bucket != config.Storage.Bucket ||
		manifest.Source.Storage.Region != config.Storage.Region ||
		!reflect.DeepEqual(manifest.Source.Storage.Prefixes, runtime.selectedPrefixes()) {
		return errors.New("recovery manifest does not match the selected instance")
	}
	if manifest.CredentialBoundary.ActiveSessionsRestored ||
		!manifest.CredentialBoundary.ApplicationCiphertextIncluded ||
		manifest.CredentialBoundary.IntegrationEncryptionKeyIncluded ||
		manifest.CredentialBoundary.RuntimeSecretsIncluded ||
		manifest.CredentialBoundary.OperatorAction !=
			"Reprovision runtime secrets out of band and require users to reauthenticate." {
		return errors.New("recovery manifest credential boundary is unsupported")
	}
	if manifest.Consistency.Mode != "quiesced-single-instance" ||
		manifest.Consistency.DistributedAtomic ||
		manifest.Consistency.ApplicationConnections != 0 ||
		manifest.Consistency.TransitionalRows != 0 {
		return errors.New("recovery manifest consistency boundary is unsupported")
	}
	timestamps := make([]time.Time, 0, 7)
	for _, value := range []string{
		manifest.CreatedAt,
		manifest.Consistency.DatabaseSnapshotStartedAt,
		manifest.Consistency.DatabaseSnapshotCompletedAt,
		manifest.Consistency.ObjectCopyStartedAt,
		manifest.Consistency.ObjectCopyCompletedAt,
		manifest.Consistency.ObjectRecheckCompletedAt,
		manifest.CompletedAt,
	} {
		parsed, err := time.Parse(time.RFC3339Nano, value)
		if err != nil || parsed.Location() != time.UTC {
			return errors.New("recovery manifest timestamps are invalid")
		}
		timestamps = append(timestamps, parsed)
	}
	for index := 1; index < len(timestamps); index++ {
		if timestamps[index].Before(timestamps[index-1]) {
			return errors.New("recovery manifest timestamps are inconsistent")
		}
	}
	if manifest.Tools.ASPMCTL != version ||
		manifest.Tools.PostgresServer != runtime.tools.Server ||
		manifest.Tools.PGDump != runtime.tools.PGDump ||
		manifest.Tools.PGRestore != runtime.tools.PGRestore ||
		manifest.Tools.PSQL != runtime.tools.PSQL ||
		manifest.Tools.S3Client != "github.com/aws/aws-sdk-go-v2/service/s3@v1.113.1" ||
		manifest.Tools.Checksum != "sha256" {
		return errors.New("recovery manifest tool inventory is unsupported")
	}
	if manifest.Database.Dump.Path != "database.dump" ||
		manifest.Database.Inventory.Path != "database-inventory.json" ||
		manifest.Objects.Index.Path != "objects.jsonl" ||
		!recoveryDigestPattern.MatchString(manifest.Database.Dump.SHA256) ||
		!recoveryDigestPattern.MatchString(manifest.Database.Inventory.SHA256) ||
		!recoveryDigestPattern.MatchString(manifest.Objects.Index.SHA256) ||
		manifest.Database.Dump.SizeBytes < 1 ||
		manifest.Database.Dump.SizeBytes > config.Limits.MaxDatabaseBytes ||
		manifest.Database.Inventory.SizeBytes < 1 ||
		manifest.Objects.Index.SizeBytes < 0 ||
		manifest.Database.Inventory.TableCount != len(recoveryTables) {
		return errors.New("recovery manifest artifact inventory is invalid")
	}
	if manifest.Objects.ObjectCount < 0 ||
		manifest.Objects.ObjectCount > config.Limits.MaxObjects ||
		manifest.Objects.LogicalBytes < 0 ||
		manifest.Objects.LogicalBytes > config.Limits.MaxTotalObjectBytes ||
		manifest.Objects.BlobCount < 0 ||
		manifest.Objects.BlobCount > manifest.Objects.ObjectCount ||
		manifest.Objects.BlobBytes < 0 ||
		manifest.Objects.BlobBytes > manifest.Objects.LogicalBytes {
		return errors.New("recovery manifest object inventory is invalid")
	}
	if !reflect.DeepEqual(manifest.Limitations, []string{
		"quiesced-single-instance-only", "no-distributed-atomicity", "same-release-schema-v27-only",
		"no-ha-or-online-multi-replica", "no-point-in-time-recovery", "no-cross-target-or-name-remap",
		"no-object-version-history", "no-cloud-snapshot-certification", "no-rto-rpo-or-throughput-claim",
	}) {
		return errors.New("recovery manifest limitations are unsupported")
	}
	return nil
}

func backupStatus(backup string) (recoveryReceipt, error) {
	manifestBytes, err := os.ReadFile(filepath.Join(backup, "manifest.json"))
	if err != nil {
		return recoveryReceipt{}, errors.New("backup status is unavailable")
	}
	var manifest recoveryManifest
	if json.Unmarshal(manifestBytes, &manifest) != nil || manifest.BackupID == "" {
		return recoveryReceipt{}, errors.New("backup status is invalid")
	}
	return recoveryReceipt{
		APIVersion: recoveryAPIVersion, Kind: "BackupReceipt", Phase: "completed",
		BackupID: manifest.BackupID, ManifestSHA256: recoveryHash(manifestBytes),
	}, nil
}

func cleanupPartialBackup(backup, confirmation string) error {
	if backup == "" || !filepath.IsAbs(backup) || filepath.Clean(backup) != backup ||
		!recoveryHexPattern.MatchString(confirmation) ||
		!bytes.HasSuffix([]byte(filepath.Base(backup)), []byte(".partial-"+confirmation)) {
		return errors.New("partial backup path or confirmation is invalid")
	}
	info, err := os.Lstat(backup)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("partial backup directory is unavailable")
	}
	markerPath := filepath.Join(backup, "partial.json")
	markerInfo, err := os.Lstat(markerPath)
	if err != nil || !markerInfo.Mode().IsRegular() || markerInfo.Mode()&os.ModeSymlink != 0 {
		return errors.New("partial backup marker is unavailable")
	}
	data, err := os.ReadFile(markerPath)
	if err != nil {
		return errors.New("partial backup marker is unavailable")
	}
	var marker struct {
		APIVersion string `json:"apiVersion"`
		Kind       string `json:"kind"`
		BackupID   string `json:"backupId"`
		Phase      string `json:"phase"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&marker) != nil || marker.APIVersion != recoveryAPIVersion ||
		marker.Kind != "PartialBackup" || marker.Phase != "failed" ||
		confirmation == "" || confirmation != marker.BackupID {
		return errors.New("partial backup confirmation does not match")
	}
	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return errors.New("partial backup marker is invalid")
	}
	canonical, err := json.Marshal(marker)
	if err != nil || !bytes.Equal(append(canonical, '\n'), data) {
		return errors.New("partial backup marker is invalid")
	}
	return os.RemoveAll(backup)
}
