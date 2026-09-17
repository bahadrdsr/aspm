package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5"
)

type FindingNotification struct {
	Title    string `json:"title"`
	Body     string `json:"body"`
	DeepLink string `json:"deepLink"`
}

type FindingDeliveryReceipt struct {
	RemoteID  string `json:"remoteId"`
	RemoteURL string `json:"remoteUrl"`
}

type FindingDeliveryFailure struct {
	Code              string `json:"code"`
	NativeCode        string `json:"nativeCode"`
	HTTPStatus        int    `json:"httpStatus"`
	RetryAfterSeconds int64  `json:"retryAfterSeconds"`
	Retryable         bool   `json:"retryable"`
}

type FindingDelivery struct {
	ID                 string                  `json:"id"`
	WorkspaceID        string                  `json:"workspaceId"`
	FindingID          string                  `json:"findingId"`
	ConnectionID       string                  `json:"connectionId"`
	ConnectionRevision int64                   `json:"connectionRevision"`
	Profile            string                  `json:"profile"`
	Channel            string                  `json:"channel"`
	RequestedBy        string                  `json:"requestedBy"`
	State              string                  `json:"state"`
	Payload            FindingNotification     `json:"payload"`
	CreatedAt          time.Time               `json:"createdAt"`
	DispatchStartedAt  *time.Time              `json:"dispatchStartedAt"`
	CompletedAt        *time.Time              `json:"completedAt"`
	Receipt            *FindingDeliveryReceipt `json:"receipt"`
	Failure            *FindingDeliveryFailure `json:"failure"`
}

type findingDeliveryRecord struct {
	FindingDelivery
	bindingDigest []byte
	approvalRef   string
	fence         int64
}

const findingDeliveryColumns = `id,workspace_id,finding_id,connection_id,connection_revision,
	profile,channel,requested_by,state,payload,created_at,dispatch_started_at,completed_at,
	receipt,failure,binding_digest,approval_ref,fence`

func scanFindingDelivery(row pgx.Row) (findingDeliveryRecord, error) {
	var record findingDeliveryRecord
	var payload, receipt, failure []byte
	err := row.Scan(&record.ID, &record.WorkspaceID, &record.FindingID, &record.ConnectionID,
		&record.ConnectionRevision, &record.Profile, &record.Channel, &record.RequestedBy, &record.State,
		&payload, &record.CreatedAt, &record.DispatchStartedAt, &record.CompletedAt,
		&receipt, &failure, &record.bindingDigest, &record.approvalRef, &record.fence)
	if err != nil {
		return record, err
	}
	if err = json.Unmarshal(payload, &record.Payload); err != nil {
		return record, err
	}
	if len(receipt) != 0 {
		if err = json.Unmarshal(receipt, &record.Receipt); err != nil {
			return record, err
		}
	}
	if len(failure) != 0 {
		if err = json.Unmarshal(failure, &record.Failure); err != nil {
			return record, err
		}
	}
	return record, nil
}

func deliveryBinding(delivery FindingDelivery) ([]byte, error) {
	value := struct {
		Workspace, Finding, Connection, Actor, Profile, Channel string
		Revision                                                int64
		Payload                                                 FindingNotification
	}{
		Workspace: delivery.WorkspaceID, Finding: delivery.FindingID, Connection: delivery.ConnectionID,
		Actor: delivery.RequestedBy, Profile: delivery.Profile, Channel: delivery.Channel,
		Revision: delivery.ConnectionRevision, Payload: delivery.Payload,
	}
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(data)
	return digest[:], nil
}

func (a *Application) enqueueFindingDelivery(w http.ResponseWriter, r *http.Request, workspace string, session authenticatedSession, findingID string) error {
	var input struct {
		ConnectionID   string `json:"connectionId"`
		IdempotencyKey string `json:"idempotencyKey"`
	}
	if err := a.decode(w, r, &input, 16<<10); err != nil {
		return err
	}
	if !validID(input.ConnectionID) || !validText(input.IdempotencyKey, 256) {
		return errInvalid
	}
	if a.config.PublicOrigin == "" {
		return errUnavailable
	}
	tx, err := a.pool.Begin(r.Context())
	if err != nil {
		return err
	}
	defer rollback(tx)
	var workspaceID string
	if err = tx.QueryRow(r.Context(), `SELECT id FROM `+a.table("workspaces")+`
		WHERE id=$1 FOR KEY SHARE`, workspace).Scan(&workspaceID); err != nil {
		return err
	}
	role, err := a.sessionRole(r.Context(), tx, session, workspace)
	if err != nil {
		return err
	}
	if !canWrite(Workspace{Role: role}) {
		return errForbidden
	}
	finding, err := scanWork(tx.QueryRow(r.Context(), `SELECT `+workColumns+a.workFrom()+
		` WHERE f.workspace_id=$1 AND f.id=$2 FOR SHARE OF f,asset`, workspace, findingID))
	if err != nil {
		return err
	}
	connection, err := scanIntegrationConnection(tx.QueryRow(r.Context(), `SELECT `+integrationConnectionColumns+
		` FROM `+a.table("integration_connections")+` WHERE workspace_id=$1 AND id=$2 FOR SHARE`,
		workspace, input.ConnectionID))
	if err != nil {
		return err
	}
	if !connection.Enabled {
		return errConflict
	}
	delivery := FindingDelivery{
		ID: newID(), WorkspaceID: workspace, FindingID: findingID, ConnectionID: connection.ID,
		ConnectionRevision: connection.Revision, Profile: connection.Profile, Channel: connection.Channel,
		RequestedBy: session.User.ID, State: "queued", CreatedAt: a.config.Now().UTC(),
		Payload: FindingNotification{
			Title: finding.Title, Body: "Severity: " + finding.Severity + "\nAsset: " + finding.AssetName,
			DeepLink: a.config.PublicOrigin + "/#/work?finding=" + url.QueryEscape(finding.ID),
		},
	}
	digest, err := deliveryBinding(delivery)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(delivery.Payload)
	if err != nil {
		return err
	}
	record, err := scanFindingDelivery(tx.QueryRow(r.Context(), `INSERT INTO `+a.table("finding_deliveries")+`
		(id,workspace_id,finding_id,connection_id,connection_revision,profile,channel,requested_by,
		 idempotency_key,binding_digest,approval_ref,payload,created_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		ON CONFLICT (workspace_id,idempotency_key) DO NOTHING RETURNING `+findingDeliveryColumns,
		delivery.ID, workspace, findingID, connection.ID, connection.Revision, connection.Profile,
		connection.Channel, session.User.ID, input.IdempotencyKey, digest,
		"aspm:remediation:"+workspace+":"+delivery.ID, payload, delivery.CreatedAt))
	status := http.StatusAccepted
	if errors.Is(err, pgx.ErrNoRows) {
		record, err = scanFindingDelivery(tx.QueryRow(r.Context(), `SELECT `+findingDeliveryColumns+
			` FROM `+a.table("finding_deliveries")+` WHERE workspace_id=$1 AND idempotency_key=$2`,
			workspace, input.IdempotencyKey))
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
	writeJSON(w, status, map[string]any{"delivery": record.FindingDelivery})
	return nil
}

func (a *Application) getFindingDelivery(w http.ResponseWriter, r *http.Request, workspace, id string) error {
	record, err := scanFindingDelivery(a.pool.QueryRow(r.Context(), `SELECT `+findingDeliveryColumns+
		` FROM `+a.table("finding_deliveries")+` WHERE workspace_id=$1 AND id=$2`, workspace, id))
	if err != nil {
		return err
	}
	writeJSON(w, http.StatusOK, map[string]any{"delivery": record.FindingDelivery})
	return nil
}

func (a *Application) listFindingDeliveries(w http.ResponseWriter, r *http.Request, workspace, findingID string) error {
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
	if err = tx.QueryRow(r.Context(), `SELECT id FROM `+a.table("findings")+` WHERE workspace_id=$1 AND id=$2`,
		workspace, findingID).Scan(&id); err != nil {
		return err
	}
	var total int64
	if err = tx.QueryRow(r.Context(), `SELECT count(*) FROM `+a.table("finding_deliveries")+`
		WHERE workspace_id=$1 AND finding_id=$2`, workspace, findingID).Scan(&total); err != nil {
		return err
	}
	rows, err := tx.Query(r.Context(), `SELECT `+findingDeliveryColumns+` FROM `+a.table("finding_deliveries")+`
		WHERE workspace_id=$1 AND finding_id=$2 AND id>$3 ORDER BY id LIMIT $4`, workspace, findingID, cursor, limit+1)
	if err != nil {
		return err
	}
	items := []FindingDelivery{}
	for rows.Next() {
		record, scanErr := scanFindingDelivery(rows)
		if scanErr != nil {
			rows.Close()
			return scanErr
		}
		items = append(items, record.FindingDelivery)
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
