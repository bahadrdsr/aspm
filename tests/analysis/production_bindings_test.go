package analysis

import (
	"context"
	"errors"

	"github.com/bahadrdsr/aspm/internal/providers"
	"github.com/bahadrdsr/aspm/internal/verification"
)

func init() {
	Production.OpenAssessor = func(ctx context.Context, config ProviderConfig) (Assessor, error) {
		profiles := make(map[string]providers.Profile, len(config.Profiles))
		for id, p := range config.Profiles {
			profiles[id] = providers.Profile{
				ID: p.ID, Family: p.Family, Endpoint: p.Endpoint, Model: p.Model,
				Deployment: p.Deployment, APIKey: p.APIKey, Revision: p.Revision, StructuredOutput: p.StructuredOutput,
			}
		}
		a, err := providers.Open(ctx, providers.Config{
			Profiles: profiles, Policy: providers.Policy(config.Policy), Client: config.Client,
			MaxAttempts: config.MaxAttempts, MaxOutputTokens: config.MaxOutputTokens, MaxResponseBytes: config.MaxResponseBytes,
		})
		if err != nil {
			return nil, translated(err)
		}
		return assessorBinding{a}, nil
	}
	Production.OpenVerifier = func(ctx context.Context, config VerifierConfig) (Verifier, error) {
		v, err := verification.Open(ctx, verification.Config{
			Evidence: config.Evidence, Now: config.Now, MaxBytes: config.MaxBytes,
			LookupApproval: func(ctx context.Context, ref string) (verification.Approval, error) {
				a, err := config.LookupApproval(ctx, ref)
				return verification.Approval(a), err
			},
		})
		if err != nil {
			return nil, translated(err)
		}
		return verifierBinding{v}, nil
	}
}

func translated(err error) error {
	for _, pair := range [][2]error{
		{providers.ErrPolicy, ErrPolicy}, {providers.ErrAuth, ErrAuth}, {providers.ErrRateLimited, ErrRateLimited},
		{providers.ErrProvider, ErrProvider}, {providers.ErrOutput, ErrOutput}, {providers.ErrToolOutput, ErrToolOutput},
		{providers.ErrLimit, ErrLimit}, {providers.ErrCapability, ErrCapability},
		{verification.ErrPolicy, ErrPolicy}, {verification.ErrLimit, ErrLimit},
	} {
		if errors.Is(err, pair[0]) {
			return errors.Join(pair[1], err)
		}
	}
	return err
}

type assessorBinding struct{ *providers.Assessor }

func (a assessorBinding) Assess(ctx context.Context, request AssessmentRequest) (AssessmentResult, error) {
	r, err := a.Assessor.Assess(ctx, providers.Request(request))
	result := AssessmentResult{
		WorkspaceID: r.WorkspaceID, RunID: r.RunID, FindingID: r.FindingID, ProfileID: r.ProfileID,
		ProfileRevision: r.ProfileRevision, PolicyRevision: r.PolicyRevision, PromptRevision: r.PromptRevision,
		EvidenceDigest: r.EvidenceDigest, RequestID: r.RequestID, RequestedModel: r.RequestedModel,
		ReturnedModel: r.ReturnedModel, Deployment: r.Deployment, StopReason: r.StopReason, Usage: Usage(r.Usage), RetryAfter: r.RetryAfter,
	}
	if r.Assessment != nil {
		value := Assessment(*r.Assessment)
		result.Assessment = &value
	}
	return result, translated(err)
}

type verifierBinding struct{ *verification.Verifier }

func (v verifierBinding) Verify(ctx context.Context, request VerificationRequest) (VerificationResult, error) {
	r, err := v.Verifier.Verify(ctx, verification.Request(request))
	return VerificationResult(r), translated(err)
}
