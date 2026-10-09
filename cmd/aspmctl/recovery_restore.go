package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/jackc/pgx/v5"
)

type recoveryRestoreObservation struct {
	ManifestSHA256 string               `json:"manifestSha256"`
	Manifest       recoveryManifest     `json:"manifest"`
	Inventory      recoveryInventory    `json:"inventory"`
	Objects        []recoveryObject     `json:"objects"`
	Tools          recoveryToolVersions `json:"tools"`
	State          string               `json:"state"`
}

func restorePlan(ctx context.Context, config recoveryConfiguration,
	backup, statePath string) (recoveryPlan, error) {
	if statePath == "" || !filepath.IsAbs(statePath) {
		return recoveryPlan{}, errors.New("restore state path must be explicit and absolute")
	}
	if _, err := os.Stat(statePath); !errors.Is(err, os.ErrNotExist) {
		return recoveryPlan{}, errors.New("restore state already exists")
	}
	runtime, err := openRecoveryRuntime(ctx, config)
	if err != nil {
		return recoveryPlan{}, err
	}
	defer runtime.close()
	manifest, manifestDigest, inventory, objects, err :=
		verifyBackupArtifactsDetailed(ctx, runtime, backup, config)
	if err != nil {
		return recoveryPlan{}, err
	}
	if err = runtime.requireNoApplicationConnections(ctx); err != nil {
		return recoveryPlan{}, err
	}
	exists, err := runtime.schemaExists(ctx)
	if err != nil || exists {
		return recoveryPlan{}, errors.New("restore destination schema must be absent")
	}
	empty, err := runtime.selectedPrefixesEmpty(ctx)
	if err != nil || !empty {
		return recoveryPlan{}, errors.New("restore destination prefixes must be empty")
	}
	planID, err := recoveryPlanID("restore", config, recoveryRestoreObservation{
		ManifestSHA256: manifestDigest, Manifest: manifest, Inventory: inventory,
		Objects: objects, Tools: runtime.tools, State: filepath.Clean(statePath),
	})
	if err != nil {
		return recoveryPlan{}, err
	}
	return recoveryPlan{
		APIVersion: recoveryAPIVersion, Kind: "RestorePlan", ID: planID,
		Operation: "restore", InstanceID: config.InstanceID, ReadOnly: true,
	}, nil
}

func applyRestore(ctx context.Context, config recoveryConfiguration, backup, statePath,
	approval, confirmation string) (receipt recoveryReceipt, err error) {
	plan, err := restorePlan(ctx, config, backup, statePath)
	if err != nil {
		return receipt, err
	}
	if approval == "" || approval != plan.ID || confirmation == "" || confirmation != config.InstanceID {
		return receipt, errors.New("restore approval or instance confirmation does not match current inputs")
	}
	runtime, err := openRecoveryRuntime(ctx, config)
	if err != nil {
		return receipt, err
	}
	defer runtime.close()
	manifest, manifestDigest, expectedInventory, objects, err :=
		verifyBackupArtifactsDetailed(ctx, runtime, backup, config)
	if err != nil {
		return receipt, err
	}
	if err = runtime.requireNoApplicationConnections(ctx); err != nil {
		return receipt, err
	}
	exists, err := runtime.schemaExists(ctx)
	if err != nil || exists {
		return receipt, errors.New("restore destination schema must remain absent")
	}
	empty, err := runtime.selectedPrefixesEmpty(ctx)
	if err != nil || !empty {
		return receipt, errors.New("restore destination prefixes must remain empty")
	}
	restoreID, err := recoveryHexID()
	if err != nil {
		return receipt, err
	}
	state := recoveryRestoreState{
		APIVersion: recoveryAPIVersion, Kind: "RestoreState", Phase: "applying",
		BackupID: manifest.BackupID, RestoreID: restoreID, ManifestSHA256: manifestDigest,
		InstanceID: config.InstanceID, Schema: config.Database.Schema,
		CreatedKeys: []string{}, UpdatedAt: time.Now().UTC().Format(time.RFC3339Nano),
	}
	if _, err = writeCanonicalJSON(statePath, state, 0600); err != nil {
		return receipt, errors.New("create restore journal failed")
	}
	defer func() {
		if err == nil {
			return
		}
		state.Phase = "failed"
		state.Failure = "restore did not complete; inspect status and use explicit cleanup"
		state.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		_, _ = writeCanonicalJSON(statePath, state, 0600)
	}()
	if err = runtime.restoreObjects(ctx, backup, objects, &state, statePath); err != nil {
		return receipt, err
	}
	state.SchemaPending = true
	state.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = writeCanonicalJSON(statePath, state, 0600); err != nil {
		return receipt, errors.New("record recovery schema checkpoint failed")
	}
	if err = runtime.restoreDatabase(ctx, filepath.Join(backup, "database.dump")); err != nil {
		return receipt, err
	}
	if _, err = runtime.requireSourceDatabase(ctx); err != nil {
		return receipt, errors.New("restored database catalog is not exact V27")
	}
	state.SchemaPending = false
	state.SchemaCreated = true
	state.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = writeCanonicalJSON(statePath, state, 0600); err != nil {
		return receipt, err
	}
	if err = runtime.requireNoApplicationConnections(ctx); err != nil {
		return receipt, err
	}
	actualInventory, err := recoveryInventoryFor(ctx, runtime.pool, config.Database.Schema, true)
	if err != nil || !recoveryInventoryEqual(expectedInventory, actualInventory) {
		return receipt, errors.New("restored database inventory does not match the completed backup")
	}
	actualObjects, err := runtime.readSelectedObjects(ctx, "")
	if err != nil || !recoveryObjectListEqual(objects, actualObjects.Objects) {
		return receipt, errors.New("restored object inventory does not match the completed backup")
	}
	state.Phase = "verified"
	state.Failure = ""
	state.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = writeCanonicalJSON(statePath, state, 0600); err != nil {
		return receipt, err
	}
	return recoveryReceipt{
		APIVersion: recoveryAPIVersion, Kind: "RestoreReceipt", Phase: "verified",
		BackupID: manifest.BackupID, RestoreID: state.RestoreID, ManifestSHA256: manifestDigest,
	}, nil
}

func verifyRestore(ctx context.Context, config recoveryConfiguration,
	backup, statePath string) (recoveryReceipt, error) {
	state, err := readRestoreState(statePath)
	if err != nil || state.Phase != "verified" || state.InstanceID != config.InstanceID ||
		state.Schema != config.Database.Schema {
		return recoveryReceipt{}, errors.New("verified restore state is unavailable")
	}
	runtime, err := openRecoveryRuntime(ctx, config)
	if err != nil {
		return recoveryReceipt{}, err
	}
	defer runtime.close()
	manifest, manifestDigest, expectedInventory, objects, err :=
		verifyBackupArtifactsDetailed(ctx, runtime, backup, config)
	if err != nil || manifest.BackupID != state.BackupID ||
		manifestDigest != state.ManifestSHA256 {
		return recoveryReceipt{}, errors.New("restore state does not match the completed backup")
	}
	if err = runtime.requireNoApplicationConnections(ctx); err != nil {
		return recoveryReceipt{}, err
	}
	if _, err = runtime.requireSourceDatabase(ctx); err != nil {
		return recoveryReceipt{}, errors.New("restored database catalog failed verification")
	}
	actualInventory, err := recoveryInventoryFor(ctx, runtime.pool, config.Database.Schema, true)
	if err != nil || !recoveryInventoryEqual(expectedInventory, actualInventory) {
		return recoveryReceipt{}, errors.New("restored database inventory failed verification")
	}
	actualObjects, err := runtime.readSelectedObjects(ctx, "")
	if err != nil || !recoveryObjectListEqual(objects, actualObjects.Objects) {
		return recoveryReceipt{}, errors.New("restored object inventory failed verification")
	}
	return recoveryReceipt{
		APIVersion: recoveryAPIVersion, Kind: "RestoreVerification", Phase: "verified",
		BackupID: state.BackupID, RestoreID: state.RestoreID, ManifestSHA256: manifestDigest,
	}, nil
}

func readRestoreState(path string) (recoveryRestoreState, error) {
	var state recoveryRestoreState
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return state, errors.New("restore state path must be explicit and absolute")
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
		info.Size() < 1 || info.Size() > 4<<20 {
		return state, errors.New("restore state is unavailable")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return state, errors.New("restore state is unavailable")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&state) != nil || state.APIVersion != recoveryAPIVersion ||
		state.Kind != "RestoreState" ||
		!recoveryHexPattern.MatchString(state.BackupID) ||
		!recoveryHexPattern.MatchString(state.RestoreID) ||
		!recoveryDigestPattern.MatchString(state.ManifestSHA256) ||
		!recoveryInstanceID.MatchString(state.InstanceID) ||
		!recoverySchemaName.MatchString(state.Schema) ||
		state.CreatedKeys == nil || state.SchemaCreated && state.SchemaPending {
		return recoveryRestoreState{}, errors.New("restore state is invalid")
	}
	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return recoveryRestoreState{}, errors.New("restore state contains trailing JSON")
	}
	canonical, err := json.Marshal(state)
	if err != nil || !bytes.Equal(append(canonical, '\n'), data) {
		return recoveryRestoreState{}, errors.New("restore state is not canonical")
	}
	updatedAt, err := time.Parse(time.RFC3339Nano, state.UpdatedAt)
	if err != nil || updatedAt.Location() != time.UTC {
		return recoveryRestoreState{}, errors.New("restore state timestamp is invalid")
	}
	switch state.Phase {
	case "applying":
		if state.Failure != "" {
			return recoveryRestoreState{}, errors.New("restore state phase is invalid")
		}
	case "failed":
		if state.Failure == "" {
			return recoveryRestoreState{}, errors.New("restore state phase is invalid")
		}
	case "verified":
		if state.Failure != "" || state.PendingKey != "" || state.SchemaPending ||
			!state.SchemaCreated {
			return recoveryRestoreState{}, errors.New("restore state phase is invalid")
		}
	default:
		return recoveryRestoreState{}, errors.New("restore state phase is invalid")
	}
	keys := map[string]bool{}
	for _, key := range state.CreatedKeys {
		if !validRecoveryObjectKey(key, "") || keys[key] || key == state.PendingKey {
			return recoveryRestoreState{}, errors.New("restore state object checkpoints are invalid")
		}
		keys[key] = true
	}
	if state.PendingKey != "" && !validRecoveryObjectKey(state.PendingKey, "") {
		return recoveryRestoreState{}, errors.New("restore state object checkpoints are invalid")
	}
	return state, nil
}

func restoreStatus(statePath string) (recoveryReceipt, error) {
	state, err := readRestoreState(statePath)
	if err != nil {
		return recoveryReceipt{}, err
	}
	return recoveryReceipt{
		APIVersion: recoveryAPIVersion, Kind: "RestoreReceipt", Phase: state.Phase,
		BackupID: state.BackupID, RestoreID: state.RestoreID, ManifestSHA256: state.ManifestSHA256,
	}, nil
}

func cleanupRestore(ctx context.Context, config recoveryConfiguration, backup, statePath string,
	dryRun bool, approval, confirmation string) (recoveryPlan, error) {
	state, err := readRestoreState(statePath)
	if err != nil || state.InstanceID != config.InstanceID || state.Phase == "verified" {
		return recoveryPlan{}, errors.New("restore cleanup is unavailable")
	}
	runtime, err := openRecoveryRuntime(ctx, config)
	if err != nil {
		return recoveryPlan{}, err
	}
	defer runtime.close()
	manifest, manifestDigest, _, objects, err := verifyBackupArtifactsDetailed(ctx, runtime, backup, config)
	if err != nil || manifest.BackupID != state.BackupID || manifestDigest != state.ManifestSHA256 {
		return recoveryPlan{}, errors.New("restore cleanup does not match the selected backup")
	}
	if err = runtime.requireNoApplicationConnections(ctx); err != nil {
		return recoveryPlan{}, err
	}
	planID, err := recoveryPlanID("restore-cleanup", config, state)
	if err != nil {
		return recoveryPlan{}, err
	}
	plan := recoveryPlan{
		APIVersion: recoveryAPIVersion, Kind: "RestorePlan", ID: planID,
		Operation: "restore", InstanceID: config.InstanceID, ReadOnly: true,
	}
	if dryRun {
		return plan, nil
	}
	if approval != planID || confirmation != config.InstanceID {
		return recoveryPlan{}, errors.New("restore cleanup approval does not match")
	}
	state.Phase = "failed"
	state.Failure = "restore cleanup is incomplete; rerun the approved cleanup procedure"
	state.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if _, err = writeCanonicalJSON(statePath, state, 0600); err != nil {
		return recoveryPlan{}, errors.New("record restore cleanup checkpoint failed")
	}
	index := map[string]recoveryObject{}
	for _, object := range objects {
		index[object.Key] = object
	}
	keys := append([]string(nil), state.CreatedKeys...)
	if state.PendingKey != "" {
		keys = append(keys, state.PendingKey)
	}
	for _, key := range keys {
		object, present := index[key]
		if !present {
			return recoveryPlan{}, errors.New("restore cleanup key is not in the completed backup")
		}
		output, getErr := runtime.s3.GetObject(ctx, &s3.GetObjectInput{
			Bucket: aws.String(config.Storage.Bucket), Key: aws.String(key),
		})
		if getErr != nil {
			if recoveryS3Missing(getErr) {
				if err = recordCleanedRestoreObject(&state, statePath, key); err != nil {
					return recoveryPlan{}, err
				}
				continue
			}
			return recoveryPlan{}, errors.New("restore cleanup object is unavailable")
		}
		data, readErr := io.ReadAll(io.LimitReader(output.Body, object.SizeBytes+1))
		closeErr := output.Body.Close()
		if errors.Join(readErr, closeErr) != nil || int64(len(data)) != object.SizeBytes ||
			recoveryHash(data) != object.SHA256 {
			return recoveryPlan{}, errors.New("restore cleanup refuses a modified object")
		}
		if _, deleteErr := runtime.s3.DeleteObject(ctx, &s3.DeleteObjectInput{
			Bucket: aws.String(config.Storage.Bucket), Key: aws.String(key),
		}); deleteErr != nil {
			return recoveryPlan{}, errors.New("restore cleanup object deletion failed")
		}
		if err = recordCleanedRestoreObject(&state, statePath, key); err != nil {
			return recoveryPlan{}, err
		}
	}
	if state.SchemaPending || state.SchemaCreated {
		exists, existsErr := runtime.schemaExists(ctx)
		if existsErr != nil {
			return recoveryPlan{}, errors.New("restore cleanup schema is unavailable")
		}
		if exists {
			if _, err = runtime.pool.Exec(ctx,
				"DROP SCHEMA "+pgx.Identifier{config.Database.Schema}.Sanitize()+" CASCADE"); err != nil {
				return recoveryPlan{}, errors.New("restore cleanup schema removal failed")
			}
		}
		state.SchemaPending = false
		state.SchemaCreated = false
		state.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
		if _, err = writeCanonicalJSON(statePath, state, 0600); err != nil {
			return recoveryPlan{}, errors.New("record restore cleanup checkpoint failed")
		}
	}
	if err = os.Remove(statePath); err != nil {
		return recoveryPlan{}, errors.New("restore cleanup journal removal failed")
	}
	return plan, nil
}

func recordCleanedRestoreObject(state *recoveryRestoreState, statePath, key string) error {
	filtered := state.CreatedKeys[:0]
	for _, created := range state.CreatedKeys {
		if created != key {
			filtered = append(filtered, created)
		}
	}
	state.CreatedKeys = filtered
	if state.PendingKey == key {
		state.PendingKey = ""
	}
	state.UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := writeCanonicalJSON(statePath, *state, 0600); err != nil {
		return errors.New("record restore cleanup checkpoint failed")
	}
	return nil
}

func recoveryObjectListEqual(expected, actual []recoveryObject) bool {
	sort.Slice(expected, func(i, j int) bool { return expected[i].Key < expected[j].Key })
	sort.Slice(actual, func(i, j int) bool { return actual[i].Key < actual[j].Key })
	return reflect.DeepEqual(expected, actual)
}
