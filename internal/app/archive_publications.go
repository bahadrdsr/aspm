package app

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5"
)

const orphanArchiveGrace = 24 * time.Hour

type archivePublication struct {
	ID, WorkspaceID, ObjectKey, ResourceKind, ResourceID, ObjectDigest, State string
	ObjectSize                                                                int64
	Revision                                                                  int64
	CreatedAt, UpdatedAt                                                      time.Time
}

func (w *RetentionWorker) beginArchivePublication(ctx context.Context, run retentionRunRecord,
	item retentionItemRecord, kind string, data []byte) (archivePublication, error) {
	key, digest, err := w.store.archiveIdentity(run.WorkspaceID, kind, item.ResourceID, data)
	if err != nil {
		return archivePublication{}, err
	}
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return archivePublication{}, err
	}
	defer rollback(tx)
	if err = w.lockRetentionRun(ctx, tx, run); err != nil {
		return archivePublication{}, err
	}
	now := w.now()
	var publication archivePublication
	err = tx.QueryRow(ctx, `INSERT INTO `+w.table("archive_publications")+` AS publication
		(id,workspace_id,object_key,resource_kind,resource_id,object_digest,object_size,
		 state,revision,created_at,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,'publishing',1,$8,$8)
		ON CONFLICT(workspace_id,object_key) DO UPDATE SET
		 resource_kind=EXCLUDED.resource_kind,resource_id=EXCLUDED.resource_id,
		 object_digest=EXCLUDED.object_digest,object_size=EXCLUDED.object_size,
		 state='publishing',revision=publication.revision+1,
		 updated_at=EXCLUDED.updated_at,referenced_at=NULL,deleted_at=NULL
		RETURNING id,workspace_id,object_key,resource_kind,resource_id,object_digest,
		 object_size,state,revision,created_at,updated_at`,
		newID(), run.WorkspaceID, key, kindToResource(kind), item.ResourceID, digest,
		len(data), now).Scan(&publication.ID, &publication.WorkspaceID, &publication.ObjectKey,
		&publication.ResourceKind, &publication.ResourceID, &publication.ObjectDigest,
		&publication.ObjectSize, &publication.State, &publication.Revision,
		&publication.CreatedAt, &publication.UpdatedAt)
	if err != nil {
		return archivePublication{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return archivePublication{}, err
	}
	return publication, nil
}

func kindToResource(kind string) string {
	if kind == "correlation-events" {
		return "correlation-event"
	}
	return "observation"
}

func (w *RetentionWorker) finishArchivePublication(ctx context.Context, tx pgx.Tx,
	publication archivePublication, state string) error {
	now := w.now()
	referencedAt, deletedAt := any(nil), any(nil)
	if state == "referenced" {
		referencedAt = now
	}
	if state == "deleted" {
		deletedAt = now
	}
	result, err := tx.Exec(ctx, `UPDATE `+w.table("archive_publications")+`
		SET state=$5,revision=revision+1,updated_at=$6,referenced_at=$7,deleted_at=$8
		WHERE workspace_id=$1 AND id=$2 AND object_key=$3 AND revision=$4`,
		publication.WorkspaceID, publication.ID, publication.ObjectKey,
		publication.Revision, state, now, referencedAt, deletedAt)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return errRetentionLeaseLost
	}
	return nil
}

func archiveReferenceExists(ctx context.Context, db queryRower, table func(string) string,
	workspace, key string) (bool, error) {
	var referenced bool
	err := db.QueryRow(ctx, `SELECT
		EXISTS(SELECT 1 FROM `+table("observations")+`
		 WHERE workspace_id=$1 AND archive_key=$2)
		OR EXISTS(SELECT 1 FROM `+table("finding_correlation_events")+`
		 WHERE workspace_id=$1 AND archive_key=$2)`, workspace, key).Scan(&referenced)
	return referenced, err
}

func scanArchivePublication(row pgx.Row) (archivePublication, error) {
	var publication archivePublication
	err := row.Scan(&publication.ID, &publication.WorkspaceID, &publication.ObjectKey,
		&publication.ResourceKind, &publication.ResourceID, &publication.ObjectDigest,
		&publication.ObjectSize, &publication.State, &publication.Revision,
		&publication.CreatedAt, &publication.UpdatedAt)
	return publication, err
}
