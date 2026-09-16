//go:build integration

package acceptance

import (
	"context"

	"github.com/bahadrdsr/aspm/internal/app"
)

func init() {
	Production.OpenApplication = func(ctx context.Context, config ApplicationConfig) (Application, error) {
		var oidc *app.OIDCConfig
		if config.OIDC != nil {
			oidc = &app.OIDCConfig{
				Issuer: config.OIDC.Issuer, ClientID: config.OIDC.ClientID,
				ClientSecret: config.OIDC.ClientSecret, WorkspaceID: config.OIDC.WorkspaceID, Client: config.OIDC.Client,
			}
		}
		instance, err := app.Open(ctx, app.Config{
			DatabaseURL: config.DatabaseURL, Schema: config.Schema, ApplicationName: config.ApplicationName,
			MaxConnections: config.MaxConnections, BootstrapToken: config.BootstrapToken,
			Now: config.Now, LogOutput: config.LogOutput, SessionTTL: config.SessionTTL,
			MaxUploadBytes: config.MaxUploadBytes, ManualProcessing: config.ManualProcessing,
			OIDC: oidc, QueryTracer: config.QueryTracer,
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
}
