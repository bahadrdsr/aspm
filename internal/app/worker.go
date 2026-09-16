package app

import (
	"context"
	"errors"
	"time"

	"github.com/bahadrdsr/aspm/internal/evidence"
	"github.com/bahadrdsr/aspm/internal/parsers"
	"github.com/jackc/pgx/v5"
)

var errLeaseLost = errors.New("import lease expired or was fenced")

const (
	importLease    = 90 * time.Second
	importWorkTime = 60 * time.Second
	importAttempts = 3
)

func (a *ImportWorker) runWorker(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if err := a.ProcessImports(ctx); err != nil && ctx.Err() == nil {
			a.log.Print("import worker could not drain durable queue")
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// ProcessImports is the manual/standalone worker entrypoint. It drains durable
// queued work, including crash-expired claims, to terminal states within ctx.
// Workers are serial per caller; SKIP LOCKED plus fences allow multiple callers.
// Lease time comes exclusively from PostgreSQL, never the injected domain clock.
func (a *ImportWorker) ProcessImports(ctx context.Context) error {
	workerID := newID()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		record, err := a.claimImport(ctx, workerID)
		if errors.Is(err, pgx.ErrNoRows) {
			var pending bool
			err = a.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM `+a.table("imports")+` WHERE state IN ('queued','processing'))`).Scan(&pending)
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
			return errors.New("claim import work failed")
		}
		err = a.processImport(ctx, record)
		if err != nil && !errors.Is(err, errLeaseLost) {
			return err
		}
	}
}

func (a *ImportWorker) claimImport(ctx context.Context, worker string) (importRecord, error) {
	_, err := a.pool.Exec(ctx, `WITH exhausted AS (
		SELECT id FROM `+a.table("imports")+` WHERE attempts>=$1 AND
		(state='queued' OR (state='processing' AND lease_until<=clock_timestamp()))
		ORDER BY available_at,id FOR UPDATE SKIP LOCKED LIMIT 100)
		UPDATE `+a.table("imports")+` j SET state='failed',worker_id=NULL,lease_until=NULL,
		failure_code='attempt-limit',failure_message='Import could not complete within its permitted attempts'
		FROM exhausted e WHERE j.id=e.id`, importAttempts)
	if err != nil {
		return importRecord{}, err
	}
	return scanImport(a.pool.QueryRow(ctx, `WITH candidate AS (
		SELECT id FROM `+a.table("imports")+` WHERE attempts<$1 AND
		((state='queued' AND available_at<=clock_timestamp()) OR
		(state='processing' AND lease_until<=clock_timestamp()))
		ORDER BY available_at,id FOR UPDATE SKIP LOCKED LIMIT 1)
		UPDATE `+a.table("imports")+` j SET state='processing',worker_id=$2,fence=j.fence+1,
		attempts=j.attempts+1,lease_until=clock_timestamp()+($3::double precision*interval '1 second')
		FROM candidate c WHERE j.id=c.id RETURNING `+importColumns("j"), importAttempts, worker, importLease.Seconds()))
}

func (a *ImportWorker) processImport(ctx context.Context, record importRecord) error {
	workCtx, cancel := context.WithTimeout(ctx, importWorkTime)
	defer cancel()
	data, err := a.readAuthorizedReport(workCtx, record)
	code, message, retryable := "evidence-unavailable", "Original report evidence could not be read and verified", true
	if errors.Is(err, evidence.ErrScope) || errors.Is(err, evidence.ErrNotFound) || errors.Is(err, evidence.ErrIntegrity) {
		retryable = false
	}
	if err == nil {
		var findings []parsers.Finding
		findings, err = parsers.Parse(record.Format, data, record.Mapping)
		if err != nil {
			code, message, retryable = "invalid-report", "Report does not match the admitted format or exceeds parser limits", false
		} else {
			err = a.reconcile(workCtx, record, findings)
			if err == nil || errors.Is(err, errLeaseLost) {
				return err
			}
			code, message = "processing-failed", "Report processing could not be committed"
		}
	}
	if errors.Is(err, errAuthorizationRevoked) {
		return a.failRevokedImport(ctx, record)
	}
	if errors.Is(err, errLeaseLost) {
		return err
	}
	if ctx.Err() != nil {
		// A clean shutdown releases only our fence. A crash leaves the same job
		// recoverable at its database lease deadline.
		releaseCtx, release := context.WithTimeout(context.Background(), 3*time.Second)
		defer release()
		_, _ = a.pool.Exec(releaseCtx, `UPDATE `+a.table("imports")+` SET state='queued',worker_id=NULL,lease_until=NULL
			WHERE id=$1 AND worker_id=$2 AND fence=$3 AND state='processing'`, record.ID, record.WorkerID, record.Fence)
		return ctx.Err()
	}
	result, err := a.pool.Exec(ctx, `UPDATE `+a.table("imports")+` SET
		state=CASE WHEN $4 AND attempts<$5 THEN 'queued' ELSE 'failed' END,
		available_at=clock_timestamp()+interval '1 second'*attempts,
		failure_code=$6,failure_message=$7,worker_id=NULL,lease_until=NULL
		WHERE id=$1 AND worker_id=$2 AND fence=$3 AND state='processing' AND lease_until>clock_timestamp()`,
		record.ID, record.WorkerID, record.Fence, retryable, importAttempts, code, message)
	if err != nil {
		return errors.New("record import failure failed")
	}
	if result.RowsAffected() != 1 {
		return errLeaseLost
	}
	return nil
}
