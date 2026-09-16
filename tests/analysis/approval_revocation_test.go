package analysis

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bahadrdsr/aspm/internal/evidence"
)

type approvalEvidenceFunc func(context.Context, string, evidence.Ref) (io.ReadCloser, error)

func (f approvalEvidenceFunc) Open(ctx context.Context, workspace string, ref evidence.Ref) (io.ReadCloser, error) {
	return f(ctx, workspace, ref)
}

type approvalReadGate struct {
	started, terminated, closed    chan struct{}
	startOnce, stopOnce, closeOnce sync.Once
	opens, reads, closes           atomic.Int32
}

type approvalBlockedReader struct {
	ctx  context.Context
	gate *approvalReadGate
}

func (r *approvalBlockedReader) Read([]byte) (int, error) {
	r.gate.reads.Add(1)
	r.gate.startOnce.Do(func() { close(r.gate.started) })
	select {
	case <-r.ctx.Done():
		r.gate.stopOnce.Do(func() { close(r.gate.terminated) })
		return 0, r.ctx.Err()
	case <-r.gate.closed:
		r.gate.stopOnce.Do(func() { close(r.gate.terminated) })
		return 0, io.ErrClosedPipe
	}
}

func (r *approvalBlockedReader) Close() error {
	r.gate.closes.Add(1)
	r.gate.closeOnce.Do(func() { close(r.gate.closed) })
	return nil
}

type approvalOutcome struct {
	result VerificationResult
	err    error
}

func TestM12ApprovalChangesTerminateBlockedRead(t *testing.T) {
	for _, mode := range []string{"expires", "revoked", "caller-cancelled"} {
		t.Run(mode, func(t *testing.T) {
			_, config, request := verificationFixture(true)
			config.Now = time.Now
			var revoked atomic.Bool
			var lookups atomic.Int32
			validUntil := time.Now().Add(10 * time.Second)
			if mode == "expires" {
				validUntil = time.Now().Add(time.Second)
			}
			lookup := config.LookupApproval
			config.LookupApproval = func(ctx context.Context, id string) (Approval, error) {
				lookups.Add(1)
				approval, err := lookup(ctx, id)
				approval.ExpiresAt, approval.Approved = validUntil, !revoked.Load()
				return approval, err
			}
			gate := &approvalReadGate{started: make(chan struct{}), terminated: make(chan struct{}), closed: make(chan struct{})}
			config.Evidence = approvalEvidenceFunc(func(ctx context.Context, workspace string, ref evidence.Ref) (io.ReadCloser, error) {
				gate.opens.Add(1)
				if workspace != request.WorkspaceID || ref != request.Evidence {
					return nil, evidence.ErrScope
				}
				return &approvalBlockedReader{ctx: ctx, gate: gate}, nil
			})
			caller, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			t.Cleanup(cancel)
			if Production.OpenVerifier == nil {
				t.Fatal("M12 production binding missing: OpenVerifier")
			}
			// Keep the constructor/caller deadline outside the policy termination window.
			verifier, err := Production.OpenVerifier(caller, config)
			requireOK(t, err)
			if verifier == nil {
				t.Fatal("OpenVerifier returned nil")
			}
			outcomes := make(chan approvalOutcome, 1)
			finished := make(chan struct{})
			t.Cleanup(func() {
				cancel()
				gate.closeOnce.Do(func() { close(gate.closed) })
				select {
				case <-finished:
				case <-time.After(2 * time.Second):
					t.Error("verifier did not terminate even after fixture cleanup cancellation")
				}
			})
			go func() {
				result, err := verifier.Verify(caller, request)
				outcomes <- approvalOutcome{result, err}
				close(finished)
			}()
			select {
			case <-gate.started:
			case <-finished:
				t.Fatal("verification finished without entering the approved blocked read")
			case <-time.After(2 * time.Second):
				t.Fatal("verification did not begin the approved evidence read")
			}
			if lookups.Load() == 0 {
				t.Fatal("evidence read began without consulting trusted approval")
			}
			switch mode {
			case "expires":
				timer := time.NewTimer(time.Until(validUntil))
				defer timer.Stop()
				select {
				case <-timer.C:
				case <-gate.terminated:
					if time.Now().Before(validUntil) {
						t.Fatal("approved evidence read terminated before approval expiry")
					}
				}
			case "revoked":
				select {
				case <-gate.terminated:
					t.Fatal("approved evidence read terminated before revocation")
				default:
				}
				revoked.Store(true)
			case "caller-cancelled":
				cancel()
			}
			// No data is released: policy must stop I/O, not finish it and discard a result.
			select {
			case <-gate.terminated:
			case <-time.After(2 * time.Second):
				t.Fatalf("%s approval/caller change left the evidence Read blocked; active cancellation/termination is required", mode)
			}
			var outcome approvalOutcome
			select {
			case outcome = <-outcomes:
			case <-time.After(2 * time.Second):
				t.Fatal("stopped evidence read did not terminate verification")
			}
			if mode == "caller-cancelled" {
				check(t, errors.Is(outcome.err, context.Canceled) && !errors.Is(outcome.err, ErrPolicy) && outcome.result.Outcome == "cancelled",
					"caller cancellation was conflated with approval expiry/revocation")
			} else {
				check(t, caller.Err() == nil && errors.Is(outcome.err, ErrPolicy) && outcome.result.Outcome == "blocked",
					"expired/revoked approval must produce a policy-blocked result while the caller remains active")
				if mode == "revoked" {
					check(t, lookups.Load() >= 2, "revocation was not observed through current trusted approval")
				}
			}
			check(t, gate.opens.Load() == 1 && gate.reads.Load() == 1 && gate.closes.Load() >= 1,
				"termination retried/later opened evidence, resumed reads or leaked the reader")
			check(t, len(outcome.result.Artifacts) == 0 && !outcome.result.CloseFinding && !outcome.result.FalsePositive,
				"cancelled or policy-blocked verification published success artifacts or finding decisions")
		})
	}
}

type approvalCloseReader struct {
	io.ReadCloser
	revoke func()
}

func (r approvalCloseReader) Close() error {
	err := r.ReadCloser.Close()
	r.revoke()
	return err
}

func TestM12ApprovalIsCurrentAfterEvidenceClose(t *testing.T) {
	fixture, config, request := verificationFixture(true)
	config.Now = time.Now
	var revoked atomic.Bool
	var postCloseLookups atomic.Int32
	lookup := config.LookupApproval
	config.LookupApproval = func(ctx context.Context, id string) (Approval, error) {
		approval, err := lookup(ctx, id)
		approval.ExpiresAt = time.Now().Add(time.Minute)
		approval.Approved = !revoked.Load()
		if !approval.Approved {
			postCloseLookups.Add(1)
		}
		return approval, err
	}
	config.Evidence = approvalEvidenceFunc(func(ctx context.Context, workspace string, ref evidence.Ref) (io.ReadCloser, error) {
		reader, err := fixture.Open(ctx, workspace, ref)
		if err != nil {
			return nil, err
		}
		return approvalCloseReader{ReadCloser: reader, revoke: func() { revoked.Store(true) }}, nil
	})
	result, err := openVerifier(t, config).Verify(boundedContext(t), request)
	check(t, fixture.opens.Load() == 1 && fixture.closes.Load() == 1 && revoked.Load(), "fixture did not revoke at the completed evidence boundary")
	check(t, postCloseLookups.Load() > 0, "final result used an approval snapshot from before evidence EOF/Close")
	check(t, errors.Is(err, ErrPolicy) && result.Outcome == "blocked" && len(result.Artifacts) == 0,
		"final approval revocation was ignored or success artifacts were returned")
	check(t, !result.CloseFinding && !result.FalsePositive, "revocation changed canonical finding decisions")
}
