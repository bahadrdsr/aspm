package analysis

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bahadrdsr/aspm/internal/evidence"
)

type evidenceFixture struct {
	data                       []byte
	ref                        evidence.Ref
	openErr, readErr, closeErr error
	opens, closes              atomic.Int32
}

type errorReader struct{ err error }

func (r errorReader) Read([]byte) (int, error) { return 0, r.err }

type fixtureReader struct {
	io.Reader
	owner *evidenceFixture
}

func (r fixtureReader) Close() error {
	r.owner.closes.Add(1)
	return r.owner.closeErr
}

func (f *evidenceFixture) Open(_ context.Context, workspace string, ref evidence.Ref) (io.ReadCloser, error) {
	f.opens.Add(1)
	if workspace != f.ref.WorkspaceID || ref != f.ref {
		return nil, evidence.ErrScope
	}
	if f.openErr != nil {
		return nil, f.openErr
	}
	var reader io.Reader = bytes.NewReader(f.data)
	if f.readErr != nil {
		reader = io.MultiReader(reader, errorReader{f.readErr})
	}
	return fixtureReader{reader, f}, nil
}

func verificationFixture(condition bool) (*evidenceFixture, VerifierConfig, VerificationRequest) {
	now := time.Date(2026, 9, 15, 10, 0, 0, 0, time.UTC)
	data := []byte(fmt.Sprintf(`{"schema":"aspm.synthetic-fixture/v1","environmentId":"synthetic-environment","condition":%t}`, condition))
	fixture := &evidenceFixture{data: data}
	request := VerificationRequest{
		WorkspaceID: "synthetic-workspace", FindingID: "synthetic-finding", RunID: "synthetic-proof-run",
		ApprovalRef: "synthetic-approval", Method: "deterministic-evidence", ScopeRevision: "synthetic-scope-v1", EnvironmentID: "synthetic-environment",
		Evidence: evidence.Ref{WorkspaceID: "synthetic-workspace", Bucket: "synthetic-fixture-bucket", Key: "synthetic/synthetic-workspace/report.json",
			SHA256: fmt.Sprintf("sha256:%x", sha256.Sum256(data)), SizeBytes: int64(len(data))},
	}
	fixture.ref = request.Evidence
	approval := Approval{ID: request.ApprovalRef, WorkspaceID: request.WorkspaceID, FindingID: request.FindingID,
		Method: request.Method, ScopeRevision: request.ScopeRevision, EnvironmentID: request.EnvironmentID,
		EvidenceSHA256: request.Evidence.SHA256, Approved: true, ExpiresAt: now.Add(time.Hour)}
	return fixture, VerifierConfig{Evidence: fixture, MaxBytes: 4096, Now: func() time.Time { return now },
		LookupApproval: func(_ context.Context, ref string) (Approval, error) {
			if ref != approval.ID {
				return Approval{}, ErrPolicy
			}
			return approval, nil
		}}, request
}

func TestM12DeterministicEvidenceOutcomes(t *testing.T) {
	for _, tc := range []struct {
		name, outcome string
		want          error
	}{
		{"condition-present", "reproduced", nil}, {"condition-absent", "not-reproduced", nil},
		{"stream-integrity", "error", evidence.ErrIntegrity}, {"close-integrity", "error", evidence.ErrIntegrity},
		{"missing-artifact", "error", evidence.ErrNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture, config, request := verificationFixture(tc.name != "condition-absent")
			switch tc.name {
			case "stream-integrity":
				fixture.readErr = evidence.ErrIntegrity
			case "close-integrity":
				fixture.closeErr = evidence.ErrIntegrity
			case "missing-artifact":
				fixture.openErr = evidence.ErrNotFound
			}
			result, err := openVerifier(t, config).Verify(boundedContext(t), request)
			check(t, errors.Is(err, tc.want) && result.Outcome == tc.outcome, "deterministic evidence condition/integrity outcome was conflated")
			check(t, !result.FalsePositive && !result.CloseFinding, "verification result classified a false positive or closed a finding")
			check(t, result.WorkspaceID == request.WorkspaceID && result.RunID == request.RunID && result.FindingID == request.FindingID && result.EnvironmentID == request.EnvironmentID && result.ScopeRevision == request.ScopeRevision, "proof outcome lost approved identity/environment snapshot")
			check(t, fixture.opens.Load() == 1, "verification did not delegate exactly one read to the existing evidence boundary")
			if fixture.openErr == nil {
				check(t, fixture.closes.Load() == 1, "verification ignored streaming/Close integrity semantics or leaked its reader")
			}
			if tc.want == nil {
				check(t, len(result.Artifacts) == 1 && result.Artifacts[0] == request.Evidence, "verified fixture outcome lost the exact evidence reference")
			}
		})
	}
}

func TestM12ApprovalScopeAndCancellation(t *testing.T) {
	for _, mode := range []string{"no-approval", "foreign-workspace", "foreign-environment", "expired-approval", "changed-evidence", "arbitrary-shell-method", "byte-budget", "cancelled"} {
		t.Run(mode, func(t *testing.T) {
			fixture, config, request := verificationFixture(true)
			ctx := boundedContext(t)
			want, outcome := ErrPolicy, "blocked"
			switch mode {
			case "no-approval":
				request.ApprovalRef = ""
			case "foreign-workspace":
				request.WorkspaceID = "other-workspace"
			case "foreign-environment":
				request.EnvironmentID = "other-environment"
			case "expired-approval":
				lookup := config.LookupApproval
				config.LookupApproval = func(ctx context.Context, ref string) (Approval, error) {
					approval, err := lookup(ctx, ref)
					approval.ExpiresAt = config.Now().Add(-time.Second)
					return approval, err
				}
			case "changed-evidence":
				request.Evidence.SHA256 = fmt.Sprintf("sha256:%x", sha256.Sum256([]byte("different synthetic evidence")))
			case "arbitrary-shell-method":
				request.Method = "arbitrary-shell"
			case "byte-budget":
				config.MaxBytes, want = 16, ErrLimit
			case "cancelled":
				cancelled, cancel := context.WithCancel(ctx)
				cancel()
				ctx, want, outcome = cancelled, context.Canceled, "cancelled"
			}
			result, err := openVerifier(t, config).Verify(ctx, request)
			check(t, errors.Is(err, want) && result.Outcome == outcome, "unapproved/out-of-scope/cancelled verification was not blocked explicitly")
			check(t, fixture.opens.Load() == 0 && !result.FalsePositive && !result.CloseFinding, "denied verification accessed evidence or changed finding semantics")
		})
	}
}
