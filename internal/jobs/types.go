package jobs

import (
	"errors"
	"time"

	"github.com/bahadrdsr/aspm/internal/evidence"
)

var (
	ErrNoJob     = errors.New("no eligible job")
	ErrInvalid   = errors.New("invalid job input")
	ErrConflict  = errors.New("idempotency conflict")
	ErrLeaseLost = errors.New("lease expired or fenced")
	ErrNotFound  = errors.New("job not found")
)

const (
	Version       = "aspm/v1alpha1"
	IntegrityKind = "evidence-integrity-check"
)

type Config struct {
	DatabaseURL     string `json:"-"`
	Schema          string
	ApplicationName string
	MaxConnections  int32
	MaxLease        time.Duration
	MaxAttempts     int
	RetryDelay      time.Duration
	MaxRetryDelay   time.Duration
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
	Evidence       evidence.Ref
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
