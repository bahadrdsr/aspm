//go:build integration

package assessment_runtime

import (
	"context"
	"time"

	"github.com/bahadrdsr/aspm/internal/app"
	"github.com/bahadrdsr/aspm/internal/jobs"
	"github.com/bahadrdsr/aspm/internal/service"
)

func init() {
	Production.Environment = func(role string) (runtimeConfig, error) {
		c, err := service.Environment(role)
		return runtimeConfig{
			Database: app.DatabaseConfig{DatabaseURL: c.Jobs.DatabaseURL, Schema: c.Jobs.Schema,
				ApplicationName: c.Jobs.ApplicationName, MaxConnections: c.Jobs.MaxConnections},
			Raw: c.Evidence, CollectionStorage: c.CollectionStorage, Listen: c.Listen, WorkerID: c.WorkerID,
			Assets: c.Assets, PublicOrigin: c.PublicOrigin, Bootstrap: c.BootstrapToken, Key: c.IntegrationEncryptionKey,
			Scope: c.AssessmentScope, Lease: c.AssessmentLeaseDuration, AuthorizationInterval: c.AssessmentAuthorizationInterval,
			RequestTimeout: c.AssessmentRequestTimeout, Window: c.AssessmentRequestWindow,
			MaxConcurrent: c.AssessmentMaxConcurrent, RequestsPerWindow: c.AssessmentRequestsPerWindow,
			MaxInput: c.AssessmentMaxInputBytes, MaxOutput: c.AssessmentMaxOutputTokens, MaxResponse: c.AssessmentMaxResponseBytes,
			Client: c.AssessmentClient, CAFile: c.AssessmentCAFile,
			ReadinessKey: c.ReadinessKey, NormalizedPrefix: c.NormalizedPrefix, PrepareReadiness: c.PrepareReadiness,
			DeliveryClient: c.DeliveryClient, CollectionClient: c.CollectionClient,
			SlackEndpoint: c.SlackEndpoint, GitHubEndpoint: c.GitHubEndpoint,
		}, err
	}
	Production.Run = func(ctx context.Context, role string, c runtimeConfig) error {
		return service.Run(ctx, role, service.Config{
			Listen: c.Listen, WorkerID: c.WorkerID, Assets: c.Assets, PublicOrigin: c.PublicOrigin,
			BootstrapToken: c.Bootstrap, IntegrationEncryptionKey: c.Key,
			Evidence: c.Raw, CollectionStorage: c.CollectionStorage,
			AssessmentScope: c.Scope, AssessmentLeaseDuration: c.Lease,
			AssessmentAuthorizationInterval: c.AuthorizationInterval, AssessmentRequestTimeout: c.RequestTimeout,
			AssessmentRequestWindow: c.Window, AssessmentMaxConcurrent: c.MaxConcurrent,
			AssessmentRequestsPerWindow: c.RequestsPerWindow, AssessmentMaxInputBytes: c.MaxInput,
			AssessmentMaxOutputTokens: c.MaxOutput, AssessmentMaxResponseBytes: c.MaxResponse,
			AssessmentClient: c.Client, AssessmentCAFile: c.CAFile,
			ReadinessKey: c.ReadinessKey, NormalizedPrefix: c.NormalizedPrefix, PrepareReadiness: c.PrepareReadiness,
			DeliveryClient: c.DeliveryClient, CollectionClient: c.CollectionClient,
			SlackEndpoint: c.SlackEndpoint, GitHubEndpoint: c.GitHubEndpoint,
			Jobs: jobs.Config{DatabaseURL: c.Database.DatabaseURL, Schema: c.Database.Schema,
				ApplicationName: c.Database.ApplicationName, MaxConnections: c.Database.MaxConnections,
				MaxLease: time.Second, MaxAttempts: 3, RetryDelay: time.Second, MaxRetryDelay: 2 * time.Second},
		})
	}
}
