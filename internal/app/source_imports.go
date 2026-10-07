package app

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bahadrdsr/aspm/internal/connectors"
	"github.com/bahadrdsr/aspm/internal/parsers"
	"github.com/jackc/pgx/v5"
)

type sourceImportAuthority struct {
	workspace, sourceID, collectionID, recordID, assetID string
	session                                              authenticatedSession
	revision                                             int64
	target                                               AzureDevOpsTarget
	selection                                            AzureDevOpsSelection
}

func (a *Application) checkSourceImportAuthority(ctx context.Context, tx pgx.Tx, authority sourceImportAuthority) error {
	if err := a.authorizeIntake(ctx, tx, authority.session, authority.workspace, authority.assetID); err != nil {
		return err
	}
	source, err := scanSourceConnection(tx.QueryRow(ctx, `SELECT `+sourceConnectionColumns+
		` FROM `+a.table("source_connections")+` WHERE workspace_id=$1 AND id=$2 FOR SHARE`,
		authority.workspace, authority.sourceID))
	if err != nil {
		return err
	}
	if !source.Enabled || source.Profile != connectors.ADOArtifacts || source.Revision != authority.revision ||
		source.AzureDevOps == nil || *source.AzureDevOps != authority.target {
		return errConflict
	}
	target, err := json.Marshal(authority.target)
	if err != nil {
		return err
	}
	selection, err := json.Marshal(authority.selection)
	if err != nil {
		return err
	}
	var valid bool
	err = tx.QueryRow(ctx, `SELECT state IN ('succeeded','partial') AND asset_id=$5 AND
		connection_revision=$6 AND azure_devops_target=$7::jsonb AND azure_devops_selection=$8::jsonb
		FROM `+a.table("source_collections")+`
		WHERE workspace_id=$1 AND id=$2 AND source_id=$3 AND profile=$4`,
		authority.workspace, authority.collectionID, authority.sourceID, connectors.ADOArtifacts,
		authority.assetID, authority.revision, string(target), string(selection)).Scan(&valid)
	if errors.Is(err, pgx.ErrNoRows) {
		return errNotFound
	}
	if err != nil {
		return err
	}
	if !valid {
		return errConflict
	}
	var kind string
	err = tx.QueryRow(ctx, `SELECT kind FROM `+a.table("source_collection_records")+`
		WHERE workspace_id=$1 AND collection_id=$2 AND id=$3`,
		authority.workspace, authority.collectionID, authority.recordID).Scan(&kind)
	if errors.Is(err, pgx.ErrNoRows) {
		return errNotFound
	}
	if err != nil {
		return err
	}
	if kind != "report" {
		return errInvalid
	}
	return nil
}

func (a *Application) sourceImportContext(ctx context.Context, workspace, collectionID, recordID string,
	session authenticatedSession) (sourceImportAuthority, sourceEvidenceRecord, time.Time, *time.Time, error) {
	var authority sourceImportAuthority
	var evidence sourceEvidenceRecord
	tx, err := a.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return authority, evidence, time.Time{}, nil, err
	}
	defer rollback(tx)
	collection, err := scanSourceCollection(tx.QueryRow(ctx, `SELECT `+sourceCollectionColumns+`
		FROM `+a.table("source_collections")+` WHERE workspace_id=$1 AND id=$2`,
		workspace, collectionID))
	if err != nil {
		return authority, evidence, time.Time{}, nil, err
	}
	if collection.Profile != connectors.ADOArtifacts {
		return authority, evidence, time.Time{}, nil, errNotFound
	}
	if collection.State != "succeeded" && collection.State != "partial" ||
		collection.AssetID == nil || collection.CollectedAt == nil ||
		collection.AzureDevOps == nil || collection.Selection == nil {
		return authority, evidence, time.Time{}, nil, errConflict
	}
	target, validTarget := normalizeAzureDevOpsTarget(*collection.AzureDevOps)
	if !validTarget || target != *collection.AzureDevOps || !validAzureDevOpsSelection(*collection.Selection) {
		return authority, evidence, time.Time{}, nil, errConflict
	}
	evidence, err = scanSourceEvidence(tx.QueryRow(ctx, `SELECT `+sourceEvidenceColumns+`
		FROM `+a.table("source_collection_records")+`
		WHERE workspace_id=$1 AND collection_id=$2 AND id=$3`, workspace, collectionID, recordID))
	if err != nil {
		return authority, evidence, time.Time{}, nil, err
	}
	if evidence.Kind != "report" {
		return authority, evidence, time.Time{}, nil, errInvalid
	}
	artifactID, path, found := strings.Cut(evidence.ExternalID, ":")
	if !found || !sourceAzureDevOpsBuild.MatchString(artifactID) || path != collection.Selection.ArtifactPath ||
		!strings.EqualFold(evidence.ParentID, target.RepositoryID) ||
		evidence.NativeRunID != collection.Selection.BuildID {
		return authority, evidence, time.Time{}, nil, errConflict
	}
	authority = sourceImportAuthority{
		workspace: workspace, sourceID: collection.SourceID, collectionID: collectionID, recordID: recordID,
		assetID: *collection.AssetID, session: session, revision: collection.ConnectionRevision,
		target: target, selection: *collection.Selection,
	}
	if err = a.checkSourceImportAuthority(ctx, tx, authority); err != nil {
		return authority, evidence, time.Time{}, nil, err
	}
	if err = tx.Commit(ctx); err != nil {
		return authority, evidence, time.Time{}, nil, err
	}
	return authority, evidence, *collection.CollectedAt, evidence.SourceScanAt, nil
}

func (a *Application) watchSourceImport(ctx context.Context, cancel context.CancelCauseFunc,
	authority sourceImportAuthority) func() {
	watch, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-watch.Done():
				return
			case <-ticker.C:
				tx, err := a.pool.Begin(watch)
				if err == nil {
					err = a.checkSourceImportAuthority(watch, tx, authority)
					if err == nil {
						err = tx.Commit(watch)
					} else {
						rollback(tx)
					}
				}
				if err != nil {
					if watch.Err() == nil {
						cancel(err)
					}
					return
				}
			}
		}
	}()
	return func() {
		stop()
		<-done
	}
}

func (a *Application) importSourceRecord(w http.ResponseWriter, r *http.Request, workspace, collectionID, recordID string,
	session authenticatedSession) error {
	var input struct {
		APIVersion   string          `json:"apiVersion"`
		Format       string          `json:"format"`
		Scope        Scope           `json:"scope"`
		SourceStatus string          `json:"sourceStatus"`
		ScanKind     string          `json:"scanKind"`
		Completeness string          `json:"completeness"`
		Mapping      parsers.Mapping `json:"mapping"`
	}
	if err := a.decode(w, r, &input, 16<<10); err != nil {
		return err
	}
	if input.APIVersion != APIVersion || !validText(input.Scope.ID, 512) ||
		!validText(input.Scope.Revision, 512) || !validText(input.Scope.Branch, 512) ||
		(input.SourceStatus != "succeeded" && input.SourceStatus != "failed") ||
		(input.ScanKind != "full" && input.ScanKind != "delta") ||
		(input.Completeness != "complete" && input.Completeness != "partial" && input.Completeness != "unknown") {
		return errInvalid
	}
	if input.Format == "" {
		return errInvalid
	}
	if input.Format != "sarif" {
		return errUnsupported
	}
	if parsers.ValidateMapping(input.Format, input.Mapping) != nil {
		return errInvalid
	}
	authority, evidence, collectedAt, sourceScanAt, err := a.sourceImportContext(
		r.Context(), workspace, collectionID, recordID, session)
	if err != nil {
		return err
	}
	if a.collectionEvidence == nil {
		return errUnavailable
	}
	access, cancel := context.WithCancelCause(r.Context())
	defer cancel(context.Canceled)
	stopWatch := a.watchSourceImport(access, cancel, authority)
	defer stopWatch()
	report, err := a.collectionEvidence.read(access, workspace, evidence.ref)
	if cause := context.Cause(access); cause != nil {
		return cause
	}
	if err != nil {
		return errUnavailable
	}
	if !utf8.Valid(report) {
		return errInvalid
	}
	if int64(len(report)) > a.config.MaxUploadBytes {
		return errTooLarge
	}
	sourceID, scanID := azureDevOpsImportIdentities(authority.target, authority.selection)
	value := importInput{
		APIVersion: input.APIVersion, AssetID: authority.assetID, Format: input.Format,
		Report: string(report), SourceID: sourceID, ScanID: scanID, Scope: input.Scope,
		SourceScanAt: sourceScanAt, CollectedAt: collectedAt, SourceStatus: input.SourceStatus,
		ScanKind: input.ScanKind, Completeness: input.Completeness, Mapping: input.Mapping,
	}
	additional := func(ctx context.Context, tx pgx.Tx) error {
		return a.checkSourceImportAuthority(ctx, tx, authority)
	}
	err = a.acceptImport(access, w, workspace, session, value, report, true, additional)
	if cause := context.Cause(access); cause != nil {
		return cause
	}
	return err
}
