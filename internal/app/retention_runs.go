package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type retentionRunRecord struct {
	RetentionRun
	WorkerID       string
	Fence          int64
	Attempts       int
	LeaseUntil     *time.Time
	IdempotencyKey string
	BindingDigest  string
}

func retentionRunColumns(alias string) string {
	fields := []string{"id", "workspace_id", "operation", "preview_id", "target_kind", "target_id", "state",
		"requested_by", "rationale", "created_at", "completed_at", "worker_id", "fence", "attempts", "lease_until",
		"failure_code", "failure_message", "idempotency_key", "binding_digest"}
	if alias != "" {
		for index := range fields {
			fields[index] = alias + "." + fields[index]
		}
	}
	return strings.Join(fields, ",")
}

func scanRetentionRun(row pgx.Row) (retentionRunRecord, error) {
	var run retentionRunRecord
	var worker, failureCode, failureMessage *string
	err := row.Scan(&run.ID, &run.WorkspaceID, &run.Operation, &run.PreviewID, &run.TargetKind,
		&run.TargetID, &run.State, &run.RequestedBy, &run.Rationale, &run.CreatedAt,
		&run.CompletedAt, &worker, &run.Fence, &run.Attempts, &run.LeaseUntil,
		&failureCode, &failureMessage, &run.IdempotencyKey, &run.BindingDigest)
	if err != nil {
		return run, err
	}
	run.CreatedAt = run.CreatedAt.UTC()
	if run.CompletedAt != nil {
		value := run.CompletedAt.UTC()
		run.CompletedAt = &value
	}
	if run.LeaseUntil != nil {
		value := run.LeaseUntil.UTC()
		run.LeaseUntil = &value
	}
	if worker != nil {
		run.WorkerID = *worker
	}
	if failureCode != nil {
		run.Failure = &Failure{Code: *failureCode}
		if failureMessage != nil {
			run.Failure.Message = *failureMessage
		}
	}
	return run, nil
}

func scanRetentionRunItem(row pgx.Row) (RetentionRunItem, error) {
	var item RetentionRunItem
	var reasons []byte
	var failureCode, failureMessage *string
	err := row.Scan(&item.ID, &item.Class, &item.ResourceKind, &item.ResourceID, &item.Action,
		&item.State, &reasons, &item.Outcome, &failureCode, &failureMessage,
		&item.StartedAt, &item.CompletedAt)
	if err != nil {
		return item, err
	}
	if err = json.Unmarshal(reasons, &item.ProtectedReasons); err != nil {
		return item, err
	}
	if item.StartedAt != nil {
		value := item.StartedAt.UTC()
		item.StartedAt = &value
	}
	if item.CompletedAt != nil {
		value := item.CompletedAt.UTC()
		item.CompletedAt = &value
	}
	if failureCode != nil {
		item.Failure = &Failure{
			Code:      *failureCode,
			Retryable: item.State == "queued" && *failureCode == "retention-action-failed",
		}
		if failureMessage != nil {
			item.Failure.Message = *failureMessage
		}
	}
	return item, nil
}

func (a *Application) loadRetentionRun(ctx context.Context, db retentionDB,
	workspace, id string) (retentionRunRecord, error) {
	run, err := scanRetentionRun(db.QueryRow(ctx, `SELECT `+retentionRunColumns("")+`
		FROM `+a.table("retention_runs")+` WHERE workspace_id=$1 AND id=$2`, workspace, id))
	if err != nil {
		return run, err
	}
	rows, err := db.Query(ctx, `SELECT id,class,resource_kind,resource_id,action,state,
		protected_reasons,outcome,failure_code,failure_message,started_at,completed_at
		FROM `+a.table("retention_run_items")+`
		WHERE workspace_id=$1 AND run_id=$2 ORDER BY ordinal`, workspace, id)
	if err != nil {
		return run, err
	}
	defer rows.Close()
	for rows.Next() {
		item, err := scanRetentionRunItem(rows)
		if err != nil {
			return run, err
		}
		run.Items = append(run.Items, item)
		run.Total++
		switch item.State {
		case "succeeded":
			run.Succeeded++
		case "protected":
			run.Protected++
		case "missing":
			run.Missing++
		case "corrupt":
			run.Corrupt++
		case "failed":
			run.Failed++
		}
	}
	return run, rows.Err()
}

func retentionRunDigest(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return reportDigest(data), nil
}

func (a *Application) retentionRunReplay(ctx context.Context, db queryRower,
	workspace, key, digest string) (string, bool, error) {
	var id, stored string
	err := db.QueryRow(ctx, `SELECT id,binding_digest FROM `+a.table("retention_runs")+`
		WHERE workspace_id=$1 AND idempotency_key=$2`, workspace, key).Scan(&id, &stored)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	if stored != digest {
		return "", false, errConflict
	}
	return id, true, nil
}

func (a *Application) queueRetentionExecution(w http.ResponseWriter, r *http.Request,
	workspace, actor, previewID string) error {
	var input struct {
		Revision       int64  `json:"revision"`
		SnapshotDigest string `json:"snapshotDigest"`
		Rationale      string `json:"rationale"`
		IdempotencyKey string `json:"idempotencyKey"`
	}
	if err := a.decode(w, r, &input, 16<<10); err != nil {
		return err
	}
	if input.Revision < 1 || !validDigest(input.SnapshotDigest) ||
		!validText(input.Rationale, 8192) || !validText(input.IdempotencyKey, 256) {
		return errInvalid
	}
	binding, err := retentionRunDigest(struct {
		Workspace, Actor, Preview, Snapshot, Rationale, Key string
		Revision                                            int64
	}{workspace, actor, previewID, input.SnapshotDigest, input.Rationale, input.IdempotencyKey, input.Revision})
	if err != nil {
		return err
	}
	tx, err := a.pool.Begin(r.Context())
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`,
		"aspm/retention-run/"+workspace+"/"+input.IdempotencyKey); err != nil {
		return err
	}
	if id, replay, err := a.retentionRunReplay(r.Context(), tx, workspace,
		input.IdempotencyKey, binding); err != nil {
		return err
	} else if replay {
		run, err := a.loadRetentionRun(r.Context(), tx, workspace, id)
		if err != nil {
			return err
		}
		if err = tx.Commit(r.Context()); err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, map[string]any{"retentionRun": run.RetentionRun})
		return nil
	}
	preview, err := a.loadRetentionPreview(r.Context(), tx, workspace, previewID, true)
	if err != nil {
		return err
	}
	if preview.State != "approved" || preview.Revision != input.Revision ||
		preview.SnapshotDigest != input.SnapshotDigest {
		return errConflict
	}
	runID, now := newID(), a.config.Now().UTC()
	if _, err = tx.Exec(r.Context(), `INSERT INTO `+a.table("retention_runs")+`
		(id,workspace_id,operation,preview_id,state,requested_by,rationale,idempotency_key,binding_digest,created_at)
		VALUES($1,$2,'apply-preview',$3,'queued',$4,$5,$6,$7,$8)`,
		runID, workspace, previewID, actor, input.Rationale, input.IdempotencyKey, binding, now); err != nil {
		return err
	}
	rows, err := tx.Query(r.Context(), `SELECT ordinal,class,resource_kind,resource_id,action
		FROM `+a.table("retention_preview_items")+`
		WHERE workspace_id=$1 AND preview_id=$2 ORDER BY ordinal`, workspace, previewID)
	if err != nil {
		return err
	}
	type previewItem struct {
		ordinal                       int
		class, kind, resource, action string
	}
	items := []previewItem{}
	ordinal := 0
	for rows.Next() {
		var item previewItem
		if err = rows.Scan(&item.ordinal, &item.class, &item.kind, &item.resource, &item.action); err != nil {
			rows.Close()
			return err
		}
		if item.ordinal != ordinal {
			rows.Close()
			return errConflict
		}
		items = append(items, item)
		ordinal++
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, item := range items {
		if _, err = tx.Exec(r.Context(), `INSERT INTO `+a.table("retention_run_items")+`
			(id,workspace_id,run_id,ordinal,class,resource_kind,resource_id,action)
			VALUES($1,$2,$3,$4,$5,$6,$7,$8)`,
			newID(), workspace, runID, item.ordinal, item.class, item.kind, item.resource, item.action); err != nil {
			return err
		}
	}
	run, err := a.loadRetentionRun(r.Context(), tx, workspace, runID)
	if err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"retentionRun": run.RetentionRun})
	return nil
}

func (a *Application) queueObservationRestore(w http.ResponseWriter, r *http.Request,
	workspace, actor, observationID string) error {
	var input struct {
		Rationale      string `json:"rationale"`
		IdempotencyKey string `json:"idempotencyKey"`
	}
	if err := a.decode(w, r, &input, 16<<10); err != nil {
		return err
	}
	if !validText(input.Rationale, 8192) || !validText(input.IdempotencyKey, 256) {
		return errInvalid
	}
	binding, err := retentionRunDigest(struct {
		Workspace, Actor, Observation, Rationale, Key string
	}{workspace, actor, observationID, input.Rationale, input.IdempotencyKey})
	if err != nil {
		return err
	}
	tx, err := a.pool.Begin(r.Context())
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`,
		"aspm/retention-run/"+workspace+"/"+input.IdempotencyKey); err != nil {
		return err
	}
	if id, replay, err := a.retentionRunReplay(r.Context(), tx, workspace,
		input.IdempotencyKey, binding); err != nil {
		return err
	} else if replay {
		run, err := a.loadRetentionRun(r.Context(), tx, workspace, id)
		if err != nil {
			return err
		}
		if err = tx.Commit(r.Context()); err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, map[string]any{"retentionRun": run.RetentionRun})
		return nil
	}
	var availability, transition string
	if err = tx.QueryRow(r.Context(), `SELECT evidence_availability,COALESCE(retention_transition,'')
		FROM `+a.table("observations")+`
		WHERE workspace_id=$1 AND id=$2 FOR UPDATE`, workspace, observationID).Scan(&availability, &transition); err != nil {
		return err
	}
	if availability != "archived" || transition != "" {
		return errConflict
	}
	runID, now := newID(), a.config.Now().UTC()
	if _, err = tx.Exec(r.Context(), `INSERT INTO `+a.table("retention_runs")+`
		(id,workspace_id,operation,target_kind,target_id,state,requested_by,rationale,idempotency_key,binding_digest,created_at)
		VALUES($1,$2,'restore-observation','observation',$3,'queued',$4,$5,$6,$7,$8)`,
		runID, workspace, observationID, actor, input.Rationale, input.IdempotencyKey, binding, now); err != nil {
		return err
	}
	if _, err = tx.Exec(r.Context(), `INSERT INTO `+a.table("retention_run_items")+`
		(id,workspace_id,run_id,ordinal,class,resource_kind,resource_id,action)
		VALUES($1,$2,$3,0,'archived-evidence','observation',$4,'restore-archive')`,
		newID(), workspace, runID, observationID); err != nil {
		return err
	}
	run, err := a.loadRetentionRun(r.Context(), tx, workspace, runID)
	if err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"retentionRun": run.RetentionRun})
	return nil
}

func (a *Application) getRetentionRun(w http.ResponseWriter, r *http.Request, workspace, id string) error {
	run, err := a.loadRetentionRun(r.Context(), a.pool, workspace, id)
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"retentionRun": run.RetentionRun})
	return nil
}
