//go:build integration

package collection_runtime

import (
	"context"
	"time"

	"github.com/bahadrdsr/aspm/internal/app"
	"github.com/bahadrdsr/aspm/internal/jobs"
	"github.com/bahadrdsr/aspm/internal/service"
)

func init() {
	Production.Environment = func(role string) (runtimeConfig, error) {
		config, err := service.Environment(role)
		return runtimeConfig{
			Database: app.DatabaseConfig{DatabaseURL: config.Jobs.DatabaseURL, Schema: config.Jobs.Schema,
				ApplicationName: config.Jobs.ApplicationName, MaxConnections: config.Jobs.MaxConnections},
			Raw: config.Evidence, CollectionStorage: config.CollectionStorage, Key: config.IntegrationEncryptionKey,
			Listen: config.Listen, Assets: config.Assets, PublicOrigin: config.PublicOrigin,
			Bootstrap: config.BootstrapToken, WorkerID: config.WorkerID,
			Lease: config.CollectionLeaseDuration, Endpoint: config.GitHubEndpoint,
			Client: config.CollectionClient, CAFile: config.CollectionCAFile, Limits: config.CollectionLimits,
			ReadinessKey: config.ReadinessKey, NormalizedPrefix: config.NormalizedPrefix, PrepareReadiness: config.PrepareReadiness,
		}, err
	}
	Production.Run = func(ctx context.Context, role string, config runtimeConfig) error {
		return service.Run(ctx, role, service.Config{
			Listen: config.Listen, Assets: config.Assets, PublicOrigin: config.PublicOrigin,
			BootstrapToken: config.Bootstrap, WorkerID: config.WorkerID, IntegrationEncryptionKey: config.Key,
			Evidence: config.Raw, CollectionStorage: config.CollectionStorage,
			CollectionLeaseDuration: config.Lease, GitHubEndpoint: config.Endpoint,
			CollectionClient: config.Client, CollectionCAFile: config.CAFile, CollectionLimits: config.Limits,
			ReadinessKey: config.ReadinessKey, NormalizedPrefix: config.NormalizedPrefix, PrepareReadiness: config.PrepareReadiness,
			Jobs: jobs.Config{DatabaseURL: config.Database.DatabaseURL, Schema: config.Database.Schema,
				ApplicationName: config.Database.ApplicationName, MaxConnections: config.Database.MaxConnections,
				MaxLease: time.Second, MaxAttempts: 3, RetryDelay: time.Second, MaxRetryDelay: 2 * time.Second},
		})
	}
}
