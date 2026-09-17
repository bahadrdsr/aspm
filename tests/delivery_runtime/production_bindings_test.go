//go:build integration

package delivery_runtime

import (
	"context"

	"github.com/bahadrdsr/aspm/internal/jobs"
	"github.com/bahadrdsr/aspm/internal/service"
)

func init() {
	Production.Environment = func(role string) (roleConfig, error) {
		config, err := service.Environment(role)
		return roleConfig{
			Database: databaseConfig{
				URL: config.Jobs.DatabaseURL, Schema: config.Jobs.Schema, ApplicationName: config.Jobs.ApplicationName,
				MaxConnections: config.Jobs.MaxConnections,
			},
			Listen: config.Listen, WorkerID: config.WorkerID, EncryptionKey: config.IntegrationEncryptionKey,
			LeaseDuration: config.DeliveryLeaseDuration, SlackEndpoint: config.SlackEndpoint,
			Client: config.DeliveryClient, CAFile: config.DeliveryCAFile,
			Evidence: config.Evidence, BootstrapToken: config.BootstrapToken,
		}, err
	}
	Production.Run = func(ctx context.Context, config roleConfig) error {
		return service.Run(ctx, "delivery", service.Config{
			Listen: config.Listen, WorkerID: config.WorkerID, IntegrationEncryptionKey: config.EncryptionKey,
			DeliveryLeaseDuration: config.LeaseDuration, SlackEndpoint: config.SlackEndpoint,
			DeliveryClient: config.Client, DeliveryCAFile: config.CAFile,
			Evidence: config.Evidence, BootstrapToken: config.BootstrapToken,
			Jobs: jobs.Config{
				DatabaseURL: config.Database.URL, Schema: config.Database.Schema,
				ApplicationName: config.Database.ApplicationName, MaxConnections: config.Database.MaxConnections,
			},
		})
	}
}
