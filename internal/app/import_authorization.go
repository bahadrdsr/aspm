package app

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

var errAuthorizationRevoked = errors.New("import actor authorization revoked")

func (a *ImportWorker) authorizeActor(ctx context.Context, db queryRower, record importRecord, lock bool) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if record.SubmittedBy == "" {
		return errAuthorizationRevoked
	}
	query := `SELECT role FROM ` + a.table("memberships") + ` WHERE workspace_id=$1 AND user_id=$2`
	if lock {
		query += ` FOR SHARE`
	}
	var role string
	err := db.QueryRow(ctx, query, record.WorkspaceID, record.SubmittedBy).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && role != "admin" && role != "analyst") {
		return errAuthorizationRevoked
	}
	return err
}

func (a *ImportWorker) authorizeImport(ctx context.Context, db queryRower, record importRecord) error {
	if err := a.authorizeActor(ctx, db, record, false); err != nil {
		return err
	}
	var state, code, worker, actor string
	var fence int64
	var live bool
	err := db.QueryRow(ctx, `SELECT state,COALESCE(failure_code,''),COALESCE(worker_id,''),
		COALESCE(submitted_by,''),fence,COALESCE(lease_until>clock_timestamp(),false)
		FROM `+a.table("imports")+` WHERE workspace_id=$1 AND id=$2`, record.WorkspaceID, record.ID).Scan(
		&state, &code, &worker, &actor, &fence, &live)
	if errors.Is(err, pgx.ErrNoRows) {
		return errLeaseLost
	}
	if err != nil {
		return err
	}
	if code == "authorization-revoked" || actor != record.SubmittedBy {
		return errAuthorizationRevoked
	}
	if state != "processing" || fence != record.Fence || worker != record.WorkerID || !live {
		return errLeaseLost
	}
	return nil
}

func (a *ImportWorker) readAuthorizedReport(ctx context.Context, record importRecord) ([]byte, error) {
	if err := a.authorizeImport(ctx, a.pool, record); err != nil {
		return nil, err
	}
	accessCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(context.Canceled)
	watchCtx, stop := context.WithCancel(accessCtx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-watchCtx.Done():
				return
			case <-ticker.C:
				if err := a.authorizeImport(watchCtx, a.pool, record); err != nil {
					if watchCtx.Err() == nil {
						cancel(err)
					}
					return
				}
			}
		}
	}()
	data, err := a.readReport(accessCtx, record)
	stop()
	<-done
	if cause := context.Cause(accessCtx); cause != nil {
		return nil, cause
	}
	if err != nil {
		return nil, err
	}
	if err := a.authorizeImport(ctx, a.pool, record); err != nil {
		return nil, err
	}
	return data, nil
}

func (a *ImportWorker) failRevokedImport(ctx context.Context, record importRecord) error {
	result, err := a.pool.Exec(ctx, `UPDATE `+a.table("imports")+`
		SET state='failed',failure_code='authorization-revoked',
			failure_message='Submitting actor no longer has selected-workspace write permission',
			worker_id=NULL,lease_until=NULL
		WHERE workspace_id=$1 AND id=$2 AND worker_id=$3 AND fence=$4 AND state='processing'`,
		record.WorkspaceID, record.ID, record.WorkerID, record.Fence)
	if err != nil {
		return err
	}
	if result.RowsAffected() == 1 {
		return nil
	}
	var revoked bool
	if err = a.pool.QueryRow(ctx, `SELECT state='failed' AND failure_code='authorization-revoked'
		FROM `+a.table("imports")+` WHERE workspace_id=$1 AND id=$2`, record.WorkspaceID, record.ID).Scan(&revoked); err != nil {
		return err
	}
	if !revoked {
		return errLeaseLost
	}
	return nil
}
