package jobs

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Store struct {
	pool   *pgxpool.Pool
	config Config
	table  string
}

var schemaPattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,62}$`)
var digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

func Open(ctx context.Context, config Config) (*Store, error) {
	if !schemaPattern.MatchString(config.Schema) || config.DatabaseURL == "" ||
		config.ApplicationName == "" || config.MaxConnections < 1 || config.MaxConnections > 100 ||
		config.MaxLease <= 0 || config.MaxLease > 10*time.Minute ||
		config.MaxAttempts < 1 || config.MaxAttempts > 20 ||
		config.RetryDelay <= 0 || config.MaxRetryDelay < config.RetryDelay {
		return nil, ErrInvalid
	}
	pc, err := pgxpool.ParseConfig(config.DatabaseURL)
	if err != nil {
		return nil, errors.New("invalid database configuration; connection value withheld")
	}
	pc.MaxConns = config.MaxConnections
	pc.MinConns = 0
	pc.ConnConfig.ConnectTimeout = 5 * time.Second
	pc.ConnConfig.RuntimeParams["application_name"] = config.ApplicationName
	s := &Store{config: config, table: pgx.Identifier{config.Schema, "jobs"}.Sanitize()}
	s.pool, err = pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, errors.New("open job database connection pool failed")
	}
	if err := s.migrate(ctx); err != nil {
		s.pool.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) migrate(ctx context.Context) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin job migration: %w", err)
	}
	defer rollback(tx)
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock(hashtextextended($1, 0))", "aspm/jobs/"+s.config.Schema); err != nil {
		return fmt.Errorf("lock job migration: %w", err)
	}
	_, err = tx.Exec(ctx, `CREATE TABLE IF NOT EXISTS `+s.table+` (
		id text PRIMARY KEY,
		workspace_id text NOT NULL,
		source_id text NOT NULL,
		run_id text NOT NULL,
		kind text NOT NULL,
		idempotency_key text NOT NULL,
		envelope jsonb NOT NULL,
		state text NOT NULL DEFAULT 'queued' CHECK (state IN ('queued','leased','retry-wait','succeeded','failed')),
		attempts integer NOT NULL DEFAULT 0 CHECK (attempts >= 0),
		max_attempts integer NOT NULL CHECK (max_attempts BETWEEN 1 AND 20),
		fence bigint NOT NULL DEFAULT 0,
		worker_id text,
		expires_at timestamptz,
		available_at timestamptz NOT NULL DEFAULT clock_timestamp(),
		created_at timestamptz NOT NULL DEFAULT clock_timestamp(),
		last_failure jsonb,
		receipt jsonb,
		UNIQUE (workspace_id,source_id,run_id,kind,idempotency_key)
	);
	CREATE INDEX IF NOT EXISTS jobs_claim_idx ON `+s.table+` (workspace_id, available_at, created_at)
		WHERE state IN ('queued','retry-wait','leased');`)
	if err != nil {
		return fmt.Errorf("apply job migration: %w", err)
	}
	return tx.Commit(ctx)
}

func rollback(tx pgx.Tx) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = tx.Rollback(ctx) // A committed/closed transaction needs no second action.
}

func (s *Store) Close() error {
	s.pool.Close()
	return nil
}

func (s *Store) Ping(ctx context.Context) error { return s.pool.Ping(ctx) }

func (s *Store) PendingWorkspaces(ctx context.Context, limit int) ([]string, error) {
	if limit < 1 || limit > 1000 {
		return nil, ErrInvalid
	}
	rows, err := s.pool.Query(ctx, `SELECT workspace_id FROM `+s.table+`
		WHERE (state IN ('queued','retry-wait') AND available_at<=clock_timestamp())
		OR (state='leased' AND expires_at<=clock_timestamp())
		GROUP BY workspace_id ORDER BY min(available_at) LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var workspaces []string
	for rows.Next() {
		var workspace string
		if err := rows.Scan(&workspace); err != nil {
			return nil, err
		}
		workspaces = append(workspaces, workspace)
	}
	return workspaces, rows.Err()
}

func validIdentity(value string) bool {
	return strings.TrimSpace(value) != "" && len(value) <= 512 && !strings.ContainsRune(value, 0)
}

func (s *Store) validateEnvelope(e Envelope) error {
	if e.Version != Version || e.Kind != IntegrityKind || e.MaxAttempts < 1 || e.MaxAttempts > s.config.MaxAttempts {
		return ErrInvalid
	}
	for _, identity := range []string{e.WorkspaceID, e.SourceID, e.RunID, e.BatchID, e.ParserRevision, e.TraceID, e.IdempotencyKey} {
		if !validIdentity(identity) {
			return ErrInvalid
		}
	}
	if e.Evidence.WorkspaceID != e.WorkspaceID || !validIdentity(e.Evidence.Bucket) ||
		!validIdentity(e.Evidence.Key) || !digestPattern.MatchString(e.Evidence.SHA256) || e.Evidence.SizeBytes < 0 {
		return ErrInvalid
	}
	return nil
}

func (s *Store) Enqueue(ctx context.Context, envelope Envelope) (Enqueued, error) {
	if err := s.validateEnvelope(envelope); err != nil {
		return Enqueued{}, err
	}
	id := NewID()
	payload, err := json.Marshal(envelope)
	if err != nil {
		return Enqueued{}, err
	}
	var inserted string
	err = s.pool.QueryRow(ctx, `INSERT INTO `+s.table+`
		(id,workspace_id,source_id,run_id,kind,idempotency_key,envelope,max_attempts)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (workspace_id,source_id,run_id,kind,idempotency_key)
		DO NOTHING RETURNING id`, id, envelope.WorkspaceID, envelope.SourceID, envelope.RunID,
		envelope.Kind, envelope.IdempotencyKey, payload, envelope.MaxAttempts).Scan(&inserted)
	if err == nil {
		return Enqueued{JobID: inserted, Created: true}, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Enqueued{}, fmt.Errorf("enqueue job: %w", err)
	}
	var matches bool
	err = s.pool.QueryRow(ctx, `SELECT id,envelope=$6::jsonb FROM `+s.table+`
		WHERE workspace_id=$1 AND source_id=$2 AND run_id=$3 AND kind=$4 AND idempotency_key=$5`,
		envelope.WorkspaceID, envelope.SourceID, envelope.RunID, envelope.Kind, envelope.IdempotencyKey, payload).Scan(&id, &matches)
	if err != nil {
		return Enqueued{}, fmt.Errorf("read enqueue receipt: %w", err)
	}
	if !matches {
		return Enqueued{}, ErrConflict
	}
	return Enqueued{JobID: id}, nil
}

func NewID() string {
	var bytes [16]byte
	_, _ = rand.Read(bytes[:])
	return hex.EncodeToString(bytes[:])
}

func (s *Store) Claim(ctx context.Context, request Claim) (Lease, error) {
	if !validIdentity(request.WorkspaceID) || !validIdentity(request.WorkerID) ||
		request.LeaseFor <= 0 || request.LeaseFor > s.config.MaxLease {
		return Lease{}, ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Lease{}, err
	}
	defer rollback(tx)
	_, err = tx.Exec(ctx, `UPDATE `+s.table+` SET state='failed', worker_id=NULL, expires_at=NULL,
		last_failure=CASE WHEN state='leased'
			THEN '{"Code":"lease-expired","Message":"Final permitted lease expired before completion","Retryable":false}'::jsonb
			ELSE COALESCE(last_failure,'{"Code":"attempt-limit","Message":"Configured attempt limit exhausted","Retryable":false}'::jsonb) END
		WHERE workspace_id=$1 AND attempts>=LEAST(max_attempts,$2) AND
		(state IN ('queued','retry-wait') OR (state='leased' AND expires_at<=clock_timestamp()))`,
		request.WorkspaceID, s.config.MaxAttempts)
	if err != nil {
		return Lease{}, err
	}
	var lease Lease
	var envelope []byte
	err = tx.QueryRow(ctx, `WITH candidate AS (
		SELECT id FROM `+s.table+` WHERE workspace_id=$1 AND attempts<LEAST(max_attempts,$4) AND
		((state IN ('queued','retry-wait') AND available_at<=clock_timestamp()) OR
		(state='leased' AND expires_at<=clock_timestamp()))
		ORDER BY available_at,created_at,id FOR UPDATE SKIP LOCKED LIMIT 1)
		UPDATE `+s.table+` j SET state='leased',attempts=j.attempts+1,fence=j.fence+1,
		worker_id=$2,expires_at=clock_timestamp()+($3::double precision*interval '1 second')
		FROM candidate c WHERE j.id=c.id
		RETURNING j.id,j.envelope,j.worker_id,j.fence,j.attempts,j.expires_at`,
		request.WorkspaceID, request.WorkerID, request.LeaseFor.Seconds(), s.config.MaxAttempts).Scan(
		&lease.JobID, &envelope, &lease.WorkerID, &lease.Fence, &lease.Attempt, &lease.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := tx.Commit(ctx); err != nil {
			return Lease{}, err
		}
		return Lease{}, ErrNoJob
	}
	if err != nil {
		return Lease{}, err
	}
	if err := json.Unmarshal(envelope, &lease.Envelope); err != nil {
		return Lease{}, fmt.Errorf("decode durable envelope: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Lease{}, err
	}
	return lease, nil
}

func (s *Store) Heartbeat(ctx context.Context, lease Lease, duration time.Duration) (Lease, error) {
	if duration <= 0 || duration > s.config.MaxLease {
		return Lease{}, ErrInvalid
	}
	err := s.pool.QueryRow(ctx, `UPDATE `+s.table+` SET expires_at=clock_timestamp()+($5::double precision*interval '1 second')
		WHERE id=$1 AND workspace_id=$2 AND worker_id=$3 AND fence=$4 AND state='leased' AND expires_at>clock_timestamp()
		RETURNING expires_at`, lease.JobID, lease.Envelope.WorkspaceID, lease.WorkerID, lease.Fence, duration.Seconds()).Scan(&lease.ExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Lease{}, ErrLeaseLost
	}
	if err != nil {
		return Lease{}, err
	}
	return lease, nil
}

func (s *Store) Complete(ctx context.Context, lease Lease, result Result) (Receipt, error) {
	if !digestPattern.MatchString(result.SHA256) || result.SizeBytes < 0 {
		return Receipt{}, ErrInvalid
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return Receipt{}, err
	}
	defer rollback(tx)
	var state, owner string
	var fence int64
	var active bool
	var saved, payload []byte
	err = tx.QueryRow(ctx, `SELECT state,COALESCE(worker_id,''),fence,
		COALESCE(expires_at>clock_timestamp(),false),receipt,envelope FROM `+s.table+`
		WHERE id=$1 AND workspace_id=$2 FOR UPDATE`, lease.JobID, lease.Envelope.WorkspaceID).Scan(&state, &owner, &fence, &active, &saved, &payload)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && (owner != lease.WorkerID || fence != lease.Fence)) {
		return Receipt{}, ErrLeaseLost
	}
	if err != nil {
		return Receipt{}, err
	}
	var envelope Envelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return Receipt{}, err
	}
	if result.SHA256 != envelope.Evidence.SHA256 || result.SizeBytes != envelope.Evidence.SizeBytes {
		return Receipt{}, ErrInvalid
	}
	if state == "succeeded" {
		var receipt Receipt
		if err := json.Unmarshal(saved, &receipt); err != nil {
			return Receipt{}, err
		}
		if receipt.Result != result {
			return Receipt{}, ErrConflict
		}
		return receipt, tx.Commit(ctx)
	}
	if state != "leased" || !active {
		return Receipt{}, ErrLeaseLost
	}
	receipt := Receipt{ID: NewID(), JobID: lease.JobID, Fence: fence, Result: result}
	if err := tx.QueryRow(ctx, "SELECT clock_timestamp()").Scan(&receipt.CompletedAt); err != nil {
		return Receipt{}, err
	}
	encoded, err := json.Marshal(receipt)
	if err != nil {
		return Receipt{}, err
	}
	updated, err := tx.Exec(ctx, `UPDATE `+s.table+` SET state='succeeded',receipt=$2,expires_at=NULL
		WHERE id=$1 AND state='leased' AND expires_at>clock_timestamp()`, lease.JobID, encoded)
	if err != nil {
		return Receipt{}, err
	}
	if updated.RowsAffected() != 1 {
		return Receipt{}, ErrLeaseLost
	}
	if err := tx.Commit(ctx); err != nil {
		return Receipt{}, err
	}
	return receipt, nil
}

func (s *Store) Fail(ctx context.Context, lease Lease, failure Failure) error {
	if !validIdentity(failure.Code) || len(failure.Message) > 4096 {
		return ErrInvalid
	}
	encoded, err := json.Marshal(failure)
	if err != nil {
		return err
	}
	delay := s.config.RetryDelay
	for i := 1; i < lease.Attempt && delay < s.config.MaxRetryDelay; i++ {
		delay = min(delay*2, s.config.MaxRetryDelay)
	}
	result, err := s.pool.Exec(ctx, `UPDATE `+s.table+` SET
		state=CASE WHEN $6 AND attempts<LEAST(max_attempts,$8) THEN 'retry-wait' ELSE 'failed' END,
		available_at=clock_timestamp()+($7::double precision*interval '1 second'),last_failure=$5,
		worker_id=NULL,expires_at=NULL
		WHERE id=$1 AND workspace_id=$2 AND worker_id=$3 AND fence=$4 AND state='leased' AND expires_at>clock_timestamp()`,
		lease.JobID, lease.Envelope.WorkspaceID, lease.WorkerID, lease.Fence, encoded, failure.Retryable, delay.Seconds(), s.config.MaxAttempts)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return ErrLeaseLost
	}
	return nil
}

func (s *Store) Get(ctx context.Context, workspace, id string) (Job, error) {
	var job Job
	var payload, failure, receipt []byte
	var owner *string
	var expires *time.Time
	var fence int64
	err := s.pool.QueryRow(ctx, `SELECT id,envelope,state,attempts,available_at,worker_id,expires_at,fence,last_failure,receipt
		FROM `+s.table+` WHERE id=$1 AND workspace_id=$2`, id, workspace).Scan(
		&job.ID, &payload, &job.State, &job.Attempts, &job.AvailableAt, &owner, &expires, &fence, &failure, &receipt)
	if errors.Is(err, pgx.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, err
	}
	if err := json.Unmarshal(payload, &job.Envelope); err != nil {
		return Job{}, err
	}
	if owner != nil && expires != nil {
		job.Lease = &Lease{JobID: id, Envelope: job.Envelope, WorkerID: *owner, Fence: fence, Attempt: job.Attempts, ExpiresAt: *expires}
	}
	if len(failure) > 0 {
		if err := json.Unmarshal(failure, &job.LastFailure); err != nil {
			return Job{}, err
		}
	}
	if len(receipt) > 0 {
		if err := json.Unmarshal(receipt, &job.Receipt); err != nil {
			return Job{}, err
		}
	}
	return job, nil
}
