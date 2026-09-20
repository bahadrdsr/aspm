//go:build integration

package ai_configuration

import (
	"context"

	"github.com/bahadrdsr/aspm/internal/app"
)

func init() {
	Production.OpenResolver = func(ctx context.Context, database app.DatabaseConfig, key []byte) (resolver, error) {
		instance, err := app.OpenAIConfigurationResolver(ctx, app.AIConfigurationResolverConfig{
			Database: database, EncryptionKey: key,
		})
		if instance == nil {
			return nil, err
		}
		return resolverBinding{instance}, err
	}
}

type resolverBinding struct{ value *app.AIConfigurationResolver }

func (r resolverBinding) Resolve(ctx context.Context, request lookupRequest) (resolved, error) {
	value, err := r.value.Resolve(ctx, app.AIConfigurationRequest{
		WorkspaceID: request.WorkspaceID, ActorID: request.ActorID, ProfileID: request.ProfileID,
		GrantID: request.GrantID, Task: request.Task, DataClass: request.DataClass,
	})
	return resolved{Profile: value.Profile, Policy: value.Policy}, err
}
func (r resolverBinding) Close() error { return r.value.Close() }
