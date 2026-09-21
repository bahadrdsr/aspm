package app

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/bahadrdsr/aspm/internal/providers"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

func assessmentLockBusy(err error) bool {
	var pgerr *pgconn.PgError
	return errors.As(err, &pgerr) && pgerr.Code == "55P03"
}

func (w *AssessmentWorker) assessmentAuthority(ctx context.Context, q queryRower, job assessmentJobRecord) (aiResolution, error) {
	resolved, err := w.readAIConfiguration(ctx, q, w.credentials, assessmentRequest(job.AssessmentBinding), true)
	if err != nil {
		return aiResolution{}, err
	}
	secrets := append(append([]string{}, w.secrets...), resolved.configuration.Profile.APIKey)
	if job.Scope != w.scope || !assessmentBindingCurrent(job.AssessmentBinding, resolved) ||
		job.ContextRef != "reviewed-context:"+job.PreviewID ||
		assessmentBindingHasSecret(job.AssessmentBinding, secrets) {
		return aiResolution{}, providers.ErrPolicy
	}
	var observation string
	if err = q.QueryRow(ctx, `SELECT id FROM `+w.table("observations")+`
		WHERE workspace_id=$1 AND finding_id=$2 AND id=$3 FOR KEY SHARE`,
		job.WorkspaceID, job.FindingID, job.ObservationID).Scan(&observation); err != nil {
		return aiResolution{}, err
	}
	valid, err := w.assessmentTimeValid(ctx, q, assessmentExpiry(job.ConsentExpiresAt, resolved))
	if err != nil {
		return aiResolution{}, err
	}
	if !valid {
		return aiResolution{}, providers.ErrPolicy
	}
	return resolved, nil
}

func (w *AssessmentWorker) claimAssessment(ctx context.Context) (assessmentDispatch, AIConfiguration, bool, error) {
	none := assessmentDispatch{}
	job, err := scanAssessmentJob(w.pool.QueryRow(ctx, w.assessmentJobSelect()+`
		WHERE j.scope=$1 AND (j.state='queued' OR (j.state='dispatching' AND j.lease_until<=clock_timestamp()))
		ORDER BY CASE WHEN j.state='dispatching' THEN 0 ELSE 1 END,j.created_at,j.id LIMIT 1`, w.scope))
	if errors.Is(err, pgx.ErrNoRows) {
		return none, AIConfiguration{}, false, nil
	}
	if err != nil {
		return none, AIConfiguration{}, false, err
	}
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return none, AIConfiguration{}, false, err
	}
	defer rollback(tx)
	var workspace string
	if err = tx.QueryRow(ctx, `SELECT id FROM `+w.table("workspaces")+`
		WHERE id=$1 FOR SHARE NOWAIT`, job.WorkspaceID).Scan(&workspace); err != nil {
		if assessmentLockBusy(err) {
			return none, AIConfiguration{}, false, nil
		}
		return none, AIConfiguration{}, false, err
	}
	if job.State == "dispatching" {
		// Retiring public state/fence does not acknowledge the old owner's I/O.
		// Its separate reservation survives until cleanup or its hard deadline.
		failure, _ := json.Marshal(&Failure{Code: "lease-expired", Message: "The dispatched assessment lost its lease; delivery and usage are uncertain"})
		command, err := tx.Exec(ctx, `UPDATE `+w.table("assessment_jobs")+`
			SET state='uncertain',dispatch_state='possibly-sent',result=NULL,failure=$5,
				completed_at=clock_timestamp(),worker_id=NULL,lease_until=NULL,fence=fence+1
			WHERE workspace_id=$1 AND id=$2 AND scope=$3 AND fence=$4
				AND state='dispatching' AND lease_until<=clock_timestamp()`,
			job.WorkspaceID, job.ID, w.scope, job.fence, failure)
		if err != nil {
			return none, AIConfiguration{}, false, err
		}
		if err = tx.Commit(ctx); err != nil {
			return none, AIConfiguration{}, false, err
		}
		return none, AIConfiguration{}, command.RowsAffected() == 1, nil
	}
	resolved, authorityErr := w.assessmentAuthority(ctx, tx, job)
	if authorityErr != nil && !assessmentAuthorityDenied(authorityErr) {
		return none, AIConfiguration{}, false, authorityErr
	}
	// Authority and expired consent are settled even when the pool is full.
	// Denied queued work therefore cannot indefinitely head-block other jobs.
	if authorityErr != nil || len(job.Context) > w.limits.Input {
		state, code, message := "invalidated", "authorization-revoked", "The approved assessment authority or consent is no longer current"
		if authorityErr == nil {
			state, code, message = "failed", "input-limit", "The approved context exceeds the worker input bound"
		} else if errors.Is(authorityErr, providers.ErrCapability) {
			code = "capability"
		}
		failure, _ := json.Marshal(&Failure{Code: code, Message: message})
		command, err := tx.Exec(ctx, `UPDATE `+w.table("assessment_jobs")+`
			SET state=$4,failure=$5,completed_at=clock_timestamp(),fence=fence+1
			WHERE workspace_id=$1 AND id=$2 AND scope=$3 AND state='queued' AND attempts=0`,
			job.WorkspaceID, job.ID, w.scope, state, failure)
		if err != nil {
			return none, AIConfiguration{}, false, err
		}
		if err = tx.Commit(ctx); err != nil {
			return none, AIConfiguration{}, false, err
		}
		return none, AIConfiguration{}, command.RowsAffected() == 1, nil
	}
	var concurrent, requests int
	var window int64
	if err = tx.QueryRow(ctx, `SELECT max_concurrent,requests_per_window,request_window_us
		FROM `+w.table("assessment_quotas")+` WHERE singleton AND scope=$1 FOR UPDATE NOWAIT`, w.scope).
		Scan(&concurrent, &requests, &window); err != nil {
		if assessmentLockBusy(err) {
			return none, AIConfiguration{}, false, nil
		}
		return none, AIConfiguration{}, false, err
	}
	if concurrent != w.limits.Concurrent || requests != w.limits.Requests || window != intervalMicros(w.limits.Window) {
		return none, AIConfiguration{}, false, errUnavailable
	}
	var active, spent int64
	if err = tx.QueryRow(ctx, `SELECT
		(SELECT count(*) FROM `+w.table("assessment_jobs")+`
		 WHERE scope=$1 AND io_token IS NOT NULL AND io_released_at IS NULL AND io_deadline>clock_timestamp()),
		(SELECT count(*) FROM `+w.table("assessment_jobs")+`
		 WHERE scope=$1 AND dispatch_started_at>clock_timestamp()-$2::bigint*interval '1 microsecond')`,
		w.scope, window).Scan(&active, &spent); err != nil {
		return none, AIConfiguration{}, false, err
	}
	if active >= int64(concurrent) || spent >= int64(requests) {
		return none, AIConfiguration{}, false, nil
	}
	current, err := scanAssessmentJob(tx.QueryRow(ctx, w.assessmentJobSelect()+`
		WHERE j.workspace_id=$1 AND j.id=$2 AND j.scope=$3 AND j.state='queued'
		FOR UPDATE OF j NOWAIT`, job.WorkspaceID, job.ID, w.scope))
	if errors.Is(err, pgx.ErrNoRows) || assessmentLockBusy(err) {
		return none, AIConfiguration{}, false, nil
	}
	if err != nil {
		return none, AIConfiguration{}, false, err
	}
	expires := assessmentExpiry(current.ConsentExpiresAt, resolved)
	if !expires.After(w.now().UTC()) {
		return none, AIConfiguration{}, false, errAssessmentChanged
	}
	// This is the sole admission/attempt transition. Nothing after its commit
	// may return this job to the queue, refund the request, or send another POST.
	budget := min(w.limits.Timeout, w.client.Timeout)
	token := newID()
	// Start the owner's monotonic deadline BEFORE the marker query, including
	// any query/commit pause. It can expire earlier, never restart later than
	// the database's reservation budget. Do not map a remote wall clock to Now.
	deadline := time.Now().Add(budget)
	err = tx.QueryRow(ctx, `WITH timing AS MATERIALIZED (SELECT clock_timestamp() AS started)
		UPDATE `+w.table("assessment_jobs")+`
		SET state='dispatching',attempts=1,dispatch_state='possibly-sent',
			dispatch_started_at=timing.started,worker_id=$4,fence=fence+1,
			lease_until=timing.started+$5::bigint*interval '1 microsecond',
			io_owner_id=$4,io_token=$8,io_deadline=timing.started+$7::bigint*interval '1 microsecond'
		FROM timing
		WHERE workspace_id=$1 AND id=$2 AND scope=$3 AND state='queued' AND attempts=0
			AND consent_expires_at>clock_timestamp() AND $6::timestamptz>clock_timestamp()
		RETURNING fence,lease_until,dispatch_started_at`,
		current.WorkspaceID, current.ID, w.scope, w.workerID, intervalMicros(w.limits.Lease), expires,
		intervalMicros(budget), token).
		Scan(&current.fence, &current.leaseUntil, &current.DispatchStartedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return none, AIConfiguration{}, false, errAssessmentChanged
	}
	if err != nil {
		return none, AIConfiguration{}, false, err
	}
	valid, err := w.assessmentTimeValid(ctx, tx, expires)
	if err != nil {
		return none, AIConfiguration{}, false, err
	}
	if !valid {
		return none, AIConfiguration{}, false, errAssessmentChanged
	}
	var liveLease bool
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()<$1`, current.leaseUntil).Scan(&liveLease); err != nil {
		return none, AIConfiguration{}, false, err
	}
	if !liveLease {
		return none, AIConfiguration{}, false, errAssessmentChanged
	}
	if err = ctx.Err(); err != nil {
		return none, AIConfiguration{}, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return none, AIConfiguration{}, false, err
	}
	current.State, current.Attempts, current.DispatchState = "dispatching", 1, "possibly-sent"
	current.workerID = &w.workerID
	return assessmentDispatch{assessmentJobRecord: current, token: token, deadline: deadline}, resolved.configuration, true, nil
}

func (w *AssessmentWorker) refreshAssessment(ctx context.Context, job assessmentJobRecord) error {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	resolved, authorityErr := w.assessmentAuthority(ctx, tx, job)
	if authorityErr != nil && !assessmentAuthorityDenied(authorityErr) {
		return authorityErr
	}
	var id string
	if err = tx.QueryRow(ctx, `SELECT id FROM `+w.table("assessment_jobs")+`
		WHERE workspace_id=$1 AND id=$2 AND scope=$3 AND worker_id=$4 AND fence=$5
			AND state='dispatching' AND lease_until>clock_timestamp() FOR UPDATE`,
		job.WorkspaceID, job.ID, w.scope, w.workerID, job.fence).Scan(&id); errors.Is(err, pgx.ErrNoRows) {
		return errAssessmentLeaseLost
	} else if err != nil {
		return err
	}
	if authorityErr != nil {
		failure, _ := json.Marshal(&Failure{Code: "authorization-revoked", Message: "The approved assessment authority or consent is no longer current"})
		command, err := tx.Exec(ctx, `UPDATE `+w.table("assessment_jobs")+`
			SET state='invalidated',failure=$6,completed_at=clock_timestamp(),
				worker_id=NULL,lease_until=NULL,fence=fence+1
			WHERE workspace_id=$1 AND id=$2 AND scope=$3 AND worker_id=$4 AND fence=$5
				AND state='dispatching' AND lease_until>clock_timestamp()`,
			job.WorkspaceID, job.ID, w.scope, w.workerID, job.fence, failure)
		if err != nil {
			return err
		}
		if command.RowsAffected() != 1 {
			return errAssessmentLeaseLost
		}
		if err = tx.Commit(ctx); err != nil {
			return err
		}
		return providers.ErrPolicy
	}
	expires := assessmentExpiry(job.ConsentExpiresAt, resolved)
	command, err := tx.Exec(ctx, `UPDATE `+w.table("assessment_jobs")+`
		SET lease_until=clock_timestamp()+$6::bigint*interval '1 microsecond'
		WHERE workspace_id=$1 AND id=$2 AND scope=$3 AND worker_id=$4 AND fence=$5
			AND state='dispatching' AND lease_until>clock_timestamp() AND io_deadline>clock_timestamp()
			AND $7::timestamptz>clock_timestamp()`,
		job.WorkspaceID, job.ID, w.scope, w.workerID, job.fence, intervalMicros(w.limits.Lease), expires)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return errAssessmentLeaseLost
	}
	valid, err := w.assessmentTimeValid(ctx, tx, expires)
	if err != nil {
		return err
	}
	if !valid {
		return errAssessmentChanged
	}
	return tx.Commit(ctx)
}

func (w *AssessmentWorker) monitorAssessment(ctx context.Context, job assessmentJobRecord, finished <-chan struct{}, cancel context.CancelCauseFunc) {
	ticker := time.NewTicker(min(w.limits.Authorization, w.limits.Lease/3))
	defer ticker.Stop()
	for {
		select {
		case <-finished:
			return
		case <-ctx.Done():
			return
		case <-ticker.C:
			select {
			case <-finished:
				return
			default:
			}
			if err := w.refreshAssessment(ctx, job); err != nil {
				cancel(err)
				return
			}
		}
	}
}
