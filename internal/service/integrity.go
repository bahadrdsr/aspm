package service

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/bahadrdsr/aspm/internal/evidence"
	"github.com/bahadrdsr/aspm/internal/ingestion"
	"github.com/bahadrdsr/aspm/internal/jobs"
)

// The legacy integrity entrypoint accepts a writable Store. Main-role work
// instead composes the same durable lease API with the accepted read-only
// Reader; all S3 requests and stream verification remain in evidence.
func processIntegrity(ctx context.Context, queue *jobs.Store, reader *evidence.Reader, claim jobs.Claim) (ingestion.WorkReport, error) {
	lease, err := queue.Claim(ctx, claim)
	if errors.Is(err, jobs.ErrNoJob) {
		return ingestion.WorkReport{}, nil
	}
	if err != nil {
		return ingestion.WorkReport{}, err
	}
	report := ingestion.WorkReport{Processed: true, JobID: lease.JobID}
	workCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	heartbeat := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(max(claim.LeaseFor/3, time.Millisecond))
		defer ticker.Stop()
		for {
			select {
			case <-workCtx.Done():
				heartbeat <- nil
				return
			case <-ticker.C:
				if _, err := queue.Heartbeat(workCtx, lease, claim.LeaseFor); err != nil {
					if errors.Is(err, context.Canceled) && workCtx.Err() != nil {
						heartbeat <- nil
						return
					}
					cancel()
					heartbeat <- err
					return
				}
			}
		}
	}()
	body, readErr := reader.Open(workCtx, lease.Envelope.WorkspaceID, lease.Envelope.Evidence)
	if readErr == nil {
		_, copyErr := io.Copy(io.Discard, body)
		readErr = errors.Join(copyErr, body.Close())
	}
	cancel()
	if err := <-heartbeat; err != nil {
		return report, err
	}
	if ctx.Err() != nil {
		return report, ctx.Err()
	}
	if readErr != nil {
		failure := jobs.Failure{Code: "evidence-read", Message: "Evidence could not be verified", Retryable: true}
		if errors.Is(readErr, evidence.ErrIntegrity) {
			failure.Code, failure.Retryable = "evidence-integrity", false
		} else if errors.Is(readErr, evidence.ErrScope) || errors.Is(readErr, evidence.ErrNotFound) {
			failure.Code, failure.Retryable = "evidence-unavailable", false
		}
		if err := queue.Fail(ctx, lease, failure); err != nil {
			return report, err
		}
		saved, err := queue.Get(ctx, lease.Envelope.WorkspaceID, lease.JobID)
		if err != nil {
			return report, err
		}
		report.State, report.FailureCode = saved.State, failure.Code
		return report, nil
	}
	_, err = queue.Complete(ctx, lease, jobs.Result{SHA256: lease.Envelope.Evidence.SHA256, SizeBytes: lease.Envelope.Evidence.SizeBytes})
	if err == nil {
		report.State = "succeeded"
	}
	return report, err
}
