//go:build integration

package ai_assessments

import (
	"context"

	"github.com/bahadrdsr/aspm/internal/app"
)

func init() {
	Production.OpenCore = func(ctx context.Context, config app.Config, scope string) (*app.Application, error) {
		config.AssessmentScope = scope
		return app.Open(ctx, config)
	}
	Production.OpenWorker = func(ctx context.Context, config workerConfig) (assessmentWorker, error) {
		instance, err := app.OpenAssessmentWorker(ctx, app.AssessmentWorkerConfig{
			Database: config.Database, EncryptionKey: config.EncryptionKey,
			WorkerID: config.WorkerID, Scope: config.Scope, Client: config.Client,
			LeaseDuration: config.LeaseDuration, AuthorizationInterval: config.AuthorizationInterval,
			RequestTimeout: config.RequestTimeout, RequestWindow: config.RequestWindow,
			MaxConcurrent: config.MaxConcurrent, RequestsPerWindow: config.RequestsPerWindow,
			MaxInputBytes: config.MaxInputBytes, MaxOutputTokens: config.MaxOutputTokens,
			MaxResponseBytes: config.MaxResponseBytes,
		})
		if instance == nil {
			return nil, err
		}
		return instance, err
	}
}
