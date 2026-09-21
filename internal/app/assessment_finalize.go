package app

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bahadrdsr/aspm/internal/providers"
	"github.com/jackc/pgx/v5"
)

type assessmentOutcome struct {
	state, dispatchState                 string
	result                               *providers.Assessment
	failure                              *Failure
	usage                                AssessmentUsage
	requestID, returnedModel, stopReason string
	retryAfterMillis                     int64
}

func assessmentFailure(cause error) *Failure {
	code, message := "provider", "The provider did not return a usable assessment"
	var output *providers.OutputError
	switch {
	case errors.Is(cause, context.DeadlineExceeded):
		code, message = "timeout", "The assessment request deadline elapsed; delivery may have occurred"
	case errors.Is(cause, context.Canceled):
		code, message = "cancelled", "The dispatched assessment was cancelled; delivery may have occurred"
	case errors.Is(cause, providers.ErrAuth):
		code, message = "auth", "The provider rejected authentication"
	case errors.Is(cause, providers.ErrRateLimited):
		code, message = "rate-limited", "The provider rejected the request due to rate limits; no retry was scheduled"
	case errors.Is(cause, providers.ErrToolOutput):
		code, message = "tool-output", "Provider tool output was rejected; no tool was executed"
	case errors.Is(cause, providers.ErrLimit):
		code, message = "response-limit", "The provider response exceeded an assessment bound"
	case errors.Is(cause, providers.ErrCapability):
		code, message = "capability", "The required structured-output capability is not approved"
	case errors.Is(cause, providers.ErrPolicy), errors.Is(cause, errAssessmentChanged):
		code, message = "authorization-revoked", "The approved assessment authority or consent is no longer current"
	case errors.As(cause, &output):
		switch output.Kind {
		case providers.OutputRefusal:
			code, message = "refusal", "The provider refused the assessment"
		case providers.OutputGrounding:
			code, message = "grounding", "The assessment referenced context outside the approved snapshot"
		case providers.OutputIncomplete:
			code, message = "incomplete", "The provider response was incomplete"
		default:
			code, message = "schema", "The provider output did not satisfy the assessment schema"
		}
	case errors.Is(cause, providers.ErrOutput):
		code, message = "schema", "The provider output did not satisfy the assessment schema"
	}
	return &Failure{Code: code, Message: message}
}

func assessmentNativeOutcome(job assessmentJobRecord, native providers.Result, cause error, httpState *assessmentHTTPState, secrets []string) assessmentOutcome {
	outcome := assessmentOutcome{state: "failed", dispatchState: "possibly-sent"}
	received := httpState.received.Load() && !httpState.interrupted.Load()
	if received {
		outcome.dispatchState = "response-received"
		outcome.usage = AssessmentUsage{Known: native.Usage.Known,
			InputTokens: native.Usage.InputTokens, OutputTokens: native.Usage.OutputTokens,
			CachedInputTokens: native.Usage.CachedInputTokens, CacheWriteTokens: native.Usage.CacheWriteTokens}
	}
	for _, field := range []struct {
		value  string
		target *string
	}{{native.RequestID, &outcome.requestID}, {native.ReturnedModel, &outcome.returnedModel}, {native.StopReason, &outcome.stopReason}} {
		if len(field.value) > 512 {
			if cause == nil {
				cause = providers.ErrLimit
			}
		} else if !utf8.ValidString(field.value) || strings.ContainsRune(field.value, 0) || containsAssessmentSecret(field.value, secrets) {
			if cause == nil {
				cause = &providers.OutputError{Kind: providers.OutputSchema}
			}
		} else {
			*field.target = field.value
		}
	}
	outcome.retryAfterMillis = min(max(native.RetryAfter.Milliseconds(), 0), 86400000)
	if httpState.timedOut.Load() || errors.Is(cause, context.DeadlineExceeded) {
		cause = context.DeadlineExceeded
	}
	if cause == nil {
		value := native.Assessment
		switch {
		case !received || value == nil ||
			native.WorkspaceID != job.WorkspaceID || native.RunID != job.ID || native.FindingID != job.FindingID ||
			native.ProfileID != job.ProfileID || native.ProfileRevision != job.ProfileRevision ||
			native.PolicyRevision != job.PolicyRevision || native.PromptRevision != job.PromptRevision ||
			native.EvidenceDigest != job.ContextDigest || native.RequestedModel != job.Model || native.Deployment != job.Deployment:
			cause = &providers.OutputError{Kind: providers.OutputSchema}
		case len(value.Uncertainty) > 8192 || len(value.EvidenceRefs) > 16:
			cause = providers.ErrLimit
		case !utf8.ValidString(value.Uncertainty) || strings.TrimSpace(value.Uncertainty) == "" ||
			strings.ContainsRune(value.Uncertainty, 0) || containsAssessmentSecret(value.Uncertainty, secrets):
			cause = &providers.OutputError{Kind: providers.OutputSchema}
		default:
			for _, ref := range value.EvidenceRefs {
				if ref != job.ContextRef {
					cause = &providers.OutputError{Kind: providers.OutputGrounding}
					break
				}
			}
			if cause == nil {
				copy := *value
				copy.EvidenceRefs = append([]string{}, value.EvidenceRefs...)
				outcome.state, outcome.result = "succeeded", &copy
			}
		}
	}
	if cause != nil {
		outcome.failure = assessmentFailure(cause)
		if !received {
			outcome.state = "uncertain"
		}
		if errors.Is(cause, context.Canceled) {
			outcome.state = "cancelled"
		}
	}
	return outcome
}

func (w *AssessmentWorker) finalizeAssessment(ctx, operation context.Context, job assessmentJobRecord, outcome assessmentOutcome) error {
	tx, err := w.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer rollback(tx)
	resolved, authorityErr := w.assessmentAuthority(ctx, tx, job)
	if authorityErr != nil && !assessmentAuthorityDenied(authorityErr) {
		return authorityErr
	}
	var lease time.Time
	if err = tx.QueryRow(ctx, `SELECT lease_until FROM `+w.table("assessment_jobs")+`
		WHERE workspace_id=$1 AND id=$2 AND scope=$3 AND worker_id=$4 AND fence=$5
			AND state='dispatching' AND lease_until>clock_timestamp() FOR UPDATE`,
		job.WorkspaceID, job.ID, w.scope, w.workerID, job.fence).Scan(&lease); errors.Is(err, pgx.ErrNoRows) {
		return errAssessmentLeaseLost
	} else if err != nil {
		return err
	}
	if authorityErr != nil {
		outcome.state, outcome.result = "invalidated", nil
		outcome.failure = assessmentFailure(providers.ErrPolicy)
	} else if cause := context.Cause(operation); cause != nil {
		outcome.state, outcome.result = "uncertain", nil
		if errors.Is(cause, context.Canceled) {
			outcome.state = "cancelled"
		}
		outcome.failure = assessmentFailure(cause)
	}
	var result, failure []byte
	if outcome.result != nil {
		result, err = json.Marshal(outcome.result)
		if err != nil {
			return err
		}
	}
	if outcome.failure != nil {
		failure, err = json.Marshal(outcome.failure)
		if err != nil {
			return err
		}
	}
	usage, err := json.Marshal(outcome.usage)
	if err != nil {
		return err
	}
	expires := assessmentExpiry(job.ConsentExpiresAt, resolved)
	command, err := tx.Exec(ctx, `UPDATE `+w.table("assessment_jobs")+`
		SET state=$6,dispatch_state=$7,result=$8,failure=$9,usage=$10,
			request_id=$11,returned_model=$12,stop_reason=$13,retry_after_millis=$14,
			completed_at=clock_timestamp(),worker_id=NULL,lease_until=NULL,fence=fence+1
		WHERE workspace_id=$1 AND id=$2 AND scope=$3 AND worker_id=$4 AND fence=$5
			AND state='dispatching' AND lease_until>clock_timestamp()
			AND ($15::boolean OR $16::timestamptz>clock_timestamp())`,
		job.WorkspaceID, job.ID, w.scope, w.workerID, job.fence,
		outcome.state, outcome.dispatchState, result, failure, usage,
		outcome.requestID, outcome.returnedModel, outcome.stopReason, outcome.retryAfterMillis,
		authorityErr != nil, expires)
	if err != nil {
		return err
	}
	if command.RowsAffected() != 1 {
		return errAssessmentChanged
	}
	// Check the actual clock again after the final write, including a delayed
	// query callback. A stale response must not make an expired fence current.
	var liveLease bool
	if err = tx.QueryRow(ctx, `SELECT clock_timestamp()<$1`, lease).Scan(&liveLease); err != nil {
		return err
	}
	if !liveLease {
		return errAssessmentLeaseLost
	}
	if authorityErr == nil {
		valid, err := w.assessmentTimeValid(ctx, tx, expires)
		if err != nil {
			return err
		}
		if !valid || outcome.state == "succeeded" && operation.Err() != nil {
			return errAssessmentChanged
		}
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
