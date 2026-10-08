package app

import (
	"bytes"
	"context"
	"crypto/cipher"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/bahadrdsr/aspm/internal/connectors"
	"github.com/jackc/pgx/v5"
)

type DeliveryWorkerConfig struct {
	Database       DatabaseConfig
	EncryptionKey  []byte `json:"-"`
	WorkerID       string
	LeaseDuration  time.Duration
	PublicOrigin   string
	WebhookOrigins []string
	SlackEndpoint  string       `json:"-"`
	Client         *http.Client `json:"-"`
}

// DeliveryWorker owns a separate database pool and no application HTTP or S3 capability.
type DeliveryWorker struct {
	*database
	credentials    cipher.AEAD
	workerID       string
	lease          time.Duration
	endpoint       string
	publicOrigin   string
	webhookOrigins []string
	client         *http.Client
	lifetime       context.Context
	cancel         context.CancelFunc
	mu             sync.Mutex
	closed         bool
	active         sync.WaitGroup
	closeOnce      sync.Once
}

// ValidateDeliveryGateway checks a trusted HTTPS base without performing I/O.
func ValidateDeliveryGateway(value string) (string, error) {
	if value == "" {
		return "https://slack.com", nil
	}
	u, err := url.Parse(value)
	if err != nil || len(value) > 16384 || u.Scheme != "https" || u.Hostname() == "" ||
		u.User != nil || u.Opaque != "" || u.Fragment != "" || u.RawQuery != "" || u.ForceQuery ||
		strings.ContainsFunc(value, unicode.IsControl) || strings.ContainsAny(u.Path, "\\%?#") ||
		strings.Contains(u.Path, "//") {
		return "", errors.New("delivery gateway must be an explicit HTTPS base without credentials, query or fragment")
	}
	for _, segment := range strings.Split(u.Path, "/") {
		if segment == "." || segment == ".." {
			return "", errors.New("invalid delivery gateway path")
		}
	}
	escaped := strings.ToLower(u.EscapedPath())
	if strings.Contains(escaped, "%2f") || strings.Contains(escaped, "%5c") {
		return "", errors.New("invalid delivery gateway path")
	}
	if port := u.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return "", errors.New("invalid delivery gateway port")
		}
	}
	return value, nil
}

// ValidateDeliveryWorkerConfig validates the explicit worker inputs without opening resources.
func ValidateDeliveryWorkerConfig(config DeliveryWorkerConfig) error {
	if len(config.EncryptionKey) != 32 {
		return errors.New("delivery worker requires an explicit 32-byte integration key")
	}
	if config.Client == nil || config.Client.Transport == nil {
		return errors.New("delivery worker requires an explicitly approved HTTP client and transport")
	}
	if !validText(config.WorkerID, 128) || strings.TrimSpace(config.WorkerID) != config.WorkerID ||
		strings.ContainsFunc(config.WorkerID, unicode.IsControl) ||
		config.LeaseDuration < 250*time.Millisecond || config.LeaseDuration > time.Minute {
		return errors.New("delivery worker requires a bounded identity and a lease from 250ms to one minute")
	}
	if _, err := ValidateDeliveryGateway(config.SlackEndpoint); err != nil {
		return err
	}
	if err := ValidatePublicOrigin(config.PublicOrigin); err != nil {
		return err
	}
	if err := ValidateWebhookOrigins(config.WebhookOrigins); err != nil {
		return err
	}
	return ValidateDatabaseConfig(config.Database)
}

func OpenDeliveryWorker(ctx context.Context, config DeliveryWorkerConfig) (*DeliveryWorker, error) {
	if err := ValidateDeliveryWorkerConfig(config); err != nil {
		return nil, err
	}
	endpoint, err := ValidateDeliveryGateway(config.SlackEndpoint)
	if err != nil {
		return nil, err
	}
	if err = ctx.Err(); err != nil {
		return nil, err
	}
	credentials, err := newIntegrationCipher(config.EncryptionKey)
	if err != nil {
		return nil, err
	}
	client := *config.Client
	client.Jar = nil
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	if client.Timeout <= 0 || client.Timeout > 30*time.Second {
		client.Timeout = 30 * time.Second
	}
	db, err := openDatabase(ctx, config.Database)
	if err != nil {
		return nil, err
	}
	lifetime, cancel := context.WithCancel(context.Background())
	return &DeliveryWorker{
		database: db, credentials: credentials, workerID: config.WorkerID, lease: config.LeaseDuration,
		endpoint: endpoint, publicOrigin: config.PublicOrigin,
		webhookOrigins: append([]string(nil), config.WebhookOrigins...),
		client:         &client, lifetime: lifetime, cancel: cancel,
	}, nil
}

func (w *DeliveryWorker) Ping(ctx context.Context) error { return w.database.ping(ctx) }

func (w *DeliveryWorker) Close() error {
	w.closeOnce.Do(func() {
		w.mu.Lock()
		w.closed = true
		w.cancel()
		w.mu.Unlock()
		w.active.Wait()
		_ = w.database.close()
		w.credentials = nil
	})
	return nil
}

type deliveryDispatch struct {
	record      findingDeliveryRecord
	adapter     connectors.DeliveryAdapter
	token       string
	credential  []byte
	closeClient func()
}

func deliveryInfrastructureError(ctx context.Context, message string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return errors.New(message)
}

func (w *DeliveryWorker) settleClaim(ctx context.Context, tx pgx.Tx, record findingDeliveryRecord, state, code string) error {
	failure, err := json.Marshal(FindingDeliveryFailure{Code: code})
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE `+w.table("finding_deliveries")+`
		SET state=$3,failure=$4,receipt=NULL,completed_at=clock_timestamp(),
			worker_id=$5,fence=fence+1,lease_until=NULL
		WHERE workspace_id=$1 AND id=$2`, record.WorkspaceID, record.ID, state, failure, w.workerID)
	return err
}

func (w *DeliveryWorker) claimDelivery(ctx context.Context) (*deliveryDispatch, bool, error) {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return nil, false, deliveryInfrastructureError(ctx, "open delivery claim failed")
	}
	defer rollback(tx)
	record, err := scanFindingDelivery(tx.QueryRow(ctx, `SELECT `+findingDeliveryColumns+`
		FROM `+w.table("finding_deliveries")+`
		WHERE state='queued' OR (state='dispatching' AND lease_until<=clock_timestamp())
		ORDER BY created_at,id LIMIT 1 FOR UPDATE SKIP LOCKED`))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, deliveryInfrastructureError(ctx, "read delivery claim failed")
	}
	if record.State == "dispatching" {
		// An expired possible write is terminal, never a fresh send.
		state, code := "uncertain", "uncertain"
		if (record.Profile == connectors.JiraCloudV3 || record.Profile == connectors.TeamsWorkflows ||
			record.Profile == connectors.GenericWebhookV1) && record.CreateAttemptedAt == nil {
			state, code = "blocked", "lease-expired"
		}
		if err = w.settleClaim(ctx, tx, record, state, code); err != nil {
			return nil, false, deliveryInfrastructureError(ctx, "record expired delivery uncertainty failed")
		}
		if err = tx.Commit(ctx); err != nil {
			return nil, false, deliveryInfrastructureError(ctx, "commit delivery recovery failed")
		}
		return nil, true, nil
	}
	reason := ""
	if !validDeliveryBinding(record) {
		reason = "binding-changed"
	}
	var role string
	err = tx.QueryRow(ctx, `SELECT role FROM `+w.table("memberships")+`
		WHERE workspace_id=$1 AND user_id=$2 FOR SHARE`, record.WorkspaceID, record.RequestedBy).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) {
		reason = "authorization-revoked"
	} else if err != nil {
		return nil, false, deliveryInfrastructureError(ctx, "read delivery actor authority failed")
	} else if !canWrite(Workspace{Role: role}) {
		reason = "authorization-revoked"
	}
	var connection integrationConnectionRecord
	if reason == "" {
		connection, err = scanIntegrationConnection(tx.QueryRow(ctx, `SELECT `+integrationConnectionColumns+
			` FROM `+w.table("integration_connections")+` WHERE workspace_id=$1 AND id=$2 FOR SHARE`,
			record.WorkspaceID, record.ConnectionID))
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			reason = "connection-changed"
		case err != nil:
			return nil, false, deliveryInfrastructureError(ctx, "read selected delivery connection failed")
		case !connection.Enabled:
			reason = "connection-disabled"
		case connection.Revision != record.ConnectionRevision || connection.Profile != record.Profile || connection.Channel != record.Channel:
			reason = "connection-changed"
		case !reflect.DeepEqual(connection.Jira, record.Jira):
			reason = "connection-changed"
		case record.Profile == connectors.TeamsWorkflows && (!validTeamsMetadata(connection.Teams) ||
			!reflect.DeepEqual(&TeamsDestination{connection.Name, *connection.Teams}, record.Destination)):
			reason = "connection-changed"
		case record.Profile == connectors.GenericWebhookV1 &&
			(!validWebhookTarget(connection.Webhook) ||
				!reflect.DeepEqual(connection.Webhook, record.Webhook) ||
				!webhookOriginAllowed(connection.Webhook.Origin, w.webhookOrigins)):
			reason = "connection-changed"
		}
	}
	var adapter connectors.DeliveryAdapter
	var token string
	var closeClient func()
	defer func() {
		if closeClient != nil {
			closeClient()
		}
	}()
	if reason == "" {
		plain, openErr := openConnectionToken(w.credentials, record.Profile, record.WorkspaceID, record.ConnectionID, connection.ciphertext)
		if openErr != nil {
			reason = "credential-unavailable"
		} else {
			token = string(plain)
			clear(plain)
			config := connectors.DeliveryConfig{
				Profile: record.Profile, Endpoint: w.endpoint, WorkspaceID: record.WorkspaceID,
				Channel: record.Channel, Token: token, Client: w.client,
				Limits: connectors.Limits{Requests: 1, Pages: 1, PageSize: 1, Bytes: 64 << 10},
			}
			if record.Profile == connectors.JiraCloudV3 {
				config.Endpoint, config.Project, config.IssueType = record.Jira.APIBase, record.Jira.Project, record.Jira.IssueType
				config.Limits = connectors.Limits{Requests: 4, Pages: 4, PageSize: 50, Bytes: 64 << 10}
				config.Client, err = sourceNativeClient(w.client, config.Endpoint)
				if err != nil {
					reason = "native-client-unavailable"
				} else {
					closeClient = config.Client.CloseIdleConnections
				}
			}
			if record.Profile == connectors.TeamsWorkflows {
				origin, valid := teamsWorkflowOrigin(token)
				if !valid || origin != connection.Teams.WorkflowOrigin {
					reason = "credential-unavailable"
				} else {
					config.Endpoint, config.Token = token, ""
					config.Client, err = sourceNativeClient(w.client, origin)
					if err != nil {
						reason = "native-client-unavailable"
					} else {
						closeClient = config.Client.CloseIdleConnections
					}
				}
			}
			if record.Profile == connectors.GenericWebhookV1 {
				config.Endpoint, config.AllowedOrigins = webhookEndpoint(record.Webhook),
					append([]string(nil), w.webhookOrigins...)
			}
			if reason == "" {
				adapter, err = connectors.OpenDelivery(ctx, config)
			}
			if ctx.Err() != nil {
				return nil, false, ctx.Err()
			}
			if err != nil {
				reason = "credential-unavailable"
			}
		}
	}
	if reason != "" {
		if err = w.settleClaim(ctx, tx, record, "blocked", reason); err != nil {
			return nil, false, deliveryInfrastructureError(ctx, "record blocked delivery failed")
		}
		if err = tx.Commit(ctx); err != nil {
			return nil, false, deliveryInfrastructureError(ctx, "commit blocked delivery failed")
		}
		return nil, true, nil
	}
	record, err = scanFindingDelivery(tx.QueryRow(ctx, `UPDATE `+w.table("finding_deliveries")+`
		SET state='dispatching',worker_id=$3,fence=fence+1,
			dispatch_started_at=clock_timestamp(),
			lease_until=clock_timestamp()+($4*interval '1 millisecond')
		WHERE workspace_id=$1 AND id=$2 RETURNING `+findingDeliveryColumns,
		record.WorkspaceID, record.ID, w.workerID, w.lease.Milliseconds()))
	if err != nil {
		return nil, false, deliveryInfrastructureError(ctx, "record delivery dispatch start failed")
	}
	// Release all SQL locks and the pool connection before the adapter can POST.
	if err = tx.Commit(ctx); err != nil {
		return nil, false, deliveryInfrastructureError(ctx, "commit delivery dispatch start failed")
	}
	dispatch := &deliveryDispatch{
		record: record, adapter: adapter, token: token, credential: bytes.Clone(connection.ciphertext), closeClient: closeClient,
	}
	closeClient = nil
	return dispatch, true, nil
}

func deliveryOutcome(ctx context.Context, result connectors.Delivery, nativeErr error, token string) (string, *FindingDeliveryReceipt, *FindingDeliveryFailure) {
	if nativeErr == nil && (result.State == "confirmed" || result.State == "accepted") {
		return result.State, &FindingDeliveryReceipt{RemoteID: result.RemoteID, RemoteURL: result.RemoteURL}, nil
	}
	failure := &FindingDeliveryFailure{Code: "uncertain"}
	var detail *connectors.Error
	if errors.As(nativeErr, &detail) {
		failure.Code, failure.NativeCode, failure.HTTPStatus = detail.Code, detail.NativeCode, detail.HTTPStatus
		failure.RetryAfterSeconds = max(0, int64(detail.RetryAfter/time.Second))
		if token != "" && strings.Contains(failure.NativeCode, token) {
			failure.NativeCode = ""
		}
	}
	state := result.State
	switch state {
	case "blocked", "failed", "rate-limited", "uncertain":
	default:
		state, failure.Code = "uncertain", "uncertain"
	}
	if ctx.Err() != nil {
		state, failure.Code = "uncertain", "uncertain"
	}
	return state, nil, failure
}

func (w *DeliveryWorker) finishDelivery(record findingDeliveryRecord, state string, receipt *FindingDeliveryReceipt, failure *FindingDeliveryFailure) error {
	receiptJSON, err := json.Marshal(receipt)
	if err != nil {
		return errors.New("encode delivery receipt failed")
	}
	failureJSON, err := json.Marshal(failure)
	if err != nil {
		return errors.New("encode delivery failure failed")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	command, err := w.pool.Exec(ctx, `UPDATE `+w.table("finding_deliveries")+`
		SET state=$5,receipt=$6,failure=$7,completed_at=clock_timestamp(),lease_until=NULL
		WHERE workspace_id=$1 AND id=$2 AND worker_id=$3 AND fence=$4
			AND state='dispatching' AND lease_until>clock_timestamp()`,
		record.WorkspaceID, record.ID, w.workerID, record.fence, state, receiptJSON, failureJSON)
	if err != nil {
		return errors.New("persist delivery outcome failed")
	}
	if command.RowsAffected() != 1 {
		return errors.New("delivery lease is no longer current; the outcome cannot be confirmed by this worker")
	}
	return nil
}

func (w *DeliveryWorker) ProcessNext(ctx context.Context) (bool, error) {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		return false, errors.New("delivery worker is closed")
	}
	w.active.Add(1)
	w.mu.Unlock()
	defer w.active.Done()
	requestCtx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(w.lifetime, cancel)
	defer stop()
	defer cancel()
	if w.lifetime.Err() != nil {
		cancel()
	}
	dispatch, processed, err := w.claimDelivery(requestCtx)
	if err != nil {
		return processed, err
	}
	if dispatch == nil {
		if processed {
			return true, nil
		}
		return w.processNextFindingChangeEvent(requestCtx)
	}
	record := dispatch.record
	if dispatch.closeClient != nil {
		defer dispatch.closeClient()
	}
	defer clear(dispatch.credential)
	if record.Profile == connectors.JiraCloudV3 {
		return true, w.processJiraDelivery(requestCtx, dispatch)
	}
	if record.Profile == connectors.TeamsWorkflows {
		return true, w.processTeamsDelivery(requestCtx, dispatch)
	}
	if record.Profile == connectors.GenericWebhookV1 {
		return true, w.processGenericWebhookDelivery(requestCtx, dispatch)
	}
	result, nativeErr := dispatch.adapter.Send(requestCtx, connectors.Action{
		WorkspaceID: record.WorkspaceID, IntentID: record.ID, ApprovalRef: record.approvalRef,
		FindingID: record.FindingID, Title: record.Payload.Title, Body: record.Payload.Body, DeepLink: record.Payload.DeepLink,
		Trigger: deliveryTrigger(record),
	})
	state, receipt, failure := deliveryOutcome(requestCtx, result, nativeErr, dispatch.token)
	if err = w.finishDelivery(record, state, receipt, failure); err != nil {
		return true, err
	}
	if err = requestCtx.Err(); err != nil {
		return true, err
	}
	return true, nil
}

func deliveryTrigger(record findingDeliveryRecord) connectors.DeliveryTrigger {
	return connectors.DeliveryTrigger{
		Kind: record.TriggerKind, PolicyID: record.PolicyID,
		PolicyRevision:        record.PolicyRevision,
		FindingChangeRevision: record.FindingChangeRevision,
	}
}
