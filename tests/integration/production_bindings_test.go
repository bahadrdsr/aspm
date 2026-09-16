//go:build integration

package integration

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/bahadrdsr/aspm/internal/evidence"
	"github.com/bahadrdsr/aspm/internal/ingestion"
	"github.com/bahadrdsr/aspm/internal/jobs"
)

func init() {
	Production.OpenJobs = func(ctx context.Context, config JobConfig) (Jobs, error) {
		store, err := jobs.Open(ctx, jobs.Config(config))
		if err != nil {
			return nil, contractError(err)
		}
		return jobBinding{store}, nil
	}
	Production.OpenEvidence = func(ctx context.Context, config EvidenceConfig) (Evidence, error) {
		store, err := evidence.Open(ctx, evidence.Config(config))
		if err != nil {
			return nil, contractError(err)
		}
		return evidenceBinding{store}, nil
	}
	Production.RunIntegrityOnce = func(ctx context.Context, config WorkerConfig) (WorkReport, error) {
		report, err := ingestion.RunIntegrityOnce(ctx, ingestion.Config{
			Jobs: jobs.Config(config.Jobs), Evidence: evidence.Config(config.Evidence), Claim: jobs.Claim(config.Claim),
		})
		return WorkReport(report), contractError(err)
	}
}

func contractError(err error) error {
	for _, pair := range [][2]error{
		{jobs.ErrNoJob, ErrNoJob}, {jobs.ErrInvalid, ErrInvalid}, {jobs.ErrConflict, ErrConflict},
		{jobs.ErrLeaseLost, ErrLeaseLost}, {jobs.ErrNotFound, ErrNotFound},
		{evidence.ErrInvalid, ErrInvalid}, {evidence.ErrScope, ErrScope},
		{evidence.ErrIntegrity, ErrIntegrity}, {evidence.ErrNotFound, ErrNotFound},
	} {
		if errors.Is(err, pair[0]) {
			return errors.Join(pair[1], err)
		}
	}
	return err
}

func toEnvelope(e Envelope) jobs.Envelope {
	return jobs.Envelope{
		Version: e.Version, Kind: e.Kind, WorkspaceID: e.WorkspaceID, SourceID: e.SourceID,
		RunID: e.RunID, BatchID: e.BatchID, ParserRevision: e.ParserRevision, TraceID: e.TraceID,
		IdempotencyKey: e.IdempotencyKey, MaxAttempts: e.MaxAttempts, Evidence: evidence.Ref(e.Evidence),
	}
}

func fromEnvelope(e jobs.Envelope) Envelope {
	return Envelope{
		Version: e.Version, Kind: e.Kind, WorkspaceID: e.WorkspaceID, SourceID: e.SourceID,
		RunID: e.RunID, BatchID: e.BatchID, ParserRevision: e.ParserRevision, TraceID: e.TraceID,
		IdempotencyKey: e.IdempotencyKey, MaxAttempts: e.MaxAttempts, Evidence: EvidenceRef(e.Evidence),
	}
}

func toLease(l Lease) jobs.Lease {
	return jobs.Lease{JobID: l.JobID, Envelope: toEnvelope(l.Envelope), WorkerID: l.WorkerID,
		Fence: l.Fence, Attempt: l.Attempt, ExpiresAt: l.ExpiresAt}
}

func fromLease(l jobs.Lease) Lease {
	return Lease{JobID: l.JobID, Envelope: fromEnvelope(l.Envelope), WorkerID: l.WorkerID,
		Fence: l.Fence, Attempt: l.Attempt, ExpiresAt: l.ExpiresAt}
}

func fromReceipt(r jobs.Receipt) Receipt {
	return Receipt{ID: r.ID, JobID: r.JobID, Fence: r.Fence, Result: Result(r.Result), CompletedAt: r.CompletedAt}
}

type jobBinding struct{ *jobs.Store }

func (s jobBinding) Enqueue(ctx context.Context, e Envelope) (Enqueued, error) {
	result, err := s.Store.Enqueue(ctx, toEnvelope(e))
	return Enqueued(result), contractError(err)
}

func (s jobBinding) Claim(ctx context.Context, c Claim) (Lease, error) {
	lease, err := s.Store.Claim(ctx, jobs.Claim(c))
	return fromLease(lease), contractError(err)
}

func (s jobBinding) Heartbeat(ctx context.Context, l Lease, extension time.Duration) (Lease, error) {
	lease, err := s.Store.Heartbeat(ctx, toLease(l), extension)
	return fromLease(lease), contractError(err)
}

func (s jobBinding) Complete(ctx context.Context, l Lease, r Result) (Receipt, error) {
	receipt, err := s.Store.Complete(ctx, toLease(l), jobs.Result(r))
	return fromReceipt(receipt), contractError(err)
}

func (s jobBinding) Fail(ctx context.Context, l Lease, f Failure) error {
	return contractError(s.Store.Fail(ctx, toLease(l), jobs.Failure(f)))
}

func (s jobBinding) Get(ctx context.Context, workspace, id string) (Job, error) {
	j, err := s.Store.Get(ctx, workspace, id)
	if err != nil {
		return Job{}, contractError(err)
	}
	result := Job{ID: j.ID, Envelope: fromEnvelope(j.Envelope), State: j.State, Attempts: j.Attempts, AvailableAt: j.AvailableAt}
	if j.Lease != nil {
		l := fromLease(*j.Lease)
		result.Lease = &l
	}
	if j.LastFailure != nil {
		f := Failure(*j.LastFailure)
		result.LastFailure = &f
	}
	if j.Receipt != nil {
		r := fromReceipt(*j.Receipt)
		result.Receipt = &r
	}
	return result, nil
}

type evidenceBinding struct{ *evidence.Store }

func (s evidenceBinding) Put(ctx context.Context, workspace, objectID string, r io.Reader, size int64) (EvidenceRef, error) {
	ref, err := s.Store.Put(ctx, workspace, objectID, r, size)
	return EvidenceRef(ref), contractError(err)
}

func (s evidenceBinding) Open(ctx context.Context, workspace string, ref EvidenceRef) (io.ReadCloser, error) {
	reader, err := s.Store.Open(ctx, workspace, evidence.Ref(ref))
	if err != nil {
		return nil, contractError(err)
	}
	return readerBinding{reader}, nil
}

type readerBinding struct{ io.ReadCloser }

func (r readerBinding) Read(p []byte) (int, error) {
	n, err := r.ReadCloser.Read(p)
	return n, contractError(err)
}

func (r readerBinding) Close() error { return contractError(r.ReadCloser.Close()) }
