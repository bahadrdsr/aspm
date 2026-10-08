package app

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"github.com/bahadrdsr/aspm/internal/evidence"
	"github.com/jackc/pgx/v5"
)

var errRetentionLeaseLost = errors.New("retention lease or fence is no longer current")

const (
	defaultRetentionLease = 30 * time.Second
	retentionAttempts     = 3
)

type RetentionWorkerConfig struct {
	Database         DatabaseConfig
	RawStorage       StorageConfig
	ArchiveStorage   StorageConfig
	MaxEvidenceBytes int64
	Lease            time.Duration
}

type RetentionWorker struct {
	*database
	store     *retentionStore
	workerID  string
	lease     time.Duration
	closeOnce sync.Once
	closeErr  error
}

type retentionItemRecord struct {
	RetentionRunItem
	Attempts int
}

type retentionOutcome struct {
	state, outcome string
	reasons        []string
}

type retentionArchiveWrite struct {
	key, digest string
	publication archivePublication
}

func OpenRetentionWorker(ctx context.Context, config RetentionWorkerConfig) (*RetentionWorker, error) {
	if config.Lease == 0 {
		config.Lease = defaultRetentionLease
	}
	if config.MaxEvidenceBytes == 0 {
		config.MaxEvidenceBytes = 32 << 20
	}
	if config.Lease < time.Second || config.Lease > 5*time.Minute {
		return nil, errors.New("retention worker lease must be from one second to five minutes")
	}
	if err := ValidateDatabaseConfig(config.Database); err != nil {
		return nil, err
	}
	store, err := openRetentionStore(ctx, config.RawStorage, config.ArchiveStorage, config.MaxEvidenceBytes)
	if err != nil {
		return nil, err
	}
	db, err := openDatabase(ctx, config.Database)
	if err != nil {
		_ = store.close()
		return nil, err
	}
	return &RetentionWorker{
		database: db, store: store, workerID: "retention-" + newID(), lease: config.Lease,
	}, nil
}

func (w *RetentionWorker) Close() error {
	if w == nil {
		return nil
	}
	w.closeOnce.Do(func() {
		w.closeErr = errors.Join(w.store.close(), w.database.close())
	})
	return w.closeErr
}

func (w *RetentionWorker) Ping(ctx context.Context) error { return w.database.ping(ctx) }

func (w *RetentionWorker) ProcessNext(ctx context.Context) (bool, error) {
	run, item, worked, err := w.claimRetentionItem(ctx)
	if err != nil || !worked {
		return worked, err
	}
	if item.ID == "" {
		return true, nil
	}
	outcome, err := w.processRetentionItem(ctx, run, item)
	if errors.Is(err, errRetentionLeaseLost) {
		return true, nil
	}
	if err != nil {
		return true, w.retryRetentionItem(ctx, run, item, err)
	}
	if err = w.finishRetentionItem(ctx, run, item, outcome); errors.Is(err, errRetentionLeaseLost) {
		return true, nil
	}
	return true, err
}

func (w *RetentionWorker) claimRetentionItem(ctx context.Context) (retentionRunRecord, retentionItemRecord, bool, error) {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return retentionRunRecord{}, retentionItemRecord{}, false, err
	}
	defer rollback(tx)
	var exhausted retentionRunRecord
	exhausted, err = scanRetentionRun(tx.QueryRow(ctx, `SELECT `+retentionRunColumns("")+`
		FROM `+w.table("retention_runs")+` WHERE attempts>=$1 AND
		(state='queued' OR (state='processing' AND lease_until<=clock_timestamp()))
		ORDER BY available_at,id FOR UPDATE SKIP LOCKED LIMIT 1`, retentionAttempts))
	if err == nil {
		var itemID string
		var itemAttempts int
		itemErr := tx.QueryRow(ctx, `SELECT id,attempts FROM `+w.table("retention_run_items")+`
			WHERE workspace_id=$1 AND run_id=$2 AND state IN ('queued','processing')
			ORDER BY ordinal LIMIT 1 FOR UPDATE`, exhausted.WorkspaceID, exhausted.ID).Scan(&itemID, &itemAttempts)
		if errors.Is(itemErr, pgx.ErrNoRows) {
			if _, err = tx.Exec(ctx, `UPDATE `+w.table("retention_runs")+`
				SET state='failed',completed_at=$3,worker_id=NULL,lease_until=NULL,
					failure_code='attempt-limit',failure_message='Retention item ownership was repeatedly abandoned'
				WHERE workspace_id=$1 AND id=$2`,
				exhausted.WorkspaceID, exhausted.ID, w.now()); err != nil {
				return retentionRunRecord{}, retentionItemRecord{}, false, err
			}
		} else if itemErr != nil {
			return retentionRunRecord{}, retentionItemRecord{}, false, itemErr
		} else {
			if _, err = tx.Exec(ctx, `UPDATE `+w.table("retention_run_items")+`
				SET state='failed',attempts=$4,completed_at=$5,
					failure_code='attempt-limit',failure_message='Retention item ownership was repeatedly abandoned'
				WHERE workspace_id=$1 AND run_id=$2 AND id=$3`,
				exhausted.WorkspaceID, exhausted.ID, itemID, max(itemAttempts, retentionAttempts), w.now()); err != nil {
				return retentionRunRecord{}, retentionItemRecord{}, false, err
			}
			exhausted.WorkerID = ""
			if _, err = tx.Exec(ctx, `UPDATE `+w.table("retention_runs")+`
				SET state='processing',worker_id=$3,fence=fence+1,
					lease_until=clock_timestamp()+($4::double precision*interval '1 second')
				WHERE workspace_id=$1 AND id=$2`,
				exhausted.WorkspaceID, exhausted.ID, w.workerID, w.lease.Seconds()); err != nil {
				return retentionRunRecord{}, retentionItemRecord{}, false, err
			}
			exhausted.WorkerID = w.workerID
			exhausted.Fence++
			if err = w.advanceRetentionRunTx(ctx, tx, exhausted); err != nil {
				return retentionRunRecord{}, retentionItemRecord{}, false, err
			}
		}
		if err = tx.Commit(ctx); err != nil {
			return retentionRunRecord{}, retentionItemRecord{}, false, err
		}
		return exhausted, retentionItemRecord{}, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return retentionRunRecord{}, retentionItemRecord{}, false, err
	}
	run, err := scanRetentionRun(tx.QueryRow(ctx, `WITH candidate AS (
		SELECT id FROM `+w.table("retention_runs")+` WHERE attempts<$1 AND
		((state='queued' AND available_at<=clock_timestamp()) OR
		 (state='processing' AND lease_until<=clock_timestamp()))
		ORDER BY available_at,id FOR UPDATE SKIP LOCKED LIMIT 1)
		UPDATE `+w.table("retention_runs")+` run
		SET state='processing',worker_id=$2,fence=run.fence+1,attempts=run.attempts+1,
			lease_until=clock_timestamp()+($3::double precision*interval '1 second'),
			failure_code=NULL,failure_message=NULL
		FROM candidate c WHERE run.id=c.id RETURNING `+retentionRunColumns("run"),
		retentionAttempts, w.workerID, w.lease.Seconds()))
	if errors.Is(err, pgx.ErrNoRows) {
		return retentionRunRecord{}, retentionItemRecord{}, false, nil
	}
	if err != nil {
		return retentionRunRecord{}, retentionItemRecord{}, false, err
	}
	var item retentionItemRecord
	var reasons []byte
	var failureCode, failureMessage *string
	err = tx.QueryRow(ctx, `SELECT id,class,resource_kind,resource_id,action,state,
		protected_reasons,outcome,attempts,failure_code,failure_message,started_at,completed_at,
		object_key,object_digest,object_revision
		FROM `+w.table("retention_run_items")+`
		WHERE workspace_id=$1 AND run_id=$2 AND state IN ('queued','processing')
		ORDER BY ordinal LIMIT 1 FOR UPDATE`, run.WorkspaceID, run.ID).Scan(
		&item.ID, &item.Class, &item.ResourceKind, &item.ResourceID, &item.Action, &item.State,
		&reasons, &item.Outcome, &item.Attempts, &failureCode, &failureMessage,
		&item.StartedAt, &item.CompletedAt, &item.ObjectKey, &item.ObjectDigest, &item.ObjectRevision)
	if errors.Is(err, pgx.ErrNoRows) {
		if err = w.finalizeRetentionRunTx(ctx, tx, run); err != nil {
			return retentionRunRecord{}, retentionItemRecord{}, false, err
		}
		if err = tx.Commit(ctx); err != nil {
			return retentionRunRecord{}, retentionItemRecord{}, false, err
		}
		return run, retentionItemRecord{}, true, nil
	}
	if err != nil {
		return retentionRunRecord{}, retentionItemRecord{}, false, err
	}
	if err = json.Unmarshal(reasons, &item.ProtectedReasons); err != nil {
		return retentionRunRecord{}, retentionItemRecord{}, false, err
	}
	now := w.now()
	if _, err = tx.Exec(ctx, `UPDATE `+w.table("retention_run_items")+`
		SET state='processing',attempts=attempts+1,started_at=COALESCE(started_at,$4),
			failure_code=NULL,failure_message=NULL
		WHERE workspace_id=$1 AND run_id=$2 AND id=$3`,
		run.WorkspaceID, run.ID, item.ID, now); err != nil {
		return retentionRunRecord{}, retentionItemRecord{}, false, err
	}
	item.State, item.Attempts = "processing", item.Attempts+1
	if item.StartedAt == nil {
		item.StartedAt = &now
	}
	if err = tx.Commit(ctx); err != nil {
		return retentionRunRecord{}, retentionItemRecord{}, false, err
	}
	return run, item, true, nil
}

func (w *RetentionWorker) lockRetentionRun(ctx context.Context, tx pgx.Tx, run retentionRunRecord) error {
	var active bool
	err := tx.QueryRow(ctx, `SELECT state='processing' AND worker_id=$2 AND fence=$3
		AND lease_until>clock_timestamp() FROM `+w.table("retention_runs")+`
		WHERE workspace_id=$1 AND id=$4 FOR UPDATE`,
		run.WorkspaceID, run.WorkerID, run.Fence, run.ID).Scan(&active)
	if err != nil {
		return err
	}
	if !active {
		return errRetentionLeaseLost
	}
	return nil
}

func (w *RetentionWorker) renewRetentionLease(ctx context.Context, run retentionRunRecord) error {
	result, err := w.pool.Exec(ctx, `UPDATE `+w.table("retention_runs")+`
		SET lease_until=clock_timestamp()+($5::double precision*interval '1 second')
		WHERE workspace_id=$1 AND id=$2 AND worker_id=$3 AND fence=$4
		AND state='processing' AND lease_until>clock_timestamp()`,
		run.WorkspaceID, run.ID, run.WorkerID, run.Fence, w.lease.Seconds())
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return errRetentionLeaseLost
	}
	return nil
}

func withRetentionLease[T any](ctx context.Context, w *RetentionWorker, run retentionRunRecord,
	operation func(context.Context) (T, error)) (T, error) {
	var zero T
	if err := w.renewRetentionLease(ctx, run); err != nil {
		return zero, err
	}
	operationContext, cancelOperation := context.WithCancel(ctx)
	defer cancelOperation()
	interval := w.lease / 3
	if interval < 100*time.Millisecond {
		interval = 100 * time.Millisecond
	}
	heartbeat := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-operationContext.Done():
				heartbeat <- nil
				return
			case <-ticker.C:
				renewContext, cancelRenew := context.WithTimeout(context.WithoutCancel(ctx), interval)
				err := w.renewRetentionLease(renewContext, run)
				cancelRenew()
				if err != nil {
					select {
					case <-operationContext.Done():
						heartbeat <- nil
					default:
						cancelOperation()
						heartbeat <- err
					}
					return
				}
			}
		}
	}()
	value, operationErr := operation(operationContext)
	cancelOperation()
	if heartbeatErr := <-heartbeat; heartbeatErr != nil {
		return zero, heartbeatErr
	}
	return value, operationErr
}

func (w *RetentionWorker) rawEvidencePresent(ctx context.Context, run retentionRunRecord,
	record importRecord) (bool, error) {
	_, err := withRetentionLease(ctx, w, run, func(storageContext context.Context) ([]byte, error) {
		return w.store.readRaw(storageContext, record)
	})
	switch {
	case err == nil, errors.Is(err, evidence.ErrIntegrity):
		return true, nil
	case errors.Is(err, evidence.ErrNotFound):
		return false, nil
	default:
		return false, err
	}
}

func (w *RetentionWorker) archiveEvidencePresent(ctx context.Context, run retentionRunRecord,
	workspace, key, digest string, size int64) (bool, error) {
	_, err := withRetentionLease(ctx, w, run, func(storageContext context.Context) ([]byte, error) {
		return w.store.readArchive(storageContext, workspace, key, digest, size)
	})
	switch {
	case err == nil, errors.Is(err, evidence.ErrIntegrity):
		return true, nil
	case errors.Is(err, evidence.ErrNotFound):
		return false, nil
	default:
		return false, err
	}
}

func (w *RetentionWorker) observationProtection(ctx context.Context, db queryRower,
	workspace, id string) ([]string, error) {
	var hold, decision, assessment bool
	err := db.QueryRow(ctx, `SELECT
		EXISTS(SELECT 1 FROM `+w.table("retention_holds")+` h
		 JOIN `+w.table("observations")+` held ON held.workspace_id=h.workspace_id
		 WHERE h.workspace_id=$1 AND h.released_at IS NULL AND
		  ((h.resource_kind='observation' AND h.resource_id=$2)
		   OR (h.resource_kind='import' AND h.resource_id=(
		    SELECT i.id FROM `+w.table("observations")+` o JOIN `+w.table("imports")+` i
		    ON i.workspace_id=o.workspace_id AND i.run_id=o.run_id
		    WHERE o.workspace_id=$1 AND o.id=$2)))),
		EXISTS(SELECT 1 FROM `+w.table("observations")+` o JOIN `+w.table("findings")+` f
		 ON f.workspace_id=o.workspace_id AND f.id=o.finding_id
		 WHERE o.workspace_id=$1 AND o.id=$2 AND
		  (f.owner_id IS NOT NULL OR f.workflow_state<>'open' OR f.disposition<>'none'
		   OR EXISTS(SELECT 1 FROM `+w.table("notes")+` n
		    WHERE n.workspace_id=f.workspace_id AND n.finding_id=f.id)
		   OR EXISTS(SELECT 1 FROM `+w.table("finding_correlation_members")+` cm
		    JOIN `+w.table("finding_correlations")+` c
		    ON c.workspace_id=cm.workspace_id AND c.id=cm.correlation_id AND c.state='active'
		    WHERE cm.workspace_id=f.workspace_id AND cm.finding_id=f.id AND cm.released_at IS NULL))),
		EXISTS(SELECT 1 FROM `+w.table("assessment_previews")+`
		 WHERE workspace_id=$1 AND observation_id=$2)`,
		workspace, id).Scan(&hold, &decision, &assessment)
	if err != nil {
		return nil, err
	}
	reasons := retentionReasons(hold, decision, false, "")
	if assessment {
		reasons = append(reasons, "assessment-reference")
	}
	return reasons, nil
}

func (w *RetentionWorker) importProtection(ctx context.Context, db queryRower,
	workspace, id string) ([]string, error) {
	var hold, decision, assessment bool
	var references int
	err := db.QueryRow(ctx, `SELECT
		EXISTS(SELECT 1 FROM `+w.table("retention_holds")+` h
		 WHERE h.workspace_id=$1 AND h.released_at IS NULL AND
		  ((h.resource_kind='import' AND h.resource_id=$2)
		   OR (h.resource_kind='observation' AND EXISTS(
		    SELECT 1 FROM `+w.table("observations")+` o JOIN `+w.table("imports")+` i
		    ON i.workspace_id=o.workspace_id AND i.run_id=o.run_id
		    WHERE i.workspace_id=$1 AND i.id=$2 AND o.id=h.resource_id)))),
		EXISTS(SELECT 1 FROM `+w.table("imports")+` i JOIN `+w.table("observations")+` o
		 ON o.workspace_id=i.workspace_id AND o.run_id=i.run_id
		 JOIN `+w.table("findings")+` f ON f.workspace_id=o.workspace_id AND f.id=o.finding_id
		 WHERE i.workspace_id=$1 AND i.id=$2 AND
		  (f.owner_id IS NOT NULL OR f.workflow_state<>'open' OR f.disposition<>'none'
		   OR EXISTS(SELECT 1 FROM `+w.table("notes")+` n
		    WHERE n.workspace_id=f.workspace_id AND n.finding_id=f.id)
		   OR EXISTS(SELECT 1 FROM `+w.table("finding_correlation_members")+` cm
		    JOIN `+w.table("finding_correlations")+` c
		    ON c.workspace_id=cm.workspace_id AND c.id=cm.correlation_id AND c.state='active'
		    WHERE cm.workspace_id=f.workspace_id AND cm.finding_id=f.id AND cm.released_at IS NULL))),
		EXISTS(SELECT 1 FROM `+w.table("imports")+` i JOIN `+w.table("observations")+` o
		 ON o.workspace_id=i.workspace_id AND o.run_id=i.run_id
		 JOIN `+w.table("assessment_previews")+` p
		 ON p.workspace_id=o.workspace_id AND p.observation_id=o.id
		 WHERE i.workspace_id=$1 AND i.id=$2),
		(SELECT count(*) FROM `+w.table("imports")+` i JOIN `+w.table("observations")+` o
		 ON o.workspace_id=i.workspace_id AND o.run_id=i.run_id
		 WHERE i.workspace_id=$1 AND i.id=$2)`,
		workspace, id).Scan(&hold, &decision, &assessment, &references)
	if err != nil {
		return nil, err
	}
	extra := ""
	if assessment {
		extra = "assessment-reference"
	}
	return retentionReasons(hold, decision, references > 1, extra), nil
}

func (w *RetentionWorker) auditProtection(ctx context.Context, db queryRower,
	workspace, id string) ([]string, error) {
	var hold, active bool
	err := db.QueryRow(ctx, `SELECT
		EXISTS(SELECT 1 FROM `+w.table("retention_holds")+`
		 WHERE workspace_id=$1 AND resource_kind='correlation-event' AND resource_id=$2
		 AND released_at IS NULL),
		EXISTS(SELECT 1 FROM `+w.table("finding_correlation_events")+` e
		 JOIN `+w.table("finding_correlations")+` c
		 ON c.workspace_id=e.workspace_id AND c.id=e.correlation_id
		 WHERE e.workspace_id=$1 AND e.id=$2 AND c.state='active')`,
		workspace, id).Scan(&hold, &active)
	if err != nil {
		return nil, err
	}
	reasons := retentionReasons(hold, false, false, "")
	if active {
		reasons = append(reasons, "active-correlation")
	}
	return reasons, nil
}

func (w *RetentionWorker) processRetentionItem(ctx context.Context,
	run retentionRunRecord, item retentionItemRecord) (retentionOutcome, error) {
	switch item.Action {
	case "archive-history":
		return w.archiveObservation(ctx, run, item)
	case "expire-raw-report":
		return w.expireRawReport(ctx, run, item)
	case "expire-archive":
		return w.expireObservationArchive(ctx, run, item)
	case "archive-audit":
		if isHistoryRetentionKind(item.ResourceKind) {
			return w.archiveHistoryEvent(ctx, run, item)
		}
		return w.archiveAuditEvent(ctx, run, item)
	case "restore-archive":
		return w.restoreObservation(ctx, run, item)
	case "delete-orphan":
		return w.deleteOrphanArchive(ctx, run, item)
	default:
		return retentionOutcome{}, errors.New("unsupported retention action")
	}
}

func (w *RetentionWorker) deleteOrphanArchive(ctx context.Context,
	run retentionRunRecord, item retentionItemRecord) (retentionOutcome, error) {
	if item.ObjectKey == nil || item.ObjectDigest == nil || item.ObjectRevision == nil {
		return retentionOutcome{}, errors.New("orphan cleanup item omitted exact object binding")
	}
	if err := w.renewRetentionLease(ctx, run); err != nil {
		return retentionOutcome{}, err
	}
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return retentionOutcome{}, err
	}
	defer rollback(tx)
	publication, err := scanArchivePublication(tx.QueryRow(ctx, `SELECT id,workspace_id,object_key,
			resource_kind,resource_id,object_digest,object_size,state,revision,created_at,updated_at
			FROM `+w.table("archive_publications")+`
			WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, run.WorkspaceID, item.ResourceID))
	if err != nil {
		return retentionOutcome{}, err
	}
	if publication.ObjectKey != *item.ObjectKey || publication.ObjectDigest != *item.ObjectDigest ||
		publication.Revision != *item.ObjectRevision ||
		(publication.State != "publishing" && publication.State != "orphan") ||
		publication.UpdatedAt.After(w.now().Add(-orphanArchiveGrace)) {
		if err = tx.Commit(ctx); err != nil {
			return retentionOutcome{}, err
		}
		return retentionOutcome{state: "protected", outcome: "state-changed",
			reasons: []string{"state-changed"}}, nil
	}
	referenced, err := archiveReferenceExists(ctx, tx, w.table, run.WorkspaceID, publication.ObjectKey)
	if err != nil {
		return retentionOutcome{}, err
	}
	if referenced {
		if err = w.finishArchivePublication(ctx, tx, publication, "referenced"); err != nil {
			return retentionOutcome{}, err
		}
		if err = tx.Commit(ctx); err != nil {
			return retentionOutcome{}, err
		}
		return retentionOutcome{state: "protected", outcome: "protected",
			reasons: []string{"archive-reference"}}, nil
	}
	_, readErr := withRetentionLease(ctx, w, run, func(storageContext context.Context) ([]byte, error) {
		return w.store.readArchive(storageContext, run.WorkspaceID, publication.ObjectKey,
			publication.ObjectDigest, publication.ObjectSize)
	})
	if readErr != nil && !errors.Is(readErr, evidence.ErrNotFound) &&
		!errors.Is(readErr, evidence.ErrIntegrity) {
		return retentionOutcome{}, readErr
	}
	outcome := "deleted"
	if errors.Is(readErr, evidence.ErrNotFound) {
		outcome = "already-missing"
	} else {
		if _, err = withRetentionLease(ctx, w, run, func(storageContext context.Context) (struct{}, error) {
			return struct{}{}, w.store.deleteArchive(storageContext, run.WorkspaceID, publication.ObjectKey)
		}); err != nil {
			return retentionOutcome{}, err
		}
	}
	if err = w.finishArchivePublication(ctx, tx, publication, "deleted"); err != nil {
		return retentionOutcome{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return retentionOutcome{}, err
	}
	return retentionOutcome{state: "succeeded", outcome: outcome}, nil
}

func compactObservation(data []byte) ([]byte, error) {
	var observation Observation
	if err := json.Unmarshal(data, &observation); err != nil {
		return nil, err
	}
	observation.Impact, observation.Remediation = "", ""
	observation.Unmapped = map[string]any{}
	observation.EvidenceAvailability = ""
	return json.Marshal(observation)
}

func (w *RetentionWorker) archiveObservation(ctx context.Context,
	run retentionRunRecord, item retentionItemRecord) (retentionOutcome, error) {
	var data []byte
	var availability string
	var revision int64
	err := w.pool.QueryRow(ctx, `SELECT data,evidence_availability,evidence_revision
		FROM `+w.table("observations")+` WHERE workspace_id=$1 AND id=$2`,
		run.WorkspaceID, item.ResourceID).Scan(&data, &availability, &revision)
	if err != nil {
		return retentionOutcome{}, err
	}
	if availability == "archived" {
		return retentionOutcome{state: "succeeded", outcome: "already-archived"}, nil
	}
	if availability != "available" {
		return retentionOutcome{state: "protected", outcome: "state-changed", reasons: []string{"state-changed"}}, nil
	}
	reasons, err := w.observationProtection(ctx, w.pool, run.WorkspaceID, item.ResourceID)
	if err != nil {
		return retentionOutcome{}, err
	}
	if len(reasons) != 0 {
		return retentionOutcome{state: "protected", outcome: "protected", reasons: reasons}, nil
	}
	summary, err := compactObservation(data)
	if err != nil {
		return retentionOutcome{}, err
	}
	publication, err := w.beginArchivePublication(ctx, run, item, "observations", data)
	if err != nil {
		return retentionOutcome{}, err
	}
	published, err := withRetentionLease(ctx, w, run, func(storageContext context.Context) (retentionArchiveWrite, error) {
		key, digest, writeErr := w.store.putArchive(
			storageContext, run.WorkspaceID, "observations", item.ResourceID, data)
		return retentionArchiveWrite{key: key, digest: digest, publication: publication}, writeErr
	})
	if err != nil {
		return retentionOutcome{}, err
	}
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return retentionOutcome{}, err
	}
	defer rollback(tx)
	if err = w.lockRetentionRun(ctx, tx, run); err != nil {
		return retentionOutcome{}, err
	}
	reasons, err = w.observationProtection(ctx, tx, run.WorkspaceID, item.ResourceID)
	if err != nil {
		return retentionOutcome{}, err
	}
	if len(reasons) != 0 {
		if err = w.finishArchivePublication(ctx, tx, publication, "orphan"); err != nil {
			return retentionOutcome{}, err
		}
		if err = tx.Commit(ctx); err != nil {
			return retentionOutcome{}, err
		}
		return retentionOutcome{state: "protected", outcome: "protected-after-archive-write", reasons: reasons}, nil
	}
	result, err := tx.Exec(ctx, `UPDATE `+w.table("observations")+`
		SET data=NULL,summary=$4,evidence_availability='archived',
			archive_key=$5,archive_digest=$6,archive_size=$7,archived_at=$8,
			evidence_expired_at=NULL,retention_transition=NULL,evidence_revision=evidence_revision+1
		WHERE workspace_id=$1 AND id=$2 AND evidence_revision=$3 AND evidence_availability='available'`,
		run.WorkspaceID, item.ResourceID, revision, summary, published.key, published.digest, len(data), w.now())
	if err != nil {
		return retentionOutcome{}, err
	}
	if result.RowsAffected() != 1 {
		return retentionOutcome{}, errRetentionLeaseLost
	}
	if err = w.finishArchivePublication(ctx, tx, published.publication, "referenced"); err != nil {
		return retentionOutcome{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return retentionOutcome{}, err
	}
	return retentionOutcome{state: "succeeded", outcome: "archived"}, nil
}

func (w *RetentionWorker) markImportAvailability(ctx context.Context, run retentionRunRecord,
	record importRecord, state string) error {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err = w.lockRetentionRun(ctx, tx, run); err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `UPDATE `+w.table("imports")+`
		SET evidence_availability=$4,evidence_revision=evidence_revision+1,retention_transition=NULL
		WHERE workspace_id=$1 AND id=$2 AND evidence_revision=$3`,
		record.WorkspaceID, record.ID, record.EvidenceRevision, state)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return errRetentionLeaseLost
	}
	return tx.Commit(ctx)
}

func (w *RetentionWorker) expireRawReport(ctx context.Context,
	run retentionRunRecord, item retentionItemRecord) (retentionOutcome, error) {
	record, err := scanImport(w.pool.QueryRow(ctx, `SELECT `+importColumns("")+`
		FROM `+w.table("imports")+` WHERE workspace_id=$1 AND id=$2`,
		run.WorkspaceID, item.ResourceID))
	if err != nil {
		return retentionOutcome{}, err
	}
	switch record.EvidenceAvailability {
	case "expired":
		return retentionOutcome{state: "succeeded", outcome: "already-expired"}, nil
	case "missing", "corrupt":
		return retentionOutcome{state: record.EvidenceAvailability, outcome: record.EvidenceAvailability}, nil
	case "available":
	default:
		return retentionOutcome{state: "protected", outcome: "state-changed", reasons: []string{"state-changed"}}, nil
	}
	reasons, err := w.importProtection(ctx, w.pool, run.WorkspaceID, item.ResourceID)
	if err != nil {
		return retentionOutcome{}, err
	}
	deleteConfirmed := false
	if len(reasons) != 0 {
		if record.RetentionTransition == "" {
			return retentionOutcome{state: "protected", outcome: "protected", reasons: reasons}, nil
		}
		present, presenceErr := w.rawEvidencePresent(ctx, run, record)
		if presenceErr != nil {
			return retentionOutcome{}, presenceErr
		}
		if present {
			if err = w.clearImportTransition(ctx, run, item.ResourceID); err != nil {
				return retentionOutcome{}, err
			}
			return retentionOutcome{state: "protected", outcome: "protected", reasons: reasons}, nil
		}
		deleteConfirmed = true
	}
	if record.RetentionTransition == "" {
		if _, err = withRetentionLease(ctx, w, run, func(storageContext context.Context) ([]byte, error) {
			return w.store.readRaw(storageContext, record)
		}); err != nil {
			state, _ := evidenceAvailabilityError(err)
			if state == "" {
				return retentionOutcome{}, err
			}
			if err = w.markImportAvailability(ctx, run, record, state); err != nil {
				return retentionOutcome{}, err
			}
			return retentionOutcome{state: state, outcome: state}, nil
		}
		tx, err := w.pool.Begin(ctx)
		if err != nil {
			return retentionOutcome{}, err
		}
		defer rollback(tx)
		if err = w.lockRetentionRun(ctx, tx, run); err != nil {
			return retentionOutcome{}, err
		}
		reasons, err = w.importProtection(ctx, tx, run.WorkspaceID, item.ResourceID)
		if err != nil {
			return retentionOutcome{}, err
		}
		if len(reasons) != 0 {
			if err = tx.Commit(ctx); err != nil {
				return retentionOutcome{}, err
			}
			return retentionOutcome{state: "protected", outcome: "protected", reasons: reasons}, nil
		}
		result, err := tx.Exec(ctx, `UPDATE `+w.table("imports")+`
			SET retention_transition='expiring',evidence_revision=evidence_revision+1
			WHERE workspace_id=$1 AND id=$2 AND evidence_revision=$3
			AND evidence_availability='available' AND retention_transition IS NULL`,
			run.WorkspaceID, item.ResourceID, record.EvidenceRevision)
		if err != nil {
			return retentionOutcome{}, err
		}
		if result.RowsAffected() != 1 {
			return retentionOutcome{}, errRetentionLeaseLost
		}
		if err = tx.Commit(ctx); err != nil {
			return retentionOutcome{}, err
		}
		record.EvidenceRevision++
		record.RetentionTransition = "expiring"
	}
	if !deleteConfirmed {
		reasons, err = w.importProtection(ctx, w.pool, run.WorkspaceID, item.ResourceID)
		if err != nil {
			return retentionOutcome{}, err
		}
		if len(reasons) != 0 {
			present, presenceErr := w.rawEvidencePresent(ctx, run, record)
			if presenceErr != nil {
				return retentionOutcome{}, presenceErr
			}
			if present {
				if err = w.clearImportTransition(ctx, run, item.ResourceID); err != nil {
					return retentionOutcome{}, err
				}
				return retentionOutcome{state: "protected", outcome: "protected-before-delete", reasons: reasons}, nil
			}
			deleteConfirmed = true
		}
	}
	if !deleteConfirmed {
		if _, err = withRetentionLease(ctx, w, run, func(storageContext context.Context) (struct{}, error) {
			return struct{}{}, w.store.deleteRaw(storageContext, run.WorkspaceID, record.ReportKey)
		}); err != nil {
			return retentionOutcome{}, err
		}
	}
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return retentionOutcome{}, err
	}
	defer rollback(tx)
	if err = w.lockRetentionRun(ctx, tx, run); err != nil {
		return retentionOutcome{}, err
	}
	result, err := tx.Exec(ctx, `UPDATE `+w.table("imports")+`
		SET evidence_availability='expired',evidence_expired_at=$4,retention_transition=NULL,
			evidence_revision=evidence_revision+1
		WHERE workspace_id=$1 AND id=$2 AND evidence_revision=$3 AND retention_transition='expiring'`,
		run.WorkspaceID, item.ResourceID, record.EvidenceRevision, w.now())
	if err != nil {
		return retentionOutcome{}, err
	}
	if result.RowsAffected() != 1 {
		return retentionOutcome{}, errRetentionLeaseLost
	}
	if err = tx.Commit(ctx); err != nil {
		return retentionOutcome{}, err
	}
	return retentionOutcome{state: "succeeded", outcome: "expired"}, nil
}

func (w *RetentionWorker) archiveAuditEvent(ctx context.Context,
	run retentionRunRecord, item retentionItemRecord) (retentionOutcome, error) {
	var before, after []byte
	var availability string
	var revision int64
	err := w.pool.QueryRow(ctx, `SELECT before_state,after_state,detail_availability,detail_revision
		FROM `+w.table("finding_correlation_events")+` WHERE workspace_id=$1 AND id=$2`,
		run.WorkspaceID, item.ResourceID).Scan(&before, &after, &availability, &revision)
	if err != nil {
		return retentionOutcome{}, err
	}
	if availability == "archived" {
		return retentionOutcome{state: "succeeded", outcome: "already-archived"}, nil
	}
	if availability != "available" {
		return retentionOutcome{state: "protected", outcome: "state-changed", reasons: []string{"state-changed"}}, nil
	}
	reasons, err := w.auditProtection(ctx, w.pool, run.WorkspaceID, item.ResourceID)
	if err != nil {
		return retentionOutcome{}, err
	}
	if len(reasons) != 0 {
		return retentionOutcome{state: "protected", outcome: "protected", reasons: reasons}, nil
	}
	payload, err := json.Marshal(struct {
		Before json.RawMessage `json:"beforeState"`
		After  json.RawMessage `json:"afterState"`
	}{before, after})
	if err != nil {
		return retentionOutcome{}, err
	}
	publication, err := w.beginArchivePublication(ctx, run, item, "correlation-events", payload)
	if err != nil {
		return retentionOutcome{}, err
	}
	published, err := withRetentionLease(ctx, w, run, func(storageContext context.Context) (retentionArchiveWrite, error) {
		key, digest, writeErr := w.store.putArchive(
			storageContext, run.WorkspaceID, "correlation-events", item.ResourceID, payload)
		return retentionArchiveWrite{key: key, digest: digest, publication: publication}, writeErr
	})
	if err != nil {
		return retentionOutcome{}, err
	}
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return retentionOutcome{}, err
	}
	defer rollback(tx)
	if err = w.lockRetentionRun(ctx, tx, run); err != nil {
		return retentionOutcome{}, err
	}
	reasons, err = w.auditProtection(ctx, tx, run.WorkspaceID, item.ResourceID)
	if err != nil {
		return retentionOutcome{}, err
	}
	if len(reasons) != 0 {
		if err = w.finishArchivePublication(ctx, tx, publication, "orphan"); err != nil {
			return retentionOutcome{}, err
		}
		if err = tx.Commit(ctx); err != nil {
			return retentionOutcome{}, err
		}
		return retentionOutcome{state: "protected", outcome: "protected-after-archive-write", reasons: reasons}, nil
	}
	result, err := tx.Exec(ctx, `UPDATE `+w.table("finding_correlation_events")+`
		SET before_state='{}',after_state='{}',detail_availability='archived',
			archive_key=$4,archive_digest=$5,archive_size=$6,archived_at=$7,
			detail_expired_at=NULL,retention_transition=NULL,detail_revision=detail_revision+1
		WHERE workspace_id=$1 AND id=$2 AND detail_revision=$3 AND detail_availability='available'`,
		run.WorkspaceID, item.ResourceID, revision, published.key, published.digest, len(payload), w.now())
	if err != nil {
		return retentionOutcome{}, err
	}
	if result.RowsAffected() != 1 {
		return retentionOutcome{}, errRetentionLeaseLost
	}
	if err = w.finishArchivePublication(ctx, tx, published.publication, "referenced"); err != nil {
		return retentionOutcome{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return retentionOutcome{}, err
	}
	return retentionOutcome{state: "succeeded", outcome: "archived"}, nil
}

func (w *RetentionWorker) expireObservationArchive(ctx context.Context,
	run retentionRunRecord, item retentionItemRecord) (retentionOutcome, error) {
	var availability, key, digest, transition string
	var size, revision int64
	err := w.pool.QueryRow(ctx, `SELECT evidence_availability,archive_key,archive_digest,archive_size,
		evidence_revision,COALESCE(retention_transition,'') FROM `+w.table("observations")+`
		WHERE workspace_id=$1 AND id=$2`, run.WorkspaceID, item.ResourceID).Scan(
		&availability, &key, &digest, &size, &revision, &transition)
	if err != nil {
		return retentionOutcome{}, err
	}
	switch availability {
	case "expired":
		return retentionOutcome{state: "succeeded", outcome: "already-expired"}, nil
	case "missing", "corrupt":
		return retentionOutcome{state: availability, outcome: availability}, nil
	case "archived":
	default:
		return retentionOutcome{state: "protected", outcome: "state-changed", reasons: []string{"state-changed"}}, nil
	}
	reasons, err := w.observationProtection(ctx, w.pool, run.WorkspaceID, item.ResourceID)
	if err != nil {
		return retentionOutcome{}, err
	}
	deleteConfirmed := false
	if len(reasons) != 0 {
		if transition == "" {
			return retentionOutcome{state: "protected", outcome: "protected", reasons: reasons}, nil
		}
		present, presenceErr := w.archiveEvidencePresent(ctx, run, run.WorkspaceID, key, digest, size)
		if presenceErr != nil {
			return retentionOutcome{}, presenceErr
		}
		if present {
			if err = w.clearObservationTransition(ctx, run, item.ResourceID); err != nil {
				return retentionOutcome{}, err
			}
			return retentionOutcome{state: "protected", outcome: "protected", reasons: reasons}, nil
		}
		deleteConfirmed = true
	}
	if transition == "" {
		if _, err = withRetentionLease(ctx, w, run, func(storageContext context.Context) ([]byte, error) {
			return w.store.readArchive(storageContext, run.WorkspaceID, key, digest, size)
		}); err != nil {
			state, _ := evidenceAvailabilityError(err)
			if state == "" {
				return retentionOutcome{}, err
			}
			tx, beginErr := w.pool.Begin(ctx)
			if beginErr != nil {
				return retentionOutcome{}, beginErr
			}
			defer rollback(tx)
			if beginErr = w.lockRetentionRun(ctx, tx, run); beginErr != nil {
				return retentionOutcome{}, beginErr
			}
			if _, beginErr = tx.Exec(ctx, `UPDATE `+w.table("observations")+`
				SET evidence_availability=$4,evidence_revision=evidence_revision+1,retention_transition=NULL
				WHERE workspace_id=$1 AND id=$2 AND evidence_revision=$3`,
				run.WorkspaceID, item.ResourceID, revision, state); beginErr != nil {
				return retentionOutcome{}, beginErr
			}
			if beginErr = tx.Commit(ctx); beginErr != nil {
				return retentionOutcome{}, beginErr
			}
			return retentionOutcome{state: state, outcome: state}, nil
		}
		tx, err := w.pool.Begin(ctx)
		if err != nil {
			return retentionOutcome{}, err
		}
		defer rollback(tx)
		if err = w.lockRetentionRun(ctx, tx, run); err != nil {
			return retentionOutcome{}, err
		}
		reasons, err = w.observationProtection(ctx, tx, run.WorkspaceID, item.ResourceID)
		if err != nil {
			return retentionOutcome{}, err
		}
		if len(reasons) != 0 {
			if err = tx.Commit(ctx); err != nil {
				return retentionOutcome{}, err
			}
			return retentionOutcome{state: "protected", outcome: "protected", reasons: reasons}, nil
		}
		result, err := tx.Exec(ctx, `UPDATE `+w.table("observations")+`
			SET retention_transition='expiring',evidence_revision=evidence_revision+1
			WHERE workspace_id=$1 AND id=$2 AND evidence_revision=$3
			AND evidence_availability='archived' AND retention_transition IS NULL`,
			run.WorkspaceID, item.ResourceID, revision)
		if err != nil {
			return retentionOutcome{}, err
		}
		if result.RowsAffected() != 1 {
			return retentionOutcome{}, errRetentionLeaseLost
		}
		if err = tx.Commit(ctx); err != nil {
			return retentionOutcome{}, err
		}
		revision++
	}
	if !deleteConfirmed {
		reasons, err = w.observationProtection(ctx, w.pool, run.WorkspaceID, item.ResourceID)
		if err != nil {
			return retentionOutcome{}, err
		}
		if len(reasons) != 0 {
			present, presenceErr := w.archiveEvidencePresent(ctx, run, run.WorkspaceID, key, digest, size)
			if presenceErr != nil {
				return retentionOutcome{}, presenceErr
			}
			if present {
				if err = w.clearObservationTransition(ctx, run, item.ResourceID); err != nil {
					return retentionOutcome{}, err
				}
				return retentionOutcome{state: "protected", outcome: "protected-before-delete", reasons: reasons}, nil
			}
			deleteConfirmed = true
		}
	}
	if !deleteConfirmed {
		if _, err = withRetentionLease(ctx, w, run, func(storageContext context.Context) (struct{}, error) {
			return struct{}{}, w.store.deleteArchive(storageContext, run.WorkspaceID, key)
		}); err != nil {
			return retentionOutcome{}, err
		}
	}
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return retentionOutcome{}, err
	}
	defer rollback(tx)
	if err = w.lockRetentionRun(ctx, tx, run); err != nil {
		return retentionOutcome{}, err
	}
	expiredAt := w.now()
	result, err := tx.Exec(ctx, `UPDATE `+w.table("observations")+`
		SET evidence_availability='expired',evidence_expired_at=$4,retention_transition=NULL,
			evidence_revision=evidence_revision+1
		WHERE workspace_id=$1 AND id=$2 AND evidence_revision=$3 AND retention_transition='expiring'`,
		run.WorkspaceID, item.ResourceID, revision, expiredAt)
	if err != nil {
		return retentionOutcome{}, err
	}
	if result.RowsAffected() != 1 {
		return retentionOutcome{}, errRetentionLeaseLost
	}
	if _, err = tx.Exec(ctx, `UPDATE `+w.table("archive_publications")+`
		SET state='deleted',revision=revision+1,updated_at=$3,referenced_at=NULL,deleted_at=$3
		WHERE workspace_id=$1 AND object_key=$2 AND state<>'deleted'`,
		run.WorkspaceID, key, expiredAt); err != nil {
		return retentionOutcome{}, err
	}
	if err = tx.Commit(ctx); err != nil {
		return retentionOutcome{}, err
	}
	return retentionOutcome{state: "succeeded", outcome: "expired"}, nil
}

func (w *RetentionWorker) restoreObservation(ctx context.Context,
	run retentionRunRecord, item retentionItemRecord) (retentionOutcome, error) {
	var availability, key, digest, transition string
	var size, revision int64
	err := w.pool.QueryRow(ctx, `SELECT evidence_availability,archive_key,archive_digest,archive_size,
		evidence_revision,COALESCE(retention_transition,'')
		FROM `+w.table("observations")+` WHERE workspace_id=$1 AND id=$2`,
		run.WorkspaceID, item.ResourceID).Scan(&availability, &key, &digest, &size, &revision, &transition)
	if err != nil {
		return retentionOutcome{}, err
	}
	if availability == "available" {
		return retentionOutcome{state: "succeeded", outcome: "already-available"}, nil
	}
	if availability == "expired" {
		return retentionOutcome{state: "protected", outcome: "state-changed", reasons: []string{"state-changed"}}, nil
	}
	if transition != "" {
		return retentionOutcome{state: "protected", outcome: "state-changed", reasons: []string{"state-changed"}}, nil
	}
	data, err := withRetentionLease(ctx, w, run, func(storageContext context.Context) ([]byte, error) {
		return w.store.readArchive(storageContext, run.WorkspaceID, key, digest, size)
	})
	if err != nil {
		state, _ := evidenceAvailabilityError(err)
		if state == "" {
			return retentionOutcome{}, err
		}
		tx, beginErr := w.pool.Begin(ctx)
		if beginErr != nil {
			return retentionOutcome{}, beginErr
		}
		defer rollback(tx)
		if beginErr = w.lockRetentionRun(ctx, tx, run); beginErr != nil {
			return retentionOutcome{}, beginErr
		}
		if _, beginErr = tx.Exec(ctx, `UPDATE `+w.table("observations")+`
			SET evidence_availability=$4,evidence_revision=evidence_revision+1
			WHERE workspace_id=$1 AND id=$2 AND evidence_revision=$3`,
			run.WorkspaceID, item.ResourceID, revision, state); beginErr != nil {
			return retentionOutcome{}, beginErr
		}
		if beginErr = tx.Commit(ctx); beginErr != nil {
			return retentionOutcome{}, beginErr
		}
		return retentionOutcome{state: state, outcome: state}, nil
	}
	var observation Observation
	if err = json.Unmarshal(data, &observation); err != nil {
		return retentionOutcome{}, err
	}
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return retentionOutcome{}, err
	}
	defer rollback(tx)
	if err = w.lockRetentionRun(ctx, tx, run); err != nil {
		return retentionOutcome{}, err
	}
	result, err := tx.Exec(ctx, `UPDATE `+w.table("observations")+`
		SET data=$4,summary=NULL,evidence_availability='available',evidence_expired_at=NULL,
			retention_transition=NULL,evidence_revision=evidence_revision+1
		WHERE workspace_id=$1 AND id=$2 AND evidence_revision=$3
		AND evidence_availability IN ('archived','missing','corrupt')`,
		run.WorkspaceID, item.ResourceID, revision, data)
	if err != nil {
		return retentionOutcome{}, err
	}
	if result.RowsAffected() != 1 {
		return retentionOutcome{}, errRetentionLeaseLost
	}
	if err = tx.Commit(ctx); err != nil {
		return retentionOutcome{}, err
	}
	return retentionOutcome{state: "succeeded", outcome: "restored"}, nil
}

func (w *RetentionWorker) finishRetentionItem(ctx context.Context, run retentionRunRecord,
	item retentionItemRecord, outcome retentionOutcome) error {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err = w.lockRetentionRun(ctx, tx, run); err != nil {
		return err
	}
	reasonValues := outcome.reasons
	if reasonValues == nil {
		reasonValues = []string{}
	}
	reasons, err := json.Marshal(reasonValues)
	if err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `UPDATE `+w.table("retention_run_items")+`
		SET state=$4,protected_reasons=$5,outcome=$6,completed_at=$7,
			failure_code=NULL,failure_message=NULL
		WHERE workspace_id=$1 AND run_id=$2 AND id=$3 AND state='processing'`,
		run.WorkspaceID, run.ID, item.ID, outcome.state, reasons, outcome.outcome, w.now())
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return errRetentionLeaseLost
	}
	if err = w.advanceRetentionRunTx(ctx, tx, run); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (w *RetentionWorker) advanceRetentionRunTx(ctx context.Context, tx pgx.Tx,
	run retentionRunRecord) error {
	var remaining int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM `+w.table("retention_run_items")+`
		WHERE workspace_id=$1 AND run_id=$2 AND state IN ('queued','processing')`,
		run.WorkspaceID, run.ID).Scan(&remaining); err != nil {
		return err
	}
	if remaining == 0 {
		return w.finalizeRetentionRunTx(ctx, tx, run)
	}
	result, err := tx.Exec(ctx, `UPDATE `+w.table("retention_runs")+`
		SET state='queued',worker_id=NULL,lease_until=NULL,available_at=clock_timestamp(),attempts=0
		WHERE workspace_id=$1 AND id=$2 AND worker_id=$3 AND fence=$4 AND state='processing'`,
		run.WorkspaceID, run.ID, run.WorkerID, run.Fence)
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return errRetentionLeaseLost
	}
	return nil
}

func (w *RetentionWorker) clearImportTransition(ctx context.Context,
	run retentionRunRecord, id string) error {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err = w.lockRetentionRun(ctx, tx, run); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE `+w.table("imports")+`
		SET retention_transition=NULL,evidence_revision=evidence_revision+1
		WHERE workspace_id=$1 AND id=$2 AND retention_transition='expiring'`,
		run.WorkspaceID, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (w *RetentionWorker) clearObservationTransition(ctx context.Context,
	run retentionRunRecord, id string) error {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err = w.lockRetentionRun(ctx, tx, run); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE `+w.table("observations")+`
		SET retention_transition=NULL,evidence_revision=evidence_revision+1
		WHERE workspace_id=$1 AND id=$2 AND retention_transition='expiring'`,
		run.WorkspaceID, id); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (w *RetentionWorker) finalizeRetentionRunTx(ctx context.Context, tx pgx.Tx,
	run retentionRunRecord) error {
	var failures int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM `+w.table("retention_run_items")+`
		WHERE workspace_id=$1 AND run_id=$2 AND state IN ('missing','corrupt','failed')`,
		run.WorkspaceID, run.ID).Scan(&failures); err != nil {
		return err
	}
	state := "succeeded"
	if failures != 0 {
		state = "partial"
	}
	result, err := tx.Exec(ctx, `UPDATE `+w.table("retention_runs")+`
		SET state=$5,completed_at=$6,worker_id=NULL,lease_until=NULL,
			failure_code=NULL,failure_message=NULL
		WHERE workspace_id=$1 AND id=$2 AND worker_id=$3 AND fence=$4 AND state='processing'`,
		run.WorkspaceID, run.ID, run.WorkerID, run.Fence, state, w.now())
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return errRetentionLeaseLost
	}
	return nil
}

func (w *RetentionWorker) retryRetentionItem(ctx context.Context, run retentionRunRecord,
	item retentionItemRecord, cause error) error {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	if err = w.lockRetentionRun(ctx, tx, run); err != nil {
		return err
	}
	if item.Attempts >= retentionAttempts {
		if _, err = tx.Exec(ctx, `UPDATE `+w.table("retention_run_items")+`
			SET state='failed',failure_code='retention-action-failed',
				failure_message='Retention action could not complete within its permitted attempts',
				completed_at=$4
			WHERE workspace_id=$1 AND run_id=$2 AND id=$3 AND state='processing'`,
			run.WorkspaceID, run.ID, item.ID, w.now()); err != nil {
			return err
		}
		if err = w.advanceRetentionRunTx(ctx, tx, run); err != nil {
			return err
		}
		return tx.Commit(ctx)
	}
	if _, err = tx.Exec(ctx, `UPDATE `+w.table("retention_run_items")+`
		SET state='queued',failure_code='retention-action-failed',
			failure_message='Retention action will be retried'
		WHERE workspace_id=$1 AND run_id=$2 AND id=$3 AND state='processing'`,
		run.WorkspaceID, run.ID, item.ID); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `UPDATE `+w.table("retention_runs")+`
		SET state='queued',worker_id=NULL,lease_until=NULL,
			available_at=clock_timestamp()+interval '1 second',attempts=0
		WHERE workspace_id=$1 AND id=$2 AND worker_id=$3 AND fence=$4 AND state='processing'`,
		run.WorkspaceID, run.ID, run.WorkerID, run.Fence); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	_ = cause
	return nil
}
