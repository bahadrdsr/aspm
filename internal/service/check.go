package service

import (
	"bytes"
	"context"
	"errors"
	"time"

	"github.com/bahadrdsr/aspm/internal/app"
	"github.com/bahadrdsr/aspm/internal/evidence"
	"github.com/bahadrdsr/aspm/internal/jobs"
)

type CheckReport struct {
	JobID      string `json:"jobId"`
	State      string `json:"state"`
	Kind       string `json:"kind"`
	ReceiptID  string `json:"receiptId"`
	Bytes      int64  `json:"bytes"`
	VerifiedBy string `json:"verifiedBy"`
}

// Check verifies shared storage and an independently running worker. It does
// not classify application findings or exercise any external target.
func Check(ctx context.Context, config Config) (CheckReport, error) {
	if err := app.ValidateStorageConfig(storageConfig(config.Evidence)); err != nil {
		return CheckReport{}, err
	}
	if err := app.ValidateDatabaseConfig(databaseConfig(config.Jobs)); err != nil {
		return CheckReport{}, err
	}
	ctx, cancel := context.WithTimeout(ctx, 45*time.Second)
	defer cancel()
	queue, err := jobs.Open(ctx, config.Jobs)
	if err != nil {
		return CheckReport{}, err
	}
	defer queue.Close()
	store, err := evidence.Open(ctx, config.Evidence)
	if err != nil {
		return CheckReport{}, err
	}
	defer store.Close()
	workspace := config.Workspace
	if workspace == "" {
		workspace = "system-checks"
	}
	id := jobs.NewID()
	data := []byte("ASPM explicit synthetic shared-evidence check\r\n" + id)
	ref, err := store.Put(ctx, workspace, id, bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return CheckReport{}, err
	}
	job, err := queue.Enqueue(ctx, jobs.Envelope{
		Version: jobs.Version, Kind: jobs.IntegrityKind, WorkspaceID: workspace, SourceID: "system-check",
		RunID: id, BatchID: id, ParserRevision: "integrity-v1", TraceID: id, IdempotencyKey: id, MaxAttempts: 2, Evidence: ref,
	})
	if err != nil {
		return CheckReport{}, err
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return CheckReport{}, errors.New("independent worker did not complete the synthetic check before the deadline")
		case <-ticker.C:
			saved, err := queue.Get(ctx, workspace, job.JobID)
			if err != nil {
				return CheckReport{}, err
			}
			if saved.State == "failed" {
				return CheckReport{}, errors.New("independent worker reported failure; inspect the check job")
			}
			if saved.State == "succeeded" {
				if saved.Receipt == nil || saved.Receipt.Result.SHA256 != ref.SHA256 || saved.Receipt.Result.SizeBytes != ref.SizeBytes {
					return CheckReport{}, errors.New("independent worker returned an inconsistent integrity receipt")
				}
				return CheckReport{
					JobID: saved.ID, State: saved.State, Kind: jobs.IntegrityKind,
					ReceiptID: saved.Receipt.ID, Bytes: ref.SizeBytes, VerifiedBy: "independent-durable-worker",
				}, nil
			}
		}
	}
}
