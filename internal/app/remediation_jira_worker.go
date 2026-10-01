package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/bahadrdsr/aspm/internal/connectors"
	"github.com/jackc/pgx/v5"
)

type deliveryDenial string

func (denial deliveryDenial) Error() string { return string(denial) }

const deliveryLeaseLost deliveryDenial = "lease-lost"

func (w *DeliveryWorker) jiraDispatchAuthority(ctx context.Context, db queryRower, dispatch *deliveryDispatch, lock bool) (findingDeliveryRecord, error) {
	suffix := ""
	if lock {
		suffix = " FOR UPDATE"
	}
	prior := dispatch.record
	current, err := scanFindingDelivery(db.QueryRow(ctx, `SELECT `+findingDeliveryColumns+`
		FROM `+w.table("finding_deliveries")+`
		WHERE workspace_id=$1 AND id=$2 AND worker_id=$3 AND fence=$4
		AND state='dispatching' AND lease_until>clock_timestamp()`+suffix,
		prior.WorkspaceID, prior.ID, w.workerID, prior.fence))
	if errors.Is(err, pgx.ErrNoRows) {
		return current, deliveryLeaseLost
	}
	if err != nil {
		return current, err
	}
	if !validDeliveryBinding(current) || !bytes.Equal(current.bindingDigest, prior.bindingDigest) {
		return current, deliveryDenial("binding-changed")
	}
	if lock {
		suffix = " FOR SHARE"
	}
	var role string
	err = db.QueryRow(ctx, `SELECT role FROM `+w.table("memberships")+`
		WHERE workspace_id=$1 AND user_id=$2`+suffix, prior.WorkspaceID, prior.RequestedBy).Scan(&role)
	if errors.Is(err, pgx.ErrNoRows) || err == nil && !canWrite(Workspace{Role: role}) {
		return current, deliveryDenial("authorization-revoked")
	}
	if err != nil {
		return current, err
	}
	connection, err := scanIntegrationConnection(db.QueryRow(ctx, `SELECT `+integrationConnectionColumns+`
		FROM `+w.table("integration_connections")+` WHERE workspace_id=$1 AND id=$2`+suffix,
		prior.WorkspaceID, prior.ConnectionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return current, deliveryDenial("connection-changed")
	}
	if err != nil {
		return current, err
	}
	if !connection.Enabled {
		return current, deliveryDenial("connection-disabled")
	}
	target, err := json.Marshal(connection.Jira)
	if err != nil {
		return current, err
	}
	approved, err := json.Marshal(prior.Jira)
	if err != nil {
		return current, err
	}
	if connection.Revision != prior.ConnectionRevision || connection.Profile != prior.Profile ||
		connection.Channel != prior.Channel || !bytes.Equal(target, approved) ||
		!bytes.Equal(connection.ciphertext, dispatch.credential) {
		return current, deliveryDenial("connection-changed")
	}
	return current, nil
}

func (w *DeliveryWorker) watchJiraDelivery(ctx context.Context, cancel context.CancelCauseFunc, dispatch *deliveryDispatch) func() {
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
				probe, end := context.WithTimeout(watch, 250*time.Millisecond)
				_, err := w.jiraDispatchAuthority(probe, w.pool, dispatch, false)
				end()
				if err != nil {
					if watch.Err() == nil {
						cancel(err)
					}
					return
				}
			}
		}
	}()
	return func() { stop(); <-done }
}

func (w *DeliveryWorker) lockDeliveryWorkspace(ctx context.Context, tx pgx.Tx, workspace string) error {
	var id string
	return tx.QueryRow(ctx, `SELECT id FROM `+w.table("workspaces")+` WHERE id=$1 FOR KEY SHARE`,
		workspace).Scan(&id)
}

func (w *DeliveryWorker) markJiraCreate(ctx context.Context, dispatch *deliveryDispatch) (findingDeliveryRecord, error) {
	record := dispatch.record
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return record, err
	}
	defer rollback(tx)
	if err = w.lockDeliveryWorkspace(ctx, tx, record.WorkspaceID); err != nil {
		return record, err
	}
	current, err := w.jiraDispatchAuthority(ctx, tx, dispatch, true)
	if err != nil {
		return current, err
	}
	if current.CreateAttemptedAt != nil {
		return current, deliveryDenial("create-already-attempted")
	}
	current, err = scanFindingDelivery(tx.QueryRow(ctx, `UPDATE `+w.table("finding_deliveries")+`
		SET create_attempted_at=clock_timestamp()
		WHERE workspace_id=$1 AND id=$2 AND worker_id=$3 AND fence=$4
		AND state='dispatching' AND lease_until>clock_timestamp() AND create_attempted_at IS NULL
		RETURNING `+findingDeliveryColumns, record.WorkspaceID, record.ID, w.workerID, record.fence))
	if errors.Is(err, pgx.ErrNoRows) {
		return current, deliveryLeaseLost
	}
	if err != nil {
		return current, err
	}
	// The create marker must be durable before the adapter can send a POST.
	return current, tx.Commit(ctx)
}

var jiraDiagnosticField = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,127}$`)

func jiraMissingFields(fields []string, token string) []string {
	result := []string{}
	for _, field := range fields {
		if jiraDiagnosticField.MatchString(field) && (token == "" || !strings.Contains(field, token)) {
			result = append(result, field)
		}
	}
	slices.Sort(result)
	return slices.Compact(result)
}

func jiraInterruptedOutcome(cause error, attempted bool) (string, *FindingDeliveryFailure) {
	if attempted {
		return "uncertain", &FindingDeliveryFailure{Code: "uncertain", Stage: "create"}
	}
	code := "unavailable"
	var denial deliveryDenial
	if errors.As(cause, &denial) {
		code = string(denial)
	} else if errors.Is(cause, context.Canceled) || errors.Is(cause, context.DeadlineExceeded) {
		code = "canceled"
	}
	return "blocked", &FindingDeliveryFailure{Code: code, Stage: "metadata"}
}

func jiraMetadataOutcome(preview connectors.Preview, nativeErr error, token string) (string, *FindingDeliveryFailure) {
	state := "failed"
	switch {
	case errors.Is(nativeErr, connectors.ErrScope):
		state = "blocked"
	case errors.Is(nativeErr, connectors.ErrRateLimited):
		state = "rate-limited"
	}
	state, _, failure := deliveryOutcome(context.Background(), connectors.Delivery{State: state}, nativeErr, token)
	failure.Stage = "metadata"
	failure.MissingFields = jiraMissingFields(preview.MissingFields, token)
	return state, failure
}

func (w *DeliveryWorker) finishJiraDelivery(dispatch *deliveryDispatch, state string, receipt *FindingDeliveryReceipt, failure *FindingDeliveryFailure) error {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return errors.New("open Jira delivery finalization failed")
	}
	defer rollback(tx)
	prior := dispatch.record
	if err = w.lockDeliveryWorkspace(ctx, tx, prior.WorkspaceID); err != nil {
		return errors.New("lock Jira delivery workspace failed")
	}
	current, err := w.jiraDispatchAuthority(ctx, tx, dispatch, true)
	if err != nil {
		var denial deliveryDenial
		if !errors.As(err, &denial) || denial == deliveryLeaseLost {
			return deliveryInfrastructureError(ctx, "Jira delivery authority or lease is no longer current")
		}
		state, failure = jiraInterruptedOutcome(err, current.CreateAttemptedAt != nil)
		receipt = nil
	}
	receiptJSON, err := json.Marshal(receipt)
	if err != nil {
		return errors.New("encode Jira delivery receipt failed")
	}
	failureJSON, err := json.Marshal(failure)
	if err != nil {
		return errors.New("encode Jira delivery failure failed")
	}
	tag, err := tx.Exec(ctx, `UPDATE `+w.table("finding_deliveries")+`
		SET state=$5,receipt=$6,failure=$7,completed_at=clock_timestamp(),lease_until=NULL
		WHERE workspace_id=$1 AND id=$2 AND worker_id=$3 AND fence=$4
		AND state='dispatching' AND lease_until>clock_timestamp()`,
		prior.WorkspaceID, prior.ID, w.workerID, prior.fence, state, receiptJSON, failureJSON)
	if err != nil {
		return errors.New("persist Jira delivery outcome failed")
	}
	if tag.RowsAffected() != 1 {
		return deliveryLeaseLost
	}
	if err = tx.Commit(ctx); err != nil {
		return errors.New("commit Jira delivery outcome failed")
	}
	return nil
}

func (w *DeliveryWorker) processJiraDelivery(ctx context.Context, dispatch *deliveryDispatch) error {
	nativeCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	stop := w.watchJiraDelivery(nativeCtx, cancel, dispatch)
	defer stop()
	record := dispatch.record
	action := connectors.Action{
		WorkspaceID: record.WorkspaceID, IntentID: record.ID, ApprovalRef: record.approvalRef,
		FindingID: record.FindingID, Title: record.Payload.Title, Body: record.Payload.Body,
		DeepLink: record.Payload.DeepLink, Fields: record.Payload.Fields,
	}
	preview, err := dispatch.adapter.Preview(nativeCtx, action)
	if err != nil {
		stop()
		state, failure := jiraMetadataOutcome(preview, err, dispatch.token)
		if cause := context.Cause(nativeCtx); cause != nil {
			state, failure = jiraInterruptedOutcome(cause, false)
		}
		return w.finishJiraDelivery(dispatch, state, nil, failure)
	}
	record, err = w.markJiraCreate(nativeCtx, dispatch)
	if err != nil {
		stop()
		cause := err
		if interrupted := context.Cause(nativeCtx); interrupted != nil {
			cause = interrupted
		}
		state, failure := jiraInterruptedOutcome(cause, record.CreateAttemptedAt != nil)
		return w.finishJiraDelivery(dispatch, state, nil, failure)
	}
	result, nativeErr := dispatch.adapter.Send(nativeCtx, action)
	stop()
	state, receipt, failure := deliveryOutcome(nativeCtx, result, nativeErr, dispatch.token)
	if receipt != nil {
		if !strings.HasPrefix(receipt.RemoteID, record.Jira.Project+"-") ||
			!jiraIssueType.MatchString(strings.TrimPrefix(receipt.RemoteID, record.Jira.Project+"-")) {
			state, receipt, failure = "uncertain", nil, &FindingDeliveryFailure{Code: "uncertain"}
		} else {
			receipt.RemoteURL = record.Jira.SiteOrigin + "/browse/" + receipt.RemoteID
		}
	}
	if failure != nil {
		failure.Stage = "create"
		failure.MissingFields = jiraMissingFields(result.MissingFields, dispatch.token)
	}
	if context.Cause(nativeCtx) != nil {
		state, failure = jiraInterruptedOutcome(context.Cause(nativeCtx), true)
		receipt = nil
	}
	return w.finishJiraDelivery(dispatch, state, receipt, failure)
}
