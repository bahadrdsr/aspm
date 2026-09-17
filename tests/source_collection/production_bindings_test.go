//go:build integration

package source_collection

import (
	"context"

	"github.com/bahadrdsr/aspm/internal/app"
)

func init() {
	Production.OpenCore = func(ctx context.Context, config coreConfig) (core, error) {
		application, err := app.Open(ctx, app.Config{
			DatabaseURL: config.Database.DatabaseURL, Schema: config.Database.Schema,
			ApplicationName: config.Database.ApplicationName, MaxConnections: config.Database.MaxConnections,
			Storage: config.Storage, CollectionStorage: config.CollectionStorage,
			IntegrationEncryptionKey: config.Key, BootstrapToken: config.Bootstrap, LogOutput: config.Log,
			PublicOrigin: "https://source-collection.synthetic.invalid", ManualProcessing: true,
		})
		if application == nil {
			return core{}, err
		}
		return core{Handler: application.Handler, Close: application.Close}, err
	}
	Production.OpenWorker = func(ctx context.Context, config workerConfig) (collectionWorker, error) {
		database := config.Database
		database.QueryTracer = config.Tracer
		worker, err := app.OpenCollectionWorker(ctx, app.CollectionWorkerConfig{
			Database: database, Storage: config.Storage, EncryptionKey: config.Key,
			WorkerID: config.WorkerID, LeaseDuration: config.LeaseDuration,
			GitHubEndpoint: config.Endpoint, Client: config.Client, Limits: config.Limits,
		})
		if worker == nil {
			return nil, err
		}
		return worker, err
	}
}
