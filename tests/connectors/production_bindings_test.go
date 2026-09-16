package connectors

import (
	"context"
	"errors"

	native "github.com/bahadrdsr/aspm/internal/connectors"
)

func init() {
	Production.OpenDelivery = func(ctx context.Context, config DeliveryConfig) (DeliveryAdapter, error) {
		adapter, err := native.OpenDelivery(ctx, native.DeliveryConfig{
			Profile: config.Profile, Endpoint: config.Endpoint, Token: config.Token, WorkspaceID: config.WorkspaceID,
			Project: config.Project, IssueType: config.IssueType, Channel: config.Channel, StatusMap: config.StatusMap,
			Client: config.Client, Limits: native.Limits(config.Limits),
		})
		if err != nil {
			return nil, bindingError(err)
		}
		return deliveryBinding{adapter: adapter}, nil
	}
	Production.OpenCollector = func(ctx context.Context, config CollectorConfig) (Collector, error) {
		adapter, err := native.OpenCollector(ctx, native.CollectorConfig{
			Profile: config.Profile, Endpoint: config.Endpoint, FindingsEndpoint: config.FindingsEndpoint,
			Token: config.Token, Client: config.Client, Credentials: config.Credentials, Limits: native.Limits(config.Limits),
		})
		if err != nil {
			return nil, bindingError(err)
		}
		return collectorBinding{adapter: adapter}, nil
	}
}

func bindingError(err error) error {
	if err == nil {
		return nil
	}
	translated := []error{err}
	for _, pair := range [][2]error{
		{native.ErrAuth, ErrAuth}, {native.ErrRateLimited, ErrRateLimited}, {native.ErrUncertain, ErrUncertain},
		{native.ErrRequiredFields, ErrRequiredFields}, {native.ErrScope, ErrScope}, {native.ErrLimit, ErrLimit},
		{native.ErrUnavailable, ErrUnavailable},
	} {
		if errors.Is(err, pair[0]) {
			translated = append(translated, pair[1])
		}
	}
	return errors.Join(translated...)
}

func bindingAction(action Action) native.Action {
	var prior *native.Delivery
	if action.Prior != nil {
		converted := native.Delivery(*action.Prior)
		prior = &converted
	}
	return native.Action{
		WorkspaceID: action.WorkspaceID, IntentID: action.IntentID, ApprovalRef: action.ApprovalRef, FindingID: action.FindingID,
		Title: action.Title, Body: action.Body, DeepLink: action.DeepLink, Fields: action.Fields, Prior: prior,
	}
}

type deliveryBinding struct{ adapter native.DeliveryAdapter }

func (b deliveryBinding) Preview(ctx context.Context, action Action) (Preview, error) {
	result, err := b.adapter.Preview(ctx, bindingAction(action))
	return Preview(result), bindingError(err)
}

func (b deliveryBinding) Send(ctx context.Context, action Action) (Delivery, error) {
	result, err := b.adapter.Send(ctx, bindingAction(action))
	return Delivery(result), bindingError(err)
}

func (b deliveryBinding) Status(ctx context.Context, key string) (StatusLink, error) {
	result, err := b.adapter.Status(ctx, key)
	return StatusLink(result), bindingError(err)
}

type collectorBinding struct{ adapter native.Collector }

func (b collectorBinding) Collect(ctx context.Context, request CollectRequest) (Collection, error) {
	result, err := b.adapter.Collect(ctx, native.CollectRequest{
		Identity: native.Identity(request.Identity), Repository: request.Repository, Organization: request.Organization,
		Project: request.Project, PipelineID: request.PipelineID, JobID: request.JobID, BuildID: request.BuildID,
		ArtifactName: request.ArtifactName, ArtifactPath: request.ArtifactPath, SubscriptionID: request.SubscriptionID,
		AccountID: request.AccountID, Region: request.Region,
	})
	records := make([]Record, len(result.Records))
	for i, item := range result.Records {
		records[i] = Record(item)
	}
	return Collection{
		Identity: Identity(result.Identity), CollectedAt: result.CollectedAt, Complete: result.Complete,
		Records: records, Gaps: result.Gaps, Continuation: result.Continuation, RetryAfter: result.RetryAfter,
	}, bindingError(err)
}
