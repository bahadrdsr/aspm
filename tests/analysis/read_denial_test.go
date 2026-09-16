package analysis

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/bahadrdsr/aspm/internal/evidence"
)

type synchronousDenialState struct {
	firstReadActive, revoked, denied, monitorParked atomic.Bool
	closeWithoutCancellation                        atomic.Bool
	opens, reads, closes, deniedLookups             atomic.Int32
	afterDenialReads                                atomic.Int32
	prefixBytes, afterDenialBytes                   atomic.Int64
	monitorEntered, releaseMonitor                  chan struct{}
	releaseOnce                                     sync.Once
}

func (s *synchronousDenialState) release() {
	s.releaseOnce.Do(func() { close(s.releaseMonitor) })
}

type synchronousDenialReader struct {
	ctx       context.Context
	state     *synchronousDenialState
	data      *bytes.Reader
	mu        sync.Mutex
	closeOnce sync.Once
	closeErr  error
}

func (r *synchronousDenialReader) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	call := r.state.reads.Add(1)
	afterDenial := r.state.denied.Load()
	if afterDenial {
		r.state.afterDenialReads.Add(1)
	}
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	if call == 1 {
		r.state.firstReadActive.Store(true)
		// Park a lookup that started before revocation so no monitor tick can
		// supply the cancellation required from the next synchronous guard.
		select {
		case <-r.state.monitorEntered:
		case <-r.ctx.Done():
			return 0, r.ctx.Err()
		}
		r.mu.Lock()
		n, err := r.data.Read(p[:min(len(p), 8)])
		r.mu.Unlock()
		r.state.prefixBytes.Add(int64(n))
		r.state.revoked.Store(true)
		r.state.firstReadActive.Store(false)
		return n, err
	}
	r.mu.Lock()
	n, err := r.data.Read(p)
	r.mu.Unlock()
	if afterDenial {
		r.state.afterDenialBytes.Add(int64(n))
	}
	return n, err
}

func (r *synchronousDenialReader) Close() error {
	r.state.closes.Add(1)
	if r.ctx.Err() == nil {
		r.state.closeWithoutCancellation.Store(true)
	}
	r.closeOnce.Do(func() {
		defer r.state.release()
		if err := r.ctx.Err(); err != nil {
			r.closeErr = err
			return
		}
		// Model the unfinished evidence reader's real drain-on-Close behavior.
		_, r.closeErr = io.Copy(io.Discard, r)
	})
	return r.closeErr
}

func TestM12SynchronousReadDenialCancelsBeforeCloseDrain(t *testing.T) {
	fixture, config, request := verificationFixture(true)
	state := &synchronousDenialState{monitorEntered: make(chan struct{}), releaseMonitor: make(chan struct{})}
	t.Cleanup(state.release)
	lookup := config.LookupApproval
	config.LookupApproval = func(ctx context.Context, id string) (Approval, error) {
		approval, err := lookup(ctx, id)
		if err != nil {
			return approval, err
		}
		if state.firstReadActive.Load() && state.monitorParked.CompareAndSwap(false, true) {
			close(state.monitorEntered)
			select {
			case <-state.releaseMonitor:
				return approval, nil
			case <-ctx.Done():
				return Approval{}, ctx.Err()
			}
		}
		if state.revoked.Load() {
			state.denied.Store(true)
			state.deniedLookups.Add(1)
			approval.Approved = false
		}
		return approval, nil
	}
	config.Evidence = approvalEvidenceFunc(func(ctx context.Context, workspace string, ref evidence.Ref) (io.ReadCloser, error) {
		state.opens.Add(1)
		if workspace != request.WorkspaceID || ref != request.Evidence {
			return nil, evidence.ErrScope
		}
		return &synchronousDenialReader{ctx: ctx, state: state, data: bytes.NewReader(fixture.data)}, nil
	})
	if Production.OpenVerifier == nil {
		t.Fatal("M12 production binding missing: OpenVerifier")
	}
	caller := boundedContext(t)
	verifier, err := Production.OpenVerifier(caller, config)
	requireOK(t, err)
	if verifier == nil {
		t.Fatal("OpenVerifier returned nil")
	}
	result, err := verifier.Verify(caller, request)
	check(t, caller.Err() == nil, "caller deadline/cancellation must not supply the required policy cancellation")
	check(t, state.opens.Load() == 1 && state.prefixBytes.Load() > 0 && state.prefixBytes.Load() < int64(len(fixture.data)),
		"fixture must open once and return only a bounded prefix before revocation")
	check(t, state.monitorParked.Load() && state.deniedLookups.Load() > 0,
		"a current synchronous approval lookup must deny while the older monitor lookup is parked")
	check(t, state.closes.Load() >= 1, "denied verification leaked its evidence reader")
	check(t, !state.closeWithoutCancellation.Load(), "known read denial entered Close before cancelling the derived evidence-access context")
	check(t, state.reads.Load() == 1 && state.afterDenialReads.Load() == 0 && state.afterDenialBytes.Load() == 0,
		fmt.Sprintf("underlying evidence reads after denial: calls=%d bytes=%d; Close must not drain unauthorized data",
			state.afterDenialReads.Load(), state.afterDenialBytes.Load()))
	check(t, errors.Is(err, ErrPolicy) && result.Outcome == "blocked", "synchronous read denial must remain ErrPolicy/blocked")
	check(t, len(result.Artifacts) == 0 && !result.CloseFinding && !result.FalsePositive,
		"denied verification published artifacts or changed finding flags")
	check(t, result.WorkspaceID == request.WorkspaceID && result.FindingID == request.FindingID &&
		result.RunID == request.RunID && result.EnvironmentID == request.EnvironmentID && result.ScopeRevision == request.ScopeRevision,
		"blocked verification changed the approved finding/run/scope identity")
}
