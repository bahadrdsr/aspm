package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/jackc/pgx/v5"
)

func sourceCollectionBinding(value SourceCollection) ([]byte, error) {
	binding := struct {
		Workspace, Source, Profile, Repository, Actor string
		Revision                                      int64
	}{value.WorkspaceID, value.SourceID, value.Profile, value.Repository, value.RequestedBy, value.ConnectionRevision}
	data, err := json.Marshal(binding)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(data)
	return sum[:], nil
}

func (a *Application) enqueueSourceCollection(w http.ResponseWriter, r *http.Request, workspace string, session authenticatedSession, sourceID string) error {
	var input struct {
		IdempotencyKey string `json:"idempotencyKey"`
	}
	if err := a.decode(w, r, &input, 16<<10); err != nil {
		return err
	}
	if !validText(input.IdempotencyKey, 256) {
		return errInvalid
	}
	if a.collectionEvidence == nil {
		return errUnavailable
	}
	tx, err := a.pool.Begin(r.Context())
	if err != nil {
		return err
	}
	defer rollback(tx)
	role, err := a.sourceSessionRole(r.Context(), tx, session, workspace)
	if err != nil {
		return err
	}
	if !canWrite(Workspace{Role: role}) {
		return errForbidden
	}
	source, err := scanSourceConnection(tx.QueryRow(r.Context(), `SELECT `+sourceConnectionColumns+
		` FROM `+a.table("source_connections")+` WHERE workspace_id=$1 AND id=$2 FOR SHARE`, workspace, sourceID))
	if err != nil {
		return err
	}
	if !source.Enabled {
		return errConflict
	}
	value := SourceCollection{
		ID: newID(), WorkspaceID: workspace, SourceID: sourceID, Profile: source.Profile,
		ConnectionRevision: source.Revision, Repository: source.Repository, RequestedBy: session.User.ID,
		State: "queued", CreatedAt: a.config.Now().UTC(),
	}
	digest, err := sourceCollectionBinding(value)
	if err != nil {
		return err
	}
	record, err := scanSourceCollection(tx.QueryRow(r.Context(), `INSERT INTO `+a.table("source_collections")+`
		(id,workspace_id,source_id,profile,connection_revision,repository,requested_by,idempotency_key,binding_digest,created_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (workspace_id,idempotency_key) DO NOTHING RETURNING `+sourceCollectionColumns,
		value.ID, workspace, sourceID, source.Profile, source.Revision, source.Repository,
		session.User.ID, input.IdempotencyKey, digest, value.CreatedAt))
	status := http.StatusAccepted
	if errors.Is(err, pgx.ErrNoRows) {
		record, err = scanSourceCollection(tx.QueryRow(r.Context(), `SELECT `+sourceCollectionColumns+
			` FROM `+a.table("source_collections")+` WHERE workspace_id=$1 AND idempotency_key=$2`, workspace, input.IdempotencyKey))
		if err != nil {
			return err
		}
		if !bytes.Equal(record.bindingDigest, digest) {
			return errConflict
		}
		status = http.StatusOK
	} else if err != nil {
		return err
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	writeJSON(w, status, map[string]any{"collection": record.SourceCollection})
	return nil
}

func (a *Application) getSourceCollection(w http.ResponseWriter, r *http.Request, workspace, id string) error {
	record, err := scanSourceCollection(a.pool.QueryRow(r.Context(), `SELECT `+sourceCollectionColumns+
		` FROM `+a.table("source_collections")+` WHERE workspace_id=$1 AND id=$2`, workspace, id))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"collection": record.SourceCollection})
	return nil
}

func (a *Application) listSourceCollections(w http.ResponseWriter, r *http.Request, workspace, sourceID string) error {
	limit, cursor, err := pageParameters(r)
	if err != nil {
		return err
	}
	tx, err := a.pool.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer rollback(tx)
	var id string
	if err = tx.QueryRow(r.Context(), `SELECT id FROM `+a.table("source_connections")+` WHERE workspace_id=$1 AND id=$2`,
		workspace, sourceID).Scan(&id); err != nil {
		return err
	}
	var total int64
	if err = tx.QueryRow(r.Context(), `SELECT count(*) FROM `+a.table("source_collections")+`
		WHERE workspace_id=$1 AND source_id=$2`, workspace, sourceID).Scan(&total); err != nil {
		return err
	}
	rows, err := tx.Query(r.Context(), `SELECT `+sourceCollectionColumns+` FROM `+a.table("source_collections")+`
		WHERE workspace_id=$1 AND source_id=$2 AND id>$3 ORDER BY id LIMIT $4`, workspace, sourceID, cursor, limit+1)
	if err != nil {
		return err
	}
	items := []SourceCollection{}
	for rows.Next() {
		record, scanErr := scanSourceCollection(rows)
		if scanErr != nil {
			rows.Close()
			return scanErr
		}
		items = append(items, record.SourceCollection)
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
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "nextCursor": next})
	return nil
}

func (a *Application) listSourceRecords(w http.ResponseWriter, r *http.Request, workspace, collectionID string) error {
	limit, cursor, err := pageParameters(r)
	if err != nil {
		return err
	}
	tx, err := a.pool.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead, AccessMode: pgx.ReadOnly})
	if err != nil {
		return err
	}
	defer rollback(tx)
	var id string
	if err = tx.QueryRow(r.Context(), `SELECT id FROM `+a.table("source_collections")+`
		WHERE workspace_id=$1 AND id=$2`, workspace, collectionID).Scan(&id); err != nil {
		return err
	}
	var total int64
	if err = tx.QueryRow(r.Context(), `SELECT count(*) FROM `+a.table("source_collection_records")+`
		WHERE workspace_id=$1 AND collection_id=$2`, workspace, collectionID).Scan(&total); err != nil {
		return err
	}
	rows, err := tx.Query(r.Context(), `SELECT `+sourceEvidenceColumns+` FROM `+a.table("source_collection_records")+`
		WHERE workspace_id=$1 AND collection_id=$2 AND id>$3 ORDER BY id LIMIT $4`, workspace, collectionID, cursor, limit+1)
	if err != nil {
		return err
	}
	items := []SourceCollectionRecord{}
	for rows.Next() {
		record, scanErr := scanSourceEvidence(rows)
		if scanErr != nil {
			rows.Close()
			return scanErr
		}
		items = append(items, record.SourceCollectionRecord)
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
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": total, "nextCursor": next})
	return nil
}

func (a *Application) sourceRecordEvidence(w http.ResponseWriter, r *http.Request, workspace, collectionID, recordID string) error {
	record, err := scanSourceEvidence(a.pool.QueryRow(r.Context(), `SELECT `+sourceEvidenceColumns+
		` FROM `+a.table("source_collection_records")+` WHERE workspace_id=$1 AND collection_id=$2 AND id=$3`,
		workspace, collectionID, recordID))
	if err != nil {
		return err
	}
	if a.collectionEvidence == nil {
		return errUnavailable
	}
	data, err := a.collectionEvidence.read(r.Context(), workspace, record.ref)
	if err != nil {
		return errUnavailable
	}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
	return nil
}
