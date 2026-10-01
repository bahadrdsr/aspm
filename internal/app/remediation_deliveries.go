package app

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/bahadrdsr/aspm/internal/connectors"
	"github.com/jackc/pgx/v5"
)

type FindingNotification struct {
	Title    string            `json:"title"`
	Body     string            `json:"body"`
	DeepLink string            `json:"deepLink"`
	Fields   map[string]string `json:"fields,omitempty"`
}

func (payload FindingNotification) MarshalJSON() ([]byte, error) {
	var fields *map[string]string
	if payload.Fields != nil {
		fields = &payload.Fields
	}
	return json.Marshal(struct {
		Title    string             `json:"title"`
		Body     string             `json:"body"`
		DeepLink string             `json:"deepLink"`
		Fields   *map[string]string `json:"fields,omitempty"`
	}{payload.Title, payload.Body, payload.DeepLink, fields})
}

type FindingDeliveryReceipt struct {
	RemoteID  string `json:"remoteId"`
	RemoteURL string `json:"remoteUrl"`
}

type FindingDeliveryFailure struct {
	Code              string   `json:"code"`
	NativeCode        string   `json:"nativeCode"`
	HTTPStatus        int      `json:"httpStatus"`
	RetryAfterSeconds int64    `json:"retryAfterSeconds"`
	Retryable         bool     `json:"retryable"`
	Stage             string   `json:"stage,omitempty"`
	MissingFields     []string `json:"missingFields,omitempty"`
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
	Jira               *JiraTarget             `json:"jira,omitempty"`
	Destination        *TeamsDestination       `json:"destination,omitempty"`
	CreateAttemptedAt  *time.Time              `json:"-"`
}

func (delivery FindingDelivery) MarshalJSON() ([]byte, error) {
	type value FindingDelivery
	if delivery.Profile == connectors.JiraCloudV3 {
		return json.Marshal(struct {
			value
			CreateAttemptedAt *time.Time `json:"createAttemptedAt"`
		}{value(delivery), delivery.CreateAttemptedAt})
	}
	if delivery.Profile == connectors.TeamsWorkflows {
		return json.Marshal(struct {
			value
			OutboundAttemptedAt *time.Time `json:"outboundAttemptedAt"`
		}{value(delivery), delivery.CreateAttemptedAt})
	}
	return json.Marshal(value(delivery))
}

type findingDeliveryRecord struct {
	FindingDelivery
	bindingDigest  []byte
	approvalRef    string
	fence          int64
	idempotencyKey string
}

const findingDeliveryColumns = `id,workspace_id,finding_id,connection_id,connection_revision,
	profile,channel,requested_by,state,payload,created_at,dispatch_started_at,completed_at,
	receipt,failure,binding_digest,approval_ref,fence,idempotency_key,jira_target,create_attempted_at,teams_target`

func scanFindingDelivery(row pgx.Row) (findingDeliveryRecord, error) {
	var record findingDeliveryRecord
	var payload, receipt, failure, target, teams []byte
	err := row.Scan(&record.ID, &record.WorkspaceID, &record.FindingID, &record.ConnectionID,
		&record.ConnectionRevision, &record.Profile, &record.Channel, &record.RequestedBy, &record.State,
		&payload, &record.CreatedAt, &record.DispatchStartedAt, &record.CompletedAt,
		&receipt, &failure, &record.bindingDigest, &record.approvalRef, &record.fence,
		&record.idempotencyKey, &target, &record.CreateAttemptedAt, &teams)
	if err != nil {
		return record, err
	}
	if err = json.Unmarshal(payload, &record.Payload); err != nil {
		return record, err
	}
	if len(target) != 0 {
		if err = json.Unmarshal(target, &record.Jira); err != nil {
			return record, err
		}
	}
	if len(teams) != 0 {
		if err = json.Unmarshal(teams, &record.Destination); err != nil {
			return record, err
		}
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
	var data []byte
	var err error
	if delivery.Profile == connectors.JiraCloudV3 {
		data, err = json.Marshal(struct {
			Binding any
			Jira    *JiraTarget
		}{value, delivery.Jira})
	} else if delivery.Profile == connectors.TeamsWorkflows {
		data, err = json.Marshal(struct {
			Binding     any
			Destination *TeamsDestination
		}{value, delivery.Destination})
	} else {
		data, err = json.Marshal(value)
	}
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(data)
	return digest[:], nil
}

func deliveryIntentBinding(delivery FindingDelivery, key string) ([]byte, error) {
	binding, err := deliveryBinding(delivery)
	if err != nil || delivery.Profile != connectors.JiraCloudV3 && delivery.Profile != connectors.TeamsWorkflows {
		return binding, err
	}
	data, err := json.Marshal(struct {
		Binding        []byte
		IdempotencyKey string
	}{binding, key})
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(data)
	return digest[:], nil
}

func validDeliveryBinding(record findingDeliveryRecord) bool {
	if record.approvalRef != "aspm:remediation:"+record.WorkspaceID+":"+record.ID ||
		!validText(record.idempotencyKey, 256) {
		return false
	}
	if record.Profile == connectors.JiraCloudV3 &&
		(!validJiraTarget(record.Jira) || record.Channel != "" || record.Payload.Fields == nil || record.Destination != nil) {
		return false
	}
	if record.Profile == connectors.SlackWorkspaceBot && (record.Jira != nil || record.Payload.Fields != nil || record.Destination != nil) {
		return false
	}
	if record.Profile == connectors.TeamsWorkflows && (record.Jira != nil || record.Channel != "" ||
		!validTeamsDestination(record.Destination) || !teamsPayloadValid(record.Payload)) {
		return false
	}
	binding, err := deliveryIntentBinding(record.FindingDelivery, record.idempotencyKey)
	return err == nil && bytes.Equal(binding, record.bindingDigest)
}

func (a *Application) enqueueFindingDelivery(w http.ResponseWriter, r *http.Request, workspace string, session authenticatedSession, findingID string) error {
	var input struct {
		ConnectionID   string           `json:"connectionId"`
		IdempotencyKey string           `json:"idempotencyKey"`
		PreviewDigest  optional[string] `json:"previewDigest"`
		Confirm        optional[bool]   `json:"confirm"`
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
	delivery, err := a.selectedFindingDelivery(r, tx, workspace, session, findingID, input.ConnectionID)
	if err != nil {
		return err
	}
	if delivery.Profile == connectors.JiraCloudV3 || delivery.Profile == connectors.TeamsWorkflows {
		if input.Confirm.Value == nil || !*input.Confirm.Value || input.PreviewDigest.Value == nil ||
			len(*input.PreviewDigest.Value) != 71 {
			return errInvalid
		}
		if delivery.Profile == connectors.TeamsWorkflows {
			raw, err := hex.DecodeString((*input.PreviewDigest.Value)[7:])
			if !strings.HasPrefix(*input.PreviewDigest.Value, "sha256:") || err != nil || len(raw) != 32 ||
				strings.ToLower(*input.PreviewDigest.Value) != *input.PreviewDigest.Value {
				return errInvalid
			}
		}
		binding, err := deliveryBinding(delivery)
		if err != nil {
			return err
		}
		if *input.PreviewDigest.Value != "sha256:"+hex.EncodeToString(binding) {
			return errConflict
		}
	} else if input.Confirm.Set || input.PreviewDigest.Set {
		return errInvalid
	}
	delivery.ID, delivery.State, delivery.CreatedAt = newID(), "queued", a.config.Now().UTC()
	digest, err := deliveryIntentBinding(delivery, input.IdempotencyKey)
	if err != nil {
		return err
	}
	payload, err := json.Marshal(delivery.Payload)
	if err != nil {
		return err
	}
	var target, teams []byte
	if delivery.Jira != nil {
		target, err = json.Marshal(delivery.Jira)
		if err != nil {
			return err
		}
	}
	if delivery.Destination != nil {
		teams, err = json.Marshal(delivery.Destination)
		if err != nil {
			return err
		}
	}
	record, err := scanFindingDelivery(tx.QueryRow(r.Context(), `INSERT INTO `+a.table("finding_deliveries")+`
		(id,workspace_id,finding_id,connection_id,connection_revision,profile,channel,requested_by,
		 idempotency_key,binding_digest,approval_ref,payload,created_at,jira_target,teams_target)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15)
		ON CONFLICT (workspace_id,idempotency_key) DO NOTHING RETURNING `+findingDeliveryColumns,
		delivery.ID, workspace, findingID, delivery.ConnectionID, delivery.ConnectionRevision, delivery.Profile,
		delivery.Channel, session.User.ID, input.IdempotencyKey, digest,
		"aspm:remediation:"+workspace+":"+delivery.ID, payload, delivery.CreatedAt, target, teams))
	status := http.StatusAccepted
	if errors.Is(err, pgx.ErrNoRows) {
		record, err = scanFindingDelivery(tx.QueryRow(r.Context(), `SELECT `+findingDeliveryColumns+
			` FROM `+a.table("finding_deliveries")+` WHERE workspace_id=$1 AND idempotency_key=$2`,
			workspace, input.IdempotencyKey))
		if err != nil {
			return err
		}
		if !bytes.Equal(record.bindingDigest, digest) || !validDeliveryBinding(record) {
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
	profile, err := deliveryProfile(r)
	if err != nil {
		return err
	}
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
		WHERE workspace_id=$1 AND finding_id=$2 AND profile=$3`, workspace, findingID, profile).Scan(&total); err != nil {
		return err
	}
	rows, err := tx.Query(r.Context(), `SELECT `+findingDeliveryColumns+` FROM `+a.table("finding_deliveries")+`
		WHERE workspace_id=$1 AND finding_id=$2 AND profile=$3 AND id>$4 ORDER BY id LIMIT $5`,
		workspace, findingID, profile, cursor, limit+1)
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
