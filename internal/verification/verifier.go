package verification

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/bahadrdsr/aspm/internal/evidence"
)

var (
	ErrPolicy = errors.New("verification approval or scope denied")
	ErrLimit  = errors.New("verification evidence exceeds its limit")
	ErrFormat = errors.New("unsupported deterministic fixture")
)

type EvidenceReader interface {
	Open(context.Context, string, evidence.Ref) (io.ReadCloser, error)
}

type Approval struct {
	ID, WorkspaceID, FindingID, Method, ScopeRevision, EnvironmentID, EvidenceSHA256 string
	Approved                                                                         bool
	ExpiresAt                                                                        time.Time
}

type Config struct {
	Evidence       EvidenceReader
	LookupApproval func(context.Context, string) (Approval, error)
	Now            func() time.Time
	MaxBytes       int64
}

type Request struct {
	WorkspaceID, FindingID, RunID, ApprovalRef, Method, ScopeRevision, EnvironmentID string
	Evidence                                                                         evidence.Ref
}

type Result struct {
	WorkspaceID, FindingID, RunID, EnvironmentID, ScopeRevision, Outcome string
	Artifacts                                                            []evidence.Ref
	CloseFinding, FalsePositive                                          bool
}

type Verifier struct{ config Config }

func Open(_ context.Context, config Config) (*Verifier, error) {
	if config.Evidence == nil || config.LookupApproval == nil || config.Now == nil {
		return nil, ErrPolicy
	}
	if config.MaxBytes < 1 || config.MaxBytes > 16<<20 {
		return nil, ErrLimit
	}
	return &Verifier{config: config}, nil
}

func (v *Verifier) Verify(ctx context.Context, request Request) (result Result, err error) {
	result = Result{
		WorkspaceID: request.WorkspaceID, FindingID: request.FindingID, RunID: request.RunID,
		EnvironmentID: request.EnvironmentID, ScopeRevision: request.ScopeRevision, Outcome: "blocked",
	}
	if ctx.Err() != nil {
		result.Outcome = "cancelled"
		return result, ctx.Err()
	}
	if request.Method != "deterministic-evidence" || request.ApprovalRef == "" || request.WorkspaceID == "" ||
		request.FindingID == "" || request.RunID == "" || request.EnvironmentID == "" || request.ScopeRevision == "" {
		return result, ErrPolicy
	}
	if request.Evidence.SizeBytes < 0 || request.Evidence.SizeBytes > v.config.MaxBytes {
		return result, ErrLimit
	}
	approval, err := v.config.LookupApproval(ctx, request.ApprovalRef)
	if ctx.Err() != nil {
		result.Outcome = "cancelled"
		return result, ctx.Err()
	}
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			result.Outcome = "cancelled"
			return result, err
		}
		return result, ErrPolicy
	}
	if !v.matches(approval, request) {
		return result, ErrPolicy
	}
	if ctx.Err() != nil {
		result.Outcome = "cancelled"
		return result, ctx.Err()
	}
	access, cancelAccess, stopAccess := v.guard(ctx, approval, request)
	defer stopAccess()
	reader, err := v.config.Evidence.Open(access, request.WorkspaceID, request.Evidence)
	if err != nil {
		result.Outcome, err = accessFailure(ctx, access, err)
		return result, err
	}
	if reader == nil {
		result.Outcome = "error"
		return result, errors.New("evidence reader is unavailable")
	}
	gated := &authorizedReader{
		reader: reader, validate: func() error { return v.current(access, request) }, denied: cancelAccess,
	}
	data, readErr := io.ReadAll(io.LimitReader(gated, v.config.MaxBytes+1))
	if readErr != nil {
		cancelAccess(readErr)
	} else if int64(len(data)) > v.config.MaxBytes {
		cancelAccess(ErrLimit)
	}
	closeErr := reader.Close()
	if ctx.Err() != nil {
		result.Outcome = "cancelled"
		return result, ctx.Err()
	}
	if err := errors.Join(readErr, closeErr); err != nil {
		result.Outcome, err = accessFailure(ctx, access, err)
		return result, err
	}
	if err := v.current(access, request); err != nil {
		result.Outcome, err = accessFailure(ctx, access, err)
		return result, err
	}
	if int64(len(data)) > v.config.MaxBytes {
		return result, ErrLimit
	}
	if int64(len(data)) != request.Evidence.SizeBytes ||
		fmt.Sprintf("sha256:%x", sha256.Sum256(data)) != request.Evidence.SHA256 {
		result.Outcome = "error"
		return result, evidence.ErrIntegrity
	}
	var fixture struct {
		Schema        string `json:"schema"`
		EnvironmentID string `json:"environmentId"`
		Condition     *bool  `json:"condition"`
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&fixture); err != nil || fixture.Schema != "aspm.synthetic-fixture/v1" ||
		fixture.EnvironmentID != request.EnvironmentID || fixture.Condition == nil {
		result.Outcome = "error"
		return result, ErrFormat
	}
	if err := decoder.Decode(new(any)); !errors.Is(err, io.EOF) {
		result.Outcome = "error"
		return result, ErrFormat
	}
	if ctx.Err() != nil {
		result.Outcome = "cancelled"
		return result, ctx.Err()
	}
	if err := v.current(access, request); err != nil {
		result.Outcome, err = accessFailure(ctx, access, err)
		return result, err
	}
	result.Outcome = "not-reproduced"
	if *fixture.Condition {
		result.Outcome = "reproduced"
	}
	result.Artifacts = []evidence.Ref{request.Evidence}
	return result, nil
}

func (v *Verifier) matches(approval Approval, request Request) bool {
	return approval.Approved && approval.ExpiresAt.After(v.config.Now()) &&
		approval.ID == request.ApprovalRef && approval.WorkspaceID == request.WorkspaceID &&
		approval.FindingID == request.FindingID && approval.Method == request.Method &&
		approval.ScopeRevision == request.ScopeRevision && approval.EnvironmentID == request.EnvironmentID &&
		request.Evidence.WorkspaceID == request.WorkspaceID && approval.EvidenceSHA256 == request.Evidence.SHA256
}

func (v *Verifier) current(ctx context.Context, request Request) error {
	if err := context.Cause(ctx); err != nil {
		return err
	}
	lookup, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	approval, err := v.config.LookupApproval(lookup, request.ApprovalRef)
	if cause := context.Cause(ctx); cause != nil {
		return cause
	}
	if err != nil || !v.matches(approval, request) {
		return ErrPolicy
	}
	return nil
}

// The derived context interrupts in-flight, context-aware storage I/O rather
// than waiting for the read to finish and only then rejecting its result.
func (v *Verifier) guard(parent context.Context, approval Approval, request Request) (context.Context, context.CancelCauseFunc, func()) {
	ctx, cancel := context.WithCancelCause(parent)
	timer := time.AfterFunc(approval.ExpiresAt.Sub(v.config.Now()), func() { cancel(ErrPolicy) })
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if err := v.current(ctx, request); err != nil {
					if parent.Err() == nil && ctx.Err() == nil {
						cancel(ErrPolicy)
					}
					return
				}
			}
		}
	}()
	return ctx, cancel, func() {
		timer.Stop()
		cancel(nil)
		<-done
	}
}

type authorizedReader struct {
	reader   io.Reader
	validate func() error
	denied   context.CancelCauseFunc
}

func (r *authorizedReader) Read(p []byte) (int, error) {
	if err := r.validate(); err != nil {
		r.denied(err)
		return 0, err
	}
	return r.reader.Read(p)
}

func accessFailure(parent, access context.Context, err error) (string, error) {
	if parent.Err() != nil {
		return "cancelled", parent.Err()
	}
	if errors.Is(context.Cause(access), ErrPolicy) || errors.Is(err, ErrPolicy) {
		return "blocked", ErrPolicy
	}
	if errors.Is(context.Cause(access), ErrLimit) || errors.Is(err, ErrLimit) {
		return "blocked", ErrLimit
	}
	if cause := context.Cause(access); cause != nil {
		return boundaryOutcome(cause), errors.Join(cause, err)
	}
	return boundaryOutcome(err), err
}

func boundaryOutcome(err error) string {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return "cancelled"
	}
	if errors.Is(err, evidence.ErrScope) {
		return "blocked"
	}
	return "error"
}
