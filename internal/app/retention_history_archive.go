package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/bahadrdsr/aspm/internal/evidence"
	"github.com/jackc/pgx/v5"
)

type historyArchiveRecord struct {
	payload                   []byte
	availability, key, digest string
	size, revision            int64
	archivedAt                *time.Time
}

func historyTable(kind string) string {
	switch kind {
	case retentionFindingDecisionEvent:
		return "finding_decision_events"
	case retentionNotificationPolicyRev:
		return "notification_policy_revisions"
	case retentionFindingChangeEvent:
		return "finding_change_events"
	case retentionNotificationPolicyEvent:
		return "notification_policy_events"
	default:
		return ""
	}
}

func historyDirectory(kind string) string {
	switch kind {
	case retentionFindingDecisionEvent:
		return "finding-decision-events"
	case retentionNotificationPolicyRev:
		return "notification-policy-revisions"
	case retentionFindingChangeEvent:
		return "finding-change-events"
	case retentionNotificationPolicyEvent:
		return "notification-policy-events"
	default:
		return ""
	}
}

func scanHistoryMetadata(record *historyArchiveRecord, key, digest *string, size *int64) {
	if key != nil {
		record.key = *key
	}
	if digest != nil {
		record.digest = *digest
	}
	if size != nil {
		record.size = *size
	}
}

func (a *database) readHistoryArchiveRecord(ctx context.Context, db queryRower,
	workspace, kind, id string) (historyArchiveRecord, error) {
	var record historyArchiveRecord
	var key, digest *string
	var size *int64
	switch kind {
	case retentionFindingDecisionEvent:
		var payload findingDecisionArchivePayload
		var changed, before, after []byte
		var created time.Time
		err := db.QueryRow(ctx, `SELECT id,workspace_id,finding_id,decision_revision,actor_id,
			action,rationale,changed_fields,before_state,after_state,created_at,
			detail_availability,detail_revision,archive_key,archive_digest,archive_size,archived_at
			FROM `+a.table("finding_decision_events")+` WHERE workspace_id=$1 AND id=$2`,
			workspace, id).Scan(&payload.ID, &payload.WorkspaceID, &payload.FindingID,
			&payload.DecisionRevision, &payload.ActorID, &payload.Action, &payload.Rationale,
			&changed, &before, &after, &created, &record.availability, &record.revision,
			&key, &digest, &size, &record.archivedAt)
		if err != nil {
			return record, err
		}
		payload.SchemaVersion, payload.ResourceKind = 1, kind
		if err = json.Unmarshal(changed, &payload.ChangedFields); err != nil {
			return record, err
		}
		if payload.BeforeState, err = canonicalJSONValue(before); err != nil {
			return record, err
		}
		if payload.AfterState, err = canonicalJSONValue(after); err != nil {
			return record, err
		}
		payload.CreatedAt = timeString(created)
		record.payload, err = json.Marshal(payload)
		if err != nil {
			return record, err
		}
	case retentionNotificationPolicyRev:
		var payload notificationPolicyRevisionArchivePayload
		var created time.Time
		err := db.QueryRow(ctx, `SELECT id,workspace_id,policy_id,name,connection_id,
			connection_profile,connection_revision,enabled,change_kinds,minimum_severity,
			revision,epoch,actor_id,actor_name,rationale,created_at,
			detail_availability,detail_revision,archive_key,archive_digest,archive_size,archived_at
			FROM `+a.table("notification_policy_revisions")+` WHERE workspace_id=$1 AND id=$2`,
			workspace, id).Scan(&payload.ID, &payload.WorkspaceID, &payload.PolicyID, &payload.Name,
			&payload.ConnectionID, &payload.ConnectionProfile, &payload.ConnectionRevision,
			&payload.Enabled, &payload.ChangeKinds, &payload.MinimumSeverity, &payload.Revision,
			&payload.Epoch, &payload.ActorID, &payload.ActorName, &payload.Rationale, &created,
			&record.availability, &record.revision, &key, &digest, &size, &record.archivedAt)
		if err != nil {
			return record, err
		}
		payload.SchemaVersion, payload.ResourceKind = 1, kind
		payload.CreatedAt = timeString(created)
		record.payload, err = json.Marshal(payload)
		if err != nil {
			return record, err
		}
	case retentionFindingChangeEvent:
		var payload findingChangeArchivePayload
		var changed time.Time
		var lease *time.Time
		err := db.QueryRow(ctx, `SELECT id,workspace_id,finding_id,policy_epoch,title,severity,
			asset_name,change_kind,change_revision,change_at,state,worker_id,fence,lease_until,
			detail_availability,detail_revision,archive_key,archive_digest,archive_size,archived_at
			FROM `+a.table("finding_change_events")+` WHERE workspace_id=$1 AND id=$2`,
			workspace, id).Scan(&payload.ID, &payload.WorkspaceID, &payload.FindingID,
			&payload.PolicyEpoch, &payload.Title, &payload.Severity, &payload.AssetName,
			&payload.ChangeKind, &payload.ChangeRevision, &changed, &payload.State,
			&payload.WorkerID, &payload.Fence, &lease, &record.availability, &record.revision,
			&key, &digest, &size, &record.archivedAt)
		if err != nil {
			return record, err
		}
		payload.SchemaVersion, payload.ResourceKind = 1, kind
		payload.ChangeAt = timeString(changed)
		if lease != nil {
			value := timeString(*lease)
			payload.LeaseUntil = &value
		}
		record.payload, err = json.Marshal(payload)
		if err != nil {
			return record, err
		}
	case retentionNotificationPolicyEvent:
		var payload notificationPolicyEventArchivePayload
		var created time.Time
		err := db.QueryRow(ctx, `SELECT id,workspace_id,policy_id,policy_revision,finding_id,
			finding_change_revision,outcome,delivery_id,created_at,
			detail_availability,detail_revision,archive_key,archive_digest,archive_size,archived_at
			FROM `+a.table("notification_policy_events")+` WHERE workspace_id=$1 AND id=$2`,
			workspace, id).Scan(&payload.ID, &payload.WorkspaceID, &payload.PolicyID,
			&payload.PolicyRevision, &payload.FindingID, &payload.FindingChangeRevision,
			&payload.Outcome, &payload.DeliveryID, &created, &record.availability,
			&record.revision, &key, &digest, &size, &record.archivedAt)
		if err != nil {
			return record, err
		}
		payload.SchemaVersion, payload.ResourceKind = 1, kind
		payload.CreatedAt = timeString(created)
		record.payload, err = json.Marshal(payload)
		if err != nil {
			return record, err
		}
	default:
		return record, pgx.ErrNoRows
	}
	scanHistoryMetadata(&record, key, digest, size)
	return record, nil
}

func (w *RetentionWorker) historyProtection(ctx context.Context, db queryRower,
	workspace, kind, id string) ([]string, error) {
	var hold, current, pending, active bool
	if err := db.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM `+w.table("retention_holds")+`
		WHERE workspace_id=$1 AND resource_kind=$2 AND resource_id=$3 AND released_at IS NULL)`,
		workspace, kind, id).Scan(&hold); err != nil {
		return nil, err
	}
	switch kind {
	case retentionNotificationPolicyRev:
		if err := db.QueryRow(ctx, `SELECT
			EXISTS(SELECT 1 FROM `+w.table("notification_policy_revisions")+` r
			 JOIN `+w.table("notification_policies")+` p
			 ON p.workspace_id=r.workspace_id AND p.id=r.policy_id AND p.revision=r.revision
			 WHERE r.workspace_id=$1 AND r.id=$2),
			EXISTS(SELECT 1 FROM `+w.table("notification_policy_revisions")+` r
			 JOIN `+w.table("finding_change_events")+` e
			 ON e.workspace_id=r.workspace_id AND e.state IN ('pending','processing')
			 AND r.epoch<=e.policy_epoch
			 WHERE r.workspace_id=$1 AND r.id=$2
			 AND NOT EXISTS(SELECT 1 FROM `+w.table("notification_policy_revisions")+` later
			  WHERE later.workspace_id=r.workspace_id AND later.policy_id=r.policy_id
			  AND later.epoch>r.epoch AND later.epoch<=e.policy_epoch))`,
			workspace, id).Scan(&current, &pending); err != nil {
			return nil, err
		}
	case retentionFindingChangeEvent:
		if err := db.QueryRow(ctx, `SELECT state IN ('pending','processing') FROM `+
			w.table("finding_change_events")+` WHERE workspace_id=$1 AND id=$2`,
			workspace, id).Scan(&pending); err != nil {
			return nil, err
		}
	case retentionNotificationPolicyEvent:
		if err := db.QueryRow(ctx, `SELECT COALESCE(d.state IN ('queued','dispatching'),false)
			FROM `+w.table("notification_policy_events")+` e
			LEFT JOIN `+w.table("finding_deliveries")+` d
			ON d.workspace_id=e.workspace_id AND d.id=e.delivery_id
			WHERE e.workspace_id=$1 AND e.id=$2`, workspace, id).Scan(&active); err != nil {
			return nil, err
		}
	}
	return historyRetentionReasons(hold, current, pending, active), nil
}

func (w *RetentionWorker) existingHistoryPublication(ctx context.Context, run retentionRunRecord,
	item retentionItemRecord, directory string, payload []byte) (retentionArchiveWrite, bool, error) {
	key, digest, err := w.store.archiveIdentity(run.WorkspaceID, directory, item.ResourceID, payload)
	if err != nil {
		return retentionArchiveWrite{}, false, err
	}
	publication, err := scanArchivePublication(w.pool.QueryRow(ctx, `SELECT
		id,workspace_id,object_key,resource_kind,resource_id,object_digest,
		object_size,state,revision,created_at,updated_at
		FROM `+w.table("archive_publications")+`
		WHERE workspace_id=$1 AND object_key=$2`, run.WorkspaceID, key))
	if errors.Is(err, pgx.ErrNoRows) {
		return retentionArchiveWrite{}, false, nil
	}
	if err != nil {
		return retentionArchiveWrite{}, false, err
	}
	if publication.ResourceKind != item.ResourceKind || publication.ResourceID != item.ResourceID ||
		publication.ObjectDigest != digest || publication.ObjectSize != int64(len(payload)) {
		return retentionArchiveWrite{}, false, nil
	}
	verified, err := withRetentionLease(ctx, w, run, func(storageContext context.Context) ([]byte, error) {
		return w.store.readArchive(storageContext, run.WorkspaceID, key, digest, int64(len(payload)))
	})
	if err != nil || !bytes.Equal(verified, payload) {
		return retentionArchiveWrite{}, false, nil
	}
	return retentionArchiveWrite{key: key, digest: digest, publication: publication}, true, nil
}

func (w *RetentionWorker) publishHistory(ctx context.Context, run retentionRunRecord,
	item retentionItemRecord, directory string, payload []byte) (retentionArchiveWrite, error) {
	if existing, present, err := w.existingHistoryPublication(ctx, run, item, directory, payload); err != nil {
		return retentionArchiveWrite{}, err
	} else if present {
		return existing, nil
	}
	publication, err := w.beginArchivePublication(ctx, run, item, directory, payload)
	if err != nil {
		return retentionArchiveWrite{}, err
	}
	return withRetentionLease(ctx, w, run, func(storageContext context.Context) (retentionArchiveWrite, error) {
		key, digest, writeErr := w.store.putArchive(
			storageContext, run.WorkspaceID, directory, item.ResourceID, payload)
		return retentionArchiveWrite{key: key, digest: digest, publication: publication}, writeErr
	})
}

func (w *RetentionWorker) archiveHistoryEvent(ctx context.Context,
	run retentionRunRecord, item retentionItemRecord) (retentionOutcome, error) {
	record, err := w.readHistoryArchiveRecord(ctx, w.pool, run.WorkspaceID, item.ResourceKind, item.ResourceID)
	if err != nil {
		return retentionOutcome{}, err
	}
	if record.availability == "archived" {
		return retentionOutcome{state: "succeeded", outcome: "already-archived"}, nil
	}
	if record.availability != "available" {
		return retentionOutcome{state: "protected", outcome: "state-changed", reasons: []string{"state-changed"}}, nil
	}
	reasons, err := w.historyProtection(ctx, w.pool, run.WorkspaceID, item.ResourceKind, item.ResourceID)
	if err != nil {
		return retentionOutcome{}, err
	}
	if len(reasons) != 0 {
		return retentionOutcome{state: "protected", outcome: "protected", reasons: reasons}, nil
	}
	directory := historyDirectory(item.ResourceKind)
	published, err := w.publishHistory(ctx, run, item, directory, record.payload)
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
	current, err := w.readHistoryArchiveRecord(ctx, tx, run.WorkspaceID, item.ResourceKind, item.ResourceID)
	if err != nil {
		return retentionOutcome{}, err
	}
	reasons, err = w.historyProtection(ctx, tx, run.WorkspaceID, item.ResourceKind, item.ResourceID)
	if err != nil {
		return retentionOutcome{}, err
	}
	if current.availability != "available" || !bytes.Equal(current.payload, record.payload) {
		if err = w.finishArchivePublication(ctx, tx, published.publication, "orphan"); err != nil {
			return retentionOutcome{}, err
		}
		if err = tx.Commit(ctx); err != nil {
			return retentionOutcome{}, err
		}
		return retentionOutcome{state: "protected", outcome: "state-changed", reasons: []string{"state-changed"}}, nil
	}
	if len(reasons) != 0 {
		if err = w.finishArchivePublication(ctx, tx, published.publication, "orphan"); err != nil {
			return retentionOutcome{}, err
		}
		if err = tx.Commit(ctx); err != nil {
			return retentionOutcome{}, err
		}
		return retentionOutcome{state: "protected", outcome: "protected", reasons: reasons}, nil
	}
	table := historyTable(item.ResourceKind)
	result, err := tx.Exec(ctx, `UPDATE `+w.table(table)+`
		SET detail_availability='archived',detail_revision=detail_revision+1,
			archive_key=$4,archive_digest=$5,archive_size=$6,archived_at=$7
		WHERE workspace_id=$1 AND id=$2 AND detail_revision=$3 AND detail_availability='available'`,
		run.WorkspaceID, item.ResourceID, current.revision, published.key,
		published.digest, len(record.payload), w.now())
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

func (a *Application) historyEvidence(w http.ResponseWriter, r *http.Request,
	workspace, kind, id string) error {
	record, err := a.readHistoryArchiveRecord(r.Context(), a.pool, workspace, kind, id)
	if err != nil {
		return err
	}
	if record.availability == "available" {
		return errHistoryNotArchived
	}
	if a.archiveEvidence == nil || record.key == "" || record.digest == "" ||
		record.size < 0 || record.archivedAt == nil {
		return errUnavailable
	}
	data, err := a.archiveEvidence.readExact(r.Context(), workspace, evidence.Ref{
		WorkspaceID: workspace, Bucket: a.archiveEvidence.config.Bucket, Key: record.key,
		SHA256: record.digest, SizeBytes: record.size,
	})
	if err != nil {
		state, problem := evidenceAvailabilityError(err)
		if state != "" {
			if _, updateErr := a.pool.Exec(r.Context(), `UPDATE `+a.table(historyTable(kind))+`
				SET detail_availability=$4,detail_revision=detail_revision+1
				WHERE workspace_id=$1 AND id=$2 AND detail_revision=$3
				AND detail_availability IN ('archived','missing','corrupt')`,
				workspace, id, record.revision, state); updateErr != nil {
				return updateErr
			}
			return problem
		}
		return err
	}
	if record.availability != "archived" {
		result, updateErr := a.pool.Exec(r.Context(), `UPDATE `+a.table(historyTable(kind))+`
			SET detail_availability='archived',detail_revision=detail_revision+1
			WHERE workspace_id=$1 AND id=$2 AND detail_revision=$3
			AND detail_availability IN ('missing','corrupt')`, workspace, id, record.revision)
		if updateErr != nil {
			return updateErr
		}
		if result.RowsAffected() == 1 {
			record.revision++
		}
		record.availability = "archived"
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="history-`+kind+`-`+id+`.json"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.Header().Set("X-ASPM-History-Resource-Kind", kind)
	w.Header().Set("X-ASPM-History-Resource-ID", id)
	w.Header().Set("X-ASPM-History-Availability", "archived")
	w.Header().Set("X-ASPM-Archive-Digest", record.digest)
	w.Header().Set("X-ASPM-Archive-Size", strconv.FormatInt(record.size, 10))
	w.Header().Set("X-ASPM-Detail-Revision", strconv.FormatInt(record.revision, 10))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
	return nil
}
