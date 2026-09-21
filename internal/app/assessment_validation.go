package app

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/bahadrdsr/aspm/internal/providers"
	"github.com/jackc/pgx/v5"
)

func validAssessmentIdentity(value string) bool {
	return validAIText(value, 128) && strings.TrimSpace(value) == value
}

// ValidateAssessmentScope checks a nonempty explicit scope without opening resources.
func ValidateAssessmentScope(scope string) error {
	if !validAssessmentIdentity(scope) {
		return errors.New("assessment scope must be an explicit bounded identity")
	}
	return nil
}

type assessmentContextText string

func (text *assessmentContextText) UnmarshalJSON(data []byte) error {
	var value string
	if len(data) < 2 || data[0] != '"' || json.Unmarshal(data, &value) != nil {
		return errInvalid
	}
	// encoding/json repairs unpaired UTF-16 escapes. Reviewed bytes must instead
	// be rejected, not silently replaced with a different consented context.
	for i := 1; i < len(data)-1; i++ {
		if data[i] != '\\' {
			continue
		}
		i++
		if data[i] != 'u' {
			continue
		}
		code, err := strconv.ParseUint(string(data[i+1:i+5]), 16, 16)
		if err != nil {
			return errInvalid
		}
		i += 4
		if code >= 0xdc00 && code <= 0xdfff {
			return errInvalid
		}
		if code >= 0xd800 && code <= 0xdbff {
			if i+6 >= len(data) || data[i+1] != '\\' || data[i+2] != 'u' {
				return errInvalid
			}
			low, err := strconv.ParseUint(string(data[i+3:i+7]), 16, 16)
			if err != nil || low < 0xdc00 || low > 0xdfff {
				return errInvalid
			}
			i += 6
		}
	}
	*text = assessmentContextText(value)
	return nil
}

func validAssessmentContext(value string) bool {
	return value != "" && utf8.ValidString(value) && strings.TrimSpace(value) != "" &&
		len(value) <= assessmentContextLimit && !strings.ContainsRune(value, 0)
}

func assessmentRequest(binding AssessmentBinding) AIConfigurationRequest {
	return AIConfigurationRequest{WorkspaceID: binding.WorkspaceID, ActorID: binding.RequestedBy,
		ProfileID: binding.ProfileID, GrantID: binding.GrantID, Task: binding.Task, DataClass: binding.DataClass}
}

func assessmentBindingCurrent(binding AssessmentBinding, resolved aiResolution) bool {
	profile, policy := resolved.configuration.Profile, resolved.configuration.Policy
	return binding.ProfileID == profile.ID && binding.ProfileRevision == profile.Revision &&
		binding.PolicyRevision == policy.Revision && binding.GrantID == policy.ApprovalRef &&
		binding.Destination == profile.Endpoint && binding.Family == profile.Family &&
		binding.Model == profile.Model && binding.Deployment == profile.Deployment &&
		binding.Task == aiTask && binding.DataClass == aiDataClass &&
		binding.PromptRevision == assessmentPromptRevision && binding.ContextOrigin == assessmentContextOrigin &&
		validAssessmentContext(binding.Context) && binding.ContextDigest == reportDigest([]byte(binding.Context))
}

func containsAssessmentSecret(value string, secrets []string) bool {
	for _, secret := range secrets {
		if secret != "" && strings.Contains(value, secret) {
			return true
		}
	}
	return false
}

func assessmentBindingHasSecret(binding AssessmentBinding, secrets []string) bool {
	for _, value := range []string{binding.WorkspaceID, binding.FindingID, binding.ObservationID,
		binding.SourceEvidenceDigest, binding.RequestedBy, binding.ProfileID, binding.ProfileRevision,
		binding.PolicyRevision, binding.GrantID, binding.Destination, binding.Family, binding.Model,
		binding.Deployment, binding.Task, binding.DataClass, binding.PromptRevision, binding.ContextRef,
		binding.ContextDigest, binding.ContextOrigin, binding.Context} {
		if containsAssessmentSecret(value, secrets) {
			return true
		}
	}
	return false
}

func assessmentAuthorityDenied(err error) bool {
	return errors.Is(err, providers.ErrPolicy) || errors.Is(err, providers.ErrCapability) ||
		errors.Is(err, pgx.ErrNoRows) || errors.Is(err, errAICredential)
}

func assessmentAPIError(err error) error {
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return errNotFound
	case errors.Is(err, providers.ErrPolicy), errors.Is(err, providers.ErrCapability):
		return errConflict
	case errors.Is(err, errAICredential):
		return errUnavailable
	default:
		return err
	}
}

func assessmentExpiry(consent time.Time, resolved aiResolution) time.Time {
	if resolved.grantExpiresAt != nil && resolved.grantExpiresAt.Before(consent) {
		return *resolved.grantExpiresAt
	}
	return consent
}

// The policy clock may deny earlier, but cannot extend real expiry. Scheduling
// and rate admission always use PostgreSQL's clock, never this optional clock.
func (db *database) assessmentTimeValid(ctx context.Context, q queryRower, expires time.Time) (bool, error) {
	var live bool
	err := q.QueryRow(ctx, `SELECT clock_timestamp()<$1`, expires).Scan(&live)
	return live && expires.After(db.now().UTC()), err
}
