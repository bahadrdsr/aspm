package app

import (
	"context"

	"github.com/bahadrdsr/aspm/internal/connectors"
)

func teamsDeliveryOutcome(ctx context.Context, result connectors.Delivery, nativeErr error) (string, *FindingDeliveryFailure) {
	if nativeErr == nil && result.State == "accepted" && ctx.Err() == nil {
		return "accepted", nil
	}
	if nativeErr == nil {
		nativeErr = connectors.ErrUncertain
	}
	state, _, failure := deliveryOutcome(ctx, result, nativeErr, "")
	if failure == nil {
		state, failure = "uncertain", &FindingDeliveryFailure{Code: "uncertain"}
	}
	failure.NativeCode, failure.Stage, failure.MissingFields = "", "", nil
	return state, failure
}

func (w *DeliveryWorker) processTeamsDelivery(ctx context.Context, dispatch *deliveryDispatch) error {
	nativeCtx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	stop := w.watchDeliveryAuthority(nativeCtx, cancel, dispatch)
	defer stop()
	record := dispatch.record
	action := connectors.Action{
		WorkspaceID: record.WorkspaceID, IntentID: record.ID, ApprovalRef: record.approvalRef,
		FindingID: record.FindingID, Title: record.Payload.Title, Body: record.Payload.Body, DeepLink: record.Payload.DeepLink,
	}
	record, err := w.markDeliveryAttempt(nativeCtx, dispatch)
	if err != nil {
		stop()
		cause := err
		if interrupted := context.Cause(nativeCtx); interrupted != nil {
			cause = interrupted
		}
		state, failure := guardedDeliveryInterruptedOutcome(connectors.TeamsWorkflows, cause, record.CreateAttemptedAt != nil)
		return w.finishGuardedDelivery(dispatch, state, nil, failure)
	}
	result, nativeErr := dispatch.adapter.Send(nativeCtx, action)
	stop()
	state, failure := teamsDeliveryOutcome(nativeCtx, result, nativeErr)
	if cause := context.Cause(nativeCtx); cause != nil {
		state, failure = guardedDeliveryInterruptedOutcome(connectors.TeamsWorkflows, cause, true)
	}
	return w.finishGuardedDelivery(dispatch, state, nil, failure)
}
