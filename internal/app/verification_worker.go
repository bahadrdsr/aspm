package app

import (
	"bytes"
	"context"
	"errors"
	"io"
	"time"

	"github.com/bahadrdsr/aspm/internal/evidence"
	deterministic "github.com/bahadrdsr/aspm/internal/verification"
	"github.com/jackc/pgx/v5"
)

const verificationAttempts = 3

type VerificationWorkerConfig struct {
	Database              DatabaseConfig
	WorkerID              string
	LeaseDuration         time.Duration
	AuthorizationInterval time.Duration
	MaxFixtureBytes       int64
}

type VerificationWorker struct {
	*database
	workerID              string
	lease                 time.Duration
	authorizationInterval time.Duration
	maxFixtureBytes       int64
}

func OpenVerificationWorker(ctx context.Context, config VerificationWorkerConfig) (*VerificationWorker, error) {
	config, err := normalizeVerificationWorkerConfig(config)
	if err != nil {
		return nil, err
	}
	db, err := openDatabase(ctx, config.Database)
	if err != nil {
		return nil, err
	}
	return &VerificationWorker{
		database: db, workerID: config.WorkerID, lease: config.LeaseDuration,
		authorizationInterval: config.AuthorizationInterval, maxFixtureBytes: config.MaxFixtureBytes,
	}, nil
}

func ValidateVerificationWorkerConfig(config VerificationWorkerConfig) error {
	_, err := normalizeVerificationWorkerConfig(config)
	return err
}

func normalizeVerificationWorkerConfig(config VerificationWorkerConfig) (VerificationWorkerConfig, error) {
	if config.LeaseDuration == 0 {
		config.LeaseDuration = 90 * time.Second
	}
	if config.AuthorizationInterval == 0 {
		config.AuthorizationInterval = 100 * time.Millisecond
	}
	if config.MaxFixtureBytes == 0 {
		config.MaxFixtureBytes = verificationFixtureLimit
	}
	if !validID(config.WorkerID) || config.LeaseDuration < time.Second || config.LeaseDuration > 10*time.Minute ||
		config.AuthorizationInterval < 10*time.Millisecond || config.AuthorizationInterval > time.Second ||
		config.MaxFixtureBytes < 1 || config.MaxFixtureBytes > verificationFixtureLimit {
		return VerificationWorkerConfig{}, errors.New("invalid verification worker configuration")
	}
	if err := ValidateDatabaseConfig(config.Database); err != nil {
		return VerificationWorkerConfig{}, err
	}
	return config, nil
}

func (w *VerificationWorker) Ping(ctx context.Context) error { return w.database.ping(ctx) }
func (w *VerificationWorker) Close() error                   { return w.database.close() }

func (w *VerificationWorker) ProcessNext(ctx context.Context) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	settled, err := w.failExhausted(ctx)
	if err != nil {
		return false, err
	}
	if settled {
		return true, nil
	}
	job, err := w.claimVerification(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, errors.New("claim verification work failed")
	}
	if err = w.processVerification(ctx, job); err != nil {
		return true, err
	}
	return true, nil
}

func (w *VerificationWorker) failExhausted(ctx context.Context) (bool, error) {
	result, err := w.pool.Exec(ctx, `WITH exhausted AS (
		SELECT id FROM `+w.table("verification_jobs")+`
		WHERE attempts>=$1 AND state='processing' AND lease_until<=clock_timestamp()
		ORDER BY lease_until,id FOR UPDATE SKIP LOCKED LIMIT 100)
		UPDATE `+w.table("verification_jobs")+` j
		SET state='failed',completed_at=$2,failure_code='attempt-limit',
		 failure_message='Verification could not complete within its permitted attempts',
		 worker_id=NULL,lease_until=NULL
		FROM exhausted e WHERE j.id=e.id`, verificationAttempts, w.now().UTC())
	return err == nil && result.RowsAffected() > 0, err
}

func (w *VerificationWorker) claimVerification(ctx context.Context) (verificationJobRecord, error) {
	return scanVerificationJob(w.pool.QueryRow(ctx, `WITH candidate AS (
		SELECT id FROM `+w.table("verification_jobs")+`
		WHERE attempts<$1 AND ((state='queued' AND available_at<=clock_timestamp())
		 OR (state='processing' AND lease_until<=clock_timestamp()))
		ORDER BY available_at,id FOR UPDATE SKIP LOCKED LIMIT 1)
		UPDATE `+w.table("verification_jobs")+` j
		SET state='processing',worker_id=$2,fence=j.fence+1,attempts=j.attempts+1,
		 lease_until=clock_timestamp()+($3::double precision*interval '1 second')
		FROM candidate c WHERE j.id=c.id
		RETURNING `+verificationJobColumns("j"),
		verificationAttempts, w.workerID, w.lease.Seconds()))
}

func (w *VerificationWorker) processVerification(ctx context.Context, job verificationJobRecord) error {
	var size int64
	err := w.pool.QueryRow(ctx, `SELECT content_size FROM `+w.table("verification_evidence")+`
		WHERE workspace_id=$1 AND finding_id=$2 AND id=$3 AND content_digest=$4`,
		job.WorkspaceID, job.FindingID, job.EvidenceID, job.EvidenceDigest).Scan(&size)
	if err != nil {
		return w.settleVerificationFailure(ctx, job, "failed", "evidence-integrity",
			"Verification evidence metadata is unavailable", true)
	}
	ref := evidence.Ref{
		WorkspaceID: job.WorkspaceID, Bucket: "postgresql", Key: "verification-evidence/" + job.EvidenceID,
		SHA256: job.EvidenceDigest, SizeBytes: size,
	}
	reader := verificationDatabaseEvidence{worker: w, job: job}
	lookup := func(lookupCtx context.Context, approvalID string) (deterministic.Approval, error) {
		return w.lookupVerificationApproval(lookupCtx, job, approvalID)
	}
	verifier, err := deterministic.Open(ctx, deterministic.Config{
		Evidence: reader, LookupApproval: lookup, Now: w.now, MaxBytes: w.maxFixtureBytes,
		AuthorizationInterval: w.authorizationInterval,
	})
	if err != nil {
		return w.retryVerification(ctx, job)
	}
	result, verifyErr := verifier.Verify(ctx, deterministic.Request{
		WorkspaceID: job.WorkspaceID, FindingID: job.FindingID, RunID: job.ID,
		ApprovalRef: job.ApprovalID, Method: job.Method, ScopeRevision: job.ScopeRevision,
		EnvironmentID: job.EnvironmentID, Evidence: ref,
	})
	if verifyErr == nil {
		return w.publishVerificationSuccess(ctx, job, result.Outcome)
	}
	if ctx.Err() != nil {
		return w.requeueCancelledVerification(ctx, job)
	}
	switch {
	case errors.Is(verifyErr, deterministic.ErrFormat):
		return w.settleVerificationFailure(ctx, job, "failed", "fixture-format",
			"Verification fixture format is invalid", true)
	case errors.Is(verifyErr, evidence.ErrIntegrity):
		return w.settleVerificationFailure(ctx, job, "failed", "evidence-integrity",
			"Verification evidence failed integrity validation", true)
	case errors.Is(verifyErr, deterministic.ErrPolicy):
		state, code, message := w.verificationPolicyOutcome(ctx, job)
		return w.settleVerificationFailure(ctx, job, state, code, message, false)
	default:
		return w.retryVerification(ctx, job)
	}
}

type verificationDatabaseEvidence struct {
	worker *VerificationWorker
	job    verificationJobRecord
}

func (r verificationDatabaseEvidence) Open(ctx context.Context, workspace string,
	ref evidence.Ref) (io.ReadCloser, error) {
	if workspace != r.job.WorkspaceID || ref.WorkspaceID != workspace || ref.Bucket != "postgresql" ||
		ref.Key != "verification-evidence/"+r.job.EvidenceID || ref.SHA256 != r.job.EvidenceDigest {
		return nil, evidence.ErrScope
	}
	var content []byte
	var digest string
	var size int64
	err := r.worker.pool.QueryRow(ctx, `SELECT content,content_digest,content_size
		FROM `+r.worker.table("verification_evidence")+`
		WHERE workspace_id=$1 AND finding_id=$2 AND id=$3`,
		workspace, r.job.FindingID, r.job.EvidenceID).Scan(&content, &digest, &size)
	if err != nil {
		return nil, err
	}
	if digest != ref.SHA256 || size != ref.SizeBytes {
		return nil, evidence.ErrIntegrity
	}
	return io.NopCloser(bytes.NewReader(content)), nil
}

func (w *VerificationWorker) lookupVerificationApproval(ctx context.Context, job verificationJobRecord,
	approvalID string) (deterministic.Approval, error) {
	var approval deterministic.Approval
	var revoked *time.Time
	var currentRevision int64
	err := w.pool.QueryRow(ctx, `SELECT a.id,a.workspace_id,a.finding_id,a.method,a.scope_revision,
		a.environment_id,a.evidence_digest,a.expires_at,a.revoked_at,f.evidence_revision
		FROM `+w.table("verification_approvals")+` a
		JOIN `+w.table("findings")+` f ON f.workspace_id=a.workspace_id AND f.id=a.finding_id
		WHERE a.workspace_id=$1 AND a.finding_id=$2 AND a.id=$3 AND a.evidence_id=$4`,
		job.WorkspaceID, job.FindingID, approvalID, job.EvidenceID).Scan(
		&approval.ID, &approval.WorkspaceID, &approval.FindingID, &approval.Method,
		&approval.ScopeRevision, &approval.EnvironmentID, &approval.EvidenceSHA256,
		&approval.ExpiresAt, &revoked, &currentRevision)
	if err != nil {
		return deterministic.Approval{}, deterministic.ErrPolicy
	}
	approval.Approved = revoked == nil && currentRevision == job.FindingEvidenceRevision &&
		approval.Method == job.Method && approval.ScopeRevision == job.ScopeRevision &&
		approval.EnvironmentID == job.EnvironmentID && approval.EvidenceSHA256 == job.EvidenceDigest
	return approval, nil
}

func (w *VerificationWorker) verificationPolicyOutcome(ctx context.Context,
	job verificationJobRecord) (string, string, string) {
	var revoked *time.Time
	var expires time.Time
	var approvalRevision, currentRevision int64
	var method, environment, scope, digest string
	err := w.pool.QueryRow(ctx, `SELECT a.revoked_at,a.expires_at,a.finding_evidence_revision,
		f.evidence_revision,a.method,a.environment_id,a.scope_revision,a.evidence_digest
		FROM `+w.table("verification_approvals")+` a
		JOIN `+w.table("findings")+` f ON f.workspace_id=a.workspace_id AND f.id=a.finding_id
		WHERE a.workspace_id=$1 AND a.finding_id=$2 AND a.id=$3`,
		job.WorkspaceID, job.FindingID, job.ApprovalID).Scan(
		&revoked, &expires, &approvalRevision, &currentRevision, &method, &environment, &scope, &digest)
	if err != nil || approvalRevision != currentRevision || method != job.Method ||
		environment != job.EnvironmentID || scope != job.ScopeRevision || digest != job.EvidenceDigest {
		return "blocked", "binding-changed", "Verification evidence binding is no longer current"
	}
	if revoked != nil {
		return "cancelled", "approval-revoked", "Verification approval was revoked before completion"
	}
	if !expires.After(w.now()) {
		return "blocked", "approval-expired", "Verification approval expired before completion"
	}
	return "blocked", "binding-changed", "Verification approval is no longer current"
}

func (w *VerificationWorker) publishVerificationSuccess(ctx context.Context,
	job verificationJobRecord, outcome string) error {
	tx, err := w.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer rollback(tx)
	result, err := tx.Exec(ctx, `UPDATE `+w.table("verification_jobs")+` j
		SET state='succeeded',outcome=$4,completed_at=$5,worker_id=NULL,lease_until=NULL
		WHERE j.id=$1 AND j.worker_id=$2 AND j.fence=$3 AND j.state='processing'
		AND j.lease_until>clock_timestamp()
		AND EXISTS(SELECT 1 FROM `+w.table("verification_approvals")+` a
		 JOIN `+w.table("findings")+` f ON f.workspace_id=a.workspace_id AND f.id=a.finding_id
		 WHERE a.workspace_id=j.workspace_id
		 AND a.id=j.approval_id AND a.revoked_at IS NULL
		 AND a.expires_at>$5 AND a.finding_evidence_revision=f.evidence_revision
		 AND a.finding_evidence_revision=j.finding_evidence_revision
		 AND a.evidence_digest=j.evidence_digest)`,
		job.ID, job.WorkerID, job.Fence, outcome, w.now().UTC())
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return nil
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (w *VerificationWorker) settleVerificationFailure(ctx context.Context, job verificationJobRecord,
	state, code, message string, requireFence bool) error {
	query := `UPDATE ` + w.table("verification_jobs") + `
		SET state=$4,completed_at=$5,failure_code=$6,failure_message=$7,
		 outcome=NULL,worker_id=NULL,lease_until=NULL
		WHERE id=$1 AND worker_id=$2 AND fence=$3 AND state='processing'`
	if requireFence {
		query += ` AND lease_until>clock_timestamp()`
	}
	_, err := w.pool.Exec(ctx, query, job.ID, job.WorkerID, job.Fence,
		state, w.now().UTC(), code, message)
	return err
}

func (w *VerificationWorker) retryVerification(ctx context.Context, job verificationJobRecord) error {
	if job.Attempts >= verificationAttempts {
		return w.settleVerificationFailure(ctx, job, "failed", "attempt-limit",
			"Verification could not complete within its permitted attempts", true)
	}
	result, err := w.pool.Exec(ctx, `UPDATE `+w.table("verification_jobs")+`
		SET state='queued',available_at=clock_timestamp()+interval '1 second'*attempts,
		 worker_id=NULL,lease_until=NULL
		WHERE id=$1 AND worker_id=$2 AND fence=$3 AND state='processing'
		AND lease_until>clock_timestamp()`, job.ID, job.WorkerID, job.Fence)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return nil
	}
	return nil
}

func (w *VerificationWorker) requeueCancelledVerification(_ context.Context,
	job verificationJobRecord) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_, err := w.pool.Exec(ctx, `UPDATE `+w.table("verification_jobs")+`
		SET state='queued',available_at=clock_timestamp(),worker_id=NULL,lease_until=NULL
		WHERE id=$1 AND worker_id=$2 AND fence=$3 AND state='processing'`,
		job.ID, job.WorkerID, job.Fence)
	return err
}
