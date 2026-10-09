package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	reportExportContentLimit = 256 << 10
	reportExportFailureCode  = "report-export-generation-failed"
	reportExportFailureText  = "Report export generation could not be committed"
)

type ReportExport struct {
	ID           string     `json:"id"`
	WorkspaceID  string     `json:"workspaceId"`
	SnapshotID   string     `json:"snapshotId"`
	SnapshotName string     `json:"snapshotName"`
	RequestedBy  string     `json:"requestedBy"`
	Format       string     `json:"format"`
	State        string     `json:"state"`
	CreatedAt    time.Time  `json:"createdAt"`
	CompletedAt  *time.Time `json:"completedAt"`
	Failure      *Failure   `json:"failure"`
	Digest       *string    `json:"digest"`
	SizeBytes    *int64     `json:"sizeBytes"`
	Filename     *string    `json:"filename"`
}

type reportExportJob struct {
	ID, WorkspaceID, SnapshotID, Format, WorkerID string
	Fence                                         int64
	Attempts                                      int
}

func reportExportReceiptColumns(exportAlias, snapshotAlias string) string {
	fields := []string{
		"id", "workspace_id", "snapshot_id", "requested_by", "format", "state", "created_at",
		"completed_at", "failure_code", "failure_message", "content_digest", "content_size",
	}
	for index := range fields {
		fields[index] = exportAlias + "." + fields[index]
	}
	return strings.Join(fields[:3], ",") + "," + snapshotAlias + ".name," +
		strings.Join(fields[3:], ",")
}

func scanReportExport(row pgx.Row) (ReportExport, error) {
	var result ReportExport
	var failureCode, failureMessage, digest *string
	var size *int64
	err := row.Scan(
		&result.ID, &result.WorkspaceID, &result.SnapshotID, &result.SnapshotName,
		&result.RequestedBy, &result.Format, &result.State, &result.CreatedAt,
		&result.CompletedAt, &failureCode, &failureMessage, &digest, &size,
	)
	if err != nil {
		return result, err
	}
	if result.State == "failed" && failureCode != nil && failureMessage != nil {
		result.Failure = &Failure{Code: *failureCode, Message: *failureMessage, Retryable: false}
	}
	if result.State == "succeeded" && digest != nil && size != nil {
		filename := "aspm-report-" + result.SnapshotID + "." + result.Format
		result.Digest, result.SizeBytes, result.Filename = digest, size, &filename
	}
	return result, nil
}

func reportExportStatus(state string) int {
	if state == "succeeded" || state == "failed" {
		return http.StatusOK
	}
	return http.StatusAccepted
}

func writeReportExport(w http.ResponseWriter, status int, value ReportExport) {
	writeJSON(w, status, map[string]any{"dataOrigin": "live", "export": value})
}

func validReportExportFormat(value string) bool { return value == "json" || value == "csv" }

func (a *Application) createReportExport(w http.ResponseWriter, r *http.Request,
	workspace, userID string) error {
	var input struct {
		SnapshotID     string `json:"snapshotId"`
		Format         string `json:"format"`
		IdempotencyKey string `json:"idempotencyKey"`
	}
	if err := a.decode(w, r, &input, 16<<10); err != nil {
		return err
	}
	if !validID(input.SnapshotID) || !validReportExportFormat(input.Format) ||
		!validText(input.IdempotencyKey, 256) {
		return errInvalid
	}

	tx, err := a.pool.Begin(r.Context())
	if err != nil {
		return err
	}
	defer rollback(tx)

	var state string
	var hasReport, hasCompletion bool
	err = tx.QueryRow(r.Context(), `SELECT state,report IS NOT NULL,completed_at IS NOT NULL
		FROM `+a.table("report_snapshots")+` WHERE workspace_id=$1 AND id=$2`,
		workspace, input.SnapshotID).Scan(&state, &hasReport, &hasCompletion)
	if err != nil {
		return err
	}
	if state != "succeeded" || !hasReport || !hasCompletion {
		return errConflict
	}

	_, err = tx.Exec(r.Context(), `INSERT INTO `+a.table("report_exports")+`
		(id,workspace_id,snapshot_id,requested_by,format,idempotency_key,created_at)
		VALUES($1,$2,$3,$4,$5,$6,$7)
		ON CONFLICT(workspace_id,idempotency_key) DO NOTHING`,
		newID(), workspace, input.SnapshotID, userID, input.Format, input.IdempotencyKey, a.now().UTC())
	if err != nil {
		return err
	}
	result, err := scanReportExport(tx.QueryRow(r.Context(), `SELECT `+
		reportExportReceiptColumns("e", "s")+` FROM `+a.table("report_exports")+` e
		JOIN `+a.table("report_snapshots")+` s
		ON s.workspace_id=e.workspace_id AND s.id=e.snapshot_id
		WHERE e.workspace_id=$1 AND e.idempotency_key=$2`, workspace, input.IdempotencyKey))
	if err != nil {
		return err
	}
	if result.SnapshotID != input.SnapshotID || result.Format != input.Format {
		return errConflict
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	writeReportExport(w, reportExportStatus(result.State), result)
	return nil
}

func reportExportPageParameters(r *http.Request) (int, string, error) {
	query, err := url.ParseQuery(r.URL.RawQuery)
	if err != nil {
		return 0, "", errInvalid
	}
	for key, values := range query {
		if (key != "limit" && key != "cursor") || len(values) != 1 {
			return 0, "", errInvalid
		}
	}
	limit := 100
	if values, present := query["limit"]; present {
		if values[0] == "" {
			return 0, "", errInvalid
		}
		parsed, parseErr := strconv.Atoi(values[0])
		if parseErr != nil || parsed < 1 || parsed > 100 {
			return 0, "", errInvalid
		}
		limit = parsed
	}
	cursor := ""
	if values, present := query["cursor"]; present {
		if !validID(values[0]) {
			return 0, "", errInvalid
		}
		cursor = values[0]
	}
	return limit, cursor, nil
}

func (a *Application) listReportExports(w http.ResponseWriter, r *http.Request, workspace string) error {
	limit, cursor, err := reportExportPageParameters(r)
	if err != nil {
		return err
	}
	tx, err := a.pool.BeginTx(r.Context(), pgx.TxOptions{
		IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly,
	})
	if err != nil {
		return err
	}
	defer rollback(tx)
	var total int64
	if err = tx.QueryRow(r.Context(), `SELECT count(*) FROM `+a.table("report_exports")+
		` WHERE workspace_id=$1`, workspace).Scan(&total); err != nil {
		return err
	}
	rows, err := tx.Query(r.Context(), `SELECT `+reportExportReceiptColumns("e", "s")+
		` FROM `+a.table("report_exports")+` e
		JOIN `+a.table("report_snapshots")+` s
		ON s.workspace_id=e.workspace_id AND s.id=e.snapshot_id
		WHERE e.workspace_id=$1 AND e.id>$2 ORDER BY e.id LIMIT $3`,
		workspace, cursor, limit+1)
	if err != nil {
		return err
	}
	items := []ReportExport{}
	for rows.Next() {
		item, scanErr := scanReportExport(rows)
		if scanErr != nil {
			rows.Close()
			return scanErr
		}
		items = append(items, item)
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
	writeJSON(w, http.StatusOK, map[string]any{
		"dataOrigin": "live", "items": items, "total": total, "nextCursor": next,
	})
	return nil
}

func (a *Application) getReportExport(w http.ResponseWriter, r *http.Request,
	workspace, id string) error {
	result, err := scanReportExport(a.pool.QueryRow(r.Context(), `SELECT `+
		reportExportReceiptColumns("e", "s")+` FROM `+a.table("report_exports")+` e
		JOIN `+a.table("report_snapshots")+` s
		ON s.workspace_id=e.workspace_id AND s.id=e.snapshot_id
		WHERE e.workspace_id=$1 AND e.id=$2`, workspace, id))
	if err != nil {
		return err
	}
	writeReportExport(w, http.StatusOK, result)
	return nil
}

func (a *Application) getReportExportContent(w http.ResponseWriter, r *http.Request,
	workspace, id string) error {
	var snapshotID, format, state string
	var content []byte
	var digest *string
	var size *int64
	err := a.pool.QueryRow(r.Context(), `SELECT snapshot_id,format,state,content,content_digest,content_size
		FROM `+a.table("report_exports")+` WHERE workspace_id=$1 AND id=$2`,
		workspace, id).Scan(&snapshotID, &format, &state, &content, &digest, &size)
	if err != nil {
		return err
	}
	if state != "succeeded" {
		return errConflict
	}
	sum := sha256.Sum256(content)
	actualDigest := fmt.Sprintf("sha256:%x", sum)
	if digest == nil || size == nil || *size != int64(len(content)) || *digest != actualDigest {
		return errEvidenceCorrupt
	}
	contentType := "application/json; charset=utf-8"
	if format == "csv" {
		contentType = "text/csv; charset=utf-8"
	}
	filename := "aspm-report-" + snapshotID + "." + format
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Content-Length", strconv.FormatInt(*size, 10))
	w.Header().Set("X-ASPM-Content-Digest", *digest)
	w.Header().Set("X-ASPM-Content-Length", strconv.FormatInt(*size, 10))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(content)
	return nil
}

func (a *ReportWorker) claimReportExport(ctx context.Context, worker string) (reportExportJob, error) {
	_, err := a.pool.Exec(ctx, `WITH exhausted AS (
		SELECT id FROM `+a.table("report_exports")+` WHERE attempts>=$1 AND
		(state='queued' OR (state='processing' AND lease_until<=clock_timestamp()))
		ORDER BY available_at,id FOR UPDATE SKIP LOCKED LIMIT 100)
		UPDATE `+a.table("report_exports")+` job SET state='failed',completed_at=$4,
		failure_code=$2,failure_message=$3,worker_id=NULL,lease_until=NULL
		FROM exhausted e WHERE job.id=e.id`,
		reportAttempts, reportExportFailureCode, reportExportFailureText, a.now().UTC())
	if err != nil {
		return reportExportJob{}, err
	}
	var result reportExportJob
	var claimedWorker *string
	err = a.pool.QueryRow(ctx, `WITH candidate AS (
		SELECT id FROM `+a.table("report_exports")+` WHERE attempts<$1 AND
		((state='queued' AND available_at<=clock_timestamp()) OR
		(state='processing' AND lease_until<=clock_timestamp()))
		ORDER BY available_at,id FOR UPDATE SKIP LOCKED LIMIT 1)
		UPDATE `+a.table("report_exports")+` job SET state='processing',worker_id=$2,
		fence=job.fence+1,attempts=job.attempts+1,
		lease_until=clock_timestamp()+($3::double precision*interval '1 second')
		FROM candidate c WHERE job.id=c.id
		RETURNING job.id,job.workspace_id,job.snapshot_id,job.format,
			job.worker_id,job.fence,job.attempts`,
		reportAttempts, worker, reportLease.Seconds()).Scan(
		&result.ID, &result.WorkspaceID, &result.SnapshotID, &result.Format,
		&claimedWorker, &result.Fence, &result.Attempts,
	)
	if claimedWorker != nil {
		result.WorkerID = *claimedWorker
	}
	return result, err
}

func (a *ReportWorker) processReportExport(ctx context.Context, job reportExportJob) error {
	workCtx, cancel := context.WithTimeout(ctx, reportWorkTime)
	err := a.completeReportExport(workCtx, job)
	cancel()
	if err == nil || errors.Is(err, errReportLeaseLost) {
		return err
	}
	if ctx.Err() != nil {
		releaseCtx, release := context.WithTimeout(context.Background(), 3*time.Second)
		defer release()
		_, _ = a.pool.Exec(releaseCtx, `UPDATE `+a.table("report_exports")+`
			SET state='queued',available_at=clock_timestamp(),worker_id=NULL,lease_until=NULL
			WHERE id=$1 AND worker_id=$2 AND fence=$3 AND state='processing'`,
			job.ID, job.WorkerID, job.Fence)
		return ctx.Err()
	}
	result, updateErr := a.pool.Exec(ctx, `UPDATE `+a.table("report_exports")+` SET
		state=CASE WHEN attempts<$4 THEN 'queued' ELSE 'failed' END,
		available_at=clock_timestamp()+interval '1 second'*attempts,
		completed_at=CASE WHEN attempts<$4 THEN NULL ELSE $7::timestamptz END,
		failure_code=CASE WHEN attempts<$4 THEN NULL ELSE $5 END,
		failure_message=CASE WHEN attempts<$4 THEN NULL ELSE $6 END,
		worker_id=NULL,lease_until=NULL
		WHERE id=$1 AND worker_id=$2 AND fence=$3 AND state='processing'
		AND lease_until>clock_timestamp()`,
		job.ID, job.WorkerID, job.Fence, reportAttempts,
		reportExportFailureCode, reportExportFailureText, a.now().UTC())
	if updateErr != nil {
		return errors.New("record report export failure failed")
	}
	if result.RowsAffected() != 1 {
		return errReportLeaseLost
	}
	return nil
}

func (a *ReportWorker) completeReportExport(ctx context.Context, job reportExportJob) error {
	tx, err := a.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return err
	}
	defer rollback(tx)
	var active bool
	if err = tx.QueryRow(ctx, `SELECT state='processing' AND worker_id=$2 AND fence=$3
		AND lease_until>clock_timestamp() FROM `+a.table("report_exports")+`
		WHERE id=$1 FOR UPDATE`, job.ID, job.WorkerID, job.Fence).Scan(&active); err != nil {
		return err
	}
	if !active {
		return errReportLeaseLost
	}
	var name string
	var completedAt time.Time
	var raw []byte
	if err = tx.QueryRow(ctx, `SELECT name,completed_at,report FROM `+a.table("report_snapshots")+`
		WHERE workspace_id=$1 AND id=$2 AND state='succeeded'
		AND completed_at IS NOT NULL AND report IS NOT NULL`,
		job.WorkspaceID, job.SnapshotID).Scan(&name, &completedAt, &raw); err != nil {
		return err
	}
	report, err := decodeSavedPostureReport(raw, job.WorkspaceID, completedAt)
	if err != nil {
		return err
	}
	content, err := encodeReportExport(job.Format, job.SnapshotID, name, report.AsOf, report)
	if err != nil {
		return err
	}
	if len(content) > reportExportContentLimit {
		return errors.New("report export exceeds the bounded artifact limit")
	}
	digest := fmt.Sprintf("sha256:%x", sha256.Sum256(content))
	result, err := tx.Exec(ctx, `UPDATE `+a.table("report_exports")+`
		SET state='succeeded',completed_at=$7,content=$4,content_digest=$5,
			content_size=$6,failure_code=NULL,failure_message=NULL,worker_id=NULL,lease_until=NULL
		WHERE id=$1 AND worker_id=$2 AND fence=$3 AND state='processing'
		AND lease_until>clock_timestamp()`,
		job.ID, job.WorkerID, job.Fence, content, digest, len(content), a.now().UTC())
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return errReportLeaseLost
	}
	return tx.Commit(ctx)
}

func decodeSavedPostureReport(raw []byte, workspace string, completedAt time.Time) (PostureReport, error) {
	root, err := exactReportJSONObject(raw,
		"workspaceId", "asOf", "totals", "bySeverity", "coverage", "freshnessWindow", "verification")
	if err != nil {
		return PostureReport{}, err
	}
	for key, fields := range map[string][]string{
		"totals": {
			"assets", "findings", "openFindings", "acceptedRisk", "expiredAcceptedRisk",
			"suppressed", "expiredSuppression", "falsePositive", "inferredResolved", "verifiedResolved",
		},
		"bySeverity":      {"critical", "high", "medium", "low", "info"},
		"coverage":        {"scannedAssets", "unscannedAssets", "staleAssets", "unknownFreshnessAssets", "freshnessWindowDays"},
		"freshnessWindow": {"from", "to", "days"},
		"verification":    {"state", "reason"},
	} {
		if _, err = exactReportJSONObject(root[key], fields...); err != nil {
			return PostureReport{}, err
		}
	}
	var report PostureReport
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&report); err != nil {
		return report, err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return report, errors.New("saved report contains trailing JSON")
	}
	counts := []int64{
		report.Totals.Assets, report.Totals.Findings, report.Totals.OpenFindings,
		report.Totals.AcceptedRisk, report.Totals.ExpiredAcceptedRisk,
		report.Totals.Suppressed, report.Totals.ExpiredSuppression,
		report.Totals.FalsePositive, report.Totals.InferredResolved,
		report.Totals.VerifiedResolved, report.BySeverity.Critical, report.BySeverity.High,
		report.BySeverity.Medium, report.BySeverity.Low, report.BySeverity.Info,
		report.Coverage.ScannedAssets, report.Coverage.UnscannedAssets,
		report.Coverage.StaleAssets, report.Coverage.UnknownFreshnessAssets,
	}
	for _, count := range counts {
		if count < 0 {
			return report, errors.New("saved report contains a negative count")
		}
	}
	completionDelta := report.AsOf.Sub(completedAt)
	if completionDelta < 0 {
		completionDelta = -completionDelta
	}
	if report.WorkspaceID != workspace || completionDelta >= time.Microsecond ||
		!validFreshnessDays(report.Coverage.FreshnessWindowDays) ||
		report.FreshnessWindow.Days != report.Coverage.FreshnessWindowDays ||
		!report.FreshnessWindow.To.Equal(report.AsOf) ||
		!report.FreshnessWindow.From.Equal(report.AsOf.Add(
			-time.Duration(report.FreshnessWindow.Days)*24*time.Hour)) ||
		!validText(report.Verification.State, 128) ||
		!validText(report.Verification.Reason, 4096) {
		return report, errors.New("saved report is inconsistent")
	}
	return report, nil
}

func exactReportJSONObject(raw []byte, fields ...string) (map[string]json.RawMessage, error) {
	var value map[string]json.RawMessage
	if err := json.Unmarshal(raw, &value); err != nil || value == nil || len(value) != len(fields) {
		return nil, errors.New("saved report has an invalid object shape")
	}
	for _, field := range fields {
		if _, present := value[field]; !present {
			return nil, errors.New("saved report has an invalid object shape")
		}
	}
	return value, nil
}

func encodeReportExport(format, snapshotID, name string, completedAt time.Time,
	report PostureReport) ([]byte, error) {
	if format == "csv" {
		return encodeReportExportCSV(snapshotID, name, completedAt, report)
	}
	var document struct {
		APIVersion string `json:"apiVersion"`
		Export     struct {
			SnapshotID  string        `json:"snapshotId"`
			Name        string        `json:"name"`
			CompletedAt time.Time     `json:"completedAt"`
			Report      PostureReport `json:"report"`
		} `json:"export"`
	}
	document.APIVersion = APIVersion
	document.Export.SnapshotID = snapshotID
	document.Export.Name = name
	document.Export.CompletedAt = completedAt
	document.Export.Report = report
	data, err := json.Marshal(document)
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func encodeReportExportCSV(snapshotID, name string, completedAt time.Time,
	report PostureReport) ([]byte, error) {
	rows := [][]string{
		{"section", "metric", "value"},
		{"snapshot", "id", snapshotID},
		{"snapshot", "name", csvText(name)},
		{"snapshot", "completedAt", completedAt.UTC().Format(time.RFC3339Nano)},
		{"report", "workspaceId", report.WorkspaceID},
		{"report", "asOf", report.AsOf.UTC().Format(time.RFC3339Nano)},
		{"totals", "assets", strconv.FormatInt(report.Totals.Assets, 10)},
		{"totals", "findings", strconv.FormatInt(report.Totals.Findings, 10)},
		{"totals", "openFindings", strconv.FormatInt(report.Totals.OpenFindings, 10)},
		{"totals", "acceptedRisk", strconv.FormatInt(report.Totals.AcceptedRisk, 10)},
		{"totals", "expiredAcceptedRisk", strconv.FormatInt(report.Totals.ExpiredAcceptedRisk, 10)},
		{"totals", "suppressed", strconv.FormatInt(report.Totals.Suppressed, 10)},
		{"totals", "expiredSuppression", strconv.FormatInt(report.Totals.ExpiredSuppression, 10)},
		{"totals", "falsePositive", strconv.FormatInt(report.Totals.FalsePositive, 10)},
		{"totals", "inferredResolved", strconv.FormatInt(report.Totals.InferredResolved, 10)},
		{"totals", "verifiedResolved", strconv.FormatInt(report.Totals.VerifiedResolved, 10)},
		{"severity", "critical", strconv.FormatInt(report.BySeverity.Critical, 10)},
		{"severity", "high", strconv.FormatInt(report.BySeverity.High, 10)},
		{"severity", "medium", strconv.FormatInt(report.BySeverity.Medium, 10)},
		{"severity", "low", strconv.FormatInt(report.BySeverity.Low, 10)},
		{"severity", "info", strconv.FormatInt(report.BySeverity.Info, 10)},
		{"coverage", "scannedAssets", strconv.FormatInt(report.Coverage.ScannedAssets, 10)},
		{"coverage", "unscannedAssets", strconv.FormatInt(report.Coverage.UnscannedAssets, 10)},
		{"coverage", "staleAssets", strconv.FormatInt(report.Coverage.StaleAssets, 10)},
		{"coverage", "unknownFreshnessAssets", strconv.FormatInt(report.Coverage.UnknownFreshnessAssets, 10)},
		{"coverage", "freshnessWindowDays", strconv.Itoa(report.Coverage.FreshnessWindowDays)},
		{"freshnessWindow", "from", report.FreshnessWindow.From.UTC().Format(time.RFC3339Nano)},
		{"freshnessWindow", "to", report.FreshnessWindow.To.UTC().Format(time.RFC3339Nano)},
		{"freshnessWindow", "days", strconv.Itoa(report.FreshnessWindow.Days)},
		{"verification", "state", report.Verification.State},
		{"verification", "reason", report.Verification.Reason},
	}
	var result bytes.Buffer
	for _, row := range rows {
		var encoded bytes.Buffer
		writer := csv.NewWriter(&encoded)
		if err := writer.Write(row); err != nil {
			return nil, err
		}
		writer.Flush()
		if err := writer.Error(); err != nil {
			return nil, err
		}
		line := encoded.Bytes()
		if len(line) == 0 || line[len(line)-1] != '\n' {
			return nil, errors.New("CSV encoder omitted its record delimiter")
		}
		result.Write(line[:len(line)-1])
		result.WriteString("\r\n")
	}
	return result.Bytes(), nil
}
