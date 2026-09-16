package ingestion

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/bahadrdsr/aspm/internal/evidence"
	"github.com/bahadrdsr/aspm/internal/jobs"
)

type Config struct {
	Jobs     jobs.Config
	Evidence evidence.Config
	Claim    jobs.Claim
}

type WorkReport struct {
	Processed   bool
	JobID       string
	State       string
	FailureCode string
}

func RunIntegrityOnce(ctx context.Context, config Config) (report WorkReport, err error) {
	queue, err := jobs.Open(ctx, config.Jobs)
	if err != nil {
		return report, err
	}
	defer queue.Close()
	storage, err := evidence.Open(ctx, config.Evidence)
	if err != nil {
		return report, err
	}
	defer storage.Close()
	return ProcessIntegrity(ctx, queue, storage, config.Claim)
}

func ProcessIntegrity(ctx context.Context, queue *jobs.Store, storage *evidence.Store, claim jobs.Claim) (report WorkReport, err error) {
	lease, err := queue.Claim(ctx, claim)
	if errors.Is(err, jobs.ErrNoJob) {
		return WorkReport{}, nil
	}
	if err != nil {
		return report, err
	}
	report.Processed, report.JobID = true, lease.JobID
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	heartbeatDone := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(max(claim.LeaseFor/3, time.Millisecond))
		defer ticker.Stop()
		for {
			select {
			case <-workCtx.Done():
				heartbeatDone <- nil
				return
			case <-ticker.C:
				if _, err := queue.Heartbeat(workCtx, lease, claim.LeaseFor); err != nil {
					if errors.Is(err, context.Canceled) && workCtx.Err() != nil {
						heartbeatDone <- nil
						return
					}
					cancel()
					heartbeatDone <- err
					return
				}
			}
		}
	}()
	reader, readErr := storage.Open(workCtx, lease.Envelope.WorkspaceID, lease.Envelope.Evidence)
	if readErr == nil {
		_, copyErr := io.Copy(io.Discard, reader)
		readErr = errors.Join(copyErr, reader.Close())
	}
	cancel()
	if heartbeatErr := <-heartbeatDone; heartbeatErr != nil {
		return report, fmt.Errorf("integrity worker lost lease: %w", heartbeatErr)
	}
	if readErr != nil {
		code := "evidence-read"
		retryable := true
		if errors.Is(readErr, evidence.ErrIntegrity) {
			code, retryable = "evidence-integrity", false
		} else if errors.Is(readErr, evidence.ErrNotFound) || errors.Is(readErr, evidence.ErrScope) {
			code, retryable = "evidence-unavailable", false
		}
		if err := queue.Fail(ctx, lease, jobs.Failure{Code: code, Message: "Evidence could not be verified", Retryable: retryable}); err != nil {
			return report, err
		}
		saved, err := queue.Get(ctx, lease.Envelope.WorkspaceID, lease.JobID)
		if err != nil {
			return report, err
		}
		report.State, report.FailureCode = saved.State, code
		return report, nil
	}
	_, err = queue.Complete(ctx, lease, jobs.Result{SHA256: lease.Envelope.Evidence.SHA256, SizeBytes: lease.Envelope.Evidence.SizeBytes})
	if err != nil {
		return report, err
	}
	report.State = "succeeded"
	return report, nil
}
