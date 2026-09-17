package app

import (
	"context"
	"errors"
	"time"

	"github.com/bahadrdsr/aspm/internal/connectors"
)

func (w *CollectionWorker) ProcessNext(ctx context.Context) (bool, error) {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return false, errors.New("collection worker is closed")
	}
	w.active.Add(1)
	w.mu.Unlock()
	defer w.active.Done()
	access, cancel := context.WithCancelCause(ctx)
	stopLifetime := context.AfterFunc(w.lifetime, func() { cancel(context.Canceled) })
	defer stopLifetime()
	defer cancel(context.Canceled)
	if w.lifetime.Err() != nil {
		cancel(context.Canceled)
	}
	job, source, processed, err := w.claimSource(access)
	if err != nil || job.ID == "" {
		return processed, err
	}
	plain, err := openCredential(w.credentials, sourceCredentialAAD(job.WorkspaceID, job.SourceID), source.ciphertext)
	if err != nil {
		failure := &FindingDeliveryFailure{Code: "credential-unavailable"}
		cleanup, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		return true, w.settleSource(cleanup, job, "blocked", failure)
	}
	token := string(plain)
	clear(plain)
	collector, err := connectors.OpenCollector(access, connectors.CollectorConfig{
		Profile: job.Profile, Endpoint: w.endpoint, Token: token, Client: w.client, Limits: w.limits,
	})
	if err != nil {
		if settleErr := w.failSource(job, err); settleErr != nil {
			return true, settleErr
		}
		return true, nil
	}
	if _, err = w.sourceAuthority(access, w.pool, job, false); err == nil {
		err = w.sourceLease(access, w.pool, job)
	}
	if err != nil {
		return true, w.finishDeniedSource(ctx, job, err)
	}
	stopWatch := w.watchSource(access, cancel, job)
	result, nativeErr := collector.Collect(access, connectors.CollectRequest{
		Identity:   connectors.Identity{WorkspaceID: job.WorkspaceID, SourceID: job.SourceID, RunID: job.ID, ScopeID: job.Repository},
		Repository: job.Repository,
	})
	if cause := context.Cause(access); cause != nil {
		stopWatch()
		return true, w.finishDeniedSource(ctx, job, cause)
	}
	repository, validationErr := validateSourceResult(job, result, w.limits)
	if validationErr != nil || nativeErr == nil && !result.Complete {
		stopWatch()
		if nativeErr == nil {
			nativeErr = validationErr
		}
		cleanup, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		return true, w.settleSource(cleanup, job, "failed", sourceNativeFailure(nativeErr, token))
	}
	if source.repositoryID != nil && *source.repositoryID != repository.ID.String() {
		stopWatch()
		return true, w.failSource(job, sourceIdentityConflict)
	}
	records, publicationErr := w.publishSourceRecords(access, job, result.Records)
	stopWatch()
	if cause := context.Cause(access); cause != nil {
		return true, w.finishDeniedSource(ctx, job, cause)
	}
	if publicationErr != nil {
		if err := w.failSource(job, publicationErr); err != nil {
			return true, err
		}
		return true, errors.New("source evidence publication could not be confirmed")
	}
	var failure *FindingDeliveryFailure
	if nativeErr != nil {
		failure = sourceNativeFailure(nativeErr, token)
	}
	if err = w.finalizeSource(access, job, result, repository, records, failure); err != nil {
		return true, w.finishDeniedSource(ctx, job, err)
	}
	return true, nil
}

func (w *CollectionWorker) finishDeniedSource(ctx context.Context, job sourceCollectionRecord, cause error) error {
	if err := w.failSource(job, cause); err != nil {
		return err
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var denial collectionDenial
	if errors.As(cause, &denial) {
		return nil
	}
	return errors.New("source collection processing could not be completed")
}
