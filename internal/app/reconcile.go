package app

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bahadrdsr/aspm/internal/parsers"
	"github.com/jackc/pgx/v5"
)

func (r importRecord) comparable() bool {
	return r.SourceStatus == "succeeded" && r.ScanKind == "full" &&
		r.Completeness == "complete" && r.SourceScanAt != nil
}

func (r importRecord) scopeArgs() []any {
	return []any{r.WorkspaceID, r.AssetID, r.SourceID, r.Scope.ID, r.Scope.Revision, r.Scope.Branch}
}

const sameScope = `workspace_id=$1 AND asset_id=$2 AND source_id=$3 AND scope_id=$4 AND scope_revision=$5 AND scope_branch=$6`

type reconciledFinding struct {
	ID, ChangeKind, ChangeRunID, ContentDigest string
	ChangeAt                                   *time.Time
	ChangeRevision                             int64
	SourceScanAt                               *time.Time
}

func findingContentDigest(finding parsers.Finding) string {
	data, _ := json.Marshal(struct {
		SourceFindingID, Title, Description, SourceSeverity, Severity string
		Impact, Remediation, EvidenceText, SourceLabel                string
		Location                                                      parsers.Location
		Unmapped                                                      map[string]any
	}{
		finding.SourceFindingID, finding.Title, finding.Description, finding.SourceSeverity, finding.Severity,
		finding.Impact, finding.Remediation, finding.EvidenceText, finding.SourceLabel, finding.Location, finding.Unmapped,
	})
	return reportDigest(data)
}

func boundedNotificationSnapshot(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	end := limit
	for end > 0 && !utf8.ValidString(value[:end]) {
		end--
	}
	return value[:end]
}

func observationChange(record importRecord, digest string, result reconciledFinding) (string, []string) {
	reasons := []string{}
	if record.SourceStatus != "succeeded" {
		reasons = append(reasons, "source-status-failed")
	} else if result.ChangeRunID == record.RunID {
		// This observation became the current source projection.
	} else if record.SourceScanAt != nil && result.SourceScanAt != nil &&
		record.SourceScanAt.Before(*result.SourceScanAt) {
		reasons = append(reasons, "out-of-order")
	} else if digest != result.ContentDigest {
		reasons = append(reasons, "out-of-order")
	}

	if record.Completeness == "partial" {
		reasons = append(reasons, "partial-scan")
	} else if record.Completeness == "unknown" {
		reasons = append(reasons, "unknown-completeness")
	}
	if record.ScanKind == "delta" {
		reasons = append(reasons, "delta-scan")
	}
	if record.SourceScanAt == nil {
		reasons = append(reasons, "unknown-source-time")
	}
	switch {
	case record.SourceStatus != "succeeded":
		return "non-authoritative", reasons
	case slices.Contains(reasons, "out-of-order"):
		return "historical", reasons
	case result.ChangeRunID == record.RunID:
		return result.ChangeKind, reasons
	default:
		return "unchanged", reasons
	}
}

func (a *ImportWorker) reconcile(ctx context.Context, record importRecord, findings []parsers.Finding) error {
	tx, err := a.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	// Lock actor authorization before the job row, matching role-revocation
	// ordering. A committed role change cannot be crossed by finalization.
	if err = a.authorizeActor(ctx, tx, record, true); err != nil {
		return err
	}
	scope, _ := json.Marshal(record.scopeArgs())
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`,
		"aspm/app/reconcile/"+a.schema+"/"+reportDigest(scope)); err != nil {
		return err
	}
	var active bool
	if err = tx.QueryRow(ctx, `SELECT state='processing' AND worker_id=$2 AND fence=$3
		AND lease_until>clock_timestamp() FROM `+a.table("imports")+` WHERE id=$1 FOR UPDATE`,
		record.ID, record.WorkerID, record.Fence).Scan(&active); err != nil {
		return err
	}
	if !active {
		return errLeaseLost
	}
	if err = a.authorizeImport(ctx, tx, record); err != nil {
		return err
	}
	var owner *string
	var assetName string
	var policyEpoch int64
	if err = tx.QueryRow(ctx, `SELECT a.owner_id,a.name,w.notification_policy_epoch
		FROM `+a.table("assets")+` a JOIN `+a.table("workspaces")+` w ON w.id=a.workspace_id
		WHERE a.workspace_id=$1 AND a.id=$2`,
		record.WorkspaceID, record.AssetID).Scan(&owner, &assetName, &policyEpoch); err != nil {
		return err
	}
	var coverage *time.Time
	if err = tx.QueryRow(ctx, `SELECT max(complete_at) FROM `+a.table("coverage")+` WHERE `+sameScope,
		record.scopeArgs()...).Scan(&coverage); err != nil {
		return err
	}
	upsert := a.findingUpsert()
	// Two bounded batches per slice: canonical identities first, then immutable
	// observations. All slices and the terminal receipt share this transaction.
	for start := 0; start < len(findings); start += 100 {
		end := min(start+100, len(findings))
		batch := &pgx.Batch{}
		for _, f := range findings[start:end] {
			state := "observed"
			if record.SourceStatus != "succeeded" {
				state = "unknown"
			}
			var freshness *time.Time
			if record.comparable() {
				freshness = record.SourceScanAt
			}
			covered := record.SourceStatus == "succeeded" && record.SourceScanAt != nil &&
				coverage != nil && coverage.After(*record.SourceScanAt)
			if covered {
				// This freshness belongs to an already committed authoritative
				// scan, not to the late partial/delta observation.
				state, freshness = "inferred-resolved", coverage
			}
			candidateURI, candidateLine := "", 0
			if record.SourceStatus == "succeeded" {
				candidateURI, candidateLine = correlationCandidateLocation(f.Location)
			}
			contentDigest := findingContentDigest(f)
			batch.Queue(upsert, newID(), record.WorkspaceID, record.AssetID, record.SourceID,
				record.Scope.ID, record.Scope.Revision, record.Scope.Branch, f.Identity,
				f.Title, f.Description, f.Remediation, f.Severity, f.EvidenceText, f.SourceLabel,
				record.SourceScanAt, record.CollectedAt, record.ImportedAt, freshness, state, owner,
				candidateURI, candidateLine, contentDigest, record.RunID,
				record.SourceStatus == "succeeded", record.comparable() || covered)
		}
		result := tx.SendBatch(ctx, batch)
		reconciled := make([]reconciledFinding, end-start)
		for i := range reconciled {
			if err = result.QueryRow().Scan(&reconciled[i].ID, &reconciled[i].ChangeKind,
				&reconciled[i].ChangeAt, &reconciled[i].ChangeRevision,
				&reconciled[i].SourceScanAt, &reconciled[i].ChangeRunID,
				&reconciled[i].ContentDigest); err != nil {
				_ = result.Close()
				return err
			}
		}
		if err = result.Close(); err != nil {
			return err
		}
		batch = &pgx.Batch{}
		for offset, f := range findings[start:end] {
			changeKind, changeReasons := observationChange(record, findingContentDigest(f), reconciled[offset])
			observation := Observation{
				ID: newID(), RunID: record.RunID, SourceID: record.SourceID, ScanID: record.ScanID,
				Scope: record.Scope, SourceScanAt: record.SourceScanAt, SourceFindingID: f.SourceFindingID,
				SourceSeverity: f.SourceSeverity, NormalizedSeverity: f.Severity, SourceLocation: f.Location,
				Impact: f.Impact, Remediation: f.Remediation, Unmapped: f.Unmapped, EvidenceDigest: record.ReportDigest,
				ChangeKind: changeKind, ChangeReasons: changeReasons,
			}
			data, err := json.Marshal(observation)
			if err != nil {
				return err
			}
			batch.Queue(`INSERT INTO `+a.table("observations")+`(id,workspace_id,finding_id,run_id,ordinal,data)
				VALUES($1,$2,$3,$4,$5,$6)`, observation.ID, record.WorkspaceID,
				reconciled[offset].ID, record.RunID, start+offset, data)
			change := reconciled[offset]
			if policyEpoch > 0 && record.comparable() && change.ChangeRunID == record.RunID &&
				slices.Contains(notificationChangeKinds, change.ChangeKind) && change.ChangeAt != nil {
				batch.Queue(`INSERT INTO `+a.table("finding_change_events")+`
					(id,workspace_id,finding_id,policy_epoch,title,severity,asset_name,change_kind,
					 change_revision,change_at)
					VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
					ON CONFLICT(workspace_id,finding_id,change_revision) DO NOTHING`,
					newID(), record.WorkspaceID, change.ID, policyEpoch,
					boundedNotificationSnapshot(f.Title, 1024), f.Severity,
					boundedNotificationSnapshot(assetName, 256), change.ChangeKind,
					change.ChangeRevision, *change.ChangeAt)
			}
		}
		if err = tx.SendBatch(ctx, batch).Close(); err != nil {
			return err
		}
	}
	if record.comparable() && (coverage == nil || record.SourceScanAt.After(*coverage)) {
		args := append(record.scopeArgs(), record.SourceScanAt, record.RunID, record.ImportedAt)
		// Known, strictly older positive observations are required. Human state
		// and independent verification are never altered by source absence.
		if _, err = tx.Exec(ctx, `UPDATE `+a.table("findings")+` f
			SET source_state='inferred-resolved',source_freshness_at=$7,evidence_revision=evidence_revision+1,
				change_kind='inferred-resolved',change_at=$9,change_run_id=$8,change_revision=change_revision+1
			WHERE `+sameScope+` AND source_scan_at IS NOT NULL AND source_scan_at<$7
			AND source_state<>'unknown' AND (source_freshness_at IS NULL OR source_freshness_at<$7)
			AND NOT EXISTS(SELECT 1 FROM `+a.table("observations")+` o WHERE o.finding_id=f.id AND o.run_id=$8)`, args...); err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO `+a.table("coverage")+`
			(workspace_id,asset_id,source_id,scope_id,scope_revision,scope_branch,complete_at)
			VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(workspace_id,asset_id,source_id,scope_id,scope_revision,scope_branch)
			DO UPDATE SET complete_at=EXCLUDED.complete_at`, args[:7]...); err != nil {
			return err
		}
	}
	if err = a.authorizeImport(ctx, tx, record); err != nil {
		return err
	}
	result, err := tx.Exec(ctx, `UPDATE `+a.table("imports")+` SET state='succeeded',observation_count=$4,
		failure_code=NULL,failure_message=NULL,lease_until=NULL,worker_id=NULL
		WHERE id=$1 AND worker_id=$2 AND fence=$3 AND state='processing' AND lease_until>clock_timestamp()`,
		record.ID, record.WorkerID, record.Fence, len(findings))
	if err != nil {
		return err
	}
	if result.RowsAffected() != 1 {
		return errLeaseLost
	}
	return tx.Commit(ctx)
}

func (a *ImportWorker) findingUpsert() string {
	newer := `($25 AND ((EXCLUDED.source_scan_at IS NOT NULL
		AND (f.source_scan_at IS NULL OR EXCLUDED.source_scan_at>f.source_scan_at)
		AND (f.source_freshness_at IS NULL OR EXCLUDED.source_scan_at>=f.source_freshness_at))
		OR (EXCLUDED.source_scan_at IS NULL AND f.source_scan_at IS NULL AND f.source_freshness_at IS NULL)))`
	var updates []string
	for _, field := range []string{"title", "description", "remediation", "severity", "evidence_text", "source_label",
		"source_scan_at", "collected_at", "imported_at", "source_state"} {
		updates = append(updates, field+"=CASE WHEN "+newer+" THEN EXCLUDED."+field+" ELSE f."+field+" END")
	}
	candidateNewer := newer + ` OR (f.candidate_uri='' AND $25 AND EXCLUDED.candidate_uri<>'')`
	for _, field := range []string{"candidate_uri", "candidate_line"} {
		updates = append(updates, field+"=CASE WHEN "+candidateNewer+" THEN EXCLUDED."+field+" ELSE f."+field+" END")
	}
	updates = append(updates,
		`content_digest=CASE WHEN `+newer+` THEN EXCLUDED.content_digest ELSE f.content_digest END`,
		`change_kind=CASE WHEN `+newer+` THEN CASE
			WHEN f.source_state='inferred-resolved' THEN 'reopened'
			WHEN f.content_digest='' THEN 'unchanged'
			WHEN f.content_digest<>EXCLUDED.content_digest THEN 'changed'
			ELSE 'unchanged' END ELSE f.change_kind END`,
		`change_at=CASE WHEN `+newer+` THEN EXCLUDED.change_at ELSE f.change_at END`,
		`change_run_id=CASE WHEN `+newer+` THEN EXCLUDED.change_run_id ELSE f.change_run_id END`,
		`change_revision=CASE WHEN `+newer+` THEN f.change_revision+1 ELSE f.change_revision END`)
	updates = append(updates, `source_freshness_at=CASE WHEN $26 AND EXCLUDED.source_freshness_at IS NOT NULL
		AND (f.source_freshness_at IS NULL OR EXCLUDED.source_freshness_at>f.source_freshness_at)
		THEN EXCLUDED.source_freshness_at ELSE f.source_freshness_at END`)
	updates = append(updates, `evidence_revision=f.evidence_revision+1`)
	return `INSERT INTO ` + a.table("findings") + ` AS f
		(id,workspace_id,asset_id,source_id,scope_id,scope_revision,scope_branch,identity_key,title,description,
		remediation,severity,evidence_text,source_label,source_scan_at,collected_at,imported_at,source_freshness_at,source_state,owner_id,
		candidate_uri,candidate_line,content_digest,change_kind,change_at,change_run_id)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,
			CASE WHEN $25 THEN 'new' ELSE 'unchanged' END,
			CASE WHEN $25 THEN $17::timestamptz ELSE NULL::timestamptz END,
			CASE WHEN $25 THEN $24 ELSE '' END)
		ON CONFLICT(workspace_id,asset_id,source_id,scope_id,scope_revision,scope_branch,identity_key)
		DO UPDATE SET ` + strings.Join(updates, ",") + `
		RETURNING id,change_kind,change_at,change_revision,source_scan_at,change_run_id,content_digest`
}

func correlationCandidateLocation(location parsers.Location) (string, int) {
	uri := strings.TrimSpace(location.URI)
	if uri == "" || location.Line <= 0 {
		return "", 0
	}
	return uri, location.Line
}
