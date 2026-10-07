package app

import (
	"errors"
	"net/http"
	"strconv"

	"github.com/bahadrdsr/aspm/internal/evidence"
)

func evidenceAvailabilityError(err error) (string, error) {
	switch {
	case errors.Is(err, evidence.ErrNotFound):
		return "missing", errEvidenceMissing
	case errors.Is(err, evidence.ErrIntegrity):
		return "corrupt", errEvidenceCorrupt
	default:
		return "", err
	}
}

func writeEvidenceBytes(w http.ResponseWriter, contentType, filename string, data []byte) {
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
	w.Header().Set("Content-Length", strconv.Itoa(len(data)))
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(data)
}

func (a *Application) observationEvidence(w http.ResponseWriter, r *http.Request,
	workspace, id string) error {
	var data []byte
	var availability, transition string
	var archiveKey, archiveDigest *string
	var archiveSize *int64
	var revision int64
	err := a.pool.QueryRow(r.Context(), `SELECT COALESCE(data,summary),evidence_availability,
		archive_key,archive_digest,archive_size,evidence_revision,COALESCE(retention_transition,'')
		FROM `+a.table("observations")+` WHERE workspace_id=$1 AND id=$2`,
		workspace, id).Scan(&data, &availability, &archiveKey, &archiveDigest, &archiveSize, &revision, &transition)
	if err != nil {
		return err
	}
	if availability == "expired" {
		return errEvidenceExpired
	}
	if transition != "" {
		return errUnavailable
	}
	if availability == "available" {
		writeEvidenceBytes(w, "application/json", "observation.json", data)
		return nil
	}
	if a.archiveEvidence == nil || archiveKey == nil || archiveDigest == nil || archiveSize == nil {
		return errUnavailable
	}
	data, err = a.archiveEvidence.readExact(r.Context(), workspace, evidence.Ref{
		WorkspaceID: workspace, Bucket: a.archiveEvidence.config.Bucket, Key: *archiveKey,
		SHA256: *archiveDigest, SizeBytes: *archiveSize,
	})
	if err != nil {
		state, problem := evidenceAvailabilityError(err)
		if state != "" {
			_, updateErr := a.pool.Exec(r.Context(), `UPDATE `+a.table("observations")+`
				SET evidence_availability=$4,evidence_revision=evidence_revision+1
				WHERE workspace_id=$1 AND id=$2 AND evidence_revision=$3
				AND evidence_availability IN ('archived','missing','corrupt')`,
				workspace, id, revision, state)
			if updateErr != nil {
				return updateErr
			}
			return problem
		}
		return err
	}
	if availability != "archived" {
		if _, err = a.pool.Exec(r.Context(), `UPDATE `+a.table("observations")+`
			SET evidence_availability='archived',evidence_revision=evidence_revision+1
			WHERE workspace_id=$1 AND id=$2 AND evidence_revision=$3
			AND evidence_availability IN ('missing','corrupt')`, workspace, id, revision); err != nil {
			return err
		}
	}
	writeEvidenceBytes(w, "application/json", "observation.json", data)
	return nil
}
