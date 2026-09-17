package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/bahadrdsr/aspm/internal/connectors"
	"github.com/bahadrdsr/aspm/internal/evidence"
	"github.com/jackc/pgx/v5"
)

type sourceRepository struct {
	ID       json.Number `json:"id"`
	FullName string      `json:"full_name"`
}

func validateSourceResult(job sourceCollectionRecord, result connectors.Collection, limits connectors.Limits) (sourceRepository, error) {
	var repository sourceRepository
	if result.Identity.WorkspaceID != job.WorkspaceID || result.Identity.SourceID != job.SourceID ||
		result.Identity.RunID != job.ID || result.Identity.ScopeID != job.Repository || result.CollectedAt.IsZero() ||
		len(result.Records) == 0 || len(result.Records) > 1+limits.Pages*limits.PageSize || len(result.Gaps) > 64 {
		return repository, connectors.ErrProtocol
	}
	var total int64
	seen := make(map[string]bool, len(result.Records))
	for index, record := range result.Records {
		total += int64(len(record.Raw))
		if len(record.Raw) == 0 || int64(len(record.Raw)) > limits.Bytes || total > sourceEvidenceLimit ||
			len(record.RawURL) > 16384 || len(record.Location) > 4096 || len(record.State) > 256 || len(record.Severity) > 256 {
			return repository, connectors.ErrLimit
		}
		identity := record.Kind + ":" + record.ExternalID
		if seen[identity] || !sourceRepositoryID.MatchString(record.ExternalID) || record.RawURL == "" ||
			record.NativeRunID != "" || record.SourceScanAt != nil {
			return repository, connectors.ErrProtocol
		}
		seen[identity] = true
		if index == 0 {
			if record.Kind != "repository" || json.Unmarshal(record.Raw, &repository) != nil ||
				repository.ID.String() != record.ExternalID || !validSourceRepository(repository.FullName) ||
				!strings.EqualFold(repository.FullName, job.Repository) {
				return repository, connectors.ErrScope
			}
		} else if record.Kind != "finding" || record.ParentID != repository.ID.String() {
			return repository, connectors.ErrScope
		}
	}
	for _, gap := range result.Gaps {
		if len(gap) > 256 {
			return repository, connectors.ErrLimit
		}
	}
	return repository, nil
}

func sourceNativeFailure(cause error, token string) *FindingDeliveryFailure {
	failure := &FindingDeliveryFailure{Code: "protocol"}
	var native *connectors.Error
	if errors.As(cause, &native) {
		failure.Code, failure.NativeCode, failure.HTTPStatus = native.Code, native.NativeCode, native.HTTPStatus
		failure.RetryAfterSeconds = max(0, int64(native.RetryAfter.Seconds()))
		if token != "" && strings.Contains(failure.NativeCode, token) {
			failure.NativeCode = ""
		}
		return failure
	}
	switch {
	case errors.Is(cause, connectors.ErrLimit):
		failure.Code = "limit"
	case errors.Is(cause, connectors.ErrScope):
		failure.Code = "scope"
	case errors.Is(cause, connectors.ErrAuth):
		failure.Code = "auth"
	}
	return failure
}

func (w *CollectionWorker) publishSourceRecords(ctx context.Context, job sourceCollectionRecord, records []connectors.Record) ([]sourceEvidenceRecord, error) {
	published := make([]sourceEvidenceRecord, 0, len(records))
	for ordinal, record := range records {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		id := newID()
		key, err := w.store.put(ctx, job.WorkspaceID, id, record.Raw)
		if err != nil {
			return nil, err
		}
		ref := evidence.Ref{WorkspaceID: job.WorkspaceID, Bucket: w.store.config.Bucket, Key: key,
			SHA256: reportDigest(record.Raw), SizeBytes: int64(len(record.Raw))}
		published = append(published, sourceEvidenceRecord{
			SourceCollectionRecord: SourceCollectionRecord{
				ID: id, CollectionID: job.ID, Ordinal: ordinal, Kind: record.Kind,
				ExternalID: record.ExternalID, ParentID: record.ParentID, NativeRunID: record.NativeRunID,
				State: record.State, Severity: record.Severity, Location: record.Location, RawURL: record.RawURL,
				SourceScanAt: record.SourceScanAt, SourceUpdatedAt: record.SourceUpdatedAt,
				Evidence: SourceEvidenceMetadata{SHA256: ref.SHA256, SizeBytes: ref.SizeBytes},
			},
			ref: ref,
		})
	}
	return published, nil
}

func (w *CollectionWorker) finalizeSource(ctx context.Context, job sourceCollectionRecord, result connectors.Collection, repository sourceRepository, records []sourceEvidenceRecord, failure *FindingDeliveryFailure) error {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return deliveryInfrastructureError(ctx, "open source finalization failed")
	}
	defer rollback(tx)
	source, err := w.sourceAuthority(ctx, tx, job, true)
	if err != nil {
		return err
	}
	var id string
	if err = tx.QueryRow(ctx, `SELECT id FROM `+w.table("source_collections")+`
		WHERE workspace_id=$1 AND id=$2 AND worker_id=$3 AND fence=$4
			AND state='collecting' AND lease_until>clock_timestamp() FOR UPDATE`,
		job.WorkspaceID, job.ID, job.workerID, job.fence).Scan(&id); errors.Is(err, pgx.ErrNoRows) {
		return sourceLeaseLost
	} else if err != nil {
		return deliveryInfrastructureError(ctx, "check source finalization lease failed")
	}
	repositoryID := repository.ID.String()
	if source.repositoryID != nil && *source.repositoryID != repositoryID {
		return sourceIdentityConflict
	}
	// Serialize only this stable repository identity, not unrelated workspace assets.
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`,
		"aspm/source-asset/"+job.WorkspaceID+"/"+job.Profile+"/"+repositoryID); err != nil {
		return deliveryInfrastructureError(ctx, "lock source asset identity failed")
	}
	var assetID string
	err = tx.QueryRow(ctx, `SELECT asset_id FROM `+w.table("source_repository_assets")+`
		WHERE workspace_id=$1 AND profile=$2 AND repository_id=$3 FOR SHARE`, job.WorkspaceID, job.Profile, repositoryID).Scan(&assetID)
	if errors.Is(err, pgx.ErrNoRows) {
		assetID = newID()
		if _, err = tx.Exec(ctx, `INSERT INTO `+w.table("assets")+`
			(id,workspace_id,name,kind,environment,criticality,tags,owner_id,created_at)
			VALUES($1,$2,$3,'repository','','medium','{}',NULL,clock_timestamp())`,
			assetID, job.WorkspaceID, repository.FullName); err != nil {
			return deliveryInfrastructureError(ctx, "insert collected repository asset failed")
		}
		if _, err = tx.Exec(ctx, `INSERT INTO `+w.table("source_repository_assets")+`
			(workspace_id,profile,repository_id,asset_id) VALUES($1,$2,$3,$4)`,
			job.WorkspaceID, job.Profile, repositoryID, assetID); err != nil {
			return deliveryInfrastructureError(ctx, "link collected repository asset failed")
		}
	} else if err != nil {
		return deliveryInfrastructureError(ctx, "read stable source asset identity failed")
	}
	if _, err = tx.Exec(ctx, `UPDATE `+w.table("source_connections")+`
		SET repository_id=$3 WHERE workspace_id=$1 AND id=$2`, job.WorkspaceID, job.SourceID, repositoryID); err != nil {
		return deliveryInfrastructureError(ctx, "pin collected repository identity failed")
	}
	for _, record := range records {
		ref, err := json.Marshal(record.ref)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(ctx, `INSERT INTO `+w.table("source_collection_records")+`
			(id,workspace_id,collection_id,ordinal,kind,external_id,parent_id,native_run_id,state,severity,location,raw_url,
			 source_scan_at,source_updated_at,evidence) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)`,
			record.ID, job.WorkspaceID, job.ID, record.Ordinal, record.Kind, record.ExternalID, record.ParentID,
			record.NativeRunID, record.State, record.Severity, record.Location, record.RawURL,
			record.SourceScanAt, record.SourceUpdatedAt, ref); err != nil {
			return deliveryInfrastructureError(ctx, "insert collected evidence metadata failed")
		}
	}
	state := "succeeded"
	if failure != nil || !result.Complete {
		state = "partial"
	}
	gaps, err := json.Marshal(append([]string{}, result.Gaps...))
	if err != nil {
		return err
	}
	failed, err := json.Marshal(failure)
	if err != nil {
		return err
	}
	// Use the actual PG clock after all inserts, including any blocked SQL callback.
	command, err := tx.Exec(ctx, `UPDATE `+w.table("source_collections")+`
		SET state=$5,complete=$6,asset_id=$7,repository_id=$8,record_count=$9,gaps=$10,
			collected_at=$11,completed_at=clock_timestamp(),failure=$12,lease_until=NULL
		WHERE workspace_id=$1 AND id=$2 AND worker_id=$3 AND fence=$4
			AND state='collecting' AND lease_until>clock_timestamp()`,
		job.WorkspaceID, job.ID, job.workerID, job.fence, state, result.Complete && failure == nil,
		assetID, repositoryID, len(records), gaps, result.CollectedAt, failed)
	if err != nil {
		return deliveryInfrastructureError(ctx, "finalize source collection metadata failed")
	}
	if command.RowsAffected() != 1 {
		return sourceLeaseLost
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return deliveryInfrastructureError(ctx, "commit source collection failed")
	}
	return nil
}
