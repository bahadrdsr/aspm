package app

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

type ReportSnapshotSummary struct {
	ID            string     `json:"id"`
	WorkspaceID   string     `json:"workspaceId"`
	Name          string     `json:"name"`
	RequestedBy   string     `json:"requestedBy"`
	FreshnessDays int        `json:"freshnessDays"`
	State         string     `json:"state"`
	CreatedAt     time.Time  `json:"createdAt"`
	CompletedAt   *time.Time `json:"completedAt"`
	Failure       *Failure   `json:"failure"`
}

type ReportSnapshot struct {
	ReportSnapshotSummary
	Report *PostureReport `json:"report"`
}

type reportJob struct {
	ReportSnapshot
	WorkerID string
	Fence    int64
}

func reportSnapshotColumns(alias string) string {
	fields := []string{"id", "workspace_id", "name", "requested_by", "freshness_days", "state", "created_at",
		"completed_at", "failure_code", "failure_message", "report", "worker_id", "fence"}
	if alias != "" {
		for i := range fields {
			fields[i] = alias + "." + fields[i]
		}
	}
	return strings.Join(fields, ",")
}

func scanReportSnapshot(row pgx.Row) (reportJob, error) {
	var job reportJob
	var raw []byte
	var failureCode, failureMessage, worker *string
	err := row.Scan(&job.ID, &job.WorkspaceID, &job.Name, &job.RequestedBy, &job.FreshnessDays, &job.State,
		&job.CreatedAt, &job.CompletedAt, &failureCode, &failureMessage, &raw, &worker, &job.Fence)
	if err != nil {
		return job, err
	}
	if raw != nil {
		if err = json.Unmarshal(raw, &job.Report); err != nil {
			return job, err
		}
	}
	if worker != nil {
		job.WorkerID = *worker
	}
	if failureCode != nil && failureMessage != nil {
		job.Failure = &Failure{Code: *failureCode, Message: *failureMessage, Retryable: job.State == "queued"}
	}
	return job, nil
}

func (a *Application) createReportSnapshot(w http.ResponseWriter, r *http.Request, workspace, userID string) error {
	var input struct {
		Name          string        `json:"name"`
		FreshnessDays optional[int] `json:"freshnessDays"`
	}
	if err := a.decode(w, r, &input, 16<<10); err != nil {
		return err
	}
	days := defaultFreshnessDays
	if input.FreshnessDays.Set {
		if input.FreshnessDays.Value == nil {
			return errInvalid
		}
		days = *input.FreshnessDays.Value
	}
	if !validText(input.Name, 256) || !validFreshnessDays(days) {
		return errInvalid
	}
	job, err := scanReportSnapshot(a.pool.QueryRow(r.Context(), `INSERT INTO `+a.table("report_snapshots")+`
		(id,workspace_id,name,requested_by,freshness_days,created_at) VALUES($1,$2,$3,$4,$5,$6)
		RETURNING `+reportSnapshotColumns(""), newID(), workspace, input.Name, userID, days, a.config.Now().UTC()))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"dataOrigin": "live", "snapshot": job.ReportSnapshot})
	return nil
}

func (a *Application) getReportSnapshot(w http.ResponseWriter, r *http.Request, workspace, id string) error {
	job, err := scanReportSnapshot(a.pool.QueryRow(r.Context(), `SELECT `+reportSnapshotColumns("")+
		` FROM `+a.table("report_snapshots")+` WHERE workspace_id=$1 AND id=$2`, workspace, id))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"dataOrigin": "live", "snapshot": job.ReportSnapshot})
	return nil
}

func (a *Application) listReportSnapshots(w http.ResponseWriter, r *http.Request, workspace string) error {
	limit, cursor, err := pageParameters(r)
	if err != nil {
		return err
	}
	tx, err := a.pool.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer rollback(tx)
	var total int64
	if err = tx.QueryRow(r.Context(), `SELECT count(*) FROM `+a.table("report_snapshots")+` WHERE workspace_id=$1`, workspace).Scan(&total); err != nil {
		return err
	}
	rows, err := tx.Query(r.Context(), `SELECT `+reportSnapshotColumns("")+` FROM `+a.table("report_snapshots")+`
		WHERE workspace_id=$1 AND id>$2 ORDER BY id LIMIT $3`, workspace, cursor, limit+1)
	if err != nil {
		return err
	}
	items := []ReportSnapshotSummary{}
	for rows.Next() {
		job, scanErr := scanReportSnapshot(rows)
		if scanErr != nil {
			rows.Close()
			return scanErr
		}
		items = append(items, job.ReportSnapshotSummary)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	var next *string
	if len(items) > limit {
		items = items[:limit]
		next = &items[len(items)-1].ID
	}
	writeJSON(w, http.StatusOK, map[string]any{"dataOrigin": "live", "items": items, "total": total, "nextCursor": next})
	return nil
}
