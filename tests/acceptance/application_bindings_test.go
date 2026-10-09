//go:build integration

package acceptance

import (
	"context"

	"github.com/bahadrdsr/aspm/internal/app"
)

func init() {
	Production.OpenApplication = func(ctx context.Context, config ApplicationConfig) (Application, error) {
		var oidc *app.OIDCConfig
		var archive *app.StorageConfig
		if config.OIDC != nil {
			oidc = &app.OIDCConfig{
				Issuer: config.OIDC.Issuer, ClientID: config.OIDC.ClientID,
				ClientSecret: config.OIDC.ClientSecret, WorkspaceID: config.OIDC.WorkspaceID, Client: config.OIDC.Client,
			}
		}
		if config.ArchiveStorage.Endpoint != "" {
			archive = &app.StorageConfig{
				Endpoint: config.ArchiveStorage.Endpoint, Bucket: config.ArchiveStorage.Bucket,
				Prefix: config.ArchiveStorage.Prefix, Region: config.ArchiveStorage.Region,
				AccessKey: config.ArchiveStorage.AccessKey, SecretKey: config.ArchiveStorage.SecretKey,
			}
		}
		instance, err := app.Open(ctx, app.Config{
			DatabaseURL: config.DatabaseURL, Schema: config.Schema, ApplicationName: config.ApplicationName,
			MaxConnections: config.MaxConnections, BootstrapToken: config.BootstrapToken,
			IntegrationEncryptionKey: config.IntegrationEncryptionKey, PublicOrigin: config.PublicOrigin,
			WebhookOrigins:  config.WebhookOrigins,
			AssessmentScope: config.AssessmentScope,
			Now:             config.Now, LogOutput: config.LogOutput, SessionTTL: config.SessionTTL,
			MaxUploadBytes: config.MaxUploadBytes, ManualProcessing: config.ManualProcessing,
			OIDC: oidc, QueryTracer: config.QueryTracer,
			ArchiveStorage: archive,
			Storage: app.StorageConfig{
				Endpoint: config.Storage.Endpoint, Bucket: config.Storage.Bucket, Prefix: config.Storage.Prefix,
				Region: config.Storage.Region, AccessKey: config.Storage.AccessKey, SecretKey: config.Storage.SecretKey,
			},
		})
		if err != nil {
			return Application{}, err
		}
		return Application{
			Handler: instance.Handler, Close: instance.Close,
			ProcessImports: instance.ProcessImports, ProcessReports: instance.ProcessReports,
		}, nil
	}
	Production.OpenRetentionWorker = func(ctx context.Context, config RetentionWorkerConfig) (RetentionWorker, error) {
		worker, err := app.OpenRetentionWorker(ctx, app.RetentionWorkerConfig{
			Database: app.DatabaseConfig{
				DatabaseURL: config.DatabaseURL, Schema: config.Schema, ApplicationName: config.ApplicationName,
				MaxConnections: config.MaxConnections, Now: config.Now, LogOutput: config.LogOutput,
			},
			RawStorage: app.StorageConfig{
				Endpoint: config.RawStorage.Endpoint, Bucket: config.RawStorage.Bucket, Prefix: config.RawStorage.Prefix,
				Region: config.RawStorage.Region, AccessKey: config.RawStorage.AccessKey, SecretKey: config.RawStorage.SecretKey,
			},
			ArchiveStorage: app.StorageConfig{
				Endpoint: config.ArchiveStorage.Endpoint, Bucket: config.ArchiveStorage.Bucket,
				Prefix: config.ArchiveStorage.Prefix, Region: config.ArchiveStorage.Region,
				AccessKey: config.ArchiveStorage.AccessKey, SecretKey: config.ArchiveStorage.SecretKey,
			},
			MaxEvidenceBytes: 32 << 20, Lease: config.Lease,
		})
		if err != nil {
			return RetentionWorker{}, err
		}
		return RetentionWorker{ProcessNext: worker.ProcessNext, Close: worker.Close}, nil
	}
	Production.OpenDeliveryWorker = func(ctx context.Context, config DeliveryWorkerConfig) (DeliveryWorker, error) {
		worker, err := app.OpenDeliveryWorker(ctx, app.DeliveryWorkerConfig{
			Database: app.DatabaseConfig{
				DatabaseURL: config.DatabaseURL, Schema: config.Schema, ApplicationName: config.ApplicationName,
				MaxConnections: config.MaxConnections, LogOutput: config.LogOutput, QueryTracer: config.QueryTracer,
			},
			EncryptionKey: config.EncryptionKey, WorkerID: config.WorkerID, LeaseDuration: config.LeaseDuration,
			PublicOrigin: config.PublicOrigin, WebhookOrigins: config.WebhookOrigins,
			SlackEndpoint: config.SlackEndpoint, Client: config.Client,
		})
		if err != nil {
			return DeliveryWorker{}, err
		}
		return DeliveryWorker{ProcessNext: worker.ProcessNext, Ping: worker.Ping, Close: worker.Close}, nil
	}
	Production.OpenVerificationWorker = func(ctx context.Context,
		config VerificationWorkerConfig) (VerificationWorker, error) {
		worker, err := app.OpenVerificationWorker(ctx, app.VerificationWorkerConfig{
			Database: app.DatabaseConfig{
				DatabaseURL: config.DatabaseURL, Schema: config.Schema, ApplicationName: config.ApplicationName,
				MaxConnections: config.MaxConnections, Now: config.Now, LogOutput: config.LogOutput,
				QueryTracer: config.QueryTracer,
			},
			WorkerID: config.WorkerID, LeaseDuration: config.LeaseDuration,
			AuthorizationInterval: config.AuthorizationInterval, MaxFixtureBytes: config.MaxFixtureBytes,
		})
		if err != nil {
			return VerificationWorker{}, err
		}
		return VerificationWorker{ProcessNext: worker.ProcessNext, Ping: worker.Ping, Close: worker.Close}, nil
	}
}
