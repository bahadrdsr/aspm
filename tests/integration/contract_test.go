//go:build integration

package integration

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"
)

const (
	EnvelopeVersion = "aspm/v1alpha1"
	IntegrityKind   = "evidence-integrity-check"
)

var (
	ErrNoJob     = errors.New("no eligible job")
	ErrInvalid   = errors.New("invalid contract input")
	ErrConflict  = errors.New("idempotency conflict")
	ErrLeaseLost = errors.New("lease expired or fenced")
	ErrNotFound  = errors.New("not found")
	ErrScope     = errors.New("workspace or object-prefix mismatch")
	ErrIntegrity = errors.New("evidence integrity mismatch")
)

type JobConfig struct {
	DatabaseURL     string `json:"-"`
	Schema          string
	ApplicationName string
	MaxConnections  int32
	MaxLease        time.Duration
	MaxAttempts     int
	RetryDelay      time.Duration
	MaxRetryDelay   time.Duration
}

type EvidenceConfig struct {
	Endpoint  string
	AccessKey string `json:"-"`
	SecretKey string `json:"-"`
	Bucket    string
	Prefix    string
	Region    string
	Timeout   time.Duration
}

type EvidenceRef struct {
	WorkspaceID string
	Bucket      string
	Key         string
	SHA256      string
	SizeBytes   int64
}

type Envelope struct {
	Version        string
	Kind           string
	WorkspaceID    string
	SourceID       string
	RunID          string
	BatchID        string
	ParserRevision string
	TraceID        string
	IdempotencyKey string
	MaxAttempts    int
	Evidence       EvidenceRef
}

type Enqueued struct {
	JobID   string
	Created bool
}

type Claim struct {
	WorkspaceID string
	WorkerID    string
	LeaseFor    time.Duration
}

type Lease struct {
	JobID     string
	Envelope  Envelope
	WorkerID  string
	Fence     int64
	Attempt   int
	ExpiresAt time.Time
}

type Result struct {
	SHA256    string
	SizeBytes int64
}

type Failure struct {
	Code      string
	Message   string
	Retryable bool
}

type Receipt struct {
	ID          string
	JobID       string
	Fence       int64
	Result      Result
	CompletedAt time.Time
}

type Job struct {
	ID          string
	Envelope    Envelope
	State       string
	Attempts    int
	AvailableAt time.Time
	Lease       *Lease
	LastFailure *Failure
	Receipt     *Receipt
}

type Jobs interface {
	Enqueue(context.Context, Envelope) (Enqueued, error)
	Claim(context.Context, Claim) (Lease, error)
	Heartbeat(context.Context, Lease, time.Duration) (Lease, error)
	Complete(context.Context, Lease, Result) (Receipt, error)
	Fail(context.Context, Lease, Failure) error
	Get(context.Context, string, string) (Job, error)
	Close() error
}

type Evidence interface {
	Put(context.Context, string, string, io.Reader, int64) (EvidenceRef, error)
	Open(context.Context, string, EvidenceRef) (io.ReadCloser, error)
	Close() error
}

type WorkerConfig struct {
	Jobs     JobConfig
	Evidence EvidenceConfig
	Claim    Claim
}

type WorkReport struct {
	Processed   bool
	JobID       string
	State       string
	FailureCode string
}

type Bindings struct {
	OpenJobs         func(context.Context, JobConfig) (Jobs, error)
	OpenEvidence     func(context.Context, EvidenceConfig) (Evidence, error)
	RunIntegrityOnce func(context.Context, WorkerConfig) (WorkReport, error)
}

// The coder supplies a forwarding-only production_bindings_test.go after implementing the real APIs.
var Production Bindings

func requireJobs(t *testing.T) {
	t.Helper()
	if Production.OpenJobs == nil {
		t.Fatal("M02 production binding missing: OpenJobs; add the real adapter in production_bindings_test.go")
	}
}

func requireEvidence(t *testing.T) {
	t.Helper()
	if Production.OpenEvidence == nil {
		t.Fatal("M02 production binding missing: OpenEvidence; add the real adapter in production_bindings_test.go")
	}
}

func requireWorker(t *testing.T) {
	t.Helper()
	if Production.RunIntegrityOnce == nil {
		t.Fatal("M02 production binding missing: RunIntegrityOnce; add the real adapter in production_bindings_test.go")
	}
}
