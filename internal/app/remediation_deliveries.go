package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
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
	ID                    string                  `json:"id"`
	WorkspaceID           string                  `json:"workspaceId"`
	FindingID             string                  `json:"findingId"`
	ConnectionID          string                  `json:"connectionId"`
	ConnectionRevision    int64                   `json:"connectionRevision"`
	Profile               string                  `json:"profile"`
	Channel               string                  `json:"channel"`
	RequestedBy           string                  `json:"requestedBy"`
	State                 string                  `json:"state"`
	Payload               FindingNotification     `json:"payload"`
	CreatedAt             time.Time               `json:"createdAt"`
	DispatchStartedAt     *time.Time              `json:"dispatchStartedAt"`
	CompletedAt           *time.Time              `json:"completedAt"`
	Receipt               *FindingDeliveryReceipt `json:"receipt"`
	Failure               *FindingDeliveryFailure `json:"failure"`
	Jira                  *JiraTarget             `json:"jira,omitempty"`
	Destination           *TeamsDestination       `json:"destination,omitempty"`
	Webhook               *WebhookTarget          `json:"webhook,omitempty"`
	CreateAttemptedAt     *time.Time              `json:"-"`
	TriggerKind           string                  `json:"-"`
	PolicyID              *string                 `json:"-"`
	PolicyRevision        *int64                  `json:"-"`
	FindingChangeRevision *int64                  `json:"-"`
}

func (delivery FindingDelivery) MarshalJSON() ([]byte, error) {
	type value FindingDelivery
	trigger := ""
	if delivery.TriggerKind == "notification-policy" {
		trigger = delivery.TriggerKind
	}
	if delivery.Profile == connectors.GenericWebhookV1 {
		return json.Marshal(struct {
			ID                    string                  `json:"id"`
			WorkspaceID           string                  `json:"workspaceId"`
			FindingID             string                  `json:"findingId"`
			ConnectionID          string                  `json:"connectionId"`
			ConnectionRevision    int64                   `json:"connectionRevision"`
			Profile               string                  `json:"profile"`
			RequestedBy           string                  `json:"requestedBy"`
			State                 string                  `json:"state"`
			Payload               FindingNotification     `json:"payload"`
			CreatedAt             time.Time               `json:"createdAt"`
			DispatchStartedAt     *time.Time              `json:"dispatchStartedAt"`
			OutboundAttemptedAt   *time.Time              `json:"outboundAttemptedAt"`
			CompletedAt           *time.Time              `json:"completedAt"`
			Receipt               *FindingDeliveryReceipt `json:"receipt"`
			Failure               *FindingDeliveryFailure `json:"failure"`
			Webhook               *WebhookTarget          `json:"webhook"`
			TriggerKind           string                  `json:"triggerKind,omitempty"`
			PolicyID              *string                 `json:"policyId,omitempty"`
			PolicyRevision        *int64                  `json:"policyRevision,omitempty"`
			FindingChangeRevision *int64                  `json:"findingChangeRevision,omitempty"`
		}{
			delivery.ID, delivery.WorkspaceID, delivery.FindingID, delivery.ConnectionID,
			delivery.ConnectionRevision, delivery.Profile, delivery.RequestedBy, delivery.State,
			delivery.Payload, delivery.CreatedAt, delivery.DispatchStartedAt,
			delivery.CreateAttemptedAt, delivery.CompletedAt, delivery.Receipt, delivery.Failure,
			delivery.Webhook, trigger, delivery.PolicyID, delivery.PolicyRevision,
			delivery.FindingChangeRevision,
		})
	}
	if delivery.Profile == connectors.JiraCloudV3 {
		return json.Marshal(struct {
			value
			TriggerKind           string     `json:"triggerKind,omitempty"`
			PolicyID              *string    `json:"policyId,omitempty"`
			PolicyRevision        *int64     `json:"policyRevision,omitempty"`
			FindingChangeRevision *int64     `json:"findingChangeRevision,omitempty"`
			CreateAttemptedAt     *time.Time `json:"createAttemptedAt"`
		}{value(delivery), trigger, delivery.PolicyID, delivery.PolicyRevision,
			delivery.FindingChangeRevision, delivery.CreateAttemptedAt})
	}
	if delivery.Profile == connectors.TeamsWorkflows {
		return json.Marshal(struct {
			value
			TriggerKind           string     `json:"triggerKind,omitempty"`
			PolicyID              *string    `json:"policyId,omitempty"`
			PolicyRevision        *int64     `json:"policyRevision,omitempty"`
			FindingChangeRevision *int64     `json:"findingChangeRevision,omitempty"`
			OutboundAttemptedAt   *time.Time `json:"outboundAttemptedAt"`
		}{value(delivery), trigger, delivery.PolicyID, delivery.PolicyRevision,
			delivery.FindingChangeRevision, delivery.CreateAttemptedAt})
	}
	return json.Marshal(struct {
		value
		TriggerKind           string  `json:"triggerKind,omitempty"`
		PolicyID              *string `json:"policyId,omitempty"`
		PolicyRevision        *int64  `json:"policyRevision,omitempty"`
		FindingChangeRevision *int64  `json:"findingChangeRevision,omitempty"`
	}{value(delivery), trigger, delivery.PolicyID, delivery.PolicyRevision,
		delivery.FindingChangeRevision})
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
	receipt,failure,binding_digest,approval_ref,fence,idempotency_key,jira_target,create_attempted_at,teams_target,
	trigger_kind,policy_id,policy_revision,finding_change_revision,webhook_target`

func scanFindingDelivery(row pgx.Row) (findingDeliveryRecord, error) {
	var record findingDeliveryRecord
	var payload, receipt, failure, target, teams, webhook []byte
	err := row.Scan(&record.ID, &record.WorkspaceID, &record.FindingID, &record.ConnectionID,
		&record.ConnectionRevision, &record.Profile, &record.Channel, &record.RequestedBy, &record.State,
		&payload, &record.CreatedAt, &record.DispatchStartedAt, &record.CompletedAt,
		&receipt, &failure, &record.bindingDigest, &record.approvalRef, &record.fence,
		&record.idempotencyKey, &target, &record.CreateAttemptedAt, &teams,
		&record.TriggerKind, &record.PolicyID, &record.PolicyRevision, &record.FindingChangeRevision,
		&webhook)
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
	if len(webhook) != 0 {
		if err = json.Unmarshal(webhook, &record.Webhook); err != nil {
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
	var binding any = value
	if delivery.Profile == connectors.JiraCloudV3 {
		binding = struct {
			Binding any
			Jira    *JiraTarget
		}{value, delivery.Jira}
	} else if delivery.Profile == connectors.TeamsWorkflows {
		binding = struct {
			Binding     any
			Destination *TeamsDestination
		}{value, delivery.Destination}
	} else if delivery.Profile == connectors.GenericWebhookV1 {
		binding = struct {
			Binding any
			Webhook *WebhookTarget
		}{value, delivery.Webhook}
	}
	if delivery.TriggerKind == "notification-policy" {
		binding = struct {
			Binding               any
			TriggerKind           string
			PolicyID              *string
			PolicyRevision        *int64
			FindingChangeRevision *int64
		}{binding, delivery.TriggerKind, delivery.PolicyID,
			delivery.PolicyRevision, delivery.FindingChangeRevision}
	}
	data, err := json.Marshal(binding)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(data)
	return digest[:], nil
}

func deliveryIntentBinding(delivery FindingDelivery, key string) ([]byte, error) {
	binding, err := deliveryBinding(delivery)
	if err != nil || delivery.Profile != connectors.JiraCloudV3 &&
		delivery.Profile != connectors.TeamsWorkflows &&
		delivery.Profile != connectors.GenericWebhookV1 {
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
	if !validText(record.idempotencyKey, 256) {
		return false
	}
	switch record.TriggerKind {
	case "manual":
		if record.PolicyID != nil || record.PolicyRevision != nil || record.FindingChangeRevision != nil ||
			record.approvalRef != "aspm:remediation:"+record.WorkspaceID+":"+record.ID {
			return false
		}
	case "notification-policy":
		if record.PolicyID == nil || !validID(*record.PolicyID) ||
			record.PolicyRevision == nil || *record.PolicyRevision < 1 ||
			record.FindingChangeRevision == nil || *record.FindingChangeRevision < 1 ||
			record.idempotencyKey != "notification-policy:"+*record.PolicyID+":"+
				record.FindingID+":"+strconv.FormatInt(*record.FindingChangeRevision, 10) ||
			record.approvalRef != "aspm:notification-policy:"+record.WorkspaceID+":"+
				*record.PolicyID+":"+strconv.FormatInt(*record.PolicyRevision, 10) {
			return false
		}
	default:
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
	if record.Profile == connectors.GenericWebhookV1 &&
		(record.Jira != nil || record.Destination != nil || record.Channel != "" ||
			record.Payload.Fields != nil || !validWebhookTarget(record.Webhook)) {
		return false
	}
	binding, err := deliveryIntentBinding(record.FindingDelivery, record.idempotencyKey)
	return err == nil && bytes.Equal(binding, record.bindingDigest)
}

func lockDeliveryKey(ctx context.Context, tx pgx.Tx, scope, key string) error {
	_, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, scope+"/"+key)
	return err
}

func (a *database) insertFindingDelivery(ctx context.Context, tx pgx.Tx, delivery FindingDelivery,
	key string, digest []byte, approvalRef string) (findingDeliveryRecord, error) {
	payload, err := json.Marshal(delivery.Payload)
	if err != nil {
		return findingDeliveryRecord{}, err
	}
	var target, teams, webhook []byte
	if delivery.Jira != nil {
		target, err = json.Marshal(delivery.Jira)
		if err != nil {
			return findingDeliveryRecord{}, err
		}
	}
	if delivery.Destination != nil {
		teams, err = json.Marshal(delivery.Destination)
		if err != nil {
			return findingDeliveryRecord{}, err
		}
	}
	if delivery.Webhook != nil {
		webhook, err = json.Marshal(delivery.Webhook)
		if err != nil {
			return findingDeliveryRecord{}, err
		}
	}
	return scanFindingDelivery(tx.QueryRow(ctx, `INSERT INTO `+a.table("finding_deliveries")+`
		(id,workspace_id,finding_id,connection_id,connection_revision,profile,channel,requested_by,
		 idempotency_key,binding_digest,approval_ref,payload,created_at,jira_target,teams_target,
		 trigger_kind,policy_id,policy_revision,finding_change_revision,webhook_target)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20)
		RETURNING `+findingDeliveryColumns,
		delivery.ID, delivery.WorkspaceID, delivery.FindingID, delivery.ConnectionID,
		delivery.ConnectionRevision, delivery.Profile, delivery.Channel, delivery.RequestedBy,
		key, digest, approvalRef, payload, delivery.CreatedAt, target, teams, delivery.TriggerKind,
		delivery.PolicyID, delivery.PolicyRevision, delivery.FindingChangeRevision, webhook))
}

func lockJiraFindingEffect(ctx context.Context, tx pgx.Tx, workspace, connection, finding string) error {
	return lockDeliveryKey(ctx, tx, "aspm/jira-finding-effect",
		workspace+"/"+connection+"/"+finding)
}

func (a *database) jiraFindingEffect(ctx context.Context, tx pgx.Tx,
	workspace, connection, finding string) (string, error) {
	var delivery string
	err := tx.QueryRow(ctx, `SELECT delivery_id FROM `+a.table("jira_finding_effects")+`
		WHERE workspace_id=$1 AND connection_id=$2 AND finding_id=$3`,
		workspace, connection, finding).Scan(&delivery)
	return delivery, err
}

func (a *database) insertJiraFindingEffect(ctx context.Context, tx pgx.Tx,
	delivery FindingDelivery) error {
	_, err := tx.Exec(ctx, `INSERT INTO `+a.table("jira_finding_effects")+`
		(workspace_id,connection_id,finding_id,delivery_id,created_at)
		VALUES($1,$2,$3,$4,$5)`,
		delivery.WorkspaceID, delivery.ConnectionID, delivery.FindingID, delivery.ID, delivery.CreatedAt)
	return err
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
	if delivery.Profile == connectors.JiraCloudV3 || delivery.Profile == connectors.TeamsWorkflows ||
		delivery.Profile == connectors.GenericWebhookV1 {
		if input.Confirm.Value == nil || !*input.Confirm.Value || input.PreviewDigest.Value == nil ||
			len(*input.PreviewDigest.Value) != 71 {
			return errInvalid
		}
		if delivery.Profile == connectors.TeamsWorkflows || delivery.Profile == connectors.GenericWebhookV1 {
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
	delivery.ID, delivery.State, delivery.CreatedAt, delivery.TriggerKind =
		newID(), "queued", a.config.Now().UTC(), "manual"
	digest, err := deliveryIntentBinding(delivery, input.IdempotencyKey)
	if err != nil {
		return err
	}
	if err = lockDeliveryKey(r.Context(), tx, "aspm/finding-delivery-intent",
		workspace+"/"+input.IdempotencyKey); err != nil {
		return err
	}
	record, lookupErr := scanFindingDelivery(tx.QueryRow(r.Context(), `SELECT `+findingDeliveryColumns+
		` FROM `+a.table("finding_deliveries")+` WHERE workspace_id=$1 AND idempotency_key=$2`,
		workspace, input.IdempotencyKey))
	if lookupErr == nil {
		if !bytes.Equal(record.bindingDigest, digest) || !validDeliveryBinding(record) {
			return errConflict
		}
		if err = tx.Commit(r.Context()); err != nil {
			return err
		}
		writeJSON(w, http.StatusOK, map[string]any{"delivery": record.FindingDelivery})
		return nil
	}
	if !errors.Is(lookupErr, pgx.ErrNoRows) {
		return lookupErr
	}
	if delivery.Profile == connectors.JiraCloudV3 {
		if err = lockJiraFindingEffect(r.Context(), tx, workspace, delivery.ConnectionID, findingID); err != nil {
			return err
		}
		if _, err = a.jiraFindingEffect(r.Context(), tx, workspace, delivery.ConnectionID, findingID); err == nil {
			return errConflict
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}
	record, err = a.insertFindingDelivery(r.Context(), tx, delivery, input.IdempotencyKey, digest,
		"aspm:remediation:"+workspace+":"+delivery.ID)
	if err != nil {
		return err
	}
	if delivery.Profile == connectors.JiraCloudV3 {
		if err = a.insertJiraFindingEffect(r.Context(), tx, delivery); err != nil {
			return err
		}
	}
	if err = tx.Commit(r.Context()); err != nil {
		return err
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"delivery": record.FindingDelivery})
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
