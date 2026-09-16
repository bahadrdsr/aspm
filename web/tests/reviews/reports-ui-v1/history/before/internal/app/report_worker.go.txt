package app

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

var errReportLeaseLost = errors.New("report lease expired or was fenced")

const (
	reportLease    = 90 * time.Second
	reportWorkTime = 60 * time.Second
	reportAttempts = 3
)

// ProcessReports drains only the durable reporting queue within ctx. It is an
// independently invoked worker entrypoint, not part of Open or ProcessImports.
// One job is processed at a time per caller; database leases fence other callers.
func (a *ReportWorker) ProcessReports(ctx context.Context) error {
	worker := newID()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		job, err := a.claimReport(ctx, worker)
		if errors.Is(err, pgx.ErrNoRows) {
			var pending bool
			err = a.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM `+a.table("report_snapshots")+`
				WHERE state IN ('queued','processing'))`).Scan(&pending)
			if err != nil || !pending {
				return err
			}
			timer := time.NewTimer(100 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
				continue
			}
		}
		if err != nil {
			return errors.New("claim report work failed")
		}
		if err = a.processReport(ctx, job); err != nil && !errors.Is(err, errReportLeaseLost) {
			return err
		}
	}
}

func (a *ReportWorker) claimReport(ctx context.Context, worker string) (reportJob, error) {
	_, err := a.pool.Exec(ctx, `WITH exhausted AS (
		SELECT id FROM `+a.table("report_snapshots")+` WHERE attempts>=$1 AND
		(state='queued' OR (state='processing' AND lease_until<=clock_timestamp()))
		ORDER BY available_at,id FOR UPDATE SKIP LOCKED LIMIT 100)
		UPDATE `+a.table("report_snapshots")+` job SET state='failed',worker_id=NULL,lease_until=NULL,
		failure_code='attempt-limit',failure_message='Snapshot could not complete within its permitted attempts'
		FROM exhausted e WHERE job.id=e.id`, reportAttempts)
	if err != nil {
		return reportJob{}, err
	}
	return scanReportSnapshot(a.pool.QueryRow(ctx, `WITH candidate AS (
		SELECT id FROM `+a.table("report_snapshots")+` WHERE attempts<$1 AND
		((state='queued' AND available_at<=clock_timestamp()) OR
		(state='processing' AND lease_until<=clock_timestamp()))
		ORDER BY available_at,id FOR UPDATE SKIP LOCKED LIMIT 1)
		UPDATE `+a.table("report_snapshots")+` job SET state='processing',worker_id=$2,fence=job.fence+1,
		attempts=job.attempts+1,lease_until=clock_timestamp()+($3::double precision*interval '1 second')
		FROM candidate c WHERE job.id=c.id RETURNING `+reportSnapshotColumns("job"), reportAttempts, worker, reportLease.Seconds()))
}

func (a *ReportWorker) processReport(ctx context.Context, job reportJob) error {
	workCtx, cancel := context.WithTimeout(ctx, reportWorkTime)
	err := a.completeReport(workCtx, job)
	cancel()
	if err == nil || errors.Is(err, errReportLeaseLost) {
		return err
	}
	if ctx.Err() != nil {
		releaseCtx, release := context.WithTimeout(context.Background(), 3*time.Second)
		defer release()
		_, _ = a.pool.Exec(releaseCtx, `UPDATE `+a.table("report_snapshots")+`
			SET state='queued',worker_id=NULL,lease_until=NULL
			WHERE id=$1 AND worker_id=$2 AND fence=$3 AND state='processing'`, job.ID, job.WorkerID, job.Fence)
		return ctx.Err()
	}
	result, err := a.pool.Exec(ctx, `UPDATE `+a.table("report_snapshots")+` SET
		state=CASE WHEN attempts<$4 THEN 'queued' ELSE 'failed' END,
		available_at=clock_timestamp()+interval '1 second'*attempts,
		failure_code='report-generation-failed',failure_message='Snapshot generation could not be committed',
		worker_id=NULL,lease_until=NULL
		WHERE id=$1 AND worker_id=$2 AND fence=$3 AND state='processing' AND lease_until>clock_timestamp()`,
		job.ID, job.WorkerID, job.Fence, reportAttempts)
	if err != nil {
		return errors.New("record report failure failed")
	}
	if result.RowsAffected() != 1 {
		return errReportLeaseLost
	}
	return nil
}

func (a *ReportWorker) completeReport(ctx context.Context, job reportJob) error {
	tx, err := a.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return err
	}
	defer rollback(tx)
	var active bool
	if err = tx.QueryRow(ctx, `SELECT state='processing' AND worker_id=$2 AND fence=$3 AND lease_until>clock_timestamp()
		FROM `+a.table("report_snapshots")+` WHERE id=$1 FOR UPDATE`, job.ID, job.WorkerID, job.Fence).Scan(&active); err != nil {
		return err
	}
	if !active {
		return errReportLeaseLost
	}
	report, err := a.readPosture(ctx, tx, job.WorkspaceID, job.FreshnessDays, a.now())
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		return err
	}
	// The consistent read, saved result, and terminal state share one commit.
	// Only a live processing fence may publish; completed snapshots are excluded.
	result, err := tx.Exec(ctx, `UPDATE `+a.table("report_snapshots")+`
		SET state='succeeded',report=$4,completed_at=$5,worker_id=NULL,lease_until=NULL,
			failure_code=NULL,failure_message=NULL
		WHERE id=$1 AND worker_id=$2 AND fence=$3 AND state='processing' AND lease_until>clock_timestamp()`,
		job.ID, job.WorkerID, job.Fence, encoded, report.AsOf)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return errReportLeaseLost
	}
	return tx.Commit(ctx)
}
