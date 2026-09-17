package app

import (
	"encoding/json"
	"time"

	"github.com/bahadrdsr/aspm/internal/evidence"
	"github.com/jackc/pgx/v5"
)

type SourceConnection struct {
	ID                   string    `json:"id"`
	WorkspaceID          string    `json:"workspaceId"`
	Profile              string    `json:"profile"`
	Name                 string    `json:"name"`
	Repository           string    `json:"repository"`
	Enabled              bool      `json:"enabled"`
	CredentialConfigured bool      `json:"credentialConfigured"`
	Revision             int64     `json:"revision"`
	CreatedAt            time.Time `json:"createdAt"`
	UpdatedAt            time.Time `json:"updatedAt"`
}

type sourceConnectionRecord struct {
	SourceConnection
	ciphertext   []byte
	repositoryID *string
}

const sourceConnectionColumns = `id,workspace_id,profile,name,repository,enabled,
	revision,created_at,updated_at,credential_ciphertext,repository_id`

func scanSourceConnection(row pgx.Row) (sourceConnectionRecord, error) {
	var record sourceConnectionRecord
	err := row.Scan(&record.ID, &record.WorkspaceID, &record.Profile, &record.Name, &record.Repository,
		&record.Enabled, &record.Revision, &record.CreatedAt, &record.UpdatedAt, &record.ciphertext, &record.repositoryID)
	record.CredentialConfigured = len(record.ciphertext) > 0
	return record, err
}

type SourceCollection struct {
	ID                 string                  `json:"id"`
	WorkspaceID        string                  `json:"workspaceId"`
	SourceID           string                  `json:"sourceId"`
	Profile            string                  `json:"profile"`
	ConnectionRevision int64                   `json:"connectionRevision"`
	Repository         string                  `json:"repository"`
	RequestedBy        string                  `json:"requestedBy"`
	State              string                  `json:"state"`
	Complete           bool                    `json:"complete"`
	AssetID            *string                 `json:"assetId"`
	RepositoryID       *string                 `json:"repositoryId"`
	RecordCount        int                     `json:"recordCount"`
	Gaps               []string                `json:"gaps"`
	CreatedAt          time.Time               `json:"createdAt"`
	CollectedAt        *time.Time              `json:"collectedAt"`
	CompletedAt        *time.Time              `json:"completedAt"`
	Failure            *FindingDeliveryFailure `json:"failure"`
}

type sourceCollectionRecord struct {
	SourceCollection
	bindingDigest []byte
	workerID      string
	fence         int64
}

const sourceCollectionColumns = `id,workspace_id,source_id,profile,connection_revision,repository,
	requested_by,state,complete,asset_id,repository_id,record_count,gaps,created_at,collected_at,
	completed_at,failure,binding_digest,COALESCE(worker_id,''),fence`

func scanSourceCollection(row pgx.Row) (sourceCollectionRecord, error) {
	var record sourceCollectionRecord
	var gaps, failure []byte
	err := row.Scan(&record.ID, &record.WorkspaceID, &record.SourceID, &record.Profile,
		&record.ConnectionRevision, &record.Repository, &record.RequestedBy, &record.State,
		&record.Complete, &record.AssetID, &record.RepositoryID, &record.RecordCount, &gaps,
		&record.CreatedAt, &record.CollectedAt, &record.CompletedAt, &failure, &record.bindingDigest,
		&record.workerID, &record.fence)
	if err != nil {
		return record, err
	}
	if err = json.Unmarshal(gaps, &record.Gaps); err != nil {
		return record, err
	}
	if len(failure) > 0 {
		err = json.Unmarshal(failure, &record.Failure)
	}
	return record, err
}

type SourceEvidenceMetadata struct {
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"sizeBytes"`
}

type SourceCollectionRecord struct {
	ID              string                 `json:"id"`
	CollectionID    string                 `json:"collectionId"`
	Ordinal         int                    `json:"ordinal"`
	Kind            string                 `json:"kind"`
	ExternalID      string                 `json:"externalId"`
	ParentID        string                 `json:"parentId"`
	NativeRunID     string                 `json:"nativeRunId"`
	State           string                 `json:"state"`
	Severity        string                 `json:"severity"`
	Location        string                 `json:"location"`
	RawURL          string                 `json:"rawURL"`
	SourceScanAt    *time.Time             `json:"sourceScanAt"`
	SourceUpdatedAt *time.Time             `json:"sourceUpdatedAt"`
	Evidence        SourceEvidenceMetadata `json:"evidence"`
}

type sourceEvidenceRecord struct {
	SourceCollectionRecord
	ref evidence.Ref
}

const sourceEvidenceColumns = `id,collection_id,ordinal,kind,external_id,parent_id,native_run_id,
	state,severity,location,raw_url,source_scan_at,source_updated_at,evidence`

func scanSourceEvidence(row pgx.Row) (sourceEvidenceRecord, error) {
	var record sourceEvidenceRecord
	var raw []byte
	err := row.Scan(&record.ID, &record.CollectionID, &record.Ordinal, &record.Kind, &record.ExternalID,
		&record.ParentID, &record.NativeRunID, &record.State, &record.Severity, &record.Location,
		&record.RawURL, &record.SourceScanAt, &record.SourceUpdatedAt, &raw)
	if err != nil {
		return record, err
	}
	if err = json.Unmarshal(raw, &record.ref); err != nil {
		return record, err
	}
	record.Evidence = SourceEvidenceMetadata{SHA256: record.ref.SHA256, SizeBytes: record.ref.SizeBytes}
	return record, nil
}
