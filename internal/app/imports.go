package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bahadrdsr/aspm/internal/parsers"
	"github.com/jackc/pgx/v5"
)

const importReplayWindow = 24 * time.Hour

var errReplayExpired = &apiError{409, "replay-expired", "The accepted scan's replay window has expired"}

type importInput struct {
	APIVersion   string          `json:"apiVersion"`
	AssetID      string          `json:"assetId"`
	Format       string          `json:"format"`
	Report       string          `json:"report"`
	SourceID     string          `json:"sourceId"`
	ScanID       string          `json:"scanId"`
	Scope        Scope           `json:"scope"`
	SourceScanAt *time.Time      `json:"sourceScanAt"`
	CollectedAt  time.Time       `json:"collectedAt"`
	SourceStatus string          `json:"sourceStatus"`
	ScanKind     string          `json:"scanKind"`
	Completeness string          `json:"completeness"`
	Mapping      parsers.Mapping `json:"mapping"`
}

func validImport(input importInput) bool {
	if input.APIVersion != APIVersion || !validID(input.AssetID) ||
		!validText(input.SourceID, 512) || !validText(input.ScanID, 512) ||
		!validText(input.Scope.ID, 512) || !validText(input.Scope.Revision, 512) ||
		!validText(input.Scope.Branch, 512) || input.CollectedAt.IsZero() ||
		(input.SourceScanAt != nil && input.SourceScanAt.IsZero()) {
		return false
	}
	if input.SourceStatus != "succeeded" && input.SourceStatus != "failed" {
		return false
	}
	if input.ScanKind != "full" && input.ScanKind != "delta" {
		return false
	}
	return input.Completeness == "complete" || input.Completeness == "partial" || input.Completeness == "unknown"
}

// Collection time is deliberately excluded from replay identity. Scan meaning,
// parser mapping, asset, and source time cannot change for an existing run.
func metadataDigest(input importInput) string {
	input.Report = ""
	input.CollectedAt = time.Time{}
	data, _ := json.Marshal(input)
	return reportDigest(data)
}

func runIdentity(workspace string, input importInput) string {
	data, _ := json.Marshal([]string{workspace, input.SourceID, input.ScanID})
	return reportDigest(data)
}

func (a *Application) replayAllowed(record importRecord, input importInput, digest, metadata string) error {
	if record.AssetID != input.AssetID || record.Scope != input.Scope ||
		record.ReportDigest != digest || record.MetadataDigest != metadata {
		return errConflict
	}
	if !a.config.Now().Before(record.ImportedAt.Add(importReplayWindow)) {
		return errReplayExpired
	}
	return nil
}

func (a *Application) sourceScan(ctx context.Context, db queryRower, workspace string, input importInput) (importRecord, error) {
	return scanImport(db.QueryRow(ctx, `SELECT `+importColumns("")+` FROM `+a.table("imports")+`
		WHERE workspace_id=$1 AND source_id=$2 AND scan_id=$3`, workspace, input.SourceID, input.ScanID))
}

func importReply(w http.ResponseWriter, record Import) {
	status := http.StatusAccepted
	if record.State == "succeeded" || record.State == "failed" {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{"import": record})
}

func (a *Application) upload(w http.ResponseWriter, r *http.Request, workspace string, session authenticatedSession) error {
	var input importInput
	if err := a.decode(w, r, &input, a.config.MaxUploadBytes); err != nil {
		return err
	}
	if !validImport(input) {
		return errInvalid
	}
	if !parsers.Supported(input.Format) {
		return errUnsupported
	}
	if parsers.ValidateMapping(input.Format, input.Mapping) != nil {
		return errInvalid
	}
	input.CollectedAt = input.CollectedAt.UTC()
	if input.SourceScanAt != nil {
		scanned := input.SourceScanAt.UTC()
		input.SourceScanAt = &scanned
	}
	digest, metadata := reportDigest([]byte(input.Report)), metadataDigest(input)
	check := func(tx pgx.Tx) (importRecord, error) {
		if err := a.authorizeIntake(r.Context(), tx, session, workspace, input.AssetID); err != nil {
			return importRecord{}, err
		}
		existing, err := a.sourceScan(r.Context(), tx, workspace, input)
		if err == nil {
			err = a.replayAllowed(existing, input, digest, metadata)
		}
		return existing, err
	}
	preflight, err := a.pool.Begin(r.Context())
	if err != nil {
		return err
	}
	existing, checkErr := check(preflight)
	if checkErr != nil && !errors.Is(checkErr, pgx.ErrNoRows) {
		rollback(preflight)
		return checkErr
	}
	if err = preflight.Commit(r.Context()); err != nil {
		rollback(preflight)
		return err
	}
	if checkErr == nil {
		importReply(w, existing.Import)
		return nil
	}
	record := importRecord{WorkspaceID: workspace, SubmittedBy: session.User.ID, MetadataDigest: metadata, Mapping: input.Mapping, Import: Import{
		ID: newID(), RunID: newID(), State: "queued", AssetID: input.AssetID,
		Format: input.Format, SourceID: input.SourceID, ScanID: input.ScanID, Scope: input.Scope,
		SourceScanAt: input.SourceScanAt, CollectedAt: input.CollectedAt,
		SourceStatus: input.SourceStatus, ScanKind: input.ScanKind, Completeness: input.Completeness, ReportDigest: digest,
	}}
	record.ReportSize = int64(len(input.Report))
	// No SQL connection, transaction or lock is retained during shared S3 I/O.
	record.ReportKey, err = a.storage.put(r.Context(), workspace, record.ID, []byte(input.Report))
	if err != nil {
		return err
	}
	tx, err := a.pool.Begin(r.Context())
	if err != nil {
		return err
	}
	defer rollback(tx)
	if _, err = tx.Exec(r.Context(), `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`,
		"aspm/app/intake/"+a.config.Schema+"/"+runIdentity(workspace, input)); err != nil {
		return err
	}
	existing, err = check(tx)
	if err == nil {
		if err = tx.Commit(r.Context()); err != nil {
			return err
		}
		importReply(w, existing.Import)
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	record.ImportedAt = a.config.Now().UTC().Truncate(time.Microsecond)
	mapping, err := json.Marshal(input.Mapping)
	if err != nil {
		return errInvalid
	}
	// A failed/conflicting finalization may leave an unreferenced S3 object,
	// never an acknowledged queue entry whose original evidence is missing.
	if _, err = tx.Exec(r.Context(), `INSERT INTO `+a.table("imports")+`
		(id,run_id,workspace_id,asset_id,source_id,scan_id,scope_id,scope_revision,scope_branch,format,mapping,
		source_scan_at,collected_at,imported_at,source_status,scan_kind,completeness,report_digest,report_key,report_size,metadata_digest,submitted_by)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22)`,
		record.ID, record.RunID, workspace, record.AssetID, record.SourceID, record.ScanID,
		record.Scope.ID, record.Scope.Revision, record.Scope.Branch, record.Format, mapping,
		record.SourceScanAt, record.CollectedAt, record.ImportedAt, record.SourceStatus, record.ScanKind,
		record.Completeness, record.ReportDigest, record.ReportKey, record.ReportSize, record.MetadataDigest, record.SubmittedBy); err != nil {
		return err
	}
	if err = a.authorizeIntake(r.Context(), tx, session, workspace, input.AssetID); err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	importReply(w, record.Import)
	return nil
}

func importColumns(alias string) string {
	names := []string{"id", "run_id", "state", "workspace_id", "asset_id", "format", "source_id", "scan_id",
		"scope_id", "scope_revision", "scope_branch", "source_scan_at", "collected_at", "imported_at",
		"source_status", "scan_kind", "completeness", "report_digest", "observation_count",
		"report_key", "report_size", "metadata_digest", "mapping", "failure_code", "failure_message", "fence", "worker_id", "submitted_by"}
	if alias != "" {
		for i := range names {
			names[i] = alias + "." + names[i]
		}
	}
	return strings.Join(names, ",")
}

func scanImport(row pgx.Row) (importRecord, error) {
	var record importRecord
	var mapping []byte
	var failureCode, failureMessage, workerID, submittedBy *string
	err := row.Scan(&record.ID, &record.RunID, &record.State, &record.WorkspaceID, &record.AssetID,
		&record.Format, &record.SourceID, &record.ScanID, &record.Scope.ID, &record.Scope.Revision, &record.Scope.Branch,
		&record.SourceScanAt, &record.CollectedAt, &record.ImportedAt, &record.SourceStatus, &record.ScanKind,
		&record.Completeness, &record.ReportDigest, &record.ObservationCount, &record.ReportKey, &record.ReportSize,
		&record.MetadataDigest, &mapping, &failureCode, &failureMessage, &record.Fence, &workerID, &submittedBy)
	if err != nil {
		return record, err
	}
	if err = json.Unmarshal(mapping, &record.Mapping); err != nil {
		return record, err
	}
	if workerID != nil {
		record.WorkerID = *workerID
	}
	if submittedBy != nil {
		record.SubmittedBy = *submittedBy
	}
	if failureCode != nil {
		record.Failure = &Failure{Code: *failureCode}
		if failureMessage != nil {
			record.Failure.Message = *failureMessage
		}
	}
	return record, nil
}

func (a *Application) importResource(w http.ResponseWriter, r *http.Request, workspace, id string, evidence bool) error {
	record, err := scanImport(a.pool.QueryRow(r.Context(), `SELECT `+importColumns("")+` FROM `+a.table("imports")+` WHERE workspace_id=$1 AND id=$2`, workspace, id))
	if err != nil {
		return err
	}
	if !evidence {
		writeJSON(w, 200, map[string]any{"import": record.Import})
		return nil
	}
	data, err := a.storage.read(r.Context(), record)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="report.txt"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
	return nil
}
