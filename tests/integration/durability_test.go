//go:build integration

package integration

import (
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"
)

func enqueue(t *testing.T, f *fixture, store Jobs, envelope Envelope) Enqueued {
	t.Helper()
	result, err := store.Enqueue(f.ctx, envelope)
	requireOK(t, "enqueue durable integrity job", err)
	if result.JobID == "" {
		t.Fatal("acknowledged enqueue returned no durable job identity")
	}
	return result
}

func claim(t *testing.T, f *fixture, store Jobs, worker string, duration time.Duration) Lease {
	t.Helper()
	result, err := store.Claim(f.ctx, Claim{WorkspaceID: f.workspace, WorkerID: worker, LeaseFor: duration})
	requireOK(t, "claim an eligible durable job", err)
	if result.JobID == "" || result.WorkerID != worker || result.Fence < 1 || result.Attempt < 1 {
		t.Fatal("claim lacks durable identity, owner, attempt or fencing token")
	}
	return result
}

func snapshot(t *testing.T, f *fixture, store Jobs, id string) Job {
	t.Helper()
	job, err := store.Get(f.ctx, f.workspace, id)
	requireOK(t, "read durable job diagnostics", err)
	if job.ID != id {
		t.Fatal("job diagnostics returned a different identity")
	}
	return job
}

func complete(t *testing.T, f *fixture, store Jobs, lease Lease) Receipt {
	t.Helper()
	result := Result{SHA256: lease.Envelope.Evidence.SHA256, SizeBytes: lease.Envelope.Evidence.SizeBytes}
	receipt, err := store.Complete(f.ctx, lease, result)
	requireOK(t, "complete a current lease with a durable receipt", err)
	if receipt.ID == "" || receipt.JobID != lease.JobID || receipt.Fence != lease.Fence || receipt.Result != result {
		t.Fatal("completion receipt does not identify the winning lease and exact result")
	}
	return receipt
}

func TestM02EnqueueIsIdempotentAndScoped(t *testing.T) {
	requireJobs(t)
	f := newFixture(t)
	store := f.openJobs(t, "-enqueue")
	request := f.envelope(t)
	type outcome struct {
		result Enqueued
		err    error
	}
	outcomes := make(chan outcome, 12)
	var group sync.WaitGroup
	for range 12 {
		group.Go(func() {
			result, err := store.Enqueue(f.ctx, request)
			outcomes <- outcome{result, err}
		})
	}
	group.Wait()
	close(outcomes)
	id, created := "", 0
	for item := range outcomes {
		requireOK(t, "concurrent idempotent enqueue", item.err)
		if id == "" {
			id = item.result.JobID
		}
		if id == "" || item.result.JobID != id {
			t.Fatal("the same scoped intent created different durable job IDs")
		}
		if item.result.Created {
			created++
		}
	}
	if created != 1 {
		t.Fatalf("concurrent enqueue created %d jobs; expected one", created)
	}
	if !reflect.DeepEqual(snapshot(t, f, store, id).Envelope, request) {
		t.Fatal("enqueue changed the immutable workspace/source/run/batch/trace/evidence envelope")
	}
	changed := request
	changed.Evidence = f.rawPut(t, f.workspace, identifier(t), []byte("different synthetic evidence"))
	_, err := store.Enqueue(f.ctx, changed)
	requireError(t, "same intent with changed evidence", err, ErrConflict)
	for _, axis := range []string{"workspace", "source", "run"} {
		other := request
		switch axis {
		case "workspace":
			other.WorkspaceID = identifier(t)
			other.Evidence = f.rawPut(t, other.WorkspaceID, identifier(t), []byte("other synthetic workspace"))
		case "source":
			other.SourceID = identifier(t)
		case "run":
			other.RunID = identifier(t)
		}
		separate := enqueue(t, f, store, other)
		if !separate.Created || separate.JobID == id {
			t.Fatalf("idempotency incorrectly crossed the %s identity boundary", axis)
		}
	}
	_, err = store.Get(f.ctx, identifier(t), id)
	requireError(t, "cross-workspace job lookup", err, ErrNotFound)
	requireOK(t, "close enqueue client", store.Close())
	reopened := f.openJobs(t, "-reopen")
	if !reflect.DeepEqual(snapshot(t, f, reopened, id).Envelope, request) {
		t.Fatal("acknowledged envelope did not survive closing and reopening the client")
	}
}

func TestM02ClaimsAreExclusiveAndBounded(t *testing.T) {
	requireJobs(t)
	f := newFixture(t)
	store := f.openJobs(t, "-claim")
	request := f.envelope(t)
	for _, mutate := range []func(*Envelope){
		func(e *Envelope) { e.Version = "unsupported" },
		func(e *Envelope) { e.Kind = "unsupported-arbitrary-execution" },
		func(e *Envelope) { e.WorkspaceID = "" },
		func(e *Envelope) { e.RunID = "" },
		func(e *Envelope) { e.MaxAttempts = 0 },
		func(e *Envelope) { e.MaxAttempts = f.jobs.MaxAttempts + 1 },
	} {
		invalid := request
		mutate(&invalid)
		_, err := store.Enqueue(f.ctx, invalid)
		requireError(t, "invalid job contract", err, ErrInvalid)
	}
	queued := enqueue(t, f, store, request)
	for _, duration := range []time.Duration{0, -time.Second, f.jobs.MaxLease + time.Second} {
		_, err := store.Claim(f.ctx, Claim{WorkspaceID: f.workspace, WorkerID: "synthetic-worker", LeaseFor: duration})
		requireError(t, "unbounded or invalid lease request", err, ErrInvalid)
	}
	type outcome struct {
		lease Lease
		err   error
	}
	results := make(chan outcome, 8)
	var workers sync.WaitGroup
	before := f.now(t)
	for range 8 {
		workerID := identifier(t)
		workers.Go(func() {
			value, err := store.Claim(f.ctx, Claim{WorkspaceID: f.workspace, WorkerID: workerID, LeaseFor: f.jobs.MaxLease})
			results <- outcome{value, err}
		})
	}
	workers.Wait()
	close(results)
	var winner *Lease
	for result := range results {
		if errors.Is(result.err, ErrNoJob) {
			continue
		}
		requireOK(t, "concurrent claim", result.err)
		if winner != nil {
			t.Fatal("multiple workers acquired the same unexpired lease")
		}
		value := result.lease
		winner = &value
	}
	if winner == nil || winner.JobID != queued.JobID || !reflect.DeepEqual(winner.Envelope, request) {
		t.Fatal("exclusive claim lost the acknowledged envelope")
	}
	after := f.now(t)
	if !winner.ExpiresAt.After(before) || winner.ExpiresAt.After(after.Add(f.jobs.MaxLease+100*time.Millisecond)) {
		t.Fatal("lease expiry is absent or exceeds the server-time bound")
	}
	_, err := store.Heartbeat(f.ctx, *winner, f.jobs.MaxLease+time.Second)
	requireError(t, "unbounded heartbeat", err, ErrInvalid)
	var connections int
	requireOK(t, "inspect only the owned connection-pool label", f.db.QueryRow(f.ctx,
		"SELECT count(*) FROM pg_stat_activity WHERE application_name=$1", f.jobs.ApplicationName+"-claim").Scan(&connections))
	if connections > int(f.jobs.MaxConnections) {
		t.Fatal("production job client exceeded its declared connection-pool budget")
	}
}

func TestM02HeartbeatAndReclaimFenceStaleWorkers(t *testing.T) {
	requireJobs(t)
	f := newFixture(t)
	store := f.openJobs(t, "-heartbeat")
	queued := enqueue(t, f, store, f.envelope(t))
	first := claim(t, f, store, "synthetic-worker-a", 750*time.Millisecond)
	renewed, err := store.Heartbeat(f.ctx, first, f.jobs.MaxLease)
	requireOK(t, "renew the current worker's lease", err)
	if renewed.JobID != first.JobID || renewed.Fence != first.Fence || renewed.WorkerID != first.WorkerID ||
		!reflect.DeepEqual(renewed.Envelope, first.Envelope) || !renewed.ExpiresAt.After(first.ExpiresAt) {
		t.Fatal("heartbeat changed ownership/fence or failed to extend the lease")
	}
	if renewed.ExpiresAt.After(f.now(t).Add(f.jobs.MaxLease + 100*time.Millisecond)) {
		t.Fatal("heartbeat accumulated an unbounded future lease")
	}
	f.after(t, first.ExpiresAt.Add(40*time.Millisecond))
	_, err = store.Claim(f.ctx, Claim{WorkspaceID: f.workspace, WorkerID: "synthetic-worker-b", LeaseFor: f.jobs.MaxLease})
	requireError(t, "claim after the old expiry but before renewed expiry", err, ErrNoJob)
	f.after(t, renewed.ExpiresAt.Add(40*time.Millisecond))
	originalResult := Result{SHA256: first.Envelope.Evidence.SHA256, SizeBytes: first.Envelope.Evidence.SizeBytes}
	_, err = store.Heartbeat(f.ctx, renewed, time.Second)
	requireError(t, "expired lease cannot resurrect itself before reclaim", err, ErrLeaseLost)
	_, err = store.Complete(f.ctx, renewed, originalResult)
	requireError(t, "expired lease cannot complete before reclaim", err, ErrLeaseLost)
	second := claim(t, f, store, "synthetic-worker-b", f.jobs.MaxLease)
	if second.JobID != queued.JobID || second.Fence <= first.Fence || second.Attempt != 2 ||
		!reflect.DeepEqual(second.Envelope, first.Envelope) {
		t.Fatal("expired lease was not reclaimed with a newer fence and bounded attempt accounting")
	}
	_, err = store.Heartbeat(f.ctx, first, time.Second)
	requireError(t, "stale heartbeat", err, ErrLeaseLost)
	_, err = store.Complete(f.ctx, first, originalResult)
	requireError(t, "stale completion", err, ErrLeaseLost)
	err = store.Fail(f.ctx, first, Failure{Code: "synthetic-stale", Message: "Controlled stale-worker report", Retryable: true})
	requireError(t, "stale failure report", err, ErrLeaseLost)
	receipt := complete(t, f, store, second)
	repeated := complete(t, f, store, second)
	if receipt.ID != repeated.ID {
		t.Fatal("retrying an acknowledged completion created a second processing receipt")
	}
	final := snapshot(t, f, store, queued.JobID)
	if final.State != "succeeded" || final.Receipt == nil || final.Receipt.ID != receipt.ID {
		t.Fatal("the winning completion receipt is not durably visible")
	}
	_, err = store.Complete(f.ctx, first, receipt.Result)
	requireError(t, "stale completion after another worker succeeded", err, ErrLeaseLost)
}

func TestM02KilledWorkerRecoveryPreservesAcknowledgedWork(t *testing.T) {
	requireJobs(t)
	f := newFixture(t)
	store := f.openJobs(t, "-parent")
	request := f.envelope(t)
	queued := enqueue(t, f, store, request)
	child, report := startChild(t, f, childRequest{
		Mode: "hold-lease", WorkspaceID: f.workspace, WorkerID: "synthetic-killed-worker", LeaseFor: f.jobs.MaxLease,
	})
	if report.Lease == nil || report.Lease.JobID != queued.JobID {
		t.Fatal("independent worker could not see and claim the acknowledged PostgreSQL job")
	}
	if snapshot(t, f, store, queued.JobID).State != "leased" {
		t.Fatal("child claim was not committed before the worker acknowledged it")
	}
	child.kill(t)
	requireOK(t, "close the parent job client", store.Close())
	reopened := f.openJobs(t, "-replacement")
	if !reflect.DeepEqual(snapshot(t, f, reopened, queued.JobID).Envelope, request) {
		t.Fatal("acknowledged work disappeared after worker termination and client reopen")
	}
	f.after(t, report.Lease.ExpiresAt.Add(40*time.Millisecond))
	reclaimed := claim(t, f, reopened, "synthetic-replacement-worker", f.jobs.MaxLease)
	if reclaimed.JobID != queued.JobID || reclaimed.Fence <= report.Lease.Fence {
		t.Fatal("replacement worker did not reclaim the original job with a newer fence")
	}
	_, err := reopened.Complete(f.ctx, *report.Lease, Result{SHA256: request.Evidence.SHA256, SizeBytes: request.Evidence.SizeBytes})
	requireError(t, "terminated worker's late completion", err, ErrLeaseLost)
	complete(t, f, reopened, reclaimed)
}

func TestM02RetriesAndLeaseExhaustionAreBoundedAndVisible(t *testing.T) {
	requireJobs(t)
	f := newFixture(t)
	store := f.openJobs(t, "-retry")
	queued := enqueue(t, f, store, f.envelope(t))
	first := claim(t, f, store, "synthetic-retry-a", f.jobs.MaxLease)
	before := f.now(t)
	requireOK(t, "record a retryable synthetic failure", store.Fail(f.ctx, first,
		Failure{Code: "synthetic-transient", Message: "Controlled retry fixture", Retryable: true}))
	waiting := snapshot(t, f, store, queued.JobID)
	if waiting.State != "retry-wait" || waiting.Attempts != 1 || waiting.LastFailure == nil ||
		waiting.LastFailure.Code != "synthetic-transient" || waiting.AvailableAt.Before(before.Add(f.jobs.RetryDelay)) ||
		waiting.AvailableAt.After(f.now(t).Add(f.jobs.MaxRetryDelay+100*time.Millisecond)) {
		t.Fatal("retry state, cause, attempt count or bounded backoff is not visible")
	}
	_, err := store.Claim(f.ctx, Claim{WorkspaceID: f.workspace, WorkerID: "too-early", LeaseFor: f.jobs.MaxLease})
	requireError(t, "claim before retry backoff elapses", err, ErrNoJob)
	other := f.envelope(t)
	other.WorkspaceID = identifier(t)
	other.Evidence = f.rawPut(t, other.WorkspaceID, identifier(t), []byte("unrelated synthetic workspace"))
	enqueue(t, f, store, other)
	unrelated, err := store.Claim(f.ctx, Claim{WorkspaceID: other.WorkspaceID, WorkerID: "unrelated-worker", LeaseFor: f.jobs.MaxLease})
	requireOK(t, "unrelated workspace makes progress during retry backoff", err)
	complete(t, f, store, unrelated)
	f.after(t, waiting.AvailableAt.Add(20*time.Millisecond))
	second := claim(t, f, store, "synthetic-retry-b", f.jobs.MaxLease)
	requireOK(t, "record the final retryable failure", store.Fail(f.ctx, second,
		Failure{Code: "synthetic-exhausted", Message: "Controlled exhaustion fixture", Retryable: true}))
	failed := snapshot(t, f, store, queued.JobID)
	if failed.State != "failed" || failed.Attempts != 2 || failed.LastFailure == nil ||
		failed.LastFailure.Code != "synthetic-exhausted" || failed.Receipt != nil {
		t.Fatal("exhausted retries were hidden, reset or reported as successful")
	}
	_, err = store.Claim(f.ctx, Claim{WorkspaceID: f.workspace, WorkerID: "third-attempt", LeaseFor: f.jobs.MaxLease})
	requireError(t, "retry after attempt exhaustion", err, ErrNoJob)
	abandoned := f.envelope(t)
	abandoned.MaxAttempts = 1
	last := enqueue(t, f, store, abandoned)
	expiring := claim(t, f, store, "synthetic-expired-final-attempt", 500*time.Millisecond)
	f.after(t, expiring.ExpiresAt.Add(40*time.Millisecond))
	_, err = store.Claim(f.ctx, Claim{WorkspaceID: f.workspace, WorkerID: "not-a-new-attempt", LeaseFor: f.jobs.MaxLease})
	requireError(t, "reclaim beyond the maximum attempt count", err, ErrNoJob)
	exhausted := snapshot(t, f, store, last.JobID)
	if exhausted.State != "failed" || exhausted.Attempts != 1 || exhausted.LastFailure == nil || exhausted.LastFailure.Code != "lease-expired" {
		t.Fatal("final expired lease remained stuck or silently gained unlimited attempts")
	}
}
